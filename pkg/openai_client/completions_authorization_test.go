package openaiclient

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/testutil/retrytest"
	"github.com/stretchr/testify/require"
)

func TestCompatibleSendRechecksAuthorization(t *testing.T) {
	for _, boundary := range []string{"initial", "finalizer", "retry"} {
		t.Run(boundary, func(t *testing.T) {
			client := NewWithCompatibleAPIKey("test-key", "https://compatible.test/v1", "", "")
			denied := errors.New("reviewer trust revoked")
			revoked := boundary == "initial"
			requests, finalizers := 0, 0
			client.httpClient = &http.Client{Transport: completionsRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				revoked = true
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: failingCompletionsBody{}}, nil
			})}
			ctx := llmcontracts.WithBeforeModelSend(retrytest.Context(), func(context.Context) error {
				if revoked {
					return denied
				}
				return nil
			})
			_, err := client.SendCompletions(ctx, "prepared instruction", &CompletionsOptions{DisableTools: true, FinalizeRequest: func(*http.Request, []byte) error {
				finalizers++
				if boundary == "finalizer" {
					revoked = true
				}
				return nil
			}})
			require.ErrorIs(t, err, denied)
			switch boundary {
			case "initial":
				require.Zero(t, requests)
				require.Zero(t, finalizers)
			case "finalizer":
				require.Zero(t, requests)
				require.Equal(t, 1, finalizers)
			case "retry":
				require.Equal(t, 1, requests, "revocation must stop the rebuilt request")
				require.Equal(t, 1, finalizers)
			}
		})
	}
}

func TestResponsesWebsocketRevocationDuringHandshakeBlocksFrame(t *testing.T) {
	var revoked atomic.Bool
	frames := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revoked.Store(true)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			frames <- false
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		_, _, err = conn.Read(ctx)
		frames <- err == nil
	}))
	defer srv.Close()
	oldURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldURL }()
	client := newHTTPTestAPIKeyClient("test")
	denied := errors.New("revoked during handshake")
	ctx := llmcontracts.WithBeforeModelSend(context.Background(), func(context.Context) error {
		if revoked.Load() {
			return denied
		}
		return nil
	})
	body, err := client.openResponsesWebsocketStream(ctx, map[string]any{"model": "gpt-test", "input": []any{}}, false, responsesWebsocketStreamOptions{Model: "gpt-test"})
	require.ErrorIs(t, err, denied)
	require.Nil(t, body)
	require.False(t, isRetryableResponsesTransportError(err))
	select {
	case sent := <-frames:
		require.False(t, sent, "revoked instruction reached websocket")
	case <-time.After(4 * time.Second):
		t.Fatal("connection was not released after denial")
	}
	require.Nil(t, client.responsesTransportState.conn)
}
