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

func TestExecutionListPayloadProjectionPreservesMetadata(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.FinalizeOutput(ctx, owner.ExecutionID, "retained display"))
	_, err := db.Exec(`UPDATE executions SET prompt_sent='prompt',reasoning_content='reason',error_message='error',cli_session_id='session',diff_output='diff',tokens_used=123,duration_ms=456 WHERE id=?`, owner.ExecutionID)
	require.NoError(t, err)
	full, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)

	for _, test := range []struct {
		name string
		ctx  context.Context
		want models.Execution
	}{
		{name: "ordinary", ctx: ctx, want: func() models.Execution {
			want := *full
			want.ReasoningContent, want.DiffOutput = "", ""
			return want
		}()},
		{name: "metadata", ctx: persistence.WithExecutionMetadata(ctx), want: func() models.Execution {
			want := *full
			want.Output, want.DiffOutput, want.PromptSent, want.ReasoningContent, want.ErrorMessage, want.CliSessionID = "", "", "", "", "", ""
			return want
		}()},
		{name: "error only", ctx: persistence.WithExecutionPayload(ctx, persistence.ExecutionError), want: func() models.Execution {
			want := *full
			want.Output, want.DiffOutput, want.PromptSent, want.ReasoningContent, want.CliSessionID = "", "", "", "", ""
			return want
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := repo.ListByTask(test.ctx, full.TaskID)
			require.NoError(t, err)
			require.Equal(t, []models.Execution{test.want}, rows)
			missing, err := repo.ListByTask(test.ctx, "different-task")
			require.NoError(t, err)
			require.Empty(t, missing)
		})
	}
}
