package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

func TestIndexAndPermissionsRefreshAfterToolResult(t *testing.T) {
	index := "initial skills and memories"
	allowed := true
	ctx := managedcontext.WithSource(context.Background(), func(context.Context) string { return index })
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []agenticMessage `json:"messages"`
			Tools    []ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests++
		body, _ := json.Marshal(request.Messages)
		if requests == 1 {
			require.Contains(t, string(body), "initial skills and memories")
			require.Len(t, request.Tools, 1)
			require.Equal(t, "memory_save", request.Tools[0].Name)
		} else {
			require.Contains(t, string(body), "initial skills and memories")
			require.Contains(t, string(body), "updated memory index")
			require.Len(t, request.Tools, 1)
			require.Equal(t, "memory_view", request.Tools[0].Name)
			require.Contains(t, string(body), "saved memory")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{`{"type":"message_start","message":{"id":"m","model":"claude-sonnet-4-6","usage":{"input_tokens":10}}}`}
		if requests == 1 {
			events = append(events, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"memory_save","input":{}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
		} else {
			events = append(events, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
		}
		events = append(events, `{"type":"message_stop"}`)
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = old }()
	client := NewWithAPIKey("test")
	_, err := client.SendAgentic(ctx, "save memory", &AgenticOptions{Model: "claude-sonnet-4-6", SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: "memory_save", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "memory_view", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ToolFilter: func(name string) bool {
		if name == "memory_view" {
			return !allowed
		}
		return allowed
	}, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
		index = "updated memory index"
		allowed = false
		return "saved memory", false, nil
	}})
	require.NoError(t, err)
	require.Equal(t, 2, requests)
	// A followup must replay the saved update without injecting another copy.
	_, err = client.SendAgentic(ctx, "followup", &AgenticOptions{Model: "claude-sonnet-4-6", SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: "memory_view", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	require.NoError(t, err)
	require.Equal(t, 3, requests)
	count := 0
	for _, m := range client.History {
		if m.Content == managedcontext.Text(ctx) {
			count++
		}
	}
	require.Equal(t, 1, count)
}
