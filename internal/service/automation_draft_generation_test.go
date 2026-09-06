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
	for _, role := range []string{"task", "create_notification", "native_approval", "native_inbox", "implementation", "create_github_issue", "github_assignment", "github_inbox", "open_pull_request", "pull_request_review", "completed"} {
		require.Contains(t, first.SupportedRoles, role, "Describe It must see every surfaced custom capability role")
	}
}

func TestAutomationDescriptionGenerationEnforcesGitHubCapabilityReadiness(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	githubCandidate, err := svc.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	githubJSON, err := json.Marshal(githubCandidate)
	require.NoError(t, err)
	fallback, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	fallback.Name = "Local review"
	fallback.Nodes = []models.AutomationDraftNode{{
		Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Review the local project.", "category": "backlog", "priority": 2},
	}}
	fallbackJSON, err := json.Marshal(fallback)
	require.NoError(t, err)

	t.Run("unconfigured GitHub is repaired", func(t *testing.T) {
		var snapshot models.AutomationCapabilitySnapshot
		snapshot.Integrations = map[string]models.AutomationIntegrationCapability{"github": {Configured: false}}
		calls := 0
		preview, err := svc.PreviewDescription(context.Background(), "Create a GitHub delivery workflow", snapshot,
			func(_ context.Context, prompt string) (string, error) {
				calls++
				if calls == 1 {
					return string(githubJSON), nil
				}
				require.Contains(t, prompt, "github_unavailable")
				return string(fallbackJSON), nil
			})
		require.NoError(t, err)
		require.Equal(t, 2, calls)
		require.Equal(t, AutomationAdapterCustom, preview.Candidate.AdapterKey)
	})

	t.Run("configured GitHub is accepted", func(t *testing.T) {
		var snapshot models.AutomationCapabilitySnapshot
		snapshot.Integrations = map[string]models.AutomationIntegrationCapability{"github": {Configured: true}}
		calls := 0
		preview, err := svc.PreviewDescription(context.Background(), "Create a GitHub delivery workflow", snapshot,
			func(_ context.Context, _ string) (string, error) {
				calls++
				return string(githubJSON), nil
			})
		require.NoError(t, err)
		require.Equal(t, 1, calls)
		require.Equal(t, AutomationAdapterGitHubSDLC, preview.Candidate.AdapterKey)
	})
}

func TestAutomationDescriptionPromptExposesOnlyExecutableCustomCapabilities(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Review request"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review the request.", "category": "backlog", "priority": 2}},
	}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)

	var snapshot models.AutomationCapabilitySnapshot
	snapshot.Project.ID = "project-secret-id"
	snapshot.Project.Name = "Prompt project"
	snapshot.SupportedNodeTypes = []models.AutomationNodeType{models.AutomationNodeCondition, models.AutomationNodeAgentTask}
	snapshot.SupportedRoles = []string{"implementation", "task", "vision_driver"}
	snapshot.Agents = []models.AutomationCapabilityRef{{ID: "market-reader", Name: "Market Reader", Capabilities: []string{"WebSearch", "Read"}}}
	snapshot.ReusableResources = []models.AutomationCapabilityRef{{ID: "existing-task-id", Name: "Existing task"}}
	snapshot.Integrations = map[string]models.AutomationIntegrationCapability{"native": {Configured: true}, "github": {Configured: false}}

	_, err = svc.PreviewDescription(context.Background(), "Review a request", snapshot, func(_ context.Context, prompt string) (string, error) {
		start := strings.Index(prompt, "Project capability snapshot for custom graphs:\n")
		require.NotEqual(t, -1, start)
		start += len("Project capability snapshot for custom graphs:\n")
		end := strings.Index(prompt[start:], "\n\nUser description:")
		require.NotEqual(t, -1, end)
		var exposed map[string]any
		require.NoError(t, json.Unmarshal([]byte(prompt[start:start+end]), &exposed))
		require.Equal(t, []any{"action", "agent_task", "human_gate", "outcome", "trigger"}, exposed["supported_node_types"])
		require.Equal(t, []any{"completed", "create_github_issue", "create_notification", "fixed_schedule", "github_assignment", "github_inbox", "implementation", "native_approval", "native_inbox", "open_pull_request", "pull_request_review", "task"}, exposed["supported_roles"])
		require.NotContains(t, exposed, "skills")
		require.NotContains(t, exposed, "source_files")
		require.NotContains(t, exposed, "reusable_resources")
		project := exposed["project"].(map[string]any)
		require.Equal(t, "Prompt project", project["name"])
		require.NotContains(t, project, "id")
		require.Contains(t, prompt[start:start+end], `"capabilities":["WebSearch","Read"]`)
		return string(candidateJSON), nil
	})
	require.NoError(t, err)
}

func TestAutomationDescriptionPromptMatchesStrictCustomValidationContract(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Review request"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review the request.", "category": "backlog", "priority": 2}},
	}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)

	_, err = svc.PreviewDescription(context.Background(), "Review a request", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		require.Contains(t, prompt, "Automation name must be a non-empty string of at most 200 characters")
		require.Contains(t, prompt, "at most 50 nodes and 100 edges")
		require.Contains(t, prompt, "Every node key and edge key must be non-empty and unique")
		require.Contains(t, prompt, "Every edge must use from_port right and to_port left")
		require.Contains(t, prompt, "category must be exactly active or backlog")
		require.Contains(t, prompt, "repeat_type must be exactly one of once, minutes, hours, daily, weekly, monthly")
		require.Contains(t, prompt, "repeat_interval must be an integer from 1 to 365")
		require.Contains(t, prompt, "notification_type must be a non-empty string of at most 100 characters")
		require.Contains(t, prompt, "instructions must be a non-empty string of at most 2000 characters")
		require.Contains(t, prompt, "labels must be an array of at most 10")
		require.Contains(t, prompt, "base must be a string")
		require.Contains(t, prompt, "draft must be a boolean")
		require.Contains(t, prompt, "GitHub inbox is itself the scheduled Task")
		require.Contains(t, prompt, "never add a separate Schedule before it")
		require.Contains(t, prompt, "configuration-only projection")
		require.Contains(t, prompt, "Save does not create that issue-specific Task")
		require.Contains(t, prompt, "node name must make that projection explicit")
		require.NotContains(t, prompt, "exactly one Schedule source and exactly one Human assignment source")
		require.NotContains(t, prompt, "with a separate substantive Schedule -> GitHub inbox source")
		require.Contains(t, prompt, "exactly one Human assignment source")
		require.Contains(t, prompt, "exactly one Task target")
		require.Contains(t, prompt, "that Task must use category active")
		require.Contains(t, prompt, "human assignment starts implementation immediately")
		require.Contains(t, prompt, "Open pull request must have exactly one incoming edge")
		require.Contains(t, prompt, "Human review must have exactly one outgoing edge to one Outcome")
		require.Contains(t, prompt, "Native mailbox family")
		require.Contains(t, prompt, "Human approval -> Approved inbox -> Implementation -> Outcome")
		require.Contains(t, prompt, "Approved inbox is itself the scheduled Task")
		require.Contains(t, prompt, "Process approved notifications owned by this same Automation in the current project")
		require.Contains(t, prompt, "connected upstream producers are sources and context, not a graph-branch eligibility limit")
		require.Contains(t, prompt, "durable project + Automation + notification ownership plus this current Native inbox execution")
		require.NotContains(t, prompt, "Only process approved notifications created by connected upstream producers on that inbox's own approval branch in the same Automation")
		require.NotContains(t, prompt, "exact trusted Automation/graph/branch provenance rather than content similarity or model-supplied metadata")
		require.Contains(t, prompt, "Call list_alerts without project_id, using decision_state=approved, implementation_task_linked=false")
		require.Contains(t, prompt, "Do not pass processing_state, read, type, or source")
		require.Contains(t, prompt, "every recovery-eligible processing state")
		require.Contains(t, prompt, "Never search for or reuse a project ID")
		require.Contains(t, prompt, "GitHub mailbox family")
		require.Contains(t, prompt, "process open issues assigned to the PAT owner or configured GitHub Authorized Users")
		require.Contains(t, prompt, "whether the issue was created by this Automation or manually in GitHub")
		require.Contains(t, prompt, "issue sources only, not as an eligibility limit")
		require.NotContains(t, prompt, "only process assigned issues created by connected upstream producers on that inbox's own assignment branch in the same Automation")
		require.NotContains(t, prompt, "exact trusted local Automation/graph/producer-branch creation records rather than labels or issue-content similarity")
		require.Contains(t, prompt, "Never combine Native mailbox nodes and GitHub mailbox nodes in one custom graph")
		require.Contains(t, prompt, "If requested work depends on an external capability")
		require.Contains(t, prompt, "add an explicit warning")
		return string(candidateJSON), nil
	})
	require.NoError(t, err)
}

func TestAutomationDescriptionPromptIncludesCanonicalMaintainedAdapters(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	valid, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	valid.Name = "Review request"
	valid.Nodes = []models.AutomationDraftNode{
		{Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review the request.", "category": "backlog", "priority": 2}},
	}
	validJSON, err := json.Marshal(valid)
	require.NoError(t, err)

	_, err = svc.PreviewDescription(context.Background(), "Review a request", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
			canonical, candidateErr := svc.TemplateCandidate(adapterKey)
			require.NoError(t, candidateErr)
			encoded, marshalErr := json.Marshal(canonical)
			require.NoError(t, marshalErr)
			require.Contains(t, prompt, string(encoded), "Describe It must receive the registry-derived canonical %s topology", adapterKey)
		}
		visionDriver, candidateErr := svc.TemplateCandidate(AutomationAdapterVisionDriver)
		require.NoError(t, candidateErr)
		encodedVisionDriver, marshalErr := json.Marshal(visionDriver)
		require.NoError(t, marshalErr)
		require.NotContains(t, prompt, string(encodedVisionDriver), "Describe It must not advertise the retired Vision Driver template")
		return string(validJSON), nil
	})
	require.NoError(t, err)
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
		{Key: "morning_review", From: "morning", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "review_notify", From: "review", To: "notify", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "notify_approve", From: "notify", To: "approve", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "approve_accepted", From: "approve", To: "accepted", FromPort: "right", ToPort: "left", Condition: map[string]any{"state": "approved"}},
		{Key: "approve_rejected", From: "approve", To: "rejected", FromPort: "right", ToPort: "left", Condition: map[string]any{"state": "rejected"}},
	}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)

	preview, err := svc.PreviewDescription(context.Background(), "Every morning review a change and ask me to approve or reject it", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		require.Contains(t, prompt, "adapter_key custom")
		require.Contains(t, prompt, "A Schedule or Task may connect")
		require.Contains(t, prompt, "A Schedule is the scheduled Task")
		require.Contains(t, prompt, "Schedule -> Task is an explicit downstream handoff")
		require.Contains(t, prompt, "Do not add a separate Task merely to hold the recurring work")
		require.NotContains(t, prompt, "runs that Task directly")
		require.NotContains(t, prompt, "no separate trigger task is created")
		require.Contains(t, prompt, "A Task may also stand alone")
		require.Contains(t, prompt, "fan out")
		require.NotContains(t, prompt, "Do not branch Agent tasks")
		require.Contains(t, prompt, "create_notification")
		require.Contains(t, prompt, "native_approval")
		require.Contains(t, prompt, "create_github_issue")
		require.Contains(t, prompt, "github_assignment")
		require.Contains(t, prompt, "github_inbox")
		require.Contains(t, prompt, "role implementation")
		require.Contains(t, prompt, "Native implementation")
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

func TestAutomationDescriptionGenerationAcceptsRecurringWorkWithoutExtraTask(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Daily signal review"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "daily_review", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Review current public signals and identify whether human attention is needed.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "notify", Name: "Create notification", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "actionable_signal", "instructions": "Create a notification only when the review identifies an actionable signal."}},
		{Key: "approval", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "review_notify", From: "daily_review", To: "notify", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "notify_approval", From: "notify", To: "approval", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)

	preview, err := svc.PreviewDescription(context.Background(), "Review public signals daily and notify me only when action is warranted", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		require.Contains(t, prompt, "Do not add a separate Task merely to hold the recurring work")
		return string(candidateJSON), nil
	})
	require.NoError(t, err)
	require.Empty(t, preview.ValidationErrors)
	require.Len(t, preview.Candidate.Nodes, 3)
	for _, node := range preview.Candidate.Nodes {
		require.NotEqual(t, models.AutomationNodeAgentTask, node.Type, "recurring substantive work must not require an extra Task node")
	}
}

func TestAutomationDescriptionGenerationAddsRequiredNativeApproval(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Daily actionable signal"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "daily_review", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Review public information for actionable signals.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "notify", Name: "Create notification", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "actionable_signal", "instructions": "Create a notification when action is warranted."}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "review_notify", From: "daily_review", To: "notify", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}
	require.Contains(t, svc.ValidateCandidate(candidate), models.AutomationValidationIssue{
		NodeKey: "notify", Code: "notification_target", Message: "A Create notification action needs one Human approval node.",
	}, "manual Save validation must remain strict")
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	calls := 0

	preview, err := svc.PreviewDescription(context.Background(), "Review public information daily and create a native notification for actionable signals", models.AutomationCapabilitySnapshot{}, func(_ context.Context, prompt string) (string, error) {
		calls++
		if calls == 1 {
			return `{"nodes":`, nil
		}
		require.Contains(t, prompt, "Every Create notification MUST have exactly one outgoing edge to a Human approval node")
		return string(candidateJSON), nil
	})

	require.NoError(t, err)
	require.Equal(t, 2, calls, "the repaired candidate must receive deterministic required-gate normalization")
	require.Len(t, preview.Candidate.Nodes, 3)
	require.Len(t, preview.Candidate.Edges, 2)
	approval := preview.Candidate.Nodes[2]
	require.Equal(t, models.AutomationNodeHumanGate, approval.Type)
	require.Equal(t, "native_approval", approval.Role)
	require.Equal(t, "native_alert", approval.Config["approval_method"])
	require.Equal(t, "notify", preview.Candidate.Edges[1].From)
	require.Equal(t, approval.Key, preview.Candidate.Edges[1].To)
	require.Empty(t, svc.ValidateCandidate(preview.Candidate))
}

func TestAutomationDescriptionGenerationRoutesNotificationOutcomeThroughApproval(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Daily actionable signal"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "daily_review", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Review public information for actionable signals.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "notify", Name: "Create notification", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "actionable_signal", "instructions": "Create a notification when action is warranted."}},
		{Key: "completed", Name: "Signal reviewed", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "review_notify", From: "daily_review", To: "notify", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "notify_completed", From: "notify", To: "completed", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	calls := 0

	preview, err := svc.PreviewDescription(context.Background(), "Review public information daily and create a native notification for actionable signals", models.AutomationCapabilitySnapshot{}, func(_ context.Context, _ string) (string, error) {
		calls++
		return string(candidateJSON), nil
	})

	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Len(t, preview.Candidate.Nodes, 4)
	require.Len(t, preview.Candidate.Edges, 3)
	approval := preview.Candidate.Nodes[3]
	require.Equal(t, "notify", preview.Candidate.Edges[2].From)
	require.Equal(t, approval.Key, preview.Candidate.Edges[2].To)
	require.Equal(t, approval.Key, preview.Candidate.Edges[1].From)
	require.Equal(t, "completed", preview.Candidate.Edges[1].To)
	require.Equal(t, map[string]any{"state": "approved"}, preview.Candidate.Edges[1].Condition)
	require.Empty(t, svc.ValidateCandidate(preview.Candidate))
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
		require.Contains(t, prompt, "Call list_alerts without project_id, using decision_state=approved, implementation_task_linked=false")
		require.Contains(t, prompt, "Do not pass processing_state, read, type, or source")
		require.Contains(t, prompt, "every recovery-eligible processing state")
		require.Contains(t, prompt, "Never search for or reuse a project ID")
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

type automationCapabilityGitHubResolverStub struct {
	automationCapabilityGitHubStatusStub
	resolvedURL  string
	resolvedPath string
}

func (s *automationCapabilityGitHubResolverStub) ResolveRepo(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
	s.resolvedURL = repoURL
	s.resolvedPath = repoPath
	return &GitHubRepoRef{
		Owner:    "openvibely",
		Name:     "local-project",
		FullName: "openvibely/local-project",
		HTMLURL:  "https://github.com/openvibely/local-project",
	}, nil
}

func (s *automationCapabilityGitHubResolverStub) GlobalAPIEndpoint(context.Context) string { return "" }

func TestAutomationCapabilitySnapshotAcceptsAuthorizedUserAndLocalGitHubRemote(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Local GitHub snapshot")
	project.RepoPath = "/projects/local-github"
	require.NoError(t, projectRepo.Update(ctx, &project))
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "secret-not-rendered"))
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
	provider := &automationCapabilityGitHubResolverStub{automationCapabilityGitHubStatusStub: automationCapabilityGitHubStatusStub{
		status: GitHubConnectionStatus{AuthMode: GitHubAuthModePAT, Configured: true, Connected: true, HasPAT: true},
	}}
	builder := NewAutomationCapabilitySnapshotBuilder(projectRepo, nil, nil, settingsRepo)
	builder.SetGitHubAuthRepository(githubAuthRepo)
	builder.SetGitHubConnectionProvider(provider)

	snapshot, err := builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.True(t, snapshot.Integrations["github"].Configured)
	require.Empty(t, provider.resolvedURL)
	require.Equal(t, project.RepoPath, provider.resolvedPath)
}

func TestAutomationCapabilitySnapshotRequiresUsableConnectedGitHubMode(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "GitHub snapshot")
	settingsRepo := repository.NewSettingsRepo(db)
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	builder := NewAutomationCapabilitySnapshotBuilder(projectRepo, nil, nil, settingsRepo)
	builder.SetGitHubAuthRepository(githubAuthRepo)

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
	require.False(t, snapshot.Integrations["github"].Configured, "GitHub readiness also requires a project repository URL or resolvable local Git remote")

	project.RepoURL = "https://github.com/openvibely/snapshot.git"
	require.NoError(t, projectRepo.Update(ctx, &project))
	snapshot, err = builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, snapshot.Integrations["github"].Configured, "GitHub readiness also requires at least one authorized inbox assignee")
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
	snapshot, err = builder.Build(ctx, project.ID)
	require.NoError(t, err)
	require.True(t, snapshot.Integrations["github"].Configured, "the visible Authorized Users configuration must satisfy inbox readiness")
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
