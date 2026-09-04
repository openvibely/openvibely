package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
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

func TestAutomationSaveValidatorAgentIssuesBatchesSelectableAgentReferences(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Batched Agent Validation")
	agentRepo := repository.NewAgentRepo(db)
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents: %v", err)
	}

	refs := make([]string, 0, automationCapabilityLimit)
	var firstAgent *models.Agent
	for i := 0; i < automationCapabilityLimit; i++ {
		agent := automationValidationFixtureAgent(fmt.Sprintf("Agent %02d", i), fmt.Sprintf("agent-%02d", i))
		if err := agentRepo.Create(ctx, agent); err != nil {
			t.Fatalf("create fixture Agent %d: %v", i, err)
		}
		if firstAgent == nil {
			firstAgent = agent
		}
		refs = append(refs, agent.Key)
	}

	validator := &AutomationSaveValidator{agentRepo: agentRepo}
	for _, referenceCount := range []int{1, 5, 20, 50} {
		t.Run(fmt.Sprintf("references_%d", referenceCount), func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			issues, err := validator.agentIssues(ctx, project.ID, automationValidationReferenceCandidate(refs[:referenceCount]))
			counter.SetEnabled(false)
			if err != nil {
				t.Fatalf("agentIssues: %v", err)
			}
			if len(issues) != 0 {
				t.Fatalf("agentIssues = %#v, want no issues for valid references", issues)
			}

			statements := selectableAgentValidationStatements(counter.Statements())
			if len(statements) != 1 {
				t.Fatalf("selectable Agent statements = %#v, want exactly one query", statements)
			}
			query := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
			projection := strings.TrimSpace(strings.SplitN(query, " from agents", 2)[0])
			for _, required := range []string{"select id", "coalesce(key, '')", "project_id"} {
				if !strings.Contains(projection, required) {
					t.Fatalf("validation projection = %q, want %q in %s", projection, required, statements[0])
				}
			}
			for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("validation projection selected forbidden column %q: %s", forbidden, statements[0])
				}
			}
			for _, requiredPredicate := range []string{"coalesce(generated_status, 'user_edited') <> 'archived'", "archived_at is null", "coalesce(enabled, 1) = 1", "coalesce(selectable_as_primary, 1) = 1", "project_id is null or project_id = '' or project_id = ?", "order by name asc, id asc", "limit ?"} {
				if !strings.Contains(query, requiredPredicate) {
					t.Fatalf("validation query is missing %q: %s", requiredPredicate, statements[0])
				}
			}
		})
	}

	sharedRefs := make([]string, 5)
	for i := range sharedRefs {
		sharedRefs[i] = refs[0]
	}
	counter.Reset()
	counter.SetEnabled(true)
	issues, err := validator.agentIssues(ctx, project.ID, automationValidationReferenceCandidate(sharedRefs))
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("agentIssues with shared reference: %v", err)
	}
	if len(issues) != 0 || len(selectableAgentValidationStatements(counter.Statements())) != 1 {
		t.Fatalf("shared-reference validation issues=%#v statements=%#v, want no issues and one query", issues, counter.Statements())
	}

	counter.Reset()
	counter.SetEnabled(true)
	issues, err = validator.agentIssues(ctx, project.ID, models.AutomationDraftCandidate{Nodes: []models.AutomationDraftNode{
		{Key: "unreferenced", Type: models.AutomationNodeAgentTask, Config: map[string]any{}},
		{Key: "blank-reference", Type: models.AutomationNodeTrigger, Config: map[string]any{"agent_ref": " \t "}},
	}})
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("agentIssues without references: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("agentIssues without references = %#v, want no issues", issues)
	}
	if statements := selectableAgentValidationStatements(counter.Statements()); len(statements) != 0 {
		t.Fatalf("selectable Agent statements without references = %#v, want none", statements)
	}

	full, err := agentRepo.GetByID(ctx, firstAgent.ID)
	if err != nil {
		t.Fatalf("get full fixture Agent: %v", err)
	}
	if full == nil || full.SystemPrompt == "" || len(full.Tools) == 0 || len(full.ToolConfig.ScopedFiles) == 0 || len(full.Plugins) == 0 || len(full.MCPServers) == 0 || len(full.Skills) == 0 || len(full.SourceRefs) == 0 || !full.PermissionDefaults.ReadAgents || full.ModelDefaults.Model != "gpt-5" {
		t.Fatalf("full Agent detail path lost rich fields: %#v", full)
	}
}

func TestAutomationSaveValidatorAgentIssuesPreservesReferenceAvailabilitySemantics(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Agent Validation Project")
	otherProject := automationTestProject(t, projectRepo, "Other Agent Validation Project")
	agentRepo := repository.NewAgentRepo(db)
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents: %v", err)
	}

	global := automationValidationFixtureAgent("Duplicate name", "global-agent")
	if err := agentRepo.Create(ctx, global); err != nil {
		t.Fatalf("create global Agent: %v", err)
	}
	scoped := automationValidationFixtureAgent("Scoped Agent", "scoped-agent")
	scoped.Scope = models.AgentScopeProject
	scoped.ProjectID = project.ID
	if err := agentRepo.Create(ctx, scoped); err != nil {
		t.Fatalf("create scoped Agent: %v", err)
	}
	foreign := automationValidationFixtureAgent("Foreign Agent", "foreign-agent")
	foreign.Scope = models.AgentScopeProject
	foreign.ProjectID = otherProject.ID
	if err := agentRepo.Create(ctx, foreign); err != nil {
		t.Fatalf("create foreign Agent: %v", err)
	}
	disabled := automationValidationFixtureAgent("Disabled Agent", "disabled-agent")
	disabled.Enabled = false
	if err := agentRepo.Create(ctx, disabled); err != nil {
		t.Fatalf("create disabled Agent: %v", err)
	}
	nonSelectable := automationValidationFixtureAgent("Non-selectable Agent", "non-selectable-agent")
	nonSelectable.SelectableAsPrimary = false
	if err := agentRepo.Create(ctx, nonSelectable); err != nil {
		t.Fatalf("create non-selectable Agent: %v", err)
	}
	archived := automationValidationFixtureAgent("Archived Agent", "archived-agent")
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := agentRepo.Create(ctx, archived); err != nil {
		t.Fatalf("create archived Agent: %v", err)
	}
	archivedAt := automationValidationFixtureAgent("Archived at Agent", "archived-at-agent")
	if err := agentRepo.Create(ctx, archivedAt); err != nil {
		t.Fatalf("create archived-at Agent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agents SET archived_at = datetime('now') WHERE id = ?`, archivedAt.ID); err != nil {
		t.Fatalf("archive Agent by timestamp: %v", err)
	}
	blankKey := automationValidationFixtureAgent("Blank key Agent", "")
	if err := agentRepo.Create(ctx, blankKey); err != nil {
		t.Fatalf("create blank-key Agent: %v", err)
	}
	duplicateName := automationValidationFixtureAgent("Duplicate name", "duplicate-name-agent")
	duplicateName.Enabled = false
	if err := agentRepo.Create(ctx, duplicateName); err != nil {
		t.Fatalf("create duplicate-name Agent: %v", err)
	}

	refs := []string{"  " + global.Key + " ", scoped.Key, foreign.Key, disabled.Key, nonSelectable.Key, archived.Key, archivedAt.Key, blankKey.ID, "Duplicate name"}
	validator := &AutomationSaveValidator{agentRepo: agentRepo}
	counter.Reset()
	counter.SetEnabled(true)
	issues, err := validator.agentIssues(ctx, project.ID, automationValidationReferenceCandidate(refs))
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("agentIssues: %v", err)
	}
	if len(issues) != 6 {
		t.Fatalf("agentIssues = %#v, want six unavailable references", issues)
	}
	unavailable := map[string]bool{}
	for _, issue := range issues {
		unavailable[issue.NodeKey] = true
	}
	for _, index := range []int{2, 3, 4, 5, 6, 8} {
		if !unavailable[fmt.Sprintf("agent-%02d", index)] {
			t.Fatalf("missing unavailable issue for reference index %d: %#v", index, issues)
		}
	}
	if len(selectableAgentValidationStatements(counter.Statements())) != 1 {
		t.Fatalf("selectable Agent statements = %#v, want one query", counter.Statements())
	}
	resolvedArchivedAt, err := resolveAutomationAgent(ctx, agentRepo, project.ID, archivedAt.Key)
	if err != nil {
		t.Fatalf("resolve timestamp-archived Agent: %v", err)
	}
	if resolvedArchivedAt != nil {
		t.Fatalf("timestamp-archived Agent resolved for materialization: %#v", resolvedArchivedAt)
	}

	for i := 0; i < automationCapabilityLimit; i++ {
		agent := automationValidationFixtureAgent(fmt.Sprintf("Catalog %02d", i), fmt.Sprintf("catalog-%02d", i))
		if err := agentRepo.Create(ctx, agent); err != nil {
			t.Fatalf("create catalog Agent %d: %v", i, err)
		}
	}
	outside := automationValidationFixtureAgent("zz outside catalog", "outside-catalog")
	if err := agentRepo.Create(ctx, outside); err != nil {
		t.Fatalf("create outside-catalog Agent: %v", err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	issues, err = validator.agentIssues(ctx, project.ID, automationValidationReferenceCandidate([]string{outside.Key}))
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("agentIssues outside limit: %v", err)
	}
	if len(issues) != 1 || issues[0].NodeKey != "agent-00" {
		t.Fatalf("outside-limit issues = %#v, want one issue for the reference", issues)
	}
	if len(selectableAgentValidationStatements(counter.Statements())) != 1 {
		t.Fatalf("outside-limit selectable Agent statements = %#v, want one query", counter.Statements())
	}
}

func TestAutomationCompilerPreviewAndSaveUseBatchedAgentValidation(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Compiler Agent Validation")
	agentRepo := repository.NewAgentRepo(db)
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents: %v", err)
	}
	refs := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		agent := automationValidationFixtureAgent(fmt.Sprintf("Compiler Agent %02d", i), fmt.Sprintf("compiler-agent-%02d", i))
		if err := agentRepo.Create(ctx, agent); err != nil {
			t.Fatalf("create compiler fixture Agent %d: %v", i, err)
		}
		refs = append(refs, agent.Key)
	}

	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(repository.NewAutomationRepo(db), registry)
	capabilities := NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, nil, nil)
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	validator := NewAutomationSaveValidator(registry, drafts)
	validator.SetAgentRepository(agentRepo)
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, validator)

	counter.Reset()
	counter.SetEnabled(true)
	plan, _, err := compiler.PreviewSave(ctx, project.ID, automationValidationReferenceCandidate(refs))
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("PreviewSave: %v", err)
	}
	if plan == nil || len(plan.Validation) != 0 {
		t.Fatalf("PreviewSave validation = %#v, want no issues for available references", plan)
	}
	if len(selectableAgentValidationStatements(counter.Statements())) != 1 {
		t.Fatalf("PreviewSave selectable Agent statements = %#v, want one compact query", counter.Statements())
	}
	if statements := selectableAgentValidationStatements(counter.Statements()); len(statements) == 1 {
		assertAutomationValidationProjection(t, statements[0])
	}

	noReferences := customTaskOnlyCandidate("No Agent references", "Run without a primary Agent.", models.CategoryBacklog)
	counter.Reset()
	counter.SetEnabled(true)
	noReferencePlan, _, err := compiler.PreviewSave(ctx, project.ID, noReferences)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("PreviewSave without Agent reference: %v", err)
	}
	if noReferencePlan == nil || len(noReferencePlan.Validation) != 0 {
		t.Fatalf("PreviewSave without Agent reference validation = %#v, want no issues", noReferencePlan)
	}
	if statements := selectableAgentValidationStatements(counter.Statements()); len(statements) != 0 {
		t.Fatalf("PreviewSave selectable Agent statements without references = %#v, want none", statements)
	}

	invalid := automationValidationReferenceCandidate([]string{"missing-agent"})
	counter.Reset()
	counter.SetEnabled(true)
	_, err = compiler.Save(ctx, AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: invalid})
	counter.SetEnabled(false)
	if err == nil {
		t.Fatal("Save with an unavailable Agent reference succeeded")
	}
	if len(selectableAgentValidationStatements(counter.Statements())) != 1 {
		t.Fatalf("Save selectable Agent statements = %#v, want one query", counter.Statements())
	}
	if countRows(t, db, `SELECT COUNT(*) FROM automations`) != 0 || countRows(t, db, `SELECT COUNT(*) FROM tasks`) != 0 || countRows(t, db, `SELECT COUNT(*) FROM schedules`) != 0 {
		t.Fatal("invalid Agent reference persisted Automation resources")
	}
}

func TestAutomationSaveValidatorAgentIssuesMeetsRichFixturePerformanceBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped Automation Agent validation performance fixture in short mode")
	}

	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Automation Agent validation performance")
	agentRepo := repository.NewAgentRepo(db)
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents: %v", err)
	}
	refs := make([]string, 0, automationCapabilityLimit)
	for i := 0; i < automationCapabilityLimit; i++ {
		agent := automationValidationFixtureAgent(fmt.Sprintf("Budget Agent %02d", i), fmt.Sprintf("budget-agent-%02d", i))
		if err := agentRepo.Create(ctx, agent); err != nil {
			t.Fatalf("create budget Agent %d: %v", i, err)
		}
		refs = append(refs, agent.Key)
	}
	registry := NewAutomationAdapterRegistry()
	automationRepo := repository.NewAutomationRepo(db)
	drafts := NewAutomationDraftService(automationRepo, registry)
	capabilities := NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, nil, nil)
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	validator := NewAutomationSaveValidator(registry, drafts)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, validator)

	for _, referenceCount := range []int{1, 5, 20, 50} {
		t.Run(fmt.Sprintf("references_%d", referenceCount), func(t *testing.T) {
			candidate := automationValidationReferenceCandidate(refs[:referenceCount])
			baselineAllocs := testing.AllocsPerRun(3, func() {
				if err := automationValidationBaselineConfigured(ctx, drafts, capabilities, agentRepo, project.ID, candidate); err != nil {
					t.Fatalf("baseline validation: %v", err)
				}
			})
			batchedAllocs := testing.AllocsPerRun(3, func() {
				if err := automationValidationProduction(ctx, compiler, project.ID, candidate); err != nil {
					t.Fatalf("batched validation: %v", err)
				}
			})
			baselineBytes := allocatedAutomationValidationBytes(t, 3, func() error {
				return automationValidationBaselineConfigured(ctx, drafts, capabilities, agentRepo, project.ID, candidate)
			})
			batchedBytes := allocatedAutomationValidationBytes(t, 3, func() error {
				return automationValidationProduction(ctx, compiler, project.ID, candidate)
			})
			baselineWall := medianAutomationValidationDuration(t, 3, func() error {
				return automationValidationBaselineConfigured(ctx, drafts, capabilities, agentRepo, project.ID, candidate)
			})
			batchedWall := medianAutomationValidationDuration(t, 3, func() error {
				return automationValidationProduction(ctx, compiler, project.ID, candidate)
			})

			if batchedAllocs > baselineAllocs {
				t.Fatalf("batched allocations = %.0f, baseline = %.0f; one-reference validation regressed", batchedAllocs, baselineAllocs)
			}
			if batchedBytes > baselineBytes {
				t.Fatalf("batched allocated bytes = %d, baseline = %d; one-reference validation regressed", batchedBytes, baselineBytes)
			}
			if batchedWall > baselineWall {
				t.Fatalf("batched wall time = %s, baseline = %s; one-reference validation regressed", batchedWall, baselineWall)
			}
			if referenceCount == 20 || referenceCount == 50 {
				if batchedAllocs*10 > baselineAllocs {
					t.Fatalf("batched allocations = %.0f, baseline = %.0f; want at least 90%% reduction", batchedAllocs, baselineAllocs)
				}
				if batchedBytes*10 > baselineBytes {
					t.Fatalf("batched allocated bytes = %d, baseline = %d; want at least 90%% reduction", batchedBytes, baselineBytes)
				}
				if batchedWall*10 > baselineWall {
					t.Fatalf("batched wall time = %s, baseline = %s; want at least 90%% reduction", batchedWall, baselineWall)
				}
			}
			t.Logf("references=%d baseline=%s/%d B/%.0f allocs, batched=%s/%d B/%.0f allocs", referenceCount, baselineWall, baselineBytes, baselineAllocs, batchedWall, batchedBytes, batchedAllocs)
		})
	}
}

func allocatedAutomationValidationBytes(t *testing.T, runs int, validate func() error) uint64 {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		if err := validate(); err != nil {
			t.Fatalf("validation sample %d: %v", i, err)
		}
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

func medianAutomationValidationDuration(t *testing.T, samples int, validate func() error) time.Duration {
	t.Helper()
	durations := make([]time.Duration, samples)
	for i := range durations {
		started := time.Now()
		if err := validate(); err != nil {
			t.Fatalf("validation sample %d: %v", i, err)
		}
		durations[i] = time.Since(started)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

func BenchmarkAutomationAgentReferenceValidationProjection(b *testing.B) {
	db, counter := testutil.NewStatementCountingTestDB(b)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := models.Project{Name: "Automation Agent validation benchmark"}
	require.NoError(b, projectRepo.Create(ctx, &project))
	agentRepo := repository.NewAgentRepo(db)
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		b.Fatalf("clear agents: %v", err)
	}
	refs := make([]string, 0, automationCapabilityLimit)
	for i := 0; i < automationCapabilityLimit; i++ {
		agent := automationValidationFixtureAgent(fmt.Sprintf("Benchmark Agent %02d", i), fmt.Sprintf("benchmark-agent-%02d", i))
		if err := agentRepo.Create(ctx, agent); err != nil {
			b.Fatalf("create benchmark Agent %d: %v", i, err)
		}
		refs = append(refs, agent.Key)
	}
	registry := NewAutomationAdapterRegistry()
	automationRepo := repository.NewAutomationRepo(db)
	drafts := NewAutomationDraftService(automationRepo, registry)
	capabilities := NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, nil, nil)
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	validator := NewAutomationSaveValidator(registry, drafts)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, validator)

	for _, referenceCount := range []int{1, 5, 20, 50} {
		candidate := automationValidationReferenceCandidate(refs[:referenceCount])
		b.Run(fmt.Sprintf("baseline_%d_references", referenceCount), func(b *testing.B) {
			counter.Reset()
			counter.SetEnabled(true)
			if err := automationValidationBaselineConfigured(ctx, drafts, capabilities, agentRepo, project.ID, candidate); err != nil {
				counter.SetEnabled(false)
				b.Fatalf("baseline validation: %v", err)
			}
			counter.SetEnabled(false)
			queryCount := len(selectableAgentValidationStatements(counter.Statements()))
			if queryCount != referenceCount+1 {
				b.Fatalf("baseline query count = %d, want %d", queryCount, referenceCount+1)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := automationValidationBaselineConfigured(ctx, drafts, capabilities, agentRepo, project.ID, candidate); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(queryCount), "agent_queries/op")
		})

		b.Run(fmt.Sprintf("batched_%d_references", referenceCount), func(b *testing.B) {
			counter.Reset()
			counter.SetEnabled(true)
			if err := automationValidationProduction(ctx, compiler, project.ID, candidate); err != nil {
				counter.SetEnabled(false)
				b.Fatalf("batched validation: %v", err)
			}
			counter.SetEnabled(false)
			queryCount := len(selectableAgentValidationStatements(counter.Statements()))
			if queryCount != 1 {
				b.Fatalf("batched query count = %d, want 1", queryCount)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := automationValidationProduction(ctx, compiler, project.ID, candidate); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(queryCount), "agent_queries/op")
		})
	}
}

const automationLiveYAMLBenchmarkSamples = 3

type automationLiveYAMLBenchmarkFixture struct {
	ctx           context.Context
	project       models.Project
	counter       *testutil.SQLStatementCounter
	drafts        *AutomationDraftService
	graphSvc      *AutomationGraphService
	automationIDs map[int]string
}

func newAutomationLiveYAMLBenchmarkFixture(tb testing.TB, nodeCounts []int) *automationLiveYAMLBenchmarkFixture {
	tb.Helper()
	db, counter := testutil.NewStatementCountingTestDB(tb)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := models.Project{Name: "Automation Live YAML benchmark"}
	if err := projectRepo.Create(ctx, &project); err != nil {
		tb.Fatalf("create benchmark project: %v", err)
	}

	registry := NewAutomationAdapterRegistry()
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	drafts := NewAutomationDraftService(automationRepo, registry)
	capabilities := NewAutomationCapabilitySnapshotBuilder(projectRepo, nil, taskRepo, nil)
	capabilities.SetLLMConfigRepository(repository.NewLLMConfigRepo(db))
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	validator := NewAutomationSaveValidator(registry, drafts)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	compiler := NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, validator)
	automationIDs := make(map[int]string, len(nodeCounts))
	for _, nodeCount := range nodeCounts {
		saved, err := compiler.Save(ctx, AutomationSaveRequest{
			ProjectID: project.ID, Source: "manual", CreatedVia: "benchmark",
			Candidate: automationLiveBenchmarkCandidate(nodeCount),
		})
		if err != nil {
			tb.Fatalf("save %d-node benchmark fixture: %v", nodeCount, err)
		}
		automationIDs[nodeCount] = saved.Definition.Automation.ID
	}
	return &automationLiveYAMLBenchmarkFixture{ctx: ctx, project: project, counter: counter,
		drafts: drafts, graphSvc: NewAutomationGraphService(automationRepo), automationIDs: automationIDs}
}

func (f *automationLiveYAMLBenchmarkFixture) run(nodeCount int, before bool) error {
	automationID := f.automationIDs[nodeCount]
	graph, err := f.graphSvc.GetLive(f.ctx, f.project.ID, automationID, time.Now().UTC())
	if err != nil {
		return err
	}
	var current *models.AutomationDraftResult
	if before {
		current, err = f.drafts.CurrentCandidate(f.ctx, f.project.ID, automationID)
	} else {
		current, err = f.drafts.LoadLiveCandidate(f.ctx, f.project.ID, graph)
	}
	if err != nil {
		return err
	}
	if len(current.Candidate.Nodes) != nodeCount {
		return fmt.Errorf("Live path returned %d nodes, want %d", len(current.Candidate.Nodes), nodeCount)
	}
	return nil
}

func (f *automationLiveYAMLBenchmarkFixture) warm(nodeCount int, before bool) (int, error) {
	f.counter.Reset()
	f.counter.SetEnabled(true)
	err := f.run(nodeCount, before)
	f.counter.SetEnabled(false)
	return len(f.counter.Statements()), err
}

type automationLiveYAMLBenchmarkMetrics struct {
	wallNs          float64
	bytesPerOp      float64
	allocsPerOp     float64
	statementsPerOp float64
}

func medianAutomationLiveYAMLMetric(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	return ordered[len(ordered)/2]
}

func medianAutomationLiveYAMLMetrics(samples []automationLiveYAMLBenchmarkMetrics) automationLiveYAMLBenchmarkMetrics {
	wall := make([]float64, 0, len(samples))
	bytes := make([]float64, 0, len(samples))
	allocs := make([]float64, 0, len(samples))
	statements := make([]float64, 0, len(samples))
	for _, sample := range samples {
		wall = append(wall, sample.wallNs)
		bytes = append(bytes, sample.bytesPerOp)
		allocs = append(allocs, sample.allocsPerOp)
		statements = append(statements, sample.statementsPerOp)
	}
	return automationLiveYAMLBenchmarkMetrics{wallNs: medianAutomationLiveYAMLMetric(wall),
		bytesPerOp: medianAutomationLiveYAMLMetric(bytes), allocsPerOp: medianAutomationLiveYAMLMetric(allocs),
		statementsPerOp: medianAutomationLiveYAMLMetric(statements)}
}

func measureAutomationLiveYAMLPath(tb testing.TB, fixture *automationLiveYAMLBenchmarkFixture, nodeCount int, before bool) automationLiveYAMLBenchmarkMetrics {
	tb.Helper()
	queryCount, err := fixture.warm(nodeCount, before)
	if err != nil {
		tb.Fatalf("warm Live YAML path: %v", err)
	}
	wantQueries := 12
	if before {
		wantQueries = 20
	}
	if queryCount != wantQueries {
		tb.Fatalf("Live YAML path statements = %d, want %d", queryCount, wantQueries)
	}

	var runErr error
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if runErr = fixture.run(nodeCount, before); runErr != nil {
				return
			}
		}
	})
	if runErr != nil {
		tb.Fatalf("measure Live YAML path: %v", runErr)
	}
	return automationLiveYAMLBenchmarkMetrics{wallNs: float64(result.NsPerOp()),
		bytesPerOp: float64(result.AllocedBytesPerOp()), allocsPerOp: float64(result.AllocsPerOp()),
		statementsPerOp: float64(queryCount)}
}

func TestAutomationLiveYAMLPerformanceThresholds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped Live YAML performance fixture in short mode")
	}
	fixture := newAutomationLiveYAMLBenchmarkFixture(t, []int{30})
	beforeSamples := make([]automationLiveYAMLBenchmarkMetrics, 0, automationLiveYAMLBenchmarkSamples)
	afterSamples := make([]automationLiveYAMLBenchmarkMetrics, 0, automationLiveYAMLBenchmarkSamples)
	for sample := 0; sample < automationLiveYAMLBenchmarkSamples; sample++ {
		if sample%2 == 0 {
			beforeSamples = append(beforeSamples, measureAutomationLiveYAMLPath(t, fixture, 30, true))
			afterSamples = append(afterSamples, measureAutomationLiveYAMLPath(t, fixture, 30, false))
			continue
		}
		afterSamples = append(afterSamples, measureAutomationLiveYAMLPath(t, fixture, 30, false))
		beforeSamples = append(beforeSamples, measureAutomationLiveYAMLPath(t, fixture, 30, true))
	}
	before := medianAutomationLiveYAMLMetrics(beforeSamples)
	after := medianAutomationLiveYAMLMetrics(afterSamples)
	t.Logf("30-node Live YAML medians: before=%.0f ns/op %.0f B/op %.0f allocs/op %.0f SQL; after=%.0f ns/op %.0f B/op %.0f allocs/op %.0f SQL", before.wallNs, before.bytesPerOp, before.allocsPerOp, before.statementsPerOp, after.wallNs, after.bytesPerOp, after.allocsPerOp, after.statementsPerOp)
	require.Equal(t, float64(20), before.statementsPerOp)
	require.Equal(t, float64(12), after.statementsPerOp)
	if testing.CoverMode() == "" {
		require.LessOrEqual(t, after.wallNs, before.wallNs*0.70, "30-node Live YAML wall time must improve by at least 30%%")
	} else {
		require.Less(t, after.wallNs, before.wallNs, "30-node Live YAML path must remain faster under coverage instrumentation")
		t.Log("coverage instrumentation detected; enforcing the strict 30% wall-time gate in the uninstrumented benchmark only")
	}
	require.LessOrEqual(t, after.bytesPerOp, before.bytesPerOp*0.60, "30-node Live YAML allocated bytes must improve by at least 40%%")
	require.LessOrEqual(t, after.allocsPerOp, before.allocsPerOp*0.60, "30-node Live YAML allocation count must improve by at least 40%%")
}

// BenchmarkAutomationLiveYAMLCurrentAndLoadedPath compares the Live graph and
// candidate-loading portion measured by the issue's SQL/allocation probe. YAML
// encoding is intentionally outside the timed section because it is shared by
// both paths and is covered separately by the serialization equivalence tests.
func BenchmarkAutomationLiveYAMLCurrentAndLoadedPath(b *testing.B) {
	fixture := newAutomationLiveYAMLBenchmarkFixture(b, []int{1, 10, 30})
	for _, nodeCount := range []int{1, 10, 30} {
		nodeCount := nodeCount
		b.Run(fmt.Sprintf("before_%d_nodes", nodeCount), func(b *testing.B) {
			benchmarkAutomationLiveYAMLPath(b, fixture, nodeCount, true)
		})
		b.Run(fmt.Sprintf("after_%d_nodes", nodeCount), func(b *testing.B) {
			benchmarkAutomationLiveYAMLPath(b, fixture, nodeCount, false)
		})
	}
}

func benchmarkAutomationLiveYAMLPath(b *testing.B, fixture *automationLiveYAMLBenchmarkFixture, nodeCount int, before bool) {
	b.Helper()
	queryCount, err := fixture.warm(nodeCount, before)
	if err != nil {
		b.Fatalf("measure Live YAML path: %v", err)
	}
	wantQueries := 12
	if before {
		wantQueries = 20
	}
	if queryCount != wantQueries {
		b.Fatalf("Live YAML path statements = %d, want %d", queryCount, wantQueries)
	}

	var runErr error
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if runErr = fixture.run(nodeCount, before); runErr != nil {
			break
		}
	}
	b.StopTimer()
	if runErr != nil {
		b.Fatalf("measure Live YAML path: %v", runErr)
	}
	b.ReportMetric(float64(queryCount), "sql_statements/op")
}

func automationLiveBenchmarkCandidate(nodeCount int) models.AutomationDraftCandidate {
	promptSize := 4096
	if nodeCount >= 30 {
		promptSize = 1024
	}
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: fmt.Sprintf("Live benchmark %d nodes", nodeCount),
		Description: "Production-shaped Live YAML benchmark", AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
		Nodes: make([]models.AutomationDraftNode, 0, nodeCount)}
	for i := 0; i < nodeCount; i++ {
		candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{
			Key: fmt.Sprintf("schedule_%02d", i), Name: fmt.Sprintf("Schedule %02d", i),
			Type: models.AutomationNodeTrigger, Role: "fixed_schedule",
			Config: map[string]any{"prompt": strings.Repeat("p", promptSize), "category": string(models.CategoryScheduled),
				"priority": 2, "run_at": "09:00", "repeat_type": string(models.RepeatDaily), "repeat_interval": 1,
				"enabled": true, "clear_context_on_start": true},
			Position: &models.AutomationDraftPoint{X: float64(i * 220), Y: 0},
		})
	}
	return candidate
}

func automationValidationBaselineConfigured(ctx context.Context, drafts *AutomationDraftService, capabilities *AutomationCapabilitySnapshotBuilder, agentRepo *repository.AgentRepo, projectID string, candidate models.AutomationDraftCandidate) error {
	normalized, err := drafts.NormalizeCandidate(candidate)
	if err != nil {
		return err
	}
	snapshot, err := capabilities.Build(ctx, projectID)
	if err != nil {
		return err
	}
	issues := drafts.ValidateCandidateWithCapabilities(normalized, snapshot)
	if len(issues) != 0 {
		return fmt.Errorf("configured baseline validation issues = %#v", issues)
	}
	return automationValidationBaseline(ctx, agentRepo, projectID, normalized)
}

func automationValidationProduction(ctx context.Context, compiler *AutomationCompiler, projectID string, candidate models.AutomationDraftCandidate) error {
	plan, _, err := compiler.PreviewSave(ctx, projectID, candidate)
	if err != nil {
		return err
	}
	if plan == nil {
		return errors.New("configured production validation returned no plan")
	}
	if len(plan.Validation) != 0 {
		return fmt.Errorf("configured production validation issues = %#v", plan.Validation)
	}
	return nil
}

func automationValidationBaseline(ctx context.Context, agentRepo *repository.AgentRepo, projectID string, candidate models.AutomationDraftCandidate) error {
	for _, node := range candidate.Nodes {
		if node.Type != models.AutomationNodeAgentTask && node.Type != models.AutomationNodeTrigger {
			continue
		}
		ref, _ := node.Config["agent_ref"].(string)
		if strings.TrimSpace(ref) == "" {
			continue
		}
		agent, err := resolveAutomationAgent(ctx, agentRepo, projectID, ref)
		if err != nil {
			return err
		}
		if agent == nil {
			return fmt.Errorf("reference %q was unavailable", ref)
		}
	}
	return nil
}

func automationValidationReferenceCandidate(refs []string) models.AutomationDraftCandidate {
	nodes := make([]models.AutomationDraftNode, 0, len(refs))
	for i, ref := range refs {
		nodeType := models.AutomationNodeAgentTask
		config := map[string]any{"prompt": "Review the requested work.", "category": string(models.CategoryBacklog), "priority": 2, "agent_ref": ref}
		if i%2 == 1 {
			nodeType = models.AutomationNodeTrigger
			config["prompt"] = "Run the scheduled review."
			config["category"] = string(models.CategoryScheduled)
			config["run_at"] = "09:00"
			config["repeat_type"] = string(models.RepeatDaily)
			config["repeat_interval"] = 1
			config["enabled"] = true
		}
		nodes = append(nodes, models.AutomationDraftNode{Key: fmt.Sprintf("agent-%02d", i), Name: fmt.Sprintf("Agent node %02d", i), Type: nodeType, Role: map[models.AutomationNodeType]string{models.AutomationNodeAgentTask: "task", models.AutomationNodeTrigger: "fixed_schedule"}[nodeType], Config: config})
	}
	return models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Agent validation candidate", AutomationType: AutomationAdapterCustom, AdapterKey: AutomationAdapterCustom, Nodes: nodes}
}

func automationValidationFixtureAgent(name, key string) *models.Agent {
	return &models.Agent{
		Name:                name,
		Key:                 key,
		Description:         "production-shaped Automation validation Agent",
		SystemPrompt:        strings.Repeat("private validation prompt with irrelevant rich configuration. ", 320),
		Model:               "inherit",
		Tools:               []string{"Read", "Write", "Bash", models.AgentToolScopedFiles},
		ToolConfig:          models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read", "write"}}}},
		Plugins:             []string{"github@marketplace", "playwright@claude-plugins-official"},
		MCPServers:          []models.MCPServerConfig{{Name: "playwright", Command: []string{"npx", "-y", "@playwright/mcp"}, Env: map[string]string{"TOKEN": strings.Repeat("x", 256)}}},
		Skills:              []models.SkillConfig{{Name: "triage", Description: "rich validation skill", Content: strings.Repeat("skill body ", 256)}},
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 8192},
		SourceRefs:          []string{"agents/validation/SKILLS.md", strings.Repeat("ref", 128)},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
}

func assertAutomationValidationProjection(t *testing.T, statement string) {
	t.Helper()
	query := strings.ToLower(strings.Join(strings.Fields(statement), " "))
	projection := strings.TrimSpace(strings.SplitN(query, " from agents", 2)[0])
	for _, required := range []string{"select id", "coalesce(key, '')", "project_id"} {
		if !strings.Contains(projection, required) {
			t.Fatalf("validation projection = %q, want %q in %s", projection, required, statement)
		}
	}
	for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("validation projection selected forbidden column %q: %s", forbidden, statement)
		}
	}
}

func selectableAgentValidationStatements(statements []string) []string {
	filtered := make([]string, 0, len(statements))
	for _, statement := range statements {
		if strings.Contains(strings.ToLower(statement), "from agents") {
			filtered = append(filtered, statement)
		}
	}
	return filtered
}
