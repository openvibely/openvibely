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

func TestEmailTaskContextRepo_OutboundMessageRefsResolveWithinProjectAndSender(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	repo := NewEmailTaskContextRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Email Outbound Refs"}
	require.NoError(t, projectRepo.Create(ctx, project))
	otherProject := &models.Project{Name: "Other Email Outbound Refs"}
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	require.NoError(t, repo.RecordOutboundMessageRef(ctx, project.ID, "Alice@Example.com", "<bot-response@example.com>", "email:alice@example.com:<root@example.com>"))
	require.NoError(t, repo.RecordOutboundMessageRef(ctx, otherProject.ID, "alice@example.com", "<bot-response@example.com>", "email:alice@example.com:<foreign@example.com>"))

	sessionKey, err := repo.ResolveOutboundMessageSessionKey(ctx, project.ID, "alice@example.com", "<bot-response@example.com>")
	require.NoError(t, err)
	require.Equal(t, "email:alice@example.com:<root@example.com>", sessionKey)

	sessionKey, err = repo.ResolveOutboundMessageSessionKey(ctx, project.ID, "bob@example.com", "<bot-response@example.com>")
	require.NoError(t, err)
	require.Empty(t, sessionKey)

	sessionKey, err = repo.ResolveOutboundMessageSessionKey(ctx, project.ID, "alice@example.com", "<unknown@example.com>")
	require.NoError(t, err)
	require.Empty(t, sessionKey)
}
