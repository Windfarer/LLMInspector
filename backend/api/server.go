package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/windfarer/llminspector/metrics"
	"github.com/windfarer/llminspector/models"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Server struct {
	manager *metrics.Manager
	clients map[*websocket.Conn]bool
	mu      sync.Mutex
}

func NewServer(manager *metrics.Manager) *Server {
	s := &Server{
		manager: manager,
		clients: make(map[*websocket.Conn]bool),
	}

	// Register a hook in the manager to broadcast updates
	manager.AddHook(s.broadcastUpdate)

	return s
}

func (s *Server) HandleGetRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		return
	}
	reqs := s.manager.GetRecentRequests(1000)
	if reqs == nil {
		reqs = []*models.RequestStats{}
	}
	data, err := json.Marshal(reqs)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	s.mu.Lock()
	s.clients[conn] = true

	// Only push in-flight active requests; historical data is fetched via GET /api/requests
	for _, req := range s.manager.GetActiveRequests() {
		s.sendToConn(conn, req)
	}
	s.mu.Unlock()

	// Read loop to detect disconnects
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.clients, conn)
			s.mu.Unlock()
			conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

func (s *Server) broadcastUpdate(req *models.RequestStats) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for conn := range s.clients {
		s.sendToConn(conn, req)
	}
}

func (s *Server) sendToConn(conn *websocket.Conn, req *models.RequestStats) {
	data, err := json.Marshal(req)
	if err == nil {
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}
}
