package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailAuthRepo_CRUDAuthorizationAndNormalization(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewEmailAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Email Auth Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	hasAny, err := repo.HasAnyAuthorizedUsers(ctx, project.ID)
	require.NoError(t, err)
	assert.False(t, hasAny)

	sender := &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: " Alice@Example.COM ", DisplayName: "Alice", AddedBy: "test"}
	require.NoError(t, repo.Create(ctx, sender))
	require.NotEmpty(t, sender.ID)
	require.Equal(t, "alice@example.com", sender.EmailAddress)

	loaded, err := repo.GetByID(ctx, sender.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "alice@example.com", loaded.EmailAddress)

	ok, err := repo.IsAuthorized(ctx, project.ID, "ALICE@example.com")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = repo.IsAuthorizedAnywhere(ctx, "alice@EXAMPLE.com")
	require.NoError(t, err)
	assert.True(t, ok)

	hasAny, err = repo.HasAnyAuthorizedUsers(ctx, project.ID)
	require.NoError(t, err)
	assert.True(t, hasAny)

	hasAnyAnywhere, err := repo.HasAnyAuthorizedUsersAnywhere(ctx)
	require.NoError(t, err)
	assert.True(t, hasAnyAnywhere)

	senders, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, senders, 1)

	require.NoError(t, repo.Delete(ctx, sender.ID))
	senders, err = repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	assert.Empty(t, senders)
}

func TestEmailAuthRepo_CreateRefreshesDuplicateSender(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewEmailAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Email Duplicate"}
	otherProject := &models.Project{Name: "Email Duplicate Other"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	original := &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "Alice <Alice@Example.COM>", DisplayName: "Alice", AddedBy: "first"}
	require.NoError(t, repo.Create(ctx, original))
	require.Equal(t, "alice@example.com", original.EmailAddress)

	refresh := &models.EmailAuthorizedSender{ProjectID: otherProject.ID, EmailAddress: "ALICE@example.com", DisplayName: "Alice Updated", AddedBy: "second"}
	require.NoError(t, repo.Create(ctx, refresh))
	require.Equal(t, original.ID, refresh.ID)
	senders, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	assert.Equal(t, "alice@example.com", senders[0].EmailAddress)
	assert.Equal(t, "Alice Updated", senders[0].DisplayName)
	assert.Equal(t, "second", senders[0].AddedBy)

	emptyRefresh := &models.EmailAuthorizedSender{ProjectID: otherProject.ID, EmailAddress: "alice@example.com", DisplayName: "", AddedBy: "third"}
	require.NoError(t, repo.Create(ctx, emptyRefresh))
	senders, err = repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, senders, 1)
	assert.Equal(t, "Alice Updated", senders[0].DisplayName)
	assert.Equal(t, "second", senders[0].AddedBy)
}

func TestEmailAuthRepo_SystemAuthorizationAcrossProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewEmailAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Email Unique"}
	otherProject := &models.Project{Name: "Email Other"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, otherProject))
	require.NoError(t, repo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "a@example.com", AddedBy: "test"}))
	require.NoError(t, repo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: otherProject.ID, EmailAddress: "A@EXAMPLE.com", AddedBy: "test"}))

	ok, err := repo.IsAuthorized(ctx, otherProject.ID, "a@example.com")
	require.NoError(t, err)
	require.True(t, ok)
	projectScoped, err := repo.IsAuthorizedForProject(ctx, otherProject.ID, "a@example.com")
	require.NoError(t, err)
	require.False(t, projectScoped)
	senders, err := repo.ListByProject(ctx, otherProject.ID)
	require.NoError(t, err)
	require.Len(t, senders, 1)
}
