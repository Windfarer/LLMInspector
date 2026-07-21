package models

import "time"

type RequestStats struct {
	ID               string    `json:"id"`
	UserID           string    `json:"user_id"`
	Model            string    `json:"model"`
	StartTime        time.Time `json:"start_time"`
	TTFTMs           int64     `json:"ttft_ms"`
	E2EMs            int64     `json:"e2e_ms"`
	PromptTokens     int       `json:"prompt_tokens"`
	ContentTokens    int       `json:"content_tokens"`
	ReasoningTokens  int       `json:"reasoning_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CachedTokens     int       `json:"cached_tokens"`
	InputContent     string    `json:"input_content"`
	OutputContent    string    `json:"output_content"`
	ReasoningContent string    `json:"reasoning_content"`
	ToolCalls        string    `json:"tool_calls"`
	IsThinking       bool      `json:"is_thinking"`
	IsStreaming      bool      `json:"is_streaming"`
	IsCompleted      bool      `json:"is_completed"`
}
