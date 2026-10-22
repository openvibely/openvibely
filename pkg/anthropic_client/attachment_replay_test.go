package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

func TestAttachmentContextPersistsAcrossAnthropicFollowups(t *testing.T) {
	ctx := managedcontext.With(context.Background(), "PDF skill")
	ctx = attachmentcontext.With(ctx, "/files/report.pdf")
	requests := 0
	var prefix []agenticMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []agenticMessage `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		count := 0
		for _, m := range request.Messages {
			if text, ok := m.Content.(string); ok && strings.HasPrefix(text, attachmentcontext.Marker+"\n") {
				count++
			}
		}
		want := 1
		if requests >= 2 {
			want = 2
		}
		if requests >= 4 {
			want = 3
		}
		require.Equal(t, want, count)
		if prefix != nil {
			require.Equal(t, prefix, request.Messages[:len(prefix)], "followup must preserve the previous request prefix")
		}
		prefix = append([]agenticMessage(nil), request.Messages...)
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"m","model":"claude-sonnet-4-6","usage":{"input_tokens":10}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = old }()
	var history []Message
	for turn := 0; turn < 6; turn++ {
		if turn == 2 {
			ctx = attachmentcontext.With(ctx, "/files/report.pdf\n/files/new.pdf")
		}
		if turn == 4 {
			ctx = attachmentcontext.With(ctx, "")
		}
		client := NewWithAPIKey("test")
		client.History = append([]Message(nil), history...)
		var saved []Message
		_, err := client.SendAgentic(ctx, fmt.Sprintf("question %d", turn), &AgenticOptions{
			Model: "claude-sonnet-4-6", SkipDefaultTools: true, OmitExecutionEnvironment: true,
			OnReplay: func(messages []Message) { saved = append([]Message(nil), messages...) },
		})
		require.NoError(t, err)
		count := 0
		for _, m := range saved {
			if strings.HasPrefix(m.Content, attachmentcontext.Marker+"\n") {
				count++
				if turn == 4 {
					require.Contains(t, m.Content, "No files are currently attached")
				}
			}
		}
		if turn == 0 || turn == 2 || turn == 4 {
			require.Equal(t, 1, count)
		} else {
			require.Zero(t, count, "unchanged context belongs to earlier replay")
		}
		history = append(history, saved...)
	}
	require.Equal(t, 6, requests)
}
