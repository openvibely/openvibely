package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackTaskContextRepo_UpsertGetDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	repo := NewSlackTaskContextRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Slack Task Context"}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Slack Origin Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Do thing",
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	stc := &models.SlackTaskContext{
		TaskID:         task.ID,
		SlackTeamID:    "T123",
		SlackChannelID: "C123",
		SlackThreadTS:  "1710000000.100000",
		SlackUserID:    "U123",
	}
	require.NoError(t, repo.Upsert(ctx, stc))

	got, err := repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, stc.SlackTeamID, got.SlackTeamID)
	require.Equal(t, stc.SlackChannelID, got.SlackChannelID)
	require.Equal(t, stc.SlackThreadTS, got.SlackThreadTS)
	require.Equal(t, stc.SlackUserID, got.SlackUserID)

	stc.SlackThreadTS = "1710000000.200000"
	require.NoError(t, repo.Upsert(ctx, stc))

	got, err = repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "1710000000.200000", got.SlackThreadTS)

	require.NoError(t, repo.DeleteByTaskID(ctx, task.ID))
	got, err = repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}
