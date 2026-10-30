package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestRawStreamFinalizationIsAtomicAndIdempotent(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	for _, part := range []string{"hello ", "世界"} {
		require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, part))
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_items WHERE execution_id=?`, owner.ExecutionID).Scan(&count))
	require.Zero(t, count)
	_, err := db.Exec(`CREATE TRIGGER fail_finalize BEFORE DELETE ON execution_stream_text BEGIN SELECT RAISE(ABORT,'finalize failed'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.FinalizeOutput(ctx, owner.ExecutionID, "hello 世界"), "finalize failed")
	got, err := repo.ReadOutputAfter(ctx, owner.ExecutionID, 0)
	require.NoError(t, err)
	require.Equal(t, "hello 世界", got.Output)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_items WHERE execution_id=?`, owner.ExecutionID).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_finalize`)
	require.NoError(t, err)
	require.NoError(t, repo.FinalizeOutput(ctx, owner.ExecutionID, "hello 世界"))
	var sequence int64
	require.NoError(t, db.QueryRow(`SELECT max(sequence) FROM conversation_items WHERE execution_id=?`, owner.ExecutionID).Scan(&sequence))
	require.NoError(t, repo.Complete(ctx, owner.ExecutionID, models.ExecCompleted, "hello 世界", "", 0, 0))
	var after int64
	require.NoError(t, db.QueryRow(`SELECT max(sequence) FROM conversation_items WHERE execution_id=?`, owner.ExecutionID).Scan(&after))
	require.Equal(t, sequence, after)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM execution_stream_text WHERE execution_id=?`, owner.ExecutionID).Scan(&count))
	require.Zero(t, count)
}

func TestInitialReplayBatchRollbackAndRetry(t *testing.T) {
	for _, size := range []int{1, 140} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			db, repo, owner := conversationTestRepo(t)
			ctx := context.Background()
			items := make([]any, size)
			for i := range items {
				items[i] = map[string]any{"role": "assistant", "content": fmt.Sprintf("message %d", i)}
			}
			recorder := transcript.NewRecorder(repo, owner)
			_, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_replay_batch BEFORE INSERT ON conversation_items WHEN NEW.item_id='replay:000000000000:item:%012d' BEGIN SELECT RAISE(ABORT,'batch failed'); END`, size-1))
			require.NoError(t, err)
			require.ErrorContains(t, recorder.Save(ctx, transcript.ReplayHeader{Format: "array"}, items), "batch failed")
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_items WHERE execution_id=?`, owner.ExecutionID).Scan(&count))
			require.Zero(t, count)
			require.Empty(t, recorder.Recovery())
			_, err = db.Exec(`DROP TRIGGER fail_replay_batch`)
			require.NoError(t, err)
			require.NoError(t, recorder.Save(ctx, transcript.ReplayHeader{Format: "array"}, items))
			require.Equal(t, items, recorder.Recovery())
			require.NoError(t, recorder.Save(ctx, transcript.ReplayHeader{Format: "array"}, items))
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_items WHERE execution_id=?`, owner.ExecutionID).Scan(&count))
			require.Equal(t, size+2, count)
		})
	}
}

func TestInterruptedStreamCompletesWithoutReturnedOutput(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, "durable partial response"))
	require.NoError(t, repo.Complete(ctx, owner.ExecutionID, models.ExecCancelled, "", "cancelled", 0, 0))
	got, err := repo.ReadOutputAfter(ctx, owner.ExecutionID, 0)
	require.NoError(t, err)
	require.Equal(t, "durable partial response", got.Output)
	require.Equal(t, models.ExecCancelled, got.Status)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM execution_stream_text WHERE execution_id=?`, owner.ExecutionID).Scan(&count))
	require.Zero(t, count)
}

func TestRawStreamUsesDedicatedWriter(t *testing.T) {
	connections, err := database.NewReadWrite(filepath.Join(t.TempDir(), "stream.db"))
	require.NoError(t, err)
	defer connections.Close()
	unregister := RegisterDedicatedWriter(connections.Reader, connections.Writer)
	defer unregister()
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, connections.Writer)
	task := createThreadInputTask(t, ctx, connections.Writer, project.ID)
	agent := &models.LLMConfig{Name: "Stream test", Provider: models.ProviderOpenAI, Model: "test", APIKey: "test"}
	require.NoError(t, NewLLMConfigRepo(connections.Writer).Create(ctx, agent))
	repo := NewExecutionRepo(connections.Reader)
	execution := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, &execution))
	require.NoError(t, repo.AppendOutput(ctx, execution.ID, "hello "))
	require.NoError(t, repo.AppendOutput(ctx, execution.ID, "世界"))
	output, err := repo.ReadOutputAfter(ctx, execution.ID, 6)
	require.NoError(t, err)
	require.Equal(t, "世界", output.Output)
	require.NoError(t, repo.Complete(ctx, execution.ID, models.ExecCompleted, "hello 世界", "", 0, 0))
	require.NoError(t, repo.AppendOutput(ctx, execution.ID, "late"))
	output, err = repo.ReadOutputAfter(ctx, execution.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "hello 世界", output.Output)
}

// The optimized recorder batch must preserve the generic writer's atomicity,
// optimistic revision checks and uncertain-commit retry semantics.
func TestEncodedReplayBatchConflictRollbackAndRetry(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	initial := models.ConversationChange{Lane: models.ConversationProvider, ItemID: "item", Operation: models.ConversationPut, Payload: json.RawMessage(`"old"`), MutationID: "first"}
	saved, err := repo.AppendEncodedReplayChanges(ctx, owner, []models.ConversationChange{initial})
	require.NoError(t, err)
	update := initial
	update.PreviousRevision, update.MutationID, update.Payload = saved[0].Sequence, "second", json.RawMessage(`"new"`)
	added := models.ConversationChange{Lane: models.ConversationProvider, ItemID: "next", Position: 1, Operation: models.ConversationPut, Payload: json.RawMessage(`"next"`), MutationID: "third"}
	_, err = db.Exec(`CREATE TRIGGER fail_suffix BEFORE INSERT ON conversation_items WHEN NEW.item_id='next' BEGIN SELECT RAISE(ABORT,'suffix failed'); END`)
	require.NoError(t, err)
	_, err = repo.AppendEncodedReplayChanges(ctx, owner, []models.ConversationChange{update, added})
	require.ErrorContains(t, err, "suffix failed")
	var payload string
	require.NoError(t, db.QueryRow(`SELECT payload FROM conversation_items WHERE execution_id=? AND item_id='item'`, owner.ExecutionID).Scan(&payload))
	require.Equal(t, `"old"`, payload)
	_, err = db.Exec(`DROP TRIGGER fail_suffix`)
	require.NoError(t, err)
	committed, err := repo.AppendEncodedReplayChanges(ctx, owner, []models.ConversationChange{update, added})
	require.NoError(t, err)
	retry, err := repo.AppendEncodedReplayChanges(ctx, owner, []models.ConversationChange{update, added})
	require.NoError(t, err)
	require.Equal(t, committed, retry)
	update.MutationID = "stale"
	_, err = repo.AppendEncodedReplayChanges(ctx, owner, []models.ConversationChange{update})
	require.ErrorIs(t, err, ErrConversationConflict)
	update.PreviousRevision = committed[0].Sequence
	update.Payload = json.RawMessage(`invalid JSON`)
	_, err = repo.AppendEncodedReplayChanges(ctx, owner, []models.ConversationChange{update})
	require.Error(t, err)
	require.NoError(t, db.QueryRow(`SELECT payload FROM conversation_items WHERE execution_id=? AND item_id='item'`, owner.ExecutionID).Scan(&payload))
	require.Equal(t, `"new"`, payload)
}
