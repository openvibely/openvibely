package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

func TestCheckpointRevisionTracksPayloadAndSessionChanges(t *testing.T) {
	db, repo, _ := conversationTestRepo(t)
	createCheckpointTask(t, db, "revision")
	ctx := persistence.WithCheckpointRows(context.Background())
	cp := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "revision", ModelConfigID: "model", CompatibilityKey: "compatible", ProviderStateJSON: `[{"role":"user","content":"first"}]`}
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, cp))
	got, version, err := repo.GetChatCompactionCheckpointIfChanged(ctx, "task", "revision", "")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotEmpty(t, version)
	got, next, err := repo.GetChatCompactionCheckpointIfChanged(ctx, "task", "revision", version)
	require.NoError(t, err)
	require.Nil(t, got)
	require.Equal(t, version, next)
	// Session saves touch updated_at even when the state is unchanged.
	_, err = db.Exec(`UPDATE chat_compaction_checkpoints SET updated_at=datetime(updated_at,'+1 second') WHERE scope_id='revision'`)
	require.NoError(t, err)
	got, next, err = repo.GetChatCompactionCheckpointIfChanged(ctx, "task", "revision", version)
	require.NoError(t, err)
	require.Nil(t, got)
	require.Equal(t, version, next)
	// Revisions cannot rely on second-resolution timestamps.
	cp.ProviderStateJSON = `[{"role":"user","content":"second"}]`
	require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, cp))
	_, err = db.Exec(`UPDATE chat_compaction_checkpoints SET updated_at=created_at WHERE scope_id='revision'`)
	require.NoError(t, err)
	got, next, err = repo.GetChatCompactionCheckpointIfChanged(ctx, "task", "revision", version)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotEqual(t, version, next)
	version = next
	cp.ProviderSessionStateJSON = `{"setting":"new"}`
	require.NoError(t, repo.UpsertChatProviderSessionState(ctx, cp))
	got, next, err = repo.GetChatCompactionCheckpointIfChanged(ctx, "task", "revision", version)
	require.NoError(t, err)
	require.Equal(t, cp.ProviderSessionStateJSON, got.ProviderSessionStateJSON)
	require.NotEqual(t, version, next)
	version = next
	cp.CompatibilityKey = "other"
	require.NoError(t, repo.UpsertChatProviderSessionState(ctx, cp))
	got, next, err = repo.GetChatCompactionCheckpointIfChanged(ctx, "task", "revision", version)
	require.NoError(t, err)
	require.False(t, got.HasNativeState())
	require.NotEqual(t, version, next)
	_, err = db.Exec(`DELETE FROM chat_compaction_checkpoints WHERE scope_id='revision'`)
	require.NoError(t, err)
	got, next, err = repo.GetChatCompactionCheckpointIfChanged(ctx, "task", "revision", next)
	require.NoError(t, err)
	require.Nil(t, got)
	require.Empty(t, next)
}
