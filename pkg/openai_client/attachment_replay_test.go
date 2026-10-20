package openaiclient

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

func TestAttachmentContextPersistsAcrossCompletionsFollowups(t *testing.T) {
	ctx := managedcontext.With(context.Background(), "PDF skill")
	ctx = attachmentcontext.With(ctx, "/files/report.pdf")
	requests := 0
	var prefix []completionsMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []completionsMessage `json:"messages"`
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
		require.Equal(t, want, count)
		if prefix != nil {
			require.Equal(t, prefix, request.Messages[:len(prefix)], "followup must preserve the previous request prefix")
		}
		prefix = append([]completionsMessage(nil), request.Messages...)
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	var history []CompletionsHistoryMessage
	for turn := 0; turn < 4; turn++ {
		if turn == 2 {
			ctx = attachmentcontext.With(ctx, "/files/report.pdf\n/files/new.pdf")
		}
		client := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
		client.SetCompletionsHistory(history)
		_, err := client.SendCompletions(ctx, fmt.Sprintf("question %d", turn), &CompletionsOptions{OmitExecutionEnvironment: true})
		require.NoError(t, err)
		saved := client.LastCompletionsTranscript()
		count := 0
		for _, m := range saved {
			if strings.HasPrefix(m.Content, attachmentcontext.Marker+"\n") {
				count++
			}
		}
		if turn == 0 || turn == 2 {
			require.Equal(t, 1, count)
		} else {
			require.Zero(t, count, "unchanged context belongs to earlier replay")
		}
		history = append(history, saved...)
	}
	require.Equal(t, 4, requests)
}
