package service

import (
	"context"
	"encoding/json"
	"fmt"
	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/pkg/attachment"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSkillInstallationLiveAnswerAllProviders(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderOpenAICompatible, models.ProviderAnthropic} {
		for _, late := range []bool{false, true} {
			for _, allow := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/allow=%v/late=%v", provider, allow, late), func(t *testing.T) {
					env := teststores.New(t)
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					var requestCount atomic.Int32
					originalPromptCount := 0
					expected := "installed fixture"
					if !allow {
						expected = "human declined"
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != http.MethodPost {
							http.Error(w, "HTTP only", http.StatusUpgradeRequired)
							return
						}
						var body json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							return
						}
						requests := int(requestCount.Add(1))
						if requests == 1 {
							originalPromptCount = strings.Count(string(body), "use skill")
						} else {
							if !strings.Contains(string(body), "earlier_context") || strings.Count(string(body), "use skill") != originalPromptCount {
								t.Errorf("lost history or repeated original prompt: %s", body)
							}
						}
						if (requests > 1) && (!strings.Contains(string(body), expected) || strings.Contains(string(body), "__openvibely_deferred")) {
							t.Errorf("missing tool result: %s", body)
						}
						if late && requests > 1 {
							assertWireSkillResult(t, body, expected)
							wire := string(body)
							if !strings.Contains(wire, "call_skill") || strings.Contains(wire, contracts.SkillInputResultPrefix) || strings.Contains(wire, contracts.PendingSkillInputPrefix) {
								t.Errorf("not an original tool continuation: %s", body)
							}
						}
						w.Header().Set("Content-Type", "text/event-stream")
						var events []string
						switch provider {
						case models.ProviderOpenAI:
							if requests == 1 {
								events = []string{`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_first","name":"read_file","arguments":"{\"file_path\":\"fixture\"}"}}`, `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_skill","name":"skill_view","arguments":"{}"}}`, `{"type":"response.completed","response":{"status":"completed","model":"gpt-5.5"}}`}
							} else {
								events = []string{`{"type":"response.output_text.delta","delta":"done"}`, `{"type":"response.completed","response":{"status":"completed","model":"gpt-5.5"}}`}
							}
						case models.ProviderOpenAICompatible:
							if requests == 1 {
								events = []string{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_first","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"fixture\"}"}},{"index":1,"id":"call_skill","type":"function","function":{"name":"skill_view","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`}
							} else {
								events = []string{`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`}
							}
							events = append(events, "[DONE]")
						case models.ProviderAnthropic:
							events = []string{`{"type":"message_start","message":{"id":"msg","model":"claude-sonnet-5","usage":{"input_tokens":10}}}`}
							if requests == 1 {
								events = append(events, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_first","name":"read_file","input":{"file_path":"fixture"}}}`, `{"type":"content_block_stop","index":0}`, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_skill","name":"skill_view","input":{}}}`, `{"type":"content_block_stop","index":1}`, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`)
							} else {
								events = append(events, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
							}
							events = append(events, `{"type":"message_stop"}`)
						}
						for _, event := range events {
							fmt.Fprintf(w, "data: %s\n\n", event)
						}
					}))
					defer server.Close()
					oldOpenAI, oldAnthropic := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
					openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
					defer func() { openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldOpenAI, oldAnthropic }()
					model := models.LLMConfig{Name: "fixture", Provider: provider, Model: "gpt-5.5", APIKey: "fixture", AuthMethod: models.AuthMethodAPIKey, MaxTokens: 4096, ContextWindow: 128000}
					if provider == models.ProviderAnthropic {
						model.Model = "claude-sonnet-5"
					}
					if provider == models.ProviderOpenAICompatible {
						model.BaseURL = server.URL + "/v1"
						model.Transport = "chat_completions"
						model.PresetSlug = "vllm"
					}
					if err := env.LLMConfigs.Create(ctx, &model); err != nil {
						t.Fatal(err)
					}
					task := models.Task{ProjectID: "default", Title: "install", Category: models.CategoryActive, Status: models.StatusRunning}
					if err := env.Tasks.Create(ctx, &task); err != nil {
						t.Fatal(err)
					}
					execution := models.Execution{TaskID: task.ID, AgentConfigID: model.ID, Status: models.ExecRunning, PromptSent: "use skill"}
					if err := env.Executions.Create(ctx, &execution); err != nil {
						t.Fatal(err)
					}
					rt := &contracts.RuntimeTools{Definitions: []contracts.RuntimeToolDefinition{{Name: "read_file", Description: "Fixture read", Parameters: json.RawMessage(`{"type":"object"}`), Access: contracts.RuntimeToolAccessRead}, {Name: "skill_view", Description: "Read skill", Parameters: json.RawMessage(`{"type":"object"}`), Access: contracts.RuntimeToolAccessRead}}, Executor: func(toolCtx context.Context, name string, _ json.RawMessage) (string, bool, bool, error) {
						if name == "read_file" {
							out, err := attachment.StoreToolImage(toolCtx, attachment.ToolImage{Name: "DURABLE_SIBLING_RESULT", MediaType: "image/png", Data: []byte("fixture image")})
							return out, true, err != nil, err
						}
						if name != "skill_view" {
							return "", false, false, nil
						}
						_, ask := skilldeps.ApprovalFromContext(toolCtx)
						if ask == nil {
							return "missing hook", true, true, nil
						}
						err := ask(toolCtx, skilldeps.InstallationRequest{Identity: skilldeps.SkillIdentity{ProjectID: "default", Source: "project", Skill: "fixture"}, Platform: "fixture", Dependencies: []skilldeps.Dependency{{Name: "fixture", Version: "1"}}})
						if err != nil {
							return err.Error(), true, true, nil
						}
						return "installed fixture", true, false, nil
					}}
					s := NewLLMService(env.LLMConfigs, env.Executions, env.Tasks, env.Projects, env.Schedules, env.Attachments)
					asked, answer := make(chan struct{}), make(chan bool)
					s.SetSkillInstallationPrompt(func(ctx context.Context, _ contracts.AgentRequest, _ skilldeps.InstallationRequest) error {
						if id := contracts.ToolCallIDFromContext(ctx); id != "call_skill" {
							t.Errorf("question received call ID %q", id)
						}
						saved, saveErr := env.Executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
						deadline := time.Now().Add(2 * time.Second)
						for saveErr == nil && (len(saved[execution.ID]) == 0 || !strings.Contains(saved[execution.ID][0].TranscriptJSON, "DURABLE_SIBLING_RESULT")) && time.Now().Before(deadline) {
							time.Sleep(time.Millisecond)
							saved, saveErr = env.Executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
						}
						if saveErr != nil || len(saved[execution.ID]) == 0 || !strings.Contains(saved[execution.ID][0].TranscriptJSON, "DURABLE_SIBLING_RESULT") {
							t.Errorf("completed sibling missing while approval waits: %v %v", saved, saveErr)
						}

						if saveErr != nil || len(saved[execution.ID]) == 0 || !strings.Contains(saved[execution.ID][0].TranscriptJSON, "skill_view") {
							t.Errorf("pending tool call must be durable before displaying question: rows=%v err=%v", saved, saveErr)
						}
						if len(saved[execution.ID]) > 0 && !strings.Contains(saved[execution.ID][0].TranscriptJSON, "Zml4dHVyZSBpbWFnZQ==") {
							t.Errorf("sibling image not durable: %v", saved)
						}
						close(asked)
						if late {
							return fmt.Errorf("%squestion", contracts.PendingSkillInputPrefix)
						}
						select {
						case allow := <-answer:
							if !allow {
								return fmt.Errorf("human declined")
							}
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})

					prior := models.Execution{PromptSent: "earlier_context", Output: "earlier answer", Status: models.ExecCompleted}
					req := contracts.AgentRequest{ChatHistory: []models.Execution{prior}, Ctx: contracts.WithRuntimeTools(ctx, rt), Agent: model, ExecID: execution.ID, ProjectID: "default", Message: "use skill", Operation: contracts.OperationStreaming, Followup: true, WorkDir: t.TempDir()}
					adapter, _ := s.adapterFor(provider)
					if late {
						_, firstErr := s.callProviderWithCompaction(adapter, req)
						if firstErr == nil {
							t.Fatal("unanswered tool must end the attempt")
						}
						if requestCount.Load() != 1 {
							t.Fatal("model sampled after unanswered question")
						}
						source := execution
						execution = models.Execution{TaskID: task.ID, AgentConfigID: model.ID, Status: models.ExecRunning}
						if err := env.Executions.Create(ctx, &execution); err != nil {
							t.Fatal(err)
						}
						s = NewLLMService(env.LLMConfigs, env.Executions, env.Tasks, env.Projects, env.Schedules, env.Attachments)
						adapter, _ = s.adapterFor(provider)
						req.ExecID = execution.ID
						req.ChatHistory = append(req.ChatHistory, source)
						encoded, _ := json.Marshal(map[string]any{"source_execution_id": source.ID, "question_id": "question", "arguments": json.RawMessage(`{}`), "output": expected, "is_error": !allow})
						s.skillInstallationResult = func(context.Context, contracts.AgentRequest) (string, error) {
							return contracts.SkillInputResultPrefix + string(encoded), nil
						}
					}

					done := make(chan error, 1)
					go func() { _, e := s.callProviderWithCompaction(adapter, req); done <- e }()
					var e error
					if !late {
						select {
						case <-asked:
						case <-ctx.Done():
							t.Fatal("no installation question")
						}
						if requestCount.Load() != 1 {
							t.Fatal("provider advanced before answer")
						}
						running, e := env.Executions.GetByID(ctx, execution.ID)
						if e != nil || running.Status != models.ExecRunning {
							t.Fatalf("changed execution status: %+v %v", running, e)
						}
						answer <- allow
					}
					select {
					case e = <-done:
						if e != nil {
							t.Fatal(e)
						}
					case <-ctx.Done():
						t.Fatal("provider did not continue")
					}
					wantRequests := int32(2)

					if requestCount.Load() != wantRequests {
						t.Fatalf("model requests=%d", requestCount.Load())
					}
					replay, e := env.Executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
					if e != nil || len(replay[execution.ID]) == 0 || (!late && !strings.Contains(replay[execution.ID][0].TranscriptJSON, expected)) {
						t.Fatalf("normal transcript missing tool result: %+v %v", replay, e)
					}
				})
			}
		}
	}
}

func TestCompleteSkillToolTranscript(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"responses", `{"responses_input":[{"type":"message","role":"user","content":"original prompt"},{"type":"reasoning","encrypted_content":"keep reasoning"},{"type":"function_call","name":"skill_view","call_id":"original","arguments":"{}"},{"type":"function_call_output","call_id":"original","output":"skill_view awaiting user input: question"},{"type":"message","role":"assistant","content":"later assistant message"}]}`},
		{"completions", `[{"role":"user","content":"original prompt"},{"role":"assistant","reasoning_content":"keep reasoning","tool_calls":[{"id":"original","type":"function","function":{"name":"skill_view","arguments":"{}"}}]},{"role":"tool","tool_call_id":"original","content":"skill_view awaiting user input: question"},{"role":"assistant","content":"later assistant message"}]`},
		{"anthropic", `{"anthropic_execution_environment":"keep environment","messages":[{"role":"user","content":"original prompt"},{"role":"assistant","content_blocks":[{"type":"thinking","thinking":"keep reasoning","signature":"keep signature"},{"type":"tool_use","id":"original","name":"skill_view","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"original","content":"skill_view awaiting user input: question","is_error":true}]},{"role":"assistant","content":"later assistant message"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, denied := range []bool{false, true} {
				result, found, err := completeSkillToolTranscript(tc.raw, "question", json.RawMessage(`{}`), "actual tool result", denied)
				if err != nil || !found {
					t.Fatalf("%v %v", found, err)
				}
				for _, keep := range []string{"original prompt", "original", "keep reasoning", "actual tool result"} {
					if !strings.Contains(result, keep) {
						t.Errorf("lost %q: %s", keep, result)
					}
				}
				if !strings.Contains(result, "later assistant message") || strings.Contains(result, "awaiting user input") {
					t.Errorf("changed unrelated messages: %s", result)
				}
				// Reprocessing a saved answer must be idempotent, including after result
				// persistence succeeded but the next model request failed.
				again, found, err := completeSkillToolTranscript(result, "question", json.RawMessage(`{}`), "actual tool result", denied)
				if err != nil || !found || again != result {
					t.Fatalf("retry changed result: %s %v", again, err)
				}
			}
		})
	}
	for _, raw := range []string{
		`[{"role":"assistant","tool_calls":[{"id":"one","function":{"name":"skill_view","arguments":"{}"}},{"id":"two","function":{"name":"skill_view","arguments":"{}"}}]}]`,
	} {
		if _, _, err := completeSkillToolTranscript(raw, "question", json.RawMessage(`{}`), "result", false); err == nil {
			t.Fatal("accepted ambiguous exchange")
		}
	}
}

func assertWireSkillResult(t *testing.T, raw json.RawMessage, expected string) {
	t.Helper()
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, child := range v {
				visit(child)
			}
		case map[string]any:
			if v["type"] == "function_call_output" && v["call_id"] == "call_skill" && v["output"] == expected {
				found = true
			}
			if v["role"] == "tool" && v["tool_call_id"] == "call_skill" && v["content"] == expected {
				found = true
			}
			if v["type"] == "tool_result" && v["tool_use_id"] == "call_skill" && v["content"] == expected {
				found = true
			}
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(payload)
	if !found {
		t.Errorf("result was not attached to original tool ID: %s", raw)
	}
}

func TestSkillContinuationWaitsForSiblingAnswerWithoutLosingFirstResult(t *testing.T) {
	raw := `{"responses_input":[{"type":"function_call","name":"skill_view","call_id":"first","arguments":"{}"},{"type":"function_call","name":"skill_view","call_id":"second","arguments":"{}"},{"type":"function_call_output","call_id":"first","output":"skill_view awaiting user input: one"},{"type":"function_call_output","call_id":"second","output":"skill_view awaiting user input: two"}]}`
	partial, found, err := completeSkillToolTranscript(raw, "one", json.RawMessage(`{}`), "first result", false)
	if !found || err == nil || !strings.Contains(partial, "first result") {
		t.Fatalf("lost first answer: %s %v", partial, err)
	}
	completed, found, err := completeSkillToolTranscript(partial, "two", json.RawMessage(`{}`), "second result", false)
	if !found || err != nil || !strings.Contains(completed, "first result") || !strings.Contains(completed, "second result") || strings.Contains(completed, contracts.PendingSkillInputPrefix) {
		t.Fatalf("invalid sibling continuation: %s %v", completed, err)
	}
}

func TestSkillContinuationPreservesOtherJSONItems(t *testing.T) {
	const pending = `{"type":"function_call_output","call_id":"skill","output":"skill_view awaiting user input: question"}`
	const raw = `{"responses_input":[{"type":"function_call","call_id":"image","name":"view_image","arguments":"{}"},{"type":"function_call","call_id":"skill","name":"skill_view","arguments":"{}"},{"type":"function_call","call_id":"other","name":"read_file","arguments":"{\"file_path\":\"fixture\"}"},{"type":"function_call_output","call_id":"image","output":"loaded"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,KEEP"}]},{"type":"function_call_output","call_id":"other","output":"KEEP sibling result"},` + pending + `,{"type":"message","role":"user","content":"KEEP steering"},{"type":"message","role":"assistant","content":"KEEP assistant"}]}`
	for _, denied := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			input := raw
			if missing {
				input = strings.Replace(raw, ","+pending, "", 1)
			}
			result, found, err := completeSkillToolTranscript(input, "question", json.RawMessage(`{}`), "actual result", denied)
			if err != nil || !found {
				t.Fatalf("missing=%v denied=%v: %v", missing, denied, err)
			}
			var before, after map[string][]json.RawMessage
			if err = json.Unmarshal([]byte(input), &before); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(result), &after); err != nil {
				t.Fatal(err)
			}
			stripResult := func(items []json.RawMessage) []any {
				var preserved []any
				for _, item := range items {
					var m map[string]any
					if err := json.Unmarshal(item, &m); err != nil {
						t.Fatal(err)
					}
					if m["type"] == "function_call_output" && m["call_id"] == "skill" {
						continue
					}
					preserved = append(preserved, m)
				}
				return preserved
			}
			want, _ := json.Marshal(stripResult(before["responses_input"]))
			got, _ := json.Marshal(stripResult(after["responses_input"]))
			if string(want) != string(got) {
				t.Fatalf("changed unrelated JSON: want %s got %s", want, got)
			}
			expectedLen := len(before["responses_input"])
			if missing {
				expectedLen++
			}
			if len(after["responses_input"]) != expectedLen || !strings.Contains(result, `"output":"actual result"`) {
				t.Fatalf("wrong output insertion: %s", result)
			}
		}
	}
}

func TestSkillContinuationPreservesLaterReplayAndHistory(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	task := models.Task{ProjectID: "default", Title: "preserve history", Category: models.CategoryActive, Status: models.StatusRunning}
	if err := env.Tasks.Create(ctx, &task); err != nil {
		t.Fatal(err)
	}
	source := models.Execution{TaskID: task.ID, Status: models.ExecFailed}
	current := models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	for _, ex := range []*models.Execution{&source, &current} {
		if err := env.Executions.Create(ctx, ex); err != nil {
			t.Fatal(err)
		}
	}
	tail := models.ExecutionReplayMessage{TranscriptJSON: `[{"role":"user","content":"later saved message"}]`}
	replay := []models.ExecutionReplayMessage{{TranscriptJSON: `[{"role":"assistant","tool_calls":[{"id":"skill","function":{"name":"skill_view","arguments":"{}"}}]},{"role":"tool","tool_call_id":"skill","content":"skill_view awaiting user input: question"}]`}, tail}
	if err := env.Executions.ReplaceReasoningReplay(ctx, source.ID, "", replay); err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(map[string]any{"source_execution_id": source.ID, "question_id": "question", "arguments": json.RawMessage(`{}`), "output": "skill result"})
	later := models.Execution{ID: "later", PromptSent: "later user message", Output: "later assistant answer"}
	svc := NewLLMService(env.LLMConfigs, env.Executions, env.Tasks, env.Projects, env.Schedules, env.Attachments)
	result, err := svc.completeInstallationTranscript(contracts.AgentRequest{Ctx: ctx, ExecID: current.ID, ProjectID: "default", Message: contracts.SkillInputResultPrefix + string(envelope), ChatHistory: []models.Execution{source, later}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChatHistory) != 2 || result.ChatHistory[1].ID != later.ID || result.ChatHistory[1].Output != later.Output {
		t.Fatalf("lost later history: %+v", result.ChatHistory)
	}
	saved, err := env.Executions.ReplayMessagesByExecutionIDs(ctx, []string{source.ID})
	if err != nil || len(saved[source.ID]) != 2 || saved[source.ID][1].TranscriptJSON != tail.TranscriptJSON {
		t.Fatalf("lost saved replay: %+v %v", saved, err)
	}
}

// A process can stop before any timeout marker is written. The saved question's
// provider ID must identify the right call even when earlier arguments match.
func TestSkillContinuationUsesSavedCallIDAfterRestart(t *testing.T) {
	fixtures := map[string]string{
		"responses":  `{"responses_input":[{"type":"function_call","name":"skill_view","call_id":"old","arguments":"{}"},{"type":"function_call_output","call_id":"old","output":"earlier denial"},{"type":"function_call","name":"skill_view","call_id":"new","arguments":"{}"}]}`,
		"compatible": `[{"role":"assistant","tool_calls":[{"id":"old","function":{"name":"skill_view","arguments":"{}"}}]},{"role":"tool","tool_call_id":"old","content":"earlier denial"},{"role":"assistant","tool_calls":[{"id":"new","function":{"name":"skill_view","arguments":"{}"}}]}]`,
		"anthropic":  `{"messages":[{"role":"assistant","content_blocks":[{"type":"tool_use","id":"old","name":"skill_view","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"old","content":"earlier denial"}]},{"role":"assistant","content_blocks":[{"type":"tool_use","id":"new","name":"skill_view","input":{}}]}]}`,
	}
	for name, raw := range fixtures {
		t.Run(name, func(t *testing.T) {
			for _, denied := range []bool{false, true} {
				output := "installed instructions"
				if denied {
					output = "installation declined"
				}
				updated, found, err := completeSkillToolTranscript(raw, "question", json.RawMessage(`{}`), output, denied, "new")
				if err != nil || !found {
					t.Fatalf("complete exact call: %v", err)
				}
				if strings.Count(updated, "earlier denial") != 1 || strings.Count(updated, output) != 1 {
					t.Fatalf("changed wrong result: %s", updated)
				}
				repeated, found, err := completeSkillToolTranscript(updated, "question", json.RawMessage(`{}`), output, denied, "new")
				if err != nil || !found || repeated != updated {
					t.Fatalf("lost call identity: %s %v", repeated, err)
				}
				_, found, err = completeSkillToolTranscript(raw, "question", json.RawMessage(`{}`), output, denied, "unknown")
				if err != nil || found {
					t.Fatalf("unknown ID matched another call: %v", err)
				}
			}
		})
	}
}

func TestSkillContinuationInsertsCompletionsResultBeforeImages(t *testing.T) {
	const raw = `[{"role":"assistant","tool_calls":[{"id":"image","function":{"name":"view_image","arguments":"{}"}},{"id":"skill","function":{"name":"skill_view","arguments":"{}"}}]},{"role":"tool","tool_call_id":"image","content":"loaded"},{"role":"user","image_content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,KEEP"}}]},{"role":"user","content":"later steering"},{"role":"assistant","content":"later answer"}]`
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("denied=%t", denied), func(t *testing.T) {
			output := "skill instructions"
			if denied {
				output = "installation declined"
			}
			updated, found, err := completeSkillToolTranscript(raw, "question", json.RawMessage(`{}`), output, denied, "skill")
			if err != nil || !found {
				t.Fatalf("complete missing result: %v", err)
			}
			var before, after []json.RawMessage
			if err := json.Unmarshal([]byte(raw), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(updated), &after); err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before)+1 {
				t.Fatalf("unexpected item count: %s", updated)
			}
			var result map[string]any
			json.Unmarshal(after[2], &result)
			if result["role"] != "tool" || result["tool_call_id"] != "skill" || result["content"] != output {
				t.Fatalf("incorrect insertion: %s", updated)
			}
			remaining := append(append([]json.RawMessage(nil), after[:2]...), after[3:]...)
			for i := range before {
				var a, b any
				json.Unmarshal(before[i], &a)
				json.Unmarshal(remaining[i], &b)
				aa, _ := json.Marshal(a)
				bb, _ := json.Marshal(b)
				if string(aa) != string(bb) {
					t.Fatalf("changed unrelated item %d: %s", i, updated)
				}
			}
			again, found, err := completeSkillToolTranscript(updated, "question", json.RawMessage(`{}`), output, denied, "skill")
			if err != nil || !found || again != updated {
				t.Fatalf("existing result moved or duplicated: %s %v", again, err)
			}
		})
	}
}

func TestCompleteSkillToolTranscriptInterruptedSiblings(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"responses", `{"responses_input":[{"type":"function_call","name":"skill_view","call_id":"skill","arguments":"{}"},{"type":"function_call","name":"read_file","call_id":"missing","arguments":"{}"},{"type":"function_call","name":"read_file","call_id":"done","arguments":"{}"},{"type":"function_call_output","call_id":"done","output":"KEEP result"},{"type":"message","role":"user","content":"KEEP image"}]}`},
		{"completions", `[{"role":"assistant","tool_calls":[{"id":"skill","function":{"name":"skill_view","arguments":"{}"}},{"id":"missing","function":{"name":"read_file","arguments":"{}"}},{"id":"done","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"done","content":"KEEP result"},{"role":"user","content":"KEEP image"}]`},
		{"anthropic", `{"messages":[{"role":"assistant","content_blocks":[{"type":"tool_use","id":"skill","name":"skill_view","input":{}},{"type":"tool_use","id":"missing","name":"read_file","input":{}},{"type":"tool_use","id":"done","name":"read_file","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"done","content":"KEEP result"},{"type":"text","text":"KEEP image"}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, denied := range []bool{false, true} {
				result, found, err := completeSkillToolTranscript(tc.raw, "question", json.RawMessage(`{}`), "skill result", denied, "skill")
				if err != nil || !found {
					t.Fatalf("found=%v err=%v", found, err)
				}
				for _, keep := range []string{"KEEP result", "KEEP image", "skill result", "Tool execution was interrupted. Try again."} {
					if strings.Count(result, keep) != 1 {
						t.Fatalf("missing/duplicated %q: %s", keep, result)
					}
				}
				if strings.Index(result, "Tool execution was interrupted.") > strings.Index(result, "KEEP image") {
					t.Fatalf("tool reply follows image: %s", result)
				}
				if tc.name == "anthropic" && !strings.Contains(result, `"is_error":true`) {
					t.Fatal("missing error flag")
				}
				again, found, err := completeSkillToolTranscript(result, "question", json.RawMessage(`{}`), "skill result", denied, "skill")
				if err != nil || !found || again != result {
					t.Fatalf("not idempotent: %v %s", err, again)
				}
			}
		})
	}
}
