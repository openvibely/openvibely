package openai

import (
	"context"
	"encoding/json"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeRowsBudgetAndSendOwnership(t *testing.T) {
	rows := &models.ReplayRows{Format: "array", Items: []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`), json.RawMessage(`{"type":"function_call_output","call_id":"c","output":"tool text"}`)}}
	replay := models.ExecutionReplayMessage{Rows: rows}
	ctx := preparedhistory.WithScope(context.Background())
	_, want := EstimateContinuationTokens(nil, replay.FullTranscriptJSON())
	_, got := EstimateContinuationReplayTokens(ctx, nil, replay)
	require.Equal(t, want, got)
	ctx = llmcontracts.WithNativeCompactionState(ctx, replay)
	items := nativeCompactionInputItems(ctx)
	require.Len(t, items, 2)
	items[0].(map[string]any)["encrypted_content"] = "mutated"
	again := nativeCompactionInputItems(ctx)
	require.Equal(t, "opaque", again[0].(map[string]any)["encrypted_content"])
	// Provider error recovery clears both the checkpoint and cached decode.
	ctx = llmcontracts.WithNativeCompactionStateJSON(ctx, "")
	require.Empty(t, nativeCompactionInputItems(ctx))
	ctx = llmcontracts.WithNativeCompactionStateJSON(ctx, `[{"type":"compaction","encrypted_content":"replacement"}]`)
	require.Equal(t, "replacement", nativeCompactionInputItems(ctx)[0].(map[string]any)["encrypted_content"])
	require.Equal(t, `{"type":"compaction","encrypted_content":"opaque"}`, string(rows.Items[0]))
}
