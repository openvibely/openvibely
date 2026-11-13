package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/internal/llm/recovery"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/retrytest"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

func TestResponsesSummaryMessagesGroupsParallelCalls(t *testing.T) {
	var input []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(`[
		{"type":"function_call","call_id":"a","name":"read_file","arguments":"{\"path\":\"a\"}"},
		{"type":"reasoning","summary":[]},
		{"type":"function_call","call_id":"b","name":"read_file","arguments":"{\"path\":\"b\"}"},
		{"type":"function_call_output","call_id":"b","output":"file B"},
		{"type":"function_call_output","call_id":"a","output":"file A"},
		{"type":"function_call","call_id":"c","name":"read_file","arguments":"{}"},
		{"type":"function_call_output","call_id":"c","output":"file C"}
	]`), &input))
	messages := ResponsesSummaryMessages(input)
	require.Len(t, messages, 5)
	require.Equal(t, "assistant", messages[0].Role)
	require.Len(t, messages[0].ToolCalls, 2)
	require.Equal(t, "a", messages[0].ToolCalls[0].ID)
	require.Equal(t, "b", messages[0].ToolCalls[1].ID)
	require.Equal(t, "read_file", messages[0].ToolCalls[0].Function.Name)
	require.JSONEq(t, `{"path":"a"}`, messages[0].ToolCalls[0].Function.Arguments)
	require.JSONEq(t, `{"path":"b"}`, messages[0].ToolCalls[1].Function.Arguments)
	require.Equal(t, "tool", messages[1].Role)
	require.Equal(t, "b", messages[1].ToolCallID)
	require.Equal(t, "file B", messages[1].Content)
	require.Equal(t, "tool", messages[2].Role)
	require.Equal(t, "a", messages[2].ToolCallID)
	require.Equal(t, "file A", messages[2].Content)
	require.Equal(t, "assistant", messages[3].Role)
	require.Len(t, messages[3].ToolCalls, 1, "a later tool round must remain separate")
	require.Equal(t, "c", messages[3].ToolCalls[0].ID)
	require.Equal(t, "tool", messages[4].Role)
	require.Equal(t, "c", messages[4].ToolCallID)
}

func TestOrdinaryFollowupRetainsSkillResults(t *testing.T) {
	for _, transport := range []string{"http", "websocket", "completions"} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancelled=%v", transport, cancelled), func(t *testing.T) {
				ctx := retrytest.Context()
				stores := teststores.New(t).Stores
				task := &models.Task{ProjectID: "default", Title: "Replay", Category: models.CategoryChat, Status: models.StatusCompleted}
				require.NoError(t, stores.Tasks.Create(ctx, task))
				phase, step := 0, 0
				var followup string
				response := func(raw []byte) []string {
					if phase == 1 {
						followup = string(raw)
					}
					tool := phase == 0 && step == 0
					step++
					if transport == "completions" {
						if tool {
							return []string{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"skill-call","type":"function","function":{"name":"skill_view","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`}
						}
						return []string{`{"choices":[{"delta":{"content":"Done"},"finish_reason":"stop"}]}`}
					}
					if tool {
						return []string{`{"type":"response.output_item.done","output_index":0,"item":{"id":"empty-text","type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}}`, `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"skill-call","name":"skill_view","arguments":"{}"}}`, `{"type":"response.completed","response":{"id":"r1","status":"completed"}}`}
					}
					return []string{`{"type":"response.completed","response":{"id":"r2","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}}`}
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
						if transport != "websocket" {
							http.Error(w, "no", 426)
							return
						}
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer conn.CloseNow()
						for {
							_, raw, err := conn.Read(r.Context())
							if err != nil {
								return
							}
							for _, event := range response(raw) {
								if conn.Write(r.Context(), websocket.MessageText, []byte(event)) != nil {
									return
								}
							}
						}
					}
					var raw json.RawMessage
					if json.NewDecoder(r.Body).Decode(&raw) != nil {
						http.Error(w, "bad", 400)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range response(raw) {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
					if transport == "completions" {
						fmt.Fprint(w, "data: [DONE]\n\n")
					}
				}))
				defer srv.Close()
				old := openaiclient.OpenAIAPIBaseURL
				openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
				defer func() { openaiclient.OpenAIAPIBaseURL = old }()
				var history []models.Execution
				for phase = 0; phase < 2; phase++ {
					step = 0
					exec := &models.Execution{TaskID: task.ID, PromptSent: "edit document", Status: models.ExecRunning}
					require.NoError(t, stores.Executions.Create(ctx, exec))
					callCtx, cancel := context.WithCancel(ctx)
					callCtx = llmcontracts.WithRuntimeTools(callCtx, &llmcontracts.RuntimeTools{
						Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view", Description: "Read skill", Parameters: json.RawMessage(`{"type":"object"}`), Access: llmcontracts.RuntimeToolAccessRead}},
						Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
							if cancelled && phase == 0 {
								cancel()
							}
							return "UNIQUE_SKILL_INSTRUCTIONS", true, false, nil
						},
					})
					agent := models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", Model: "gpt-test"}
					a := New(nil, stores.Executions, nil, nil)
					var err error
					if phase == 0 && transport == "completions" {
						_, _, _, err = a.CallCompletionsStreaming(callCtx, exec.PromptSent, nil, agent, exec.ID, t.TempDir(), "", nil)
					} else if phase == 0 {
						_, _, _, err = a.CallStreaming(callCtx, exec.PromptSent, nil, agent, exec.ID, "default", t.TempDir(), "", nil)
					} else if transport == "completions" {
						_, _, err = a.CallCompletionsChatStreaming(callCtx, exec.PromptSent, nil, agent, exec.ID, history, "", true, models.ChatModeOrchestrate, t.TempDir(), nil)
					} else {
						_, _, err = a.CallChatStreaming(callCtx, exec.PromptSent, nil, agent, exec.ID, "default", "", history, "", true, models.ChatModeOrchestrate, t.TempDir(), nil)
					}
					cancel()
					if cancelled && phase == 0 {
						require.Error(t, err)
						var failure recovery.Failure
						require.True(t, errors.As(err, &failure))
						rows, snapshotErr := failure.RecoveryRows()
						require.NoError(t, snapshotErr)
						raw, snapshotErr := json.Marshal(rows)
						require.NoError(t, snapshotErr)
						require.Contains(t, string(raw), "UNIQUE_SKILL_INSTRUCTIONS")
					} else {
						require.NoError(t, err)
					}
					saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
					require.NoError(t, err)
					require.NotEmpty(t, saved[exec.ID])
					if phase == 0 {
						require.Contains(t, saved[exec.ID][0].TranscriptJSON, "UNIQUE_SKILL_INSTRUCTIONS")
						if transport != "completions" {
							assertEmptyResponsesText(t, saved[exec.ID][0].TranscriptJSON)
						}
					}
					exec.ReplayMessages = saved[exec.ID]
					exec.Status = models.ExecCompleted
					history = append(history, *exec)
				}
				require.Contains(t, followup, "UNIQUE_SKILL_INSTRUCTIONS")
				require.Contains(t, followup, "skill-call")
				if transport != "completions" {
					assertEmptyResponsesText(t, followup)
				}
			})
		}
	}
}

func TestReplaySurvivesTransportSwitchAndNativeCompaction(t *testing.T) {
	saved := `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"},{"type":"message","role":"user","content":"edit"},{"type":"function_call","call_id":"skill","name":"skill_view","arguments":"{}"},{"type":"function_call_output","call_id":"skill","output":"instructions"}]}`
	history := []models.Execution{{PromptSent: "stale", Output: "stale", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: saved}}}}
	native := HistoryInputItems(history)
	raw, err := json.Marshal(native)
	require.NoError(t, err)
	require.Contains(t, string(raw), "opaque")
	require.NotContains(t, string(raw), "stale")
	completions := buildCompletionsHistory(history)
	require.Len(t, completions, 3)
	require.Equal(t, "instructions", completions[2].Content)
	require.Equal(t, "skill", completions[2].ToolCallID)
	raw, err = json.Marshal(completions)
	require.NoError(t, err)
	history[0].ReplayMessages[0].TranscriptJSON = string(raw)
	restored := HistoryInputItems(history)
	raw, err = json.Marshal(restored)
	require.NoError(t, err)
	require.Contains(t, string(raw), "instructions")
	require.Contains(t, string(raw), "function_call_output")
	require.Greater(t, EstimateHistoryTokens(history), 0)
}

func TestMainChatSavesOnlyNewExchange(t *testing.T) {
	ctx := retrytest.Context()
	stores := teststores.New(t).Stores
	turn := 0
	serve := func(raw []byte) string {
		for i := 0; i <= turn; i++ {
			require.Contains(t, string(raw), fmt.Sprintf("MAIN_CHAT_%d", i))
		}
		return `{"type":"response.completed","response":{"id":"done","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			for {
				_, raw, err := c.Read(r.Context())
				if err != nil {
					return
				}
				if c.Write(r.Context(), websocket.MessageText, []byte(serve(raw))) != nil {
					return
				}
			}
		}
		var raw json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", serve(raw))
	}))
	defer srv.Close()
	old := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = old }()
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", Model: "gpt-test"}
	workDir := t.TempDir()
	var history []models.Execution
	for turn = 0; turn < 3; turn++ {
		task := models.Task{ProjectID: "default", Title: fmt.Sprintf("chat-%d", turn), Category: models.CategoryChat, Status: models.StatusCompleted}
		require.NoError(t, stores.Tasks.Create(ctx, &task))
		e := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: fmt.Sprintf("MAIN_CHAT_%d", turn)}
		require.NoError(t, stores.Executions.Create(ctx, &e))
		_, _, err := New(nil, stores.Executions, nil, nil).CallChatStreaming(ctx, e.PromptSent, nil, agent, e.ID, "default", "", history, "", false, models.ChatModeOrchestrate, workDir, nil)
		require.NoError(t, err)
		saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{e.ID})
		require.NoError(t, err)
		require.NotEmpty(t, saved[e.ID])
		text := saved[e.ID][0].FullTranscriptJSON()
		require.Contains(t, text, e.PromptSent)
		for i := 0; i < turn; i++ {
			require.NotContains(t, text, fmt.Sprintf("MAIN_CHAT_%d", i), "ordinary Chat sends must not save earlier exchanges again")
		}
		e.Status = models.ExecCompleted
		e.ReplayMessages = saved[e.ID]
		history = append(history, e)
	}
}

// Inspect decoded JSON so an absent text field cannot pass as an empty Go string.
func assertEmptyResponsesText(t *testing.T, raw string) {
	t.Helper()
	var value any
	require.NoError(t, json.Unmarshal([]byte(raw), &value))
	found := false
	var visit func(any)
	visit = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if v["type"] == "message" && v["id"] == "empty-text" {
				found = true
				require.Equal(t, "assistant", v["role"])
				require.Equal(t, []any{map[string]any{"type": "output_text", "text": ""}}, v["content"])
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(value)
	require.True(t, found, "empty assistant output must survive persistence and replay")
}
