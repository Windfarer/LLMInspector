package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/windfarer/llminspector/metrics"
)

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

func (p *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

				var payload struct {
					Model   string `json:"model"`
					Choices []struct {
						Message struct {
							Content          string `json:"content"`
							ReasoningContent string `json:"reasoning_content"`
						} `json:"message"`
						Text string `json:"text"`
					} `json:"choices"`
					Usage struct {
						CompletionTokens int `json:"completion_tokens"`
						PromptTokens     int `json:"prompt_tokens"`
						TotalTokens      int `json:"total_tokens"`
						PromptTokensDetails struct {
							CachedTokens int `json:"cached_tokens"`
						} `json:"prompt_tokens_details"`
					} `json:"usage"`
				}

				if err := json.Unmarshal(bodyBytes, &payload); err == nil {
					content := ""
					reasoning := ""
					if len(payload.Choices) > 0 {
						// completion (Legacy)
						if payload.Choices[0].Text != "" {
							content = payload.Choices[0].Text
						} else {
							// chat completion
							content = payload.Choices[0].Message.Content
							reasoning = payload.Choices[0].Message.ReasoningContent
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
						ExactTokens:    payload.Usage.CompletionTokens,
						PromptTokens:   payload.Usage.PromptTokens,
						TotalTokens:    payload.Usage.TotalTokens,
						CachedTokens:   payload.Usage.PromptTokensDetails.CachedTokens,
						Timestamp:      time.Now(),
					})
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

		go p.parseStream(pr, reqID, userID)

		resp.Body = &teeReadCloser{
			Reader:  tee,
			Closer:  resp.Body,
			pw:      pw,
			manager: p.manager,
			reqID:   reqID,
		}

		return nil
	}

	proxy.ServeHTTP(w, r)
}

func (p *ProxyServer) parseStream(reader io.Reader, reqID string, userID string) {
	scanner := bufio.NewScanner(reader)
	// OpenAI SSE chunks can be large if reasoning is large, increase buffer
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var model string

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
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				TotalTokens      int `json:"total_tokens"`
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
