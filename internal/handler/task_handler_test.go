package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHandler_GetTaskStatusCountsUsesOnlyCompactProjectPredicates(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Task Status Count Project").Build()
	foreign := tc.CreateProject().WithName("Foreign Task Status Count Project").Build()
	empty := tc.CreateProject().WithName("Empty Task Status Count Project").Build()

	tc.CreateTask(project.ID).WithTitle("Active pending").WithCategory(models.CategoryActive).WithStatus(models.StatusPending).Build()
	tc.CreateTask(project.ID).WithTitle("Active running").WithCategory(models.CategoryActive).WithStatus(models.StatusRunning).Build()
	tc.CreateTask(project.ID).WithTitle("Active queued").WithCategory(models.CategoryActive).WithStatus(models.StatusQueued).Build()
	tc.CreateTask(project.ID).WithTitle("Backlog queued").WithCategory(models.CategoryBacklog).WithStatus(models.StatusQueued).Build()
	tc.CreateTask(project.ID).WithTitle("Completed task").WithCategory(models.CategoryCompleted).WithStatus(models.StatusCompleted).Build()
	tc.CreateTask(project.ID).WithTitle("Chat queued").WithCategory(models.CategoryChat).WithStatus(models.StatusQueued).Build()
	tc.CreateTask(project.ID).WithTitle("Scheduled queued").WithCategory(models.CategoryScheduled).WithStatus(models.StatusQueued).Build()
	scheduledAutomationTask := &models.Task{
		ProjectID: project.ID, Title: "Scheduled capacity queued", Prompt: "scheduled automation",
		Category: models.CategoryScheduled, Status: models.StatusQueued,
	}
	require.NoError(t, tc.taskRepo.Create(context.Background(), scheduledAutomationTask))
	scheduledRunAt := time.Now().UTC().Add(time.Hour)
	scheduled := &models.Schedule{
		TaskID: scheduledAutomationTask.ID, RunAt: scheduledRunAt, NextRun: &scheduledRunAt,
		RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true,
	}
	require.NoError(t, tc.scheduleRepo.Create(context.Background(), scheduled))
	ctx := context.Background()
	reservationStatements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO automations (id, project_id, stable_key, name) VALUES ('status-count-auto', ?, 'status-count-auto', 'Status count automation')`, []any{project.ID}},
		{`INSERT INTO automation_versions (id, project_id, automation_id, version, adapter_key) VALUES ('status-count-version', ?, 'status-count-auto', 1, 'test')`, []any{project.ID}},
		{`INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role) VALUES ('status-count-node', ?, 'status-count-auto', 'status-count-version', 'trigger', 'Trigger', 'trigger', 'trigger')`, []any{project.ID}},
		{`INSERT INTO automation_invocations (id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key) VALUES ('status-count-invocation', ?, 'status-count-auto', 'status-count-version', 'status-count-node', 'schedule', ?, 'status-count-occurrence')`, []any{project.ID, scheduled.ID}},
		{`INSERT INTO automation_dispatch_outbox (id, invocation_id, task_id) VALUES ('status-count-dispatch', 'status-count-invocation', ?)`, []any{scheduledAutomationTask.ID}},
		{`INSERT INTO automation_task_run_reservations (task_id, dispatch_id, project_id) VALUES (?, 'status-count-dispatch', ?)`, []any{scheduledAutomationTask.ID, project.ID}},
	}
	for _, statement := range reservationStatements {
		_, err := tc.db.ExecContext(ctx, statement.query, statement.args...)
		require.NoError(t, err)
	}
	tc.CreateTask(project.ID).WithTitle("Active failed").WithCategory(models.CategoryActive).WithStatus(models.StatusFailed).Build()
	tc.CreateTask(project.ID).WithTitle("Active cancelled").WithCategory(models.CategoryActive).WithStatus(models.StatusCancelled).Build()

	parentID := "status-count-swarm-parent"
	parent := &models.Task{
		ID: parentID, ProjectID: project.ID, Title: "Swarm parent", Prompt: "parent",
		Category: models.CategoryCompleted, Status: models.StatusCompleted, SwarmRole: models.SwarmRoleParent,
	}
	require.NoError(t, tc.handler.taskRepo.Create(ctx, parent))
	childParentID := parent.ID
	require.NoError(t, tc.handler.taskRepo.Create(ctx, &models.Task{
		ID: "status-count-swarm-worker", ProjectID: project.ID, Title: "Swarm worker", Prompt: "worker",
		Category: models.CategoryActive, Status: models.StatusQueued, SwarmRole: models.SwarmRoleWorker,
		ParentTaskID: &childParentID,
	}))

	tc.CreateTask(foreign.ID).WithTitle("Foreign queued").WithCategory(models.CategoryActive).WithStatus(models.StatusQueued).Build()

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/status-counts?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	require.NoError(t, tc.handler.GetTaskStatusCounts(tc.echo.NewContext(req, rec)))
	require.Equal(t, http.StatusOK, rec.Code)
	var response TaskStatusCountsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Equal(t, TaskStatusCountsResponse{ActiveTasks: 3, QueuedTasks: 3}, response)

	emptyReq := httptest.NewRequest(http.MethodGet, "/api/tasks/status-counts?project_id="+empty.ID, nil)
	emptyRec := httptest.NewRecorder()
	require.NoError(t, tc.handler.GetTaskStatusCounts(tc.echo.NewContext(emptyReq, emptyRec)))
	var emptyResponse TaskStatusCountsResponse
	require.NoError(t, json.NewDecoder(emptyRec.Body).Decode(&emptyResponse))
	require.Equal(t, TaskStatusCountsResponse{}, emptyResponse)

	badReq := httptest.NewRequest(http.MethodGet, "/api/tasks/status-counts", nil)
	badRec := httptest.NewRecorder()
	badErr := tc.handler.GetTaskStatusCounts(tc.echo.NewContext(badReq, badRec))
	var httpErr *echo.HTTPError
	require.ErrorAs(t, badErr, &httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestHandler_GetTaskReferenceCatalog(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Task Reference Catalog Project").Build()
	foreign := tc.CreateProject().WithName("Foreign Task Reference Catalog Project").Build()
	empty := tc.CreateProject().WithName("Empty Task Reference Catalog Project").Build()
	model := tc.CreateLLMConfig().WithName("Catalog Model").Build()

	catalogTask := &models.Task{
		ProjectID: project.ID, Title: "Catalog metadata", Category: models.CategoryActive, Status: models.StatusPending,
		Priority: 4, Tag: models.TagBug, Prompt: strings.Repeat("p", 400), ChainConfig: `{"enabled":true}`,
	}
	require.NoError(t, tc.taskRepo.Create(ctx, catalogTask))
	require.NoError(t, repository.NewTaskGoalRepo(tc.db).CreateOrReplace(ctx, &models.TaskGoal{
		TaskID: catalogTask.ID, GoalID: "catalog-goal", Objective: "finish catalog", Status: models.TaskGoalStatusActive,
	}))
	for i := 0; i < 25; i++ {
		tc.CreateTask(project.ID).WithTitle(fmt.Sprintf("Catalog task %02d", i)).Build()
	}
	tc.CreateTask(foreign.ID).WithTitle("Foreign task").Build()
	tc.CreateTask(project.ID).WithTitle("Scheduled task").WithCategory(models.CategoryScheduled).Build()
	tc.CreateTask(project.ID).WithTitle("Scheduled running task").WithCategory(models.CategoryScheduled).WithStatus(models.StatusRunning).Build()
	tc.CreateTask(project.ID).WithTitle("Chat task").WithCategory(models.CategoryChat).Build()

	parentID := "catalog-swarm-parent"
	parent := &models.Task{
		ID: parentID, ProjectID: project.ID, Title: "Catalog swarm parent", Prompt: "parent",
		Category: models.CategoryActive, Status: models.StatusPending, SwarmRole: models.SwarmRoleParent,
	}
	require.NoError(t, tc.taskRepo.Create(ctx, parent))
	childParentID := parent.ID
	require.NoError(t, tc.taskRepo.Create(ctx, &models.Task{
		ID: "catalog-swarm-child", ProjectID: project.ID, Title: "Catalog swarm child", Prompt: "child",
		Category: models.CategoryActive, Status: models.StatusPending, SwarmRole: models.SwarmRoleWorker,
		ParentTaskID: &childParentID,
	}))

	rec := tc.HTTP().Get("/api/tasks/reference-catalog?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, rec.Code)
	var response TaskReferenceCatalogResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.NotNil(t, response.Tasks)
	require.Len(t, response.Tasks, 28)

	byID := make(map[string]TaskReference, len(response.Tasks))
	for _, task := range response.Tasks {
		byID[task.ID] = task
		require.Equal(t, project.ID, task.ProjectID)
	}
	metadata := byID[catalogTask.ID]
	require.Equal(t, strings.Repeat("p", 300), metadata.Prompt)
	require.Equal(t, []string{"Chain", "Goal", model.Name, "Bug", "Urgent"}, metadata.Badges)
	require.Contains(t, byID, parent.ID)
	foundScheduledRunning := false
	for _, task := range response.Tasks {
		if task.Title == "Scheduled running task" {
			foundScheduledRunning = true
			break
		}
	}
	require.True(t, foundScheduledRunning)
	require.NotContains(t, byID, "catalog-swarm-child")
	for _, excluded := range []string{"Foreign task", "Scheduled task", "Chat task"} {
		for _, task := range response.Tasks {
			require.NotEqual(t, excluded, task.Title)
		}
	}

	emptyRec := tc.HTTP().Get("/api/tasks/reference-catalog?project_id=" + empty.ID).Execute()
	require.Equal(t, http.StatusOK, emptyRec.Code)
	var emptyResponse TaskReferenceCatalogResponse
	require.NoError(t, json.NewDecoder(emptyRec.Body).Decode(&emptyResponse))
	require.NotNil(t, emptyResponse.Tasks)
	require.Empty(t, emptyResponse.Tasks)

	missingRec := tc.HTTP().Get("/api/tasks/reference-catalog").Execute()
	require.Equal(t, http.StatusBadRequest, missingRec.Code)
}

func TestHandler_CancelTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test",
		RepoPath:    "/tmp/test",
		IsDefault:   true,
	}
	err := h.projectSvc.Create(ctx, project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task in running status
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Running Task",
		Prompt:    "Test prompt",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Cancel the task
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the task was moved to backlog with pending status
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}
	if updatedTask.Status != models.StatusCancelled {
		t.Errorf("expected task status to be %s, got %s", models.StatusCancelled, updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Errorf("expected task category to be %s, got %s", models.CategoryBacklog, updatedTask.Category)
	}
}

func TestHandler_CancelTask_CancelsAndPublishesEveryRunningExecution(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := tc.CreateTask(project.ID).
		WithStatus(models.StatusRunning).
		WithCategory(models.CategoryActive).
		Build()
	hub := events.NewExecutionStreamHub()
	tc.handler.SetExecutionStreamHub(hub)

	executions := []string{
		tc.CreateExecution(task.ID, agent.ID).WithStatus(models.ExecRunning).Build().ID,
		tc.CreateExecution(task.ID, agent.ID).WithStatus(models.ExecRunning).Build().ID,
	}
	subs := make([]events.ExecutionStreamSubscriber, 0, len(executions))
	for _, execID := range executions {
		sub, _, err := hub.Subscribe(execID)
		if err != nil {
			t.Fatalf("subscribe execution %s: %v", execID, err)
		}
		subs = append(subs, sub)
	}

	rec := tc.HTMX().Post("/tasks/" + task.ID + "/cancel").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", rec.Code, rec.Body.String())
	}

	for i, execID := range executions {
		updated, err := tc.execRepo.GetByID(ctx, execID)
		if err != nil {
			t.Fatalf("get execution %s: %v", execID, err)
		}
		if updated == nil || updated.Status != models.ExecCancelled || updated.ErrorMessage != "cancelled" {
			t.Fatalf("execution %s after cancel = %+v", execID, updated)
		}
		select {
		case event, ok := <-subs[i]:
			if !ok || event.ExecID != execID || event.Type != events.ExecutionStreamDone || event.Status != "cancelled" {
				t.Fatalf("execution %s terminal event = %+v, open=%v", execID, event, ok)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for execution %s terminal event", execID)
		}
		if _, ok := <-subs[i]; ok {
			t.Fatalf("execution %s subscriber remained open", execID)
		}
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("subscriber count after cancellation = %d", hub.SubscriberCount())
	}
}

func TestHandler_CancelRunningExecutionRepositoryErrorIsBestEffortForTaskAndChat(t *testing.T) {
	tests := []struct {
		name         string
		category     models.TaskCategory
		path         func(taskID, projectID string) string
		wantCode     int
		wantCategory models.TaskCategory
	}{
		{
			name:         "task cancel",
			category:     models.CategoryActive,
			path:         func(taskID, _ string) string { return "/tasks/" + taskID + "/cancel" },
			wantCode:     http.StatusOK,
			wantCategory: models.CategoryBacklog,
		},
		{
			name:         "chat stop",
			category:     models.CategoryChat,
			path:         func(_, projectID string) string { return "/chat/stop?project_id=" + projectID },
			wantCode:     http.StatusOK,
			wantCategory: models.CategoryChat,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			project := tc.CreateProject().Build()
			agent := tc.CreateLLMConfig().Build()
			task := tc.CreateTask(project.ID).
				WithStatus(models.StatusRunning).
				WithCategory(test.category).
				Build()
			exec := tc.CreateExecution(task.ID, agent.ID).WithStatus(models.ExecRunning).Build()
			if _, err := tc.db.ExecContext(ctx, `
				CREATE TRIGGER fail_running_execution_cancel
				BEFORE UPDATE OF status ON executions
				WHEN OLD.id = '`+exec.ID+`' AND NEW.status = 'cancelled'
				BEGIN
					SELECT RAISE(ABORT, 'forced execution cancellation failure');
				END;
			`); err != nil {
				t.Fatalf("create failure trigger: %v", err)
			}

			rec := tc.HTMX().Post(test.path(task.ID, project.ID)).Execute()
			if rec.Code != test.wantCode {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			updatedTask, err := tc.taskRepo.GetByID(ctx, task.ID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if updatedTask == nil || updatedTask.Status != models.StatusCancelled || updatedTask.Category != test.wantCategory {
				t.Fatalf("task after best-effort execution cancellation failure = %+v", updatedTask)
			}
			updatedExec, err := tc.execRepo.GetByID(ctx, exec.ID)
			if err != nil {
				t.Fatalf("get execution: %v", err)
			}
			if updatedExec == nil || updatedExec.Status != models.ExecRunning {
				t.Fatalf("execution after forced repository error = %+v", updatedExec)
			}
		})
	}
}

func TestHandler_CancelTask_PausesActiveGoalStoppedByUser(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Goal stop task",
		Prompt:    "keep going",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	if err := tc.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	goal, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "Continue until the audit is clean", service.GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}

	rec := tc.HTMX().Post("/tasks/" + task.ID + "/cancel").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", rec.Code, rec.Body.String())
	}
	paused, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	if err != nil {
		t.Fatalf("get goal: %v", err)
	}
	if paused == nil || paused.Status != models.TaskGoalStatusPaused || paused.GoalID != goal.GoalID || paused.Reason != "stopped by user" {
		t.Fatalf("goal after manual cancel = %+v", paused)
	}
	if err := tc.handler.taskGoalSvc.ResumeGoal(ctx, task.ID, "user"); err != nil {
		t.Fatalf("resume stopped goal: %v", err)
	}
	resumed, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	if err != nil {
		t.Fatalf("get resumed goal: %v", err)
	}
	if resumed.Status != models.TaskGoalStatusActive || resumed.GoalID != goal.GoalID {
		t.Fatalf("goal after resume = %+v", resumed)
	}
}

func TestHandler_TaskThreadComposerAction_RendersStopAndSendStates(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Composer action task").
		WithStatus(models.StatusRunning).
		WithCategory(models.CategoryActive).
		Build()
	task.AgentID = &agent.ID
	if err := tc.handler.taskRepo.Update(ctx, task); err != nil {
		t.Fatalf("update task agent: %v", err)
	}
	runningExec := tc.CreateExecution(task.ID, agent.ID).
		WithStatus(models.ExecRunning).
		WithPromptSent("active").
		Build()

	rec := tc.HTMX().Get("/tasks/" + task.ID + "/thread/composer-action").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("active composer action status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="task-thread-form-primary-action" data-composer-running="true" data-active-turn-id="`+runningExec.ID+`" data-composer-stop-endpoint="/tasks/`+task.ID+`/cancel?composer_stop=1" hx-swap-oob="outerHTML"`) {
		t.Fatalf("expected active OOB primary action fragment, got %s", body)
	}
	if !strings.Contains(body, `title="Stop response"`) || !strings.Contains(body, `/tasks/`+task.ID+`/cancel?composer_stop=1`) {
		t.Fatalf("expected active task-thread action to render stop button, got %s", body)
	}

	if err := tc.handler.execRepo.Complete(ctx, runningExec.ID, models.ExecCancelled, "partial", "cancelled", 0, 1); err != nil {
		t.Fatalf("complete execution: %v", err)
	}
	if err := tc.handler.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCancelled); err != nil {
		t.Fatalf("update task status: %v", err)
	}
	if err := tc.handler.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
		t.Fatalf("update task category: %v", err)
	}

	rec = tc.HTMX().Get("/tasks/" + task.ID + "/thread/composer-action").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("terminal composer action status=%d body=%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `data-composer-running="false"`) {
		t.Fatalf("expected terminal task-thread action to clear active composer state, got %s", body)
	}
	if !strings.Contains(body, `title="Send message"`) || strings.Contains(body, `title="Stop response"`) {
		t.Fatalf("expected terminal task-thread action to render send button, got %s", body)
	}
}

func TestHandler_CancelTask_AllowsActivePendingTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Test Pending Active Task", Prompt: "Test prompt", Status: models.StatusPending, Category: models.CategoryActive}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}
	if updatedTask.Status != models.StatusCancelled {
		t.Errorf("expected task status to become %s, got %s", models.StatusCancelled, updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Errorf("expected task category to become %s, got %s", models.CategoryBacklog, updatedTask.Category)
	}
}

func TestHandler_CancelTaskPulseRefreshesPulseAndKeepsTaskInBacklog(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Pulse running task").
		WithStatus(models.StatusRunning).
		WithCategory(models.CategoryActive).
		Build()

	rec := tc.HTMX().Post("/tasks/" + task.ID + "/cancel?pulse=1&project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `id="upcoming-container"`)
	require.NotContains(t, rec.Body.String(), task.Title)
	var trigger struct {
		Toast struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"openvibelyToast"`
	}
	require.NoError(t, json.Unmarshal([]byte(rec.Header().Get("HX-Trigger")), &trigger))
	require.Equal(t, "Task stopped.", trigger.Toast.Message)
	require.Equal(t, "success", trigger.Toast.Status)

	updated, err := tc.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updated.Status)
	require.Equal(t, models.CategoryBacklog, updated.Category)
}

func TestHandler_CancelTaskPulseCancelsQueuedInputs(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Pulse queued task").
		WithStatus(models.StatusQueued).
		WithCategory(models.CategoryActive).
		Build()
	queuedInput := &models.ThreadInput{
		Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID,
		AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued follow-up",
	}
	require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, queuedInput))

	rec := tc.HTMX().Post("/tasks/" + task.ID + "/cancel?pulse=1&project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	storedInput, err := tc.handler.threadInputRepo.GetByID(ctx, queuedInput.ID)
	require.NoError(t, err)
	require.NotNil(t, storedInput)
	require.Equal(t, models.ThreadInputCancelled, storedInput.InputStatus)

	updated, err := tc.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updated.Status)
	require.Equal(t, models.CategoryBacklog, updated.Category)
}

func TestHandler_CancelTaskPulseDelegatesSwarmCascade(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Pulse Swarm Cancel")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID: project.ID, Title: "Pulse swarm parent", Prompt: "Build it", Category: models.CategoryActive,
		Priority: 2, AgentID: &agent.ID, MaxWorkers: 1, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, planner.ID, models.StatusRunning))

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+parent.ID+"/cancel?pulse=1&project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updatedParent.Status)
	updatedPlanner, err := h.taskRepo.GetByID(ctx, planner.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updatedPlanner.Status)
}
func TestHandler_CancelTaskPulseRejectsStaleAndForeignCardsWithoutRefresh(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*TestContext, *models.Project) *models.Task
		wantStatus int
		wantToast  string
	}{
		{
			name: "stale task",
			setup: func(tc *TestContext, project *models.Project) *models.Task {
				return tc.CreateTask(project.ID).WithTitle("Pulse completed task").WithStatus(models.StatusCompleted).WithCategory(models.CategoryCompleted).Build()
			},
			wantStatus: http.StatusConflict,
			wantToast:  "This task is no longer cancellable.",
		},
		{
			name: "foreign task",
			setup: func(tc *TestContext, project *models.Project) *models.Task {
				foreign := tc.CreateProject().WithName("Foreign Pulse Project").Build()
				return tc.CreateTask(foreign.ID).WithTitle("Foreign Pulse task").WithStatus(models.StatusRunning).WithCategory(models.CategoryActive).Build()
			},
			wantStatus: http.StatusNotFound,
			wantToast:  "This task is not available in the selected project.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			task := tt.setup(tc, project)
			rec := tc.HTMX().Post("/tasks/" + task.ID + "/cancel?pulse=1&project_id=" + project.ID).Execute()
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Empty(t, rec.Body.String())
			require.Contains(t, rec.Header().Get("HX-Trigger"), tt.wantToast)

			unchanged, err := tc.taskRepo.GetByID(context.Background(), task.ID)
			require.NoError(t, err)
			require.Equal(t, task.Status, unchanged.Status)
			require.Equal(t, task.Category, unchanged.Category)
		})
	}
}
func TestHandler_CancelTask_RejectsBacklogPendingTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Test Pending Backlog Task", Prompt: "Test prompt", Status: models.StatusPending, Category: models.CategoryBacklog}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected task status to remain %s, got %s", models.StatusPending, updatedTask.Status)
	}
}

func TestHandler_CancelTask_NotFound(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	// Try to cancel a non-existent task
	req := httptest.NewRequest(http.MethodPost, "/tasks/nonexistent-id/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return not found
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestHandler_RunTask_NoModelsConfiguredHTMX(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agents, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("failed to list models: %v", err)
	}
	for _, agent := range agents {
		if err := llmConfigRepo.Delete(ctx, agent.ID); err != nil {
			t.Fatalf("failed to delete model %s: %v", agent.ID, err)
		}
	}

	task := &models.Task{
		ProjectID: "default",
		Title:     "No model run",
		Prompt:    "Try to run",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/run", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger to contain openvibelyToast event, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredMessage) {
		t.Fatalf("expected HX-Trigger to contain no-models message, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredLinkURL) {
		t.Fatalf("expected HX-Trigger to contain models link URL %q, got %q", noModelsConfiguredLinkURL, hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredLinkText) {
		t.Fatalf("expected HX-Trigger to contain models link text %q, got %q", noModelsConfiguredLinkText, hxTrigger)
	}

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updatedTask == nil {
		t.Fatal("expected task to exist")
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Fatalf("expected task category to remain %s, got %s", models.CategoryBacklog, updatedTask.Category)
	}
	if updatedTask.Status != models.StatusPending {
		t.Fatalf("expected task status to remain %s, got %s", models.StatusPending, updatedTask.Status)
	}
}

func TestHandler_CreateTask_Active_NoModelsConfiguredHTMX(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agents, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("failed to list models: %v", err)
	}
	for _, agent := range agents {
		if err := llmConfigRepo.Delete(ctx, agent.ID); err != nil {
			t.Fatalf("failed to delete model %s: %v", agent.ID, err)
		}
	}

	body := strings.NewReader("title=No+Model+Task&prompt=Try+to+create&category=active&priority=0")
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id=default", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger to contain openvibelyToast event, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredMessage) {
		t.Fatalf("expected HX-Trigger to contain no-models message, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredLinkURL) {
		t.Fatalf("expected HX-Trigger to contain models link URL %q, got %q", noModelsConfiguredLinkURL, hxTrigger)
	}

	tasks, err := h.taskSvc.ListByProjectWithCategorySorts(ctx, "default", "", "", "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.Title == "No Model Task" {
			t.Fatalf("task should not be created when no models are configured")
		}
	}
}

func TestHandler_CreateTask_WithMultipleAttachments(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test",
		RepoPath:    "/tmp/test",
		IsDefault:   true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create multipart form with multiple files
	body, contentType, err := createMultipartFormWithFiles(t, map[string]string{
		"title":    "Task with Multiple Attachments",
		"prompt":   "Test prompt",
		"category": "active",
		"priority": "0",
	}, []string{"file1.txt", "file2.txt", "file3.txt"})
	if err != nil {
		t.Fatalf("failed to create multipart form: %v", err)
	}

	// Create task with attachments
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify task was created
	tasks, err := h.taskSvc.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	task := tasks[0]
	if task.Priority != 2 {
		t.Fatalf("expected multipart priority=0 to normalize to 2, got %d", task.Priority)
	}

	// Verify all 3 attachments were created
	attachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}

	if len(attachments) != 3 {
		t.Errorf("expected 3 attachments, got %d", len(attachments))
	}

	// Verify filenames
	expectedFiles := map[string]bool{
		"file1.txt": false,
		"file2.txt": false,
		"file3.txt": false,
	}

	for _, att := range attachments {
		if _, exists := expectedFiles[att.FileName]; exists {
			expectedFiles[att.FileName] = true
		} else {
			t.Errorf("unexpected attachment: %s", att.FileName)
		}
	}

	for file, found := range expectedFiles {
		if !found {
			t.Errorf("missing expected attachment: %s", file)
		}
	}
}

func TestHandler_GetTask_AfterCategoryChange(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test",
		RepoPath:    "/tmp/test",
		IsDefault:   true,
	}
	err := h.projectSvc.Create(ctx, project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task in active category with running status
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Prompt:    "Test prompt",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Get the task detail dialog (simulating opening the dialog while task is running)
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("active")) {
		t.Error("expected dialog to show 'active' category")
	}
	if !bytes.Contains([]byte(body), []byte("In Progress")) || !bytes.Contains([]byte(body), []byte("running")) {
		t.Error("expected dialog to show running status")
	}

	// Simulate task completion: update status to completed and category to completed
	err = h.taskSvc.UpdateStatus(ctx, task.ID, models.StatusCompleted)
	if err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}
	err = h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryCompleted)
	if err != nil {
		t.Fatalf("failed to update task category: %v", err)
	}

	// Get the task detail dialog again (simulating SSE refresh of open dialog)
	req = httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body = rec.Body.String()
	// Verify the dialog now shows completed category
	if !bytes.Contains([]byte(body), []byte("completed")) {
		t.Error("expected dialog to show 'completed' category after update")
	}
	// Verify the status badge is updated
	if !bytes.Contains([]byte(body), []byte("Completed")) {
		t.Error("expected dialog to show 'Completed' status label after update")
	}
	// Verify we no longer see running status
	if bytes.Contains([]byte(body), []byte("In Progress")) {
		t.Error("dialog should not show 'In Progress' after completion")
	}
}

func TestHandler_TaskThread_RendersStopButtonWhileActiveAndSendWhenTerminal(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Thread Stop UI Project", Description: "Test", RepoPath: "/tmp/thread-stop-ui", IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Running thread", Prompt: "run", Status: models.StatusRunning, Category: models.CategoryActive, AgentID: &agent.ID}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "run"}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `hx-post="/tasks/`+task.ID+`/cancel?composer_stop=1"`)
	assertContains(t, rec, `title="Stop response"`)
	assertContains(t, rec, `<rect x="6" y="6" width="12" height="12" rx="2"></rect>`)
	assertNotContains(t, rec, `title="Cancel running task"`)
	assertNotContains(t, rec, `>Cancel</button>`)

	if err := h.execRepo.Complete(ctx, exec.ID, models.ExecCancelled, "stopped", "cancelled", 0, 1); err != nil {
		t.Fatalf("cancel exec: %v", err)
	}
	if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatalf("queue task: %v", err)
	}
	rec = htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `hx-post="/tasks/`+task.ID+`/cancel?composer_stop=1"`)
	assertContains(t, rec, `title="Stop response"`)

	completedExec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "done", Output: "done"}
	if err := h.execRepo.Create(ctx, completedExec); err != nil {
		t.Fatalf("failed to create completed execution: %v", err)
	}
	if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	rec = htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `title="Send message"`)
	assertContains(t, rec, `<path d="M2 3l20 9-20 9 5-9-5-9z"></path>`)
	assertNotContains(t, rec, `hx-post="/tasks/`+task.ID+`/cancel"`)
}

func TestHandler_TaskThread_LightModeToolCallContrastStyles(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name:        "Thread Style Project",
		Description: "Test",
		RepoPath:    "/tmp/thread-style-test",
		IsDefault:   true,
	}
	err := h.projectSvc.Create(ctx, project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Thread style task",
		Prompt:    "Check thread styles",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab=chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, `id="thread-content"`) {
		t.Fatal("expected lazy thread container in task detail response")
	}
	if !strings.Contains(body, `Thread is loading...`) {
		t.Fatal("expected lazy thread loading placeholder when chat tab is active")
	}
	if !strings.Contains(body, `function _loadThreadContent(taskId, forceReload, expectedExecId)`) {
		t.Fatal("expected thread lazy-load helper in task detail response")
	}
	if !strings.Contains(body, `--ov-link-color: #7480ff;`) {
		t.Error("expected shared link color token for thread/chat link styling")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-summary .tool-name-secondary`) {
		t.Error("expected light-theme tool secondary text style in thread view response")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body {`) {
		t.Error("expected light-theme tool outer body style in thread view response")
	}
	if !strings.Contains(body, `border: none;`) {
		t.Error("expected light-theme tool outer body to blend without border emphasis")
	}
	if !strings.Contains(body, `background: transparent;`) {
		t.Error("expected light-theme tool outer body to blend without background emphasis")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body-row {`) {
		t.Error("expected light-theme tool row style in thread view response")
	}
	if !strings.Contains(body, `border-top-color: transparent;`) {
		t.Error("expected light-theme tool rows to avoid divider emphasis")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body-content {`) {
		t.Error("expected light-theme tool content block style in thread view response")
	}
	if !strings.Contains(body, `background: transparent;`) {
		t.Error("expected light-theme tool content block to remain transparent around separate IN/OUT surfaces")
	}
	if !strings.Contains(body, `border: none;`) {
		t.Error("expected light-theme tool outer content block to avoid extra border")
	}
	if !strings.Contains(body, `[data-theme="light"] .chat-bubble-assistant-msg pre {`) {
		t.Error("expected light-theme tool IN/OUT pre surfaces in thread view response")
	}
	if !strings.Contains(body, `background-color: var(--ov-l-surface);`) || !strings.Contains(body, `border-color: var(--ov-l-border);`) {
		t.Error("expected separate light-theme tool IN/OUT surface colors in thread view response")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body-content .stream-tool-output-text {`) {
		t.Error("expected light-theme tool IN/OUT surfaces to remove border emphasis")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-thinking summary {`) || !strings.Contains(body, `[data-theme="light"] .stream-thinking .stream-thinking-body {`) {
		t.Error("expected readable light-theme Thinking foreground styles")
	}
	if strings.Contains(body, `[data-theme="light"] .stream-tool-body-scroll pre`) {
		t.Error("light-theme tool output must not flatten IN/OUT pre surfaces")
	}
	if !strings.Contains(body, `overflow: hidden;`) {
		t.Error("expected tool body shell/content to clip child backgrounds to rounded corners")
	}
	if !strings.Contains(body, `[data-theme="light"] .tool-status-done`) {
		t.Error("expected light-theme tool status icon style in thread view response")
	}
	if strings.Contains(body, `[data-theme="dark"] .stream-tool-body {`) || strings.Contains(body, `[data-theme="dark"] .stream-tool-body-content`) {
		t.Error("tool output control styling must not override the original dark tool container")
	}
	if !strings.Contains(body, `.chat-markdown a {`) {
		t.Error("expected shared markdown link styling in thread view response")
	}
	if !strings.Contains(body, `.chat-markdown a:visited {`) {
		t.Error("expected markdown visited-link styling in thread view response")
	}
	if !strings.Contains(body, `.chat-markdown a:focus-visible {`) {
		t.Error("expected markdown focus-visible link styling in thread view response")
	}
}

// createMultipartFormWithFiles creates a multipart form with form fields and multiple files
func createMultipartFormWithFiles(t *testing.T, fields map[string]string, filenames []string) (io.Reader, string, error) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add form fields
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, "", err
		}
	}

	// Add files
	for i, filename := range filenames {
		fileWriter, err := writer.CreateFormFile("files", filename)
		if err != nil {
			return nil, "", err
		}
		content := []byte("test file " + filename + " content")
		if _, err := fileWriter.Write(content); err != nil {
			return nil, "", err
		}
		t.Logf("Added file %d: %s", i+1, filename)
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return body, writer.FormDataContentType(), nil
}
