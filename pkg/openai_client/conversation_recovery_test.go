package openaiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompletionsRecoveryIncludesPriorHistoryAndCurrentToolResults(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"context length exceeded","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"done\",\"type\":\"function\",\"function\":{\"name\":\"edit_file\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	old := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = server.URL + "/"
	defer func() { OpenAIAPIBaseURL = old }()
	client := newHTTPTestAPIKeyClient("test")
	client.SetCompletionsHistory([]CompletionsHistoryMessage{{Role: "user", Content: "PRIOR_REQUEST"}})
	_, err := client.SendCompletions(context.Background(), "edit", &CompletionsOptions{Model: "gpt-test", Attachments: []*FileAttachment{{FileName: "image.png", MediaType: "image/png", Data: []byte("imagebytes")}}, SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Type: "function", Name: "edit_file", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
		return "ALREADY_EDITED", false, nil
	}})
	require.Error(t, err)
	wrapped := client.ConversationError(err)
	require.ErrorIs(t, wrapped, err)
	var snapshot *ConversationError
	require.True(t, errors.As(wrapped, &snapshot))
	require.Contains(t, snapshot.MessagesJSON, "PRIOR_REQUEST")
	require.Contains(t, snapshot.MessagesJSON, "ALREADY_EDITED")
	require.Contains(t, snapshot.MessagesJSON, "edit_file")
	require.Contains(t, snapshot.MessagesJSON, base64.StdEncoding.EncodeToString([]byte("imagebytes")))
	require.NotContains(t, snapshot.MessagesJSON, "image_content")
	require.Equal(t, 2, calls)
}
