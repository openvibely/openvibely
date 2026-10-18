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
	require.Equal(t, managedcontext.Text(changed), refreshed[1].(map[string]any)["content"])
	require.Equal(t, items[2:], refreshed[2:])
	disabled := ensureAttachmentContext(managedcontext.With(ctx, ""), items)
	require.Len(t, disabled, len(items)-1)
	require.Equal(t, items[2:], disabled[1:])
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
			require.Equal(t, prefix[:len(prefix)-1], request.Messages[:at])
			require.Equal(t, managedcontext.Text(ctx), request.Messages[at].Content)
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
