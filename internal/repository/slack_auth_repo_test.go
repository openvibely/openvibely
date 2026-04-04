package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackAuthRepo_CRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSlackAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Slack Auth Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	users, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, users, 0)

	user := &models.SlackAuthorizedUser{
		ProjectID:   project.ID,
		SlackUserID: "U12345",
		DisplayName: "Alice",
		AddedBy:     "web",
	}
	require.NoError(t, repo.Create(ctx, user))
	require.NotEmpty(t, user.ID)
	require.False(t, user.AddedAt.IsZero())

	loaded, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "U12345", loaded.SlackUserID)
	assert.Equal(t, "Alice", loaded.DisplayName)

	users, err = repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)

	require.NoError(t, repo.Delete(ctx, user.ID))
	users, err = repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, users, 0)

	require.Error(t, repo.Delete(ctx, "missing-id"))
}

func TestSlackAuthRepo_AuthorizationChecks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSlackAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Slack Auth Check"}
	require.NoError(t, projectRepo.Create(ctx, project))

	hasAny, err := repo.HasAnyAuthorizedUsers(ctx, project.ID)
	require.NoError(t, err)
	assert.False(t, hasAny)

	hasAnyAnywhere, err := repo.HasAnyAuthorizedUsersAnywhere(ctx)
	require.NoError(t, err)
	assert.False(t, hasAnyAnywhere)

	authorized, err := repo.IsAuthorized(ctx, project.ID, "U999")
	require.NoError(t, err)
	assert.False(t, authorized)

	authorizedAnywhere, err := repo.IsAuthorizedAnywhere(ctx, "U999")
	require.NoError(t, err)
	assert.False(t, authorizedAnywhere)

	require.NoError(t, repo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID:   project.ID,
		SlackUserID: "U111",
		DisplayName: "Allowed",
		AddedBy:     "test",
	}))

	hasAny, err = repo.HasAnyAuthorizedUsers(ctx, project.ID)
	require.NoError(t, err)
	assert.True(t, hasAny)

	hasAnyAnywhere, err = repo.HasAnyAuthorizedUsersAnywhere(ctx)
	require.NoError(t, err)
	assert.True(t, hasAnyAnywhere)

	authorized, err = repo.IsAuthorized(ctx, project.ID, "U111")
	require.NoError(t, err)
	assert.True(t, authorized)

	authorized, err = repo.IsAuthorized(ctx, project.ID, "U222")
	require.NoError(t, err)
	assert.False(t, authorized)

	authorizedAnywhere, err = repo.IsAuthorizedAnywhere(ctx, "U111")
	require.NoError(t, err)
	assert.True(t, authorizedAnywhere)
}

func TestSlackAuthRepo_UniqueConstraint(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSlackAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Slack Unique"}
	require.NoError(t, projectRepo.Create(ctx, project))

	require.NoError(t, repo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID:   project.ID,
		SlackUserID: "UDUP",
		DisplayName: "Original",
		AddedBy:     "test",
	}))

	err := repo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID:   project.ID,
		SlackUserID: "UDUP",
		DisplayName: "Duplicate",
		AddedBy:     "test",
	})
	require.Error(t, err)
}
