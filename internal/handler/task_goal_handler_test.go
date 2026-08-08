package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/lifecycle"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

func TestTaskGoalRoutes_HTMXEditPauseResumeClear(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Goal UI", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	if err := tc.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	rec := tc.HTMX().Post("/tasks/" + task.ID + "/goal").WithForm(url.Values{"goal": {"All checks pass"}}).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("set goal status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "All checks pass") || !strings.Contains(rec.Body.String(), `>active</span>`) {
		t.Fatalf("set goal body missing panel content: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Active: true") || strings.Contains(rec.Body.String(), "Active: false") {
		t.Fatalf("goal panel should use the status pill instead of redundant boolean active text: %s", rec.Body.String())
	}

	for _, path := range []string{"/pause", "/resume", "/clear"} {
		rec = tc.HTMX().Post("/tasks/" + task.ID + "/goal" + path).Execute()
		if rec.Code != http.StatusOK {
			t.Fatalf("post %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	goal, err := tc.handler.taskGoalSvc.GetGoal(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get goal: %v", err)
	}
	if goal.Status != models.TaskGoalStatusCleared {
		t.Fatalf("goal status = %s", goal.Status)
	}
}

func TestUpdateTask_EditFormSavesGoalAndRefreshesReadOnlySummary(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Edit Goal", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	if err := tc.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	form := url.Values{
		"title":              {task.Title},
		"category":           {string(task.Category)},
		"priority":           {"2"},
		"prompt":             {task.Prompt},
		"tag":                {""},
		"agent_id":           {""},
		"goal_present":       {"1"},
		"goal":               {"Ship a clearer details UX"},
		"auto_merge_present": {"1"},
	}
	rec := tc.HTMX().Put("/tasks/" + task.ID).WithForm(form).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("update task status=%d body=%s", rec.Code, rec.Body.String())
	}
	goal, err := tc.handler.taskGoalSvc.GetGoal(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get goal: %v", err)
	}
	if goal == nil || goal.Objective != "Ship a clearer details UX" || goal.Status != models.TaskGoalStatusActive {
		t.Fatalf("unexpected saved goal: %#v", goal)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Ship a clearer details UX") || !strings.Contains(body, `>active</span>`) {
		t.Fatalf("expected refreshed read-only goal summary, got: %s", body)
	}
	if strings.Contains(body, "Active: true") || strings.Contains(body, "Active: false") {
		t.Fatalf("read-only goal summary should use the status pill instead of redundant boolean active text: %s", body)
	}
	viewStart := strings.Index(body, `id="task-detail-view"`)
	editStart := strings.Index(body, `id="task-detail-edit"`)
	if viewStart == -1 || editStart == -1 || editStart <= viewStart {
		t.Fatal("expected task detail view before edit form")
	}
	viewOnly := body[viewStart:editStart]
	for _, forbidden := range []string{"Add goal", "Pause", "Resume", "Clear"} {
		if strings.Contains(viewOnly, forbidden) {
			t.Fatalf("read-only goal summary should not include %q", forbidden)
		}
	}
}

func TestTaskGoalPanelLabelsUserStoppedPause(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Stopped Goal UI", Category: models.CategoryBacklog, Status: models.StatusCancelled, Prompt: "prompt", Priority: 2}
	if err := tc.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "Keep going", service.GoalOptions{}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := tc.handler.taskGoalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID); err != nil {
		t.Fatalf("pause after user stop: %v", err)
	}

	rec := tc.HTMX().Get("/tasks/" + task.ID + "/goal").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("goal panel status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Paused (stopped by user)") {
		t.Fatalf("goal panel missing user-stopped paused label: %s", body)
	}
	if strings.Contains(body, ">paused</span>") {
		t.Fatalf("goal panel should not show bare paused status for user-stopped goal: %s", body)
	}
}

func TestTaskGoalContext_StatusToolGuidanceMatchesAgentGrants(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Goal prompt", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "prompt", Priority: 2}
	if err := tc.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tc.handler.taskGoalSvc.SetGoal(context.Background(), task.ID, "Keep tests green", service.GoalOptions{}); err != nil {
		t.Fatalf("set goal: %v", err)
	}

	ungranted := tc.handler.taskGoalContext(context.Background(), task.ID, &models.Agent{Tools: []string{"send_to_task"}})
	if !strings.Contains(ungranted, "protected Goal Agent") {
		t.Fatalf("ungranted guidance should defer status evaluation to Goal Agent, got:\n%s", ungranted)
	}
	if strings.Contains(ungranted, "This assigned agent is explicitly granted goal status tools") {
		t.Fatalf("ungranted guidance advertised status tools, got:\n%s", ungranted)
	}

	granted := tc.handler.taskGoalContext(context.Background(), task.ID, &models.Agent{Tools: []string{"mark_task_goal_achieved"}})
	if !strings.Contains(granted, "explicitly granted these goal status tools: mark_task_goal_achieved") {
		t.Fatalf("granted guidance missing exact status-tool instruction, got:\n%s", granted)
	}
	if strings.Contains(granted, "report_task_goal_blocked") {
		t.Fatalf("single-tool guidance advertised ungranted blocker tool, got:\n%s", granted)
	}
	if !strings.Contains(granted, "goal_id") || !strings.Contains(granted, "still active") {
		t.Fatalf("granted guidance missing stale guard instruction, got:\n%s", granted)
	}
	if strings.Contains(granted, "handled by the protected Goal Agent") {
		t.Fatalf("granted guidance still says protected Goal Agent handles status, got:\n%s", granted)
	}
}

func TestGoalAgentSendToTaskSkipsContinuationWhenGoalPausedByUserStop(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Paused goal continuation", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "prompt", Priority: 2, AgentID: &agent.ID}
	if err := tc.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "Keep going", service.GoalOptions{}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := tc.handler.taskGoalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID); err != nil {
		t.Fatalf("pause after user stop: %v", err)
	}

	params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true, AgentDefinition: &models.Agent{Tools: []string{"send_to_task"}}}
	handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, "web")
	goalCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "goal-hook", SystemKind: models.AgentSystemKindGoal})
	out, err := handlers["send_to_task"](goalCtx, []byte(`{"task_id":"current","message":"Continue from stale evaluator"}`))
	if err == nil || !strings.Contains(err.Error(), "task goal is not active") {
		t.Fatalf("expected paused goal continuation to be rejected, out=%q err=%v", out, err)
	}
	pending, err := tc.handler.threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("paused goal continuation queued pending inputs: %+v", pending)
	}
}

func TestGoalAgentSendToTaskQueuesWhenGoalStillActive(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Active goal continuation", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "prompt", Priority: 2, AgentID: &agent.ID}
	if err := tc.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "Keep going", service.GoalOptions{}); err != nil {
		t.Fatalf("set goal: %v", err)
	}

	params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true, AgentDefinition: &models.Agent{Tools: []string{"send_to_task"}}}
	handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, "web")
	goalCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "goal-hook", SystemKind: models.AgentSystemKindGoal})
	out, err := handlers["send_to_task"](goalCtx, []byte(`{"task_id":"current","message":"Continue because goal remains unmet"}`))
	if err != nil {
		t.Fatalf("active goal continuation rejected: out=%q err=%v", out, err)
	}
	var result struct {
		QueuedMessageID string `json:"queued_message_id"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode send_to_task result %q: %v", out, err)
	}
	queued, err := tc.handler.threadInputRepo.GetByID(ctx, result.QueuedMessageID)
	if err != nil {
		t.Fatalf("load queued continuation: %v", err)
	}
	if queued == nil {
		t.Fatalf("queued continuation %q was not persisted", result.QueuedMessageID)
	}
	if queued.Content != "Continue because goal remains unmet" || queued.Source != models.TaskOriginSystemAgent || queued.OriginAgent != models.AgentSystemKindGoal {
		t.Fatalf("queued continuation details = %+v", queued)
	}
}

func TestGoalAgentSendToTaskQueuesCurrentRunWhenOlderAfterCompleteRowsAreLate(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Current goal continuation", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "prompt", Priority: 2, AgentID: &agent.ID}
	if err := tc.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "Audit-only completion required", service.GoalOptions{}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	tc.handler.lifecycleRepo = repository.NewLifecycleRepo(tc.db)
	lifecycleAgentRepo := repository.NewAgentRepo(tc.db)
	goalAgent := &models.Agent{Name: "Goal Agent", Model: "inherit", Enabled: true, SystemKind: models.AgentSystemKindGoal}
	if err := lifecycleAgentRepo.Create(ctx, goalAgent); err != nil {
		t.Fatalf("create goal agent: %v", err)
	}
	createLifecycleExec := func(runID string, when models.LifecycleWhen, skill string) {
		t.Helper()
		exec := &models.LifecycleExecution{TaskID: task.ID, TaskRunID: runID, AgentID: goalAgent.ID, When: when, SkillKey: skill, OutputContract: models.OutputContractActivitySummary, Status: models.LifecycleExecCompleted}
		if err := tc.handler.lifecycleRepo.CreateExecution(ctx, exec); err != nil {
			t.Fatalf("create lifecycle execution %s/%s: %v", runID, skill, err)
		}
	}
	createLifecycleExec("run-old", models.LifecycleRouteTask, "route_task")
	createLifecycleExec("run-current", models.LifecycleRouteTask, "route_task")
	createLifecycleExec("run-old", models.LifecycleAfterComplete, "observe_task_for_learning")

	params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true, AgentDefinition: &models.Agent{Tools: []string{"send_to_task"}}}
	handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, "web")
	goalCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: goalAgent.ID, SystemKind: models.AgentSystemKindGoal, TaskID: task.ID, TaskRunID: "run-current"})
	out, err := handlers["send_to_task"](goalCtx, []byte(`{"task_id":"current","message":"Continue because audit found a material issue"}`))
	if err != nil {
		t.Fatalf("current goal continuation rejected: out=%q err=%v", out, err)
	}
	var result struct {
		QueuedMessageID string `json:"queued_message_id"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode send_to_task result %q: %v", out, err)
	}
	queued, err := tc.handler.threadInputRepo.GetByID(ctx, result.QueuedMessageID)
	if err != nil {
		t.Fatalf("load queued continuation: %v", err)
	}
	if queued == nil {
		t.Fatalf("queued continuation %q was not persisted", result.QueuedMessageID)
	}
	if queued.Content != "Continue because audit found a material issue" || queued.Source != models.TaskOriginSystemAgent || queued.OriginAgent != models.AgentSystemKindGoal {
		t.Fatalf("queued continuation details = %+v", queued)
	}
}

func TestLifecycleSendToTaskRejectsStaleTaskRunContinuation(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Stale continuation", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "prompt", Priority: 2, AgentID: &agent.ID}
	if err := tc.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	tc.handler.lifecycleRepo = repository.NewLifecycleRepo(tc.db)
	lifecycleAgentRepo := repository.NewAgentRepo(tc.db)
	lifecycleAgent := &models.Agent{Name: "Lifecycle Agent", Model: "inherit", Enabled: true}
	if err := lifecycleAgentRepo.Create(ctx, lifecycleAgent); err != nil {
		t.Fatalf("create lifecycle agent: %v", err)
	}
	older := &models.LifecycleExecution{TaskID: task.ID, TaskRunID: "run-old", AgentID: lifecycleAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "manage_dynamic_loop", OutputContract: models.OutputContractActivitySummary, Status: models.LifecycleExecRunning}
	if err := tc.handler.lifecycleRepo.CreateExecution(ctx, older); err != nil {
		t.Fatalf("create older lifecycle execution: %v", err)
	}
	newer := &models.LifecycleExecution{TaskID: task.ID, TaskRunID: "run-new", AgentID: lifecycleAgent.ID, When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Status: models.LifecycleExecRunning}
	if err := tc.handler.lifecycleRepo.CreateExecution(ctx, newer); err != nil {
		t.Fatalf("create newer lifecycle execution: %v", err)
	}

	params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true, AgentDefinition: &models.Agent{Tools: []string{"send_to_task"}}}
	handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, "web")
	staleCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "hook-agent", Tools: []string{"send_to_task"}, TaskID: task.ID, TaskRunID: "run-old"})
	out, err := handlers["send_to_task"](staleCtx, []byte(`{"task_id":"current","message":"Duplicate continuation"}`))
	if err == nil || !strings.Contains(err.Error(), "stale lifecycle task run") {
		t.Fatalf("expected stale lifecycle continuation to be rejected, out=%q err=%v", out, err)
	}
	if !strings.Contains(err.Error(), "source_task=") || !strings.Contains(err.Error(), "source_run=run-old") || !strings.Contains(err.Error(), "latest_run=run-new") {
		t.Fatalf("stale lifecycle rejection missing diagnostic comparison details: %v", err)
	}
	pending, err := tc.handler.threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("stale lifecycle continuation queued pending inputs: %+v", pending)
	}
}

func TestLifecycleSendToTaskAllowsCrossTaskWhenDestinationHasNewerRuns(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	sourceTask := &models.Task{ProjectID: project.ID, Title: "Source lifecycle task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "source", Priority: 2, AgentID: &agent.ID}
	if err := tc.taskRepo.Create(ctx, sourceTask); err != nil {
		t.Fatalf("create source task: %v", err)
	}
	destinationTask := &models.Task{ProjectID: project.ID, Title: "Destination task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "destination", Priority: 2, AgentID: &agent.ID}
	if err := tc.taskRepo.Create(ctx, destinationTask); err != nil {
		t.Fatalf("create destination task: %v", err)
	}
	tc.handler.lifecycleRepo = repository.NewLifecycleRepo(tc.db)
	lifecycleAgentRepo := repository.NewAgentRepo(tc.db)
	lifecycleAgent := &models.Agent{Name: "Cross Task Lifecycle Agent", Model: "inherit", Enabled: true}
	if err := lifecycleAgentRepo.Create(ctx, lifecycleAgent); err != nil {
		t.Fatalf("create lifecycle agent: %v", err)
	}
	sourceRun := &models.LifecycleExecution{TaskID: sourceTask.ID, TaskRunID: "source-run-current", AgentID: lifecycleAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "coordinate", OutputContract: models.OutputContractActivitySummary, Status: models.LifecycleExecRunning}
	if err := tc.handler.lifecycleRepo.CreateExecution(ctx, sourceRun); err != nil {
		t.Fatalf("create source lifecycle execution: %v", err)
	}
	destinationOlder := &models.LifecycleExecution{TaskID: destinationTask.ID, TaskRunID: "destination-run-old", AgentID: lifecycleAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "observe", OutputContract: models.OutputContractActivitySummary, Status: models.LifecycleExecCompleted}
	if err := tc.handler.lifecycleRepo.CreateExecution(ctx, destinationOlder); err != nil {
		t.Fatalf("create destination older lifecycle execution: %v", err)
	}
	destinationNewer := &models.LifecycleExecution{TaskID: destinationTask.ID, TaskRunID: "destination-run-new", AgentID: lifecycleAgent.ID, When: models.LifecycleRouteTask, SkillKey: "route", OutputContract: models.OutputContractSelectedSkills, Status: models.LifecycleExecRunning}
	if err := tc.handler.lifecycleRepo.CreateExecution(ctx, destinationNewer); err != nil {
		t.Fatalf("create destination newer lifecycle execution: %v", err)
	}

	params := streamingResponseParams{TaskID: sourceTask.ID, ProjectID: project.ID, IsTaskFollowup: true, AgentDefinition: &models.Agent{Tools: []string{"send_to_task"}}}
	handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, "web")
	hookCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "hook-agent", Tools: []string{"send_to_task"}, TaskID: sourceTask.ID, TaskRunID: "source-run-current"})
	out, err := handlers["send_to_task"](hookCtx, []byte(`{"task_id":"`+destinationTask.ID+`","message":"Coordinate with destination"}`))
	if err != nil {
		t.Fatalf("cross-task lifecycle send_to_task rejected: out=%q err=%v", out, err)
	}
	pending, err := tc.handler.threadInputRepo.ListPendingForTask(ctx, destinationTask.ID)
	if err != nil {
		t.Fatalf("list destination pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Content != "Coordinate with destination" {
		t.Fatalf("destination pending inputs = %+v", pending)
	}
	sourcePending, err := tc.handler.threadInputRepo.ListPendingForTask(ctx, sourceTask.ID)
	if err != nil {
		t.Fatalf("list source pending: %v", err)
	}
	if len(sourcePending) != 0 {
		t.Fatalf("cross-task send queued on source task: %+v", sourcePending)
	}
}

func TestGoalAgentSendToTaskRejectsNonActiveGoalStates(t *testing.T) {
	for _, status := range []models.TaskGoalStatus{models.TaskGoalStatusAchieved, models.TaskGoalStatusPaused, models.TaskGoalStatusBlocked, models.TaskGoalStatusCleared, models.TaskGoalStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			project := tc.CreateProject().Build()
			agent := tc.CreateLLMConfig().Build()
			task := &models.Task{ProjectID: project.ID, Title: "Protected goal " + string(status), Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "prompt", Priority: 2, AgentID: &agent.ID}
			if err := tc.taskRepo.Create(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}
			goal, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "Keep protected", service.GoalOptions{})
			if err != nil {
				t.Fatalf("set goal: %v", err)
			}
			switch status {
			case models.TaskGoalStatusAchieved:
				if _, err := tc.handler.taskGoalSvc.MarkAchieved(ctx, task.ID, goal.GoalID, "done"); err != nil {
					t.Fatalf("mark achieved: %v", err)
				}
			case models.TaskGoalStatusPaused:
				if err := tc.handler.taskGoalSvc.PauseGoal(ctx, task.ID, "test"); err != nil {
					t.Fatalf("pause goal: %v", err)
				}
			case models.TaskGoalStatusBlocked:
				for i := 0; i < 3; i++ {
					if _, err := tc.handler.taskGoalSvc.RecordBlockedReport(ctx, task.ID, goal.GoalID, "material_issue", "needs edits"); err != nil {
						t.Fatalf("record blocked report %d: %v", i, err)
					}
				}
			case models.TaskGoalStatusCleared:
				if err := tc.handler.taskGoalSvc.ClearGoal(ctx, task.ID, "test"); err != nil {
					t.Fatalf("clear goal: %v", err)
				}
			case models.TaskGoalStatusFailed:
				if _, err := repository.NewTaskGoalRepo(tc.db).UpdateStatus(ctx, task.ID, goal.GoalID, models.TaskGoalStatusFailed, "failed", false); err != nil {
					t.Fatalf("mark failed: %v", err)
				}
			}

			params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true, AgentDefinition: &models.Agent{Tools: []string{"send_to_task"}}}
			handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, "web")
			goalCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "goal-hook", SystemKind: models.AgentSystemKindGoal, TaskID: task.ID, TaskRunID: "run-current"})
			out, err := handlers["send_to_task"](goalCtx, []byte(`{"task_id":"current","message":"Do not restart protected goal"}`))
			if err == nil || !strings.Contains(err.Error(), "task goal is not active") {
				t.Fatalf("expected %s goal continuation to be rejected, out=%q err=%v", status, out, err)
			}
			pending, err := tc.handler.threadInputRepo.ListPendingForTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("list pending: %v", err)
			}
			if len(pending) != 0 {
				t.Fatalf("%s goal continuation queued pending inputs: %+v", status, pending)
			}
		})
	}
}

func TestTaskGoalRuntimeToolsRejectForeignRawTaskID(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	projectA := tc.CreateProject().WithName("Runtime Project A").Build()
	projectB := tc.CreateProject().WithName("Runtime Project B").Build()
	sharedTitle := "Shared Runtime Goal Task"
	localTask := &models.Task{ProjectID: projectA.ID, Title: sharedTitle, Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "local prompt", Priority: 2}
	if err := tc.taskRepo.Create(ctx, localTask); err != nil {
		t.Fatalf("create local task: %v", err)
	}
	foreignTask := &models.Task{ProjectID: projectB.ID, Title: sharedTitle, Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "foreign prompt", Priority: 2}
	if err := tc.taskRepo.Create(ctx, foreignTask); err != nil {
		t.Fatalf("create foreign task: %v", err)
	}
	foreignGoal, err := tc.handler.taskGoalSvc.SetGoal(ctx, foreignTask.ID, "Foreign objective", service.GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set foreign goal: %v", err)
	}

	params := streamingResponseParams{ProjectID: projectA.ID, AgentDefinition: &models.Agent{Tools: []string{"mark_task_goal_achieved", "report_task_goal_blocked"}}}
	handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	expectForeignTaskRejected := func(name, payload string) {
		t.Helper()
		out, err := handlers[name](ctx, []byte(payload))
		if err == nil || !strings.Contains(err.Error(), "belongs to a different project") {
			t.Fatalf("expected %s to reject foreign task id, out=%q err=%v", name, out, err)
		}
	}

	expectForeignTaskRejected("set_task_goal", `{"task_id":"`+foreignTask.ID+`","goal":"changed from Project A"}`)
	goal, err := tc.handler.taskGoalSvc.GetGoal(ctx, foreignTask.ID)
	if err != nil {
		t.Fatalf("get foreign goal after rejected set: %v", err)
	}
	if goal == nil || goal.Objective != "Foreign objective" || goal.Status != models.TaskGoalStatusActive {
		t.Fatalf("foreign goal mutated by rejected set: %#v", goal)
	}

	out, err := handlers["get_task_goal"](ctx, []byte(`{"task_id":"`+foreignTask.ID+`"}`))
	if err == nil || !strings.Contains(err.Error(), "belongs to a different project") {
		t.Fatalf("expected get_task_goal to reject foreign task id, out=%q err=%v", out, err)
	}
	if strings.Contains(out, "Foreign objective") {
		t.Fatalf("get_task_goal leaked foreign goal output: %s", out)
	}

	expectForeignTaskRejected("pause_task_goal", `{"task_id":"`+foreignTask.ID+`"}`)
	goal, err = tc.handler.taskGoalSvc.GetGoal(ctx, foreignTask.ID)
	if err != nil {
		t.Fatalf("get foreign goal after rejected pause: %v", err)
	}
	if goal == nil || goal.Status != models.TaskGoalStatusActive {
		t.Fatalf("foreign goal status mutated by rejected pause: %#v", goal)
	}

	expectForeignTaskRejected("send_to_task", `{"task_id":"`+foreignTask.ID+`","message":"queued from Project A"}`)
	pending, err := tc.handler.threadInputRepo.ListPendingForTask(ctx, foreignTask.ID)
	if err != nil {
		t.Fatalf("list foreign pending inputs: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("send_to_task queued foreign pending inputs: %+v", pending)
	}

	out, err = handlers["set_task_goal"](ctx, []byte(`{"title":"`+sharedTitle+`","goal":"Local objective"}`))
	if err != nil {
		t.Fatalf("set_task_goal by shared title should resolve current project task: out=%q err=%v", out, err)
	}
	localGoal, err := tc.handler.taskGoalSvc.GetGoal(ctx, localTask.ID)
	if err != nil {
		t.Fatalf("get local title goal: %v", err)
	}
	if localGoal == nil || localGoal.Objective != "Local objective" {
		t.Fatalf("title resolution did not set local task goal: %#v", localGoal)
	}
	goal, err = tc.handler.taskGoalSvc.GetGoal(ctx, foreignTask.ID)
	if err != nil {
		t.Fatalf("get foreign goal after title set: %v", err)
	}
	if goal == nil || goal.GoalID != foreignGoal.GoalID || goal.Objective != "Foreign objective" || goal.Status != models.TaskGoalStatusActive {
		t.Fatalf("title resolution touched foreign goal: %#v", goal)
	}

	out, err = handlers["set_task_goal"](ctx, []byte(`{"task_id":"current","goal":"should fail outside follow-up"}`))
	if err == nil || !strings.Contains(err.Error(), "only valid in a persisted task thread") {
		t.Fatalf("expected current alias outside follow-up to reject, out=%q err=%v", out, err)
	}
}

func TestTaskGoalTools_CurrentAliasAndSendToTaskQueuesOnly(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := &models.Task{ProjectID: project.ID, Title: "Goal tools", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "prompt", Priority: 2, AgentID: &agent.ID}
	if err := tc.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true}
	handlers := tc.handler.chatActionHandlers(params, nil, models.ChatModeOrchestrate, "web")

	out, err := handlers["set_task_goal"](context.Background(), []byte(`{"task_id":"current","goal":"Ship complete"}`))
	if err != nil {
		t.Fatalf("set current goal: %v", err)
	}
	if !strings.Contains(out, "Ship complete") {
		t.Fatalf("set output = %s", out)
	}
	goal, err := tc.handler.taskGoalSvc.GetGoal(context.Background(), task.ID)
	if err != nil || goal == nil {
		t.Fatalf("get goal after set: %v %#v", err, goal)
	}
	if _, err := handlers["mark_task_goal_achieved"](context.Background(), []byte(`{"task_id":"current","goal_id":"`+goal.GoalID+`","reason":"done"}`)); err == nil || !strings.Contains(err.Error(), "explicit agent tool grant") {
		t.Fatalf("ungranted assistant marked goal achieved, err=%v", err)
	}
	grantParams := params
	grantParams.AgentDefinition = &models.Agent{Tools: []string{"mark_task_goal_achieved"}}
	grantHandlers := tc.handler.chatActionHandlers(grantParams, nil, models.ChatModeOrchestrate, "web")
	out, err = grantHandlers["mark_task_goal_achieved"](context.Background(), []byte(`{"task_id":"current","goal_id":"`+goal.GoalID+`","reason":"done by granted agent"}`))
	if err != nil {
		t.Fatalf("granted assistant mark achieved: %v", err)
	}
	if !strings.Contains(out, string(models.TaskGoalStatusAchieved)) {
		t.Fatalf("granted assistant achieved output = %s", out)
	}
	goal, err = tc.handler.taskGoalSvc.ReactivateAchievedGoal(context.Background(), task.ID, "test")
	if err != nil {
		t.Fatalf("reactivate goal after granted mark: %v", err)
	}
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := tc.execRepo.Create(context.Background(), active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	out, err = handlers["send_to_task"](context.Background(), []byte(`{"message":"Continue","origin":"web","origin_agent":"assistant"}`))
	if err != nil {
		t.Fatalf("send to task with implicit current task: %v", err)
	}
	if !strings.Contains(out, "queued_message_id") {
		t.Fatalf("send output = %s", out)
	}
	out, err = handlers["send_to_task"](context.Background(), []byte(`{"message":"Spoofed goal continuation","origin":"system_agent","origin_agent":"goal"}`))
	if err != nil {
		t.Fatalf("send to task with spoofed lineage: %v", err)
	}
	if !strings.Contains(out, "queued_message_id") {
		t.Fatalf("spoofed send output = %s", out)
	}

	goalParams := params
	goalParams.RuntimeOrigin = models.TaskOriginSystemAgent
	goalParams.RuntimeOriginAgent = models.AgentSystemKindGoal
	goalHandlers := tc.handler.chatActionHandlers(goalParams, nil, models.ChatModeOrchestrate, "web")
	if _, err := goalHandlers["mark_task_goal_achieved"](context.Background(), []byte(`{"task_id":"current","goal_id":"`+goal.GoalID+`","reason":"forged"}`)); err == nil || !strings.Contains(err.Error(), "explicit agent tool grant") {
		t.Fatalf("runtime-origin goal params should not grant status authority, err=%v", err)
	}
	goalHookCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "goal-hook", SystemKind: models.AgentSystemKindGoal})
	out, err = goalHandlers["mark_task_goal_achieved"](goalHookCtx, []byte(`{"task_id":"current","goal_id":"`+goal.GoalID+`","reason":"verified"}`))
	if err != nil {
		t.Fatalf("goal lifecycle hook mark achieved: %v", err)
	}
	if !strings.Contains(out, string(models.TaskGoalStatusAchieved)) {
		t.Fatalf("goal lifecycle hook achieved output = %s", out)
	}
	goal, err = tc.handler.taskGoalSvc.ReactivateAchievedGoal(context.Background(), task.ID, "test")
	if err != nil {
		t.Fatalf("reactivate goal before protected goal continuation: %v", err)
	}
	out, err = goalHandlers["send_to_task"](context.Background(), []byte(`{"message":"Goal continuation","origin":"web","origin_agent":"assistant"}`))
	if err != nil {
		t.Fatalf("goal send to task: %v", err)
	}
	if !strings.Contains(out, "queued_message_id") {
		t.Fatalf("goal send output = %s", out)
	}

	lifecycleParams := params
	lifecycleParams.AgentDefinition = &models.Agent{Tools: []string{"send_to_task"}}
	lifecycleHandlers := tc.handler.chatActionHandlers(lifecycleParams, nil, models.ChatModeOrchestrate, "web")
	lifecycleCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "custom-hook", Tools: []string{"mark_task_goal_achieved"}})
	out, err = lifecycleHandlers["mark_task_goal_achieved"](lifecycleCtx, []byte(`{"task_id":"current","goal_id":"`+goal.GoalID+`","reason":"done by lifecycle grant"}`))
	if err != nil {
		t.Fatalf("granted lifecycle hook mark achieved: %v", err)
	}
	if !strings.Contains(out, string(models.TaskGoalStatusAchieved)) {
		t.Fatalf("granted lifecycle hook achieved output = %s", out)
	}
	goal, err = tc.handler.taskGoalSvc.ReactivateAchievedGoal(context.Background(), task.ID, "test")
	if err != nil {
		t.Fatalf("reactivate goal after lifecycle mark: %v", err)
	}
	ungrantedLifecycleCtx := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "custom-hook", Tools: []string{"send_to_task"}})
	if _, err := lifecycleHandlers["mark_task_goal_achieved"](ungrantedLifecycleCtx, []byte(`{"task_id":"current","goal_id":"`+goal.GoalID+`","reason":"denied"}`)); err == nil || !strings.Contains(err.Error(), "explicit agent tool grant") {
		t.Fatalf("ungranted lifecycle hook marked goal achieved, err=%v", err)
	}

	out, err = lifecycleHandlers["send_to_task"](lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "goal-hook", SystemKind: models.AgentSystemKindGoal}), []byte(`{"message":"Lifecycle goal continuation","origin":"web","origin_agent":"assistant"}`))
	if err != nil {
		t.Fatalf("goal lifecycle send to task: %v", err)
	}
	if !strings.Contains(out, "queued_message_id") {
		t.Fatalf("goal lifecycle send output = %s", out)
	}

	pending, err := tc.handler.threadInputRepo.ListPendingForTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 4 || pending[0].Content != "Continue" || pending[1].Content != "Spoofed goal continuation" || pending[2].Content != "Goal continuation" || pending[3].Content != "Lifecycle goal continuation" {
		t.Fatalf("pending inputs = %+v", pending)
	}
	if pending[0].Source != models.TaskOriginWeb || pending[0].OriginAgent != "" {
		t.Fatalf("normal send_to_task lineage = source:%q origin_agent:%q", pending[0].Source, pending[0].OriginAgent)
	}
	if pending[1].Source != models.TaskOriginWeb || pending[1].OriginAgent != "" {
		t.Fatalf("spoofed send_to_task lineage = source:%q origin_agent:%q", pending[1].Source, pending[1].OriginAgent)
	}
	if pending[2].Source != models.TaskOriginSystemAgent {
		t.Fatalf("goal send_to_task source = %q", pending[2].Source)
	}
	if pending[2].OriginAgent != models.AgentSystemKindGoal {
		t.Fatalf("goal send_to_task origin_agent = %q", pending[2].OriginAgent)
	}
	if pending[3].Source != models.TaskOriginSystemAgent || pending[3].OriginAgent != models.AgentSystemKindGoal {
		t.Fatalf("goal lifecycle send_to_task lineage = source:%q origin_agent:%q", pending[3].Source, pending[3].OriginAgent)
	}
	execs, err := tc.execRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list execs: %v", err)
	}
	if len(execs) != 1 || execs[0].ID != active.ID {
		t.Fatalf("send_to_task created inline execution: %+v", execs)
	}
}
