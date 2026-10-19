package service

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

// This bridge keeps model responses identical across HTTP and WebSocket. HTTP
// mode rejects the upgrade so the real client's supported HTTP fallback runs.
func connectedModelServer(t *testing.T, transport string, handler http.Handler) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			handler.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(transport, "http") {
			http.Error(w, "websocket unavailable", http.StatusUpgradeRequired)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(16 << 20)
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			req := r.Clone(r.Context())
			req.Body = io.NopCloser(strings.NewReader(string(raw)))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
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
}

func TestConnectedOpenAICompactionLifecycle(t *testing.T) {
	for _, transport := range []string{"http", "websocket", "http_v2", "websocket_v2"} {
		for _, phase := range []string{"preturn", "inturn", "steering"} {
			mediaCases := []string{"text", "image"}
			if phase == "inturn" {
				mediaCases = append(mediaCases, "tool_image")
			}
			for _, media := range mediaCases {
				for _, outcome := range []string{"below_limit", "success", "failure"} {
					t.Run(transport+"/"+phase+"/"+media+"/"+outcome, func(t *testing.T) {
						stores := teststores.New(t).Stores
						ctx, cancel := context.WithTimeout(compactionTestContext(), 30*time.Second)
						defer cancel()
						agent := models.LLMConfig{Name: "connected model", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodAPIKey, APIKey: "test", ContextWindow: 200000}
						if strings.HasSuffix(transport, "_v2") {
							agent.Model = "gpt-6-astra"
						}
						require.NoError(t, stores.LLMConfigs.Create(ctx, &agent))
						task := &models.Task{ProjectID: "default", Title: "connected compaction", Category: models.CategoryBacklog, Status: models.StatusCompleted}
						require.NoError(t, stores.Tasks.Create(ctx, task))
						old := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "OLD_REQUEST", Output: "OLD_ANSWER"}
						require.NoError(t, stores.Executions.Create(ctx, &old))
						active := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "CURRENT_CONNECTED"}
						require.NoError(t, stores.Executions.Create(ctx, &active))
						dir := t.TempDir()
						file := filepath.Join(dir, "tool.txt")
						require.NoError(t, os.WriteFile(file, []byte("TOOL_CONNECTED"), 0600))
						if media == "tool_image" {
							att := connectedImageAttachment(t, dir)
							file = filepath.Join(dir, "TOOL_CONNECTED.png")
							require.NoError(t, os.Rename(att.FilePath, file))
						}
						var mu sync.Mutex
						generations, compactions, summaries, followups := 0, 0, 0, 0
						restarted, claimed := false, false
						initial := []any{map[string]any{"type": "message", "role": "user", "content": "OLD_REQUEST"}}
						if media == "image" {
							initial = append(initial, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 1000000)}}})
						}
						native, _ := json.Marshal(initial)
						handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							raw, err := io.ReadAll(r.Body)
							if err != nil {
								t.Error(err)
								return
							}
							text := string(raw)
							mu.Lock()
							defer mu.Unlock()
							if restarted {
								followups++
								if strings.Contains(text, localContextCompactionInstruction) || strings.HasSuffix(r.URL.Path, "/compact") {
									t.Error("follow-up unnecessarily compacted")
									compactionServerError(w, 401, "stop", "stop")
									return
								}
								if !strings.Contains(text, "NEXT_CONNECTED") {
									t.Error("missing next prompt")
								}
								if phase == "steering" && !strings.Contains(text, "STEER_CONNECTED") {
									t.Error("restart lost steering")
								}
								if outcome != "below_limit" && !strings.Contains(text, "SAVED_CONNECTED") {
									t.Error("restart lost compacted context")
								}
								compactionServerReply(w, models.ProviderOpenAI, "FOLLOWUP_CONNECTED", "")
								return
							}
							if !strings.Contains(text, "compaction_trigger") && (strings.Contains(text, localContextCompactionInstruction) || strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION")) {
								summaries++
								compactionServerReply(w, models.ProviderOpenAI, "SAVED_CONNECTED", "")
								return
							}
							if strings.HasSuffix(r.URL.Path, "/responses/compact") || strings.Contains(text, "compaction_trigger") {
								compactions++
								if outcome == "below_limit" {
									t.Error("image or duplicate history triggered compaction")
								}
								if phase == "inturn" && !strings.Contains(text, "TOOL_CONNECTED") {
									t.Error("compaction lost tool result")
								}
								if strings.Contains(text, "STEER_CONNECTED") {
									t.Error("pending steering consumed before compaction")
								}
								if outcome == "failure" {
									compactionServerError(w, 400, "invalid_request_error", "context management failed")
									return
								}
								compacted := []any{map[string]any{"type": "compaction", "encrypted_content": "SAVED_CONNECTED"}}
								if media == "image" {
									compacted = append(compacted, initial[1])
								}
								if strings.Contains(text, "compaction_trigger") {
									w.Header().Set("Content-Type", "text/event-stream")
									for _, event := range []any{map[string]any{"type": "response.output_item.done", "item": compacted[0]}, map[string]any{"type": "response.completed", "response": map[string]any{"id": "compacted", "status": "completed", "output": []any{compacted[0]}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 5}}}} {
										raw, _ := json.Marshal(event)
										fmt.Fprintf(w, "data: %s\n\n", raw)
									}
								} else {
									w.Header().Set("Content-Type", "application/json")
									_ = json.NewEncoder(w).Encode(map[string]any{"output": compacted})
								}
								return
							}
							generations++
							if generations > 4 {
								t.Error("unbounded generation")
								compactionServerError(w, 401, "stop", "stop")
								return
							}
							if generations == 1 && phase != "preturn" {
								rec := httptest.NewRecorder()
								tool := ""
								if phase == "inturn" {
									tool = file
								}
								compactionServerReply(rec, models.ProviderOpenAI, "FIRST_CONNECTED", tool)
								body := rec.Body.String()
								if media == "tool_image" {
									body = strings.ReplaceAll(body, `"name":"read_file"`, `"name":"view_image"`)
								}
								if outcome != "below_limit" {
									body = strings.ReplaceAll(body, `"input_tokens":20`, `"input_tokens":190000`)
								}
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprint(w, body)
								return
							}
							if phase == "steering" && !strings.Contains(text, "STEER_CONNECTED") {
								t.Error("continuation lost steering")
							}
							if media == "tool_image" && outcome == "below_limit" && !strings.Contains(text, "input_image") {
								t.Error("view_image bytes missing from continuation")
							}
							compactionServerReply(w, models.ProviderOpenAI, "FINAL_CONNECTED", "")
						})
						server := connectedModelServer(t, transport, handler)
						defer server.Close()
						oldURL := openaiclient.OpenAIAPIBaseURL
						openaiclient.OpenAIAPIBaseURL = server.URL + "/v1/"
						defer func() { openaiclient.OpenAIAPIBaseURL = oldURL }()
						req := llmcontracts.AgentRequest{ProjectID: "default", Ctx: ctx, ExecID: active.ID, Agent: agent, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: dir, Message: active.PromptSent, ChatHistory: []models.Execution{old}}
						svc := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
						seed := req
						seed.ExecID = old.ID
						svc.persistNativeProviderStateCheckpoint(seed, "openai_responses", string(native))
						if phase == "preturn" && outcome != "below_limit" {
							req.ContextTokenEstimate = 190000
						}
						var steer *models.ThreadInput
						if phase == "steering" {
							steer = &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: active.ID, Content: "STEER_CONNECTED"}
							require.NoError(t, stores.ThreadInputs.CreateSteeringForActiveExecution(ctx, steer, active.ID))
							req.Ctx = llmcontracts.WithSteeringPendingCheck(ctx, func(context.Context) (bool, error) { return !claimed, nil })
							req.Ctx = llmcontracts.WithLocalSteeringCallback(req.Ctx, func(context.Context) (llmcontracts.LocalSteeringInput, error) {
								if claimed {
									return llmcontracts.LocalSteeringInput{}, nil
								}
								claimed = true
								return llmcontracts.LocalSteeringInput{Text: steer.Content, Commit: func(commitCtx context.Context, history []any) error {
									return stores.ThreadInputs.CommitLocalSteering(commitCtx, persistence.LocalSteeringCommit{ExecutionID: active.ID, InputIDs: []string{steer.ID}, ResponsesHistory: history})
								}}, nil
							})
						}
						result, err := svc.callProviderWithCompaction(svc.providerAdapters[agent.Provider], req)
						terminal := outcome == "failure" && phase != "preturn"
						if steer != nil {
							savedSteer, readErr := stores.ThreadInputs.GetByID(ctx, steer.ID)
							require.NoError(t, readErr)
							if terminal {
								require.Equal(t, models.ThreadInputPending, savedSteer.InputStatus)
							} else {
								require.Equal(t, models.ThreadInputApplied, savedSteer.InputStatus)
							}
						}
						if terminal {
							require.Error(t, err)
							require.Equal(t, 1, generations)
							require.Zero(t, summaries)
							replay, readErr := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{active.ID})
							require.NoError(t, readErr)
							require.NotEmpty(t, replay[active.ID])
							if phase == "inturn" {
								raw, _ := json.Marshal(replay)
								require.Contains(t, string(raw), "TOOL_CONNECTED")
							}
							return
						}
						require.NoError(t, err)
						if outcome == "below_limit" {
							require.Zero(t, compactions)
						} else {
							require.Equal(t, 1, compactions)
						}
						if outcome == "failure" {
							require.Equal(t, 1, summaries)
						} else {
							require.Zero(t, summaries)
						}
						require.NoError(t, stores.Executions.Complete(ctx, active.ID, models.ExecCompleted, result.Output, "", 0, 0))
						saved, readErr := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
						require.NoError(t, readErr)
						require.NotNil(t, saved)
						history, readErr := stores.Executions.ListByTaskChronological(ctx, task.ID)
						require.NoError(t, readErr)
						next := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "NEXT_CONNECTED"}
						require.NoError(t, stores.Executions.Create(ctx, &next))
						mu.Lock()
						restarted = true
						mu.Unlock()
						// Fresh service and provider adapters: no in-memory conversation survives.
						fresh := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
						req.Ctx = ctx
						req.ExecID = next.ID
						req.Message = next.PromptSent
						req.ChatHistory = history
						req.ContextTokenEstimate = 0
						result, err = fresh.callProviderWithCompaction(fresh.providerAdapters[agent.Provider], req)
						require.NoError(t, err)
						require.Contains(t, result.Output, "FOLLOWUP_CONNECTED")
						require.Equal(t, 1, followups)
					})
				}
			}
		}
	}
}

func connectedAnthropicCompactionReply(w http.ResponseWriter, steered bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []string{
		`{"type":"message_start","message":{"id":"native","usage":{"input_tokens":30}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"compaction"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"SAVED_NATIVE_ANTHROPIC"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"FINAL_ANTHROPIC"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`,
		`{"type":"message_stop"}`,
	} {
		if steered {
			event = strings.ReplaceAll(event, "SAVED_NATIVE_ANTHROPIC", "SAVED_NATIVE_ANTHROPIC STEER_NATIVE")
		}
		fmt.Fprintf(w, "data: %s\n\n", event)
	}
}

func TestConnectedAnthropicNativeLifecycle(t *testing.T) {
	for _, phase := range []string{"preturn", "inturn", "steering"} {
		for _, media := range []string{"text", "image"} {
			for _, compact := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/compact=%t", phase, media, compact), func(t *testing.T) {
					stores := teststores.New(t).Stores
					ctx := compactionTestContext()
					agent := models.LLMConfig{Name: "native Anthropic", Provider: models.ProviderAnthropic, Model: "claude-opus-5-5", AuthMethod: models.AuthMethodAPIKey, APIKey: "test", ContextWindow: 200000, CompactionThreshold: 100000, MaxTokens: 1000}
					require.NoError(t, stores.LLMConfigs.Create(ctx, &agent))
					task := &models.Task{ProjectID: "default", Title: "native Anthropic", Category: models.CategoryBacklog, Status: models.StatusCompleted}
					require.NoError(t, stores.Tasks.Create(ctx, task))
					old := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "OLD_NATIVE", Output: "OLD_RESULT"}
					require.NoError(t, stores.Executions.Create(ctx, &old))
					active := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "CURRENT_NATIVE"}
					require.NoError(t, stores.Executions.Create(ctx, &active))
					file := filepath.Join(t.TempDir(), "read.txt")
					require.NoError(t, os.WriteFile(file, []byte("TOOL_NATIVE"), 0600))
					calls, native, nextCalls := 0, 0, 0
					claimed, restarted := false, false
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						raw, _ := io.ReadAll(r.Body)
						text := string(raw)
						if restarted {
							nextCalls++
							if compact && !strings.Contains(text, "SAVED_NATIVE_ANTHROPIC") {
								t.Error("restart lost native context")
							}
							if phase == "steering" && !strings.Contains(text, "STEER_NATIVE") {
								t.Error("restart lost steering")
							}
							if !strings.Contains(text, "NEXT_NATIVE") {
								t.Error("missing follow-up")
							}
							compactionServerReply(w, models.ProviderAnthropic, "NEXT_NATIVE_DONE", "")
							return
						}
						calls++
						if calls == 1 && media == "image" && !strings.Contains(text, "image/png") {
							t.Error("initial image missing")
						}
						if calls > 3 {
							t.Error("unexpected extra request")
							compactionServerError(w, 401, "stop", "stop")
							return
						}
						if (phase == "inturn" || phase == "steering") && calls == 1 {
							compactionServerReply(w, models.ProviderAnthropic, "", file)
							return
						}
						if compact && native == 0 {
							if !strings.Contains(text, "compact_20260112") {
								t.Error("native compaction not enabled on wire")
							}
							if phase == "inturn" && !strings.Contains(text, "TOOL_NATIVE") {
								t.Error("tool result missing")
							}
							native++
							connectedAnthropicCompactionReply(w, phase == "steering")
							return
						}
						if phase == "steering" && calls == 2 && !strings.Contains(text, "STEER_NATIVE") {
							t.Error("steer missing")
						}
						compactionServerReply(w, models.ProviderAnthropic, "FINAL_ANTHROPIC", "")
					}))
					defer server.Close()
					oldHost := anthropicclient.AnthropicAPIHost
					anthropicclient.AnthropicAPIHost = server.URL
					defer func() { anthropicclient.AnthropicAPIHost = oldHost }()
					req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: active.ID, Agent: agent, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: filepath.Dir(file), Message: active.PromptSent, ChatHistory: []models.Execution{old}}
					if media == "image" {
						req.Attachments = []models.Attachment{connectedImageAttachment(t, filepath.Dir(file))}
					}
					if compact && phase == "preturn" {
						req.ContextTokenEstimate = 110000
					}
					if phase == "steering" {
						req.Ctx = llmcontracts.WithSteeringCallback(ctx, func(context.Context) (string, error) {
							if claimed {
								return "", nil
							}
							claimed = true
							return "STEER_NATIVE", nil
						})
					}
					svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err := svc.callProviderWithCompaction(svc.providerAdapters[agent.Provider], req)
					require.NoError(t, err)
					if compact {
						require.Equal(t, 1, native)
					} else {
						require.Zero(t, native)
					}
					require.NoError(t, stores.Executions.Complete(ctx, active.ID, models.ExecCompleted, result.Output, "", 0, 0))
					if compact {
						saved, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
						require.NoError(t, err)
						require.NotNil(t, saved)
						require.Contains(t, saved.ProviderStateJSON, "SAVED_NATIVE_ANTHROPIC")
					}
					history, err := stores.Executions.ListByTaskChronological(ctx, task.ID)
					require.NoError(t, err)
					next := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "NEXT_NATIVE"}
					require.NoError(t, stores.Executions.Create(ctx, &next))
					restarted = true
					req.Ctx = ctx
					req.Attachments = nil
					req.ExecID = next.ID
					req.Message = next.PromptSent
					req.ChatHistory = history
					req.ContextTokenEstimate = 0
					fresh := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err = fresh.callProviderWithCompaction(fresh.providerAdapters[agent.Provider], req)
					require.NoError(t, err)
					require.Contains(t, result.Output, "NEXT_NATIVE_DONE")
					require.Equal(t, 1, nextCalls)
				})
			}
		}
	}
}

func TestConnectedProviderSwitchLifecycle(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	providers := []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible}
	for _, from := range providers {
		for _, to := range providers {
			modes := []string{"below_limit", "summary"}
			if from == models.ProviderOpenAI {
				modes = append(modes, "native_source")
			}
			for _, mode := range modes {
				t.Run(string(from)+"/"+string(to)+"/"+mode, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(compactionTestContext(), 30*time.Second)
					defer cancel()
					stores := teststores.New(t).Stores
					task := &models.Task{ProjectID: "default", Title: "switch lifecycle", Category: models.CategoryBacklog, Status: models.StatusCompleted}
					require.NoError(t, stores.Tasks.Create(ctx, task))
					provider := from
					stage := 0
					sourceCalls, targetCalls, summaries, restarts := 0, 0, 0, 0
					server := connectedModelServer(t, "websocket", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						raw, _ := io.ReadAll(r.Body)
						text := string(raw)
						if strings.HasSuffix(r.URL.Path, "/responses/compact") {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, `{"output":[{"type":"message","role":"user","content":"HISTORY_EVIDENCE"},{"type":"compaction","encrypted_content":"OPAQUE_SOURCE"}]}`)
							return
						}
						if strings.Contains(text, localContextCompactionInstruction) || strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
							summaries++
							if !strings.Contains(text, "HISTORY_EVIDENCE") {
								t.Error("switch summary lost old history")
							}
							if to != models.ProviderOpenAI && strings.Contains(text, "OPAQUE_SOURCE") {
								t.Error("encrypted OpenAI state sent to different provider")
							}
							compactionServerReply(w, provider, "SWITCH_SUMMARY HISTORY_EVIDENCE", "")
							return
						}
						if stage == 0 {
							sourceCalls++
							compactionServerReply(w, provider, "SOURCE_FINAL", "")
							return
						}
						if !strings.Contains(text, "HISTORY_EVIDENCE") {
							t.Error("destination lost history")
						}
						if to != models.ProviderOpenAI && strings.Contains(text, "OPAQUE_SOURCE") {
							t.Error("destination received opaque OpenAI state")
						}
						if stage == 1 {
							targetCalls++
							if !strings.Contains(text, "SWITCH_REQUEST") {
								t.Error("missing switch request")
							}
							compactionServerReply(w, provider, "DESTINATION_FINAL", "")
							return
						}
						restarts++
						if !strings.Contains(text, "NEXT_SWITCH") {
							t.Error("missing restart prompt")
						}
						if !strings.Contains(text, "DESTINATION_FINAL") {
							t.Error("restart lost destination answer")
						}
						compactionServerReply(w, provider, "RESTART_SWITCH_DONE", "")
					}))
					defer server.Close()
					oldAI, oldAnth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
					openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
					defer func() { openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldAI, oldAnth }()
					configIndex := 0
					config := func(p models.LLMProvider) models.LLMConfig {
						model := "fixture"
						if p == models.ProviderOpenAI {
							model = "gpt-5.3-codex"
						}
						if p == models.ProviderAnthropic {
							model = "claude-opus-5-5"
						}
						configIndex++
						a := models.LLMConfig{Name: fmt.Sprintf("saved model %d", configIndex), Provider: p, Model: model, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", ContextWindow: 200000, BaseURL: server.URL + "/v1", PresetSlug: "vllm"}
						require.NoError(t, stores.LLMConfigs.Create(ctx, &a))
						return a
					}
					source, target := config(from), config(to)
					old := models.Execution{TaskID: task.ID, AgentConfigID: source.ID, Status: models.ExecCompleted, PromptSent: "HISTORY_EVIDENCE", Output: "old answer"}
					require.NoError(t, stores.Executions.Create(ctx, &old))
					first := models.Execution{TaskID: task.ID, AgentConfigID: source.ID, Status: models.ExecRunning, PromptSent: "SOURCE_REQUEST"}
					require.NoError(t, stores.Executions.Create(ctx, &first))
					req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: first.ID, Agent: source, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, Message: first.PromptSent, ChatHistory: []models.Execution{old}, WorkDir: t.TempDir()}
					if mode == "native_source" {
						req.ContextTokenEstimate = 190000
					}
					svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err := svc.callProviderWithCompaction(svc.providerAdapters[from], req)
					require.NoError(t, err)
					require.Equal(t, 1, sourceCalls)
					require.NoError(t, stores.Executions.Complete(ctx, first.ID, models.ExecCompleted, result.Output, "", 0, 0))
					history, err := stores.Executions.ListByTaskChronological(ctx, task.ID)
					require.NoError(t, err)
					second := models.Execution{TaskID: task.ID, AgentConfigID: target.ID, Status: models.ExecRunning, PromptSent: "SWITCH_REQUEST"}
					require.NoError(t, stores.Executions.Create(ctx, &second))
					provider = to
					stage = 1
					req.Agent = target
					req.ExecID = second.ID
					req.Message = second.PromptSent
					req.ChatHistory = history
					req.ContextTokenEstimate = 0
					req.DisableNativeCompaction = true
					if mode == "summary" {
						req.ContextTokenEstimate = 190000
					}
					fresh := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err = fresh.callProviderWithCompaction(fresh.providerAdapters[to], req)
					require.NoError(t, err)
					require.Equal(t, 1, targetCalls)
					if mode == "summary" || (mode == "native_source" && to != models.ProviderOpenAI) {
						require.Positive(t, summaries)
					} else {
						require.Zero(t, summaries)
					}
					require.NoError(t, stores.Executions.Complete(ctx, second.ID, models.ExecCompleted, result.Output, "", 0, 0))
					history, err = stores.Executions.ListByTaskChronological(ctx, task.ID)
					require.NoError(t, err)
					third := models.Execution{TaskID: task.ID, AgentConfigID: target.ID, Status: models.ExecRunning, PromptSent: "NEXT_SWITCH"}
					require.NoError(t, stores.Executions.Create(ctx, &third))
					stage = 2
					req.ExecID = third.ID
					req.Message = third.PromptSent
					req.ChatHistory = history
					req.ContextTokenEstimate = 0
					fresh = NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					before := summaries
					result, err = fresh.callProviderWithCompaction(fresh.providerAdapters[to], req)
					require.NoError(t, err)
					require.Contains(t, result.Output, "RESTART_SWITCH_DONE")
					require.Equal(t, 1, restarts)
					require.Equal(t, before, summaries, "restart must reuse saved state")
				})
			}
		}
	}
}

func TestConnectedImageFollowupLifecycle(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	for _, route := range []string{"openai_http", "openai_websocket", "anthropic", "openai_compatible"} {
		for _, compact := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/summary=%t", route, compact), func(t *testing.T) {
				provider := models.ProviderOpenAI
				transport := "websocket"
				model := "gpt-5.3-codex"
				switch route {
				case "openai_http":
					transport = "http"
				case "anthropic":
					provider = models.ProviderAnthropic
					model = "claude-opus-5-5"
				case "openai_compatible":
					provider = models.ProviderOpenAICompatible
					model = "fixture"
				}
				ctx := compactionTestContext()
				stores := teststores.New(t).Stores
				task := &models.Task{ProjectID: "default", Title: "images", Category: models.CategoryBacklog, Status: models.StatusCompleted}
				require.NoError(t, stores.Tasks.Create(ctx, task))
				dir := t.TempDir()
				path := filepath.Join(dir, "image.png")
				img := image.NewNRGBA(image.Rect(0, 0, 600, 600))
				rng := rand.New(rand.NewSource(1))
				_, _ = rng.Read(img.Pix)
				f, err := os.Create(path)
				require.NoError(t, err)
				require.NoError(t, png.Encode(f, img))
				require.NoError(t, f.Close())
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Greater(t, info.Size(), int64(800000))
				summaries, generations := 0, 0
				server := connectedModelServer(t, transport, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					text := string(raw)
					if strings.Contains(text, localContextCompactionInstruction) || strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
						summaries++
						compactionServerReply(w, provider, "IMAGE_SUMMARY", "")
						return
					}
					generations++
					if generations > 2 {
						t.Error("unexpected generation")
						compactionServerError(w, 401, "stop", "stop")
						return
					}
					if !strings.Contains(text, "image/png") {
						t.Errorf("image missing from generation %d", generations)
					}
					if generations == 2 && !strings.Contains(text, "IMAGE_FOLLOWUP") {
						t.Error("missing follow-up")
					}
					compactionServerReply(w, provider, "IMAGE_ANSWER", "")
				}))
				defer server.Close()
				oldAI, oldAnth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
				openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
				defer func() { openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldAI, oldAnth }()
				agent := models.LLMConfig{Name: "image model", Provider: provider, Model: model, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", BaseURL: server.URL + "/v1", PresetSlug: "vllm", ContextWindow: 200000, DisableNativeCompaction: true}
				require.NoError(t, stores.LLMConfigs.Create(ctx, &agent))
				old := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "old", Output: "answer"}
				require.NoError(t, stores.Executions.Create(ctx, &old))
				active := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "DESCRIBE_IMAGE"}
				require.NoError(t, stores.Executions.Create(ctx, &active))
				req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: active.ID, Agent: agent, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: dir, Message: active.PromptSent, ChatHistory: []models.Execution{old}, DisableNativeCompaction: true, Attachments: []models.Attachment{{TaskID: task.ID, FilePath: path, FileName: "image.png", MediaType: "image/png", FileSize: info.Size()}}}
				if compact {
					req.ContextTokenEstimate = 190000
				}
				svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
				result, err := svc.callProviderWithCompaction(svc.providerAdapters[provider], req)
				require.NoError(t, err)
				require.NoError(t, stores.Executions.Complete(ctx, active.ID, models.ExecCompleted, result.Output, "", 0, 0))
				if compact {
					require.Equal(t, 1, summaries)
				} else {
					require.Zero(t, summaries)
				}
				history, err := stores.Executions.ListByTaskChronological(ctx, task.ID)
				require.NoError(t, err)
				next := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "IMAGE_FOLLOWUP"}
				require.NoError(t, stores.Executions.Create(ctx, &next))
				req.ExecID = next.ID
				req.Message = next.PromptSent
				req.ChatHistory = history
				req.Attachments = nil
				req.ContextTokenEstimate = 0
				fresh := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
				before := summaries
				result, err = fresh.callProviderWithCompaction(fresh.providerAdapters[provider], req)
				require.NoError(t, err)
				require.Contains(t, result.Output, "IMAGE_ANSWER")
				require.Equal(t, before, summaries, "image replay must not trigger another compaction")
				require.Equal(t, 2, generations)
			})
		}
	}
}

func connectedImageAttachment(t *testing.T, dir string) models.Attachment {
	t.Helper()
	path := filepath.Join(dir, "fixture.png")
	img := image.NewNRGBA(image.Rect(0, 0, 600, 600))
	rng := rand.New(rand.NewSource(1))
	_, _ = rng.Read(img.Pix)
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, img))
	require.NoError(t, f.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	return models.Attachment{FileName: "fixture.png", FilePath: path, MediaType: "image/png", FileSize: info.Size()}
}

// Keep real retry decisions and attempts, but make their timers ready immediately.
func compactionTestContext() context.Context {
	return httpretry.WithRetryTimer(context.Background(), func(time.Duration) <-chan time.Time {
		ready := make(chan time.Time, 1)
		ready <- time.Time{}
		return ready
	})
}
