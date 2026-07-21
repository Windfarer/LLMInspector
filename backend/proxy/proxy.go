package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/windfarer/llminspector/metrics"
)

// normalizedToolCall is the unified tool call format used for storage.
type normalizedToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function normalizedToolCallFunc `json:"function"`
}

type normalizedToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// serializeToolCalls converts a slice of normalizedToolCall to a JSON string.
// Returns empty string if the slice is empty.
func serializeToolCalls(calls []normalizedToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	b, err := json.Marshal(calls)
	if err != nil {
		return ""
	}
	return string(b)
}

type ProxyServer struct {
	targetURL  *url.URL
	userHeader string
	manager    *metrics.Manager
}

func NewProxyServer(target string, userHeader string, manager *metrics.Manager) (*ProxyServer, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	return &ProxyServer{
		targetURL:  u,
		userHeader: userHeader,
		manager:    manager,
	}, nil
}

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (p *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	userID := r.Header.Get(p.userHeader)
	if userID == "" {
		userID = "anonymous"
	}

	// Extract request body for input viewing and model name before proxying
	var inputContent string
	var modelName string
	if r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			// Extract model
			var payload struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(bodyBytes, &payload); err == nil {
				modelName = payload.Model
			}

			var prettyJSON bytes.Buffer
			if err := json.Indent(&prettyJSON, bodyBytes, "", "  "); err == nil {
				inputContent = prettyJSON.String()
			} else {
				inputContent = string(bodyBytes)
			}
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // Reconstruct body for proxy
		}
	}

	isAnthropicAPI := strings.HasSuffix(r.URL.Path, "/messages")

	sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	proxy := httputil.NewSingleHostReverseProxy(p.targetURL)

	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = p.targetURL.Host
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		contentType := resp.Header.Get("Content-Type")
		isStream := strings.Contains(contentType, "text/event-stream")
		isJSON := strings.Contains(contentType, "application/json")

		if !isStream && !isJSON {
			return nil
		}

		reqID := fmt.Sprintf("req-%d", time.Now().UnixNano())

		p.manager.SubmitEvent(metrics.ProxyEvent{
			Type:         metrics.EventStart,
			ReqID:        reqID,
			UserID:       userID,
			Model:        modelName,
			InputContent: inputContent,
			Timestamp:    time.Now(),
		})

		if isJSON {
			bodyBytes, err := io.ReadAll(resp.Body)
			if err == nil {
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

				if isAnthropicAPI {
					var payload struct {
						Model   string `json:"model"`
						Content []struct {
							Type     string          `json:"type"`
							Text     string          `json:"text"`
							Thinking string          `json:"thinking"`
							ID       string          `json:"id"`
							Name     string          `json:"name"`
							Input    json.RawMessage `json:"input"`
						} `json:"content"`
						Usage struct {
							InputTokens              int `json:"input_tokens"`
							OutputTokens             int `json:"output_tokens"`
							CacheReadInputTokens     int `json:"cache_read_input_tokens"`
							CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						} `json:"usage"`
					}

					if err := json.Unmarshal(bodyBytes, &payload); err == nil {
						content := ""
						reasoning := ""
						var toolCalls []normalizedToolCall
						for _, block := range payload.Content {
							switch block.Type {
							case "text":
								content += block.Text
							case "thinking":
								reasoning += block.Thinking
							case "tool_use":
								args := ""
								if len(block.Input) > 0 {
									args = string(block.Input)
								}
								toolCalls = append(toolCalls, normalizedToolCall{
									ID:   block.ID,
									Type: "function",
									Function: normalizedToolCallFunc{
										Name:      block.Name,
										Arguments: args,
									},
								})
							}
						}

						m := modelName
						if m == "" {
							m = payload.Model
						}

						p.manager.SubmitEvent(metrics.ProxyEvent{
							Type:           metrics.EventNonStream,
							ReqID:          reqID,
							UserID:         userID,
							Model:          m,
							ChunkContent:   content,
							ChunkReasoning: reasoning,
							ToolCallsJSON:  serializeToolCalls(toolCalls),
							ExactTokens:    payload.Usage.OutputTokens,
							PromptTokens:   payload.Usage.InputTokens,
							TotalTokens:    payload.Usage.InputTokens + payload.Usage.OutputTokens,
							CachedTokens:   payload.Usage.CacheReadInputTokens,
							Timestamp:      time.Now(),
						})
					}
				} else {
					var payload struct {
						Model   string `json:"model"`
						Choices []struct {
							Message struct {
								Content          string `json:"content"`
								ReasoningContent string `json:"reasoning_content"`
								ToolCalls        []struct {
									ID       string `json:"id"`
									Type     string `json:"type"`
									Function struct {
										Name      string `json:"name"`
										Arguments string `json:"arguments"`
									} `json:"function"`
								} `json:"tool_calls"`
							} `json:"message"`
							Text string `json:"text"`
						} `json:"choices"`
						Usage struct {
							CompletionTokens    int `json:"completion_tokens"`
							PromptTokens        int `json:"prompt_tokens"`
							TotalTokens         int `json:"total_tokens"`
							PromptTokensDetails struct {
								CachedTokens int `json:"cached_tokens"`
							} `json:"prompt_tokens_details"`
						} `json:"usage"`
					}

					if err := json.Unmarshal(bodyBytes, &payload); err == nil {
						content := ""
						reasoning := ""
						var toolCallsJSON string
						if len(payload.Choices) > 0 {
							// completion (Legacy)
							if payload.Choices[0].Text != "" {
								content = payload.Choices[0].Text
							} else {
								// chat completion
								content = payload.Choices[0].Message.Content
								reasoning = payload.Choices[0].Message.ReasoningContent
								if len(payload.Choices[0].Message.ToolCalls) > 0 {
									calls := make([]normalizedToolCall, 0, len(payload.Choices[0].Message.ToolCalls))
									for _, tc := range payload.Choices[0].Message.ToolCalls {
										calls = append(calls, normalizedToolCall{
											ID:   tc.ID,
											Type: tc.Type,
											Function: normalizedToolCallFunc{
												Name:      tc.Function.Name,
												Arguments: tc.Function.Arguments,
											},
										})
									}
									toolCallsJSON = serializeToolCalls(calls)
								}
							}
						}

						m := modelName
						if m == "" {
							m = payload.Model
						}

						// Send the unified NonStream event
						p.manager.SubmitEvent(metrics.ProxyEvent{
							Type:           metrics.EventNonStream,
							ReqID:          reqID,
							UserID:         userID,
							Model:          m,
							ChunkContent:   content,
							ChunkReasoning: reasoning,
							ToolCallsJSON:  toolCallsJSON,
							ExactTokens:    payload.Usage.CompletionTokens,
							PromptTokens:   payload.Usage.PromptTokens,
							TotalTokens:    payload.Usage.TotalTokens,
							CachedTokens:   payload.Usage.PromptTokensDetails.CachedTokens,
							Timestamp:      time.Now(),
						})
					}
				}
			}

			// End request immediately for non-stream
			p.manager.SubmitEvent(metrics.ProxyEvent{
				Type:      metrics.EventEnd,
				ReqID:     reqID,
				Timestamp: time.Now(),
			})
			return nil
		}

		// Streaming logic
		pr, pw := io.Pipe()
		tee := io.TeeReader(resp.Body, pw)

		if isAnthropicAPI {
			go p.parseAnthropicStream(pr, reqID, userID)
		} else {
			go p.parseStream(pr, reqID, userID)
		}

		resp.Body = &teeReadCloser{
			Reader:  tee,
			Closer:  resp.Body,
			pw:      pw,
			manager: p.manager,
			reqID:   reqID,
		}

		return nil
	}

	proxy.ServeHTTP(sr, r)

	log.Printf("[PROXY] %s %s %d user=%s model=%s %.0fms",
		r.Method, r.URL.Path, sr.status, userID, modelName, float64(time.Since(start).Milliseconds()))
}

func (p *ProxyServer) parseStream(reader io.Reader, reqID string, userID string) {
	scanner := bufio.NewScanner(reader)
	// OpenAI SSE chunks can be large if reasoning is large, increase buffer
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var model string

	// Accumulate tool call arguments by index
	type toolCallAcc struct {
		id        string
		tcType    string
		name      string
		arguments strings.Builder
	}
	toolCallAccMap := map[int]*toolCallAcc{}

	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}

		data := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}

		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				PromptTokens        int `json:"prompt_tokens"`
				TotalTokens         int `json:"total_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}

		if err := json.Unmarshal(data, &chunk); err == nil {
			if model == "" && chunk.Model != "" {
				model = chunk.Model
			}

			content := ""
			reasoning := ""
			if len(chunk.Choices) > 0 {
				content = chunk.Choices[0].Delta.Content
				reasoning = chunk.Choices[0].Delta.ReasoningContent

				// Accumulate tool_calls by index
				for _, tc := range chunk.Choices[0].Delta.ToolCalls {
					if _, ok := toolCallAccMap[tc.Index]; !ok {
						toolCallAccMap[tc.Index] = &toolCallAcc{}
					}
					acc := toolCallAccMap[tc.Index]
					if tc.ID != "" {
						acc.id = tc.ID
					}
					if tc.Type != "" {
						acc.tcType = tc.Type
					}
					if tc.Function.Name != "" {
						acc.name = tc.Function.Name
					}
					acc.arguments.WriteString(tc.Function.Arguments)
				}
			}

			p.manager.SubmitEvent(metrics.ProxyEvent{
				Type:           metrics.EventChunk,
				ReqID:          reqID,
				UserID:         userID,
				Model:          model,
				ChunkContent:   content,
				ChunkReasoning: reasoning,
				PromptTokens:   chunk.Usage.PromptTokens,
				TotalTokens:    chunk.Usage.TotalTokens,
				CachedTokens:   chunk.Usage.PromptTokensDetails.CachedTokens,
				Timestamp:      time.Now(),
			})
		}
	}

	// After stream ends, emit accumulated tool calls if any
	if len(toolCallAccMap) > 0 {
		calls := make([]normalizedToolCall, len(toolCallAccMap))
		for idx, acc := range toolCallAccMap {
			tcType := acc.tcType
			if tcType == "" {
				tcType = "function"
			}
			calls[idx] = normalizedToolCall{
				ID:   acc.id,
				Type: tcType,
				Function: normalizedToolCallFunc{
					Name:      acc.name,
					Arguments: acc.arguments.String(),
				},
			}
		}
		p.manager.SubmitEvent(metrics.ProxyEvent{
			Type:          metrics.EventChunk,
			ReqID:         reqID,
			UserID:        userID,
			Model:         model,
			ToolCallsJSON: serializeToolCalls(calls),
			Timestamp:     time.Now(),
		})
	}
}

func (p *ProxyServer) parseAnthropicStream(reader io.Reader, reqID string, userID string) {
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var model string
	var currentEventType string
	var inputTokens int
	var cacheReadTokens int
	var currentBlockIndex int

	// Accumulate tool_use blocks by content block index
	type toolUseAcc struct {
		id        string
		name      string
		arguments strings.Builder
	}
	toolUseAccMap := map[int]*toolUseAcc{}

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := []byte(strings.TrimPrefix(line, "data: "))

		switch currentEventType {
		case "message_start":
			var event struct {
				Message struct {
					Model string `json:"model"`
					Usage struct {
						InputTokens          int `json:"input_tokens"`
						CacheReadInputTokens int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal(data, &event); err == nil {
				if model == "" && event.Message.Model != "" {
					model = event.Message.Model
				}
				inputTokens = event.Message.Usage.InputTokens
				cacheReadTokens = event.Message.Usage.CacheReadInputTokens
				p.manager.SubmitEvent(metrics.ProxyEvent{
					Type:         metrics.EventChunk,
					ReqID:        reqID,
					UserID:       userID,
					Model:        model,
					PromptTokens: inputTokens,
					CachedTokens: cacheReadTokens,
					Timestamp:    time.Now(),
				})
			}

		case "content_block_start":
			var event struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal(data, &event); err == nil {
				currentBlockIndex = event.Index
				if event.ContentBlock.Type == "tool_use" {
					toolUseAccMap[event.Index] = &toolUseAcc{
						id:   event.ContentBlock.ID,
						name: event.ContentBlock.Name,
					}
				}
			}

		case "content_block_delta":
			var event struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal(data, &event); err == nil {
				_ = currentBlockIndex
				content := ""
				reasoning := ""
				switch event.Delta.Type {
				case "text_delta":
					content = event.Delta.Text
				case "thinking_delta":
					reasoning = event.Delta.Thinking
				case "input_json_delta":
					if acc, ok := toolUseAccMap[event.Index]; ok {
						acc.arguments.WriteString(event.Delta.PartialJSON)
					}
				}
				if content != "" || reasoning != "" {
					p.manager.SubmitEvent(metrics.ProxyEvent{
						Type:           metrics.EventChunk,
						ReqID:          reqID,
						UserID:         userID,
						Model:          model,
						ChunkContent:   content,
						ChunkReasoning: reasoning,
						Timestamp:      time.Now(),
					})
				}
			}

		case "message_delta":
			var event struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(data, &event); err == nil {
				p.manager.SubmitEvent(metrics.ProxyEvent{
					Type:         metrics.EventChunk,
					ReqID:        reqID,
					UserID:       userID,
					Model:        model,
					TotalTokens:  inputTokens + event.Usage.OutputTokens,
					PromptTokens: inputTokens,
					CachedTokens: cacheReadTokens,
					Timestamp:    time.Now(),
				})
			}
		}
	}

	// After stream ends, emit accumulated tool_use calls if any
	if len(toolUseAccMap) > 0 {
		calls := make([]normalizedToolCall, 0, len(toolUseAccMap))
		for _, acc := range toolUseAccMap {
			calls = append(calls, normalizedToolCall{
				ID:   acc.id,
				Type: "function",
				Function: normalizedToolCallFunc{
					Name:      acc.name,
					Arguments: acc.arguments.String(),
				},
			})
		}
		p.manager.SubmitEvent(metrics.ProxyEvent{
			Type:          metrics.EventChunk,
			ReqID:         reqID,
			UserID:        userID,
			Model:         model,
			ToolCallsJSON: serializeToolCalls(calls),
			Timestamp:     time.Now(),
		})
	}
}

type teeReadCloser struct {
	io.Reader
	io.Closer
	pw      *io.PipeWriter
	manager *metrics.Manager
	reqID   string
}

func (t *teeReadCloser) Read(p []byte) (n int, err error) {
	return t.Reader.Read(p)
}

func (t *teeReadCloser) Close() error {
	err := t.Closer.Close()
	t.pw.Close() // Close the pipe so the parser goroutine exits
	t.manager.SubmitEvent(metrics.ProxyEvent{
		Type:      metrics.EventEnd,
		ReqID:     t.reqID,
		Timestamp: time.Now(),
	})
	return err
}
