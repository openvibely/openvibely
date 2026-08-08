package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestEmailTaskContextRepo_UpsertGetDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	repo := NewEmailTaskContextRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Email Task Context"}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Email Origin Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Do thing",
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	etc := &models.EmailTaskContext{
		TaskID:          task.ID,
		EmailFrom:       "sender@example.com",
		EmailMessageID:  "<message-1@example.com>",
		EmailReferences: "<root@example.com>",
		EmailSubject:    "Original subject",
		EmailSessionKey: "sender@example.com|thread-root",
	}
	require.NoError(t, repo.Upsert(ctx, etc))

	got, err := repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, etc.EmailFrom, got.EmailFrom)
	require.Equal(t, etc.EmailMessageID, got.EmailMessageID)
	require.Equal(t, etc.EmailReferences, got.EmailReferences)
	require.Equal(t, etc.EmailSubject, got.EmailSubject)
	require.Equal(t, etc.EmailSessionKey, got.EmailSessionKey)

	etc.EmailMessageID = "<message-2@example.com>"
	etc.EmailSessionKey = "sender@example.com|thread-next"
	require.NoError(t, repo.UpsertWithExecutor(ctx, db, etc))

	got, err = repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "<message-2@example.com>", got.EmailMessageID)
	require.Equal(t, "sender@example.com|thread-next", got.EmailSessionKey)

	require.NoError(t, repo.DeleteByTaskID(ctx, task.ID))
	got, err = repo.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}
