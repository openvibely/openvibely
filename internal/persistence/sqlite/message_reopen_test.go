package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestStreamAndProviderRecoveryAfterReopen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "recovery.db")
	db, err := database.New(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewExecutionRepo(db)
	task := &models.Task{ProjectID: "default", Title: "recovery", Category: models.CategoryChat, Status: models.StatusRunning}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, execution))
	require.NoError(t, repo.AppendOutput(ctx, execution.ID, "hello "))
	require.NoError(t, repo.AppendOutput(ctx, execution.ID, "世界"))
	owner := models.ConversationOwner{ExecutionID: execution.ID}
	require.NoError(t, transcript.NewRecorder(repo, owner).Save(ctx, transcript.ReplayHeader{Format: "envelope", ItemsKey: "responses_input"}, []any{map[string]any{"type": "reasoning", "encrypted_content": "private context"}}))
	// Failure after raw text is durable must not lose it or publish half-finalized rows.
	_, err = db.Exec(`CREATE TRIGGER reject_finalization BEFORE INSERT ON conversation_items WHEN NEW.lane='display' BEGIN SELECT RAISE(ABORT,'failed finalization'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.Complete(ctx, execution.ID, models.ExecCompleted, "hello 世界", "", 0, 0), "failed finalization")
	require.NoError(t, db.Close())
	db, err = database.New(path)
	require.NoError(t, err)
	repo = NewExecutionRepo(db)
	got, err := repo.ReadOutputAfter(ctx, execution.ID, len("hello "))
	require.NoError(t, err)
	require.Equal(t, "世界", got.Output)
	require.Equal(t, models.ExecRunning, got.Status)
	replay, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.JSONEq(t, `{"responses_input":[{"type":"reasoning","encrypted_content":"private context"}]}`, replay[execution.ID][0].TranscriptJSON)
	_, err = db.Exec(`DROP TRIGGER reject_finalization`)
	require.NoError(t, err)
	require.NoError(t, repo.Complete(ctx, execution.ID, models.ExecCompleted, "hello 世界", "", 0, 0))
	got, err = repo.ReadOutputAfter(ctx, execution.ID, len("hello "))
	require.NoError(t, err)
	require.Equal(t, "世界", got.Output)
	require.Equal(t, models.ExecCompleted, got.Status)
	page, err := repo.GetDisplayExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, "hello 世界", page.Output)
	require.NotContains(t, page.Output, "private context")
}
