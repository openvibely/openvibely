package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDiscordTaskContextRepo_UpsertGetDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	repo := NewDiscordTaskContextRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Discord Task Context"}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Discord Origin Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Do thing",
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	dtc := &models.DiscordTaskContext{
		TaskID:           task.ID,
		DiscordChannelID: "chan-1",
		DiscordThreadID:  "thread-1",
		DiscordMessageID: "msg-1",
		DiscordUserID:    "user-1",
	}
	require.NoError(t, repo.Upsert(ctx, dtc))

	got, err := repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, dtc.DiscordChannelID, got.DiscordChannelID)
	require.Equal(t, dtc.DiscordThreadID, got.DiscordThreadID)
	require.Equal(t, dtc.DiscordMessageID, got.DiscordMessageID)
	require.Equal(t, dtc.DiscordUserID, got.DiscordUserID)

	dtc.DiscordThreadID = "thread-2"
	dtc.DiscordMessageID = "msg-2"
	require.NoError(t, repo.UpsertWithExecutor(ctx, db, dtc))

	got, err = repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "thread-2", got.DiscordThreadID)
	require.Equal(t, "msg-2", got.DiscordMessageID)

	require.NoError(t, repo.DeleteByTaskID(ctx, task.ID))
	got, err = repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}
