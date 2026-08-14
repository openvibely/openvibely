package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscordAuthRepo_CRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewDiscordAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Discord Auth Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	users, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, users, 0)

	user := &models.DiscordAuthorizedUser{
		ProjectID:     project.ID,
		DiscordUserID: "12345",
		DisplayName:   "Alice",
		AddedBy:       "web",
	}
	require.NoError(t, repo.Create(ctx, user))
	require.NotEmpty(t, user.ID)
	require.False(t, user.AddedAt.IsZero())

	loaded, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "12345", loaded.DiscordUserID)
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

func TestDiscordAuthRepo_CreateRefreshesDuplicateUser(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewDiscordAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Discord Duplicate"}
	otherProject := &models.Project{Name: "Discord Duplicate Other"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	original := &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: " 12345 ", DisplayName: "Alice", AddedBy: "first"}
	require.NoError(t, repo.Create(ctx, original))
	require.Equal(t, "12345", original.DiscordUserID)

	refresh := &models.DiscordAuthorizedUser{ProjectID: otherProject.ID, DiscordUserID: "12345", DisplayName: "Alice Updated", AddedBy: "second"}
	require.NoError(t, repo.Create(ctx, refresh))
	require.Equal(t, original.ID, refresh.ID)
	users, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "Alice Updated", users[0].DisplayName)
	assert.Equal(t, "second", users[0].AddedBy)

	emptyRefresh := &models.DiscordAuthorizedUser{ProjectID: otherProject.ID, DiscordUserID: " 12345 ", DisplayName: "", AddedBy: "third"}
	require.NoError(t, repo.Create(ctx, emptyRefresh))
	users, err = repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "Alice Updated", users[0].DisplayName)
	assert.Equal(t, "third", users[0].AddedBy)
}

func TestDiscordAuthRepo_DeleteByProjectClearsSystemAllowlist(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewDiscordAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Discord Delete Project"}
	otherProject := &models.Project{Name: "Discord Keep Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	require.NoError(t, repo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "111", DisplayName: "A", AddedBy: "test"}))
	require.NoError(t, repo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "222", DisplayName: "B", AddedBy: "test"}))
	require.NoError(t, repo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: otherProject.ID, DiscordUserID: "333", DisplayName: "C", AddedBy: "test"}))

	require.NoError(t, repo.DeleteByProject(ctx, project.ID))

	users, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	assert.Len(t, users, 0)
	otherUsers, err := repo.ListByProject(ctx, otherProject.ID)
	require.NoError(t, err)
	assert.Len(t, otherUsers, 0)
}

func TestDiscordAuthRepo_AuthorizationChecks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewDiscordAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Discord Auth Check"}
	require.NoError(t, projectRepo.Create(ctx, project))

	hasAny, err := repo.HasAnyAuthorizedUsers(ctx, project.ID)
	require.NoError(t, err)
	assert.False(t, hasAny)

	hasAnyAnywhere, err := repo.HasAnyAuthorizedUsersAnywhere(ctx)
	require.NoError(t, err)
	assert.False(t, hasAnyAnywhere)

	authorized, err := repo.IsAuthorized(ctx, project.ID, "999")
	require.NoError(t, err)
	assert.False(t, authorized)

	authorizedAnywhere, err := repo.IsAuthorizedAnywhere(ctx, "999")
	require.NoError(t, err)
	assert.False(t, authorizedAnywhere)

	require.NoError(t, repo.Create(ctx, &models.DiscordAuthorizedUser{
		ProjectID:     project.ID,
		DiscordUserID: "111",
		DisplayName:   "Allowed",
		AddedBy:       "test",
	}))

	hasAny, err = repo.HasAnyAuthorizedUsers(ctx, project.ID)
	require.NoError(t, err)
	assert.True(t, hasAny)

	hasAnyAnywhere, err = repo.HasAnyAuthorizedUsersAnywhere(ctx)
	require.NoError(t, err)
	assert.True(t, hasAnyAnywhere)

	authorized, err = repo.IsAuthorized(ctx, project.ID, "111")
	require.NoError(t, err)
	assert.True(t, authorized)

	authorized, err = repo.IsAuthorized(ctx, project.ID, "222")
	require.NoError(t, err)
	assert.False(t, authorized)

	authorizedAnywhere, err = repo.IsAuthorizedAnywhere(ctx, "111")
	require.NoError(t, err)
	assert.True(t, authorizedAnywhere)
}

func TestDiscordAuthRepo_SystemAuthorizationAcrossProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewDiscordAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Discord Unique"}
	otherProject := &models.Project{Name: "Discord Other"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	require.NoError(t, repo.Create(ctx, &models.DiscordAuthorizedUser{
		ProjectID:     project.ID,
		DiscordUserID: "123456789012345678",
		DisplayName:   "Original",
		AddedBy:       "test",
	}))
	require.NoError(t, repo.Create(ctx, &models.DiscordAuthorizedUser{
		ProjectID:     otherProject.ID,
		DiscordUserID: "123456789012345678",
		DisplayName:   "Duplicate",
		AddedBy:       "test",
	}))

	authorized, err := repo.IsAuthorized(ctx, otherProject.ID, "123456789012345678")
	require.NoError(t, err)
	require.True(t, authorized)
	projectScoped, err := repo.IsAuthorizedForProject(ctx, otherProject.ID, "123456789012345678")
	require.NoError(t, err)
	require.False(t, projectScoped)
	users, err := repo.ListByProject(ctx, otherProject.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
}
