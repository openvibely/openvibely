package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	for _, role := range []string{"task", "create_notification", "native_approval", "create_github_issue", "github_assignment", "github_inbox", "implementation", "open_pull_request", "pull_request_review", "completed"} {
		require.Contains(t, first.SupportedRoles, role, "Describe It must see every surfaced custom capability role")
	}
}

func TestAutomationDescriptionGenerationSupportsExpandedCustomBuilderContract(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Review proposed changes"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "morning", Name: "Morning", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "review", Name: "Review changes", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review one focused change.", "category": "backlog", "priority": 2}},
		{Key: "notify", Name: "Request approval", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Summarize the proposed change."}},
		{Key: "approve", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
		{Key: "accepted", Name: "Accepted", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		{Key: "rejected", Name: "Rejected", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "morning_review", From: "morning", To: "review", Condition: map[string]any{}},
		{Key: "review_notify", From: "review", To: "notify", Condition: map[string]any{}},
		{Key: "notify_approve", From: "notify", To: "approve", Condition: map[string]any{}},
		{Key: "approve_accepted", From: "approve", To: "accepted", Condition: map[string]any{"state": "approved"}},
		{Key: "approve_rejected", From: "approve", To: "rejected", Condition: map[string]any{"state": "rejected"}},
	}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)

	preview, err := svc.PreviewDescription(context.Background(), "Every morning review a change and ask me to approve or reject it", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		require.Contains(t, prompt, "adapter_key custom")
		require.Contains(t, prompt, "A Schedule or Agent task may connect")
		require.Contains(t, prompt, "standalone ordinary task")
		require.Contains(t, prompt, "fan out")
		require.NotContains(t, prompt, "Do not branch Agent tasks")
		require.Contains(t, prompt, "create_notification")
		require.Contains(t, prompt, "native_approval")
		require.Contains(t, prompt, "create_github_issue")
		require.Contains(t, prompt, "github_assignment")
		require.Contains(t, prompt, "github_inbox")
		require.Contains(t, prompt, "implementation")
		require.Contains(t, prompt, "open_pull_request")
		require.Contains(t, prompt, "pull_request_review")
		require.NotContains(t, prompt, "existing_workflow")
		return string(candidateJSON), nil
	})
	require.NoError(t, err)
	require.Equal(t, AutomationAdapterCustom, preview.Candidate.AdapterKey)
	require.Empty(t, preview.ValidationErrors)
	expected, err := DecodeAutomationDraftCandidate(candidateJSON)
	require.NoError(t, err)
	expected, err = svc.NormalizeCandidate(expected)
	require.NoError(t, err)
	require.Equal(t, expected.Nodes, preview.Candidate.Nodes)
	require.Equal(t, expected.Edges, preview.Candidate.Edges)
}

func TestAutomationDescriptionGenerationNormalizesOutOfRangeTaskPriority(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Urgent review"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review the request.", "category": "backlog", "priority": 5}},
		{Key: "reminder", Name: "Reminder", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the reminder.", "category": "scheduled", "priority": 0, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
	}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	calls := 0

	preview, err := svc.PreviewDescription(context.Background(), "Urgently review the request", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		calls++
		require.Contains(t, prompt, "priority must be an integer from 1 to 4")
		return string(candidateJSON), nil
	})

	require.NoError(t, err)
	require.Equal(t, 1, calls, "a safely normalizable generated value must not consume the repair attempt")
	priority, ok := draftInt(preview.Candidate.Nodes[0].Config["priority"])
	require.True(t, ok)
	require.Equal(t, 4, priority)
	schedulePriority, ok := draftInt(preview.Candidate.Nodes[1].Config["priority"])
	require.True(t, ok)
	require.Equal(t, 1, schedulePriority)

	manualCandidate, err := DecodeAutomationDraftCandidate(candidateJSON)
	require.NoError(t, err)
	manualCandidate, err = svc.NormalizeCandidate(manualCandidate)
	require.NoError(t, err)
	issues := svc.ValidateCandidate(manualCandidate)
	require.Contains(t, issues, models.AutomationValidationIssue{NodeKey: "review", Code: "priority", Message: "Task priority must be between 1 and 4."}, "normal Save validation must remain strict")
	require.Contains(t, issues, models.AutomationValidationIssue{NodeKey: "reminder", Code: "priority", Message: "Schedule task priority must be between 1 and 4."}, "normal Save validation must remain strict")
}

func TestAutomationDescriptionGenerationRepairsUnsupportedSchemaVersion(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	unsupported := candidate
	unsupported.SchemaVersion = 2
	unsupportedJSON, err := json.Marshal(unsupported)
	require.NoError(t, err)
	validJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	calls := 0

	preview, err := svc.PreviewDescription(context.Background(), "Review vision daily", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		calls++
		if calls == 1 {
			return string(unsupportedJSON), nil
		}
		require.Contains(t, prompt, "schema_version")
		return string(validJSON), nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, 1, preview.Candidate.SchemaVersion)
}

func TestAutomationDescriptionGenerationRepairReceivesExactNestedSchema(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	validJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	invalidJSON := strings.NewReplacer(`"from":`, `"source":`, `"to":`, `"target":`).Replace(string(validJSON))
	calls := 0

	preview, err := svc.PreviewDescription(context.Background(), "Review vision daily", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		calls++
		if calls == 1 {
			return invalidJSON, nil
		}
		require.Contains(t, prompt, "Edges use exactly these fields: key, from, to, from_port, to_port, label, condition.")
		require.Contains(t, prompt, "Never use source or target as edge field names.")
		require.Contains(t, prompt, "Review vision daily", "the independent repair call must receive the original request context")
		return string(validJSON), nil
	})

	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, candidate.Edges, preview.Candidate.Edges)
}

type automationCapabilityGitHubStatusStub struct {
	status GitHubConnectionStatus
	err    error
}

func (s automationCapabilityGitHubStatusStub) GetConnectionStatus(context.Context) (GitHubConnectionStatus, error) {
	return s.status, s.err
}

func TestAutomationCapabilitySnapshotRequiresUsableConnectedGitHubMode(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "GitHub snapshot")
	settingsRepo := repository.NewSettingsRepo(db)
	builder := NewAutomationCapabilitySnapshotBuilder(projectRepo, nil, nil, settingsRepo)

	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	snapshot, err := builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, snapshot.Integrations["github"].Configured, "selecting PAT mode without a credential or connection is not configured")

	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "secret-not-rendered"))
	builder.SetGitHubConnectionProvider(automationCapabilityGitHubStatusStub{status: GitHubConnectionStatus{AuthMode: GitHubAuthModePAT, Configured: true, Connected: false, HasPAT: true}})
	snapshot, err = builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, snapshot.Integrations["github"].Configured, "usable credentials without a connected status are not configured")

	builder.SetGitHubConnectionProvider(automationCapabilityGitHubStatusStub{status: GitHubConnectionStatus{AuthMode: GitHubAuthModePAT, Configured: true, Connected: true, HasPAT: true}})
	snapshot, err = builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.True(t, snapshot.Integrations["github"].Configured)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-not-rendered")

	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModeApp))
	builder.SetGitHubConnectionProvider(automationCapabilityGitHubStatusStub{status: GitHubConnectionStatus{AuthMode: GitHubAuthModeApp, Configured: true, Connected: false, AppConfigured: true}})
	snapshot, err = builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, snapshot.Integrations["github"].Configured)
	builder.SetGitHubConnectionProvider(automationCapabilityGitHubStatusStub{status: GitHubConnectionStatus{AuthMode: GitHubAuthModeApp, Configured: true, Connected: true, AppConfigured: true, InstallationID: "42"}})
	snapshot, err = builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.True(t, snapshot.Integrations["github"].Configured)
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
