package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramUserProjectRepo_SetAndGet(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Get non-existent user should return empty string
	projectID, err := repo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, "", projectID)

	// Set user project
	err = repo.SetUserProject(ctx, "12345", project.ID)
	require.NoError(t, err)

	// Get should return the project ID
	projectID, err = repo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, project.ID, projectID)
}

func TestTelegramUserProjectRepo_UpdateProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create two projects
	project1 := &models.Project{Name: "Project 1"}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Project 2"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	// Set user to project 1
	require.NoError(t, repo.SetUserProject(ctx, "12345", project1.ID))

	projectID, err := repo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, project1.ID, projectID)

	// Update user to project 2
	require.NoError(t, repo.SetUserProject(ctx, "12345", project2.ID))

	projectID, err = repo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, project2.ID, projectID)
}

func TestTelegramUserProjectRepo_MultipleUsers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project1 := &models.Project{Name: "Project A"}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Project B"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	// Two different users with different projects
	require.NoError(t, repo.SetUserProject(ctx, "111", project1.ID))
	require.NoError(t, repo.SetUserProject(ctx, "222", project2.ID))

	p1, err := repo.GetUserProject(ctx, "111")
	require.NoError(t, err)
	assert.Equal(t, project1.ID, p1)

	p2, err := repo.GetUserProject(ctx, "222")
	require.NoError(t, err)
	assert.Equal(t, project2.ID, p2)
}

func TestTelegramUserProjectRepo_DeleteUserProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Delete Test"}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Set and then delete
	require.NoError(t, repo.SetUserProject(ctx, "12345", project.ID))

	projectID, err := repo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, project.ID, projectID)

	require.NoError(t, repo.DeleteUserProject(ctx, "12345"))

	// Should return empty after delete
	projectID, err = repo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, "", projectID)
}

func TestTelegramUserProjectRepo_DeleteNonExistent(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramUserProjectRepo(db)
	ctx := context.Background()

	// Delete non-existent should not error
	err := repo.DeleteUserProject(ctx, "nonexistent")
	require.NoError(t, err)
}

func TestTelegramUserProjectRepo_CascadeOnProjectDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Cascade Test"}
	require.NoError(t, projectRepo.Create(ctx, project))

	require.NoError(t, repo.SetUserProject(ctx, "12345", project.ID))

	// Delete the project — should cascade
	require.NoError(t, projectRepo.Delete(ctx, project.ID))

	// User's project preference should be gone
	projectID, err := repo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, "", projectID)
}

func TestTelegramUserProjectRepo_InvalidProjectID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramUserProjectRepo(db)
	ctx := context.Background()

	// Setting with a non-existent project ID should fail due to FK constraint
	err := repo.SetUserProject(ctx, "12345", "nonexistent-project-id")
	assert.Error(t, err)
}
