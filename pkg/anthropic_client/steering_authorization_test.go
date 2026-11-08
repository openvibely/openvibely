package anthropicclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/stretchr/testify/require"
)

func TestModelSendAuthorizationRunsAfterToolFiltering(t *testing.T) {
	client := NewWithAPIKey("test")
	requests, checks := 0, 0
	client.httpClient = &http.Client{Transport: anthropicRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected provider call")
	})}
	revoked := false
	denied := errors.New("reviewer removed during preparation")
	ctx := llmcontracts.WithBeforeModelSend(context.Background(), func(context.Context) error {
		checks++
		if revoked {
			return denied
		}
		return nil
	})
	_, err := client.sendAgenticTurnOnce(ctx, []agenticMessage{}, []ToolDefinition{{Name: "echo"}}, &AgenticOptions{Model: "claude-opus-4-6", ToolFilter: func(string) bool { revoked = true; return true }})
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, checks)
	require.Zero(t, requests)
}
