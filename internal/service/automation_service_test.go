package service

import (
	"context"
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
	_, githubSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "GitHub Trigger")
	foreignTask, foreignSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, other.ID, "Foreign")

	cards, err := graph.List(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, cards, "ordinary tasks and schedules must not be inferred as automations")

	nativeReq := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/default", Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: nativeSchedule.ID},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: sharedTask.ID},
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

	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC,
		StableKey: nativeReq.StableKey, Resources: []models.AutomationResourceBinding{
			{NodeKey: "inbox_trigger", ResourceType: "schedule", ResourceID: githubSchedule.ID},
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: sharedTask.ID, Relation: "shared"},
		}})
	require.ErrorContains(t, err, "adapter cannot change")

	githubReq := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/default", Resources: []models.AutomationResourceBinding{
		{NodeKey: "inbox_trigger", ResourceType: "schedule", ResourceID: githubSchedule.ID},
		{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: sharedTask.ID, Relation: "shared"},
	}}
	github, reused, err := service.Register(ctx, githubReq)
	require.NoError(t, err)
	require.False(t, reused)
	require.NotEqual(t, native.Automation.ID, github.Automation.ID)

	cards, err = graph.List(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, cards, 2)

	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: other.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/foreign", Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: foreignSchedule.ID},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: sharedTask.ID},
	}})
	require.ErrorContains(t, err, "another project")

	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/foreign-task", Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: nativeSchedule.ID},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: foreignTask.ID},
	}})
	require.ErrorContains(t, err, "another project")

	foreignView, err := automationRepo.GetDefinition(ctx, other.ID, native.Automation.ID)
	require.NoError(t, err)
	require.Nil(t, foreignView)
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
		{NodeKey: "vision_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "vision_driver", ResourceType: "task", ResourceID: task.ID},
	}})
	require.ErrorContains(t, err, "unsupported maintained automation adapter", "Vision Driver is publishable from explicit drafts but is not a maintained setup registration path")

	_, _, err = service.Register(ctx, AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/shared-trigger", Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "shared"},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: task.ID},
	}})
	require.ErrorContains(t, err, "exclusive owned relation")

	base := AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: AutomationAdapterNativeSDLC, Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: task.ID},
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
	input := fmt.Sprintf(`{"adapter_key":"native_sdlc","stable_key":"native-sdlc/default","resources":[{"node_key":"suggestion_trigger","resource_type":"schedule","resource_id":%q},{"node_key":"suggestion_producer","resource_type":"task","resource_id":%q}]}`, schedule.ID, producer.ID)
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
