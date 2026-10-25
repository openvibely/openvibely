package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInterruptedToolReplayRepair(t *testing.T) {
	call := agenticMessage{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"checking","signature":"keep"},{"type":"tool_use","id":"a","name":"shell","input":{}},{"type":"tool_use","id":"b","name":"shell","input":{}}]`)}
	for _, tail := range []struct {
		name     string
		messages []agenticMessage
		missing  int
	}{
		{"single trailing round", nil, 2},
		{"next followup", []agenticMessage{{Role: "user", Content: "continue"}}, 2},
		{"partial parallel", []agenticMessage{{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"a","content":"real result"},{"type":"image","source":{"type":"url","url":"https://example.com/image.png"}}]`)}}, 1},
		{"complete", []agenticMessage{{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"a","content":"A"},{"type":"tool_result","tool_use_id":"b","content":"B"}]`)}}, 0},
	} {
		t.Run(tail.name, func(t *testing.T) {
			original := append([]agenticMessage{call}, tail.messages...)
			before, _ := json.Marshal(original)
			got := repairInterruptedToolCalls(original)
			require.Equal(t, got, repairInterruptedToolCalls(got))
			after, _ := json.Marshal(original)
			require.JSONEq(t, string(before), string(after), "must not mutate saved messages")
			require.Equal(t, call, got[0])
			blocks, err := contentBlocksJSON(got[1].Content)
			require.NoError(t, err)
			seen := map[string]int{}
			errors := 0
			for _, raw := range blocks {
				var b agenticBlock
				require.NoError(t, json.Unmarshal(raw, &b))
				if b.Type == "tool_result" {
					seen[b.ToolUseID]++
					if b.IsError {
						errors++
						require.Contains(t, string(b.Content), "outcome unknown")
					}
				}
			}
			require.Equal(t, map[string]int{"a": 1, "b": 1}, seen)
			require.Equal(t, tail.missing, errors)
			if tail.name == "partial parallel" {
				require.Contains(t, string(mustReplayJSON(t, got)), "real result")
				require.Contains(t, string(mustReplayJSON(t, got)), "image.png")
			}
			if tail.name == "next followup" {
				require.Contains(t, string(mustReplayJSON(t, got)), "continue")
			}
		})
	}
}
func mustReplayJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	require.NoError(t, e)
	return b
}

func TestSendAgenticRepairsRestoredToolCall(t *testing.T) {
	for _, recovery := range []string{"history", "recovery", "checkpoint"} {
		t.Run(fmt.Sprint(recovery), func(t *testing.T) {
			var request agenticRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&request)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			old := AnthropicAPIHost
			AnthropicAPIHost = server.URL
			defer func() { AnthropicAPIHost = old }()
			history := []Message{{Role: "assistant", ContentBlocks: json.RawMessage(`[{"type":"tool_use","id":"interrupted","name":"shell","input":{}}]`)}}
			client := NewWithAPIKey("test")
			client.History = history
			opts := &AgenticOptions{Model: "claude-test", DisableTools: true, OmitExecutionEnvironment: true, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
				t.Error("restored tool must not run")
				return "", false, nil
			}}
			if recovery == "recovery" {
				opts.RecoveryMessages = history
			}
			if recovery == "checkpoint" {
				client.History = nil
				opts.NativeCompactionStateJSON = `{"version":1,"messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary"},{"type":"tool_use","id":"interrupted","name":"shell","input":{}}]}]}`
			}
			_, err := client.SendAgentic(context.Background(), "followup", opts)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(request.Messages), 2)
			blocks, err := contentBlocksJSON(request.Messages[1].Content)
			require.NoError(t, err)
			var b agenticBlock
			require.NoError(t, json.Unmarshal(blocks[0], &b))
			require.Equal(t, "tool_result", b.Type)
			require.Equal(t, "interrupted", b.ToolUseID)
			require.True(t, b.IsError)
		})
	}
}
