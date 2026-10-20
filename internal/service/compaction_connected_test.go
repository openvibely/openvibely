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
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
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
			mediaCases := []string{"text", "image", "tool_image"}
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
						sawToolImage := false
						initial := []any{map[string]any{"type": "message", "role": "user", "content": "OLD_REQUEST"}}
						if media == "image" {
							initial = append(initial, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 1000000)}}})
						}
						if media == "tool_image" && phase != "inturn" {
							initial = append(initial,
								map[string]any{"type": "function_call", "call_id": "previous_image", "name": "view_image", "arguments": "{}"},
								map[string]any{"type": "function_call_output", "call_id": "previous_image", "output": "TOOL_CONNECTED"},
								map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 1000000)}}})
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
							if strings.Contains(text, "input_image") {
								sawToolImage = true
							}
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
								// WebSocket v2 may refer to the preceding request rather
								// than resend its image bytes.
								if media == "tool_image" && !strings.Contains(text, "input_image") && !(sawToolImage && strings.Contains(text, "previous_response_id")) {
									t.Error("native compaction request lost tool image")
								}
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
							if generations == 1 && media == "tool_image" && phase == "steering" && !strings.Contains(text, "input_image") {
								t.Error("initial steering request lost saved tool image")
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
							if media == "tool_image" && outcome == "below_limit" && (phase != "steering" || strings.HasPrefix(transport, "http")) && !strings.Contains(text, "input_image") {
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
		for _, media := range []string{"text", "image", "tool_image"} {
			for _, outcome := range []string{"none", "success", "null", "success_then_null"} {
				compact := outcome != "none"
				successful := outcome == "success" || outcome == "success_then_null"
				t.Run(fmt.Sprintf("%s/%s/%s", phase, media, outcome), func(t *testing.T) {
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
					imagePath := ""
					if media == "tool_image" {
						imagePath = filepath.Join(filepath.Dir(file), "large.png")
						img := image.NewNRGBA(image.Rect(0, 0, 600, 600))
						_, err := rand.New(rand.NewSource(1)).Read(img.Pix)
						require.NoError(t, err)
						f, err := os.Create(imagePath)
						require.NoError(t, err)
						require.NoError(t, png.Encode(f, img))
						require.NoError(t, f.Close())
					}
					calls, native, nextCalls := 0, 0, 0
					checkpointStep := 1
					if phase != "preturn" {
						checkpointStep = 2
					}
					claimed, restarted := false, false
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						raw, _ := io.ReadAll(r.Body)
						text := string(raw)
						if restarted {
							nextCalls++
							if successful && !strings.Contains(text, "SAVED_NATIVE_ANTHROPIC") {
								t.Error("restart lost native context")
							}
							if (media == "tool_image" || (media == "image" && !successful)) && !strings.Contains(text, "image/png") {
								t.Error("reload lost image")
							}
							if !successful && !strings.Contains(text, "CURRENT_NATIVE") {
								t.Error("reload lost original prompt")
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
						if strings.Contains(text, localContextCompactionInstruction) {
							t.Error("unexpected summary")
							compactionServerError(w, 401, "stop", "stop")
							return
						}
						if calls == 1 && media == "image" && !strings.Contains(text, "image/png") {
							t.Error("initial image missing")
						}
						if calls > checkpointStep+1 {
							t.Error("unexpected extra request")
							compactionServerError(w, 401, "stop", "stop")
							return
						}
						if (phase == "inturn" || phase == "steering") && calls == 1 {
							compactionServerReply(w, models.ProviderAnthropic, "", file)
							return
						}
						if phase == "steering" && calls == 2 && !strings.Contains(text, "STEER_NATIVE") {
							t.Error("steer missing from request before compaction")
						}
						if calls == checkpointStep {
							if !strings.Contains(text, "compact_20260112") {
								t.Error("native compaction not enabled on wire")
							}
							if phase == "inturn" && !strings.Contains(text, "TOOL_NATIVE") {
								t.Error("tool result missing")
							}
							if compact {
								native++
							}
							tool, path := "read_file", file
							if media == "tool_image" {
								tool, path = "view_image", imagePath
							}
							connectedAnthropicLifecycleReply(w, outcome, strings.Contains(text, "STEER_NATIVE"), tool, path)
							return
						}
						if !successful && (!strings.Contains(text, "CURRENT_NATIVE") || !strings.Contains(text, "OLD_NATIVE")) {
							t.Error("failed/no compaction lost prior context")
						}
						if media == "tool_image" && !strings.Contains(text, "image/png") {
							t.Error("tool image absent after compaction")
						}
						if outcome == "success_then_null" {
							native++
							connectedAnthropicLifecycleReply(w, "null", false, "", "")
							return
						}
						compactionServerReply(w, models.ProviderAnthropic, "FINAL_ANTHROPIC", "")
					}))
					defer server.Close()
					oldHost := anthropicclient.AnthropicAPIHost
					anthropicclient.AnthropicAPIHost = server.URL
					defer func() { anthropicclient.AnthropicAPIHost = oldHost }()
					req := llmcontracts.AgentRequest{ProjectID: "default", Ctx: ctx, ExecID: active.ID, Agent: agent, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: filepath.Dir(file), Message: active.PromptSent, ChatHistory: []models.Execution{old}}
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
					svc := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
					result, err := svc.callProviderWithCompaction(svc.providerAdapters[agent.Provider], req)
					require.NoError(t, err)
					if compact {
						want := 1
						if outcome == "success_then_null" {
							want = 2
						}
						require.Equal(t, want, native)
					} else {
						require.Zero(t, native)
					}
					require.NoError(t, stores.Executions.Complete(ctx, active.ID, models.ExecCompleted, result.Output, "", 0, 0))
					if successful {
						saved, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
						require.NoError(t, err)
						require.NotNil(t, saved)
						require.Contains(t, saved.ProviderStateJSON, "SAVED_NATIVE_ANTHROPIC")
						if media == "tool_image" {
							require.Contains(t, saved.ProviderStateJSON, "image/png")
							require.Greater(t, len(saved.ProviderStateJSON), 800000)
							budgetReq := req
							budgetReq.NativeCompactionStateJSON = saved.ProviderStateJSON
							require.Less(t, calculateRequestBudget(budgetReq).NativeStateTokens, 5000)
						}
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
					fresh := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
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
			modes := []string{"below_limit", "summary", "summary_source"}
			if from == models.ProviderOpenAI || from == models.ProviderAnthropic {
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
							if !strings.Contains(text, "RECOVER_ONLY_FROM_DB") {
								t.Error("source compaction did not receive old answer")
							}
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
							if !strings.Contains(text, "RECOVER_ONLY_FROM_DB") {
								t.Error("switch summary lost saved older answer")
							}
							summary := "SWITCH_SUMMARY HISTORY_EVIDENCE"
							if strings.Contains(text, "RECOVER_ONLY_FROM_DB") {
								summary += " RECOVER_ONLY_FROM_DB"
							}
							compactionServerReply(w, provider, summary, "")
							return
						}
						if stage == 0 {
							sourceCalls++
							if mode == "summary_source" && (!strings.Contains(text, "SWITCH_SUMMARY") || !strings.Contains(text, "SOURCE_REQUEST")) {
								t.Error("source generation missing summary or current request")
							}
							if from == models.ProviderAnthropic && mode == "native_source" {
								if !strings.Contains(text, "context_management") || !strings.Contains(text, "RECOVER_ONLY_FROM_DB") {
									t.Error("Anthropic native compaction missing configuration or history")
								}
								w.Header().Set("Content-Type", "text/event-stream")
								emit := func(event any) { raw, _ := json.Marshal(event); fmt.Fprintf(w, "data: %s\n\n", raw) }
								emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "native_source", "usage": map[string]int{"input_tokens": 30}}})
								emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "compaction", "content": nil}})
								emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "compaction_delta", "content": "ANTHROPIC_NATIVE_SUMMARY HISTORY_EVIDENCE RECOVER_ONLY_FROM_DB"}})
								emit(map[string]any{"type": "content_block_stop", "index": 0})
								emit(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}})
								emit(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "SOURCE_FINAL"}})
								emit(map[string]any{"type": "content_block_stop", "index": 1})
								emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 10}})
								emit(map[string]any{"type": "message_stop"})
								return
							}
							compactionServerReply(w, provider, "SOURCE_FINAL", "")
							return
						}
						if !strings.Contains(text, "HISTORY_EVIDENCE") {
							t.Error("destination lost history")
						}
						if to != models.ProviderOpenAI && strings.Contains(text, "OPAQUE_SOURCE") {
							t.Error("destination received opaque OpenAI state")
						}
						if (mode != "native_source" || from != models.ProviderOpenAI || to != models.ProviderOpenAI) && !strings.Contains(text, "RECOVER_ONLY_FROM_DB") {
							t.Error("destination lost saved older answer")
						}
						if (mode == "summary_source" || (mode == "native_source" && from == models.ProviderAnthropic)) && !strings.Contains(text, "SOURCE_FINAL") {
							t.Error("destination lost post-compaction source answer")
						}
						if from == models.ProviderAnthropic && mode == "native_source" {
							if !strings.Contains(text, "ANTHROPIC_NATIVE_SUMMARY") {
								t.Error("destination lost native summary")
							}
							if strings.Contains(text, `"type":"compaction"`) {
								t.Error("destination received incompatible native block")
							}
						}
						if mode == "summary_source" && !strings.Contains(text, "SWITCH_SUMMARY") {
							t.Error("destination lost source summary")
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
					old := models.Execution{TaskID: task.ID, AgentConfigID: source.ID, Status: models.ExecCompleted, PromptSent: "HISTORY_EVIDENCE", Output: "RECOVER_ONLY_FROM_DB"}
					require.NoError(t, stores.Executions.Create(ctx, &old))
					require.NoError(t, stores.Executions.Complete(ctx, old.ID, models.ExecCompleted, old.Output, "", 0, 0))
					first := models.Execution{TaskID: task.ID, AgentConfigID: source.ID, Status: models.ExecRunning, PromptSent: "SOURCE_REQUEST"}
					require.NoError(t, stores.Executions.Create(ctx, &first))
					req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: first.ID, Agent: source, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, Message: first.PromptSent, ChatHistory: []models.Execution{old}, WorkDir: t.TempDir()}
					if mode == "native_source" {
						req.ContextTokenEstimate = 190000
						if from == models.ProviderAnthropic {
							req.ContextTokenEstimate = 110000
						}
					}
					if mode == "summary_source" {
						req.ContextTokenEstimate = 190000
						req.DisableNativeCompaction = true
					}
					svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err := svc.callProviderWithCompaction(svc.providerAdapters[from], req)
					require.NoError(t, err)
					require.Equal(t, 1, sourceCalls)
					if mode == "summary_source" {
						require.Equal(t, 1, summaries)
						checkpoint, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
						require.NoError(t, err)
						require.NotNil(t, checkpoint)
						require.Empty(t, checkpoint.ProviderStateJSON)
						require.Equal(t, source.ID, checkpoint.ModelConfigID)
						require.Equal(t, old.ID, checkpoint.SourceExecutionID)
						require.Equal(t, "local_summary", checkpoint.Strategy)
						require.Contains(t, checkpoint.Summary, "SWITCH_SUMMARY")
						require.Contains(t, checkpoint.Summary, "RECOVER_ONLY_FROM_DB")
					}
					if mode == "native_source" {
						checkpoint, checkpointErr := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
						require.NoError(t, checkpointErr)
						require.NotNil(t, checkpoint)
						require.NotEmpty(t, checkpoint.ProviderStateJSON)
						if from == models.ProviderAnthropic {
							require.Contains(t, checkpoint.ProviderStateJSON, "ANTHROPIC_NATIVE_SUMMARY")
							require.Contains(t, checkpoint.ProviderStateJSON, "SOURCE_FINAL")
						}
					}
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
					if mode == "summary_source" || (from == models.ProviderAnthropic && mode == "native_source") {
						// Only recent executions are loaded; older facts must come from the checkpoint.
						req.ChatHistory = history[1:]
					}
					req.ContextTokenEstimate = 0
					req.DisableNativeCompaction = true
					if mode == "summary" {
						req.ContextTokenEstimate = 190000
					}
					fresh := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err = fresh.callProviderWithCompaction(fresh.providerAdapters[to], req)
					require.NoError(t, err)
					require.Equal(t, 1, targetCalls)
					if from == models.ProviderAnthropic && mode == "native_source" {
						saved, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
						require.NoError(t, err)
						require.Empty(t, saved.ProviderStateJSON)
						require.Equal(t, target.ID, saved.ModelConfigID)
						require.Equal(t, "provider_recovery", saved.Strategy)
					}
					if mode == "summary_source" {
						require.Equal(t, 1, summaries, "switch must reuse the source summary")
					} else if mode == "summary" || (mode == "native_source" && from == models.ProviderOpenAI && to != models.ProviderOpenAI) {
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
					if mode == "summary_source" {
						req.ChatHistory = history[1:]
					}
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

func TestConnectedRepeatedSummaryPreservesSavedReferences(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible} {
		for _, phase := range []string{"preturn", "provider_overflow"} {
			t.Run(string(provider)+"/"+phase, func(t *testing.T) {
				t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
				ctx := compactionTestContext()
				stores := teststores.New(t).Stores
				task := &models.Task{ProjectID: "default", Title: "references", Category: models.CategoryBacklog, Status: models.StatusCompleted}
				require.NoError(t, stores.Tasks.Create(ctx, task))
				model := "gpt-5.3-codex"
				if provider == models.ProviderAnthropic {
					model = "claude-opus-5-5"
				} else if provider == models.ProviderOpenAICompatible {
					model = "fixture"
				}
				summaries, generations := 0, 0
				roundNeedsSummary, overflowSent := false, false
				server := connectedModelServer(t, "http", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					text := string(raw)
					if strings.Contains(text, localContextCompactionInstruction) || strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
						summaries++
						compactionServerReply(w, provider, "SUMMARY_WITHOUT_REFERENCES", "")
						return
					}
					if phase == "provider_overflow" && roundNeedsSummary && !overflowSent {
						overflowSent = true
						compactionServerError(w, 400, "context_length_exceeded", "context length exceeded")
						return
					}
					generations++
					if !strings.Contains(text, "/uploads/saved-report.pdf") {
						t.Errorf("request missing %s", "/uploads/saved-report.pdf")
					}
					if !strings.Contains(text, "SAVED_KNOWLEDGE_INDEX") {
						t.Errorf("request missing %s", "SAVED_KNOWLEDGE_INDEX")
					}
					compactionServerReply(w, provider, "DONE", "")
				}))
				defer server.Close()
				oldAI, oldAnth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
				openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
				defer func() { openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldAI, oldAnth }()
				agent := models.LLMConfig{Name: "saved references", Provider: provider, Model: model, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", BaseURL: server.URL + "/v1", ContextWindow: 200000, DisableNativeCompaction: true, PresetSlug: "vllm"}
				require.NoError(t, stores.LLMConfigs.Create(ctx, &agent))
				old := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "OLD"}
				require.NoError(t, stores.Executions.Create(ctx, &old))
				sourceCtx := attachmentcontext.With(ctx, "/uploads/saved-report.pdf")
				sourceCtx = managedcontext.With(sourceCtx, "SAVED_KNOWLEDGE_INDEX")
				savedHistory := restoreCompactedApplicationContext(llmcontracts.AgentRequest{Ctx: sourceCtx}, buildCompactedReplacementHistory(nil, "OLD_SUMMARY"))
				require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(ctx, models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: task.ID, ModelConfigID: agent.ID, CompatibilityKey: providerCompatibilityKey(agent), SourceExecutionID: old.ID, Strategy: "local_summary", History: savedHistory, Summary: "OLD_SUMMARY"}))
				for round := 0; round < 3; round++ {
					history, err := stores.Executions.ListByTaskChronological(ctx, task.ID)
					require.NoError(t, err)
					active := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: fmt.Sprintf("FOLLOWUP_%d", round)}
					require.NoError(t, stores.Executions.Create(ctx, &active))
					req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: active.ID, Agent: agent, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, Message: active.PromptSent, ChatHistory: history, DisableNativeCompaction: true}
					roundNeedsSummary, overflowSent = round < 2, false
					if round < 2 && phase == "preturn" {
						req.ContextTokenEstimate = 190000
					}
					svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
					result, err := svc.callProviderWithCompaction(svc.providerAdapters[provider], req)
					require.NoError(t, err)
					require.NoError(t, stores.Executions.Complete(ctx, active.ID, models.ExecCompleted, result.Output, "", 0, 0))
					checkpoint, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
					require.NoError(t, err)
					require.NotNil(t, checkpoint)
					encoded, err := json.Marshal(checkpoint.History)
					require.NoError(t, err)
					require.Contains(t, string(encoded), "/uploads/saved-report.pdf")
					require.Contains(t, string(encoded), "SAVED_KNOWLEDGE_INDEX")
				}
				require.Equal(t, 2, summaries)
				require.Equal(t, 3, generations)
			})
		}
	}
}

// Return a compaction result followed by a tool request, so the test observes
// context both during continuation and after durable checkpoint restoration.
func connectedAnthropicLifecycleReply(w http.ResponseWriter, outcome string, steered bool, tool, path string) {
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(v any) { raw, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", raw) }
	emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "native", "usage": map[string]int{"input_tokens": 30}}})
	if outcome != "none" {
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "compaction"}})
		if outcome != "null" {
			summary := "SAVED_NATIVE_ANTHROPIC"
			if steered {
				summary += " STEER_NATIVE"
			}
			emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "compaction_delta", "content": summary}})
		}
		emit(map[string]any{"type": "content_block_stop", "index": 0})
	}
	emit(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}})
	emit(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "FINAL_ANTHROPIC"}})
	emit(map[string]any{"type": "content_block_stop", "index": 1})
	stop := "end_turn"
	if tool != "" {
		stop = "tool_use"
		key := "file_path"
		args, _ := json.Marshal(map[string]string{key: path})
		emit(map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "tool_use", "id": "post_compaction_tool", "name": tool}})
		emit(map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
		emit(map[string]any{"type": "content_block_stop", "index": 2})
	}
	emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 10}})
	emit(map[string]any{"type": "message_stop"})
}
