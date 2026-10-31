package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeCheckpointRowsAndCompatibility(t *testing.T) {
	_, repo, _ := conversationTestRepo(t)
	ctx := context.Background()
	for _, raw := range []string{`[{"type":"compaction","encrypted_content":"opaque"},{"type":"function_call_output","output":"tool text"}]`, `{"version":1,"messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary"}]}]}`} {
		cp := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "rows", ProviderStateJSON: raw}
		require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, cp))
		got, err := repo.GetChatCompactionCheckpoint(persistence.WithCheckpointRows(ctx), cp.ScopeType, cp.ScopeID)
		require.NoError(t, err)
		require.Empty(t, got.ProviderStateJSON)
		require.NotNil(t, got.ProviderStateRows)
		require.True(t, got.HasNativeState())
		require.JSONEq(t, raw, got.NativeState().FullTranscriptJSON())
		// Saving the internal representation must keep compatibility and invalidation.
		require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, *got))
		legacy, err := repo.GetChatCompactionCheckpoint(ctx, cp.ScopeType, cp.ScopeID)
		require.NoError(t, err)
		require.Nil(t, legacy.ProviderStateRows)
		require.JSONEq(t, raw, legacy.ProviderStateJSON)
		got.ProviderStateRows = nil
		require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, *got))
		cleared, err := repo.GetChatCompactionCheckpoint(persistence.WithCheckpointRows(ctx), cp.ScopeType, cp.ScopeID)
		require.NoError(t, err)
		require.False(t, cleared.HasNativeState())
	}
}
func TestExecutionPayloadProjection(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.FinalizeOutput(ctx, owner.ExecutionID, "display must not load"))
	_, err := db.Exec(`UPDATE executions SET prompt_sent='prompt',reasoning_content='reason',error_message='error',cli_session_id='session',diff_output='diff' WHERE id=?`, owner.ExecutionID)
	require.NoError(t, err)
	for _, fields := range []persistence.ExecutionPayload{0, persistence.ExecutionPrompt, persistence.ExecutionReasoning, persistence.ExecutionError, persistence.ExecutionSession, persistence.ExecutionPrompt | persistence.ExecutionReasoning | persistence.ExecutionSession} {
		got, err := repo.GetByID(persistence.WithExecutionPayload(ctx, fields), owner.ExecutionID)
		require.NoError(t, err)
		require.Empty(t, got.Output)
		require.Empty(t, got.DiffOutput)
		for _, item := range []struct {
			flag         persistence.ExecutionPayload
			actual, want string
		}{{persistence.ExecutionPrompt, got.PromptSent, "prompt"}, {persistence.ExecutionReasoning, got.ReasoningContent, "reason"}, {persistence.ExecutionError, got.ErrorMessage, "error"}, {persistence.ExecutionSession, got.CliSessionID, "session"}} {
			if fields&item.flag != 0 {
				require.Equal(t, item.want, item.actual)
			} else {
				require.Empty(t, item.actual)
			}
		}
	}
	full, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, "display must not load", full.Output)
}
