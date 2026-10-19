package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

// No provider adapter is mocked: failures enter through HTTP responses and
// traverse serialization, parsing, conversation recovery and service fallback.
// Native success is covered by the provider client server tests. OpenAI native
// in-turn failure is terminal (TestWebsocketCompactionFailureLeavesSteeringQueued),
// so it must not be treated as a native-to-summary fallback here.
func TestCompactionServerSummaryMatrix(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible} {
		for _, phase := range []string{"preturn", "inturn", "preturn_native_failure", "inturn_native_failure"} {
			if strings.Contains(phase, "native") && (provider == models.ProviderOpenAICompatible || (provider == models.ProviderOpenAI && strings.HasPrefix(phase, "inturn"))) {
				continue
			}
			for _, outcome := range []string{"success", "summary_overflow_once", "summary_overflow_exhausted", "authentication", "empty_summary", "continuation_overflow", "transient_once", "transient_exhausted"} {
				// Retry exhaustion is independent of native fallback; exercise it in both summary entry points.
				if outcome == "transient_exhausted" && strings.Contains(phase, "native") {
					continue
				}
				t.Run(string(provider)+"/"+phase+"/"+outcome, func(t *testing.T) {
					stores := teststores.New(t).Stores
					task := &models.Task{ProjectID: "default", Title: "compaction fixture", Category: models.CategoryBacklog, Status: models.StatusCompleted}
					require.NoError(t, stores.Tasks.Create(context.Background(), task))
					exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "CURRENT_REQUEST_739"}
					require.NoError(t, stores.Executions.Create(context.Background(), exec))
					dir := t.TempDir()
					file := filepath.Join(dir, "evidence.txt")
					require.NoError(t, os.WriteFile(file, []byte("TOOL_EVIDENCE_739"), 0600))
					var mu sync.Mutex
					var events []string
					summaries, generations, lastSummarySize := 0, 0, 0
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						raw, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							http.Error(w, "read", 500)
							return
						}
						var body map[string]any
						if err = json.Unmarshal(raw, &body); err != nil {
							t.Error(err)
							http.Error(w, "json", 400)
							return
						}
						mu.Lock()
						defer mu.Unlock()
						if len(events) > 30 {
							t.Error("unbounded requests")
							http.Error(w, "stop", 401)
							return
						}
						text := string(raw)
						if strings.Contains(text, localContextCompactionInstruction) || strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
							events = append(events, "summary")
							summaries++
							key := "messages"
							if provider == models.ProviderOpenAI {
								key = "input"
							}
							items, _ := body[key].([]any)
							if summaries == 1 && strings.HasPrefix(phase, "inturn") {
								if !strings.Contains(text, "TOOL_EVIDENCE_739") {
									t.Error("summary lost completed tool result")
								}
							}
							if summaries > 1 && strings.HasPrefix(outcome, "summary_overflow") && len(items) >= lastSummarySize {
								t.Error("summary retry did not shrink")
							}
							lastSummarySize = len(items)
							if outcome == "summary_overflow_exhausted" || outcome == "summary_overflow_once" && summaries == 1 {
								compactionServerError(w, 400, "context_length_exceeded", "context length exceeded")
								return
							}
							if outcome == "authentication" {
								compactionServerError(w, 401, "authentication_error", "invalid API key")
								return
							}
							if outcome == "transient_exhausted" || outcome == "transient_once" && summaries == 1 {
								compactionServerError(w, 503, "server_error", "temporarily unavailable")
								return
							}
							answer := "RECOVERED_739: TOOL_EVIDENCE_739 already read"
							if outcome == "empty_summary" {
								answer = ""
							}
							compactionServerReply(w, provider, answer, "")
							return
						}
						if phase == "preturn_native_failure" && summaries == 0 {
							events = append(events, "native_failure")
							compactionServerError(w, 400, "invalid_request_error", "context management failed")
							return
						}
						generations++
						if strings.HasPrefix(phase, "inturn") && generations == 1 {
							events = append(events, "tool_call")
							compactionServerReply(w, provider, "", file)
							return
						}
						if strings.HasPrefix(phase, "inturn") && generations == 2 {
							events = append(events, "tool_result_overflow")
							if !strings.Contains(text, "TOOL_EVIDENCE_739") {
								t.Error("tool did not execute before overflow")
							}
							if phase == "inturn_native_failure" {
								compactionServerError(w, 400, "invalid_request_error", "context management failed")
							} else {
								compactionServerError(w, 400, "context_length_exceeded", "context length exceeded")
							}
							return
						}
						events = append(events, "continuation")
						if !strings.Contains(text, "RECOVERED_739") {
							t.Error("continuation missing summary")
						}
						if strings.Count(text, "CURRENT_REQUEST_739") != 1 {
							t.Errorf("prompt missing or duplicated: count=%d", strings.Count(text, "CURRENT_REQUEST_739"))
						}
						if outcome == "continuation_overflow" {
							compactionServerError(w, 400, "context_length_exceeded", "context length exceeded")
							return
						}
						compactionServerReply(w, provider, "FINAL_739", "")
					})
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Upgrade") != "websocket" {
							handler.ServeHTTP(w, r)
							return
						}
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.CloseNow()
						for {
							_, data, err := conn.Read(r.Context())
							if err != nil {
								return
							}
							request := r.Clone(r.Context())
							request.Body = io.NopCloser(strings.NewReader(string(data)))
							rec := httptest.NewRecorder()
							handler.ServeHTTP(rec, request)
							if rec.Code >= 400 {
								var failure map[string]any
								_ = json.Unmarshal(rec.Body.Bytes(), &failure)
								failure["type"] = "error"
								failure["status"] = rec.Code
								encoded, _ := json.Marshal(failure)
								if conn.Write(r.Context(), websocket.MessageText, encoded) != nil {
									return
								}
								continue
							}
							for _, line := range strings.Split(rec.Body.String(), "\n") {
								if strings.HasPrefix(line, "data: ") {
									if conn.Write(r.Context(), websocket.MessageText, []byte(strings.TrimPrefix(line, "data: "))) != nil {
										return
									}
								}
							}
						}
					}))
					defer server.Close()
					oldAI, oldAnth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
					openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
					defer func() { openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldAI, oldAnth }()
					model := "test"
					if provider == models.ProviderOpenAI {
						model = "gpt-5.3-codex"
					}
					if provider == models.ProviderAnthropic {
						model = "claude-opus-5-5"
					}
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancel()
					req := llmcontracts.AgentRequest{ExecID: exec.ID, Ctx: ctx, Operation: llmcontracts.OperationStreaming, Followup: true, Message: "CURRENT_REQUEST_739", WorkDir: dir, DisableNativeCompaction: true, ChatHistory: []models.Execution{{PromptSent: "OLDER_REQUEST", Output: "OLDER_ANSWER", Status: models.ExecCompleted}}, Agent: models.LLMConfig{Provider: provider, Model: model, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", BaseURL: server.URL + "/v1", PresetSlug: "vllm", ContextWindow: 200000, MaxTokens: 1000, DisableNativeCompaction: true}}
					if strings.Contains(phase, "native") {
						req.DisableNativeCompaction = false
						req.Agent.DisableNativeCompaction = false
					}
					if phase == "preturn_native_failure" {
						req.ForceNativeCompaction = true
						req.Agent.ForceNativeCompaction = true
					}
					if phase == "preturn" {
						req.ContextTokenEstimate = 190000
					}
					svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err := svc.callProviderWithCompaction(svc.providerAdapters[provider], req)
					mu.Lock()
					defer mu.Unlock()
					expected := []string{}
					if phase == "preturn_native_failure" {
						expected = append(expected, "native_failure")
					}
					if strings.HasPrefix(phase, "inturn") {
						expected = append(expected, "tool_call", "tool_result_overflow")
					}
					for i := 0; i < summaries; i++ {
						expected = append(expected, "summary")
					}
					success := outcome == "success" || outcome == "summary_overflow_once" || outcome == "transient_once"
					if success || outcome == "continuation_overflow" {
						expected = append(expected, "continuation")
					}
					require.Equal(t, expected, events)
					if success {
						require.NoError(t, err)
						require.Contains(t, result.Output, "FINAL_739")
					} else {
						require.Error(t, err)
					}
					require.NoError(t, ctx.Err(), "test must finish through application handling, not deadline")
					switch outcome {
					case "summary_overflow_once", "transient_once":
						require.Equal(t, 2, summaries)
					case "transient_exhausted":
						require.Equal(t, 7, summaries, "initial request plus six client retries; no service retry")
					case "summary_overflow_exhausted":
						require.Greater(t, summaries, 1)
					default:
						require.Equal(t, 1, summaries)
					}
				})
			}
		}
	}
}

func compactionServerError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": kind, "code": kind, "message": message}})
}

func compactionServerReply(w http.ResponseWriter, provider models.LLMProvider, text, file string) {
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
	args, _ := json.Marshal(map[string]string{"file_path": file})
	switch provider {
	case models.ProviderOpenAI:
		var item map[string]any
		if file != "" {
			item = map[string]any{"type": "function_call", "id": "fc_read", "call_id": "read739", "name": "read_file", "arguments": string(args)}
		} else {
			item = map[string]any{"type": "message", "id": "msg739", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
		}
		emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp739", "status": "completed", "output": []any{item}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 5}}})
	case models.ProviderAnthropic:
		emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg739", "usage": map[string]int{"input_tokens": 20}}})
		block := map[string]any{"type": "text", "text": ""}
		delta := map[string]any{"type": "text_delta", "text": text}
		stop := "end_turn"
		if file != "" {
			block = map[string]any{"type": "tool_use", "id": "read739", "name": "read_file", "input": map[string]any{}}
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(args)}
			stop = "tool_use"
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
		emit(map[string]any{"type": "content_block_stop", "index": 0})
		emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 5}})
		emit(map[string]any{"type": "message_stop"})
	default:
		delta := map[string]any{"content": text}
		stop := "stop"
		if file != "" {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "read739", "type": "function", "function": map[string]any{"name": "read_file", "arguments": string(args)}}}}
			stop = "tool_calls"
		}
		emit(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": stop}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}
