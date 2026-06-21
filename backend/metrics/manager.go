package metrics

import (
	"sync"
	"time"

	"github.com/windfarer/llminspector/models"
	"github.com/windfarer/llminspector/store"
)

type EventType string

const (
	EventStart     EventType = "start"
	EventChunk     EventType = "chunk"
	EventNonStream EventType = "nonstream"
	EventEnd       EventType = "end"
)

type ProxyEvent struct {
	Type            EventType
	ReqID           string
	UserID          string
	Model           string
	ChunkContent    string
	ChunkReasoning  string
	InputContent    string
	ExactTokens     int // For non-stream
	PromptTokens    int
	TotalTokens     int
	CachedTokens    int
	Timestamp       time.Time
}

type Manager struct {
	events      chan ProxyEvent
	activeReqs  map[string]*models.RequestStats
	mu          sync.RWMutex
	store       *store.Store
	counter     TokenCounter
	updateHooks []func(req *models.RequestStats)
}

func NewManager(db *store.Store, useTiktoken bool) *Manager {
	var counter TokenCounter
	if useTiktoken {
		c, _ := NewTiktokenCounter("gpt-3.5-turbo")
		counter = c
	} else {
		counter = &LengthEstimator{}
	}

	m := &Manager{
		events:     make(chan ProxyEvent, 10000), // Async buffer to avoid blocking
		activeReqs: make(map[string]*models.RequestStats),
		store:      db,
		counter:    counter,
	}

	go m.processEvents()
	return m
}

func (m *Manager) AddHook(hook func(req *models.RequestStats)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateHooks = append(m.updateHooks, hook)
}

func (m *Manager) SubmitEvent(e ProxyEvent) {
	select {
	case m.events <- e:
	default:
		// Channel full, drop event to not block proxy
	}
}

func (m *Manager) processEvents() {
	for e := range m.events {
		m.mu.Lock()
		req, exists := m.activeReqs[e.ReqID]

		switch e.Type {
		case EventStart:
			if !exists {
				req = &models.RequestStats{
					ID:           e.ReqID,
					UserID:       e.UserID,
					Model:        e.Model,
					StartTime:    e.Timestamp,
					InputContent: e.InputContent,
					IsStreaming:  true,
				}
				m.activeReqs[e.ReqID] = req
				m.triggerHooks(req)
			}
		case EventChunk:
			if exists {
				if req.TTFTMs == 0 {
					req.TTFTMs = e.Timestamp.Sub(req.StartTime).Milliseconds()
				}
				if e.ChunkReasoning != "" {
					req.ReasoningTokens += m.counter.Count(e.ChunkReasoning)
					req.ReasoningContent += e.ChunkReasoning
					req.IsThinking = true
				}
				if e.ChunkContent != "" {
					req.ContentTokens += m.counter.Count(e.ChunkContent)
					req.OutputContent += e.ChunkContent
					req.IsThinking = false
				}
				if e.PromptTokens > 0 {
					req.PromptTokens = e.PromptTokens
				}
				if e.TotalTokens > 0 {
					req.TotalTokens = e.TotalTokens
				}
				if e.CachedTokens > 0 {
					req.CachedTokens = e.CachedTokens
				}
				m.triggerHooks(req)
			}
		case EventNonStream:
			if exists {
				req.IsStreaming = false
				if req.TTFTMs == 0 {
					req.TTFTMs = e.Timestamp.Sub(req.StartTime).Milliseconds()
				}
				req.OutputContent = e.ChunkContent
				req.ContentTokens = e.ExactTokens
				req.PromptTokens = e.PromptTokens
				req.TotalTokens = e.TotalTokens
				req.CachedTokens = e.CachedTokens
				req.IsThinking = false
				m.triggerHooks(req)
			}
		case EventEnd:
			if exists {
				req.E2EMs = e.Timestamp.Sub(req.StartTime).Milliseconds()
				req.IsCompleted = true
				if m.store != nil {
					m.store.SaveRequest(req)
				}
				m.triggerHooks(req)
				delete(m.activeReqs, e.ReqID)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) triggerHooks(req *models.RequestStats) {
	for _, hook := range m.updateHooks {
		hook(req)
	}
}

func (m *Manager) GetActiveRequests() []*models.RequestStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	reqs := make([]*models.RequestStats, 0, len(m.activeReqs))
	for _, r := range m.activeReqs {
		// make a copy to avoid race conditions
		cpy := *r
		reqs = append(reqs, &cpy)
	}
	return reqs
}

func (m *Manager) GetRecentRequests(limit int) []*models.RequestStats {
	if m.store == nil {
		return nil
	}
	reqs, err := m.store.GetRecentRequests(limit)
	if err != nil {
		return nil
	}
	return reqs
}
