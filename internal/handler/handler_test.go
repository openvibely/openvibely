package handler

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func setupTestHandler(t *testing.T) (*Handler, *echo.Echo, *repository.LLMConfigRepo) {
	h, e, llmConfigRepo, _ := setupTestHandlerWithDB(t)
	return h, e, llmConfigRepo
}

func setupTestHandlerWithDB(t *testing.T) (*Handler, *echo.Echo, *repository.LLMConfigRepo, *sql.DB) {
	t.Helper()
	oldUploadsDir := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() {
		uploadsDir = oldUploadsDir
	})

	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)

	alertRepo := repository.NewAlertRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)

	settingsRepo := repository.NewSettingsRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, nil, nil)
	h.SetSlackAuthRepo(slackAuthRepo)
	h.SetLocalRepoPathEnabled(true)

	e := echo.New()
	h.RegisterRoutes(e)

	return h, e, llmConfigRepo, db
}

// createProject creates a test project with the given name.
func createProject(t *testing.T, h *Handler, name string) *models.Project {
	t.Helper()
	p := &models.Project{Name: name}
	if err := h.projectSvc.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

// createAgent creates a test LLM config with sensible defaults.
// Uses ProviderTest so tests never hit real APIs or spawn CLI subprocesses.
func createAgent(t *testing.T, repo *repository.LLMConfigRepo, opts ...func(*models.LLMConfig)) *models.LLMConfig {
	t.Helper()
	a := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, IsDefault: true,
	}
	for _, o := range opts {
		o(a)
	}
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return a
}

// createTask creates a test task with sensible defaults (active/pending).
func createTask(t *testing.T, h *Handler, projectID, title string, opts ...func(*models.Task)) *models.Task {
	t.Helper()
	task := &models.Task{
		ProjectID: projectID, Title: title,
		Category: models.CategoryActive, Status: models.StatusPending,
		Prompt: "test prompt",
	}
	for _, o := range opts {
		o(task)
	}
	if err := h.taskSvc.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// createSchedule creates a test schedule for a task.
func createSchedule(t *testing.T, h *Handler, taskID string, runAt time.Time, opts ...func(*models.Schedule)) *models.Schedule {
	t.Helper()
	s := &models.Schedule{
		TaskID: taskID, RunAt: runAt,
		RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true,
	}
	for _, o := range opts {
		o(s)
	}
	if err := h.scheduleRepo.Create(context.Background(), s); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return s
}

// createExec creates a test execution.
func createExec(t *testing.T, h *Handler, taskID, agentID string, opts ...func(*models.Execution)) *models.Execution {
	t.Helper()
	ex := &models.Execution{
		TaskID: taskID, AgentConfigID: agentID,
		Status: models.ExecRunning,
	}
	for _, o := range opts {
		o(ex)
	}
	if err := h.execRepo.Create(context.Background(), ex); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	return ex
}

// htmxGet makes an HTMX GET request and returns the recorder.
func htmxGet(e *echo.Echo, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// htmxPost makes an HTMX POST request with form data and returns the recorder.
func htmxPost(e *echo.Echo, path string, form url.Values) *httptest.ResponseRecorder {
	var body string
	if form != nil {
		body = form.Encode()
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// htmxPatch makes an HTMX PATCH request with form data and returns the recorder.
func htmxPatch(e *echo.Echo, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// htmxPut makes an HTMX PUT request with form data and returns the recorder.
func htmxPut(e *echo.Echo, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// htmxDelete makes an HTMX DELETE request and returns the recorder.
func htmxDelete(e *echo.Echo, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// postForm makes a regular POST request with form data and returns the recorder.
func postForm(e *echo.Echo, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// assertCode checks that the response has the expected status code.
func assertCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("expected status %d, got %d; body=%s", want, rec.Code, rec.Body.String())
	}
}

// assertContains checks that the response body contains the given string.
func assertContains(t *testing.T, rec *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), substr) {
		t.Errorf("expected body to contain %q", substr)
	}
}

// assertNotContains checks that the response body does NOT contain the given string.
func assertNotContains(t *testing.T, rec *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if strings.Contains(rec.Body.String(), substr) {
		t.Errorf("expected body to NOT contain %q", substr)
	}
}

func boolPtr(b bool) *bool { return &b }

func setupTestHandlerWithInsights(t *testing.T) (*Handler, *echo.Echo) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	insightsRepo := repository.NewInsightsRepo(db)
	alertRepo := repository.NewAlertRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	insightsSvc := service.NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	insightsSvc.SetLLMService(llmSvc)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, nil, insightsSvc, nil, nil, nil, nil, nil, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, nil, nil, nil)
	e := echo.New()
	h.RegisterRoutes(e)
	return h, e
}

func TestHandler_GetTask_HTMX(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Test Task", func(tk *models.Task) {
		tk.Priority = 1
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId")
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTask(c); err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "task-detail-content")
	assertContains(t, rec, task.Title)
	assertNotContains(t, rec, "task_detail_modal")
}

func TestHandler_TasksPage_NoDialogContainer(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	assertNotContains(t, rec, `id="task-dialog-container"`)
	assertNotContains(t, rec, `task_detail_modal`)
}

func TestHandler_GetTaskExecutions(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "test-agent"
		a.Provider = "anthropic"
		a.Model = "claude-3-5-sonnet-20241022"
		a.IsDefault = false
	})
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Test Task", func(tk *models.Task) {
		tk.Prompt = "Test Prompt"
	})

	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Output = "Working on it..."
	})
	task.Status = models.StatusRunning
	if err := h.taskSvc.Update(ctx, task); err != nil {
		t.Fatalf("failed to update task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/executions", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId/executions")
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskExecutions(c); err != nil {
		t.Fatalf("GetTaskExecutions failed: %v", err)
	}
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Execution History")
	assertContains(t, rec, `id="task-execution-history"`)
	assertContains(t, rec, "hx-trigger")
	assertContains(t, rec, "/executions")
	body := rec.Body.String()
	if !strings.Contains(body, "loading-spinner") && !strings.Contains(body, "Model is working") {
		t.Errorf("expected execution status in response")
	}
}

func TestHandler_GetTaskDetailStatus(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Status Test Task", func(tk *models.Task) {
		tk.Priority = 2
	})

	// Test 1: Pending task returns status badge
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/detail-status", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId/detail-status")
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskDetailStatus(c); err != nil {
		t.Fatalf("GetTaskDetailStatus failed: %v", err)
	}
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `id="task-detail-metrics"`)
	assertContains(t, rec, "hx-trigger")
	assertContains(t, rec, "/detail-status")
	assertContains(t, rec, "Queued")

	// Test 2: Running task shows running status and elapsed time
	createExec(t, h, task.ID, agent.ID)
	task.Status = models.StatusRunning
	if err := h.taskSvc.Update(ctx, task); err != nil {
		t.Fatalf("failed to update task: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/detail-status", nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	c2.SetPath("/tasks/:taskId/detail-status")
	c2.SetParamNames("taskId")
	c2.SetParamValues(task.ID)

	if err := h.GetTaskDetailStatus(c2); err != nil {
		t.Fatalf("GetTaskDetailStatus (running) failed: %v", err)
	}
	assertContains(t, rec2, "In Progress")
	assertContains(t, rec2, "badge-warning")
	assertContains(t, rec2, "Elapsed")

	// Test 3: Not found task returns 404
	req3 := httptest.NewRequest(http.MethodGet, "/tasks/nonexistent/detail-status", nil)
	rec3 := httptest.NewRecorder()
	c3 := e.NewContext(req3, rec3)
	c3.SetPath("/tasks/:taskId/detail-status")
	c3.SetParamNames("taskId")
	c3.SetParamValues("nonexistent")

	if err := h.GetTaskDetailStatus(c3); err == nil {
		t.Errorf("expected error for nonexistent task")
	}
}

func TestHandler_GetTask_RunningTask(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Running Task", func(tk *models.Task) {
		tk.Priority = 1
		tk.Status = models.StatusRunning
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.PromptSent = task.Prompt
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId")
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTask(c); err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `id="thread-content"`)
	assertContains(t, rec, "Thread is loading...")
	assertContains(t, rec, "function _loadThreadContent(taskId)")
}

func TestHandler_GetTask_CompletedTaskDefaultsToChat(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	for _, tc := range []struct {
		name   string
		status models.TaskStatus
	}{
		{"completed", models.StatusCompleted},
		{"failed", models.StatusFailed},
		{"cancelled", models.StatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := createTask(t, h, project.ID, fmt.Sprintf("Task %s", tc.name), func(tk *models.Task) {
				tk.Priority = 1
				tk.Status = tc.status
			})

			req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/tasks/:taskId")
			c.SetParamNames("taskId")
			c.SetParamValues(task.ID)

			if err := h.GetTask(c); err != nil {
				t.Fatalf("GetTask failed: %v", err)
			}
			assertCode(t, rec, http.StatusOK)
			assertContains(t, rec, `data-tab="chat"`)
			assertContains(t, rec, `id="tab-chat"`)
			assertContains(t, rec, "tab-active")
		})
	}
}

func TestHandler_GetTask_StatusIndicator(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Test Project")

	cases := []struct {
		name       string
		taskStatus models.TaskStatus
		category   models.TaskCategory
		execStatus models.ExecutionStatus
		execOutput string
		execError  string
		complete   bool
		wantTexts  []string
		wantAbsent []string
	}{
		{"completed_shows_success", models.StatusCompleted, models.CategoryCompleted, models.ExecCompleted, "Done!", "", true, []string{"Task completed", "text-success"}, nil},
		{"failed_shows_error", models.StatusFailed, models.CategoryCompleted, models.ExecFailed, "", "something went wrong", true, []string{"Task failed", "text-error"}, nil},
		{"running_no_indicator", models.StatusRunning, models.CategoryActive, models.ExecRunning, "", "", false, nil, []string{"Task completed", "Task failed"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := createTask(t, h, project.ID, tc.name, func(tk *models.Task) {
				tk.Status = tc.taskStatus
				tk.Category = tc.category
				tk.Prompt = "Do something"
				tk.Priority = 2
			})
			ex := createExec(t, h, task.ID, agent.ID, func(e *models.Execution) {
				e.Status = tc.execStatus
				e.PromptSent = "Do something"
				e.Output = tc.execOutput
				e.ErrorMessage = tc.execError
				e.DurationMs = 5000
			})
			if tc.complete {
				if err := h.execRepo.Complete(ctx, ex.ID, tc.execStatus, tc.execOutput, tc.execError, 100, 5000); err != nil {
					t.Fatalf("complete execution: %v", err)
				}
			}

			req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/thread", nil)
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/tasks/:taskId/thread")
			c.SetParamNames("taskId")
			c.SetParamValues(task.ID)

			if err := h.GetTaskThread(c); err != nil {
				t.Fatalf("GetTaskThread failed: %v", err)
			}

			body := rec.Body.String()
			for _, want := range tc.wantTexts {
				if !strings.Contains(body, want) {
					t.Errorf("expected %q in response", want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(body, absent) {
					t.Errorf("did not expect %q in response", absent)
				}
			}
		})
	}
}

func TestHandler_CreateModel(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Test Agent")
	form.Set("provider", "anthropic")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")
	form.Set("is_default", "on")

	rec := postForm(e, "/models", form)
	assertCode(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); loc != "/models" {
		t.Errorf("expected redirect to /models, got %q", loc)
	}
}

func TestHandler_CreateModel_Normalization(t *testing.T) {
	cases := []struct {
		name          string
		provider      string
		inputModel    string
		reasoning     string
		wantModel     string
		wantReasoning string
	}{
		{"openai_preserves_gpt54", "openai", "gpt-5.4", "xhigh", "gpt-5.4", "xhigh"},
		{"openai_normalizes_unknown", "openai", "unknown-model", "high", "gpt-5.4", "high"},
		{"non_openai_preserves", "anthropic", "claude-opus-4-6", "xhigh", "claude-opus-4-6", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e, llmConfigRepo := setupTestHandler(t)
			ctx := context.Background()

			form := url.Values{}
			form.Set("name", "Model "+tc.name)
			form.Set("provider", tc.provider)
			form.Set("model", tc.inputModel)
			form.Set("reasoning_effort", tc.reasoning)
			form.Set("max_tokens", "4096")
			form.Set("temperature", "0")

			rec := postForm(e, "/models", form)
			assertCode(t, rec, http.StatusSeeOther)

			agents, _ := llmConfigRepo.List(ctx)
			var created *models.LLMConfig
			for i := range agents {
				if agents[i].Name == "Model "+tc.name {
					created = &agents[i]
					break
				}
			}
			if created == nil {
				t.Fatal("model not found")
			}
			if created.Model != tc.wantModel {
				t.Errorf("model: got %q, want %q", created.Model, tc.wantModel)
			}
			if created.ReasoningEffort != tc.wantReasoning {
				t.Errorf("reasoning: got %q, want %q", created.ReasoningEffort, tc.wantReasoning)
			}
		})
	}
}

func TestHandler_CreateTask_WithSchedule(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	form := url.Values{}
	form.Set("title", "Scheduled Task")
	form.Set("category", "scheduled")
	form.Set("priority", "0")
	form.Set("prompt", "What is 2+2?")
	form.Set("run_at", "2026-02-22T10:00")
	form.Set("repeat_type", "daily")
	form.Set("repeat_interval", "1")
	rec := postForm(e, "/tasks?project_id=default", form)
	assertCode(t, rec, http.StatusOK)
}

func TestHandler_CreateTask_ActiveCategory(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	form := url.Values{}
	form.Set("title", "Active Task")
	form.Set("category", "active")
	form.Set("prompt", "Do something")
	rec := postForm(e, "/tasks?project_id=default", form)
	assertCode(t, rec, http.StatusOK)
}

func TestHandler_CreateTask_DuplicateTitle(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	form1 := url.Values{}
	form1.Set("title", "Duplicate Task")
	form1.Set("category", "active")
	form1.Set("prompt", "Do something")
	rec1 := postForm(e, "/tasks?project_id=default", form1)
	assertCode(t, rec1, http.StatusOK)

	form2 := url.Values{}
	form2.Set("title", "Duplicate Task")
	form2.Set("category", "backlog")
	form2.Set("prompt", "Do something else")
	rec2 := postForm(e, "/tasks?project_id=default", form2)
	assertCode(t, rec2, http.StatusConflict)
	assertContains(t, rec2, "task with this name already exists")
}

func TestHandler_DeleteModel_HTMX(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Agent To Delete"
		a.Provider = models.ProviderTest
		a.APIKey = "sk-test"
		a.Temperature = 0.5
		a.IsDefault = false
	})
	agentsBefore, _ := llmConfigRepo.List(ctx)
	initialCount := len(agentsBefore)

	rec := htmxDelete(e, "/models/"+agent.ID)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "models-container")

	if deleted, _ := llmConfigRepo.GetByID(ctx, agent.ID); deleted != nil {
		t.Error("expected agent to be deleted")
	}
	if agentsAfter, _ := h.llmConfigRepo.List(ctx); len(agentsAfter) != initialCount-1 {
		t.Errorf("expected %d agents after delete, got %d", initialCount-1, len(agentsAfter))
	}
}

func TestHandler_DeleteModel_DefaultAgent_AutoReassignsWhenAnotherExists(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	defaultAgent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || defaultAgent == nil {
		t.Fatal("expected seeded default model")
	}

	replacement := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Replacement Default"
		a.Provider = models.ProviderTest
		a.IsDefault = false
	})

	rec := htmxDelete(e, "/models/"+defaultAgent.ID)
	assertCode(t, rec, http.StatusOK)
	if deleted, _ := llmConfigRepo.GetByID(ctx, defaultAgent.ID); deleted != nil {
		t.Error("default model should have been deleted")
	}
	newDefault, _ := llmConfigRepo.GetDefault(ctx)
	if newDefault == nil {
		t.Fatal("expected a new default model")
	}
	if newDefault.ID != replacement.ID {
		t.Errorf("expected replacement model %s to be default, got %s", replacement.ID, newDefault.ID)
	}
}

func TestHandler_DeleteModel_OnlyModel_Allowed(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	defaultAgent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || defaultAgent == nil {
		t.Fatal("expected seeded default model")
	}

	rec := htmxDelete(e, "/models/"+defaultAgent.ID)
	assertCode(t, rec, http.StatusOK)

	count, err := llmConfigRepo.Count(ctx)
	if err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 models after deleting only model, got %d", count)
	}
	def, err := llmConfigRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("get default after delete: %v", err)
	}
	if def != nil {
		t.Fatal("expected no default model when no models remain")
	}
}

func TestHandler_DeleteModel_WithTaskReferences(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Agent With Tasks"; a.IsDefault = false })
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Task With Agent", func(tk *models.Task) { tk.Category = models.CategoryBacklog; tk.AgentID = &agent.ID })

	rec := htmxDelete(e, "/models/"+agent.ID)
	assertCode(t, rec, http.StatusOK)

	if deleted, _ := llmConfigRepo.GetByID(ctx, agent.ID); deleted != nil {
		t.Error("expected agent to be deleted")
	}
	gotTask, _ := h.taskRepo.GetByID(ctx, task.ID)
	if gotTask == nil {
		t.Fatal("expected task to still exist")
	}
	if gotTask.AgentID != nil {
		t.Errorf("expected task agent_id to be NULL, got %v", *gotTask.AgentID)
	}
}

func TestHandler_ListModels(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := htmxGet(e, "/models")
	assertCode(t, rec, http.StatusOK)
}

func TestHandler_ListModels_DefaultBadgeUsesCanonicalClass(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	if !strings.Contains(body, `ov-badge-default">Default</span>`) {
		t.Errorf("expected models page default badge to use canonical ov-badge-default class")
	}
	if strings.Contains(body, `badge badge-primary badge-sm ml-2">Default</span>`) {
		t.Errorf("expected models page to stop using page-specific badge-primary default badge class")
	}
}

func TestHandler_ListModels_ProviderIconsUseExpectedBrandMarkup(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Anthropic Icon Agent"
		a.Provider = models.ProviderAnthropic
		a.Model = "claude-sonnet-4-5-20250929"
		a.IsDefault = false
	})
	createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "OpenAI Icon Agent"
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-5"
		a.IsDefault = false
	})

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	if !strings.Contains(body, `M17.3041 3.541h-3.6718l6.696 16.918H24Z`) {
		t.Errorf("expected Anthropic card icon to render official Anthropic path")
	}
	if strings.Contains(body, `cx="12" cy="12" r="11"`) {
		t.Errorf("expected Anthropic card icon to stop rendering legacy circular glyph")
	}
	if !strings.Contains(body, `M22.282 9.821a5.985 5.985`) {
		t.Errorf("expected OpenAI card icon markup to remain present")
	}
}

func TestHandler_ListModels_IncludesToastModalStackingHooks(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	if !strings.Contains(body, "id=\"new_model_modal\" class=\"modal\" onclose=\"if (typeof syncToastContainerHost === 'function') syncToastContainerHost()\"") {
		t.Errorf("expected new model modal to resync toast host on close")
	}
	if !strings.Contains(body, "id=\"reassign_default_modal\" class=\"modal\" onclose=\"if (typeof syncToastContainerHost === 'function') syncToastContainerHost()\"") {
		t.Errorf("expected reassign-default modal to resync toast host on close")
	}
	if !strings.Contains(body, "dialog.modal > .modal-box") {
		t.Errorf("expected modal z-index layering rules for modal box")
	}
	if !strings.Contains(body, "dialog.modal #toast-container") {
		t.Errorf("expected modal-scoped toast container z-index override")
	}
}

func TestHandler_SetDefaultModel(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	original, _ := llmConfigRepo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model")
	}
	second := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Second Agent"; a.IsDefault = false })

	rec := htmxPost(e, "/models/"+second.ID+"/set-default", nil)
	assertCode(t, rec, http.StatusOK)

	newDefault, _ := llmConfigRepo.GetDefault(ctx)
	if newDefault == nil || newDefault.ID != second.ID {
		t.Errorf("expected second agent to be default")
	}
	origAgent, _ := llmConfigRepo.GetByID(ctx, original.ID)
	if origAgent.IsDefault {
		t.Error("expected original agent to no longer be default")
	}
}

func TestHandler_SetDefaultModel_NotFound(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := postForm(e, "/models/nonexistent/set-default", url.Values{})
	assertCode(t, rec, http.StatusNotFound)
}

func TestHandler_CreateModel_PreservesExistingDefault(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	original, _ := llmConfigRepo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model")
	}
	form := url.Values{}
	form.Set("name", "Non-Default Agent")
	form.Set("provider", "anthropic")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")
	rec := postForm(e, "/models", form)
	assertCode(t, rec, http.StatusSeeOther)
	if stillDefault, _ := llmConfigRepo.GetDefault(ctx); stillDefault == nil || stillDefault.ID != original.ID {
		t.Error("expected original agent to still be default")
	}
}

func TestHandler_UpdateModel_HTMX(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatal("expected seeded default model")
	}
	form := url.Values{}
	form.Set("name", "Updated Agent Name")
	form.Set("provider", "anthropic")
	form.Set("model", "claude-opus-4-6")
	form.Set("max_tokens", "8192")
	form.Set("temperature", "0.5")
	form.Set("is_default", "on")
	rec := htmxPut(e, "/models/"+agent.ID, form)
	assertCode(t, rec, http.StatusOK)

	updated, _ := llmConfigRepo.GetByID(ctx, agent.ID)
	if updated.Name != "Updated Agent Name" {
		t.Errorf("expected name 'Updated Agent Name', got %q", updated.Name)
	}
	if updated.Model != "claude-opus-4-6" {
		t.Errorf("expected model 'claude-opus-4-6', got %q", updated.Model)
	}
}

func TestHandler_HomeRedirectsToChat(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := htmxGet(e, "/")
	assertCode(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); loc != "/chat" {
		t.Fatalf("expected redirect to /chat, got %q", loc)
	}
}

func TestHandler_Dashboard(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := htmxGet(e, "/dashboard")
	assertCode(t, rec, http.StatusOK)
}

func TestHandler_TasksPage_DoesNotContainChatRootSelector(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Tasks Page Project")

	rec := htmxGet(e, "/tasks?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.NotContains(t, body, `id="chat-page-root"`, "tasks page must not include chat page root")
	assert.NotContains(t, body, `document.getElementById('chat-page-root')`, "tasks page must not include chat page chat-root selectors")
}

func TestHandler_WorkerSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	p1 := createProject(t, h, "Worker Test Project 1")
	p2 := createProject(t, h, "Worker Test Project 2")

	rec := htmxGet(e, "/workers")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, p := range []*models.Project{p1, p2} {
		if c := strings.Count(body, fmt.Sprintf("id=\"project-row-%s\"", p.ID)); c != 1 {
			t.Errorf("expected project %s once, found %d", p.Name, c)
		}
	}
	if c := strings.Count(body, "id=\"project-stats-tbody\""); c != 1 {
		t.Errorf("expected 1 project-stats-tbody, found %d", c)
	}
	assertContains(t, rec, "Worker Capacity &amp; Utilization")
	assertContains(t, rec, "badge badge-primary badge-sm\">Global")
	assertNotContains(t, rec, "Global Worker Pool")
	assertContains(t, rec, "if (!window._workerSettingsHandlersBound)")
	assertContains(t, rec, "window._workerLimitSuppressDirtyRestoreUntil")
	assertContains(t, rec, "suppressDirtyRestore(2000)")
	assertContains(t, rec, "#worker-settings-content .worker-limit-input:focus")
	assertContains(t, rec, "#worker-settings-content .worker-limit-input:focus-visible")
	assertNotContains(t, rec, "input-warning")
}

func TestHandler_WorkersPage_DoesNotContainChatRootSelector(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	createProject(t, h, "Worker Test Project")

	rec := htmxGet(e, "/workers")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.NotContains(t, body, `id="chat-page-root"`, "workers page must not include chat page root")
	assert.NotContains(t, body, `document.getElementById('chat-page-root')`, "workers page must not include chat page chat-root selectors")
}

func TestHandler_UpdateWorkerSettings(t *testing.T) {
	t.Run("regular request redirects", func(t *testing.T) {
		_, e, _ := setupTestHandler(t)
		form := url.Values{}
		form.Set("max_workers", "3")
		rec := postForm(e, "/workers", form)
		assertCode(t, rec, http.StatusSeeOther)
		if loc := rec.Header().Get("Location"); loc != "/workers" {
			t.Errorf("expected redirect to /workers, got %q", loc)
		}
	})

	t.Run("HTMX request returns content", func(t *testing.T) {
		_, e, _ := setupTestHandler(t)
		form := url.Values{}
		form.Set("max_workers", "5")
		rec := htmxPost(e, "/workers", form)
		assertCode(t, rec, http.StatusOK)
	})

	t.Run("actually resizes worker pool", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		ctx := context.Background()
		h.workerSvc.Start(ctx)
		defer h.workerSvc.Stop()

		form := url.Values{}
		form.Set("max_workers", "3")
		rec := htmxPost(e, "/workers", form)
		assertCode(t, rec, http.StatusOK)

		if n := h.workerSvc.NumWorkers(); n != 3 {
			t.Errorf("expected worker pool to be resized to 3, got %d", n)
		}
		assertContains(t, rec, "Worker Capacity &amp; Utilization")
		assertContains(t, rec, "id=\"global-row\"")
		maxWorkers, _ := h.workerRepo.GetMaxWorkers(ctx)
		if maxWorkers != 3 {
			t.Errorf("expected max_workers in DB to be 3, got %d", maxWorkers)
		}
	})
}

func TestHandler_GlobalWorkerStats(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := htmxGet(e, "/workers/stats/global")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Worker Pool Size")
	assertContains(t, rec, "Tasks Running")
	assertContains(t, rec, "Queue")
	assertContains(t, rec, `hx-get="/workers/stats/global"`)
}

func TestHandler_ProjectWorkerStats(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	p1 := createProject(t, h, "Test Project 1")
	p2 := createProject(t, h, "Test Project 2")

	rec := htmxGet(e, "/workers/stats/projects")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Test Project 1")
	assertContains(t, rec, "Test Project 2")
	assertContains(t, rec, `hx-get="/workers/stats/projects"`)
	assertContains(t, rec, "id=\"global-row\"")
	assertContains(t, rec, "badge badge-primary badge-sm\">Global")
	assertContains(t, rec, "id=\"limit-input-global\"")
	body := rec.Body.String()
	for _, p := range []*models.Project{p1, p2} {
		if c := strings.Count(body, fmt.Sprintf("id=\"project-row-%s\"", p.ID)); c != 1 {
			t.Errorf("expected project %s once, found %d", p.Name, c)
		}
	}
	if c := strings.Count(body, "<tbody"); c != 1 {
		t.Errorf("expected 1 tbody, found %d", c)
	}
	if c := strings.Count(body, "id=\"global-row\""); c != 1 {
		t.Errorf("expected 1 global row, found %d", c)
	}
	assertContains(t, rec, fmt.Sprintf("id=\"limit-input-%s\"", p1.ID))
	assertContains(t, rec, "id=\"limit-cell-global\"")
	assertContains(t, rec, "data-project-id")
	assertContains(t, rec, "worker-limit-input")
	assertContains(t, rec, "worker-limit-form")
}

func TestHandler_UpdateProjectWorkerLimit(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	if project.MaxWorkers != nil {
		t.Errorf("expected new project to have no max_workers limit, got %d", *project.MaxWorkers)
	}
	path := "/workers/projects/" + project.ID + "/limit"

	postLimit := func(t *testing.T, limitPath, val string) *httptest.ResponseRecorder {
		form := url.Values{"max_workers": {val}}
		return htmxPost(e, limitPath, form)
	}

	t.Run("set per-project worker limit", func(t *testing.T) {
		rec := postLimit(t, path, "2")
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, "id=\"global-row\"")
		assertContains(t, rec, "id=\"limit-input-global\"")
		p, _ := h.projectSvc.GetByID(ctx, project.ID)
		if p.MaxWorkers == nil || *p.MaxWorkers != 2 {
			t.Errorf("expected max_workers=2, got %v", p.MaxWorkers)
		}
	})
	t.Run("remove per-project worker limit (set to 0)", func(t *testing.T) {
		rec := postLimit(t, path, "0")
		assertCode(t, rec, http.StatusOK)
		p, _ := h.projectSvc.GetByID(ctx, project.ID)
		if p.MaxWorkers != nil {
			t.Errorf("expected max_workers nil, got %d", *p.MaxWorkers)
		}
	})
	t.Run("enforce max limit of 10", func(t *testing.T) {
		rec := postLimit(t, path, "50")
		assertCode(t, rec, http.StatusOK)
		p, _ := h.projectSvc.GetByID(ctx, project.ID)
		if p.MaxWorkers == nil || *p.MaxWorkers != 10 {
			t.Errorf("expected max_workers=10, got %v", p.MaxWorkers)
		}
	})
	t.Run("project not found returns 404", func(t *testing.T) {
		rec := postLimit(t, "/workers/projects/nonexistent/limit", "2")
		assertCode(t, rec, http.StatusNotFound)
	})
}

func TestHandler_ListTasks_KanbanBoard(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	createTask(t, h, "default", "Active Task", func(tk *models.Task) { tk.Prompt = "Do something" })
	createTask(t, h, "default", "Backlog Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Prompt = "Do something later"
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Active Task")
	assertContains(t, rec, "Backlog Task")
	assertContains(t, rec, "kanban-board")
}

func TestHandler_UpdateTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Original Title", func(tk *models.Task) { tk.Category = models.CategoryBacklog })

	form := url.Values{}
	form.Set("title", "Updated Title")
	form.Set("category", "active")
	form.Set("priority", "1")
	form.Set("prompt", "Updated prompt")
	rec := htmxPut(e, "/tasks/"+task.ID, form)
	assertCode(t, rec, http.StatusOK)

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", updated.Title)
	}
	if updated.Category != models.CategoryActive {
		t.Errorf("expected category 'active', got %q", updated.Category)
	}
}

func TestHandler_UpdateTask_NonRunningOnly(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Test Task")

	form := url.Values{}
	form.Set("title", "Updated While Pending")
	form.Set("category", "active")
	form.Set("priority", "0")
	form.Set("prompt", "test")
	rec := htmxPut(e, "/tasks/"+task.ID, form)
	assertCode(t, rec, http.StatusOK)

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.Title != "Updated While Pending" {
		t.Errorf("expected title update to succeed for pending task")
	}
}

func TestHandler_UpdateTask_DuplicateTitle(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	createTask(t, h, "default", "Existing Task", func(tk *models.Task) { tk.Category = models.CategoryBacklog })
	task2 := createTask(t, h, "default", "Task to Update", func(tk *models.Task) { tk.Category = models.CategoryBacklog })

	form := url.Values{}
	form.Set("title", "Existing Task")
	form.Set("category", "backlog")
	form.Set("priority", "0")
	form.Set("prompt", "test prompt 2")
	rec := htmxPut(e, "/tasks/"+task2.ID, form)
	assertCode(t, rec, http.StatusConflict)
	assertContains(t, rec, "task with this name already exists")
}

func TestHandler_UpdateTaskCategory_RemovesFromCurrentView(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Test Task to Move")

	form := url.Values{}
	form.Set("category", "backlog")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/category?viewing=active", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "kanban-board")
	assertContains(t, rec, task.Title)

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.Category != models.CategoryBacklog {
		t.Errorf("expected category 'backlog', got %q", updated.Category)
	}
}

func TestHandler_UpdateTaskCategory_FromCompletedToActive(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	task := createTask(t, h, "default", "Completed Task To Reactivate", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
	})
	form := url.Values{}
	form.Set("category", "active")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/category", form)
	assertCode(t, rec, http.StatusOK)
}

func TestHandler_UpdateTaskCategory_RejectsNonScheduledTaskToScheduled(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Non-Scheduled Task", func(tk *models.Task) { tk.Category = models.CategoryBacklog })

	form := url.Values{}
	form.Set("category", "scheduled")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/category", form)
	assertCode(t, rec, http.StatusBadRequest)
	assertContains(t, rec, "no schedule")

	updatedTask, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updatedTask.Category != models.CategoryBacklog {
		t.Errorf("expected task to remain in backlog, got %s", updatedTask.Category)
	}
}

func TestHandler_UpdateTaskCategory_AllowsScheduledTaskToScheduled(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Scheduled Task", func(tk *models.Task) { tk.Category = models.CategoryBacklog })
	createSchedule(t, h, task.ID, time.Now().Add(24*time.Hour))

	form := url.Values{}
	form.Set("category", "scheduled")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/category", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "kanban-board")

	updatedTask, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updatedTask.Category != models.CategoryScheduled {
		t.Errorf("expected task to be in scheduled, got %s", updatedTask.Category)
	}
}

func TestHandler_UpdateTask_CategoryChangeFromCompletedToActive(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Completed Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
	})

	form := url.Values{}
	form.Set("title", task.Title)
	form.Set("category", "active")
	form.Set("prompt", task.Prompt)
	form.Set("priority", "0")
	rec := htmxPut(e, "/tasks/"+task.ID, form)
	assertCode(t, rec, http.StatusOK)

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.Category != models.CategoryActive {
		t.Errorf("expected category 'active', got %q", updated.Category)
	}
	if updated.Status != models.StatusPending {
		t.Errorf("expected status 'pending' after moving to active, got %q", updated.Status)
	}
}

func TestHandler_UpdateTaskStatus_DragDrop(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Test Task for Drag Drop")

	form := url.Values{}
	form.Set("status", "running")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/status", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, task.Title)
	assertContains(t, rec, "kanban-board")

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.Status != models.StatusRunning {
		t.Errorf("expected status 'running', got %q", updated.Status)
	}
}

func TestHandler_UpdateTaskStatus_MovesToRunning(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Test Completed Task", func(tk *models.Task) { tk.Status = models.StatusCompleted })

	form := url.Values{}
	form.Set("status", "running")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/status?viewing=active", form)
	assertCode(t, rec, http.StatusOK)

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.Status != models.StatusRunning {
		t.Errorf("expected status 'running', got %q", updated.Status)
	}
}

func TestHandler_DeleteAllTasksByCategory(t *testing.T) {
	cases := []struct {
		name     string
		category models.TaskCategory
		status   models.TaskStatus
		endpoint string
	}{
		{"completed", models.CategoryCompleted, models.StatusCompleted, "/tasks/completed"},
		{"backlog", models.CategoryBacklog, models.StatusPending, "/tasks/backlog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			ctx := context.Background()
			project1 := createProject(t, h, "Project 1")
			project2 := createProject(t, h, "Project 2")

			task1 := createTask(t, h, project1.ID, tc.name+" Task 1", func(tk *models.Task) { tk.Category = tc.category; tk.Status = tc.status })
			task2 := createTask(t, h, project1.ID, tc.name+" Task 2", func(tk *models.Task) { tk.Category = tc.category; tk.Status = tc.status })
			activeTask := createTask(t, h, project1.ID, "Active Task")
			otherProjectTask := createTask(t, h, project2.ID, tc.name+" Task P2", func(tk *models.Task) { tk.Category = tc.category; tk.Status = tc.status })

			rec := htmxDelete(e, tc.endpoint+"?project_id="+project1.ID)
			assertCode(t, rec, http.StatusOK)

			for _, id := range []string{task1.ID, task2.ID} {
				if got, _ := h.taskSvc.GetByID(ctx, id); got != nil {
					t.Errorf("expected task %s to be deleted", id)
				}
			}
			if got, _ := h.taskSvc.GetByID(ctx, activeTask.ID); got == nil {
				t.Error("expected active task to still exist")
			}
			if got, _ := h.taskSvc.GetByID(ctx, otherProjectTask.ID); got == nil {
				t.Error("expected other project task to still exist")
			}
		})
	}
}

func TestHandler_ActivateAllBacklogTasks(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project1 := createProject(t, h, "Test Project 1")
	bt1 := createTask(t, h, project1.ID, "Backlog Task 1", func(tk *models.Task) { tk.Category = models.CategoryBacklog })
	bt2 := createTask(t, h, project1.ID, "Backlog Task 2", func(tk *models.Task) { tk.Category = models.CategoryBacklog })

	rec := htmxPost(e, "/tasks/backlog/activate?project_id="+project1.ID, nil)
	assertCode(t, rec, http.StatusOK)

	for _, id := range []string{bt1.ID, bt2.ID} {
		task, _ := h.taskSvc.GetByID(ctx, id)
		if task == nil {
			t.Fatalf("expected task %s to exist", id)
		}
		if task.Category != models.CategoryActive {
			t.Errorf("task %s: expected category active, got %s", id, task.Category)
		}
		if task.Status != models.StatusPending {
			t.Errorf("task %s: expected status pending, got %s", id, task.Status)
		}
	}
}

func TestHandler_DeleteTask_HTMX_UpdatesKanbanBoard(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	ct1 := createTask(t, h, project.ID, "Completed Task 1", func(tk *models.Task) { tk.Category = models.CategoryCompleted; tk.Status = models.StatusCompleted })
	ct2 := createTask(t, h, project.ID, "Completed Task 2", func(tk *models.Task) { tk.Category = models.CategoryCompleted; tk.Status = models.StatusCompleted })

	rec := htmxDelete(e, "/tasks/"+ct1.ID)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "kanban-board")
	assertContains(t, rec, "Completed Task 2")
	assertNotContains(t, rec, "Completed Task 1")

	if deleted, _ := h.taskSvc.GetByID(ctx, ct1.ID); deleted != nil {
		t.Error("expected task to be deleted")
	}
	if remaining, _ := h.taskSvc.GetByID(ctx, ct2.ID); remaining == nil {
		t.Error("expected remaining task to still exist")
	}
}

func TestHandler_DeleteTask_FromDetailPage_RedirectsToList(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "test-agent"
		a.Provider = "anthropic"
		a.Model = "claude-3-5-sonnet-20241022"
		a.IsDefault = false
	})
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Task To Delete", func(tk *models.Task) { tk.Category = models.CategoryBacklog; tk.Status = models.StatusCompleted })
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "test prompt"
		ex.Output = "test output"
	})
	runAt := time.Now().Add(24 * time.Hour)
	createSchedule(t, h, task.ID, runAt, func(s *models.Schedule) { s.NextRun = &runAt })

	rec := htmxDelete(e, "/tasks/"+task.ID+"?redirect=list")
	assertCode(t, rec, http.StatusOK)

	expectedRedirect := "/tasks?project_id=" + project.ID
	if hxRedirect := rec.Header().Get("HX-Redirect"); hxRedirect != expectedRedirect {
		t.Errorf("expected HX-Redirect=%q, got %q", expectedRedirect, hxRedirect)
	}
	if deleted, _ := h.taskSvc.GetByID(ctx, task.ID); deleted != nil {
		t.Error("expected task to be deleted")
	}
	if execs, err := h.execRepo.ListByTask(ctx, task.ID); err != nil {
		t.Fatalf("list executions: %v", err)
	} else if len(execs) != 0 {
		t.Errorf("expected 0 executions after delete, got %d", len(execs))
	}
	if schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID); err != nil {
		t.Fatalf("list schedules: %v", err)
	} else if len(schedules) != 0 {
		t.Errorf("expected 0 schedules after delete, got %d", len(schedules))
	}
}

func TestHandler_ViewSchedule_RecurringTasks(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Daily Recurring Task", func(tk *models.Task) {
		tk.Category = models.CategoryScheduled
		tk.Prompt = "test daily task"
	})
	createSchedule(t, h, task.ID, time.Now(), func(s *models.Schedule) {
		s.RepeatType = models.RepeatDaily
	})

	rec := htmxGet(e, "/schedule?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Daily Recurring Task")
	assertContains(t, rec, "Schedule")
	assertContains(t, rec, "Today")
}

func TestHandler_ViewSchedule_NewTaskDialogRepeatIntervalControls(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	rec := htmxGet(e, "/schedule?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	if !strings.Contains(body, `id="sched-repeat-interval-container"`) {
		t.Fatal("expected schedule create dialog to render repeat interval container")
	}
	if strings.Contains(body, `id="sched-repeat-interval-container" style="display: none;"`) {
		t.Fatal("expected schedule create dialog default repeat interval row to be visible for Daily repeat")
	}
	if !strings.Contains(body, `id="sched-repeat-interval-input"`) {
		t.Fatal("expected schedule create dialog to render repeat interval input")
	}
	if !strings.Contains(body, `name="repeat_interval"`) {
		t.Fatal("expected schedule create dialog to submit repeat_interval")
	}
	if !strings.Contains(body, `window.updateScheduleCreateRepeatInterval`) {
		t.Fatal("expected schedule create dialog repeat interval behavior hook")
	}
	if !strings.Contains(body, `Repeat interval must be a whole number of at least 1`) {
		t.Fatal("expected schedule create dialog interval validation message")
	}
	if !strings.Contains(body, `<option value="daily" selected>Daily</option>`) {
		t.Fatal("expected schedule create dialog default repeat selection to be Daily")
	}
	if !strings.Contains(body, `repeatTypeSelect.value = 'daily';`) {
		t.Fatal("expected schedule create dialog reset behavior to restore repeat type to Daily")
	}
	if !strings.Contains(body, `class="grid grid-cols-1 gap-3 md:grid-cols-2"`) {
		t.Fatal("expected schedule configuration controls to use responsive balanced grid layout")
	}
}

func TestHandler_ViewSchedule_WeekNavigation(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Future Task", func(tk *models.Task) {
		tk.Category = models.CategoryScheduled
		tk.Prompt = "test future task"
	})
	createSchedule(t, h, task.ID, time.Now().AddDate(0, 0, 14))
	base := "/schedule?project_id=" + project.ID

	// Current week should not show the future task
	rec := htmxGet(e, base)
	assertCode(t, rec, http.StatusOK)
	assertNotContains(t, rec, "Future Task")
	assertContains(t, rec, "Previous Week")
	assertContains(t, rec, "Next Week")

	// Week +2 should show the task
	rec = htmxGet(e, base+"&week=2")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Future Task")
	assertContains(t, rec, "This Week")

	// Week -1 should show "This Week" button
	rec = htmxGet(e, base+"&week=-1")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "This Week")

	// HTMX request returns exactly one schedule-content div
	rec = htmxGet(e, base+"&week=1")
	assertCode(t, rec, http.StatusOK)
	if c := strings.Count(rec.Body.String(), `id="schedule-content"`); c != 1 {
		t.Errorf("expected 1 schedule-content div, got %d", c)
	}
}

func TestHandler_ViewSchedule_WeekNavigation_TimelineMarkup(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	for _, offset := range []int{-2, -1, 0, 1, 2} {
		t.Run(fmt.Sprintf("week=%d", offset), func(t *testing.T) {
			rec := htmxGet(e, fmt.Sprintf("/schedule?project_id=%s&week=%d", project.ID, offset))
			assertCode(t, rec, http.StatusOK)
			assertContains(t, rec, `id="schedule-timeline-container"`)
			assertContains(t, rec, "data-date=")
			assertContains(t, rec, "data-hour=")
			assertContains(t, rec, "_schedLoadRegistered")
			body := rec.Body.String()
			if !strings.Contains(body, "Sun") && !strings.Contains(body, "Mon") {
				t.Error("missing day header labels")
			}
			if !strings.Contains(body, "AM") && !strings.Contains(body, "PM") {
				t.Error("missing time slot labels")
			}
		})
	}
}

func TestHandler_ViewSchedule_TimelineTracerUsesAccentColor(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	rec := htmxGet(e, "/schedule?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assertContains(t, rec, `id="timeline-before"`)
	assertContains(t, rec, `id="timeline-current"`)
	if !strings.Contains(body, `border-top: 2px dashed var(--ov-link-color);`) {
		t.Error("expected dashed timeline tracer to use shared accent token var(--ov-link-color)")
	}
	if c := strings.Count(body, `style="background-color: var(--ov-link-color);"`); c < 3 {
		t.Errorf("expected timeline dots/line to use accent token var(--ov-link-color) in 3 elements, got %d", c)
	}
	if strings.Contains(body, "#166534") || strings.Contains(body, "bg-green-800") {
		t.Error("schedule timeline tracer must not use legacy green styles")
	}
}

func TestHandler_ViewSchedule_NoFlickerOnHTMXNav(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")
	base := "/schedule?project_id=" + project.ID

	// Full page load
	rec := htmxGet(e, base)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `overflow-y-auto opacity-0"`)

	// HTMX partial swap
	rec = htmxGet(e, base+"&week=1")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `overflow-y-auto opacity-0"`)
	assertContains(t, rec, `id="schedule-timeline-container"`)
}

func TestHandler_Schedule_NoViewportHeightOverflow(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")
	base := "/schedule?project_id=" + project.ID

	rec := htmxGet(e, base)
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	// The schedule-content root must use flex layout to fill available space
	// instead of a viewport-relative calc() height that causes outer scrollbar.
	if !strings.Contains(body, `id="schedule-content"`) {
		t.Fatal("missing schedule-content element")
	}
	if strings.Contains(body, "100vh") {
		t.Error("schedule page must not use viewport-relative height (100vh); use flex layout instead")
	}
	// The timeline container should use flex-1 to fill remaining space
	if !strings.Contains(body, "flex-1 min-h-0") {
		t.Error("schedule-timeline-container should use flex-1 min-h-0 for proper overflow")
	}
}

func TestHandler_RescheduleTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Task to Reschedule", func(tk *models.Task) { tk.Category = models.CategoryScheduled })
	originalTime := time.Date(2026, 2, 22, 10, 30, 0, 0, time.UTC)
	schedule := createSchedule(t, h, task.ID, originalTime)

	form := url.Values{}
	form.Set("new_date", "2026-02-23")
	form.Set("hour", "14")
	rec := htmxPatch(e, "/schedules/"+schedule.ID+"/reschedule", form)
	assertCode(t, rec, http.StatusNoContent)

	updated, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("fetch schedule: %v", err)
	}

	expectedTime := time.Date(2026, 2, 23, 14, 30, 0, 0, time.Local)
	if !updated.RunAt.Equal(expectedTime) {
		t.Errorf("RunAt: got %v, want %v", updated.RunAt, expectedTime)
	}
	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set")
	}
	if !updated.NextRun.Equal(expectedTime) {
		t.Errorf("NextRun: got %v, want %v", *updated.NextRun, expectedTime)
	}

	// Verify UTC storage after DB round-trip
	zone, _ := updated.RunAt.Zone()
	if zone != "UTC" {
		t.Errorf("RunAt zone: got %s, want UTC", zone)
	}
	if h := updated.RunAt.Local().Hour(); h != 14 {
		t.Errorf("local hour: got %d, want 14", h)
	}
}

func TestHandler_RescheduleTask_Errors(t *testing.T) {
	cases := []struct {
		name       string
		date       string
		hour       string
		setupSched bool
		wantCode   int
	}{
		{"invalid_date", "invalid-date", "10", false, http.StatusBadRequest},
		{"invalid_hour", "2026-02-23", "25", true, http.StatusBadRequest},
		{"not_found", "2026-02-23", "10", false, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			schedID := "nonexistent-schedule"
			if tc.setupSched {
				project := createProject(t, h, "Test")
				task := createTask(t, h, project.ID, "Test Task", func(tk *models.Task) { tk.Category = models.CategoryScheduled })
				sched := createSchedule(t, h, task.ID, time.Now())
				schedID = sched.ID
			}
			form := url.Values{}
			form.Set("new_date", tc.date)
			form.Set("hour", tc.hour)
			rec := htmxPatch(e, "/schedules/"+schedID+"/reschedule", form)
			assertCode(t, rec, tc.wantCode)
		})
	}
}

func TestHandler_UpdateSchedule(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Task to Update Schedule", func(tk *models.Task) { tk.Category = models.CategoryScheduled })
	originalTime := time.Date(2026, 2, 22, 10, 30, 0, 0, time.UTC)
	schedule := createSchedule(t, h, task.ID, originalTime)

	form := url.Values{}
	form.Set("run_at", "2099-06-15T14:00")
	form.Set("repeat_type", "daily")
	form.Set("repeat_interval", "2")
	rec := htmxPut(e, "/schedules/"+schedule.ID, form)
	assertCode(t, rec, http.StatusOK)

	updatedSchedule, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated schedule: %v", err)
	}

	expectedTime := time.Date(2099, 6, 15, 14, 0, 0, 0, time.Local)
	if !updatedSchedule.RunAt.Equal(expectedTime) {
		t.Errorf("expected RunAt %v, got %v", expectedTime, updatedSchedule.RunAt)
	}
	if updatedSchedule.RepeatType != models.RepeatDaily {
		t.Errorf("expected RepeatType %v, got %v", models.RepeatDaily, updatedSchedule.RepeatType)
	}
	if updatedSchedule.RepeatInterval != 2 {
		t.Errorf("expected RepeatInterval 2, got %d", updatedSchedule.RepeatInterval)
	}
	if updatedSchedule.NextRun == nil {
		t.Error("expected NextRun to be set")
	} else if !updatedSchedule.NextRun.Equal(expectedTime) {
		t.Errorf("expected NextRun %v, got %v", expectedTime, *updatedSchedule.NextRun)
	}
}

func TestHandler_UpdateSchedule_Errors(t *testing.T) {
	cases := []struct {
		name       string
		runAt      string
		setupSched bool
		wantCode   int
	}{
		{"invalid_date", "invalid-date-time", true, http.StatusBadRequest},
		{"not_found", "2026-02-25T14:00", false, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			schedID := "nonexistent-schedule"
			if tc.setupSched {
				project := createProject(t, h, "Test")
				task := createTask(t, h, project.ID, "Task", func(tk *models.Task) { tk.Category = models.CategoryScheduled })
				sched := createSchedule(t, h, task.ID, time.Now())
				schedID = sched.ID
			}
			form := url.Values{}
			form.Set("run_at", tc.runAt)
			form.Set("repeat_type", "daily")
			form.Set("repeat_interval", "1")
			rec := htmxPut(e, "/schedules/"+schedID, form)
			assertCode(t, rec, tc.wantCode)
		})
	}
}

func TestHandler_UploadMultipleAttachments(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Task with Attachments")

	// Create multipart form with multiple files
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add first file
	file1, err := writer.CreateFormFile("files", "test1.txt")
	if err != nil {
		t.Fatalf("failed to create form file 1: %v", err)
	}
	file1.Write([]byte("test file 1 content"))

	// Add second file
	file2, err := writer.CreateFormFile("files", "test2.txt")
	if err != nil {
		t.Fatalf("failed to create form file 2: %v", err)
	}
	file2.Write([]byte("test file 2 content"))

	// Add third file
	file3, err := writer.CreateFormFile("files", "test3.txt")
	if err != nil {
		t.Fatalf("failed to create form file 3: %v", err)
	}
	file3.Write([]byte("test file 3 content"))

	writer.Close()

	// Create request
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId/attachments")
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.UploadAttachment(c); err != nil {
		t.Fatalf("UploadAttachment failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
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
	filenames := make(map[string]bool)
	for _, att := range attachments {
		filenames[att.FileName] = true
	}

	if !filenames["test1.txt"] {
		t.Error("expected test1.txt to be uploaded")
	}
	if !filenames["test2.txt"] {
		t.Error("expected test2.txt to be uploaded")
	}
	if !filenames["test3.txt"] {
		t.Error("expected test3.txt to be uploaded")
	}

	// Verify response HTML contains only the list portion, not the "Add Attachment" button
	responseBody := rec.Body.String()
	if !strings.Contains(responseBody, `id="attachment-list"`) {
		t.Error("expected response to contain attachment-list div")
	}
	if strings.Contains(responseBody, `id="add-attachment-btn"`) {
		t.Error("expected response to NOT contain add-attachment-btn (should only render list portion)")
	}
	if strings.Contains(responseBody, "Add Attachment") {
		t.Error("expected response to NOT contain 'Add Attachment' button text (should only render list portion)")
	}
}

func TestHandler_AttachmentPersistenceCompletedTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Completed Task with Attachments", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
	})

	// Upload an attachment
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	file, err := writer.CreateFormFile("files", "completed-task-file.txt")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	file.Write([]byte("test content for completed task"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId/attachments")
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.UploadAttachment(c); err != nil {
		t.Fatalf("UploadAttachment failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Verify attachment was created
	attachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Errorf("expected 1 attachment after upload, got %d", len(attachments))
	}

	// Now simulate reopening the task - fetch it again via the GetTask handler
	req2 := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()

	c2 := e.NewContext(req2, rec2)
	c2.SetPath("/tasks/:taskId")
	c2.SetParamNames("taskId")
	c2.SetParamValues(task.ID)

	if err := h.GetTask(c2); err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	// Verify attachments are still present when re-fetching
	attachmentsAfterReopen, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list attachments after reopen: %v", err)
	}
	if len(attachmentsAfterReopen) != 1 {
		t.Errorf("expected 1 attachment after reopening, got %d - attachments vanished!", len(attachmentsAfterReopen))
	}

	// Verify the response HTML contains the attachment
	responseBody := rec2.Body.String()
	if !strings.Contains(responseBody, "completed-task-file.txt") {
		t.Error("expected response to contain the attachment filename")
	}
}

func TestHandler_DeleteAttachment(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Task with Attachment to Delete")
	tempUploadsDir := t.TempDir()
	attachmentPath := filepath.Join(tempUploadsDir, task.ID, "test-delete.txt")

	// Create an attachment directly
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "test-delete.txt",
		FilePath:  attachmentPath,
		MediaType: "text/plain",
		FileSize:  100,
	}
	if err := h.attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("failed to create attachment: %v", err)
	}

	// Create the file on disk
	taskDir := filepath.Dir(attachmentPath)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}
	if err := os.WriteFile(attachment.FilePath, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create DELETE request
	req := httptest.NewRequest(http.MethodDelete, "/attachments/"+attachment.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetPath("/attachments/:id")
	c.SetParamNames("id")
	c.SetParamValues(attachment.ID)

	if err := h.DeleteAttachment(c); err != nil {
		t.Fatalf("DeleteAttachment failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Verify attachment was deleted from database
	attachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}

	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments after deletion, got %d", len(attachments))
	}

	// Verify response HTML contains only the list portion, not the "Add Attachment" button
	responseBody := rec.Body.String()
	if !strings.Contains(responseBody, `id="attachment-list"`) {
		t.Error("expected response to contain attachment-list div")
	}
	if strings.Contains(responseBody, `id="add-attachment-btn"`) {
		t.Error("expected response to NOT contain add-attachment-btn (should only render list portion)")
	}
	if strings.Contains(responseBody, "Add Attachment") {
		t.Error("expected response to NOT contain 'Add Attachment' button text (should only render list portion)")
	}
}

func TestHandler_CreateTaskWithTag(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	for _, tc := range []struct {
		title, tag string
		want       models.TaskTag
	}{
		{"Feature Task", "feature", models.TagFeature},
		{"Bug Task", "bug", models.TagBug},
	} {
		form := url.Values{}
		form.Set("title", tc.title)
		form.Set("category", "active")
		form.Set("priority", "1")
		form.Set("prompt", "test")
		form.Set("tag", tc.tag)
		rec := htmxPost(e, "/tasks?project_id=default", form)
		assertCode(t, rec, http.StatusOK)
	}

	tasks, _ := h.taskSvc.ListByProject(ctx, "default", "")
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		switch task.Title {
		case "Feature Task":
			if task.Tag != models.TagFeature {
				t.Errorf("expected Tag=feature, got %q", task.Tag)
			}
		case "Bug Task":
			if task.Tag != models.TagBug {
				t.Errorf("expected Tag=bug, got %q", task.Tag)
			}
		}
	}
}

func TestHandler_UpdateTaskTag(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Test Task", func(tk *models.Task) { tk.Tag = models.TagNone })

	baseForm := func(tag string) url.Values {
		form := url.Values{}
		form.Set("title", task.Title)
		form.Set("category", string(task.Category))
		form.Set("priority", "0")
		form.Set("prompt", task.Prompt)
		form.Set("tag", tag)
		return form
	}

	for _, tc := range []struct {
		tag  string
		want models.TaskTag
	}{
		{"bug", models.TagBug},
		{"feature", models.TagFeature},
		{"", models.TagNone},
	} {
		rec := htmxPut(e, "/tasks/"+task.ID, baseForm(tc.tag))
		assertCode(t, rec, http.StatusOK)
		updated, _ := h.taskSvc.GetByID(ctx, task.ID)
		if updated.Tag != tc.want {
			t.Errorf("after setting tag=%q: expected %q, got %q", tc.tag, tc.want, updated.Tag)
		}
	}
}

func createAlert(t *testing.T, h *Handler, projectID, title string) *models.Alert {
	t.Helper()
	a := &models.Alert{
		ProjectID: projectID,
		Type:      models.AlertTaskFailed,
		Severity:  models.SeverityError,
		Title:     title,
		Message:   "Test message",
	}
	if err := h.alertSvc.Create(context.Background(), a); err != nil {
		t.Fatalf("create alert: %v", err)
	}
	return a
}

func assertAlertUpdate(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if hx := rec.Header().Get("HX-Trigger"); hx != "alertUpdate" {
		t.Errorf("expected HX-Trigger 'alertUpdate', got %q", hx)
	}
}

func TestHandler_MarkAlertRead_TriggersAlertUpdate(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")
	alert := createAlert(t, h, project.ID, "Test Alert")

	rec := htmxPost(e, "/alerts/"+alert.ID+"/read?project_id="+project.ID, nil)
	assertCode(t, rec, http.StatusOK)
	assertAlertUpdate(t, rec)
	alerts, _ := h.alertSvc.ListByProject(context.Background(), project.ID, 100)
	if len(alerts) != 1 || !alerts[0].IsRead {
		t.Error("expected alert to be marked as read")
	}
}

func TestHandler_MarkAllAlertsRead_TriggersAlertUpdate(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")
	for i := 1; i <= 3; i++ {
		createAlert(t, h, project.ID, fmt.Sprintf("Test Alert %d", i))
	}

	rec := htmxPost(e, "/alerts/read-all?project_id="+project.ID, nil)
	assertCode(t, rec, http.StatusOK)
	assertAlertUpdate(t, rec)
	count, _ := h.alertSvc.CountUnread(context.Background(), project.ID)
	if count != 0 {
		t.Errorf("expected 0 unread, got %d", count)
	}
}

func TestHandler_DeleteAlert_TriggersAlertUpdate(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	a1 := createAlert(t, h, project.ID, "Test Alert 1")
	a2 := createAlert(t, h, project.ID, "Test Alert 2")

	// Delete first alert
	rec := htmxDelete(e, "/alerts/"+a1.ID+"?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)
	assertAlertUpdate(t, rec)
	assertContains(t, rec, "Test Alert 2")
	assertNotContains(t, rec, "Test Alert 1")
	alerts, _ := h.alertSvc.ListByProject(ctx, project.ID, 100)
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert, got %d", len(alerts))
	}

	// Delete second alert - should show empty state
	rec2 := htmxDelete(e, "/alerts/"+a2.ID+"?project_id="+project.ID)
	assertCode(t, rec2, http.StatusOK)
	assertContains(t, rec2, "No alerts")
	count, _ := h.alertSvc.CountUnread(ctx, project.ID)
	if count != 0 {
		t.Errorf("expected 0 unread, got %d", count)
	}
}

func TestHandler_DeleteAllAlerts_TriggersAlertUpdate(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	for i := 1; i <= 5; i++ {
		createAlert(t, h, project.ID, fmt.Sprintf("Test Alert %d", i))
	}

	rec := htmxDelete(e, "/alerts?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)
	assertAlertUpdate(t, rec)
	alerts, _ := h.alertSvc.ListByProject(ctx, project.ID, 100)
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(alerts))
	}
	count, _ := h.alertSvc.CountUnread(ctx, project.ID)
	if count != 0 {
		t.Errorf("expected 0 unread, got %d", count)
	}
}

// TestHandler_RescheduleTask_SubDailyPreservesRunAt verifies that rescheduling a
// sub-daily schedule (hours, minutes, seconds) only updates NextRun, preserving
// the original RunAt. This prevents the calendar display from shifting its start
// point when a sub-daily task is drag-and-dropped to a new time slot.
func TestHandler_RescheduleTask_SubDailyPreservesRunAt(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Hourly Task", func(tk *models.Task) { tk.Category = models.CategoryScheduled })

	originalRunAt := time.Date(2026, 2, 22, 10, 30, 0, 0, time.UTC)
	nextRun := time.Date(2026, 2, 25, 15, 30, 0, 0, time.UTC)
	schedule := createSchedule(t, h, task.ID, originalRunAt, func(s *models.Schedule) {
		s.NextRun = &nextRun
		s.RepeatType = models.RepeatHours
	})

	form := url.Values{}
	form.Set("new_date", "2026-06-26") // Future date to avoid adjustment
	form.Set("hour", "9")
	rec := htmxPatch(e, "/schedules/"+schedule.ID+"/reschedule", form)
	assertCode(t, rec, http.StatusNoContent)

	updated, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated schedule: %v", err)
	}
	if !updated.RunAt.Equal(originalRunAt) {
		t.Errorf("sub-daily RunAt should be preserved: expected %v, got %v", originalRunAt, updated.RunAt)
	}
	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set")
	}
	expectedNextRun := time.Date(2026, 6, 26, 9, 30, 0, 0, time.Local).UTC()
	if !updated.NextRun.Equal(expectedNextRun) {
		t.Errorf("expected NextRun %v, got %v (local: %v)", expectedNextRun, *updated.NextRun, updated.NextRun.Local())
	}
}

func TestHandler_RescheduleTask_DailyUpdatesBothRunAtAndNextRun(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Daily Task", func(tk *models.Task) { tk.Category = models.CategoryScheduled })

	originalRunAt := time.Date(2026, 2, 22, 10, 30, 0, 0, time.UTC)
	schedule := createSchedule(t, h, task.ID, originalRunAt, func(s *models.Schedule) {
		s.RepeatType = models.RepeatDaily
	})

	form := url.Values{}
	form.Set("new_date", "2026-06-26") // Future date to avoid adjustment
	form.Set("hour", "9")
	rec := htmxPatch(e, "/schedules/"+schedule.ID+"/reschedule", form)
	assertCode(t, rec, http.StatusNoContent)

	updated, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated schedule: %v", err)
	}

	expectedTime := time.Date(2026, 6, 26, 9, 30, 0, 0, time.Local).UTC()
	if !updated.RunAt.Equal(expectedTime) {
		t.Errorf("daily RunAt should be updated: expected %v, got %v", expectedTime, updated.RunAt)
	}
	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set")
	}
	if !updated.NextRun.Equal(expectedTime) {
		t.Errorf("expected NextRun %v, got %v", expectedTime, *updated.NextRun)
	}
}

func TestHandler_Analytics_FullPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	req := httptest.NewRequest(http.MethodGet, "/analytics?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "OpenVibely")
	assertContains(t, rec, "Analytics Dashboard")
	assertContains(t, rec, `data-project-id="`+project.ID+`"`)
	assertContains(t, rec, "dataset.projectId")
	assertNotContains(t, rec, "templ.JSONString")
	assertContains(t, rec, "flex items-center justify-between gap-3")
	assertContains(t, rec, "badge badge-primary shrink-0 inline-flex items-center justify-center whitespace-nowrap h-auto min-h-6 px-3 py-1 leading-none text-center")
}

func TestHandler_Analytics_ExecutionTimeDisplayedInMinutes(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	req := httptest.NewRequest(http.MethodGet, "/analytics?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertCode(t, rec, http.StatusOK)

	body := rec.Body.String()
	if strings.Contains(body, `label: &#39;Avg Time (ms)&#39;`) || strings.Contains(body, `label: 'Avg Time (ms)'`) {
		t.Error("chart still uses milliseconds label")
	}
	assertContains(t, rec, "Avg Time (min)")
	assertContains(t, rec, "function formatDuration")
	assertContains(t, rec, "/ 60000")
}

func TestHandler_Analytics_HTMX(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	rec := htmxGet(e, "/analytics?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Analytics Dashboard")
	assertNotContains(t, rec, "<!DOCTYPE html>")
	assertContains(t, rec, `data-project-id="`+project.ID+`"`)
}

func TestHandler_Analytics_APIEndpoints_ReturnJSON(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Provider = "anthropic"; a.Model = "claude-3-5-sonnet-20241022" })
	task := createTask(t, h, project.ID, "Analytics Test Task", func(tk *models.Task) { tk.Category = models.CategoryBacklog })
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.PromptSent = "test prompt" })
	if err := h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output", "", 100, 5000); err != nil {
		t.Fatal(err)
	}

	endpoints := []string{
		"/api/analytics/success-failure-rates", "/api/analytics/avg-execution-time-by-task",
		"/api/analytics/avg-execution-time-by-agent", "/api/analytics/execution-trends-by-hour",
		"/api/analytics/agent-usage-by-project", "/api/analytics/most-frequent-tasks",
		"/api/analytics/failed-task-patterns",
	}
	for _, ep := range endpoints {
		rec := htmxGet(e, ep+"?project_id="+project.ID)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", ep, rec.Code)
			continue
		}
		if body := rec.Body.String(); !strings.HasPrefix(strings.TrimSpace(body), "[") {
			t.Errorf("%s: expected JSON array, got: %s", ep, body[:min(len(body), 50)])
		}
	}
}

func TestHandler_CreateProject_DirectoryVariations(t *testing.T) {
	cases := []struct {
		name           string
		repoPath       func(t *testing.T) string
		createDir      bool
		wantCode       int
		checkDirExists *bool
		wantBody       string
	}{
		{"creates_nested_dir", func(t *testing.T) string { return filepath.Join(t.TempDir(), "new", "nested") }, true, http.StatusSeeOther, boolPtr(true), ""},
		{"disabled_no_create", func(t *testing.T) string { return filepath.Join(t.TempDir(), "should-not-exist") }, false, http.StatusSeeOther, boolPtr(false), ""},
		{"relative_path_rejected", func(t *testing.T) string { return "relative/path" }, true, http.StatusBadRequest, nil, "absolute path"},
		{"empty_path_ok", func(t *testing.T) string { return "" }, true, http.StatusSeeOther, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e, _ := setupTestHandler(t)
			path := tc.repoPath(t)
			form := url.Values{}
			form.Set("name", "Project "+tc.name)
			form.Set("repo_path", path)
			if tc.createDir {
				form.Set("create_directory", "true")
			}
			rec := postForm(e, "/projects", form)
			assertCode(t, rec, tc.wantCode)
			if tc.checkDirExists != nil && path != "" {
				_, err := os.Stat(path)
				exists := err == nil
				if exists != *tc.checkDirExists {
					t.Errorf("directory exists=%v, want %v", exists, *tc.checkDirExists)
				}
			}
			if tc.wantBody != "" {
				assertContains(t, rec, tc.wantBody)
			}
		})
	}
}

func TestHandler_CreateProject_LocalPathDisabledRejectsLocalSource(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "false")
	t.Setenv("ENVIRONMENT", "production")

	h, e, _ := setupTestHandler(t)
	h.SetLocalRepoPathEnabled(false)
	form := url.Values{}
	form.Set("name", "Local Disabled Project")
	form.Set("repo_source", "local")
	form.Set("repo_path", "/tmp/local-disabled")

	rec := postForm(e, "/projects", form)
	assertCode(t, rec, http.StatusBadRequest)
	assertContains(t, rec, "Local repository paths are disabled in this environment")
}

func TestHandler_UpdateProject_LegacyLocalPreservedWhenLocalPathDisabled(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "false")
	t.Setenv("ENVIRONMENT", "production")

	h, e, _ := setupTestHandler(t)
	h.SetLocalRepoPathEnabled(false)
	p := &models.Project{
		Name:        "Legacy Local Project",
		Description: "legacy",
		RepoPath:    "/tmp/legacy-local-path",
		RepoURL:     "",
	}
	if err := h.projectSvc.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Legacy Local Project Updated")
	form.Set("description", "updated description")
	form.Set("repo_source", "local")

	req := httptest.NewRequest(http.MethodPut, "/projects/"+p.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertCode(t, rec, http.StatusSeeOther)

	updated, err := h.projectSvc.GetByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("fetch project: %v", err)
	}
	if updated.RepoPath != "/tmp/legacy-local-path" {
		t.Fatalf("expected legacy local repo_path preserved, got %q", updated.RepoPath)
	}
	if updated.RepoURL != "" {
		t.Fatalf("expected repo_url to remain empty for legacy local project, got %q", updated.RepoURL)
	}
}

func TestHandler_UpdateProject_LocalPathDisabledRejectsSwitchFromGitHub(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "false")
	t.Setenv("ENVIRONMENT", "production")

	h, e, _ := setupTestHandler(t)
	h.SetLocalRepoPathEnabled(false)
	p := &models.Project{
		Name:        "GitHub Project",
		Description: "github",
		RepoPath:    "/tmp/github-project",
		RepoURL:     "https://github.com/openvibely/openvibely",
	}
	if err := h.projectSvc.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	form := url.Values{}
	form.Set("name", "GitHub Project")
	form.Set("description", "try switching to local")
	form.Set("repo_source", "local")
	form.Set("repo_path", "/tmp/disallowed-local")

	req := httptest.NewRequest(http.MethodPut, "/projects/"+p.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertCode(t, rec, http.StatusBadRequest)
	assertContains(t, rec, "Local repository paths are disabled in this environment")
}

func TestHandler_CreateProject_RepoPathPreserved(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	specifiedPath := "/Users/testuser/go/src/github.com/myorg/my-project"

	form := url.Values{}
	form.Set("name", "RepoPath Test Project")
	form.Set("description", "Testing repo_path preservation")
	form.Set("repo_path", specifiedPath)

	rec := postForm(e, "/projects", form)
	assertCode(t, rec, http.StatusSeeOther)
	if rec.Header().Get("Location") == "" {
		t.Fatal("expected Location header in redirect")
	}

	ctx := context.Background()
	projects, err := h.projectSvc.List(ctx)
	if err != nil {
		t.Fatalf("List projects: %v", err)
	}
	var found *models.Project
	for i := range projects {
		if projects[i].Name == "RepoPath Test Project" {
			found = &projects[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find created project")
	}
	if found.RepoPath != specifiedPath {
		t.Errorf("expected RepoPath=%q, got %q", specifiedPath, found.RepoPath)
	}
}

func TestHandler_CreateProject_RepoPathTildeExpanded(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home dir unavailable")
	}

	form := url.Values{}
	form.Set("name", "RepoPath Tilde Project")
	form.Set("description", "Testing repo_path tilde expansion")
	form.Set("repo_path", "~/go/src/github.com/claude-code")

	rec := postForm(e, "/projects", form)
	assertCode(t, rec, http.StatusSeeOther)

	ctx := context.Background()
	projects, err := h.projectSvc.List(ctx)
	if err != nil {
		t.Fatalf("List projects: %v", err)
	}
	var found *models.Project
	for i := range projects {
		if projects[i].Name == "RepoPath Tilde Project" {
			found = &projects[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find created project")
	}
	want := filepath.Join(home, "go", "src", "github.com", "claude-code")
	if found.RepoPath != want {
		t.Errorf("expected RepoPath=%q, got %q", want, found.RepoPath)
	}
}

func TestHandler_CreateProject_CreateDirectoryExistingPath(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	form := url.Values{}
	form.Set("name", "Existing Dir Project")
	form.Set("repo_path", t.TempDir())
	form.Set("create_directory", "true")
	rec := postForm(e, "/projects", form)
	assertCode(t, rec, http.StatusSeeOther)
}

// TestTabDuplication_CacheControlMiddleware verifies the middleware that prevents broken UI
// when duplicating a browser tab. The middleware sets Vary: HX-Request on all responses and
// Cache-Control: no-store on HTMX partial responses.
func TestTabDuplication_CacheControlMiddleware(t *testing.T) {
	e := echo.New()

	// Apply the same middleware as main.go
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Vary", "HX-Request")
			if c.Request().Header.Get("HX-Request") == "true" {
				c.Response().Header().Set("Cache-Control", "no-store")
			}
			return next(c)
		}
	})

	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Full page request (no HX-Request) — e.g., duplicated tab
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("full page: expected Vary=HX-Request, got %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("full page: expected no Cache-Control, got %q", got)
	}

	// HTMX partial request — partial must not be cached
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if got := rec2.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("HTMX partial: expected Vary=HX-Request, got %q", got)
	}
	if got := rec2.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("HTMX partial: expected Cache-Control=no-store, got %q", got)
	}
}

// TestTabDuplication_PagesReturnFullLayoutOrPartial verifies that all main navigable pages
// return full HTML (with layout/CSS/JS) for non-HTMX requests and partial content for
// HTMX requests. If a page always returns a partial, a duplicated tab would show unstyled content.
func TestTabDuplication_PagesReturnFullLayoutOrPartial(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderTest
		a.Model = "claude-3-sonnet-20240229"
		a.APIKey = "test-key"
	})

	pages := []struct {
		path string
		name string
	}{
		{"/tasks", "Tasks"},
		{"/chat", "Chat"},
		{"/schedule", "Schedule"},
		{"/models", "Models"},
		{"/analytics", "Analytics"},
		{"/alerts", "Alerts"},
		{"/upcoming", "Upcoming"},
		{"/history", "History"},
		{"/insights", "Insights"},
	}

	for _, pg := range pages {
		t.Run(pg.name+"_FullPage", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, pg.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			body := rec.Body.String()

			if rec.Code != http.StatusOK {
				t.Fatalf("%s full page: expected 200, got %d", pg.name, rec.Code)
			}
			if !strings.Contains(body, "<!doctype html>") {
				t.Errorf("%s full page: missing <!doctype html> — page would be unstyled when tab is duplicated", pg.name)
			}
			if !strings.Contains(body, "htmx.org") {
				t.Errorf("%s full page: missing htmx.org script — page would be non-functional when tab is duplicated", pg.name)
			}
		})

		t.Run(pg.name+"_HTMXPartial", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, pg.path, nil)
			req.Header.Set("HX-Request", "true")
			req.Header.Set("HX-Target", "main-content") // sidebar navigation targets #main-content
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			body := rec.Body.String()

			if rec.Code != http.StatusOK {
				t.Fatalf("%s HTMX: expected 200, got %d", pg.name, rec.Code)
			}
			if strings.Contains(body, "<!doctype html>") {
				t.Errorf("%s HTMX: must not include <!doctype html> — would cause nested layouts in HTMX swap", pg.name)
			}
		})
	}
}

// TestSidebar_SamePageNavPrevention verifies the sidebar contains the script that prevents
// re-navigation when clicking a nav link for the page you're already on. Without this,
// clicking e.g. "Chat" while on /chat would trigger a full HTMX content swap, tearing
// down SSE connections and resetting scroll — looking like a full page reload.
func TestSidebar_SamePageNavPrevention(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	// Full page load — sidebar is included in the layout
	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	// Verify the sidebar contains the re-navigation prevention script
	if !strings.Contains(body, "htmx:beforeRequest") {
		t.Error("sidebar must contain htmx:beforeRequest handler for same-page nav prevention")
	}
	if !strings.Contains(body, "window.location.pathname === navBase") {
		t.Error("sidebar must check current pathname against nav link base to prevent re-navigation")
	}

	// Verify all main nav links have data-nav-base attributes
	for _, navBase := range []string{`data-nav-base="/chat"`, `data-nav-base="/tasks"`, `data-nav-base="/schedule"`} {
		if !strings.Contains(body, navBase) {
			t.Errorf("sidebar must have nav link with %s", navBase)
		}
	}
}

func TestSidebar_ProjectSelectorSingleBorderAndFocusVisible(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	required := []string{
		`id="project-selector"`,
		`class="select select-bordered select-sm w-full sidebar-project-select"`,
		`.sidebar-project-select,`,
		`.sidebar-project-select:hover,`,
		`.sidebar-project-select:focus,`,
		`.sidebar-project-select:active {`,
		`box-shadow: none;`,
		`--tw-ring-shadow: 0 0 #0000;`,
		`.sidebar-project-select:focus-visible {`,
		`box-shadow: 0 0 0 2px hsl(var(--p) / 0.28);`,
		`[data-theme="dark"] .sidebar-project-select:focus-visible {`,
		`box-shadow: 0 0 0 2px hsl(var(--bc) / 0.35);`,
	}
	for _, snippet := range required {
		if !strings.Contains(body, snippet) {
			t.Fatalf("sidebar project selector styling missing snippet: %s", snippet)
		}
	}
}

func TestSidebar_LightModeBackgroundAndNavReadability(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	pages := []string{
		"/tasks",
		"/chat",
		"/schedule",
		"/models",
		"/analytics",
		"/alerts",
		"/upcoming",
		"/history",
		"/insights",
	}

		requiredSnippets := []string{
			`id="sidebar"`,
			`class="sidebar-aside bg-base-100`,
			`id="project-selector"`,
			`--ov-l-bg: #FAFAFA;`,
			`--ov-l-surface: #F5F5F5;`,
			`[data-theme="light"] .sidebar-aside {`,
			`background-color: #FAFAFA;`,
			`border-color: var(--ov-l-border);`,
		`[data-theme="light"] .menu-title span {`,
		`color: var(--ov-l-text);`,
		`[data-theme="light"] .menu a,`,
		`color: var(--ov-l-text-strong);`,
		`[data-theme="light"] .menu a:hover,`,
		`background-color: var(--ov-l-surface-hover);`,
		`[data-theme="light"] .menu a svg,`,
		`color: var(--ov-l-text-muted);`,
		`[data-theme="light"] .bg-base-100 {`,
		`background-color: var(--ov-l-bg);`,
		`[data-theme="light"] .bg-base-200 {`,
		`[data-theme="light"] .stats {`,
		`background-color: var(--ov-l-bg);`,
		`[data-theme="light"] .hover\:border-primary:hover {`,
		`border-color: oklch(var(--p));`,
		`[data-theme="light"] .hover\:border-primary\/40:hover {`,
		`border-color: oklch(var(--p) / 0.4);`,
		`[data-theme="light"] .chat-input-container {`,
		`background-color: #FFFFFF;`,
		`[data-theme="light"] .chat-bubble-user-msg,`,
		`[data-theme="light"] .sidebar-divider hr {`,
		`border-color: var(--ov-l-border-contrast);`,
	}

	for _, path := range pages {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			body := rec.Body.String()

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", path, rec.Code)
			}
			for _, snippet := range requiredSnippets {
				if !strings.Contains(body, snippet) {
					t.Fatalf("%s missing required sidebar styling/structure snippet: %s", path, snippet)
				}
			}
		})
	}
}

func TestTasks_DeleteButton_LightMode_NoDefaultCircularBackground(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	createTask(t, h, "default", "Delete Backlog", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusPending
	})
	createTask(t, h, "default", "Delete Active", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
	})
	createTask(t, h, "default", "Delete Completed", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	required := []string{
		`class="btn btn-circle btn-ghost btn-xs absolute top-2 right-2 z-10"`,
		`[data-theme="light"] .card .btn-circle.btn-ghost {`,
		`background-color: transparent;`,
		`[data-theme="light"] .card .btn-circle.btn-ghost:hover {`,
		`background-color: #F8514922;`,
	}
	for _, snippet := range required {
		if !strings.Contains(body, snippet) {
			t.Fatalf("/tasks missing expected delete button snippet: %s", snippet)
		}
	}

	if strings.Contains(body, `background-color: #0000000D;`) {
		t.Fatal("light-mode delete X button should not have a default circular background fill")
	}
}

func TestSidebar_AlertsGroupedUnderSystem(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	insightsIdx := strings.Index(body, `>Insights</span>`)
	systemIdx := strings.Index(body, `>System</span>`)
	alertsIdx := strings.Index(body, `data-nav-base="/alerts"`)
	modelsIdx := strings.Index(body, `data-nav-base="/models"`)
	if insightsIdx < 0 || systemIdx < 0 || alertsIdx < 0 || modelsIdx < 0 {
		t.Fatal("sidebar missing Insights/System/Alerts/Models markers")
	}
	if !(insightsIdx < systemIdx) {
		t.Fatal("Insights section must appear before System section")
	}
	if !(systemIdx < alertsIdx && alertsIdx < modelsIdx) {
		t.Fatal("Alerts nav item must be grouped under System, before Models")
	}
	if strings.Contains(body[:systemIdx], `data-nav-base="/alerts"`) {
		t.Fatal("Alerts nav item must not appear before the System section")
	}

	requiredAlertsSnippets := []string{
		`href="/alerts?project_id=`,
		`hx-get="/alerts?project_id=`,
		`id="alert-badge"`,
		`hx-get="/alerts/unread-count?project_id=`,
		`hx-trigger="load, every 30s, alertUpdate from:body"`,
	}
	for _, snippet := range requiredAlertsSnippets {
		if !strings.Contains(body, snippet) {
			t.Fatalf("alerts nav item must preserve route/badge behavior snippet: %s", snippet)
		}
	}
}

func TestHandler_TaskThreadSend(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	project := createProject(t, h, "Chat Test Project")
	task := createTask(t, h, project.ID, "Chat Test Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.Priority = 2
		tk.AgentID = &agent.ID
	})

	form := url.Values{}
	form.Set("message", "Can you explain that in more detail?")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Can you explain that in more detail?")
	assertContains(t, rec, "chat-bubble-assistant-msg")

	execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if len(execs) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(execs))
	}
	if !execs[0].IsFollowup {
		t.Error("expected execution to be marked as followup")
	}
	updatedTask, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updatedTask.Status != models.StatusQueued {
		t.Errorf("expected status queued, got %s", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryActive {
		t.Errorf("expected category active, got %s", updatedTask.Category)
	}
}

func TestHandler_TaskThreadSend_BacklogMovesToActive(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Backlog Chat Test")
	task := createTask(t, h, project.ID, "Backlog Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Priority = 2
		tk.AgentID = &agent.ID
	})

	form := url.Values{}
	form.Set("message", "Follow up from backlog")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)

	updatedTask, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updatedTask.Category != models.CategoryActive {
		t.Errorf("expected category active, got %s", updatedTask.Category)
	}
	if updatedTask.Status != models.StatusQueued {
		t.Errorf("expected status queued, got %s", updatedTask.Status)
	}
}

func TestHandler_TaskThreadSend_WithExplicitAgent(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	defaultAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Default Agent" })
	explicitAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Explicit Agent"
		a.Provider = "anthropic"
		a.Model = "claude-opus-4-20250514"
		a.MaxTokens = 8192
		a.IsDefault = false
	})
	project := createProject(t, h, "Agent Select Test")
	task := createTask(t, h, project.ID, "Agent Select Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.AgentID = &defaultAgent.ID
	})

	form := url.Values{}
	form.Set("message", "Use this specific agent")
	form.Set("agent_id", explicitAgent.ID)
	rec := postForm(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)

	execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if len(execs) != 1 || execs[0].AgentConfigID != explicitAgent.ID {
		t.Errorf("expected execution with explicit agent %s", explicitAgent.ID)
	}
}

func TestHandler_GetTaskThread_LoadsAttachments(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Provider = "anthropic"; a.Model = "claude-sonnet-4-5-20250929" })
	project := createProject(t, h, "Attachment Test")
	task := createTask(t, h, project.ID, "Attachment Task", func(tk *models.Task) { tk.AgentID = &agent.ID })

	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "Analyze this image"
		ex.Output = "I see a screenshot"
		ex.IsFollowup = true
	})
	h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "I see a screenshot", "", 10, 100)
	h.chatAttachmentRepo.Create(ctx, &models.ChatAttachment{
		ExecutionID: exec.ID, FileName: "screenshot.png",
		FilePath: "/tmp/fake/screenshot.png", MediaType: "image/png", FileSize: 12345,
	})

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "screenshot.png")
}

func TestHandler_TaskThreadSend_EmptyMessage(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	form := url.Values{}
	form.Set("message", "")
	rec := postForm(e, "/tasks/fake-id/thread", form)
	assertCode(t, rec, http.StatusBadRequest)
}

func TestHandler_TaskThreadSend_QueuesWhenModelAtCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Limited Model"
		a.MaxWorkers = 1
		a.WorkerTimeout = 1
	})
	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)
	project := createProject(t, h, "Worker Limit Project")
	task := createTask(t, h, project.ID, "Worker Limit Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})

	h.workerSvc.TryAcquireModelSlot(agent.ID)
	defer h.workerSvc.ReleaseModelSlot(agent.ID)

	// Thread messages are always accepted even when model is at capacity
	form := url.Values{}
	form.Set("message", "Follow up message")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Follow up message")

	// Execution record should be created (message saved for processing)
	execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if len(execs) != 1 {
		t.Fatalf("expected 1 execution (queued), got %d", len(execs))
	}
	if !execs[0].IsFollowup {
		t.Error("expected execution to be marked as followup")
	}
}

func TestHandler_TaskThreadSend_QueuesWhenProjectAtCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	maxWorkers := 1
	project := &models.Project{Name: "Worker Limit Project", MaxWorkers: &maxWorkers}
	h.projectSvc.Create(ctx, project)
	h.workerSvc.SetProjectRepo(h.projectRepo)

	createTask(t, h, project.ID, "Running Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	task2 := createTask(t, h, project.ID, "Idle Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})

	h.workerSvc.TryAcquireProjectSlot(project.ID)
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	// Thread messages are always accepted even when project is at capacity
	form := url.Values{}
	form.Set("message", "Follow up message")
	rec := htmxPost(e, "/tasks/"+task2.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Follow up message")

	// Task should be moved to queued/active (waiting for worker slots)
	updatedTask2, _ := h.taskSvc.GetByID(ctx, task2.ID)
	if updatedTask2.Status != models.StatusQueued {
		t.Errorf("expected task status queued (waiting for worker slots), got %s", updatedTask2.Status)
	}
	if updatedTask2.Category != models.CategoryActive {
		t.Errorf("expected category active, got %s", updatedTask2.Category)
	}
	// Execution record should be created (message saved)
	execs, _ := h.execRepo.ListByTaskChronological(ctx, task2.ID)
	if len(execs) != 1 {
		t.Fatalf("expected 1 execution (queued), got %d", len(execs))
	}
	if execs[0].PromptSent != "Follow up message" {
		t.Errorf("expected message saved in execution, got %q", execs[0].PromptSent)
	}
}

func TestHandler_TaskThreadSend_AllowsWhenProjectHasCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	maxWorkers := 2
	project := &models.Project{Name: "Capacity Project", MaxWorkers: &maxWorkers}
	h.projectSvc.Create(ctx, project)
	h.workerSvc.SetProjectRepo(h.projectRepo)

	task := createTask(t, h, project.ID, "Chat Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})

	h.workerSvc.TryAcquireProjectSlot(project.ID)
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	form := url.Values{}
	form.Set("message", "Follow up with capacity")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)

	updatedTask, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updatedTask.Status != models.StatusQueued {
		t.Errorf("expected status queued, got %s", updatedTask.Status)
	}
}

func TestHandler_TaskThreadSend_SkipsCheckWhenAlreadyRunning(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	maxWorkers := 1
	project := &models.Project{Name: "Already Running Project", MaxWorkers: &maxWorkers}
	h.projectSvc.Create(ctx, project)
	h.workerSvc.SetProjectRepo(h.projectRepo)

	task := createTask(t, h, project.ID, "Already Running Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})

	h.workerSvc.TryAcquireProjectSlot(project.ID)
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	form := url.Values{}
	form.Set("message", "Continue working")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
}

func TestHandler_GetTaskThread(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	project := createProject(t, h, "Chat View Project")
	task := createTask(t, h, project.ID, "Chat View Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Priority = 2
		tk.Prompt = "Test prompt"
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "Test prompt"
	})
	h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "Task output", "", 100, 500)

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "task-thread-view")
	assertContains(t, rec, "Test prompt")
	assertContains(t, rec, "Task output")
	assertContains(t, rec, "task-thread-form")
}

func TestHandler_GetTaskThread_DoesNotPollWhenPending(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	project := createProject(t, h, "Pending Thread Polling Project")
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	task := createTask(t, h, project.ID, "Pending Task", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.Category = models.CategoryActive
		tk.Prompt = "Pending prompt"
		tk.AgentID = &agent.ID
	})

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, `id="task-thread-view"`)
	assert.NotContains(t, body, `hx-trigger="every 3s"`)
}

func TestHandler_GetTaskThread_PollsWhenQueued(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	project := createProject(t, h, "Queued Thread Polling Project")
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	task := createTask(t, h, project.ID, "Queued Task", func(tk *models.Task) {
		tk.Status = models.StatusQueued
		tk.Category = models.CategoryActive
		tk.Prompt = "Queued prompt"
		tk.AgentID = &agent.ID
	})

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, `id="task-thread-view"`)
	assert.Contains(t, body, `hx-trigger="every 3s"`)
	assert.Contains(t, body, `hx-get="/tasks/`+task.ID+`/thread"`)
}

func TestHandler_GetTaskThread_DraftClearLogic_DoesNotTreatPollingGetAsSend(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	project := createProject(t, h, "Thread Draft Clear Regression Project")
	agent := createAgent(t, llmConfigRepo)
	task := createTask(t, h, project.ID, "Thread Draft Clear Regression Task", func(tk *models.Task) {
		tk.Status = models.StatusQueued
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
	})

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, "var isPost = requestMethod === 'POST';")
	assert.Contains(t, body, "var isThreadSendRequest = isPost && isThreadPath;")
	assert.NotContains(t, body, "|| requestPath.indexOf('/thread') !== -1;")
}

func TestHandler_GetTaskThread_RunningPlaceholder_NoLiteralThinkingText(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	project := createProject(t, h, "Thread Running Placeholder Project")
	task := createTask(t, h, project.ID, "Thread Running Placeholder Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.Prompt = "Thread prompt"
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Thread prompt"
	})
	assert.NoError(t, h.execRepo.UpdateOutput(ctx, exec.ID, ""))

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, "task-thread-view")
	assert.Contains(t, body, "ov-loading-dots ov-loading-dots-sm")
	assert.GreaterOrEqual(t, strings.Count(body, `class="ov-loading-dot"`), 3)
	assert.Contains(t, body, "class=\"block h-5\" aria-hidden=\"true\"")
	assert.NotContains(t, body, "Thinking...")
}

func TestHandler_GetTaskThread_RunningWithPartialOutput_ShowsStreamingDots(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	project := createProject(t, h, "Thread Running Partial Output Project")
	task := createTask(t, h, project.ID, "Thread Running Partial Output Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.Prompt = "Thread prompt"
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Thread prompt"
	})
	assert.NoError(t, h.execRepo.UpdateOutput(ctx, exec.ID, "Partial thread output"))

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, "task-thread-view")
	assert.Contains(t, body, "streaming-dots-resume-"+exec.ID)
	assert.Contains(t, body, `id="streaming-dots-resume-`+exec.ID+`" class="flex items-center gap-1 mt-2 opacity-40"`)
	assert.NotContains(t, body, `id="streaming-dots-resume-`+exec.ID+`" class="hidden`)
	assert.Contains(t, body, "ov-loading-dots ov-loading-dots-xs")
	assert.GreaterOrEqual(t, strings.Count(body, `class="ov-loading-dot"`), 3)
	assert.NotContains(t, body, "Thinking...")
}

// TestHandler_GetTaskThread_MultiTurnOrdering verifies that follow-up messages
// appear after the original task prompt in the thread timeline (chronological order).
// This was a bug where GetTask used ListByTask (DESC) instead of ListByTaskChronological (ASC),
// causing follow-ups to appear at the top of the thread instead of the bottom.
func TestHandler_GetTaskThread_MultiTurnOrdering(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Multi-turn Project")
	task := createTask(t, h, project.ID, "Multi-turn Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.Priority = 2
		tk.Prompt = "Original task prompt"
	})

	exec1 := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.PromptSent = "Original task prompt" })
	h.execRepo.Complete(ctx, exec1.ID, models.ExecCompleted, "Original output", "", 100, 500)
	exec2 := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.PromptSent = "Summarize this followup"; ex.IsFollowup = true })
	h.execRepo.Complete(ctx, exec2.ID, models.ExecCompleted, "Summary output", "", 50, 200)

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	origIdx := strings.Index(body, "Original task prompt")
	followupIdx := strings.Index(body, "Summarize this followup")
	if origIdx == -1 || followupIdx == -1 {
		t.Fatal("expected both prompts in thread view")
	}
	if origIdx >= followupIdx {
		t.Error("BUG: original prompt should appear BEFORE follow-up")
	}

	// GetTask now lazy-loads thread content, so /tasks?tab=chat should not eagerly
	// include full thread transcript in the initial HTML.
	req2 := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab=chat", nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	c2.SetPath("/tasks/:taskId")
	c2.SetParamNames("taskId")
	c2.SetParamValues(task.ID)
	h.GetTask(c2)
	body2 := rec2.Body.String()
	if !strings.Contains(body2, "Thread is loading...") {
		t.Error("expected lazy thread loading placeholder in task detail response")
	}
	if strings.Contains(body2, "Summary output") || strings.Contains(body2, `id="task-thread-view"`) {
		t.Error("did not expect eager thread transcript in task detail response")
	}
}

// TestHandler_GetTaskThread_NoDuplicatePrompt verifies that when a task has multiple
// non-followup executions (re-runs), the task prompt only appears once in the thread.
func TestHandler_GetTaskThread_NoDuplicatePrompt(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Dedup Project")
	task := createTask(t, h, project.ID, "Dedup Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.Prompt = "UNIQUE_TASK_PROMPT_TEXT"
	})

	for i := 0; i < 3; i++ {
		ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.PromptSent = "UNIQUE_TASK_PROMPT_TEXT" })
		h.execRepo.Complete(ctx, ex.ID, models.ExecCompleted, fmt.Sprintf("output run %d", i+1), "", 50, 100)
	}
	followup := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.PromptSent = "Summarize all runs"; ex.IsFollowup = true })
	h.execRepo.Complete(ctx, followup.ID, models.ExecCompleted, "summary output", "", 50, 100)

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	if promptCount := strings.Count(body, "UNIQUE_TASK_PROMPT_TEXT"); promptCount != 1 {
		t.Errorf("task prompt appears %d times (expected 1)", promptCount)
	}
	assertContains(t, rec, "Summarize all runs")
	for i := 1; i <= 3; i++ {
		if !strings.Contains(body, fmt.Sprintf("output run %d", i)) {
			t.Errorf("expected output run %d in thread", i)
		}
	}
}

func TestHandler_GetTaskThread_PreservesHistoryAfterFailureAndRetry(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Failure Retry Continuity Project")
	task := createTask(t, h, project.ID, "Failure Retry Continuity Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusFailed
		tk.Prompt = "Original continuity prompt"
	})

	// Initial successful run (existing history that must never disappear)
	initial := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.PromptSent = "Original continuity prompt"
		ex.IsFollowup = false
	})
	h.execRepo.Complete(ctx, initial.ID, models.ExecCompleted, "initial success output", "", 50, 100)

	// Follow-up that fails after streaming output; failure completion called with empty output
	failedFollowup := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.PromptSent = "Run tests now"
		ex.IsFollowup = true
	})
	streamedFailureOutput := "running go test ./...\n--- FAIL: TestWidget"
	if err := h.execRepo.UpdateOutput(ctx, failedFollowup.ID, streamedFailureOutput); err != nil {
		t.Fatalf("failed to seed streamed failure output: %v", err)
	}
	h.execRepo.Complete(ctx, failedFollowup.ID, models.ExecFailed, "", "go test failed", 0, 120)

	// Retry after failure should append another execution, preserving full prior history
	retry := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.PromptSent = "Fix tests and retry"
		ex.IsFollowup = true
	})
	h.execRepo.Complete(ctx, retry.ID, models.ExecCompleted, "retry success output", "", 60, 140)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(execs) != 3 {
		t.Fatalf("expected 3 executions preserved across failure+retry, got %d", len(execs))
	}
	if execs[1].Status != models.ExecFailed {
		t.Fatalf("expected second execution to be failed, got %s", execs[1].Status)
	}
	if execs[1].Output != streamedFailureOutput {
		t.Fatalf("expected failed execution to preserve streamed output, got %q", execs[1].Output)
	}

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	if !strings.Contains(body, "initial success output") {
		t.Fatal("expected existing history to remain visible after failure")
	}
	if !strings.Contains(body, "Error: go test failed") {
		t.Fatal("expected failure error to be appended in thread")
	}
	if !strings.Contains(body, streamedFailureOutput) {
		t.Fatal("expected failure output to be preserved in thread")
	}
	if !strings.Contains(body, "retry success output") {
		t.Fatal("expected retry output to be appended without replacing prior messages")
	}
}

func TestHandler_GetTaskThread_PreservesHistoryAfterRateLimitFailure(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Rate Limit Continuity Project")
	task := createTask(t, h, project.ID, "Rate Limit Continuity Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusFailed
		tk.Prompt = "Original continuity prompt"
	})

	initial := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.PromptSent = "Original continuity prompt"
		ex.IsFollowup = false
	})
	if err := h.execRepo.Complete(ctx, initial.ID, models.ExecCompleted, "initial success output", "", 50, 100); err != nil {
		t.Fatalf("complete initial execution: %v", err)
	}

	rateLimited := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.PromptSent = "Continue work"
		ex.IsFollowup = true
	})
	streamedFailureOutput := "starting retry flow..."
	if err := h.execRepo.UpdateOutput(ctx, rateLimited.ID, streamedFailureOutput); err != nil {
		t.Fatalf("failed to seed streamed failure output: %v", err)
	}
	rateLimitErr := `API error 429: {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`
	if err := h.execRepo.Complete(ctx, rateLimited.ID, models.ExecFailed, "", rateLimitErr, 0, 120); err != nil {
		t.Fatalf("complete rate-limited execution: %v", err)
	}

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	if !strings.Contains(body, "initial success output") {
		t.Fatal("expected prior history to remain visible after 429 failure")
	}
	if !strings.Contains(body, "Error: API error 429") {
		t.Fatal("expected 429 rate-limit failure header to be appended as an error entry")
	}
	if !strings.Contains(body, "rate_limit_error") {
		t.Fatal("expected rate_limit_error details in thread failure entry")
	}
	if !strings.Contains(body, streamedFailureOutput) {
		t.Fatal("expected 429 failure execution output to be preserved in thread")
	}
}

func TestHandler_GradeIdeas_NoService(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	// GradeIdeas with nil insightsSvc should return bad request
	req := httptest.NewRequest(http.MethodPost, "/history/grade-ideas?project_id=test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandler_GradeIdeas_MissingProjectID(t *testing.T) {
	_, e := setupTestHandlerWithInsights(t)
	rec := htmxPost(e, "/history/grade-ideas", nil)
	assertCode(t, rec, http.StatusBadRequest)
}

func TestHandler_GradeIdeas_NoTasks(t *testing.T) {
	h, e := setupTestHandlerWithInsights(t)
	project := createProject(t, h, "Empty Project")

	rec := htmxPost(e, "/history/grade-ideas?project_id="+project.ID, nil)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "failed")
}
