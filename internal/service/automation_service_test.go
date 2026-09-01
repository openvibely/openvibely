package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type automationEnterpriseRepoResolver struct {
	endpoint string
}

func (r automationEnterpriseRepoResolver) ResolveRepo(context.Context, string, string) (*GitHubRepoRef, error) {
	return &GitHubRepoRef{Owner: "acme", Name: "widgets", HTMLURL: "https://github.example.com/acme/widgets"}, nil
}

func (r automationEnterpriseRepoResolver) GlobalAPIEndpoint(context.Context) string {
	return r.endpoint
}

func TestCurrentAutomationTemplateRevisionTracksMaintainedTemplateChanges(t *testing.T) {
	require.Equal(t, 9, CurrentAutomationTemplateRevision(AutomationAdapterNativeSDLC))
	require.Equal(t, 15, CurrentAutomationTemplateRevision(AutomationAdapterGitHubSDLC))
	require.Zero(t, CurrentAutomationTemplateRevision(AutomationAdapterCustom))
}

func TestResolveAutomationProjectGitHubRepository_AppliesGlobalEndpoint(t *testing.T) {
	const endpoint = "https://github.example.com/api/v3"
	project := &models.Project{RepoURL: "https://github.example.com/acme/widgets"}
	repo, err := resolveAutomationProjectGitHubRepository(context.Background(), automationEnterpriseRepoResolver{endpoint: endpoint}, project)
	require.NoError(t, err)
	require.Equal(t, endpoint, repo.APIBaseURL)
}

func automationTestProject(t *testing.T, repo *repository.ProjectRepo, name string) models.Project {
	t.Helper()
	project := models.Project{Name: name}
	require.NoError(t, repo.Create(context.Background(), &project))
	return project
}

func automationTestTaskAndSchedule(t *testing.T, dbRepo *repository.TaskRepo, scheduleRepo *repository.ScheduleRepo, projectID, title string) (models.Task, models.Schedule) {
	t.Helper()
	task := models.Task{ProjectID: projectID, Title: title, Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "visible automation task"}
	require.NoError(t, dbRepo.Create(context.Background(), &task))
	runAt := time.Now().UTC().Add(time.Hour)
	schedule := models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(context.Background(), &schedule))
	return task, schedule
}

func TestAutomationRegistrationTelemetryReportsCreationTruthfully(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Registration telemetry")
	task, schedule := automationTestTaskAndSchedule(t, repository.NewTaskRepo(db, nil), repository.NewScheduleRepo(db), project.ID, "Registered task")
	registration := NewAutomationRegistrationService(repository.NewAutomationRepo(db), NewAutomationAdapterRegistry())
	request := AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/telemetry",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	}

	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalOutput) })

	_, reused, err := registration.Register(ctx, request)
	require.NoError(t, err)
	require.False(t, reused)
	require.Contains(t, logs.String(), `event=automation.registration.completed`)
	require.Contains(t, logs.String(), `created="true"`)

	logs.Reset()
	_, reused, err = registration.Register(ctx, request)
	require.NoError(t, err)
	require.True(t, reused)
	require.Contains(t, logs.String(), `event=automation.registration.completed`)
	require.Contains(t, logs.String(), `created="false"`)
	require.NotContains(t, strings.TrimSpace(logs.String()), `created="true"`)
}

func TestAutomationRegistrationExplicitIdentityAndIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	service := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
	graph := NewAutomationGraphService(automationRepo)
	ctx := context.Background()

	project := automationTestProject(t, projectRepo, "Automations")
	other := automationTestProject(t, projectRepo, "Other")
	sharedTask, nativeSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Shared Inbox")
	githubTask, githubSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "GitHub Trigger")
	foreignTask, foreignSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, other.ID, "Foreign")

	cards, err := graph.List(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, cards, "ordinary tasks and schedules must not be inferred as automations")

	nativeReq := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/default", Resources: []models.AutomationResourceBinding{
		{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: nativeSchedule.ID},
		{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: sharedTask.ID},
	}}
	native, reused, err := service.Register(ctx, nativeReq)
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, models.AutomationActive, native.Automation.LifecycleState)
	require.Equal(t, models.AutomationVersionPublished, native.Version.State)

	nativeReq.Name = "Updated Native Automation"
	again, reused, err := service.Register(ctx, nativeReq)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, native.Automation.ID, again.Automation.ID)
	require.Equal(t, native.Version.ID, again.Version.ID, "setup reruns must not replace the point-in-time snapshot")
	require.Equal(t, native.Automation.Name, again.Automation.Name, "setup reruns must not rename the saved Automation")
	var retainedGraphID string
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO automation_versions
		(project_id, automation_id, version, state, source, adapter_key)
		VALUES (?, ?, 2, 'draft', 'bootstrap', ?) RETURNING id`, project.ID,
		native.Automation.ID, AutomationAdapterNativeSDLC).Scan(&retainedGraphID))
	again, reused, err = service.Register(ctx, nativeReq)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, native.Version.ID, again.Version.ID)
	require.Zero(t, tableCountWhere(t, db, "automation_versions", "id", retainedGraphID),
		"unchanged maintained registration must remove pre-existing retained draft graphs")
	require.Equal(t, 1, tableCountWhere(t, db, "automation_versions", "automation_id", native.Automation.ID))

	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC,
		StableKey: nativeReq.StableKey, Resources: []models.AutomationResourceBinding{
			{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: githubSchedule.ID},
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: githubTask.ID},
		}})
	require.ErrorContains(t, err, "adapter cannot change")

	githubReq := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/default", Resources: []models.AutomationResourceBinding{
		{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: githubSchedule.ID},
		{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: githubTask.ID},
	}}
	github, reused, err := service.Register(ctx, githubReq)
	require.NoError(t, err)
	require.False(t, reused)
	require.NotEqual(t, native.Automation.ID, github.Automation.ID)

	cards, err = graph.List(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, cards, 2)

	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/foreign", Resources: []models.AutomationResourceBinding{
		{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: foreignSchedule.ID},
		{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: foreignTask.ID},
	}})
	require.ErrorContains(t, err, "another project")

	foreignView, err := automationRepo.GetDefinition(ctx, other.ID, native.Automation.ID)
	require.NoError(t, err)
	require.Nil(t, foreignView)
}

func TestRegisteredMaintainedAutomationCanBeReopenedAndSaved(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Editable registered maintained Automation")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Registered maintained trigger")

	definition, reused, err := NewAutomationRegistrationService(automationRepo, registry).Register(ctx, AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/editable",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)
	require.False(t, reused)
	var registeredApprovalConfig map[string]any
	for _, node := range definition.Nodes {
		if node.NodeKey == "approval" {
			require.NoError(t, json.Unmarshal([]byte(node.ConfigJSON), &registeredApprovalConfig))
		}
	}
	require.Equal(t, "native_alert", registeredApprovalConfig["approval_method"], "new registered snapshots must persist valid maintained defaults")

	_, err = db.ExecContext(ctx, `UPDATE automation_nodes SET config_json = '{}'
		WHERE automation_id = ? AND node_type IN ('action', 'human_gate')`, definition.Automation.ID)
	require.NoError(t, err, "simulate a registered snapshot created before maintained defaults were persisted")

	drafts := NewAutomationDraftService(automationRepo, registry)
	reopened, err := drafts.CurrentCandidate(ctx, project.ID, definition.Automation.ID)
	require.NoError(t, err)
	require.Empty(t, reopened.ValidationErrors, "registered maintained snapshots must reopen as valid editable graphs")
	require.Equal(t, "native_alert", automationDraftNodeByKey(t, reopened.Candidate, "approval").Config["approval_method"])

	validator := NewAutomationSaveValidator(registry, drafts)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, validator)
	saved, err := compiler.Save(ctx, AutomationSaveRequest{
		ProjectID: project.ID, AutomationID: definition.Automation.ID, Source: "manual", CreatedVia: "web", Candidate: reopened.Candidate,
	})
	require.NoError(t, err)
	require.NotEqual(t, definition.Version.ID, saved.Definition.Version.ID)
	require.Equal(t, task.ID, automationResourceID(t, saved.Definition, "vision_suggestions", "task"))
	require.Equal(t, schedule.ID, automationResourceID(t, saved.Definition, "vision_suggestions", "schedule"))

	invalidSubmission, err := drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	delete(automationDraftNodeByKey(t, invalidSubmission, "approval").Config, "approval_method")
	require.Contains(t, issueCodes(drafts.ValidateCandidate(invalidSubmission)), "approval_method", "public Save validation must not hydrate missing maintained settings")

	githubTask, githubSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Registered GitHub trigger")
	githubDefinition, reused, err := NewAutomationRegistrationService(automationRepo, registry).Register(ctx, AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/editable",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: githubTask.ID},
			{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: githubSchedule.ID},
		},
	})
	require.NoError(t, err)
	require.False(t, reused)
	var registeredImplementationConfig map[string]any
	for _, node := range githubDefinition.Nodes {
		if node.NodeKey == "implementation" {
			require.NoError(t, json.Unmarshal([]byte(node.ConfigJSON), &registeredImplementationConfig))
		}
	}
	require.NotEmpty(t, registeredImplementationConfig["prompt"], "first registration must publish executable issue-task instructions")
	require.Equal(t, string(models.CategoryActive), registeredImplementationConfig["category"])
	require.EqualValues(t, 2, registeredImplementationConfig["priority"])

	_, err = db.ExecContext(ctx, `UPDATE automation_nodes SET config_json = '{}'
		WHERE automation_id = ? AND node_key = 'implementation'`, githubDefinition.Automation.ID)
	require.NoError(t, err, "simulate an older point-in-time GitHub snapshot with empty issue-task configuration")
	rerun, reused, err := NewAutomationRegistrationService(automationRepo, registry).Register(ctx, AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/editable",
	})
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, githubDefinition.Version.ID, rerun.Version.ID, "registration reruns must not upgrade the saved snapshot")
	for _, node := range rerun.Nodes {
		if node.NodeKey == "implementation" {
			require.JSONEq(t, `{}`, node.ConfigJSON, "registration reruns must preserve explicit stored configuration")
		}
	}

	githubReopened, err := drafts.CurrentCandidate(ctx, project.ID, githubDefinition.Automation.ID)
	require.NoError(t, err)
	require.Empty(t, githubReopened.ValidationErrors)
	require.Equal(t, []any{}, automationDraftNodeByKey(t, githubReopened.Candidate, "issue").Config["labels"])
	require.Equal(t, false, automationDraftNodeByKey(t, githubReopened.Candidate, "open_pr").Config["draft"])
	require.Equal(t, "github_assignment", automationDraftNodeByKey(t, githubReopened.Candidate, "assignment").Config["approval_method"])
}

func TestRegisteredSharedInboxTaskCanBeReopenedAndSaved(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Editable shared inbox Automation")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	worker, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Shared inbox worker")

	definition, reused, err := NewAutomationRegistrationService(automationRepo, registry).Register(ctx, AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/shared-inbox-editable",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "inbox", ResourceType: "task", ResourceID: worker.ID, Relation: "shared"},
			{NodeKey: "inbox", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)
	require.False(t, reused)
	var initialTaskRelation string
	for _, resource := range definition.Resources {
		if resource.NodeKey == "inbox" && resource.ResourceType == "task" {
			initialTaskRelation = resource.Relation
		}
	}
	require.Equal(t, "shared", initialTaskRelation)

	before, err := taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, before)
	drafts := NewAutomationDraftService(automationRepo, registry)
	reopened, err := drafts.CurrentCandidate(ctx, project.ID, definition.Automation.ID)
	require.NoError(t, err)
	require.Empty(t, reopened.ValidationErrors)
	for i := range reopened.Candidate.Nodes {
		if reopened.Candidate.Nodes[i].Key == "inbox" {
			reopened.Candidate.Nodes[i].Config["prompt"] = "Edited graph-only inbox prompt"
			reopened.Candidate.Nodes[i].Config["priority"] = 3
		}
	}
	validator := NewAutomationSaveValidator(registry, drafts)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, validator)

	saved, err := compiler.Save(ctx, AutomationSaveRequest{ProjectID: project.ID, AutomationID: definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: reopened.Candidate})
	require.NoError(t, err)
	require.Equal(t, worker.ID, automationResourceID(t, saved.Definition, "inbox", "task"))
	require.Equal(t, schedule.ID, automationResourceID(t, saved.Definition, "inbox", "schedule"))
	var savedTaskRelation string
	for _, resource := range saved.Definition.Resources {
		if resource.NodeKey == "inbox" && resource.ResourceType == "task" {
			savedTaskRelation = resource.Relation
		}
	}
	require.Equal(t, "shared", savedTaskRelation)

	after, err := taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Equal(t, before.Title, after.Title)
	require.Equal(t, before.Prompt, after.Prompt)
	require.Equal(t, before.Category, after.Category)
	require.Equal(t, before.Priority, after.Priority)
	require.Equal(t, before.Status, after.Status)
}

func TestAutomationPortfolioListUsesCompactPublishedCardProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Compact Automation portfolio")
	other := automationTestProject(t, repository.NewProjectRepo(db), "Other Compact Automation portfolio")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	largeConfig := strings.Repeat(`{"prompt":"large hidden graph config"}`, 200)
	for i := 0; i < 4; i++ {
		automationID := fmt.Sprintf("compact-saved-%03d", i)
		versionID := fmt.Sprintf("compact-version-%03d", i)
		state := models.AutomationActive
		health := models.AutomationHealthHealthy
		revision := CurrentAutomationTemplateRevision(AutomationAdapterNativeSDLC)
		if i == 0 {
			state = models.AutomationPaused
			health = models.AutomationHealthDegraded
			revision = 0
		}
		_, err := db.ExecContext(ctx, `INSERT INTO automations
			(id, project_id, stable_key, name, description, automation_type, lifecycle_state, health_state, health_reason, template_revision, updated_at)
			VALUES (?, ?, ?, ?, ?, 'custom', ?, ?, 'visible health reason', ?, datetime('now', ?))`,
			automationID, project.ID, "compact/"+automationID, fmt.Sprintf("Compact Automation %03d", i), "visible searchable description",
			state, health, revision, fmt.Sprintf("+%d seconds", i))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
			(id, project_id, automation_id, version, state, source, adapter_key, schema_version, published_at)
			VALUES (?, ?, ?, 1, 'published', 'template', ?, 1, CURRENT_TIMESTAMP)`, versionID, project.ID, automationID, AutomationAdapterNativeSDLC)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE automations SET published_version_id = ? WHERE id = ? AND project_id = ?`, versionID, automationID, project.ID)
		require.NoError(t, err)

		nodeA := repository.NewID()
		nodeB := repository.NewID()
		_, err = db.ExecContext(ctx, `INSERT INTO automation_nodes
			(id, project_id, automation_id, version_id, node_key, name, node_type, role, config_json, position_x, position_y)
			VALUES (?, ?, ?, ?, 'trigger', 'Trigger', 'trigger', 'schedule', ?, 10, 20),
				(?, ?, ?, ?, 'task', 'Task', 'agent_task', 'task', ?, 30, 40)`,
			nodeA, project.ID, automationID, versionID, largeConfig, nodeB, project.ID, automationID, versionID, largeConfig)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO automation_edges
			(project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, label, condition_json)
			VALUES (?, ?, ?, ?, ?, ?, 'hidden edge', ?)`, project.ID, automationID, versionID, nodeA, nodeB, fmt.Sprintf("edge-%03d", i), largeConfig)
		require.NoError(t, err)
		task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Compact schedule "+automationID)
		_, err = db.ExecContext(ctx, `INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES (?, ?, ?, ?, 'task', ?, 'owned'), (?, ?, ?, ?, 'schedule', ?, 'owned')`,
			project.ID, automationID, versionID, nodeB, task.ID, project.ID, automationID, versionID, nodeA, schedule.ID)
		require.NoError(t, err)
	}

	_, err := db.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, description, automation_type, lifecycle_state, updated_at)
		VALUES ('compact-draft-published-version', ?, 'compact/draft-version', 'Draft Version Automation', '', 'custom', 'active', CURRENT_TIMESTAMP)`, project.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key)
		VALUES ('compact-draft-version', ?, 'compact-draft-published-version', 1, 'draft', 'manual', 'custom')`, project.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE automations SET published_version_id = 'compact-draft-version' WHERE id = 'compact-draft-published-version'`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, description, automation_type, lifecycle_state)
		VALUES ('compact-foreign', ?, 'compact/foreign', 'Foreign Compact Automation', '', 'custom', 'active')`, other.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, published_at)
		VALUES ('compact-foreign-version', ?, 'compact-foreign', 1, 'published', 'manual', 'custom', CURRENT_TIMESTAMP)`, other.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE automations SET published_version_id = 'compact-foreign-version' WHERE id = 'compact-foreign'`)
	require.NoError(t, err)

	counter.Reset()
	counter.SetEnabled(true)
	cards, err := NewAutomationGraphService(repository.NewAutomationRepo(db)).List(ctx, project.ID)
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.Len(t, cards, 4)
	require.Len(t, counter.Statements(), 1, "portfolio card rendering should be one compact statement, not 2 + 5N + S enrichment statements")
	statements := strings.ToLower(strings.Join(counter.Statements(), "\n"))
	for _, hidden := range []string{"portfoliooperationalcounts", "automation_nodes", "automation_edges", "automation_definition_resources", "schedules", "automation_activities", "automation_work_items", "config_json", "condition_json"} {
		require.NotContains(t, statements, hidden)
	}

	first := cards[0]
	require.Equal(t, "compact-saved-003", first.Automation.ID)
	require.Equal(t, models.AutomationVersionPublished, first.Version.State)
	require.Equal(t, "template", first.Version.Source)
	require.Equal(t, AutomationAdapterNativeSDLC, first.Version.AdapterKey)
	require.Empty(t, first.Resources)
	require.Nil(t, first.NextRun)
	require.Nil(t, first.LastRun)
	require.Zero(t, first.Counts)

	var paused *models.AutomationCard
	for i := range cards {
		if cards[i].Automation.ID == "compact-saved-000" {
			paused = &cards[i]
		}
	}
	require.NotNil(t, paused)
	require.Equal(t, models.AutomationPaused, paused.Automation.LifecycleState)
	require.Equal(t, models.AutomationHealthDegraded, paused.Automation.HealthState)
	require.True(t, paused.TemplateUpdateAvailable)
	for _, card := range cards {
		require.NotEqual(t, "compact-draft-published-version", card.Automation.ID)
		require.NotEqual(t, "compact-foreign", card.Automation.ID)
	}
}

func TestAutomationPortfolioListsEverySavedAutomationBeyondOneHundred(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Large Automation portfolio")
	other := automationTestProject(t, repository.NewProjectRepo(db), "Other Automation portfolio")

	for i := 0; i < 5; i++ {
		_, err := db.ExecContext(ctx, `INSERT INTO automations
			(id, project_id, stable_key, name, description, automation_type, lifecycle_state, updated_at)
			VALUES (?, ?, ?, ?, '', 'custom', 'draft', datetime('now', ?))`,
			fmt.Sprintf("draft-%03d", i), project.ID, fmt.Sprintf("draft/%03d", i), fmt.Sprintf("Draft %03d", i), fmt.Sprintf("+%d seconds", 1000+i))
		require.NoError(t, err)
	}
	for i := 0; i < 101; i++ {
		automationID := fmt.Sprintf("saved-%03d", i)
		versionID := fmt.Sprintf("saved-version-%03d", i)
		_, err := db.ExecContext(ctx, `INSERT INTO automations
			(id, project_id, stable_key, name, description, automation_type, lifecycle_state, updated_at)
			VALUES (?, ?, ?, ?, ?, 'custom', 'active', datetime('now', ?))`,
			automationID, project.ID, "saved/"+automationID, fmt.Sprintf("Saved Automation %03d", i), "searchable saved automation", fmt.Sprintf("+%d seconds", i))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
			(id, project_id, automation_id, version, state, source, adapter_key, published_at)
			VALUES (?, ?, ?, 1, 'published', 'manual', 'custom', CURRENT_TIMESTAMP)`, versionID, project.ID, automationID)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE automations SET published_version_id = ? WHERE id = ? AND project_id = ?`, versionID, automationID, project.ID)
		require.NoError(t, err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, description, automation_type, lifecycle_state)
		VALUES ('foreign-saved', ?, 'saved/foreign', 'Foreign saved Automation', '', 'custom', 'active')`, other.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, published_at)
		VALUES ('foreign-version', ?, 'foreign-saved', 1, 'published', 'manual', 'custom', CURRENT_TIMESTAMP)`, other.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE automations SET published_version_id = 'foreign-version' WHERE id = 'foreign-saved'`)
	require.NoError(t, err)

	cards, err := NewAutomationGraphService(repository.NewAutomationRepo(db)).List(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, cards, 101)
	seen := make(map[string]bool, len(cards))
	for _, card := range cards {
		seen[card.Automation.ID] = true
		require.NotNil(t, card.Automation.PublishedVersionID)
	}
	require.True(t, seen["saved-000"])
	require.True(t, seen["saved-100"])
	require.False(t, seen["foreign-saved"])
}

func TestGitHubSDLCRegistrationHydratesInitialSnapshotAcrossPause(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Registered GitHub prompt parity")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())

	maintainedPrompt := githubSDLCDevInboxPrompt
	require.Contains(t, maintainedPrompt, "Always call `github_get_project_inbox`")
	require.Contains(t, maintainedPrompt, "call `github_list_assigned_issues` for every returned Authorized User")
	require.Contains(t, maintainedPrompt, "also call `github_list_my_assigned_issues`")
	require.Contains(t, maintainedPrompt, "compact body-free discovery lists")
	require.Contains(t, maintainedPrompt, "Do not call `github_get_issue` for every listed issue as a default scan step")
	require.Contains(t, maintainedPrompt, "Deduplicate issues by repository plus issue number")
	task := models.Task{ProjectID: project.ID, Title: "GitHub Dev Inbox", Category: models.CategoryScheduled, Priority: 3, Status: models.StatusPending, Prompt: maintainedPrompt}
	require.NoError(t, taskRepo.Create(ctx, &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, &schedule))

	definition, _, err := registration.Register(ctx, AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/prompt-parity",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)

	var config map[string]any
	for _, node := range definition.Nodes {
		if node.NodeKey == "dev_inbox" {
			require.NoError(t, json.Unmarshal([]byte(node.ConfigJSON), &config))
		}
	}
	require.Equal(t, maintainedPrompt, config["prompt"], "registration must show the real bound Task prompt in the graph")
	require.Equal(t, string(models.CategoryScheduled), config["category"])
	require.EqualValues(t, task.Priority, config["priority"])
	require.Equal(t, string(models.RepeatHours), config["repeat_type"])
	require.EqualValues(t, 1, config["repeat_interval"])
	require.Equal(t, false, config["clear_context_on_start"])

	require.NoError(t, automationRepo.SetAutomationLifecycle(ctx, project.ID, definition.Automation.ID, models.AutomationPaused))
	storedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, maintainedPrompt, storedTask.Prompt, "pausing must not alter the runtime Task prompt")
	reopened, err := NewAutomationDraftService(automationRepo, NewAutomationAdapterRegistry()).CurrentCandidate(ctx, project.ID, definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, maintainedPrompt, automationDraftNodeByKey(t, reopened.Candidate, "dev_inbox").Config["prompt"])
}

func seedRetainedAutomationSchedule(t *testing.T, db *sql.DB, taskRepo *repository.TaskRepo, scheduleRepo *repository.ScheduleRepo,
	projectID, automationID, versionID, label string) (models.Task, models.Schedule) {
	t.Helper()
	ctx := context.Background()
	task := models.Task{ProjectID: projectID, Title: "Retained schedule task " + label, Category: models.CategoryScheduled,
		Priority: 2, Status: models.StatusPending, Prompt: "Preserve this domain Task."}
	require.NoError(t, taskRepo.Create(ctx, &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily,
		RepeatInterval: 1, Enabled: false}
	require.NoError(t, scheduleRepo.Create(ctx, &schedule))
	nodeID := repository.NewID()
	_, err := db.ExecContext(ctx, `INSERT INTO automation_nodes
		(id, project_id, automation_id, version_id, node_key, name, node_type, role)
		VALUES (?, ?, ?, ?, ?, ?, 'trigger', 'schedule')`, nodeID, projectID, automationID, versionID,
		"retained_schedule_"+label, "Retained schedule "+label)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_definition_resources
		(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'owned')`, projectID, automationID, versionID, nodeID, schedule.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_trigger_owners
		(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
		VALUES (?, ?, ?, ?, ?, 'active')`, schedule.ID, projectID, automationID, versionID, nodeID)
	require.NoError(t, err)
	return task, schedule
}

func TestMaintainedAutomationRegistrationPreservesPointInTimeSnapshot(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Point-in-time maintained template")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	registration := NewAutomationRegistrationService(automationRepo, registry)

	originalTask, originalSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Original maintained schedule")
	request := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC,
		StableKey: "native-sdlc/point-in-time", Name: "User's saved Automation", Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: originalSchedule.ID},
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: originalTask.ID},
		}}
	original, reused, err := registration.Register(ctx, request)
	require.NoError(t, err)
	require.False(t, reused)

	var triggerNodeID string
	for _, node := range original.Nodes {
		if node.NodeKey == "vision_suggestions" {
			triggerNodeID = node.ID
			break
		}
	}
	require.NotEmpty(t, triggerNodeID)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'point-in-time-runtime', 'running')`, project.ID, original.Automation.ID,
		original.Version.ID, triggerNodeID, originalSchedule.ID)
	require.NoError(t, err)

	upgraded := registry.adapters[AutomationAdapterNativeSDLC]
	upgraded.Nodes = append([]AutomationAdapterNode(nil), upgraded.Nodes...)
	upgraded.Edges = append([]AutomationAdapterEdge(nil), upgraded.Edges...)
	upgraded.Nodes[0].Key = "vision_suggestions_v2"
	upgraded.Nodes[0].Name = "Renamed Vision Suggestions"
	upgraded.Nodes[0].Type = "agent_task"
	upgraded.Nodes[0].Role = "bug_finder"
	upgraded.Nodes[0].X = 999
	upgraded.Edges[0].From = "vision_suggestions_v2"
	upgraded.Edges[4].To = "rejected"
	upgraded.Edges[4].Label = "new release boundary"
	upgraded.Edges[4].Condition = `{"state":"changed"}`
	registry.adapters[AutomationAdapterNativeSDLC] = upgraded

	replacementTask, replacementSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "New release schedule")
	request.Name = "New bundled template name"
	request.Resources = []models.AutomationResourceBinding{
		{NodeKey: "vision_suggestions_v2", ResourceType: "schedule", ResourceID: replacementSchedule.ID},
		{NodeKey: "vision_suggestions_v2", ResourceType: "task", ResourceID: replacementTask.ID},
	}
	preserved, reused, err := registration.Register(ctx, request)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, original.Automation.ID, preserved.Automation.ID)
	require.Equal(t, original.Automation.Name, preserved.Automation.Name)
	require.Equal(t, original.Version.ID, preserved.Version.ID)
	require.Equal(t, original.Nodes, preserved.Nodes)
	require.Equal(t, original.Edges, preserved.Edges)
	require.Equal(t, original.Resources, preserved.Resources)
	require.Equal(t, 1, tableCountWhere(t, db, "schedules", "id", originalSchedule.ID))
	require.Equal(t, 1, tableCountWhere(t, db, "schedules", "id", replacementSchedule.ID),
		"newly supplied resources remain ordinary domain resources and must not replace the saved snapshot")
	require.Equal(t, 1, tableCountWhere(t, db, "automation_invocations", "version_id", original.Version.ID),
		"re-registration must not delete current runtime projection")
}

func TestMaintainedAutomationRegistrationPreservesCurrentGraphAndLifecycle(t *testing.T) {
	for _, test := range []struct {
		name           string
		state          models.AutomationLifecycleState
		ownershipState string
		expectEnabled  bool
	}{
		{name: "active", state: models.AutomationActive, ownershipState: "active", expectEnabled: true},
		{name: "paused", state: models.AutomationPaused, ownershipState: "paused"},
		{name: "archived", state: models.AutomationArchived, ownershipState: "archived"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			project := automationTestProject(t, repository.NewProjectRepo(db), "Maintained "+test.name)
			taskRepo := repository.NewTaskRepo(db, nil)
			scheduleRepo := repository.NewScheduleRepo(db)
			automationRepo := repository.NewAutomationRepo(db)
			registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())

			firstTask, firstSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "First maintained schedule")
			request := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC,
				StableKey: "native-sdlc/lifecycle-" + test.name, Resources: []models.AutomationResourceBinding{
					{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: firstSchedule.ID},
					{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: firstTask.ID},
				}}
			first, _, err := registration.Register(ctx, request)
			require.NoError(t, err)
			if test.state != models.AutomationActive {
				require.NoError(t, automationRepo.SetAutomationLifecycle(ctx, project.ID, first.Automation.ID, test.state))
			}
			var triggerNodeID string
			for _, node := range first.Nodes {
				if node.NodeKey == "vision_suggestions" {
					triggerNodeID = node.ID
				}
			}
			require.NotEmpty(t, triggerNodeID)
			_, err = db.ExecContext(ctx, `INSERT INTO automation_invocations
				(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, completed_at)
				VALUES (?, ?, ?, ?, 'schedule', ?, ?, 'completed', CURRENT_TIMESTAMP)`, project.ID, first.Automation.ID,
				first.Version.ID, triggerNodeID, firstSchedule.ID, "old-"+test.name)
			require.NoError(t, err)
			retainedDraftID := "retained-draft-" + test.name
			_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
				(id, project_id, automation_id, version, state, source, adapter_key)
				VALUES (?, ?, ?, 2, 'draft', 'bootstrap', ?)`, retainedDraftID, project.ID,
				first.Automation.ID, AutomationAdapterNativeSDLC)
			require.NoError(t, err)
			retainedTask, retainedSchedule := seedRetainedAutomationSchedule(t, db, taskRepo, scheduleRepo, project.ID,
				first.Automation.ID, retainedDraftID, test.name)

			secondTask, secondSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Replacement maintained schedule")
			request.Resources = []models.AutomationResourceBinding{
				{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: secondSchedule.ID},
				{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: secondTask.ID},
			}
			preserved, reused, err := registration.Register(ctx, request)
			require.NoError(t, err)
			require.True(t, reused)
			require.Equal(t, test.state, preserved.Automation.LifecycleState)
			require.Equal(t, first.Version.ID, preserved.Version.ID)
			require.Equal(t, 1, tableCountWhere(t, db, "automation_versions", "automation_id", first.Automation.ID), "registration must retain exactly one current graph")
			require.Equal(t, 1, tableCountWhere(t, db, "automation_versions", "id", first.Version.ID))
			require.Zero(t, tableCountWhere(t, db, "automation_versions", "id", retainedDraftID))
			require.Equal(t, 1, tableCountWhere(t, db, "automation_invocations", "automation_id", first.Automation.ID), "registration must preserve current runtime projection")
			require.Equal(t, 1, tableCountWhere(t, db, "schedules", "id", firstSchedule.ID), "registration must preserve the current exclusively owned schedule")
			require.Zero(t, tableCountWhere(t, db, "schedules", "id", retainedSchedule.ID), "retained draft schedule must be deleted before its owner row")
			require.Equal(t, 1, tableCountWhere(t, db, "tasks", "id", retainedTask.ID), "retained graph backing Task must survive")
			stored, err := scheduleRepo.GetByID(ctx, firstSchedule.ID)
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Equal(t, test.expectEnabled, stored.Enabled)
			owner, err := automationRepo.GetTriggerOwner(ctx, firstSchedule.ID)
			require.NoError(t, err)
			require.NotNil(t, owner, "inactive Automations must retain exclusive schedule provenance")
			require.Equal(t, test.ownershipState, owner.OwnershipState)
			replacementStored, err := scheduleRepo.GetByID(ctx, secondSchedule.ID)
			require.NoError(t, err)
			require.NotNil(t, replacementStored, "incoming replacement resources must remain ordinary domain resources")
			replacementOwner, err := automationRepo.GetTriggerOwner(ctx, secondSchedule.ID)
			require.NoError(t, err)
			require.Nil(t, replacementOwner)
		})
	}
}

func TestMaintainedAutomationRegistrationUnchangedCleansRetainedGraphSchedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Unchanged maintained retained schedule")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
	currentTask, currentSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Current maintained schedule")
	request := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC,
		StableKey: "native-sdlc/unchanged-retained", Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: currentSchedule.ID},
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: currentTask.ID},
		}}
	current, _, err := registration.Register(ctx, request)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key)
		VALUES ('unchanged-retained-draft', ?, ?, 2, 'draft', 'bootstrap', ?)`, project.ID,
		current.Automation.ID, AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	retainedTask, retainedSchedule := seedRetainedAutomationSchedule(t, db, taskRepo, scheduleRepo, project.ID,
		current.Automation.ID, "unchanged-retained-draft", "unchanged")

	reconciled, unchanged, err := registration.Register(ctx, request)
	require.NoError(t, err)
	require.True(t, unchanged)
	require.Equal(t, current.Version.ID, reconciled.Version.ID)
	require.Equal(t, 1, tableCountWhere(t, db, "automation_versions", "automation_id", current.Automation.ID))
	require.Zero(t, tableCountWhere(t, db, "schedules", "id", retainedSchedule.ID),
		"unchanged registration must delete a discarded graph's exclusively owned schedule")
	require.Equal(t, 1, tableCountWhere(t, db, "tasks", "id", retainedTask.ID),
		"unchanged registration must preserve the backing domain Task")
	require.Equal(t, 1, tableCountWhere(t, db, "schedules", "id", currentSchedule.ID),
		"unchanged registration must preserve the current graph schedule")
}

func TestMaintainedAutomationRegistrationEnablesBoundSchedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Disabled maintained schedule")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
	task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Disabled maintained trigger")
	_, err := db.ExecContext(ctx, `UPDATE schedules SET enabled = 0 WHERE id = ?`, schedule.ID)
	require.NoError(t, err)
	request := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC,
		StableKey: "native-sdlc/disabled", Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
		}}

	_, _, err = registration.Register(ctx, request)
	require.NoError(t, err)
	stored, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.True(t, stored.Enabled)

	_, _, err = registration.Register(ctx, request)
	require.NoError(t, err)
	stored, err = scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.True(t, stored.Enabled)
}

func TestMaintainedAutomationRegistrationUsesGlobalPauseAndResume(t *testing.T) {
	for _, test := range []struct {
		name              string
		configuredEnabled bool
	}{
		{name: "enabled", configuredEnabled: true},
		{name: "disabled", configuredEnabled: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			project := automationTestProject(t, repository.NewProjectRepo(db), "Paused maintained schedule "+test.name)
			taskRepo := repository.NewTaskRepo(db, nil)
			scheduleRepo := repository.NewScheduleRepo(db)
			automationRepo := repository.NewAutomationRepo(db)
			registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
			task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Paused maintained trigger")
			if !test.configuredEnabled {
				_, err := db.ExecContext(ctx, `UPDATE schedules SET enabled = 0 WHERE id = ?`, schedule.ID)
				require.NoError(t, err)
			}
			request := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC,
				StableKey: "native-sdlc/paused-intent-" + test.name, Resources: []models.AutomationResourceBinding{
					{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
					{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
				}}

			definition, _, err := registration.Register(ctx, request)
			require.NoError(t, err)
			require.NoError(t, automationRepo.SetAutomationLifecycle(ctx, project.ID, definition.Automation.ID, models.AutomationPaused))
			pausedSchedule, err := scheduleRepo.GetByID(ctx, schedule.ID)
			require.NoError(t, err)
			require.False(t, pausedSchedule.Enabled)

			_, _, err = registration.Register(ctx, request)
			require.NoError(t, err, "maintained re-registration while paused must leave the schedule paused")
			_, err = automationRepo.ResumeAutomation(ctx, project.ID, definition.Automation.ID)
			require.NoError(t, err)
			resumedSchedule, err := scheduleRepo.GetByID(ctx, schedule.ID)
			require.NoError(t, err)
			require.True(t, resumedSchedule.Enabled)
		})
	}
}

func TestAutomationRegistrationRejectsScheduleTaskMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Mismatched scheduled task")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	firstTask, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Actual scheduled task")
	secondTask := models.Task{ProjectID: project.ID, Title: "Incorrect visual task", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2, Prompt: "different task"}
	require.NoError(t, taskRepo.Create(context.Background(), &secondTask))

	_, _, err := NewAutomationRegistrationService(repository.NewAutomationRepo(db), NewAutomationAdapterRegistry()).Register(context.Background(), AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/mismatch",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: secondTask.ID},
		},
	})
	require.ErrorContains(t, err, "must target the task bound to that same node")
	require.NotEqual(t, firstTask.ID, secondTask.ID)
}

func TestAutomationRegistrationRejectsUnsupportedAdapterAndExclusiveTriggerReuse(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	service := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
	project := automationTestProject(t, projectRepo, "Exclusive")
	task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Trigger")
	ctx := context.Background()

	_, _, err := service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: "custom", StableKey: "custom/default", Resources: []models.AutomationResourceBinding{{NodeKey: "x", ResourceType: "task", ResourceID: task.ID}}})
	require.ErrorContains(t, err, "unsupported maintained automation adapter")
	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterVisionDriver, StableKey: "vision-driver/default", Resources: []models.AutomationResourceBinding{
		{NodeKey: "vision_driver", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "vision_driver", ResourceType: "task", ResourceID: task.ID},
	}})
	require.ErrorContains(t, err, "unsupported maintained automation adapter", "Vision Driver remains supported only for existing saved graphs, not maintained setup registration")

	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/shared-trigger", Resources: []models.AutomationResourceBinding{
		{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "shared"},
		{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
	}})
	require.ErrorContains(t, err, "exclusive owned relation")

	base := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, Resources: []models.AutomationResourceBinding{
		{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
	}}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, stableKey := range []string{"native-sdlc/one", "native-sdlc/two"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			request := base
			request.StableKey = key
			_, _, registerErr := service.Register(ctx, request)
			results <- registerErr
		}(stableKey)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, ownershipFailures := 0, 0
	for registerErr := range results {
		if registerErr == nil {
			successes++
		} else if errors.Is(registerErr, repository.ErrAutomationTriggerOwned) {
			ownershipFailures++
		} else {
			t.Fatalf("unexpected concurrent registration error: %v", registerErr)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, ownershipFailures)

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automations WHERE project_id = ?`, project.ID).Scan(&count))
	require.Equal(t, 1, count, "failed publication must roll back its draft identity")
}

func TestAutomationBootstrapRuntimeToolIsSelectedSkillScoped(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Runtime scope")
	repo := repository.NewAutomationRepo(db)
	llm := &LLMService{}
	llm.SetAutomationRegistrationService(NewAutomationRegistrationService(repo, NewAutomationAdapterRegistry()))
	task := models.Task{ID: "bootstrap-task", ProjectID: project.ID}

	require.Nil(t, llm.automationBootstrapRuntimeTools(context.Background(), task), "ordinary tasks must not receive bootstrap registration")
	nativeCtx := withLifecycleTurnContext(context.Background(), lifecycleTurnContext{SelectedSkillHandles: []string{"openvibely_native_autonomous_sdlc_bootstrap"}})
	runtime := llm.automationBootstrapRuntimeTools(nativeCtx, task)
	require.NotNil(t, runtime)
	require.Len(t, runtime.Definitions, 1)
	require.Equal(t, "register_automation_resources", runtime.Definitions[0].Name)

	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	producer, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Runtime producer")
	input := fmt.Sprintf(`{"adapter_key":"native_sdlc","stable_key":"native-sdlc/default","resources":[{"node_key":"vision_suggestions","resource_type":"schedule","resource_id":%q},{"node_key":"vision_suggestions","resource_type":"task","resource_id":%q}]}`, schedule.ID, producer.ID)
	output, handled, isError, err := runtime.Executor(nativeCtx, "register_automation_resources", []byte(input))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError)
	require.Contains(t, output, `"status":"active"`)
	require.Contains(t, output, "/automations/")

	_, handled, isError, err = runtime.Executor(nativeCtx, "register_automation_resources", []byte(`{"adapter_key":"github_sdlc","stable_key":"github-sdlc/default","resources":[]}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "unavailable for the selected maintained bootstrap")
}

func TestAutomationCompositeConstraintsAndProjectCascade(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Cascade")
	other := automationTestProject(t, projectRepo, "Mismatch")

	_, err := db.Exec(`INSERT INTO automations (id, project_id, stable_key, name) VALUES ('a', ?, 'a', 'A')`, project.ID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO automation_versions (id, project_id, automation_id, version, adapter_key) VALUES ('v', ?, 'a', 1, 'native_sdlc')`, other.ID)
	require.Error(t, err, "composite parent constraints must reject a foreign project")

	require.NoError(t, projectRepo.Delete(context.Background(), project.ID))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automations WHERE id = 'a'`).Scan(&count))
	require.Zero(t, count)
}

func TestAutomationGraphServiceHistoryAndResourceWrappers(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Automation graph wrappers")
	task, schedule := automationTestTaskAndSchedule(t, repository.NewTaskRepo(db, nil), repository.NewScheduleRepo(db), project.ID, "Automation wrapper task")
	automationRepo := repository.NewAutomationRepo(db)
	registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
	definition, reused, err := registration.Register(ctx, AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/wrappers",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)
	require.False(t, reused)
	require.NotEmpty(t, definition.Nodes)

	graph := NewAutomationGraphService(automationRepo)
	loaded, resources, err := graph.GetDefinition(ctx, project.ID, definition.Automation.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Len(t, resources, 2)

	var nodeID string
	for _, node := range loaded.Nodes {
		if node.NodeKey == "vision_suggestions" {
			nodeID = node.ID
			break
		}
	}
	require.NotEmpty(t, nodeID)
	resourcePage, err := graph.ListNodeResources(ctx, project.ID, definition.Automation.ID, nodeID, 10, "")
	require.NoError(t, err)
	require.NotNil(t, resourcePage)
	require.Empty(t, resourcePage.Items)

	missingNodeResources, err := graph.ListNodeResources(ctx, project.ID, definition.Automation.ID, "missing-node", 10, "")
	require.NoError(t, err)
	require.Nil(t, missingNodeResources)
	missingDefinitionResources, err := graph.ListNodeResources(ctx, project.ID, "missing-automation", nodeID, 10, "")
	require.NoError(t, err)
	require.Nil(t, missingDefinitionResources)

	invocations, err := graph.ListInvocations(ctx, project.ID, definition.Automation.ID, 5, "")
	require.NoError(t, err)
	require.Empty(t, invocations.Items)
	missingInvocations, err := graph.ListInvocations(ctx, project.ID, "missing-automation", 5, "")
	require.NoError(t, err)
	require.Empty(t, missingInvocations.Items)

	workItems, err := graph.ListWorkItems(ctx, project.ID, definition.Automation.ID, "", 5, "")
	require.NoError(t, err)
	require.Empty(t, workItems.Items)
	missingWorkItems, err := graph.ListWorkItems(ctx, project.ID, "missing-automation", "", 5, "")
	require.NoError(t, err)
	require.Empty(t, missingWorkItems.Items)
	_, err = graph.ListWorkItems(ctx, project.ID, definition.Automation.ID, "invalid-status", 5, "")
	require.Error(t, err)
}
