package service

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationDraftServiceNormalizesRegisteredTemplatesDeterministically(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())

	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC, AutomationAdapterVisionDriver} {
		first, err := svc.TemplateCandidate(adapterKey)
		require.NoError(t, err)
		second, err := svc.NormalizeCandidate(first)
		require.NoError(t, err)
		require.Equal(t, first, second)
		require.Empty(t, svc.ValidateCandidate(second))
		for _, node := range second.Nodes {
			require.NotNil(t, node.Position, "layout must be server-owned for %s/%s", adapterKey, node.Key)
		}
	}
}

func TestAutomationBlankDraftStartsEmptyAndPersistsUserLayout(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Visual blank")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())

	blank, err := svc.BlankCandidate("")
	require.NoError(t, err)
	require.Equal(t, AutomationAdapterCustom, blank.AdapterKey)
	require.Empty(t, blank.Nodes, "Blank must start with an empty custom canvas")
	require.Empty(t, blank.Edges)
	require.Contains(t, issueCodes(svc.ValidateCandidate(blank)), "empty_graph")

	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: blank})
	require.NoError(t, err)
	require.Empty(t, created.Candidate.Nodes)
	require.NotEmpty(t, created.ValidationErrors)

	dragged := models.AutomationDraftNode{Key: "my_schedule", Name: "My schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}, Position: &models.AutomationDraftPoint{X: 37, Y: 83}}
	created.Candidate.Nodes = append(created.Candidate.Nodes, dragged)
	updated, err := svc.UpdateDraft(context.Background(), created.Definition.Automation.ID, created.Definition.Version.ID, project.ID, created.Candidate)
	require.NoError(t, err)
	require.Equal(t, &models.AutomationDraftPoint{X: 37, Y: 83}, updated.Candidate.Nodes[0].Position, "normalization must preserve user-positioned nodes")
}

func TestCustomAutomationValidatesLinearTaskHandoffsAndRejectsAnalogousUnsupportedTopologies(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "research", Name: "Research", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Research the request.", "category": "scheduled", "priority": 2}},
		{Key: "implement", Name: "Implement", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the researched request.", "category": "active", "priority": 2}},
		{Key: "done", Name: "Done", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_research", From: "schedule", To: "research", Condition: map[string]any{}},
		{Key: "research_implement", From: "research", To: "implement", Condition: map[string]any{}},
		{Key: "implement_done", From: "implement", To: "done", Condition: map[string]any{}},
	}

	require.Empty(t, svc.ValidateCandidate(candidate), "a linear Schedule → Agent task → Agent task → Outcome path must publish")

	branch := candidate
	branch.Nodes = append(append([]models.AutomationDraftNode(nil), candidate.Nodes...), models.AutomationDraftNode{
		Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Review the implementation.", "category": "backlog", "priority": 2},
	})
	branch.Edges = append(append([]models.AutomationDraftEdge(nil), candidate.Edges...), models.AutomationDraftEdge{
		Key: "research_review", From: "research", To: "review", Condition: map[string]any{},
	})
	require.Contains(t, issueCodes(svc.ValidateCandidate(branch)), "task_branching")

	cycle := candidate
	cycle.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_research", From: "schedule", To: "research", Condition: map[string]any{}},
		{Key: "research_implement", From: "research", To: "implement", Condition: map[string]any{}},
		{Key: "implement_research", From: "implement", To: "research", Condition: map[string]any{}},
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(cycle)), "unsupported_cycle")

	multipleParents := candidate
	multipleParents.Nodes = append(append([]models.AutomationDraftNode(nil), candidate.Nodes...), models.AutomationDraftNode{
		Key: "second_schedule", Name: "Second schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule",
		Config: map[string]any{"run_at": "10:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true},
	})
	multipleParents.Edges = append(append([]models.AutomationDraftEdge(nil), candidate.Edges...), models.AutomationDraftEdge{
		Key: "second_implement", From: "second_schedule", To: "implement", Condition: map[string]any{},
	})
	require.Contains(t, issueCodes(svc.ValidateCandidate(multipleParents)), "task_parents")
}

func TestCustomAutomationValidatesNativeAlertApprovalHandoffsAndRejectsAnalogousUnsafeBranches(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "review", Name: "Review changes", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review likely changes.", "category": "scheduled", "priority": 2}},
		{Key: "request", Name: "Request approval", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Summarize the proposed change for a human reviewer."}},
		{Key: "human", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
		{Key: "accepted", Name: "Accepted", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		{Key: "declined", Name: "Declined", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_review", From: "schedule", To: "review", Condition: map[string]any{}},
		{Key: "review_request", From: "review", To: "request", Condition: map[string]any{}},
		{Key: "request_human", From: "request", To: "human", Condition: map[string]any{}},
		{Key: "human_accepted", From: "human", To: "accepted", Label: "approved", Condition: map[string]any{"state": "approved"}},
		{Key: "human_declined", From: "human", To: "declined", Label: "rejected", Condition: map[string]any{"state": "rejected"}},
	}

	require.Empty(t, svc.ValidateCandidate(candidate), "a custom native Alert approval path must be publishable")

	multiTask := candidate
	multiTask.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
	multiTask.Nodes = append(multiTask.Nodes, models.AutomationDraftNode{
		Key: "research", Name: "Research first", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Research likely changes.", "category": "scheduled", "priority": 2},
	})
	for i := range multiTask.Nodes {
		if multiTask.Nodes[i].Key == "review" {
			multiTask.Nodes[i].Config = map[string]any{"prompt": "Review likely changes.", "category": "active", "priority": 2}
		}
	}
	multiTask.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	multiTask.Edges[0].To = "research"
	multiTask.Edges = append(multiTask.Edges, models.AutomationDraftEdge{Key: "research_review", From: "research", To: "review", Condition: map[string]any{}})
	require.Empty(t, svc.ValidateCandidate(multiTask), "native approval must compose after an existing task-to-task handoff")

	missingRejected := candidate
	missingRejected.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges[:len(candidate.Edges)-1]...)
	require.Contains(t, issueCodes(svc.ValidateCandidate(missingRejected)), "approval_branches")

	duplicateApproved := candidate
	duplicateApproved.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	duplicateApproved.Edges[len(duplicateApproved.Edges)-1].Condition = map[string]any{"state": "approved"}
	require.Contains(t, issueCodes(svc.ValidateCandidate(duplicateApproved)), "approval_branches")

	unsafeTarget := candidate
	unsafeTarget.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	unsafeTarget.Edges[len(unsafeTarget.Edges)-1].To = "review"
	require.Contains(t, issueCodes(svc.ValidateCandidate(unsafeTarget)), "unsupported_handoff")

	unsupportedCondition := candidate
	unsupportedCondition.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	unsupportedCondition.Edges[1].Condition = map[string]any{"state": "approved"}
	require.Contains(t, issueCodes(svc.ValidateCandidate(unsupportedCondition)), "unsupported_condition")
}

func TestCustomAutomationValidatesGitHubHandoffsAndRejectsHumanBoundaryBypasses(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "producer_schedule", Name: "Daily suggestions", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "producer", Name: "Find improvements", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Find one focused improvement.", "category": "scheduled", "priority": 2}},
		{Key: "issue", Name: "Create issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"instructions": "Open one reviewable suggestion issue.", "labels": []any{"suggestion"}}},
		{Key: "assignment", Name: "Human assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{"approval_method": "github_assignment"}},
		{Key: "inbox_schedule", Name: "Hourly inbox", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "09:15", "repeat_type": "hours", "repeat_interval": 1, "enabled": true}},
		{Key: "inbox", Name: "Process assigned issues", Type: models.AutomationNodeAgentTask, Role: "github_inbox", Config: map[string]any{"prompt": "Process newly assigned issues.", "category": "scheduled", "priority": 3}},
		{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "implementation", Config: map[string]any{"prompt": "Implement the accepted issue and run relevant validation.", "category": "active", "priority": 3}},
		{Key: "open_pr", Name: "Open pull request", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open a reviewable pull request linked to the source issue.", "base": "main", "draft": false}},
		{Key: "review", Name: "Human review", Type: models.AutomationNodeHumanGate, Role: "pull_request_review", Config: map[string]any{"approval_method": "pull_request_review"}},
		{Key: "complete", Name: "Merged", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "producer_schedule_to_producer", From: "producer_schedule", To: "producer", Condition: map[string]any{}},
		{Key: "producer_to_issue", From: "producer", To: "issue", Condition: map[string]any{}},
		{Key: "issue_to_assignment", From: "issue", To: "assignment", Condition: map[string]any{}},
		{Key: "inbox_schedule_to_inbox", From: "inbox_schedule", To: "inbox", Condition: map[string]any{}},
		{Key: "assignment_to_inbox", From: "assignment", To: "inbox", Label: "assigned", Condition: map[string]any{"state": "assigned"}},
		{Key: "inbox_to_implementation", From: "inbox", To: "implementation", Condition: map[string]any{}},
		{Key: "implementation_to_pr", From: "implementation", To: "open_pr", Condition: map[string]any{}},
		{Key: "pr_to_review", From: "open_pr", To: "review", Condition: map[string]any{}},
		{Key: "review_to_complete", From: "review", To: "complete", Condition: map[string]any{}},
	}

	require.Empty(t, svc.ValidateCandidate(candidate), "the GitHub graph must map to the existing assignment, inbox, task, PR, and review machinery")

	missingAssignment := candidate
	missingAssignment.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	missingAssignment.Edges[1].To = "inbox"
	require.Contains(t, issueCodes(svc.ValidateCandidate(missingAssignment)), "unsupported_handoff", "a producer must not bypass human assignment")

	autoAssigned := candidate
	autoAssigned.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
	for i := range autoAssigned.Nodes {
		if autoAssigned.Nodes[i].Key == "issue" {
			autoAssigned.Nodes[i].Config = map[string]any{"instructions": "Open one issue.", "labels": []any{"suggestion"}, "assignees": []any{"bot"}}
		}
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(autoAssigned)), "unknown_config", "issue actions must not assign around the human gate")

	wrongGateResult := candidate
	wrongGateResult.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	wrongGateResult.Edges[4].Condition = map[string]any{"state": "approved"}
	require.Contains(t, issueCodes(svc.ValidateCandidate(wrongGateResult)), "unsupported_condition")

	reviewBypass := candidate
	reviewBypass.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	reviewBypass.Edges[6].To = "complete"
	require.Contains(t, issueCodes(svc.ValidateCandidate(reviewBypass)), "unsupported_handoff", "opening a PR must not skip human review")
}

func TestAutomationFreeformDraftPersistsCustomNodesAndCyclesButCannotPublish(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Freeform draft")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "alpha", Name: "Alpha", Type: models.AutomationNodeAgentTask, Role: "custom_agent_task", Config: map[string]any{"prompt": "Do alpha work", "category": "backlog", "priority": 2}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
		{Key: "beta", Name: "Beta", Type: models.AutomationNodeCondition, Role: "custom_condition", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 260, Y: 0}},
		{Key: "gamma", Name: "Gamma", Type: models.AutomationNodeAction, Role: "custom_action", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 520, Y: 0}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "edge_alpha_beta", From: "alpha", To: "beta", FromPort: "left", ToPort: "right", Condition: map[string]any{}},
		{Key: "edge_beta_gamma", From: "beta", To: "gamma", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "edge_gamma_alpha", From: "gamma", To: "alpha", FromPort: "left", ToPort: "right", Condition: map[string]any{}},
	}

	issues := svc.ValidateCandidate(candidate)
	require.Contains(t, issueCodes(issues), "unsupported_capability", "unsupported capability nodes must remain visibly unpublished")
	require.NotContains(t, issueCodes(issues), "invalid_edge", "a multi-node cycle remains valid saved geometry")
	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Len(t, created.Candidate.Nodes, 3)
	require.Len(t, created.Candidate.Edges, 3)
	require.Equal(t, "left", created.Candidate.Edges[0].FromPort, "chosen connector side must survive draft persistence")
	require.Equal(t, "right", created.Candidate.Edges[0].ToPort, "chosen connector side must survive draft persistence")
	require.Contains(t, issueCodes(created.ValidationErrors), "unsupported_capability")

	planner := NewAutomationPublicationPlanner(repo, nil, nil, svc.registry, svc)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Contains(t, issueCodes(plan.Validation), "unsupported_capability", "unsupported capability graphs must not publish")
	require.Empty(t, plan.Effects, "unsupported topology must not produce runtime resource mutations")
}

func TestAutomationDraftRejectsMissingAndUnsupportedSchemaVersions(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Schema versions")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())

	for _, version := range []int{0, 2} {
		candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
		require.NoError(t, err)
		candidate.SchemaVersion = version
		normalized, err := svc.NormalizeCandidate(candidate)
		require.NoError(t, err)
		require.Equal(t, version, normalized.SchemaVersion, "normalization must preserve the supplied schema version")
		require.Contains(t, issueCodes(svc.ValidateCandidate(normalized)), "schema_version")
		_, err = svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
		require.ErrorContains(t, err, "schema version")
	}

	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	candidate.SchemaVersion = 2
	_, err = svc.UpdateDraft(context.Background(), created.Definition.Automation.ID, created.Definition.Version.ID, project.ID, candidate)
	require.ErrorContains(t, err, "schema version")
	unsupportedJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE automation_draft_metadata SET candidate_json = ? WHERE version_id = ?`, string(unsupportedJSON), created.Definition.Version.ID)
	require.NoError(t, err)
	_, err = svc.GetDraft(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.ErrorContains(t, err, "schema version")
	_, err = svc.ClonePublishedVersion(context.Background(), project.ID, created.Definition.Automation.ID)
	require.ErrorContains(t, err, "schema version")
}

func TestAutomationTaskReferencesResolveInsideSelectedProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Reference project")
	project.RepoPath = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project.RepoPath, "VISION.md"), []byte("vision"), 0o600))
	require.NoError(t, projectRepo.Update(ctx, &project))
	other := automationTestProject(t, projectRepo, "Other reference project")
	agentRepo := repository.NewAgentRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	agent := models.Agent{Name: "Project Architect", Key: "project_architect", Scope: models.AgentScopeProject, ProjectID: project.ID,
		Enabled: true, SelectableAsPrimary: true, Skills: []models.SkillConfig{{Name: "project-guidance", Description: "Guide project work", Content: "safe"}}}
	require.NoError(t, agentRepo.Create(ctx, &agent))
	foreign := models.Agent{Name: "Foreign Architect", Key: "foreign_architect", Scope: models.AgentScopeProject, ProjectID: other.ID,
		Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, &foreign))

	capabilities := NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, taskRepo, settingsRepo)
	snapshot, err := capabilities.Build(ctx, project.ID)
	require.NoError(t, err)
	require.Contains(t, snapshot.Agents, models.AutomationCapabilityRef{ID: "project_architect", Name: "Project Architect"})
	require.Contains(t, snapshot.Skills, models.AutomationCapabilityRef{ID: "project_architect:project-guidance", Name: "project-guidance"})

	svc := NewAutomationDraftService(repository.NewAutomationRepo(db), NewAutomationAdapterRegistry())
	svc.SetCapabilitySnapshotBuilder(capabilities)
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Nodes[1].Config["agent_ref"] = "project_architect"
	candidate.Nodes[1].Config["skills"] = []any{"project_architect:project-guidance"}
	candidate.Nodes[1].Config["source_files"] = []any{"VISION.md"}
	require.Empty(t, svc.ValidateCandidateWithCapabilities(candidate, snapshot))

	candidate.Nodes[1].Config["agent_ref"] = "foreign_architect"
	issues := svc.ValidateCandidateWithCapabilities(candidate, snapshot)
	require.Contains(t, issueCodes(issues), "agent_ref")
	created, err := svc.CreateDraft(ctx, AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	require.Contains(t, issueCodes(created.ValidationErrors), "agent_ref", "unresolved references must remain visible without being guessed")

	candidate.Nodes[1].Config["agent_ref"] = "project_architect"
	candidate.Nodes[1].Config["skills"] = []any{"project_architect:missing"}
	require.Contains(t, issueCodes(svc.ValidateCandidateWithCapabilities(candidate, snapshot)), "skill_ref")
	candidate.Nodes[1].Config["skills"] = []any{"project_architect:project-guidance"}
	candidate.Nodes[1].Config["source_files"] = []any{"missing.md"}
	require.Contains(t, issueCodes(svc.ValidateCandidateWithCapabilities(candidate, snapshot)), "source_file")
}

func TestAutomationDraftServiceRejectsArbitraryTopologyAndUnsafeConfiguration(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)

	candidate.Nodes[0].Config["tool"] = "create_task"
	issues := svc.ValidateCandidate(candidate)
	require.NotEmpty(t, issues)
	require.Equal(t, "unknown_config", issues[0].Code)

	candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{Key: "arbitrary", Name: "Arbitrary", Type: models.AutomationNodeAction, Role: "execute_code", Config: map[string]any{"code": "rm -rf /"}})
	issues = svc.ValidateCandidate(candidate)
	require.NotEmpty(t, issues)
	require.Contains(t, issueCodes(issues), "unsupported_topology")

	candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Edges[0].FromPort = "top"
	require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "invalid_edge", "unknown visual connector sides must not persist")

	_, err = DecodeAutomationDraftCandidate([]byte(`{"schema_version":1,"name":"x","description":"","automation_type":"vision_driver","adapter_key":"vision_driver","nodes":[],"edges":[],"database_id":"forbidden"}`))
	require.Error(t, err)

	for _, forbidden := range []string{"https://example.com/hook", "```sh\nrm -rf /\n```", "DROP TABLE tasks"} {
		candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
		require.NoError(t, err)
		candidate.Nodes[1].Config["prompt"] = forbidden
		require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "unsafe_config")
	}

	candidate, err = svc.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	for i := range candidate.Nodes {
		if _, ok := candidate.Nodes[i].Config["prompt"]; ok {
			candidate.Nodes[i].Config["prompt"] = strings.Repeat("x", 20000)
		}
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "graph_size")

	candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Nodes[1].Config["priority"] = math.NaN()
	require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "invalid_json")
}

func TestAutomationDraftCreationPersistsDefinitionOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Draft only")
	automationRepo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(automationRepo, NewAutomationAdapterRegistry())
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)

	result, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NotNil(t, result.Definition)
	require.Equal(t, models.AutomationVersionDraft, result.Definition.Version.State)
	require.Nil(t, result.Definition.Automation.PublishedVersionID)
	require.NotEmpty(t, result.URL)

	for _, table := range []string{"tasks", "schedules", "alerts", "executions", "workflow_executions", "task_pull_requests"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, "draft creation must not mutate %s", table)
	}

	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, result.Definition.Automation.ID, result.Definition.Version.ID)
	require.NoError(t, err)
	require.NotNil(t, metadata)
	require.Equal(t, candidate.AdapterKey, result.Candidate.AdapterKey)
}

func TestAutomationDraftClonePreservesPublishedVersion(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Clone")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Edges[0].FromPort = "left"
	candidate.Edges[0].ToPort = "right"
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID,
		AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)

	cloned, err := drafts.ClonePublishedVersion(context.Background(), project.ID, published.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, 2, cloned.Definition.Version.Version)
	require.Equal(t, models.AutomationVersionDraft, cloned.Definition.Version.State)
	require.NotEqual(t, published.Definition.Version.ID, cloned.Definition.Version.ID)
	require.Equal(t, "left", cloned.Candidate.Edges[0].FromPort, "editing after apply must preserve the chosen source side")
	require.Equal(t, "right", cloned.Candidate.Edges[0].ToPort, "editing after apply must preserve the chosen target side")
	current, err := automationRepo.GetDefinition(context.Background(), project.ID, published.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, published.Definition.Version.ID, current.Version.ID, "cloning must not replace the active topology")

	cloned.Candidate.Nodes[0].Name = "Draft-only name"
	_, err = drafts.UpdateDraft(context.Background(), cloned.Definition.Automation.ID, cloned.Definition.Version.ID, project.ID, cloned.Candidate)
	require.NoError(t, err)
	reopened, err := drafts.ClonePublishedVersion(context.Background(), project.ID, published.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, cloned.Definition.Version.ID, reopened.Definition.Version.ID, "Edit must reopen the current working design instead of creating another draft")
	require.Equal(t, "Draft-only name", reopened.Candidate.Nodes[0].Name, "saved edits must be reopened on the same Automation")
	var editableCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, published.Definition.Automation.ID).Scan(&editableCount))
	require.Equal(t, 1, editableCount)
	original, err := automationRepo.GetDefinitionVersion(context.Background(), project.ID, published.Definition.Automation.ID, published.Definition.Version.ID)
	require.NoError(t, err)
	require.NotEqual(t, "Draft-only name", original.Nodes[0].Name)
}

func issueCodes(issues []models.AutomationValidationIssue) []string {
	codes := make([]string, len(issues))
	for i := range issues {
		codes[i] = issues[i].Code
	}
	return codes
}
