package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, native.Version.ID, again.Version.ID, "identical setup reruns must not create versions")
	require.Equal(t, "Updated Native Automation", again.Automation.Name)
	var retainedGraphID string
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO automation_versions
		(project_id, automation_id, version, state, source, adapter_key, published_at)
		VALUES (?, ?, 2, 'published', 'bootstrap', ?, CURRENT_TIMESTAMP) RETURNING id`, project.ID,
		native.Automation.ID, AutomationAdapterNativeSDLC).Scan(&retainedGraphID))
	again, reused, err = service.Register(ctx, nativeReq)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, native.Version.ID, again.Version.ID)
	require.Zero(t, tableCountWhere(t, db, "automation_versions", "id", retainedGraphID),
		"unchanged maintained registration must remove pre-existing retained graphs")
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

func TestGitHubSDLCRegistrationHydratesBoundTaskPromptAcrossPause(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Registered GitHub prompt parity")
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())

	maintainedPrompt := githubSDLCDevInboxPrompt
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
	request := AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/prompt-parity",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	}

	// Simulate a graph registered by an older build, which bound the right Task
	// but did not persist its behavior in the graph node configuration.
	_, err = db.ExecContext(ctx, `UPDATE automation_nodes SET config_json = '{}' WHERE version_id = ? AND node_key = 'dev_inbox'`, definition.Version.ID)
	require.NoError(t, err)
	reconciled, _, err := registration.Register(ctx, request)
	require.NoError(t, err)
	require.NotEqual(t, definition.Version.ID, reconciled.Version.ID, "registration must replace an old empty graph config even when resource IDs are unchanged")
	definition = reconciled

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

	require.NoError(t, automationRepo.SetAutomationLifecycle(ctx, project.ID, definition.Automation.ID, models.AutomationPaused))
	storedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, maintainedPrompt, storedTask.Prompt, "pausing must not alter the runtime Task prompt")
	reopened, err := NewAutomationDraftService(automationRepo, NewAutomationAdapterRegistry()).CurrentCandidate(ctx, project.ID, definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, maintainedPrompt, automationDraftNodeByKey(t, reopened.Candidate, "dev_inbox").Config["prompt"])
}

func TestMaintainedAutomationRegistrationReplacesCurrentGraphAndPreservesLifecycle(t *testing.T) {
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

			secondTask, secondSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Replacement maintained schedule")
			request.Resources = []models.AutomationResourceBinding{
				{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: secondSchedule.ID},
				{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: secondTask.ID},
			}
			replaced, _, err := registration.Register(ctx, request)
			require.NoError(t, err)
			require.Equal(t, test.state, replaced.Automation.LifecycleState)
			require.NotEqual(t, first.Version.ID, replaced.Version.ID)
			require.Equal(t, 1, tableCountWhere(t, db, "automation_versions", "automation_id", first.Automation.ID), "maintained replacement must retain exactly one current graph")
			require.Zero(t, tableCountWhere(t, db, "automation_versions", "id", first.Version.ID))
			require.Zero(t, tableCountWhere(t, db, "automation_invocations", "automation_id", first.Automation.ID), "prior graph runtime projection must be deleted")
			require.Zero(t, tableCountWhere(t, db, "schedules", "id", firstSchedule.ID), "obsolete exclusively owned schedules must be deleted")
			stored, err := scheduleRepo.GetByID(ctx, secondSchedule.ID)
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Equal(t, test.expectEnabled, stored.Enabled)
			owner, err := automationRepo.GetTriggerOwner(ctx, secondSchedule.ID)
			require.NoError(t, err)
			require.NotNil(t, owner, "inactive Automations must retain exclusive schedule provenance")
			require.Equal(t, test.ownershipState, owner.OwnershipState)
		})
	}
}

func TestMaintainedAutomationRegistrationPreservesIndividuallyDisabledSchedule(t *testing.T) {
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
	require.False(t, stored.Enabled)

	_, _, err = registration.Register(ctx, request)
	require.NoError(t, err)
	stored, err = scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.False(t, stored.Enabled)
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
	require.ErrorContains(t, err, "unsupported maintained automation adapter", "Vision Driver is publishable from explicit drafts but is not a maintained setup registration path")

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
