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
	require.Equal(t, AutomationAdapterVisionDriver, blank.AdapterKey)
	require.Empty(t, blank.Nodes, "Blank must start with an empty canvas, not a populated template")
	require.Empty(t, blank.Edges)
	require.Contains(t, issueCodes(svc.ValidateCandidate(blank)), "missing_node")

	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: blank})
	require.NoError(t, err)
	require.Empty(t, created.Candidate.Nodes)
	require.NotEmpty(t, created.ValidationErrors)

	palette, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	dragged := palette.Nodes[0]
	dragged.Position = &models.AutomationDraftPoint{X: 37, Y: 83}
	created.Candidate.Nodes = append(created.Candidate.Nodes, dragged)
	updated, err := svc.UpdateDraft(context.Background(), created.Definition.Automation.ID, created.Definition.Version.ID, project.ID, created.Candidate)
	require.NoError(t, err)
	require.Equal(t, &models.AutomationDraftPoint{X: 37, Y: 83}, updated.Candidate.Nodes[0].Position, "normalization must preserve user-positioned nodes")
}

func TestAutomationFreeformDraftPersistsCustomNodesAndCyclesButCannotPublish(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Freeform draft")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterVisionDriver)
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
	require.Contains(t, issueCodes(issues), "unsupported_topology", "freeform topology must remain visibly unpublished")
	require.NotContains(t, issueCodes(issues), "invalid_edge", "a multi-node cycle is valid draft geometry")
	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Len(t, created.Candidate.Nodes, 3)
	require.Len(t, created.Candidate.Edges, 3)
	require.Equal(t, "left", created.Candidate.Edges[0].FromPort, "chosen connector side must survive draft persistence")
	require.Equal(t, "right", created.Candidate.Edges[0].ToPort, "chosen connector side must survive draft persistence")
	require.Contains(t, issueCodes(created.ValidationErrors), "unsupported_topology")

	planner := NewAutomationPublicationPlanner(repo, nil, nil, svc.registry, svc)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Contains(t, issueCodes(plan.Validation), "unsupported_topology", "the publication planner must reject freeform topology")
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
