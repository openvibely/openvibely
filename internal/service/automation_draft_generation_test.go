package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationCapabilitySnapshotIsBoundedDeterministicAndSecretFree(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Snapshot")
	agentRepo := repository.NewAgentRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	agent := models.Agent{Name: "Builder", Key: "builder", Scope: models.AgentScopeProject, ProjectID: project.ID, Enabled: true, SelectableAsPrimary: true, SystemPrompt: "secret prompt", Tools: []string{"Read", "Write"}, Skills: []models.SkillConfig{{Name: "review", Description: "Review code", Content: "private skill body"}}}
	require.NoError(t, agentRepo.Create(context.Background(), &agent))
	task := models.Task{ProjectID: project.ID, Title: "Reusable Inbox", Prompt: "private task prompt", Category: models.CategoryBacklog, Priority: 2, Status: models.StatusPending}
	require.NoError(t, taskRepo.Create(context.Background(), &task))
	require.NoError(t, settingsRepo.Set(context.Background(), GitHubSettingAuthMode, GitHubAuthModePAT))
	require.NoError(t, settingsRepo.Set(context.Background(), GitHubSettingPAT, "ghp_do_not_expose"))

	builder := NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, taskRepo, settingsRepo)
	first, err := builder.Build(context.Background(), project.ID)
	require.NoError(t, err)
	second, err := builder.Build(context.Background(), project.ID)
	require.NoError(t, err)
	require.Equal(t, first, second)
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret prompt")
	require.NotContains(t, string(encoded), "private skill body")
	require.NotContains(t, string(encoded), "private task prompt")
	require.NotContains(t, string(encoded), "ghp_do_not_expose")
	require.LessOrEqual(t, len(first.Agents), 50)
	require.LessOrEqual(t, len(first.ReusableResources), 50)
}

func TestAutomationDescriptionGenerationUsesOneRepairAndNoPersistence(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Describe")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())
	valid, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	valid.Name = "Daily Vision Review"
	validJSON, err := json.Marshal(valid)
	require.NoError(t, err)
	calls := 0
	generator := func(_ context.Context, prompt string) (string, error) {
		calls++
		require.Contains(t, prompt, "strict JSON")
		if calls == 1 {
			return `{"adapter_key":"vision_driver","nodes":`, nil
		}
		return string(validJSON), nil
	}

	preview, err := svc.PreviewDescription(context.Background(), "Review vision every day", models.AutomationCapabilitySnapshot{}, generator)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "Daily Vision Review", preview.Candidate.Name)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automations WHERE project_id = ?`, project.ID).Scan(&count))
	require.Zero(t, count, "preview must remain ephemeral")

	calls = 0
	_, err = svc.PreviewDescription(context.Background(), "Review vision", models.AutomationCapabilitySnapshot{}, func(context.Context, string) (string, error) {
		calls++
		return "not json", errors.New("generation failed")
	})
	require.Error(t, err)
	require.Equal(t, 1, calls, "provider errors are not repaired with another model call")
}
