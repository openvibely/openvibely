package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationPublicationPlanIsDeterministicAndCompilerIsIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Publish")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)

	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	first, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	second, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, first.PlanRevision, second.PlanRevision)
	require.NotEmpty(t, first.Effects)
	require.Empty(t, first.Validation)

	compiler := NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: first.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, models.AutomationActive, published.Definition.Automation.LifecycleState)
	require.Equal(t, models.AutomationVersionPublished, published.Definition.Version.State)
	require.NotEmpty(t, published.Resources)
	storedSchedule, err := scheduleRepo.GetByID(context.Background(), publishedScheduleID(t, published))
	require.NoError(t, err)
	require.True(t, storedSchedule.Enabled, "trigger becomes runnable only with the published version")

	taskCount, scheduleCount := tableCount(t, db, "tasks"), tableCount(t, db, "schedules")
	retried, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: first.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, published.Attempt.ID, retried.Attempt.ID)
	require.Equal(t, taskCount, tableCount(t, db, "tasks"))
	require.Equal(t, scheduleCount, tableCount(t, db, "schedules"))
}

func TestAutomationPublicationRejectsStalePlanAndLifecycleTouchesOwnedTriggersOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Lifecycle")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)

	candidate.Name = "Changed after preview"
	_, err = drafts.UpdateDraft(context.Background(), created.Definition.Automation.ID, created.Definition.Version.ID, project.ID, candidate)
	require.NoError(t, err)
	compiler := NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, planner)
	_, err = compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.ErrorContains(t, err, "stale publication plan")
	require.Zero(t, tableCount(t, db, "tasks"))

	fresh, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: fresh.PlanRevision})
	require.NoError(t, err)
	ownedSchedule := publishedScheduleID(t, published)

	sharedTask, sharedSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Unrelated shared worker")
	_ = sharedTask
	lifecycle := NewAutomationLifecycleService(automationRepo, scheduleRepo)
	require.NoError(t, lifecycle.Pause(context.Background(), project.ID, created.Definition.Automation.ID))
	owned, err := scheduleRepo.GetByID(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.False(t, owned.Enabled)
	shared, err := scheduleRepo.GetByID(context.Background(), sharedSchedule.ID)
	require.NoError(t, err)
	require.True(t, shared.Enabled, "unowned schedules must remain untouched")
	require.NoError(t, lifecycle.Resume(context.Background(), project.ID, created.Definition.Automation.ID))
	owned, err = scheduleRepo.GetByID(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.True(t, owned.Enabled)
	require.NoError(t, lifecycle.Archive(context.Background(), project.ID, created.Definition.Automation.ID))
	owned, err = scheduleRepo.GetByID(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.False(t, owned.Enabled)
	owner, err := automationRepo.GetTriggerOwner(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.Nil(t, owner, "archive must release exclusive trigger ownership after disabling it")
	require.ErrorContains(t, lifecycle.Resume(context.Background(), project.ID, created.Definition.Automation.ID), "archived")
}

func TestAutomationDeleteDisablesOwnedTriggersAndRemovesOnlyAutomationRecords(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Delete Automation")
	other := automationTestProject(t, projectRepo, "Delete Automation Other")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Delete-owned trigger")
	registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
	definition, _, err := registration.Register(ctx, AutomationRegistrationRequest{
		ProjectID:  project.ID,
		AdapterKey: AutomationAdapterNativeSDLC,
		StableKey:  "native-sdlc/delete-test",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
			{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: task.ID},
		},
	})
	require.NoError(t, err)
	var triggerNodeID string
	for _, node := range definition.Nodes {
		if node.NodeKey == "suggestion_trigger" {
			triggerNodeID = node.ID
		}
	}
	require.NotEmpty(t, triggerNodeID)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, completed_at)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'delete-history', 'completed', CURRENT_TIMESTAMP)`, project.ID, definition.Automation.ID, definition.Version.ID, triggerNodeID, schedule.ID)
	require.NoError(t, err)

	lifecycle := NewAutomationLifecycleService(automationRepo, scheduleRepo)
	require.ErrorContains(t, lifecycle.Delete(ctx, other.ID, definition.Automation.ID), "not found")
	stillOwned, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.True(t, stillOwned.Enabled, "cross-project delete must not touch the owned trigger")

	require.NoError(t, lifecycle.Delete(ctx, project.ID, definition.Automation.ID))
	gone, err := automationRepo.GetDefinition(ctx, project.ID, definition.Automation.ID)
	require.NoError(t, err)
	require.Nil(t, gone)
	require.Zero(t, tableCountWhere(t, db, "automation_versions", "automation_id", definition.Automation.ID))
	require.Zero(t, tableCountWhere(t, db, "automation_invocations", "automation_id", definition.Automation.ID))
	require.Equal(t, 1, tableCountWhere(t, db, "tasks", "id", task.ID), "existing tasks remain authoritative")
	require.Equal(t, 1, tableCountWhere(t, db, "schedules", "id", schedule.ID), "existing schedules remain authoritative")
	preservedSchedule, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.False(t, preservedSchedule.Enabled)
	owner, err := automationRepo.GetTriggerOwner(ctx, schedule.ID)
	require.NoError(t, err)
	require.Nil(t, owner)
}

func tableCountWhere(t *testing.T, db *sql.DB, table, column, value string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, value).Scan(&count))
	return count
}

func TestAutomationPublicationPlanGoldenRevisionExcludesLayoutAndMessages(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Golden")
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	definition, err := automationRepo.CreateAutomationDraft(context.Background(), repository.AutomationDraftWrite{
		ProjectID: project.ID, AutomationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", VersionID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		StableKey: "golden/vision", Source: "template", Candidate: candidate,
	})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, repository.NewTaskRepo(db, nil), repository.NewScheduleRepo(db), registry, drafts)
	first, err := planner.Plan(context.Background(), project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, "4d5335b5db47d2e2a0f1bfeedec71dab1b10ac1d270dea0dbba085446961d713", first.PlanRevision)

	candidate.Assumptions = []string{"Layout-only author note"}
	candidate.Warnings = []string{"Operational observation"}
	_, err = drafts.UpdateDraft(context.Background(), definition.Automation.ID, definition.Version.ID, project.ID, candidate)
	require.NoError(t, err)
	second, err := planner.Plan(context.Background(), project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, first.PlanRevision, second.PlanRevision, "non-compilation messages must not stale a confirmed plan")
}

func TestAutomationPublicationPlanGoldenGitHubDependenciesAndConfigurationChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{ID: "cccccccccccccccccccccccccccccccc", Name: "GitHub Golden", RepoURL: "https://github.com/example/repository"}
	_, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path, repo_url) VALUES (?, ?, '', '', ?)`, project.ID, project.Name, project.RepoURL)
	require.NoError(t, err)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	userID := int64(42)
	inbox := &models.GitHubProjectInbox{ProjectID: project.ID, GitHubLogin: "dev-inbox", GitHubUserID: &userID, Enabled: true}
	require.NoError(t, githubAuthRepo.UpsertProjectInbox(ctx, inbox))
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	definition, err := automationRepo.CreateAutomationDraft(ctx, repository.AutomationDraftWrite{
		ProjectID: project.ID, AutomationID: "dddddddddddddddddddddddddddddddd", VersionID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		StableKey: "golden/github", Source: "template", Candidate: candidate,
	})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, repository.NewTaskRepo(db, nil), repository.NewScheduleRepo(db), registry, drafts)
	planner.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)
	first, err := planner.Plan(ctx, project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, "bb7c0e44c397649f45b852d98cbf590a1dbe3b216544d3bfa29888a4f1a506ca", first.PlanRevision)

	inbox.Enabled = false
	require.NoError(t, githubAuthRepo.UpsertProjectInbox(ctx, inbox))
	second, err := planner.Plan(ctx, project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.NotEqual(t, first.PlanRevision, second.PlanRevision, "GitHub inbox enabled state is a compilation dependency")
}

func TestAutomationPublicationScheduleIsJournaledDisabledBeforePublish(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Disabled trigger")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	drafts := NewAutomationDraftService(automationRepo, NewAutomationAdapterRegistry())
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	effect := models.AutomationPublicationEffect{StepKey: "schedule:vision_trigger", Operation: "create", TargetKey: "schedule:vision_trigger", ResourceType: "schedule", Name: "Vision Trigger"}
	snapshot, err := automationRepo.ReservePublicationAttempt(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID, "prepublish", []models.AutomationPublicationEffect{effect})
	require.NoError(t, err)
	require.NoError(t, automationRepo.MarkPublicationStep(context.Background(), snapshot.Attempt.ID, effect.StepKey, "running", "", ""))
	task := models.Task{ProjectID: project.ID, Title: "Target", Prompt: "target", Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending}
	require.NoError(t, taskRepo.Create(context.Background(), &task))
	runAt := time.Now().UTC().Add(time.Hour)
	schedule := models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.CreateForAutomationPublication(context.Background(), &schedule, snapshot.Attempt.ID, effect.StepKey))
	require.False(t, schedule.Enabled)
	stored, err := scheduleRepo.GetByID(context.Background(), schedule.ID)
	require.NoError(t, err)
	require.False(t, stored.Enabled, "an unpublished trigger must not be runnable")
	journal, err := automationRepo.GetPublicationAttempt(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID, "prepublish")
	require.NoError(t, err)
	require.Equal(t, schedule.ID, journal.Steps[0].ResourceID, "schedule creation and journal identity must commit together")
}

type blockingAutomationTaskMutationService struct {
	inner   automationTaskMutationService
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingAutomationTaskMutationService) Create(ctx context.Context, task *models.Task) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
	}
	return s.inner.Create(ctx, task)
}

func (s *blockingAutomationTaskMutationService) Update(ctx context.Context, task *models.Task) error {
	return s.inner.Update(ctx, task)
}

type createThenFailAutomationTaskMutationService struct {
	inner automationTaskMutationService
	once  sync.Once
}

func (s *createThenFailAutomationTaskMutationService) Create(ctx context.Context, task *models.Task) error {
	var result error
	s.once.Do(func() {
		result = s.inner.Create(ctx, task)
		if result == nil {
			result = errors.New("simulated response loss after task commit")
		}
	})
	if result == nil {
		return s.inner.Create(ctx, task)
	}
	return result
}

func (s *createThenFailAutomationTaskMutationService) Update(ctx context.Context, task *models.Task) error {
	return s.inner.Update(ctx, task)
}

func TestAutomationPublicationLeaseSerializesCompilers(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Concurrent compiler")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	realTaskService := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	blocking := &blockingAutomationTaskMutationService{inner: realTaskService, started: make(chan struct{}), release: make(chan struct{})}
	firstCompiler := NewAutomationCompiler(automationRepo, blocking, taskRepo, scheduleRepo, planner)
	secondCompiler := NewAutomationCompiler(automationRepo, realTaskService, taskRepo, scheduleRepo, planner)
	request := AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision}

	firstResult := make(chan error, 1)
	go func() {
		_, publishErr := firstCompiler.Publish(context.Background(), request)
		firstResult <- publishErr
	}()
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first compiler did not reach task creation")
	}
	_, err = secondCompiler.Publish(context.Background(), request)
	require.ErrorIs(t, err, repository.ErrAutomationPublicationInProgress)
	close(blocking.release)
	require.NoError(t, <-firstResult)
	require.Equal(t, 1, tableCount(t, db, "tasks"))
	require.Equal(t, 1, tableCount(t, db, "schedules"))
}

func TestAutomationPublicationAmbiguousTaskCreationReportsAndReconcilesResource(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Ambiguous compiler")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	realTaskService := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	failing := &createThenFailAutomationTaskMutationService{inner: realTaskService}
	compiler := NewAutomationCompiler(automationRepo, failing, taskRepo, scheduleRepo, planner)
	request := AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision}

	failed, err := compiler.Publish(context.Background(), request)
	require.ErrorContains(t, err, "response loss")
	require.NotNil(t, failed)
	require.Len(t, failed.Resources, 1)
	require.Equal(t, "ambiguous", failed.Resources[0].Status)
	require.NotEmpty(t, failed.Resources[0].ResourceID, "partial result must identify the committed visible task")
	require.Equal(t, models.AutomationVersionDraft, failed.Definition.Version.State)
	require.Equal(t, 1, tableCount(t, db, "tasks"))
	require.Zero(t, tableCount(t, db, "schedules"))

	retryCompiler := NewAutomationCompiler(automationRepo, realTaskService, taskRepo, scheduleRepo, planner)
	published, err := retryCompiler.Publish(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, models.AutomationVersionPublished, published.Definition.Version.State)
	require.Equal(t, 1, tableCount(t, db, "tasks"), "retry must reconcile the stable compiler task")
	require.Equal(t, 1, tableCount(t, db, "schedules"))
}

func TestAutomationResumePreservesConfiguredDisabledTrigger(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Disabled resume")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Nodes[0].Config["enabled"] = false
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)
	scheduleID := publishedScheduleID(t, published)
	stored, err := scheduleRepo.GetByID(context.Background(), scheduleID)
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	lifecycle := NewAutomationLifecycleService(automationRepo, scheduleRepo)
	require.NoError(t, lifecycle.Pause(context.Background(), project.ID, created.Definition.Automation.ID))
	require.NoError(t, lifecycle.Resume(context.Background(), project.ID, created.Definition.Automation.ID))
	stored, err = scheduleRepo.GetByID(context.Background(), scheduleID)
	require.NoError(t, err)
	require.False(t, stored.Enabled, "resume must restore the published trigger configuration, not enable every owner row")
}

type failUpdateAutomationTaskMutationService struct {
	inner automationTaskMutationService
}

func (s *failUpdateAutomationTaskMutationService) Create(ctx context.Context, task *models.Task) error {
	return s.inner.Create(ctx, task)
}

func (s *failUpdateAutomationTaskMutationService) Update(context.Context, *models.Task) error {
	return errors.New("simulated task update failure")
}

func TestAutomationReplacementFailureKeepsPriorVersionAndSuccessfulRetrySwitchesTrigger(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Replacement publication")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	realTaskService := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	compiler := NewAutomationCompiler(automationRepo, realTaskService, taskRepo, scheduleRepo, planner)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	first, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)
	firstScheduleID := publishedScheduleID(t, first)

	cloned, err := drafts.ClonePublishedVersion(context.Background(), project.ID, created.Definition.Automation.ID)
	require.NoError(t, err)
	operationalBaseline, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, cloned.Definition.Version.ID)
	require.NoError(t, err)
	lastRun := time.Now().UTC()
	nextRun := lastRun.Add(24 * time.Hour)
	require.NoError(t, scheduleRepo.MarkRan(context.Background(), firstScheduleID, lastRun, &nextRun))
	operationalUpdate, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, cloned.Definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, operationalBaseline.PlanRevision, operationalUpdate.PlanRevision, "volatile next-run and last-run state must not stale a publication plan")
	for i := range cloned.Candidate.Nodes {
		if _, ok := cloned.Candidate.Nodes[i].Config["prompt"]; ok {
			cloned.Candidate.Nodes[i].Config["prompt"] = cloned.Candidate.Nodes[i].Config["prompt"].(string) + " Updated."
		}
		if _, ok := cloned.Candidate.Nodes[i].Config["run_at"]; ok {
			cloned.Candidate.Nodes[i].Config["run_at"] = "10:30"
		}
	}
	cloned, err = drafts.UpdateDraft(context.Background(), created.Definition.Automation.ID, cloned.Definition.Version.ID, project.ID, cloned.Candidate)
	require.NoError(t, err)
	replacementPlan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, cloned.Definition.Version.ID)
	require.NoError(t, err)
	failingCompiler := NewAutomationCompiler(automationRepo, &failUpdateAutomationTaskMutationService{inner: realTaskService}, taskRepo, scheduleRepo, planner)
	failed, err := failingCompiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: cloned.Definition.Version.ID, PlanRevision: replacementPlan.PlanRevision})
	require.ErrorContains(t, err, "task update failure")
	require.NotNil(t, failed)
	current, err := automationRepo.GetDefinition(context.Background(), project.ID, created.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, first.Definition.Version.ID, current.Version.ID)
	firstSchedule, err := scheduleRepo.GetByID(context.Background(), firstScheduleID)
	require.NoError(t, err)
	require.True(t, firstSchedule.Enabled)

	second, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: cloned.Definition.Version.ID, PlanRevision: replacementPlan.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, cloned.Definition.Version.ID, second.Definition.Version.ID)
	require.Equal(t, 1, tableCount(t, db, "tasks"), "replacement reuses the owned task")
	require.Equal(t, 2, tableCount(t, db, "schedules"), "cadence changes create a new exclusive trigger")
	firstSchedule, err = scheduleRepo.GetByID(context.Background(), firstScheduleID)
	require.NoError(t, err)
	require.False(t, firstSchedule.Enabled)
	newScheduleID := publishedScheduleID(t, second)
	require.NotEqual(t, firstScheduleID, newScheduleID)
	owner, err := automationRepo.GetTriggerOwner(context.Background(), newScheduleID)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, second.Definition.Version.ID, owner.VersionID)
}

func tableCount(t *testing.T, db interface{ QueryRow(string, ...any) *sql.Row }, table string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
	return count
}

func publishedScheduleID(t *testing.T, result *AutomationPublishResult) string {
	t.Helper()
	for _, resource := range result.Resources {
		if resource.ResourceType == "schedule" {
			return resource.ResourceID
		}
	}
	t.Fatal("published result did not include a schedule")
	return ""
}
