package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestChannelTargetRepo_ReplaceProjectTargetsDeletesRemovedRowsBeforeInsert(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Replace Targets Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	repo := NewChannelTargetRepo(db)

	first := models.ChannelTarget{ID: "target-keep", ProjectID: project.ID, Platform: "email", Name: "keep", TargetID: "keep@example.com"}
	removed := models.ChannelTarget{ID: "target-removed", ProjectID: project.ID, Platform: "email", TargetID: "restore@example.com"}
	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{first, removed}))
	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{first}))

	readded := models.ChannelTarget{ID: "target-readded", ProjectID: project.ID, Platform: "email", TargetID: "restore@example.com"}
	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{first, readded}))

	targets, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	foundReadded, err := repo.GetByID(ctx, "target-readded")
	require.NoError(t, err)
	require.NotNil(t, foundReadded)
	require.Equal(t, "restore@example.com", foundReadded.TargetID)
}

func TestChannelTargetRepo_UpsertAndReplaceShareNormalizationAndHomeClearing(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Shared Persistence Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	repo := NewChannelTargetRepo(db)

	require.NoError(t, repo.Upsert(ctx, models.ChannelTarget{ID: "upsert-home", ProjectID: project.ID, Platform: " Email ", Name: " Team ", TargetID: "Team@Example.com", Home: true, DefaultSubject: " Subject "}))
	upserted, err := repo.GetByID(ctx, "upsert-home")
	require.NoError(t, err)
	require.NotNil(t, upserted)
	require.Equal(t, "email", upserted.Platform)
	require.Equal(t, "email", upserted.TargetKind)
	require.Equal(t, "team", upserted.Name)
	require.Equal(t, "Team@Example.com", upserted.TargetID)
	require.Equal(t, "Subject", upserted.DefaultSubject)
	require.True(t, upserted.Home)

	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{
		{ID: "replace-home", Platform: " Telegram ", Name: " Alerts ", TargetID: "-100123", ThreadID: " 42 ", Home: true},
		{ID: "replace-slack", Platform: " Slack ", Name: " Ops ", TargetID: "COPS"},
	}))
	replacedHome, err := repo.GetByID(ctx, "replace-home")
	require.NoError(t, err)
	require.NotNil(t, replacedHome)
	require.Equal(t, project.ID, replacedHome.ProjectID)
	require.Equal(t, "telegram", replacedHome.Platform)
	require.Equal(t, "chat", replacedHome.TargetKind)
	require.Equal(t, "alerts", replacedHome.Name)
	require.Equal(t, "42", replacedHome.ThreadID)
	require.True(t, replacedHome.Home)
	replacedSlack, err := repo.GetByID(ctx, "replace-slack")
	require.NoError(t, err)
	require.NotNil(t, replacedSlack)
	require.Equal(t, "slack", replacedSlack.Platform)
	require.Equal(t, "channel", replacedSlack.TargetKind)
	require.Equal(t, "ops", replacedSlack.Name)
	oldHome, err := repo.GetByID(ctx, "upsert-home")
	require.NoError(t, err)
	require.Nil(t, oldHome, "replace deletes omitted rows before using the shared upsert helper")

	require.NoError(t, repo.Upsert(ctx, models.ChannelTarget{ID: "telegram-new-home", ProjectID: project.ID, Platform: "telegram", TargetID: "-100456", Home: true}))
	replacedHome, err = repo.GetByID(ctx, "replace-home")
	require.NoError(t, err)
	require.NotNil(t, replacedHome)
	require.False(t, replacedHome.Home, "Upsert must clear previous homes for the same platform using the shared helper")
	newHome, err := repo.GetByID(ctx, "telegram-new-home")
	require.NoError(t, err)
	require.NotNil(t, newHome)
	require.True(t, newHome.Home)
}

func TestChannelTargetRepo_CRUDAndAudit(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Targets Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	repo := NewChannelTargetRepo(db)

	require.NoError(t, repo.Upsert(ctx, models.ChannelTarget{ID: "target-1", ProjectID: project.ID, Platform: "Slack", Name: "Ops", TargetID: "C123", ThreadID: "169.1", Home: true}))
	home, err := repo.FindHome(ctx, project.ID, "slack")
	require.NoError(t, err)
	require.NotNil(t, home)
	require.Equal(t, "ops", home.Name)
	named, err := repo.FindByName(ctx, project.ID, "slack", "ops")
	require.NoError(t, err)
	require.Equal(t, "C123", named.TargetID)
	byTarget, err := repo.FindByTarget(ctx, project.ID, "slack", "C123", "169.1")
	require.NoError(t, err)
	require.NotNil(t, byTarget)

	require.NoError(t, repo.RecordSend(ctx, models.ChannelMessageSend{ID: "send-1", ProjectID: project.ID, Platform: "slack", TargetID: "C123", ThreadID: "169.1", RequestedBySurface: "web", MessagePreview: "hello", Success: true}))
	sends, err := repo.ListSendsByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, sends, 1)
	require.Equal(t, "web", sends[0].RequestedBySurface)

	require.NoError(t, repo.Delete(ctx, "target-1"))
	missing, err := repo.FindByName(ctx, project.ID, "slack", "ops")
	require.NoError(t, err)
	require.Nil(t, missing)
}
