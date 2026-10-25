package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompletionsInterruptedToolRecovery(t *testing.T) {
	call := CompletionsHistoryMessage{Role: "assistant", ReasoningContent: "preserve thinking", ToolCalls: []CompletionsToolCall{{ID: "a"}, {ID: "b"}}}
	for _, tc := range []struct {
		name    string
		tail    []CompletionsHistoryMessage
		missing int
	}{
		{"trailing", nil, 2},
		{"followup", []CompletionsHistoryMessage{{Role: "user", Content: "continue"}}, 2},
		{"partial", []CompletionsHistoryMessage{{Role: "tool", ToolCallID: "a", Content: "real result"}, {Role: "user", Content: "continue"}}, 1},
		{"complete", []CompletionsHistoryMessage{{Role: "tool", ToolCallID: "a", Content: "A"}, {Role: "tool", ToolCallID: "b", Content: "B"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := append([]CompletionsHistoryMessage{call}, tc.tail...)
			before, _ := json.Marshal(history)
			got := repairInterruptedCompletionsToolCalls(history)
			require.Equal(t, got, repairInterruptedCompletionsToolCalls(got))
			after, _ := json.Marshal(history)
			require.Equal(t, string(before), string(after))
			require.Equal(t, call, got[0])
			seen := map[string]int{}
			missing := 0
			for _, m := range got[1:3] {
				require.Equal(t, "tool", m.Role)
				seen[m.ToolCallID]++
				if m.Content == "Execution interrupted; outcome unknown." {
					missing++
				}
			}
			require.Equal(t, map[string]int{"a": 1, "b": 1}, seen)
			require.Equal(t, tc.missing, missing)
			if tc.name == "partial" {
				require.Equal(t, "real result", got[1].Content)
				require.Equal(t, "continue", got[3].Content)
			}
		})
	}
}
func TestSendCompletionsRepairsRestoredToolCall(t *testing.T) {
	var request struct {
		Messages []completionsMessage `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
	client.SetCompletionsHistory([]CompletionsHistoryMessage{{Role: "assistant", ToolCalls: []CompletionsToolCall{{ID: "interrupted"}}}})
	_, err := client.SendCompletions(context.Background(), "followup", &CompletionsOptions{Model: "test", DisableTools: true, OmitExecutionEnvironment: true, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
		t.Error("restored tool must not run")
		return "", false, nil
	}})
	require.NoError(t, err)
	require.Len(t, request.Messages, 3)
	require.Equal(t, "tool", request.Messages[1].Role)
	require.Equal(t, "interrupted", request.Messages[1].ToolCallID)
	require.Equal(t, "Execution interrupted; outcome unknown.", request.Messages[1].Content)
	require.Equal(t, "followup", request.Messages[2].Content)
}
