package store

import (
	"database/sql"
	"log"

	_ "github.com/mattn/go-sqlite3"
	"github.com/windfarer/llminspector/models"
)

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db}
	if err := store.initDB(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *Store) initDB() error {
	query := `
	CREATE TABLE IF NOT EXISTS requests (
		id TEXT PRIMARY KEY,
		user_id TEXT,
		model TEXT,
		start_time DATETIME,
		ttft_ms INTEGER,
		e2e_ms INTEGER,
		prompt_tokens INTEGER,
		content_tokens INTEGER,
		reasoning_tokens INTEGER,
		total_tokens INTEGER,
		cached_tokens INTEGER,
		input_content TEXT,
		output_content TEXT,
		reasoning_content TEXT,
		is_streaming BOOLEAN,
		is_completed BOOLEAN
	);`
	_, err := s.db.Exec(query)
	return err
}

func (s *Store) SaveRequest(req *models.RequestStats) error {
	query := `
	INSERT INTO requests (id, user_id, model, start_time, ttft_ms, e2e_ms, prompt_tokens, content_tokens, reasoning_tokens, total_tokens, cached_tokens, input_content, output_content, reasoning_content, is_streaming, is_completed)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		e2e_ms=excluded.e2e_ms,
		prompt_tokens=excluded.prompt_tokens,
		content_tokens=excluded.content_tokens,
		reasoning_tokens=excluded.reasoning_tokens,
		total_tokens=excluded.total_tokens,
		cached_tokens=excluded.cached_tokens,
		output_content=excluded.output_content,
		reasoning_content=excluded.reasoning_content,
		is_completed=excluded.is_completed;
	`
	_, err := s.db.Exec(query,
		req.ID, req.UserID, req.Model, req.StartTime, req.TTFTMs, req.E2EMs,
		req.PromptTokens, req.ContentTokens, req.ReasoningTokens, req.TotalTokens, req.CachedTokens, req.InputContent, req.OutputContent, req.ReasoningContent, req.IsStreaming, req.IsCompleted,
	)
	if err != nil {
		log.Printf("Error saving request to DB: %v", err)
	}
	return err
}

func (s *Store) GetRecentRequests(limit int) ([]*models.RequestStats, error) {
	query := `
	SELECT id, user_id, model, start_time, ttft_ms, e2e_ms, prompt_tokens, content_tokens, reasoning_tokens, total_tokens, cached_tokens, input_content, output_content, reasoning_content, is_streaming, is_completed
	FROM requests
	ORDER BY start_time DESC
	LIMIT ?
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []*models.RequestStats
	for rows.Next() {
		var req models.RequestStats
		err := rows.Scan(
			&req.ID, &req.UserID, &req.Model, &req.StartTime, &req.TTFTMs, &req.E2EMs,
			&req.PromptTokens, &req.ContentTokens, &req.ReasoningTokens, &req.TotalTokens, &req.CachedTokens, &req.InputContent, &req.OutputContent, &req.ReasoningContent, &req.IsStreaming, &req.IsCompleted,
		)
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, &req)
	}
	return reqs, nil
}
