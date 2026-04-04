package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackUserProjectRepo_SetGetDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSlackUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Slack Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	require.NoError(t, repo.SetUserProject(ctx, "T111", "U111", project.ID))

	got, err := repo.GetUserProject(ctx, "T111", "U111")
	require.NoError(t, err)
	require.Equal(t, project.ID, got)

	require.NoError(t, repo.DeleteUserProject(ctx, "T111", "U111"))

	got, err = repo.GetUserProject(ctx, "T111", "U111")
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestSlackUserProjectRepo_Upsert(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSlackUserProjectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	p1 := &models.Project{Name: "Slack Project One"}
	require.NoError(t, projectRepo.Create(ctx, p1))
	p2 := &models.Project{Name: "Slack Project Two"}
	require.NoError(t, projectRepo.Create(ctx, p2))

	require.NoError(t, repo.SetUserProject(ctx, "T222", "U222", p1.ID))
	require.NoError(t, repo.SetUserProject(ctx, "T222", "U222", p2.ID))

	got, err := repo.GetUserProject(ctx, "T222", "U222")
	require.NoError(t, err)
	require.Equal(t, p2.ID, got)
}
