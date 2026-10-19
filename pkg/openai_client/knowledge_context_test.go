package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

func TestResponsesKnowledgeIndexPreservesPrefixAndRestoresAfterCompaction(t *testing.T) {
	index := "document_word: Word documents"
	ctx := managedcontext.WithSource(context.Background(), func(context.Context) string { return index })
	items := []any{
		map[string]any{"type": "message", "role": "user", "content": "Summarize this document"},
		map[string]any{"type": "message", "role": "user", "content": managedcontext.Text(ctx)},
		map[string]any{"type": "function_call", "call_id": "skill-1", "name": "skill_view", "arguments": `{"handle":"document_word"}`},
		map[string]any{"type": "function_call_output", "call_id": "skill-1", "output": "Word instructions"},
		map[string]any{"type": "message", "role": "assistant", "content": "Summary"},
		map[string]any{"type": "message", "role": "user", "content": "Change the title"},
	}
	require.Equal(t, items, ensureAttachmentContext(ctx, items))
	// A pending configuration update must still be the final item.
	items = append(items, map[string]any{"type": "configuration_update"})
	require.Equal(t, items, ensureAttachmentContext(ctx, items))
	index = "document_word: updated description"
	changed := ctx
	refreshed := ensureAttachmentContext(changed, items)
	require.Equal(t, managedcontext.Text(changed), refreshed[5].(map[string]any)["content"])
	require.Equal(t, items[:5], refreshed[:5])
	require.Equal(t, items[5:], refreshed[6:])
	disabled := ensureAttachmentContext(managedcontext.With(ctx, ""), items)
	require.Len(t, disabled, len(items)+1)
	require.Equal(t, items[:5], disabled[:5])
	require.Contains(t, disabled[5].(map[string]any)["content"], "No skills or memory index")
	// An index before an opaque compaction item is no longer model-visible.
	compacted := []any{items[1], map[string]any{"type": "compaction", "encrypted_content": "opaque"}, items[5], items[6]}
	restored := ensureAttachmentContext(ctx, compacted)
	require.Equal(t, managedcontext.Text(ctx), restored[2].(map[string]any)["content"])
	require.Equal(t, compacted[:2], restored[:2])
	require.Equal(t, restored, ensureAttachmentContext(ctx, restored))
}

func TestCompletionsKnowledgeIndexReplaysAtOriginalPosition(t *testing.T) {
	ctx := managedcontext.With(context.Background(), "document_word: Word documents")
	calls := 0
	var prefix []completionsMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []completionsMessage `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		count, at := 0, -1
		for i, m := range request.Messages {
			if s, ok := m.Content.(string); ok && strings.HasPrefix(s, managedcontext.Marker+"\n") {
				count++
				at = i
			}
		}
		require.Equal(t, 1, count)
		if count == 1 {
			if prefix == nil {
				prefix = append([]completionsMessage(nil), request.Messages[:at+1]...)
			}
			require.Equal(t, prefix, request.Messages[:at+1], "index must not move after tool results or the next user message")
		}
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"skill-1\",\"type\":\"function\",\"function\":{\"name\":\"skill_view\",\"arguments\":\"{}\"}}]}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Done\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	options := &CompletionsOptions{ExtraTools: []ToolDefinition{{Type: "function", Name: "skill_view", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
		return "Word instructions", false, nil
	}}
	first := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
	_, err := first.SendCompletions(ctx, "Summarize this document", options)
	require.NoError(t, err)
	saved := first.LastCompletionsTranscript()
	second := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
	second.SetCompletionsHistory(saved)
	_, err = second.SendCompletions(ctx, "Change the title", options)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	for _, m := range second.LastCompletionsTranscript() {
		require.NotContains(t, m.Content, managedcontext.Marker, "later executions must not accumulate duplicate indexes")
	}
}

func TestCompletionsIndexChangesDuringToolLoop(t *testing.T) {
	index := "document_word: initial"
	ctx := managedcontext.WithSource(context.Background(), func(context.Context) string { return index })
	calls := 0
	var prefix []completionsMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []completionsMessage `json:"messages"`
			Tools    []json.RawMessage    `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		encoded, _ := json.Marshal(request.Tools)
		if calls == 0 {
			require.NotContains(t, string(encoded), "memory_view")
		} else {
			require.Contains(t, string(encoded), "memory_view")
		}
		count, at := 0, -1
		for i, m := range request.Messages {
			if s, ok := m.Content.(string); ok && strings.HasPrefix(s, managedcontext.Marker+"\n") {
				count++
				at = i
			}
		}
		if calls == 0 {
			require.Equal(t, 1, count)
			prefix = append([]completionsMessage(nil), request.Messages[:at+1]...)
		} else {
			require.Equal(t, 2, count)
			require.Equal(t, prefix, request.Messages[:len(prefix)])
		}
		require.Equal(t, managedcontext.Text(ctx), request.Messages[at].Content)

		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"skill-1\",\"type\":\"function\",\"function\":{\"name\":\"skill_view\",\"arguments\":\"{}\"}}]}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Done\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	options := &CompletionsOptions{ToolFilter: func(name string) bool { return name != "memory_view" || index == "document_word: updated" }, ExtraTools: []ToolDefinition{{Type: "function", Name: "memory_view", Parameters: json.RawMessage(`{"type":"object"}`)}, {Type: "function", Name: "skill_view", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
		index = "document_word: updated"
		return "Word instructions", false, nil
	}}
	first := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
	_, err := first.SendCompletions(ctx, "Summarize this document", options)
	require.NoError(t, err)
	saved := first.LastCompletionsTranscript()
	second := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
	second.SetCompletionsHistory(saved)
	_, err = second.SendCompletions(ctx, "Change the title", options)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	for _, m := range second.LastCompletionsTranscript() {
		require.NotContains(t, m.Content, managedcontext.Marker, "later executions must not accumulate duplicate indexes")
	}
}

func TestResponsesPermissionEnabledAfterToolResult(t *testing.T) {
	enabled := false
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		names := []string{}
		for _, tool := range request.Tools {
			names = append(names, tool.Name)
		}
		if calls == 0 {
			require.NotContains(t, names, "memory_view")
		} else {
			require.Contains(t, names, "memory_view")
		}
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"echo\",\"arguments\":\"{}\"}}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done\"}\n\n")
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\"}}\n\n")
	}))
	defer server.Close()
	old := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = server.URL + "/"
	defer func() { OpenAIAPIBaseURL = old }()
	_, err := newHTTPTestAPIKeyClient("test").SendAgentic(context.Background(), "test", &AgenticOptions{
		Model: "gpt-5.3-codex", SkipDefaultTools: true,
		ExtraTools: []ToolDefinition{{Type: "function", Name: "echo"}, {Type: "function", Name: "memory_view"}},
		ToolFilter: func(name string) bool { return name != "memory_view" || enabled },
		ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
			enabled = true
			return "permissions changed", false, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}
