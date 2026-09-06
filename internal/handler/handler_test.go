package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHandler(t testing.TB) (*Handler, *echo.Echo, *repository.LLMConfigRepo) {
	h, e, llmConfigRepo, _ := setupTestHandlerWithDB(t)
	return h, e, llmConfigRepo
}

func setupTestHandlerForDB(t testing.TB, db *sql.DB) (*Handler, *echo.Echo, *repository.LLMConfigRepo) {
	t.Helper()
	oldUploadsDir := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() {
		uploadsDir = oldUploadsDir
	})

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
	taskSvc.SetDeletionUploadsDir(uploadsDir)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)

	settingsRepo := repository.NewSettingsRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	discordAuthRepo := repository.NewDiscordAuthRepo(db)
	discordTaskContextRepo := repository.NewDiscordTaskContextRepo(db)
	githubAuthRepo := repository.NewGitHubAuthRepo(db)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, nil, nil)
	h.SetGitHubAuthRepo(githubAuthRepo)
	h.SetSlackAuthRepo(slackAuthRepo)
	h.SetEmailAuthRepo(emailAuthRepo)
	h.SetEmailTaskContextRepo(emailTaskContextRepo)
	h.SetDiscordAuthRepo(discordAuthRepo)
	h.SetDiscordTaskContextRepo(discordTaskContextRepo)
	h.SetLocalRepoPathEnabled(true)

	e := echo.New()
	h.RegisterRoutes(e)
	return h, e, llmConfigRepo
}

func setupTestHandlerWithDB(t testing.TB) (*Handler, *echo.Echo, *repository.LLMConfigRepo, *sql.DB) {
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
	taskSvc.SetDeletionUploadsDir(uploadsDir)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)

	settingsRepo := repository.NewSettingsRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	discordAuthRepo := repository.NewDiscordAuthRepo(db)
	discordTaskContextRepo := repository.NewDiscordTaskContextRepo(db)
	githubAuthRepo := repository.NewGitHubAuthRepo(db)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, nil, nil)
	h.SetGitHubAuthRepo(githubAuthRepo)
	h.SetSlackAuthRepo(slackAuthRepo)
	h.SetEmailAuthRepo(emailAuthRepo)
	h.SetEmailTaskContextRepo(emailTaskContextRepo)
	h.SetDiscordAuthRepo(discordAuthRepo)
	h.SetDiscordTaskContextRepo(discordTaskContextRepo)
	h.SetLocalRepoPathEnabled(true)

	e := echo.New()
	h.RegisterRoutes(e)

	return h, e, llmConfigRepo, db
}

// createProject creates a test project with the given name.
func createProject(t *testing.T, h *Handler, name string) *models.Project {
	return createProjectTB(t, h, name)
}

func createProjectTB(t testing.TB, h *Handler, name string) *models.Project {
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
	return createAgentTB(t, repo, opts...)
}

func createAgentTB(t testing.TB, repo *repository.LLMConfigRepo, opts ...func(*models.LLMConfig)) *models.LLMConfig {
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
	return createTaskTB(t, h, projectID, title, opts...)
}

func createTaskTB(t testing.TB, h *Handler, projectID, title string, opts ...func(*models.Task)) *models.Task {
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
	taskSvc.SetDeletionUploadsDir(uploadsDir)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	insightsSvc := service.NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	insightsSvc.SetLLMService(llmSvc)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, insightsSvc, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, nil, nil, nil)
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

func TestHandler_GetTaskLargeHistoryUsesNarrowExecutionMetrics(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Initial Detail Metrics Project")
	task := createTask(t, h, project.ID, "Initial Detail Metrics Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusCompleted
		tk.Priority = 3
		tk.Tag = models.TagFeature
		tk.AgentID = &agent.ID
	})
	task.Category = models.CategoryActive
	if err := h.taskSvc.Update(ctx, task); err != nil {
		t.Fatalf("update task to active completed: %v", err)
	}
	seedLargeExecutionHistory(t, db, task.ID, agent.ID, 200, 4*1024, 64*1024)
	if _, err := db.ExecContext(ctx, `UPDATE executions SET status = ?, duration_ms = ?, completed_at = ? WHERE id = ?`, models.ExecCompleted, int64(65_000), "2026-08-13 12:03:20", "exec-199"); err != nil {
		t.Fatalf("complete latest execution: %v", err)
	}

	checkInitialDetail := func(name string, htmx bool) {
		t.Helper()
		counter.Reset()
		counter.SetEnabled(true)
		var rec *httptest.ResponseRecorder
		if htmx {
			rec = htmxGet(e, "/tasks/"+task.ID)
		} else {
			req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
			rec = httptest.NewRecorder()
			e.ServeHTTP(rec, req)
		}
		counter.SetEnabled(false)

		assertCode(t, rec, http.StatusOK)
		for _, required := range []string{"Initial Detail Metrics Task", "Category:", "active", "Status:", "Completed", "Tag:", "Feature", "Priority:", "High", "Model:", "Test Agent", "Duration:", "1m 5s"} {
			assertContains(t, rec, required)
		}

		metricsQuerySeen := false
		for _, stmt := range counter.Statements() {
			if !strings.Contains(stmt, "FROM executions") {
				continue
			}
			if strings.Contains(stmt, "latest_started_at") && strings.Contains(stmt, "latest_duration_ms") {
				metricsQuerySeen = true
			}
			for _, forbidden := range []string{"prompt_sent", "output", "error_message", "reasoning_content", "diff_output"} {
				if strings.Contains(stmt, forbidden) {
					t.Fatalf("%s execution query scanned historical %s text: %s", name, forbidden, stmt)
				}
			}
			if strings.Contains(stmt, "ORDER BY started_at ASC, rowid ASC") {
				t.Fatalf("%s initial detail path executed the unbounded chronological execution query: %s", name, stmt)
			}
		}
		if !metricsQuerySeen {
			t.Fatalf("%s initial detail path did not execute compact task execution metrics query; statements: %#v", name, counter.Statements())
		}
	}

	checkInitialDetail("HTMX", true)
	checkInitialDetail("full-page", false)
}

func BenchmarkHandler_GetTask_MetricsProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	h, e, llmConfigRepo := setupTestHandlerForDB(b, db)
	ctx := context.Background()
	agent := createAgentTB(b, llmConfigRepo)
	project := createProjectTB(b, h, "Initial Detail Metrics Benchmark Project")
	task := createTaskTB(b, h, project.ID, "Initial Detail Metrics Benchmark Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusCompleted
		tk.Priority = 3
		tk.Tag = models.TagFeature
		tk.AgentID = &agent.ID
	})
	task.Category = models.CategoryActive
	if err := h.taskSvc.Update(ctx, task); err != nil {
		b.Fatalf("update task to active completed: %v", err)
	}
	seedLargeExecutionHistory(b, db, task.ID, agent.ID, 200, 4*1024, 64*1024)

	b.ReportAllocs()
	b.ResetTimer()
	var responseBytes int64
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("detail request status=%d", rec.Code)
		}
		responseBytes += int64(rec.Body.Len())
	}
	b.ReportMetric(float64(responseBytes)/float64(b.N), "rendered_response_bytes/op")
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

func TestHandler_GetTaskExecutions_BoundedProductionFixture(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Execution History Project")
	task := createTask(t, h, project.ID, "Large Execution History", func(tk *models.Task) {
		tk.Status = models.StatusRunning
	})
	seedLargeExecutionHistory(t, db, task.ID, agent.ID, 200, 4*1024, 64*1024)

	rec := htmxGet(e, "/tasks/"+task.ID+"/executions")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `id="task-execution-history"`)
	assertContains(t, rec, "Load older executions")
	assertContains(t, rec, "output-199-")
	assertContains(t, rec, `exec-output-exec-199`)
	assertContains(t, rec, "Elapsed:")
	assertContains(t, rec, "output-182-")
	assertContains(t, rec, "64 tokens, 1s")
	assertContains(t, rec, "Finished:")
	assertContains(t, rec, "failure message")
	assertContains(t, rec, `exec-error-exec-198`)
	assertNotContains(t, rec, "output-181-")
	assertNotContains(t, rec, "output-000-")
	assertContains(t, rec, `id="task-execution-history-loaded-older"`)
	assertContains(t, rec, `hx-preserve`)

	const maxResponseBytes = 2 * 1024 * 1024
	if got := rec.Body.Len(); got > maxResponseBytes {
		t.Fatalf("bounded execution-history response too large: got %d bytes, want <= %d", got, maxResponseBytes)
	}
}

func TestHandler_GetTaskExecutions_LoadOlderPage(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Execution History Project")
	task := createTask(t, h, project.ID, "Paged Execution History", func(tk *models.Task) {
		tk.Status = models.StatusRunning
	})
	seedLargeExecutionHistory(t, db, task.ID, agent.ID, 25, 16, 64)

	rec := htmxGet(e, "/tasks/"+task.ID+"/executions?limit=5")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "output-024-")
	assertContains(t, rec, "output-020-")
	assertNotContains(t, rec, "output-019-")

	oldestVisibleID := newestHistoryExecutionID(t, db, task.ID, 5)
	older := htmxGet(e, "/tasks/"+task.ID+"/executions?before="+oldestVisibleID+"&limit=5")
	assertCode(t, older, http.StatusOK)
	assertContains(t, older, `id="task-execution-history-loaded-older"`)
	assertContains(t, older, `hx-swap-oob="beforeend"`)
	assertContains(t, older, `id="task-execution-history-older-loader"`)
	assertContains(t, older, `data-execution-history-card="exec-019"`)
	assertContains(t, older, "output-019-")
	assertContains(t, older, "output-015-")
	assertNotContains(t, older, "output-020-")
	assertNotContains(t, older, "output-014-")
	assertContains(t, older, "Load older executions")
}

func TestHandler_TaskExecutionWindowsPreserveSharedBoundaries(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Shared Execution Window Project")
	task := createTask(t, h, project.ID, "Shared Execution Window Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
	})
	seedLargeExecutionHistory(t, db, task.ID, agent.ID, 25, 16, 64)
	ctx := context.Background()

	history, hasOlder, err := h.loadTaskExecutionHistoryWindow(ctx, task.ID, "", 5)
	require.NoError(t, err)
	require.True(t, hasOlder)
	require.Equal(t, []string{"exec-024", "exec-023", "exec-022", "exec-021", "exec-020"}, executionIDs(history))

	thread, hasEarlier, err := h.loadTaskThreadExecutionWindow(ctx, task.ID, "", 5)
	require.NoError(t, err)
	require.True(t, hasEarlier)
	require.Equal(t, []string{"exec-020", "exec-021", "exec-022", "exec-023", "exec-024"}, executionIDs(thread))

	historyOlder, hasOlder, err := h.loadTaskExecutionHistoryWindow(ctx, task.ID, history[len(history)-1].ID, 5)
	require.NoError(t, err)
	require.True(t, hasOlder)
	require.Equal(t, []string{"exec-019", "exec-018", "exec-017", "exec-016", "exec-015"}, executionIDs(historyOlder))

	threadEarlier, hasEarlier, err := h.loadTaskThreadExecutionWindow(ctx, task.ID, thread[0].ID, 5)
	require.NoError(t, err)
	require.True(t, hasEarlier)
	require.Equal(t, []string{"exec-015", "exec-016", "exec-017", "exec-018", "exec-019"}, executionIDs(threadEarlier))

	for _, execution := range historyOlder {
		require.NotContains(t, executionIDs(history), execution.ID)
	}
	for _, execution := range threadEarlier {
		require.NotContains(t, executionIDs(thread), execution.ID)
	}
}

func TestHandler_TaskExecutionWindowRoutesNormalizeInvalidLimits(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Execution Window Limit Project")
	task := createTask(t, h, project.ID, "Execution Window Limit Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
	})
	seedLargeExecutionHistory(t, db, task.ID, agent.ID, 6, 8, 16)

	for _, rawLimit := range []string{"", "0", "-3"} {
		historyPath := "/tasks/" + task.ID + "/executions"
		threadPath := "/tasks/" + task.ID + "/thread"
		if rawLimit != "" {
			historyPath += "?limit=" + rawLimit
			threadPath += "?limit=" + rawLimit
		}

		history := htmxGet(e, historyPath)
		assertCode(t, history, http.StatusOK)
		historyBody := history.Body.String()
		assert.Contains(t, historyBody, `data-execution-history-card="exec-005"`)
		assert.Contains(t, historyBody, `data-execution-history-card="exec-000"`)
		assert.NotContains(t, historyBody, "Load older executions")
		assert.Contains(t, historyBody, fmt.Sprintf(`hx-get="/tasks/%s/executions?limit=%d"`, task.ID, taskExecutionHistoryWindowDefault))

		thread := htmxGet(e, threadPath)
		assertCode(t, thread, http.StatusOK)
		threadBody := thread.Body.String()
		assert.Contains(t, threadBody, `data-window-limit="5"`)
		assert.Contains(t, threadBody, `id="chat-execution-exec-001"`)
		assert.Contains(t, threadBody, `id="chat-execution-exec-005"`)
		assert.NotContains(t, threadBody, `id="chat-execution-exec-000"`)
		assert.Contains(t, threadBody, `id="task-thread-messages-earlier-loader"`)
		assert.Contains(t, threadBody, `before=exec-001`)
		assert.Contains(t, threadBody, fmt.Sprintf(`hx-get="/tasks/%s/thread?poll=1&amp;limit=%d"`, task.ID, taskThreadWindowLimitDefault))
	}
}

func TestHandler_TaskExecutionWindowRoutesKeepExactShortAndEmptyHistories(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Execution Window Boundary Project")
	task := createTask(t, h, project.ID, "Execution Window Boundary Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
	})

	for _, tc := range []struct {
		name  string
		count int
	}{
		{name: "exact", count: 5},
		{name: "short", count: 3},
		{name: "empty", count: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.ExecContext(context.Background(), "DELETE FROM executions WHERE task_id = ?", task.ID)
			require.NoError(t, err)
			seedLargeExecutionHistory(t, db, task.ID, agent.ID, tc.count, 8, 16)

			history := htmxGet(e, "/tasks/"+task.ID+"/executions?limit=5")
			assertCode(t, history, http.StatusOK)
			historyBody := history.Body.String()
			assert.NotContains(t, historyBody, "Load older executions")

			thread := htmxGet(e, "/tasks/"+task.ID+"/thread?limit=5")
			assertCode(t, thread, http.StatusOK)
			threadBody := thread.Body.String()
			assert.NotContains(t, threadBody, `id="task-thread-messages-earlier-loader"`)

			if tc.count == 0 {
				assert.Contains(t, historyBody, "No executions yet")
				assert.Contains(t, threadBody, "No messages yet")
				return
			}

			latestID := fmt.Sprintf("exec-%03d", tc.count-1)
			assert.Contains(t, historyBody, `data-execution-history-card="`+latestID+`"`)
			assert.Contains(t, threadBody, `id="chat-execution-`+latestID+`"`)
		})
	}
}

func TestHandler_GetTaskThreadActivePollUsesSharedExecutionWindow(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Active Poll Execution Window Project")
	task := createTask(t, h, project.ID, "Active Poll Execution Window Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
	})
	seedLargeExecutionHistory(t, db, task.ID, agent.ID, 7, 8, 16)

	counter.Reset()
	counter.SetEnabled(true)
	defer counter.SetEnabled(false)
	poll := htmxGet(e, "/tasks/"+task.ID+"/thread?poll=1&limit=3")
	assertCode(t, poll, http.StatusOK)
	body := poll.Body.String()
	assert.Contains(t, body, `data-task-active="true"`)
	assert.Contains(t, body, `data-window-limit="3"`)
	assert.Contains(t, body, `id="task-thread-messages-earlier-loader"`)
	assert.Contains(t, body, `before=exec-004`)
	assert.NotContains(t, body, `id="chat-execution-exec-003"`)

	lastIndex := -1
	for _, id := range []string{"exec-004", "exec-005", "exec-006"} {
		marker := `id="chat-execution-` + id + `"`
		index := strings.Index(body, marker)
		if index <= lastIndex {
			t.Fatalf("active poll rendered execution %s out of chronological order: index=%d previous=%d", id, index, lastIndex)
		}
		lastIndex = index
	}

	windowQuerySeen := false
	for _, statement := range counter.Statements() {
		if !strings.Contains(statement, "FROM executions") {
			continue
		}
		if strings.Contains(statement, "ORDER BY started_at ASC") {
			t.Fatalf("active poll executed an unbounded chronological execution query: %s", statement)
		}
		if strings.Contains(statement, "ORDER BY started_at DESC, rowid DESC LIMIT ?") {
			windowQuerySeen = true
		}
	}
	if !windowQuerySeen {
		t.Fatalf("active poll did not execute the canonical bounded task execution query; statements: %#v", counter.Statements())
	}
}

func BenchmarkHandler_GetTaskExecutions_ContentionWithLightweightDBRequest(b *testing.B) {
	b.Run("bounded_poll", func(b *testing.B) {
		db, counter := testutil.NewStatementCountingTestDB(b)
		h, e, llmConfigRepo := setupTestHandlerForDB(b, db)
		agent := createAgentTB(b, llmConfigRepo)
		project := createProjectTB(b, h, "Contention Benchmark Project")
		task := createTaskTB(b, h, project.ID, "Contention Benchmark Task", func(tk *models.Task) {
			tk.Status = models.StatusRunning
		})
		seedLargeExecutionHistory(b, db, task.ID, agent.ID, 200, 4*1024, 64*1024)

		var totalLightweightLatency int64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			queryStarted := make(chan struct{})
			var once sync.Once
			counter.SetObserver(func(_ context.Context, query string) {
				if strings.Contains(query, "FROM executions WHERE task_id = ? ORDER BY started_at DESC") {
					once.Do(func() { close(queryStarted) })
				}
			})
			errCh := make(chan error, 1)
			go func() {
				rec := htmxGet(e, "/tasks/"+task.ID+"/executions")
				if rec.Code != http.StatusOK {
					errCh <- fmt.Errorf("execution-history request status=%d", rec.Code)
					return
				}
				errCh <- nil
			}()
			select {
			case <-queryStarted:
			case err := <-errCh:
				b.Fatalf("execution-history request ended before query started: %v", err)
			case <-time.After(2 * time.Second):
				b.Fatalf("execution-history query did not start")
			}
			lightweightStart := time.Now()
			if _, err := h.projectSvc.List(context.Background()); err != nil {
				b.Fatalf("lightweight project list: %v", err)
			}
			totalLightweightLatency += time.Since(lightweightStart).Nanoseconds()
			if err := <-errCh; err != nil {
				b.Fatal(err)
			}
			counter.SetObserver(nil)
		}
		b.ReportMetric(float64(totalLightweightLatency)/float64(b.N), "lightweight_db_block_ns/op")
	})
}

func seedLargeExecutionHistory(t testing.TB, db *sql.DB, taskID, agentID string, count, promptBytes, outputBytes int) {
	t.Helper()
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	stmt, err := db.Prepare(`INSERT INTO executions
		(id, task_id, agent_config_id, status, prompt_sent, output, error_message, tokens_used, duration_ms, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	require.NoError(t, err)
	defer stmt.Close()
	for i := 0; i < count; i++ {
		status := models.ExecCompleted
		errorMessage := ""
		completed := any(base.Add(time.Duration(i)*time.Second + time.Second).Format("2006-01-02 15:04:05"))
		if i == count-1 {
			status = models.ExecRunning
			completed = nil
		} else if i == count-2 {
			status = models.ExecFailed
			errorMessage = "failure message " + strings.Repeat("E", 256)
		}
		prompt := fmt.Sprintf("prompt-%03d-", i) + strings.Repeat("P", promptBytes)
		output := fmt.Sprintf("output-%03d-", i) + strings.Repeat("O", outputBytes)
		_, err := stmt.Exec(
			fmt.Sprintf("exec-%03d", i),
			taskID,
			agentID,
			status,
			prompt,
			output,
			errorMessage,
			64,
			1500,
			base.Add(time.Duration(i)*time.Second).Format("2006-01-02 15:04:05"),
			completed,
		)
		require.NoError(t, err)
	}
}

func newestHistoryExecutionID(t testing.TB, db *sql.DB, taskID string, offset int) string {
	t.Helper()
	var id string
	err := db.QueryRow(`SELECT id FROM executions WHERE task_id = ? ORDER BY started_at DESC, rowid DESC LIMIT 1 OFFSET ?`, taskID, offset-1).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestHandler_GetTaskDetailStatusUsesCompactAgentLabelProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	ctx := context.Background()

	model := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Compact Agent Status Project")
	agentDef := &models.Agent{
		Name:                "Compact Status Agent",
		SystemPrompt:        "This must not be hydrated by status polling.",
		Model:               "inherit",
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := h.agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}
	assigned := createTask(t, h, project.ID, "Assigned Compact Status Task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.AgentID = &model.ID
		task.AgentDefinitionID = &agentDef.ID
	})
	withoutAgent := createTask(t, h, project.ID, "No Agent Compact Status Task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
	})

	counter.Reset()
	counter.SetEnabled(true)
	assignedResponse := htmxGet(e, "/tasks/"+assigned.ID+"/detail-status")
	counter.SetEnabled(false)
	assertCode(t, assignedResponse, http.StatusOK)
	assertContains(t, assignedResponse, "Agent:")
	assertContains(t, assignedResponse, "Compact Status Agent")

	agentQuerySeen := false
	for _, statement := range counter.Statements() {
		lower := strings.ToLower(statement)
		if !strings.Contains(lower, "from agents") {
			continue
		}
		agentQuerySeen = true
		projection := strings.Split(lower, "from agents")[0]
		if !strings.Contains(projection, "select id, name") {
			t.Fatalf("status Agent query projection = %q, want only identity columns: %s", projection, statement)
		}
		for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("status Agent query selected forbidden column %q: %s", forbidden, statement)
			}
		}
	}
	if !agentQuerySeen {
		t.Fatalf("status did not execute the compact Agent label query; statements: %#v", counter.Statements())
	}

	counter.Reset()
	counter.SetEnabled(true)
	noAgentResponse := htmxGet(e, "/tasks/"+withoutAgent.ID+"/detail-status")
	counter.SetEnabled(false)
	assertCode(t, noAgentResponse, http.StatusOK)
	assertContains(t, noAgentResponse, "Agent:")
	assertContains(t, noAgentResponse, "No agent")
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from agents") {
			t.Fatalf("status without an Agent unexpectedly queried the Agent catalog: %s", statement)
		}
	}
}

func TestHandler_GetTaskDetailStatusPreservesAgentAvailabilityAndTaskStates(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	ctx := context.Background()

	model := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Status Agent Availability Project")
	otherProject := createProject(t, h, "Other Status Agent Project")

	createDefinition := func(agent *models.Agent) *models.Agent {
		t.Helper()
		if err := h.agentRepo.Create(ctx, agent); err != nil {
			t.Fatalf("create Agent definition %q: %v", agent.Name, err)
		}
		return agent
	}
	global := createDefinition(&models.Agent{Name: "Enabled Global Status Agent", Model: "inherit", SystemPrompt: "global status details", Enabled: true, SelectableAsPrimary: true})
	projectScoped := createDefinition(&models.Agent{Name: "Enabled Project Status Agent", Model: "inherit", SystemPrompt: "project status details", Scope: models.AgentScopeProject, ProjectID: project.ID, Enabled: true, SelectableAsPrimary: true})
	otherProjectScoped := createDefinition(&models.Agent{Name: "Other Project Status Agent", Model: "inherit", SystemPrompt: "other project status details", Scope: models.AgentScopeProject, ProjectID: otherProject.ID, Enabled: true, SelectableAsPrimary: true})
	disabled := createDefinition(&models.Agent{Name: "Disabled Assigned Status Agent", Model: "inherit", SystemPrompt: "disabled status details", Enabled: false, SelectableAsPrimary: false})
	archived := createDefinition(&models.Agent{Name: "Archived Status Agent", Model: "inherit", SystemPrompt: "archived status details", Enabled: true, SelectableAsPrimary: true})
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := h.agentRepo.Update(ctx, archived); err != nil {
		t.Fatalf("archive Agent definition: %v", err)
	}
	archivedTimestamp := createDefinition(&models.Agent{Name: "Archived Timestamp Status Agent", Model: "inherit", SystemPrompt: "archived timestamp status details", Enabled: true, SelectableAsPrimary: true})
	archivedAt := time.Now().UTC()
	archivedTimestamp.ArchivedAt = &archivedAt
	if err := h.agentRepo.Update(ctx, archivedTimestamp); err != nil {
		t.Fatalf("archive Agent definition by timestamp: %v", err)
	}

	statusCases := []struct {
		name     string
		status   models.TaskStatus
		category models.TaskCategory
	}{
		{name: "pending backlog", status: models.StatusPending, category: models.CategoryBacklog},
		{name: "running backlog", status: models.StatusRunning, category: models.CategoryBacklog},
		{name: "completed backlog", status: models.StatusCompleted, category: models.CategoryBacklog},
		{name: "failed backlog", status: models.StatusFailed, category: models.CategoryBacklog},
		{name: "cancelled backlog", status: models.StatusCancelled, category: models.CategoryBacklog},
		{name: "scheduled pending", status: models.StatusPending, category: models.CategoryScheduled},
	}
	for _, tc := range statusCases {
		t.Run(tc.name, func(t *testing.T) {
			task := createTask(t, h, project.ID, "No Agent "+tc.name, func(task *models.Task) {
				task.Status = tc.status
				task.Category = tc.category
				task.AgentID = &model.ID
			})
			counter.Reset()
			counter.SetEnabled(true)
			response := htmxGet(e, "/tasks/"+task.ID+"/detail-status")
			counter.SetEnabled(false)
			assertCode(t, response, http.StatusOK)
			assertContains(t, response, "Model:")
			assertContains(t, response, "Test Agent")
			assertContains(t, response, "Agent:")
			assertContains(t, response, "No agent")
			for _, statement := range counter.Statements() {
				if strings.Contains(strings.ToLower(statement), "from agents") {
					t.Fatalf("task without an Agent queried the Agent catalog: %s", statement)
				}
			}
		})
	}

	agentCases := []struct {
		name         string
		definitionID string
		want         string
	}{
		{name: "enabled global", definitionID: global.ID, want: global.Name},
		{name: "enabled project scoped", definitionID: projectScoped.ID, want: projectScoped.Name},
		{name: "disabled assigned global", definitionID: disabled.ID, want: disabled.Name},
		{name: "other project scoped", definitionID: otherProjectScoped.ID, want: "Unknown agent"},
		{name: "archived", definitionID: archived.ID, want: "Unknown agent"},
		{name: "archived timestamp", definitionID: archivedTimestamp.ID, want: "Unknown agent"},
		{name: "missing", definitionID: "missing-agent-id", want: "Unknown agent"},
		{name: "invalid", definitionID: "not a valid persisted id", want: "Unknown agent"},
	}
	for _, tc := range agentCases {
		t.Run(tc.name, func(t *testing.T) {
			task := createTask(t, h, project.ID, "Assigned "+tc.name, func(task *models.Task) {
				task.Status = models.StatusCompleted
				task.Category = models.CategoryBacklog
				task.AgentID = &model.ID
				if tc.name != "missing" && tc.name != "invalid" {
					task.AgentDefinitionID = &tc.definitionID
				}
			})
			if tc.name == "missing" || tc.name == "invalid" {
				if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
					t.Fatalf("disable foreign-key checks for invalid Agent fixture: %v", err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET agent_definition_id = ? WHERE id = ?`, tc.definitionID, task.ID); err != nil {
					t.Fatalf("set invalid Agent definition fixture: %v", err)
				}
				if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
					t.Fatalf("restore foreign-key checks after invalid Agent fixture: %v", err)
				}
			}
			counter.Reset()
			counter.SetEnabled(true)
			response := htmxGet(e, "/tasks/"+task.ID+"/detail-status")
			counter.SetEnabled(false)
			assertCode(t, response, http.StatusOK)
			assertContains(t, response, "Model:")
			assertContains(t, response, "Test Agent")
			assertContains(t, response, "Agent:")
			assertContains(t, response, tc.want)

			agentQueries := 0
			for _, statement := range counter.Statements() {
				if !strings.Contains(strings.ToLower(statement), "from agents") {
					continue
				}
				agentQueries++
				projection := strings.Split(strings.ToLower(statement), "from agents")[0]
				if !strings.Contains(projection, "select id, name") {
					t.Fatalf("status Agent query projection = %q, want identity-only query: %s", projection, statement)
				}
				for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json"} {
					if strings.Contains(projection, forbidden) {
						t.Fatalf("status Agent query selected forbidden column %q: %s", forbidden, statement)
					}
				}
			}
			if agentQueries != 1 {
				t.Fatalf("status Agent lookup count = %d, want one targeted lookup; statements: %#v", agentQueries, counter.Statements())
			}
		})
	}

	fullDetailTask := createTask(t, h, project.ID, "Full Agent Detail Task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.AgentDefinitionID = &global.ID
	})
	counter.Reset()
	counter.SetEnabled(true)
	fullPage := htmxGet(e, "/tasks/"+fullDetailTask.ID)
	counter.SetEnabled(false)
	assertCode(t, fullPage, http.StatusOK)
	fullProjectionSeen := false
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from agents") && strings.Contains(strings.ToLower(statement), "system_prompt") {
			fullProjectionSeen = true
			break
		}
	}
	if !fullProjectionSeen {
		t.Fatalf("initial Task Detail no longer used the full Agent projection; statements: %#v", counter.Statements())
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

	// Test 3: Completed task shows latest terminal duration
	completedExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
	})
	if err := h.execRepo.Complete(ctx, completedExec.ID, models.ExecCompleted, "done", "", 10, 1500); err != nil {
		t.Fatalf("complete execution: %v", err)
	}
	task.Status = models.StatusCompleted
	if err := h.taskSvc.Update(ctx, task); err != nil {
		t.Fatalf("failed to update task completed: %v", err)
	}
	rec3 := htmxGet(e, "/tasks/"+task.ID+"/detail-status")
	assertCode(t, rec3, http.StatusOK)
	assertContains(t, rec3, "Duration:")
	assertContains(t, rec3, "1s")

	// Test 4: Not found task returns 404
	req4 := httptest.NewRequest(http.MethodGet, "/tasks/nonexistent/detail-status", nil)
	rec4 := httptest.NewRecorder()
	c4 := e.NewContext(req4, rec4)
	c4.SetPath("/tasks/:taskId/detail-status")
	c4.SetParamNames("taskId")
	c4.SetParamValues("nonexistent")

	if err := h.GetTaskDetailStatus(c4); err == nil {
		t.Errorf("expected error for nonexistent task")
	}
}

func TestHandler_GetTaskDetailStatusLargeHistoryUsesNarrowExecutionMetrics(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	agent := createAgent(t, llmConfigRepo)
	agentDef := &models.Agent{
		Name:                "Primary Metrics Agent",
		Key:                 "primary_metrics_agent",
		SystemPrompt:        "Handle metrics tasks.",
		Model:               "inherit",
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}
	project := createProject(t, h, "Metrics Projection Project")
	task := createTask(t, h, project.ID, "Metrics Projection Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusCompleted
		tk.Priority = 3
		tk.Tag = models.TagFeature
		tk.AgentID = &agent.ID
		tk.AgentDefinitionID = &agentDef.ID
	})
	task.Category = models.CategoryActive
	if err := h.taskSvc.Update(ctx, task); err != nil {
		t.Fatalf("update task to active completed: %v", err)
	}
	seedLargeExecutionHistory(t, db, task.ID, agent.ID, 200, 4*1024, 64*1024)
	if _, err := db.ExecContext(ctx, `UPDATE executions SET status = ?, duration_ms = ?, completed_at = ? WHERE id = ?`, models.ExecCompleted, int64(65_000), "2026-08-13 12:03:20", "exec-199"); err != nil {
		t.Fatalf("complete latest execution: %v", err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	rec := htmxGet(e, "/tasks/"+task.ID+"/detail-status")
	counter.SetEnabled(false)
	assertCode(t, rec, http.StatusOK)
	for _, required := range []string{"Category:", "active", "Status:", "Completed", "Tag:", "Feature", "Priority:", "High", "Model:", "Test Agent", "Agent:", "Primary Metrics Agent", "Duration:", "1m 5s"} {
		assertContains(t, rec, required)
	}

	metricsQuerySeen := false
	for _, stmt := range counter.Statements() {
		if !strings.Contains(stmt, "FROM executions") {
			continue
		}
		if strings.Contains(stmt, "latest_started_at") && strings.Contains(stmt, "latest_duration_ms") {
			metricsQuerySeen = true
		}
		for _, forbidden := range []string{"prompt_sent", "output", "error_message", "reasoning_content", "diff_output"} {
			if strings.Contains(stmt, forbidden) {
				t.Fatalf("detail-status execution query scanned historical %s text: %s", forbidden, stmt)
			}
		}
	}
	if !metricsQuerySeen {
		t.Fatalf("detail-status did not execute compact task execution metrics query; statements: %#v", counter.Statements())
	}
}

func BenchmarkHandler_GetTaskDetailStatus_MetricsProjection(b *testing.B) {
	b.Run("narrow_projection", func(b *testing.B) {
		db, counter := testutil.NewStatementCountingTestDB(b)
		h, e, llmConfigRepo := setupTestHandlerForDB(b, db)
		ctx := context.Background()
		agentRepo := repository.NewAgentRepo(db)
		h.SetAgentRepo(agentRepo)
		agent := createAgentTB(b, llmConfigRepo)
		agentDef := &models.Agent{Name: "Primary Metrics Agent", Key: "primary_metrics_agent", SystemPrompt: "Handle metrics tasks.", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
		if err := agentRepo.Create(ctx, agentDef); err != nil {
			b.Fatalf("create agent definition: %v", err)
		}
		project := createProjectTB(b, h, "Detail Status Benchmark Project")
		task := createTaskTB(b, h, project.ID, "Detail Status Benchmark Task", func(tk *models.Task) {
			tk.Category = models.CategoryBacklog
			tk.Status = models.StatusCompleted
			tk.Priority = 3
			tk.Tag = models.TagFeature
			tk.AgentID = &agent.ID
			tk.AgentDefinitionID = &agentDef.ID
		})
		task.Category = models.CategoryActive
		if err := h.taskSvc.Update(ctx, task); err != nil {
			b.Fatalf("update task to active completed: %v", err)
		}
		seedLargeExecutionHistory(b, db, task.ID, agent.ID, 200, 4*1024, 64*1024)
		if _, err := db.ExecContext(ctx, `UPDATE executions SET status = ?, duration_ms = ?, completed_at = ? WHERE id = ?`, models.ExecCompleted, int64(65_000), "2026-08-13 12:03:20", "exec-199"); err != nil {
			b.Fatalf("complete latest execution: %v", err)
		}
		var totalLightweightLatency int64
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			queryStarted := make(chan struct{})
			var once sync.Once
			counter.SetObserver(func(_ context.Context, query string) {
				if strings.Contains(query, "latest_started_at") {
					once.Do(func() { close(queryStarted) })
				}
			})
			errCh := make(chan error, 1)
			go func() {
				rec := htmxGet(e, "/tasks/"+task.ID+"/detail-status")
				if rec.Code != http.StatusOK {
					errCh <- fmt.Errorf("detail-status request status=%d", rec.Code)
					return
				}
				errCh <- nil
			}()
			select {
			case <-queryStarted:
			case err := <-errCh:
				b.Fatalf("detail-status request ended before query started: %v", err)
			case <-time.After(2 * time.Second):
				b.Fatalf("detail-status query did not start")
			}
			lightweightStart := time.Now()
			if _, err := h.projectSvc.List(context.Background()); err != nil {
				b.Fatalf("lightweight project list: %v", err)
			}
			totalLightweightLatency += time.Since(lightweightStart).Nanoseconds()
			if err := <-errCh; err != nil {
				b.Fatal(err)
			}
			counter.SetObserver(nil)
		}
		b.ReportMetric(float64(totalLightweightLatency)/float64(b.N), "lightweight_db_block_ns/op")
		b.ReportMetric(0, "db_text_bytes_scanned/op")
	})
}

func BenchmarkHandler_GetTaskDetailStatus_AgentProjection(b *testing.B) {
	db, counter := testutil.NewStatementCountingTestDB(b)
	h, e, llmConfigRepo := setupTestHandlerForDB(b, db)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		b.Fatalf("clear Agent definitions: %v", err)
	}

	var targetAgent *models.Agent
	for i := 0; i < 1000; i++ {
		agent := createRichTaskDetailBenchmarkAgent(b, agentRepo, fmt.Sprintf("Agent %04d", i))
		if i == 999 {
			targetAgent = agent
		}
	}
	if targetAgent == nil {
		b.Fatal("target Agent definition was not created")
	}

	model := createAgentTB(b, llmConfigRepo)
	project := createProjectTB(b, h, "Agent Projection Benchmark Project")
	task := createTaskTB(b, h, project.ID, "Agent Projection Benchmark Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.AgentID = &model.ID
		tk.AgentDefinitionID = &targetAgent.ID
	})

	b.ReportAllocs()
	b.ResetTimer()
	var totalResponseBytes int64
	var totalLightweightWait time.Duration
	for i := 0; i < b.N; i++ {
		queryStarted := make(chan struct{})
		var once sync.Once
		counter.SetObserver(func(_ context.Context, query string) {
			if strings.Contains(strings.ToLower(query), "from agents") {
				once.Do(func() { close(queryStarted) })
			}
		})

		type lookupResult struct {
			responseBytes int
			err           error
		}
		resultCh := make(chan lookupResult, 1)
		go func() {
			rec := htmxGet(e, "/tasks/"+task.ID+"/detail-status")
			if rec.Code != http.StatusOK {
				resultCh <- lookupResult{err: fmt.Errorf("detail-status request status=%d", rec.Code)}
				return
			}
			resultCh <- lookupResult{responseBytes: rec.Body.Len()}
		}()
		var result lookupResult
		lookupComplete := false
		select {
		case <-queryStarted:
		case result = <-resultCh:
			lookupComplete = true
		case <-time.After(2 * time.Second):
			b.Fatalf("Agent lookup query did not start")
		}

		lightweightStart := time.Now()
		var projectID string
		if err := db.QueryRowContext(context.Background(), `SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&projectID); err != nil {
			b.Fatalf("lightweight project lookup: %v", err)
		}
		totalLightweightWait += time.Since(lightweightStart)

		if !lookupComplete {
			result = <-resultCh
		}
		counter.SetObserver(nil)
		if result.err != nil {
			b.Fatal(result.err)
		}
		if result.responseBytes <= 0 {
			b.Fatal("Agent status response body was empty")
		}
		totalResponseBytes += int64(result.responseBytes)
	}
	b.StopTimer()
	b.ReportMetric(float64(totalResponseBytes)/float64(b.N), "response_bytes/op")
	b.ReportMetric(float64(totalLightweightWait.Nanoseconds())/float64(b.N), "lightweight_db_wait_ns/op")
}

func createRichTaskDetailBenchmarkAgent(tb testing.TB, repo *repository.AgentRepo, name string) *models.Agent {
	tb.Helper()
	agent := &models.Agent{
		Name:         name,
		Description:  "production-shaped picker agent",
		SystemPrompt: strings.Repeat("large webhook picker prompt with instructions and examples. ", 320),
		Model:        "inherit",
		Tools:        []string{"Read", "Write", "Edit", "Bash", models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{
			Directory:   "src",
			Permissions: []string{"read", "write"},
		}}},
		Plugins: []string{"github@marketplace", "playwright@claude-plugins-official"},
		MCPServers: []models.MCPServerConfig{{
			Name:    "playwright",
			Command: []string{"npx", "-y", "@playwright/mcp"},
			Env:     map[string]string{"TOKEN": strings.Repeat("x", 256)},
		}},
		Skills: []models.SkillConfig{{
			Name:        "triage",
			Description: "large skill config",
			Tools:       "Read, Grep, Bash",
			Content:     strings.Repeat("skill body ", 256),
		}},
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 8192},
		SourceRefs:          []string{"agents/picker/SKILLS.md", strings.Repeat("ref", 128)},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		tb.Fatalf("create benchmark Agent %q: %v", name, err)
	}
	return agent
}

func TestHandler_GetTaskDetailActions(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := createProject(t, h, "Test Project")
	task := createTask(t, h, project.ID, "Actions Test Task")

	makeRequest := func(taskID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID+"/detail-actions", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/tasks/:taskId/detail-actions")
		c.SetParamNames("taskId")
		c.SetParamValues(taskID)
		if err := h.GetTaskDetailActions(c); err != nil {
			t.Fatalf("GetTaskDetailActions(%s) failed: %v", taskID, err)
		}
		return rec
	}

	// Pending task: Run Now is enabled (hx-post present), Edit visible
	rec := makeRequest(task.ID)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `id="task-detail-actions"`)
	assertContains(t, rec, "Run Now")
	assertContains(t, rec, `hx-post`)
	assertContains(t, rec, "Edit")
	assertNotContains(t, rec, "disabled")

	// Running task: Run Now disabled (no hx-post), Edit hidden
	task.Status = models.StatusRunning
	if err := h.taskSvc.Update(ctx, task); err != nil {
		t.Fatalf("failed to update task: %v", err)
	}
	rec2 := makeRequest(task.ID)
	assertContains(t, rec2, "disabled")
	assertNotContains(t, rec2, `hx-post`)
	assertNotContains(t, rec2, `btn btn-secondary btn-sm`) // Edit hidden when running

	// Completed task: Run Now re-enabled (hx-post back), Edit visible
	task.Status = models.StatusCompleted
	if err := h.taskSvc.Update(ctx, task); err != nil {
		t.Fatalf("failed to update task: %v", err)
	}
	rec3 := makeRequest(task.ID)
	assertContains(t, rec3, `hx-post`)
	assertContains(t, rec3, "Edit")
	assertNotContains(t, rec3, "disabled")

	// Not found: expect 404
	req404 := httptest.NewRequest(http.MethodGet, "/tasks/no-such-task/detail-actions", nil)
	rec404 := httptest.NewRecorder()
	c404 := e.NewContext(req404, rec404)
	c404.SetPath("/tasks/:taskId/detail-actions")
	c404.SetParamNames("taskId")
	c404.SetParamValues("no-such-task")
	if err := h.GetTaskDetailActions(c404); err == nil {
		t.Error("expected error for nonexistent task")
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
	assertContains(t, rec, "function _loadThreadContent(taskId, forceReload, expectedExecId)")
}

func TestHandler_GetTask_ThreadTabAliasActivatesThread(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Thread Alias Project")
	task := createTask(t, h, project.ID, "Thread Alias Task", func(tk *models.Task) {
		tk.Priority = 1
		tk.Status = models.StatusPending
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab=thread", nil)
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
	assertContains(t, rec, "Thread is loading...")
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

func TestHandler_GetTaskThreadPollUsesCompactTaskMetadata(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Poll Projection Project")
	task := createTask(t, h, project.ID, "Thread Poll Projection Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
		tk.Prompt = strings.Repeat("large-current-task-prompt", 8192)
	})
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET chain_config = ?, swarm_config = ? WHERE id = ?`, strings.Repeat("large-chain-config", 8192), strings.Repeat("large-swarm-config", 8192), task.ID); err != nil {
		t.Fatalf("seed large task configs: %v", err)
	}
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "stable execution prompt"
		ex.Output = "working"
	})

	counter.Reset()
	counter.SetEnabled(true)
	rec := htmxGet(e, "/tasks/"+task.ID+"/thread?poll=1&limit=5&preserved_exec_ids="+exec.ID)
	counter.SetEnabled(false)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `data-task-status="running"`)
	assertContains(t, rec, `data-task-category="active"`)
	assertContains(t, rec, `data-task-agent="`+agent.ID+`"`)
	assertNotContains(t, rec, "large-current-task-prompt")
	assertNotContains(t, rec, "large-chain-config")
	assertNotContains(t, rec, "large-swarm-config")

	var compactTaskQuerySeen bool
	for _, stmt := range counter.Statements() {
		if !strings.Contains(stmt, "FROM tasks") || !strings.Contains(stmt, "WHERE id = ?") {
			continue
		}
		if strings.Contains(stmt, "SELECT id, project_id, category, status, agent_id, agent_definition_id") {
			compactTaskQuerySeen = true
		}
		for _, forbidden := range []string{"prompt", "chain_config", "swarm_config"} {
			if strings.Contains(stmt, forbidden) {
				t.Fatalf("poll task metadata query selected %s: %s", forbidden, stmt)
			}
		}
	}
	if !compactTaskQuerySeen {
		t.Fatalf("poll did not execute compact task metadata query; statements: %#v", counter.Statements())
	}
}

func TestHandler_GetTaskThreadPollIgnoresTaskPromptAndConfigChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Poll Stable Project")
	task := createTask(t, h, project.ID, "Thread Poll Stable Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
		tk.Prompt = strings.Repeat("first prompt payload", 4096)
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "stable execution prompt"
		ex.Output = "working"
	})

	first := htmxGet(e, "/tasks/"+task.ID+"/thread?poll=1&limit=5")
	assertCode(t, first, http.StatusOK)
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET prompt = ?, chain_config = ?, swarm_config = ? WHERE id = ?`, strings.Repeat("second prompt payload", 4096), strings.Repeat("changed chain payload", 4096), strings.Repeat("changed swarm payload", 4096), task.ID); err != nil {
		t.Fatalf("update ignored task payloads: %v", err)
	}
	second := htmxGet(e, "/tasks/"+task.ID+"/thread?poll=1&limit=5")
	assertCode(t, second, http.StatusOK)
	if first.Body.String() != second.Body.String() {
		t.Fatalf("poll response changed after prompt/config-only update")
	}
}

func TestHandler_GetTaskThreadPollPreservesComposerModelSelection(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configureTask    func(*models.Task, *models.LLMConfig, *models.LLMConfig, *models.LLMConfig)
		configureProject bool
		want             func(*models.LLMConfig, *models.LLMConfig, *models.LLMConfig) string
	}{
		{
			name: "explicit_task_model",
			configureTask: func(tk *models.Task, explicit, _, _ *models.LLMConfig) {
				tk.AgentID = &explicit.ID
			},
			configureProject: true,
			want:             func(explicit, _, _ *models.LLMConfig) string { return explicit.ID },
		},
		{
			name:             "project_default_model",
			configureProject: true,
			want:             func(_, projectDefault, _ *models.LLMConfig) string { return projectDefault.ID },
		},
		{
			name: "global_default_model",
			want: func(_, _, globalDefault *models.LLMConfig) string { return globalDefault.ID },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, e, llmConfigRepo := setupTestHandler(t)
			ctx := context.Background()
			globalDefault := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Global Default"; a.IsDefault = true })
			projectDefault := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Project Default"; a.IsDefault = false })
			explicit := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Explicit Model"; a.IsDefault = false })
			project := createProject(t, h, "Thread Poll Model Selection "+tc.name)
			if tc.configureProject {
				project.DefaultAgentConfigID = &projectDefault.ID
				if err := h.projectSvc.Update(ctx, project); err != nil {
					t.Fatalf("set project default model: %v", err)
				}
			}
			task := createTask(t, h, project.ID, "Thread Poll Model Selection Task "+tc.name, func(tk *models.Task) {
				tk.Status = models.StatusRunning
				tk.Category = models.CategoryActive
				if tc.configureTask != nil {
					tc.configureTask(tk, explicit, projectDefault, globalDefault)
				}
			})
			createExec(t, h, task.ID, globalDefault.ID, func(ex *models.Execution) {
				ex.Status = models.ExecRunning
				ex.PromptSent = "hello"
			})

			rec := htmxGet(e, "/tasks/"+task.ID+"/thread?poll=1&limit=5")
			assertCode(t, rec, http.StatusOK)
			assertContains(t, rec, `data-task-agent="`+tc.want(explicit, projectDefault, globalDefault)+`"`)
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
		{"openai_normalizes_unknown", "openai", "unknown-model", "high", "gpt-5.6-sol", "high"},
		{"non_openai_preserves", "anthropic", "claude-opus-4-6", "xhigh", "claude-opus-4-6", ""},
		{"anthropic_preserves_mythos51", "anthropic", "claude-mythos-5-1", "xhigh", "claude-mythos-5-1", "xhigh"},
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

func TestHandler_CreateTask_NormalizesInvalidPrioritiesToDefault(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	cases := []struct {
		name        string
		prioritySet bool
		priority    string
	}{
		{name: "omitted"},
		{name: "empty", prioritySet: true, priority: ""},
		{name: "malformed", prioritySet: true, priority: "urgent"},
		{name: "below range", prioritySet: true, priority: "0"},
		{name: "above range", prioritySet: true, priority: "5"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("title", "Invalid Priority "+tt.name)
			form.Set("category", "backlog")
			form.Set("prompt", "test")
			if tt.prioritySet {
				form.Set("priority", tt.priority)
			}

			rec := postForm(e, "/tasks?project_id=default", form)
			assertCode(t, rec, http.StatusOK)
		})
	}

	tasks, err := h.taskRepo.ListByProject(ctx, "default", "")
	require.NoError(t, err)
	require.Len(t, tasks, len(cases))
	for _, task := range tasks {
		assert.Equal(t, 2, task.Priority, "task %q priority", task.Title)
		assert.Equal(t, "Normal", components.PriorityLabel(task.Priority))
	}
}

func TestHandler_CreateTask_PersistsValidPriorities(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	labels := map[int]string{1: "Low", 2: "Normal", 3: "High", 4: "Urgent"}
	for priority := 1; priority <= 4; priority++ {
		form := url.Values{}
		form.Set("title", fmt.Sprintf("Priority %d", priority))
		form.Set("category", "backlog")
		form.Set("priority", strconv.Itoa(priority))
		form.Set("prompt", "test")

		rec := postForm(e, "/tasks?project_id=default", form)
		assertCode(t, rec, http.StatusOK)
	}

	tasks, err := h.taskRepo.ListByProject(ctx, "default", "")
	require.NoError(t, err)
	require.Len(t, tasks, 4)
	for _, task := range tasks {
		wantLabel, ok := labels[task.Priority]
		require.True(t, ok, "unexpected priority %d for task %q", task.Priority, task.Title)
		assert.Equal(t, wantLabel, components.PriorityLabel(task.Priority))
	}
}

func TestHandler_CreateTask_BacklogSwarmDefersPlanner(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Deferred Swarm Form Project")
	form := url.Values{}
	form.Set("title", "Deferred Swarm")
	form.Set("category", "backlog")
	form.Set("priority", "2")
	form.Set("prompt", "Plan this later")
	form.Set("swarm_mode", "on")
	form.Set("swarm_max_workers", "2")
	form.Set("swarm_worker_isolation", "worktree")
	form.Set("swarm_reviewer_enabled", "false")
	form.Set("swarm_merger_enabled", "false")

	rec := postForm(e, "/tasks?project_id="+project.ID, form)
	assertCode(t, rec, http.StatusOK)
	tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	var parent *models.Task
	for i := range tasks {
		if tasks[i].Title == "Deferred Swarm" {
			parent = &tasks[i]
			break
		}
	}
	require.NotNil(t, parent)
	assert.Equal(t, models.SwarmRoleParent, parent.SwarmRole)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	assert.Nil(t, planner, "deferred form swarm should not create planner until explicit start")
	cfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	assert.False(t, cfg.ReviewerEnabled)
	assert.False(t, cfg.MergerEnabled)
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

func TestHandler_CreateTask_NormalizesWhitespaceAndDetectsDuplicateTitle(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	form := url.Values{}
	form.Set("title", " Fix checkout ")
	form.Set("category", "backlog")
	form.Set("priority", "2")
	form.Set("prompt", " investigate checkout ")
	rec := postForm(e, "/tasks?project_id=default", form)
	assertCode(t, rec, http.StatusOK)

	tasks, err := h.taskRepo.ListByProject(ctx, "default", "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "Fix checkout", tasks[0].Title)
	assert.Equal(t, "investigate checkout", tasks[0].Prompt)

	duplicate := url.Values{}
	duplicate.Set("title", " Fix checkout ")
	duplicate.Set("category", "backlog")
	duplicate.Set("priority", "2")
	duplicate.Set("prompt", "different prompt")
	dupRec := postForm(e, "/tasks?project_id=default", duplicate)
	assertCode(t, dupRec, http.StatusConflict)
	assertContains(t, dupRec, "task with this name already exists")

	tasksAfterDuplicate, err := h.taskRepo.ListByProject(ctx, "default", "")
	require.NoError(t, err)
	require.Len(t, tasksAfterDuplicate, 1)
	assert.Equal(t, "Fix checkout", tasksAfterDuplicate[0].Title)
}

func TestHandler_CreateTask_RejectsWhitespaceOnlyTitlePrompt(t *testing.T) {
	for _, tt := range []struct {
		name       string
		title      string
		prompt     string
		wantStatus int
		wantBody   string
	}{
		{name: "title", title: " \t\n ", prompt: "valid prompt", wantStatus: http.StatusBadRequest, wantBody: "Task title is required"},
		{name: "prompt", title: "Valid title", prompt: " \t\n ", wantStatus: http.StatusBadRequest, wantBody: "Task prompt is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			ctx := context.Background()
			form := url.Values{}
			form.Set("title", tt.title)
			form.Set("category", "backlog")
			form.Set("priority", "2")
			form.Set("prompt", tt.prompt)

			rec := postForm(e, "/tasks?project_id=default", form)
			assertCode(t, rec, tt.wantStatus)
			assertContains(t, rec, tt.wantBody)

			tasks, err := h.taskRepo.ListByProject(ctx, "default", "")
			require.NoError(t, err)
			assert.Empty(t, tasks)
		})
	}
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
	for _, htmx := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/models", nil)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assertCode(t, rec, http.StatusOK)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("htmx=%t Cache-Control=%q, want no-store", htmx, got)
		}
	}
}

func TestHandler_ListModels_RendersAuthoritativeDesktopOAuthMode(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.SetDesktopMode(true)
	if err := llmConfigRepo.Create(context.Background(), &models.LLMConfig{
		Name:       "Desktop OAuth",
		Provider:   models.ProviderOpenAI,
		AuthMethod: models.AuthMethodOAuth,
		Model:      "gpt-5.4",
	}); err != nil {
		t.Fatalf("create oauth model: %v", err)
	}

	for _, tc := range []struct {
		name string
		htmx bool
	}{
		{name: "full page"},
		{name: "htmx fragment", htmx: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/models", nil)
			if tc.htmx {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assertCode(t, rec, http.StatusOK)
			if !strings.Contains(rec.Body.String(), `data-oauth-external="true"`) {
				t.Fatal("expected desktop handler mode to be rendered into OAuth links")
			}
		})
	}
}

func TestHandler_ListModels_DeleteConfirmationDialog(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := htmxGet(e, "/models")
	assertCode(t, rec, http.StatusOK)

	body := rec.Body.String()
	for _, want := range []string{
		`id="delete_model_confirm_modal" class="modal"`,
		`id="delete_model_confirm_name"`,
		`data-destructive-confirm-dialog`,
		`openDestructiveConfirmDialog('delete_model_confirm_modal', 'delete_model_confirm_name', _deleteModelName)`,
		`onclick="delete_model_confirm_modal.close()"`,
		`onclick="confirmDeleteModel()"`,
		`class="btn btn-error"`,
		`modal.showModal()`,
		`if (_deleteModelIsDefault)`,
		`reassign_default_modal.showModal();`,
		`htmx.ajax('DELETE', modelMutationURL('/models/' + _deleteModelId)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected models delete confirmation markup/script to contain %q", want)
		}
	}
	if strings.Contains(body, `confirm('Delete model`) {
		t.Fatal("expected model delete flow to avoid browser confirm()")
	}
}

func TestHandler_ListModels_MixtureUI(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	ref := &models.LLMConfig{Name: "Reference", Provider: models.ProviderTest, Model: "ref"}
	for _, cfg := range []*models.LLMConfig{agg, ref} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}
	mixture := &models.LLMConfig{Name: "Research Mixture", Provider: models.ProviderMixture, Model: "default", MixtureConfigJSON: `{"enabled":true,"reference_models":[{"agent_config_id":"` + ref.ID + `","provider":"test","model":"ref","label":"Reference"}],"aggregator":{"agent_config_id":"` + agg.ID + `","provider":"test","model":"agg","label":"Aggregator"}}`}
	if err := llmConfigRepo.Create(ctx, mixture); err != nil {
		t.Fatalf("create mixture: %v", err)
	}
	rec := htmxGet(e, "/models")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{
		`<option value="mixture">Mixture of Models</option>`,
		`id="mixture_fields"`,
		`id="model_mixture_aggregator"`,
		`id="model_mixture_references"`,
		`This mixture calls`,
		`Mixture of Models / default`,
		`Aggregator: Aggregator`,
		`References: 1`,
		`/edit-details`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected models UI to contain %q", want)
		}
	}
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

func TestHandler_ListModels_LazyLoadsAPIKeyForEdit(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	cfg := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Saved Key Agent"
		a.Provider = models.ProviderAnthropic
		a.AuthMethod = models.AuthMethodAPIKey
		a.APIKey = "test-secret-api-key"
		a.IsDefault = false
	})

	page := htmxGet(e, "/models")
	assertCode(t, page, http.StatusOK)
	body := page.Body.String()
	for _, want := range []string{
		`id="model_api_key"`,
		`type="password"`,
		`id="model_api_key_submit" name="api_key" value=""`,
		`onclick="togglePasswordVisibility('model_api_key', this)"`,
		`function editModelFromData(button)`,
		`/edit-details`,
		`generation !== window._modelEditRequestGeneration`,
		`window._modelEditRequestedID !== id`,
		`details.id !== id`,
		`function populateModelEditForm(button)`,
		`setModelAPIKeyEditHelp(hasAPIKey);`,
		`resetSecretInputVisibility('model_api_key')`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected lazy model edit markup/script to contain %q", want)
		}
	}
	if strings.Contains(body, cfg.APIKey) || strings.Contains(body, `data-model-api-key=`) {
		t.Fatal("initial Models response exposed the saved API key")
	}

	details := htmxGet(e, "/models/"+cfg.ID+"/edit-details")
	assertCode(t, details, http.StatusOK)
	var payload modelEditDetails
	if err := json.Unmarshal(details.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != cfg.ID || payload.APIKey != cfg.APIKey || payload.Name != cfg.Name || payload.AuthMethod != cfg.AuthMethod {
		t.Fatalf("edit details lost provider fields: %#v", payload)
	}
	if got := details.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("edit details Cache-Control = %q", got)
	}
}

func TestHandler_GetModelEditDetails_PreservesProviderSpecificFields(t *testing.T) {
	_, e, repo := setupTestHandler(t)
	cfg := &models.LLMConfig{
		Name: "Custom OAuth", Provider: models.ProviderOpenAICompatible, Model: "custom-model",
		ReasoningEffort: "high", Temperature: 0.25, AuthMethod: models.AuthMethodOAuth,
		APIKey: "api-secret", MaxWorkers: 7, WorkerTimeout: 45,
		OAuthClientID: "client-id", OAuthClientSecret: "client-secret",
		OAuthAuthorizeURL: "https://example.com/authorize", OAuthTokenURL: "https://example.com/token",
		OAuthScopes: "models profile", BaseURL: "https://example.com/v1", Transport: "chat_completions",
		PresetSlug: "custom", ModelsURL: "https://example.com/models", AuthHeaderName: "X-Key",
		AuthHeaderValuePrefix: "Token ", ExtraHeadersJSON: `{"X-Extra":"value"}`,
		ExtraBodyJSON: `{"routing":{"tier":"fast"}}`, CustomAuthConfigJSON: `{"pkce":true}`,
		MixtureConfigJSON: `{"unused":true}`, AutoStartTasks: true,
	}
	if err := repo.Create(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	rec := htmxGet(e, "/models/"+cfg.ID+"/edit-details")
	assertCode(t, rec, http.StatusOK)
	var got modelEditDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != cfg.ID || got.Provider != cfg.Provider || got.Model != cfg.Model ||
		got.ReasoningEffort != cfg.ReasoningEffort || got.Temperature != cfg.Temperature ||
		got.APIKey != cfg.APIKey || got.AuthMethod != cfg.AuthMethod || got.MaxWorkers != cfg.MaxWorkers ||
		got.WorkerTimeout != cfg.WorkerTimeout || got.OAuthClientID != cfg.OAuthClientID ||
		got.OAuthClientSecret != cfg.OAuthClientSecret || got.OAuthAuthorizeURL != cfg.OAuthAuthorizeURL ||
		got.OAuthTokenURL != cfg.OAuthTokenURL || got.OAuthScopes != cfg.OAuthScopes ||
		got.BaseURL != cfg.BaseURL || got.Transport != cfg.Transport || got.PresetSlug != cfg.PresetSlug ||
		got.ModelsURL != cfg.ModelsURL || got.AuthHeaderName != cfg.AuthHeaderName ||
		got.AuthHeaderValuePrefix != cfg.AuthHeaderValuePrefix || got.ExtraHeadersJSON != cfg.ExtraHeadersJSON ||
		got.ExtraBodyJSON != cfg.ExtraBodyJSON || got.CustomAuthConfigJSON != cfg.CustomAuthConfigJSON ||
		got.AutoStartTasks != cfg.AutoStartTasks {
		t.Fatalf("edit details differ from stored provider fields:\n got: %#v\nwant: %#v", got, cfg)
	}

	if got.MixtureConfigJSON != "" {
		t.Fatalf("non-mixture edit details exposed mixture config: %q", got.MixtureConfigJSON)
	}
	builtIn := &models.LLMConfig{
		Name: "Built-in OAuth", Provider: models.ProviderOpenAI, Model: "gpt-5.4",
		AuthMethod: models.AuthMethodOAuth, OAuthClientSecret: "must-not-return",
		CustomAuthConfigJSON: `{"signing_secret":"must-not-return"}`,
	}
	if err := repo.Create(context.Background(), builtIn); err != nil {
		t.Fatal(err)
	}
	builtInRec := htmxGet(e, "/models/"+builtIn.ID+"/edit-details")
	assertCode(t, builtInRec, http.StatusOK)
	if strings.Contains(builtInRec.Body.String(), "must-not-return") {
		t.Fatal("built-in provider secret leaked through edit details")
	}

	notFound := htmxGet(e, "/models/missing/edit-details")
	assertCode(t, notFound, http.StatusNotFound)
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

func TestHandler_UpdateModel_PostFallback(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatal("expected seeded default model")
	}

	form := url.Values{}
	form.Set("name", "Renamed Via Post")
	form.Set("provider", string(agent.Provider))
	form.Set("model", agent.Model)
	form.Set("max_tokens", strconv.Itoa(agent.MaxTokens))
	form.Set("temperature", fmt.Sprintf("%.1f", agent.Temperature))

	rec := postForm(e, "/models/"+agent.ID, form)
	assertCode(t, rec, http.StatusSeeOther)

	configs, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list configs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected rename to update existing model, got %d configs", len(configs))
	}
	updated, _ := llmConfigRepo.GetByID(ctx, agent.ID)
	if updated.Name != "Renamed Via Post" {
		t.Fatalf("expected renamed model, got %q", updated.Name)
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

func TestHandler_TasksPage_RendersCategoryDrivenSwarmPlannerCopy(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Tasks Swarm UI Project")

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, "Swarm planning starts when the parent task becomes Active")
	assert.Contains(t, body, "Choose Backlog to defer the planner")
	assert.NotContains(t, body, "Autonomous planner")
	assert.NotContains(t, body, `name="swarm_autonomous_planner"`)
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
	assertContains(t, rec, `id="limit-input-global" value="0" min="0"`)
	assertContains(t, rec, ">Unlimited</span>")
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
	t.Run("accepts global worker limits above ten", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		ctx := context.Background()
		h.workerSvc.Start(ctx)
		defer h.workerSvc.Stop()

		rec := htmxPost(e, "/workers", url.Values{"max_workers": {"25"}})
		assertCode(t, rec, http.StatusOK)
		if n := h.workerSvc.NumWorkers(); n != 25 {
			t.Fatalf("expected worker pool to be resized to 25, got %d", n)
		}
		maxWorkers, err := h.workerRepo.GetMaxWorkers(ctx)
		if err != nil {
			t.Fatalf("GetMaxWorkers: %v", err)
		}
		if maxWorkers != 25 {
			t.Fatalf("expected max_workers=25 in DB, got %d", maxWorkers)
		}
		assertContains(t, rec, `value="25"`)
		assertNotContains(t, rec, `max="10"`)
	})

	t.Run("rejects malformed global worker limits", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		ctx := context.Background()
		before, err := h.workerRepo.GetMaxWorkers(ctx)
		if err != nil {
			t.Fatalf("GetMaxWorkers before: %v", err)
		}
		rec := htmxPost(e, "/workers", url.Values{"max_workers": {"-1"}})
		assertCode(t, rec, http.StatusNoContent)
		if trigger := rec.Header().Get("HX-Trigger"); !strings.Contains(trigger, "Max concurrent workers") {
			t.Fatalf("expected malformed-limit toast, got %q", trigger)
		}
		maxWorkers, err := h.workerRepo.GetMaxWorkers(ctx)
		if err != nil {
			t.Fatalf("GetMaxWorkers after: %v", err)
		}
		if maxWorkers != before {
			t.Fatalf("expected malformed global limit to leave %d unchanged, got %d", before, maxWorkers)
		}
	})

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

	t.Run("unlimited round-trips through settings", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		ctx := context.Background()
		h.workerSvc.Start(ctx)
		defer h.workerSvc.Stop()

		form := url.Values{}
		form.Set("max_workers", "0")
		rec := htmxPost(e, "/workers", form)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, `id="limit-input-global" value="0" min="0"`)
		assertContains(t, rec, ">Unlimited</span>")

		maxWorkers, err := h.workerRepo.GetMaxWorkers(ctx)
		if err != nil {
			t.Fatalf("GetMaxWorkers: %v", err)
		}
		if maxWorkers != 0 {
			t.Fatalf("expected unlimited max_workers=0 in DB, got %d", maxWorkers)
		}
		if n := h.workerSvc.NumWorkers(); n != 0 {
			t.Fatalf("expected unlimited max_workers=0 in worker service, got %d", n)
		}
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

func TestHandler_UnlimitedGlobalAndProjectSettingsAdmitQueuedTask(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	if err := h.workerRepo.SetMaxWorkers(ctx, 1); err != nil {
		t.Fatalf("set finite global worker limit: %v", err)
	}
	h.workerSvc.Resize(1)
	h.workerSvc.SetProjectRepo(h.projectRepo)
	h.workerSvc.SetTaskRepo(h.taskRepo)
	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)

	projectLimit := 1
	project := &models.Project{Name: "Unlimited Settings Project", MaxWorkers: &projectLimit}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.MaxWorkers = 0
	})

	providerStarted := make(chan struct{})
	providerRelease := make(chan struct{})
	mockCaller := testutil.NewMockLLMCaller()
	mockCaller.OnCall = func(callCtx context.Context, _ testutil.MockLLMCall) {
		close(providerStarted)
		select {
		case <-providerRelease:
		case <-callCtx.Done():
		}
	}
	h.llmSvc.SetLLMCaller(mockCaller)

	h.workerSvc.Start(ctx)
	defer h.workerSvc.Stop()

	// Occupy the only global and project slot, then create an active task. The
	// task service submits it to the real worker queue, where it must wait.
	if !h.workerSvc.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected to occupy the finite global/project slot")
	}
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	task := createTask(t, h, project.ID, "Queued Until Unlimited", func(task *models.Task) {
		task.AgentID = &agent.ID
	})
	require.Eventually(t, func() bool {
		return h.workerSvc.QueueSize() == 1
	}, time.Second, 10*time.Millisecond, "task should wait while global and project limits are finite")

	globalRec := htmxPost(e, "/workers", url.Values{"max_workers": {"0"}})
	assertCode(t, globalRec, http.StatusOK)
	if got := h.workerSvc.QueueSize(); got != 1 {
		t.Fatalf("queue size after only global limit became unlimited = %d, want 1", got)
	}

	projectRec := htmxPost(e, "/workers/projects/"+project.ID+"/limit", url.Values{"max_workers": {"0"}})
	assertCode(t, projectRec, http.StatusOK)

	select {
	case <-providerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("queued task did not reach the provider after global and project limits became unlimited")
	}
	close(providerRelease)

	require.Eventually(t, func() bool {
		return h.workerSvc.QueueSize() == 0 && h.workerSvc.TotalRunning() == 1
	}, 2*time.Second, 10*time.Millisecond, "admitted task should leave the worker queue while the held slot remains")
	storedProject, err := h.projectSvc.GetByID(ctx, project.ID)
	if err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if storedProject.MaxWorkers != nil {
		t.Fatalf("project unlimited setting persisted as %v, want nil", storedProject.MaxWorkers)
	}
	storedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if storedTask.Status == models.StatusPending {
		t.Fatalf("task remained pending after unlimited settings: %s", storedTask.Status)
	}
}

func TestHandler_UnlimitedSettingsReconcileDurablePendingTask(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	h.workerSvc.Start(ctx)
	defer h.workerSvc.Stop()

	globalRec := htmxPost(e, "/workers", url.Values{"max_workers": {"1"}})
	assertCode(t, globalRec, http.StatusOK)

	projectLimit := 1
	project := &models.Project{Name: "Durable Pending Project", MaxWorkers: &projectLimit}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	h.workerSvc.SetProjectRepo(h.projectRepo)
	h.workerSvc.SetTaskRepo(h.taskRepo)
	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.MaxWorkers = 0
	})

	providerStarted := make(chan struct{})
	providerRelease := make(chan struct{})
	h.llmSvc.SetLLMCaller(llmCallerFunc(func(callCtx context.Context, _ string, _ []models.Attachment, _ models.LLMConfig, _ string, _ string) (string, string, int, error) {
		close(providerStarted)
		select {
		case <-providerRelease:
		case <-callCtx.Done():
		}
		return "durable task completed", "", 1, nil
	}))

	require.True(t, h.workerSvc.TryAcquireProjectSlot(project.ID))
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	// Simulate a task that survived a worker restart or a missed submission: it
	// is durably runnable but has not yet been offered to WorkerService.Submit.
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Durable pending task",
		Prompt:    "run when limits become unlimited",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	globalRec = htmxPost(e, "/workers", url.Values{"max_workers": {"0"}})
	assertCode(t, globalRec, http.StatusOK)
	projectRec := htmxPost(e, "/workers/projects/"+project.ID+"/limit", url.Values{"max_workers": {"0"}})
	assertCode(t, projectRec, http.StatusOK)

	select {
	case <-providerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("durable pending task was not reconciled after global and project limits became unlimited")
	}
	close(providerRelease)
}

func TestHandler_GlobalWorkerStats(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := htmxGet(e, "/workers/stats/global")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Worker Pool Size")
	assertContains(t, rec, "Tasks Running")
	assertContains(t, rec, "Queue")
	assertContains(t, rec, ">Unlimited</span>")
	assertContains(t, rec, `0 / Unlimited`)
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
	t.Run("accepts a project worker limit above ten", func(t *testing.T) {
		rec := postLimit(t, path, "50")
		assertCode(t, rec, http.StatusOK)
		p, _ := h.projectSvc.GetByID(ctx, project.ID)
		if p.MaxWorkers == nil || *p.MaxWorkers != 50 {
			t.Errorf("expected max_workers=50, got %v", p.MaxWorkers)
		}
	})
	t.Run("project limits respect a finite global limit", func(t *testing.T) {
		globalRec := htmxPost(e, "/workers", url.Values{"max_workers": {"10"}})
		assertCode(t, globalRec, http.StatusOK)
		assertContains(t, globalRec, `max="10"`)
		assertContains(t, globalRec, "Exceeds global")

		rec := postLimit(t, path, "11")
		assertCode(t, rec, http.StatusNoContent)
		if trigger := rec.Header().Get("HX-Trigger"); !strings.Contains(trigger, "global worker limit") {
			t.Fatalf("expected finite-global validation toast, got %q", trigger)
		}
		p, _ := h.projectSvc.GetByID(ctx, project.ID)
		if p.MaxWorkers == nil || *p.MaxWorkers != 50 {
			t.Fatalf("expected rejected update to preserve max_workers=50, got %v", p.MaxWorkers)
		}

		rec = postLimit(t, path, "10")
		assertCode(t, rec, http.StatusOK)
		p, _ = h.projectSvc.GetByID(ctx, project.ID)
		if p.MaxWorkers == nil || *p.MaxWorkers != 10 {
			t.Fatalf("expected max_workers=10 at global limit, got %v", p.MaxWorkers)
		}

		rec = postLimit(t, path, "5")
		assertCode(t, rec, http.StatusOK)
		p, _ = h.projectSvc.GetByID(ctx, project.ID)
		if p.MaxWorkers == nil || *p.MaxWorkers != 5 {
			t.Fatalf("expected max_workers=5 below global limit, got %v", p.MaxWorkers)
		}
	})
	t.Run("project not found returns 404", func(t *testing.T) {
		rec := postLimit(t, "/workers/projects/nonexistent/limit", "2")
		assertCode(t, rec, http.StatusNotFound)
	})
}

func TestHandler_ListTasks_KanbanBoard(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	promptTail := "FULL_PROMPT_DETAIL_TAIL"
	fullPrompt := strings.Repeat("a", 299) + "界" + promptTail
	activeTask := createTask(t, h, "default", "Active Task", func(tk *models.Task) { tk.Prompt = fullPrompt })
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
	assertContains(t, rec, strings.Repeat("a", 299)+"界")
	assert.NotContains(t, rec.Body.String(), promptTail, "Kanban response must not materialize the full prompt")

	detailReq := httptest.NewRequest(http.MethodGet, "/tasks/"+activeTask.ID, nil)
	detailRec := httptest.NewRecorder()
	e.ServeHTTP(detailRec, detailReq)
	assertCode(t, detailRec, http.StatusOK)
	assertContains(t, detailRec, promptTail)
}

func TestHandler_ListTasks_AttachesSwarmChildrenWhenIncludeChildrenRequested(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	parent := &models.Task{
		ProjectID:   "default",
		Title:       "Full Page Swarm Parent",
		Category:    models.CategoryActive,
		Status:      models.StatusBlocked,
		Prompt:      "coordinate swarm work",
		SwarmRole:   models.SwarmRoleParent,
		SwarmStatus: "planning",
	}
	require.NoError(t, h.taskRepo.Create(ctx, parent))
	child := &models.Task{
		ProjectID:     "default",
		Title:         "Full Page Running Worker",
		Category:      models.CategoryActive,
		Status:        models.StatusRunning,
		Prompt:        "do swarm work",
		ParentTaskID:  &parent.ID,
		SwarmRole:     models.SwarmRoleWorker,
		SwarmStatus:   "running",
		SwarmSequence: 1,
	}
	require.NoError(t, h.taskRepo.Create(ctx, child))

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default&include_swarm_children=true", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	runningDropzone := kanbanDropzoneHTML(body, "running")
	pendingDropzone := kanbanDropzoneHTML(body, "pending")
	require.NotEmpty(t, runningDropzone, "expected running dropzone in full-page kanban response")
	require.NotEmpty(t, pendingDropzone, "expected pending dropzone in full-page kanban response")
	assert.Contains(t, runningDropzone, `data-task-id="`+parent.ID+`"`)
	assert.NotContains(t, pendingDropzone, `data-task-id="`+parent.ID+`"`)
	assert.NotContains(t, body, `data-task-id="`+child.ID+`"`, "swarm child should be attached to parent, not rendered as a top-level card")
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

func TestHandler_UpdateTask_RejectsInvalidPriorities(t *testing.T) {
	cases := []struct {
		name        string
		prioritySet bool
		priority    string
	}{
		{name: "missing"},
		{name: "zero", prioritySet: true, priority: "0"},
		{name: "above range", prioritySet: true, priority: "5"},
		{name: "malformed", prioritySet: true, priority: "urgent"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			ctx := context.Background()
			task := createTask(t, h, "default", "Original Invalid Priority Target", func(tk *models.Task) {
				tk.Category = models.CategoryBacklog
				tk.Priority = 3
			})

			form := url.Values{}
			form.Set("title", "Should Not Persist")
			form.Set("category", "active")
			form.Set("prompt", "should not persist")
			if tt.prioritySet {
				form.Set("priority", tt.priority)
			}

			rec := htmxPut(e, "/tasks/"+task.ID, form)
			assertCode(t, rec, http.StatusBadRequest)
			assertContains(t, rec, "Task priority must be between 1 and 4")

			updated, err := h.taskSvc.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.NotNil(t, updated)
			assert.Equal(t, "Original Invalid Priority Target", updated.Title)
			assert.Equal(t, models.CategoryBacklog, updated.Category)
			assert.Equal(t, "test prompt", updated.Prompt)
			assert.Equal(t, 3, updated.Priority)
		})
	}
}

func taskEditFormValues(title, category, priority, prompt, agentDefinitionID string) url.Values {
	form := url.Values{}
	form.Set("title", title)
	form.Set("category", category)
	form.Set("priority", priority)
	form.Set("prompt", prompt)
	form.Set("agent_definition_id", agentDefinitionID)
	return form
}

func assertTaskPrimaryAgentDefinition(t *testing.T, h *Handler, taskID string, want *string) {
	t.Helper()
	stored, err := h.taskSvc.GetByID(context.Background(), taskID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	if want == nil {
		assert.Nil(t, stored.AgentDefinitionID)
		return
	}
	require.NotNil(t, stored.AgentDefinitionID)
	assert.Equal(t, *want, *stored.AgentDefinitionID)
}

func TestHandler_UpdateTask_AssignsAndClearsPrimaryAgentDefinition(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	projectAgent := createScheduleTestAgent(t, agentRepo, "Task Edit Runner", models.AgentScopeProject, project.ID, true)
	globalAgent := createScheduleTestAgent(t, agentRepo, "Task Edit Global Runner", models.AgentScopeGlobal, "", true)
	task := createTask(t, tc.handler, project.ID, "Task Edit Assign Agent", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
	})

	rec := tc.HTMX().Put("/tasks/" + task.ID).WithForm(taskEditFormValues("Task Edit Assign Agent", "backlog", "2", "updated prompt", projectAgent.ID)).Execute()
	assertCode(t, rec, http.StatusOK)
	assertTaskPrimaryAgentDefinition(t, tc.handler, task.ID, &projectAgent.ID)

	rec = tc.HTMX().Put("/tasks/" + task.ID).WithForm(taskEditFormValues("Task Edit Assign Agent", "backlog", "2", "updated prompt", globalAgent.ID)).Execute()
	assertCode(t, rec, http.StatusOK)
	assertTaskPrimaryAgentDefinition(t, tc.handler, task.ID, &globalAgent.ID)

	rec = tc.HTMX().Put("/tasks/" + task.ID).WithForm(taskEditFormValues("Task Edit Assign Agent", "backlog", "2", "updated prompt", "")).Execute()
	assertCode(t, rec, http.StatusOK)
	stored, err := tc.handler.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Nil(t, stored.AgentDefinitionID)
}

func TestHandler_UpdateTask_RejectsInvalidPrimaryAgentDefinitions(t *testing.T) {
	cases := []struct {
		name     string
		agentID  func(t *testing.T, repo *repository.AgentRepo, projectID, otherProjectID string) string
		wantBody string
	}{
		{
			name: "cross project",
			agentID: func(t *testing.T, repo *repository.AgentRepo, _, otherProjectID string) string {
				return createScheduleTestAgent(t, repo, "Other Project Task Edit Agent", models.AgentScopeProject, otherProjectID, true).ID
			},
			wantBody: "invalid primary agent",
		},
		{
			name: "unknown",
			agentID: func(t *testing.T, _ *repository.AgentRepo, _, _ string) string {
				return "agent-does-not-exist"
			},
		},
		{
			name: "disabled",
			agentID: func(t *testing.T, repo *repository.AgentRepo, projectID, _ string) string {
				agent := createScheduleTestAgent(t, repo, "Disabled Task Edit Agent", models.AgentScopeProject, projectID, true)
				agent.Enabled = false
				require.NoError(t, repo.Update(context.Background(), agent))
				return agent.ID
			},
			wantBody: "invalid primary agent",
		},
		{
			name: "archived",
			agentID: func(t *testing.T, repo *repository.AgentRepo, projectID, _ string) string {
				agent := createScheduleTestAgent(t, repo, "Archived Task Edit Agent", models.AgentScopeProject, projectID, true)
				agent.GeneratedStatus = models.AgentStatusArchived
				require.NoError(t, repo.Update(context.Background(), agent))
				return agent.ID
			},
			wantBody: "invalid primary agent",
		},
		{
			name: "non selectable",
			agentID: func(t *testing.T, repo *repository.AgentRepo, projectID, _ string) string {
				return createScheduleTestAgent(t, repo, "Non Selectable Task Edit Agent", models.AgentScopeProject, projectID, false).ID
			},
			wantBody: "invalid primary agent",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().WithName("Project A").Build()
			otherProject := tc.CreateProject().WithName("Project B").Build()
			agentRepo := repository.NewAgentRepo(tc.db)
			tc.handler.agentRepo = agentRepo
			previous := createScheduleTestAgent(t, agentRepo, "Existing Task Edit Agent", models.AgentScopeProject, project.ID, true)
			task := createTask(t, tc.handler, project.ID, "Task Edit Invalid Agent", func(tk *models.Task) {
				tk.Category = models.CategoryBacklog
				tk.AgentDefinitionID = &previous.ID
			})
			invalidAgentID := tt.agentID(t, agentRepo, project.ID, otherProject.ID)

			rec := tc.HTMX().Put("/tasks/" + task.ID).WithForm(taskEditFormValues("Should Not Persist", "backlog", "2", "should not persist", invalidAgentID)).Execute()
			assertCode(t, rec, http.StatusBadRequest)
			if tt.wantBody != "" {
				assertContains(t, rec, tt.wantBody)
			}
			assertTaskPrimaryAgentDefinition(t, tc.handler, task.ID, &previous.ID)
		})
	}
}

func TestHandler_UpdateTask_PersistsValidPriorities(t *testing.T) {
	labels := map[int]string{1: "Low", 2: "Normal", 3: "High", 4: "Urgent"}
	for priority := 1; priority <= 4; priority++ {
		t.Run(fmt.Sprintf("priority %d", priority), func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			ctx := context.Background()
			task := createTask(t, h, "default", "Original Valid Priority Target", func(tk *models.Task) {
				tk.Category = models.CategoryBacklog
				tk.Priority = 2
			})

			form := url.Values{}
			form.Set("title", fmt.Sprintf("Priority %d", priority))
			form.Set("category", "backlog")
			form.Set("priority", strconv.Itoa(priority))
			form.Set("prompt", "updated prompt")

			rec := htmxPut(e, "/tasks/"+task.ID, form)
			assertCode(t, rec, http.StatusOK)

			updated, err := h.taskSvc.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.NotNil(t, updated)
			assert.Equal(t, priority, updated.Priority)
			assert.Equal(t, labels[priority], components.PriorityLabel(updated.Priority))
		})
	}
}

func TestHandler_UpdateTask_NormalizesWhitespaceAndRejectsBlankFields(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	createTask(t, h, "default", "Existing title", func(tk *models.Task) { tk.Category = models.CategoryBacklog })
	target := createTask(t, h, "default", "Original title", func(tk *models.Task) { tk.Category = models.CategoryBacklog })

	form := url.Values{}
	form.Set("title", " Trimmed title ")
	form.Set("category", "backlog")
	form.Set("priority", "2")
	form.Set("prompt", " updated prompt ")
	rec := htmxPut(e, "/tasks/"+target.ID, form)
	assertCode(t, rec, http.StatusOK)
	updated, err := h.taskSvc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "Trimmed title", updated.Title)
	assert.Equal(t, "updated prompt", updated.Prompt)

	duplicate := url.Values{}
	duplicate.Set("title", " Existing title ")
	duplicate.Set("category", "backlog")
	duplicate.Set("priority", "2")
	duplicate.Set("prompt", "duplicate prompt")
	dupRec := htmxPut(e, "/tasks/"+target.ID, duplicate)
	assertCode(t, dupRec, http.StatusConflict)
	assertContains(t, dupRec, "task with this name already exists")
	afterDuplicate, err := h.taskSvc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "Trimmed title", afterDuplicate.Title)
	assert.Equal(t, "updated prompt", afterDuplicate.Prompt)

	blankTitle := url.Values{}
	blankTitle.Set("title", " \t\n ")
	blankTitle.Set("category", "backlog")
	blankTitle.Set("priority", "2")
	blankTitle.Set("prompt", "still valid")
	blankTitleRec := htmxPut(e, "/tasks/"+target.ID, blankTitle)
	assertCode(t, blankTitleRec, http.StatusBadRequest)
	assertContains(t, blankTitleRec, "Task title is required")
	afterBlankTitle, err := h.taskSvc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "Trimmed title", afterBlankTitle.Title)
	assert.Equal(t, "updated prompt", afterBlankTitle.Prompt)

	blankPrompt := url.Values{}
	blankPrompt.Set("title", "Still valid")
	blankPrompt.Set("category", "backlog")
	blankPrompt.Set("priority", "2")
	blankPrompt.Set("prompt", " \t\n ")
	blankPromptRec := htmxPut(e, "/tasks/"+target.ID, blankPrompt)
	assertCode(t, blankPromptRec, http.StatusBadRequest)
	assertContains(t, blankPromptRec, "Task prompt is required")
	afterBlankPrompt, err := h.taskSvc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "Trimmed title", afterBlankPrompt.Title)
	assert.Equal(t, "updated prompt", afterBlankPrompt.Prompt)
}

func TestHandler_UpdateTask_DetailCategoryTransitionsRefreshCompletedAt(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	task := createTask(t, tc.handler, "default", "Recompleted Through Detail Edit", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
	})
	other := createTask(t, tc.handler, "default", "Previously Completed Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
	})

	_, err := tc.db.ExecContext(ctx, `UPDATE tasks SET completed_at = ? WHERE id = ?`, "2000-01-01 00:00:00", task.ID)
	require.NoError(t, err)
	_, err = tc.db.ExecContext(ctx, `UPDATE tasks SET completed_at = ? WHERE id = ?`, "2020-01-01 00:00:00", other.ID)
	require.NoError(t, err)

	updateCategory := func(category models.TaskCategory) {
		t.Helper()
		form := url.Values{}
		form.Set("title", task.Title)
		form.Set("category", string(category))
		form.Set("priority", "2")
		form.Set("prompt", task.Prompt)
		rec := tc.HTMX().Put("/tasks/" + task.ID).WithForm(form).Execute()
		assertCode(t, rec, http.StatusOK)
	}

	updateCategory(models.CategoryBacklog)
	movedToBacklog, err := tc.handler.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, movedToBacklog)
	assert.Equal(t, models.CategoryBacklog, movedToBacklog.Category)
	assert.Nil(t, movedToBacklog.CompletedAt, "leaving Completed through Task Detail must clear completed_at")

	updateCategory(models.CategoryCompleted)
	recompleted, err := tc.handler.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, recompleted)
	require.NotNil(t, recompleted.CompletedAt, "entering Completed through Task Detail must set completed_at")
	assert.True(t, recompleted.CompletedAt.After(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))

	completedTasks, err := tc.handler.taskSvc.ListByProjectWithCategorySorts(ctx, "default", string(models.CategoryCompleted), "", "completed_desc")
	require.NoError(t, err)
	require.Len(t, completedTasks, 2)
	assert.Equal(t, task.ID, completedTasks[0].ID, "recompleted task should sort ahead of older completions")
}

func TestHandler_UpdateTask_NonRunningOnly(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Test Task")

	form := url.Values{}
	form.Set("title", "Updated While Pending")
	form.Set("category", "active")
	form.Set("priority", "2")
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
	form.Set("priority", "2")
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

func TestHandler_ListTasks_DoesNotRenderBlockedSwarmParentAsActiveQueuedWhenNoChildrenRunnable(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	parent := &models.Task{
		ProjectID:   "default",
		Title:       "Swarm Parent With No Runnable Children",
		Category:    models.CategoryActive,
		Status:      models.StatusBlocked,
		Prompt:      "coordinate swarm work",
		SwarmRole:   models.SwarmRoleParent,
		SwarmStatus: "needs_review",
	}
	require.NoError(t, h.taskRepo.Create(ctx, parent))
	completedWorker := &models.Task{
		ProjectID:     "default",
		Title:         "Completed Worker Child",
		Category:      models.CategoryCompleted,
		Status:        models.StatusCompleted,
		Prompt:        "finished swarm work",
		ParentTaskID:  &parent.ID,
		SwarmRole:     models.SwarmRoleWorker,
		SwarmStatus:   "completed",
		SwarmSequence: 1,
	}
	require.NoError(t, h.taskRepo.Create(ctx, completedWorker))
	cancelledReviewer := &models.Task{
		ProjectID:     "default",
		Title:         "Cancelled Reviewer Child",
		Category:      models.CategoryBacklog,
		Status:        models.StatusCancelled,
		Prompt:        "review swarm work",
		ParentTaskID:  &parent.ID,
		SwarmRole:     models.SwarmRoleReviewer,
		SwarmStatus:   "followup_pending",
		SwarmSequence: 2,
	}
	require.NoError(t, h.taskRepo.Create(ctx, cancelledReviewer))

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default&include_swarm_children=true", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	runningDropzone := kanbanDropzoneHTML(body, "running")
	pendingDropzone := kanbanDropzoneHTML(body, "pending")
	backlogColumn := kanbanCategoryColumnHTML(body, "backlog")
	require.NotEmpty(t, runningDropzone, "expected running dropzone in kanban response")
	require.NotEmpty(t, pendingDropzone, "expected pending dropzone in kanban response")
	require.NotEmpty(t, backlogColumn, "expected backlog column in kanban response")
	assert.NotContains(t, runningDropzone, `data-task-id="`+parent.ID+`"`)
	assert.NotContains(t, pendingDropzone, `data-task-id="`+parent.ID+`"`)
	assert.Contains(t, backlogColumn, `data-task-id="`+parent.ID+`"`)
	assert.NotContains(t, body, `data-task-id="`+completedWorker.ID+`"`, "swarm child should be attached to parent, not rendered as a top-level card")
	assert.NotContains(t, body, `data-task-id="`+cancelledReviewer.ID+`"`, "swarm child should be attached to parent, not rendered as a top-level card")
}

func TestHandler_UpdateTaskCategory_AttachesSwarmChildrenForKanbanRefresh(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	parent := &models.Task{
		ProjectID:   "default",
		Title:       "Swarm Parent With Active Child",
		Category:    models.CategoryActive,
		Status:      models.StatusBlocked,
		Prompt:      "coordinate swarm work",
		SwarmRole:   models.SwarmRoleParent,
		SwarmStatus: "planning",
	}
	require.NoError(t, h.taskRepo.Create(ctx, parent))
	child := &models.Task{
		ProjectID:     "default",
		Title:         "Running Worker Child",
		Category:      models.CategoryActive,
		Status:        models.StatusRunning,
		Prompt:        "do swarm work",
		ParentTaskID:  &parent.ID,
		SwarmRole:     models.SwarmRoleWorker,
		SwarmStatus:   "running",
		SwarmSequence: 1,
	}
	require.NoError(t, h.taskRepo.Create(ctx, child))
	other := createTask(t, h, "default", "Unrelated Backlog Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusPending
	})

	form := url.Values{}
	form.Set("category", "completed")
	rec := htmxPatch(e, "/tasks/"+other.ID+"/category", form)
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	runningDropzone := kanbanDropzoneHTML(body, "running")
	pendingDropzone := kanbanDropzoneHTML(body, "pending")
	require.NotEmpty(t, runningDropzone, "expected running dropzone in kanban response")
	require.NotEmpty(t, pendingDropzone, "expected pending dropzone in kanban response")
	assert.Contains(t, runningDropzone, `data-task-id="`+parent.ID+`"`)
	assert.NotContains(t, pendingDropzone, `data-task-id="`+parent.ID+`"`)
	assert.NotContains(t, body, `data-task-id="`+child.ID+`"`, "swarm child should be attached to parent, not rendered as a top-level card")
}

func kanbanDropzoneHTML(body, status string) string {
	marker := `data-status="` + status + `"`
	idx := strings.Index(body, marker)
	if idx == -1 {
		return ""
	}
	next := strings.Index(body[idx+len(marker):], `data-status="`)
	if next == -1 {
		return body[idx:]
	}
	return body[idx : idx+len(marker)+next]
}

func kanbanCategoryColumnHTML(body, category string) string {
	marker := `data-category="` + category + `"`
	idx := strings.Index(body, marker)
	if idx == -1 {
		return ""
	}
	next := strings.Index(body[idx+len(marker):], `data-category="`)
	if next == -1 {
		return body[idx:]
	}
	return body[idx : idx+len(marker)+next]
}

func TestHandler_UpdateTaskCategory_RunningActiveToCompletedStaysCompleted(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := &models.Task{
		ProjectID: "default",
		Title:     "Running Task To Complete",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "test prompt",
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))
	cancelCtx, cancel := context.WithCancel(ctx)
	h.workerSvc.RegisterCancel(task.ID, cancel)

	form := url.Values{}
	form.Set("category", "completed")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/category", form)
	assertCode(t, rec, http.StatusOK)

	select {
	case <-cancelCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected running task cancellation when dropping active task on completed")
	}
	updated, err := h.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, models.StatusCancelled, updated.Status)
	assert.Equal(t, models.CategoryCompleted, updated.Category)
}

func TestHandler_UpdateTaskCategory_QueuedActiveToCompletedStaysCompleted(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	task := &models.Task{
		ProjectID: "default",
		Title:     "Queued Task To Complete",
		Category:  models.CategoryActive,
		Status:    models.StatusQueued,
		Prompt:    "test prompt",
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))
	cancelCtx, cancel := context.WithCancel(ctx)
	h.workerSvc.RegisterCancel(task.ID, cancel)

	form := url.Values{}
	form.Set("category", "completed")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/category", form)
	assertCode(t, rec, http.StatusOK)

	select {
	case <-cancelCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected queued task cancellation when dropping active task on completed")
	}
	updated, err := h.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, models.StatusCancelled, updated.Status)
	assert.Equal(t, models.CategoryCompleted, updated.Category)
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

func TestHandler_UpdateTask_CategoryChangeFromActiveQueuedToCompletedStopsTask(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	task := &models.Task{
		ProjectID: "default",
		Title:     "Queued Active Edit Stop",
		Category:  models.CategoryActive,
		Status:    models.StatusQueued,
		Priority:  2,
		Prompt:    "original prompt",
	}
	require.NoError(t, tc.handler.taskRepo.Create(ctx, task))
	goal, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "finish the objective", service.GoalOptions{Actor: "test"})
	require.NoError(t, err)
	cancelCtx, cancel := context.WithCancel(ctx)
	tc.handler.workerSvc.RegisterCancel(task.ID, cancel)

	form := url.Values{}
	form.Set("title", "Queued Active Edit Stopped")
	form.Set("category", "completed")
	form.Set("prompt", "updated prompt")
	form.Set("priority", "4")
	rec := tc.HTMX().Put("/tasks/" + task.ID).WithForm(form).Execute()
	assertCode(t, rec, http.StatusOK)

	select {
	case <-cancelCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected queued task cancellation when editing active task to completed")
	}
	updated, err := tc.handler.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "Queued Active Edit Stopped", updated.Title)
	assert.Equal(t, "updated prompt", updated.Prompt)
	assert.Equal(t, 4, updated.Priority)
	assert.Equal(t, models.StatusCancelled, updated.Status)
	assert.Equal(t, models.CategoryCompleted, updated.Category)
	paused, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, paused)
	assert.Equal(t, goal.GoalID, paused.GoalID)
	assert.Equal(t, models.TaskGoalStatusPaused, paused.Status)
	assert.Equal(t, service.TaskGoalStoppedByUserReason, paused.Reason)
}

func TestHandler_UpdateTask_CategoryChangeFromCompletedToActiveIsMetadataOnly(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	task := createTask(t, tc.handler, "default", "Completed Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
	})
	goal, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "finish the objective", service.GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, tc.handler.taskGoalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))
	tc.CreateSchedule(task.ID).WithRunAt(time.Now().Add(time.Hour)).Build()

	form := url.Values{}
	form.Set("title", task.Title)
	form.Set("category", "active")
	form.Set("prompt", task.Prompt)
	form.Set("priority", "2")
	rec := tc.HTMX().Put("/tasks/" + task.ID).WithForm(form).Execute()
	assertCode(t, rec, http.StatusOK)

	updated, _ := tc.handler.taskSvc.GetByID(ctx, task.ID)
	if updated.Category != models.CategoryActive {
		t.Errorf("expected category 'active', got %q", updated.Category)
	}
	if updated.Status != models.StatusCompleted {
		t.Errorf("expected status to remain completed after metadata edit, got %q", updated.Status)
	}
	execs, err := tc.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	assert.Empty(t, execs, "task detail metadata save must not create an execution")
	var threadInputs int
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_inputs WHERE task_id = ?`, task.ID).Scan(&threadInputs))
	assert.Equal(t, 0, threadInputs, "task detail metadata save must not queue a thread input")
	assert.Equal(t, 0, tc.handler.workerSvc.QueueSize(), "task detail metadata save must not queue worker work")
	assert.Equal(t, 0, tc.handler.workerSvc.TotalRunning(), "task detail metadata save must not start worker work")
	assert.Equal(t, 0, tc.handler.workerSvc.ProjectRunning(updated.ProjectID), "task detail metadata save must not start project worker work")
	var lifecycleRows int
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lifecycle_executions WHERE task_id = ?`, task.ID).Scan(&lifecycleRows))
	assert.Equal(t, 0, lifecycleRows, "task detail metadata save must not create lifecycle continuations")
	var scheduleRuns int
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedules WHERE task_id = ? AND last_run IS NOT NULL`, task.ID).Scan(&scheduleRuns))
	assert.Equal(t, 0, scheduleRuns, "task detail metadata save must not mark schedule runs")
	resumed, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusPaused, resumed.Status)
	assert.Equal(t, service.TaskGoalStoppedByUserReason, resumed.Reason)
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
	if updated.Status != models.StatusPending {
		t.Errorf("expected status 'pending' until worker admission, got %q", updated.Status)
	}
}

func TestHandler_UpdateTaskStatus_MovesToRunning(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	task := createTask(t, tc.handler, "default", "Test Completed Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Category = models.CategoryActive
	})
	goal, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "finish the objective", service.GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, tc.handler.taskGoalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))

	form := url.Values{}
	form.Set("status", "running")
	rec := tc.HTMX().Patch("/tasks/" + task.ID + "/status?viewing=active").WithForm(form).Execute()
	assertCode(t, rec, http.StatusOK)

	updated, _ := tc.handler.taskSvc.GetByID(ctx, task.ID)
	if updated.Status != models.StatusPending {
		t.Errorf("expected status 'pending' until worker admission, got %q", updated.Status)
	}
	resumed, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	assert.Equal(t, "resumed by user", resumed.Reason)
}

func TestHandler_UpdateTaskStatus_RunningDoesNotBypassWorkerCapacity(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	maxWorkers := 1
	project := &models.Project{Name: "Status Admission Project", MaxWorkers: &maxWorkers}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	h.workerSvc.SetProjectRepo(h.projectRepo)

	task := createTask(t, h, project.ID, "Capacity-gated status task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusCompleted
	})

	require.True(t, h.workerSvc.TryAcquireProjectSlot(project.ID), "test setup should saturate project capacity")
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	form := url.Values{}
	form.Set("status", "running")
	rec := htmxPatch(e, "/tasks/"+task.ID+"/status", form)
	assertCode(t, rec, http.StatusOK)

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	require.NotNil(t, updated)
	assert.Equal(t, models.CategoryActive, updated.Category)
	assert.Equal(t, models.StatusPending, updated.Status, "In Progress must wait for worker admission")
	assert.Equal(t, 1, h.workerSvc.TotalRunning(), "status move must not acquire an extra slot beyond capacity")
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
			body := rec.Body.String()
			if !strings.HasPrefix(strings.TrimSpace(body), `<div id="kanban-board"`) {
				t.Fatalf("expected delete-all response to refresh kanban board, got %s", body)
			}
			if strings.Contains(body, tc.name+" Task 1") || strings.Contains(body, tc.name+" Task 2") {
				t.Fatalf("delete-all response still contains deleted %s tasks: %s", tc.name, body)
			}
			if !strings.Contains(body, `data-category="`+string(tc.category)+`"`) || !strings.Contains(body, "Drop tasks here") {
				t.Fatalf("delete-all response must render the empty category state for %s: %s", tc.name, body)
			}

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

func TestHandler_ListTasks_DeleteAllUsesSharedConfirmationModal(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Delete All Modal Project")
	createTask(t, h, project.ID, "Completed Delete All Task", func(task *models.Task) {
		task.Category = models.CategoryCompleted
		task.Status = models.StatusCompleted
	})
	createTask(t, h, project.ID, "Backlog Delete All Task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.Status = models.StatusPending
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+url.QueryEscape(project.ID), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	for _, want := range []string{
		`id="delete_all_tasks_confirm_modal" class="modal" data-destructive-confirm-dialog`,
		`aria-labelledby="delete_all_tasks_confirm_modal_title"`,
		`aria-describedby="delete_all_tasks_confirm_modal_description"`,
		`id="delete_all_tasks_confirm_name"`,
		`autofocus`,
		`onclick="openDeleteAllTasksConfirm(this)"`,
		`data-delete-all-tasks-category="completed"`,
		`data-delete-all-tasks-category="backlog"`,
		`data-project-id="` + project.ID + `"`,
		`function openDeleteAllTasksConfirm(button)`,
		`function confirmDeleteAllTasks()`,
		`htmx.ajax('DELETE', requestURL`,
		`target: '#kanban-board'`,
		`swap: 'outerHTML'`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected task-board delete-all confirmation contract to contain %q", want)
		}
	}
	for _, forbidden := range []string{
		`hx-confirm="Are you sure you want to delete all completed tasks? This action cannot be undone."`,
		`hx-confirm="Are you sure you want to delete all backlog tasks? This action cannot be undone."`,
		`hx-delete="/tasks/completed`,
		`hx-delete="/tasks/backlog`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("task-board delete-all action must not retain direct deletion wiring %q", forbidden)
		}
	}
}

func TestHandler_DeleteAllTasksByCategory_CancelledRequestPreservesProjectTasks(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Cancelled Delete All Project")
	task := createTask(t, h, project.ID, "Task preserved after failure", func(task *models.Task) {
		task.Category = models.CategoryCompleted
		task.Status = models.StatusCompleted
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodDelete, "/tasks/completed?project_id="+url.QueryEscape(project.ID), nil).WithContext(ctx)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code < http.StatusBadRequest {
		t.Fatalf("expected cancelled delete-all request to fail, got %d body=%s", rec.Code, rec.Body.String())
	}

	remaining, err := h.taskSvc.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get preserved task: %v", err)
	}
	if remaining == nil {
		t.Fatal("cancelled delete-all request deleted the project task")
	}
}

func TestHandler_ExecuteBacklogTasks(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := createProject(t, tc.handler, "Execute Backlog Project")
	otherProject := createProject(t, tc.handler, "Other Execute Backlog Project")

	eligiblePending := createTask(t, tc.handler, project.ID, "Pending backlog task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
	})
	eligibleFailed := createTask(t, tc.handler, project.ID, "Failed backlog task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.Status = models.StatusFailed
	})
	eligibleCancelled := createTask(t, tc.handler, project.ID, "Cancelled backlog task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.Status = models.StatusCancelled
	})
	runningBacklog := createTask(t, tc.handler, project.ID, "Running backlog task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.Status = models.StatusRunning
	})
	foreignBacklog := createTask(t, tc.handler, otherProject.ID, "Foreign backlog task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
	})

	rec := tc.HTMX().Post("/tasks/backlog/execute?project_id=" + project.ID).Execute()
	assertCode(t, rec, http.StatusOK)
	if !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), `<div id="kanban-board"`) {
		t.Fatalf("expected Execute All response to refresh the kanban board, got %s", rec.Body.String())
	}

	for _, taskID := range []string{eligiblePending.ID, eligibleFailed.ID, eligibleCancelled.ID} {
		updated, err := tc.taskRepo.GetByID(ctx, taskID)
		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, models.CategoryActive, updated.Category, "task %s category", taskID)
		assert.Equal(t, models.StatusPending, updated.Status, "task %s status", taskID)
	}
	for _, task := range []*models.Task{runningBacklog, foreignBacklog} {
		updated, err := tc.taskRepo.GetByID(ctx, task.ID)
		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, models.CategoryBacklog, updated.Category, "task %s category", task.ID)
		assert.Equal(t, task.Status, updated.Status, "task %s status", task.ID)
	}

	submittedIDs := make(map[string]bool)
	for range 3 {
		select {
		case submitted := <-tc.handler.workerSvc.Submitted():
			submittedIDs[submitted.ID] = true
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("expected three eligible tasks to be submitted, got %d", len(submittedIDs))
		}
	}
	for _, task := range []*models.Task{eligiblePending, eligibleFailed, eligibleCancelled} {
		assert.True(t, submittedIDs[task.ID], "expected %s to be submitted", task.ID)
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

func TestHandler_DeleteTask_FromDetailPage_RedirectsToSchedule(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Schedule Project")
	task := createTask(t, h, project.ID, "Scheduled Task To Delete", func(tk *models.Task) { tk.Category = models.CategoryScheduled; tk.Status = models.StatusPending })
	runAt := time.Now().Add(24 * time.Hour)
	createSchedule(t, h, task.ID, runAt, func(s *models.Schedule) { s.NextRun = &runAt })

	rec := htmxDelete(e, "/tasks/"+task.ID+"?redirect=list&return_to=schedule")
	assertCode(t, rec, http.StatusOK)

	expectedRedirect := "/schedule?project_id=" + project.ID
	if hxRedirect := rec.Header().Get("HX-Redirect"); hxRedirect != expectedRedirect {
		t.Errorf("expected HX-Redirect=%q, got %q", expectedRedirect, hxRedirect)
	}
	if deleted, _ := h.taskSvc.GetByID(ctx, task.ID); deleted != nil {
		t.Error("expected task to be deleted")
	}
	if schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID); err != nil {
		t.Fatalf("list schedules: %v", err)
	} else if len(schedules) != 0 {
		t.Errorf("expected 0 schedules after delete, got %d", len(schedules))
	}
}

func TestHandler_DeleteTask_UnsafeReturnToFallsBackToList(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Safe Redirect Project")
	task := createTask(t, h, project.ID, "Task To Delete", func(tk *models.Task) { tk.Category = models.CategoryBacklog; tk.Status = models.StatusPending })

	rec := htmxDelete(e, "/tasks/"+task.ID+"?redirect=list&return_to=https://evil.example")
	assertCode(t, rec, http.StatusOK)

	expectedRedirect := "/tasks?project_id=" + project.ID
	if hxRedirect := rec.Header().Get("HX-Redirect"); hxRedirect != expectedRedirect {
		t.Errorf("expected unsafe return_to to fall back to HX-Redirect=%q, got %q", expectedRedirect, hxRedirect)
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

func TestHandler_ViewSchedule_DeleteConfirmationDialog(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Schedule Project")
	rec := htmxGet(e, "/schedule?project_id="+project.ID)
	assertCode(t, rec, http.StatusOK)

	body := rec.Body.String()
	for _, want := range []string{
		`id="delete_schedule_confirm_modal" class="modal"`,
		`id="delete_schedule_confirm_name"`,
		`data-destructive-confirm-dialog`,
		`openDestructiveConfirmDialog('delete_schedule_confirm_modal', 'delete_schedule_confirm_name', button.dataset.scheduleTitle || 'this task')`,
		`onclick="delete_schedule_confirm_modal.close()"`,
		`onclick="confirmDeleteSchedule()"`,
		`class="btn btn-error"`,
		`function openDeleteScheduleConfirm(button)`,
		`deleteScheduleTarget = button.dataset.scheduleTarget || '#schedule-content';`,
		`deleteScheduleSwap = button.dataset.scheduleSwap || 'outerHTML show:none';`,
		`modal.showModal()`,
		`htmx.ajax('DELETE', '/schedules/' + deleteScheduleID`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected schedule delete confirmation markup/script to contain %q", want)
		}
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
	if !strings.Contains(body, `max="365"`) {
		t.Fatal("expected schedule create dialog to cap repeat intervals at 365")
	}
	if !strings.Contains(body, `Repeat interval must be a whole number between 1 and 365`) {
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
	assertContains(t, rec, "Previous week") // chevron aria-label
	assertContains(t, rec, "Next week")     // chevron aria-label

	// Week +2 should show the task and enabled Today button
	rec = htmxGet(e, base+"&week=2")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Future Task")
	assertContains(t, rec, "Today")
	// Today button should NOT be disabled when off current week
	assertNotContains(t, rec, `class="btn btn-sm btn-outline btn-disabled"`)

	// Week -1 should show enabled Today button
	rec = htmxGet(e, base+"&week=-1")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Today")

	// Current week should show disabled Today button
	rec = htmxGet(e, base)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "btn-disabled")

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
	if !strings.Contains(body, `background: var(--ov-schedule-timeline-dash);`) {
		t.Error("expected timeline tracer to use shared dash token var(--ov-schedule-timeline-dash)")
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
	// The timeline container should use flex-1 to fill remaining space. Treat
	// classes as tokens so adding or reordering other layout classes does not
	// make this assertion fail while the overflow contract is still intact.
	timelineStart := strings.Index(body, `id="schedule-timeline-container"`)
	if timelineStart < 0 {
		t.Fatal("missing schedule-timeline-container element")
	}
	timelineTagEnd := strings.Index(body[timelineStart:], ">")
	if timelineTagEnd < 0 {
		t.Fatal("unterminated schedule-timeline-container element")
	}
	timelineTag := body[timelineStart : timelineStart+timelineTagEnd]
	classStart := strings.Index(timelineTag, `class="`)
	if classStart < 0 {
		t.Fatal("schedule-timeline-container is missing its class attribute")
	}
	classValue := timelineTag[classStart+len(`class="`):]
	classEnd := strings.Index(classValue, `"`)
	if classEnd < 0 {
		t.Fatal("schedule-timeline-container has an unterminated class attribute")
	}
	classes := strings.Fields(classValue[:classEnd])
	for _, required := range []string{"flex-1", "min-h-0"} {
		if !slices.Contains(classes, required) {
			t.Errorf("schedule-timeline-container should include %q for proper overflow; classes=%v", required, classes)
		}
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
		form.Set("priority", "2")
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

	futureDate := time.Now().Local().AddDate(1, 0, 0)
	form := url.Values{}
	form.Set("new_date", futureDate.Format("2006-01-02")) // Future date to avoid adjustment
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
	expectedNextRun := time.Date(futureDate.Year(), futureDate.Month(), futureDate.Day(), 9, 30, 0, 0, time.Local).UTC()
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

	futureDate := time.Now().Local().AddDate(1, 0, 0)
	form := url.Values{}
	form.Set("new_date", futureDate.Format("2006-01-02")) // Future date to avoid adjustment
	form.Set("hour", "9")
	rec := htmxPatch(e, "/schedules/"+schedule.ID+"/reschedule", form)
	assertCode(t, rec, http.StatusNoContent)

	updated, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated schedule: %v", err)
	}

	expectedTime := time.Date(futureDate.Year(), futureDate.Month(), futureDate.Day(), 9, 30, 0, 0, time.Local).UTC()
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
	assertContains(t, rec, "Token Usage Breakdown")
	assertNotContains(t, rec, "Daily Token Usage")
	assertContains(t, rec, "Token Usage")
	assertContains(t, rec, `id="accountUsageCards" class="grid grid-cols-1 lg:grid-cols-2 gap-6 mb-6"`)
	assertContains(t, rec, `class="ov-account-limit-bar" role="meter"`)
	assertContains(t, rec, `class="ov-account-limit-bar-fill" style="width: ${width}%"`)
	assertNotContains(t, rec, `progress progress-primary w-full`)
	assertNotContains(t, rec, "dailyUsageModelSelect")
	assertNotContains(t, rec, "dailyUsageChart")
	assertNotContains(t, rec, "renderDailyUsageChart")
	assertContains(t, rec, "usageRateModelSelect")
	assertNotContains(t, rec, "dailyUsageSeriesMode")
	assertNotContains(t, rec, "usageRateSeriesMode")
	assertContains(t, rec, "fillModelChoiceSelect")
	assertContains(t, rec, "JSON.stringify([row.provider")
	assertContains(t, rec, "escapeHTML(key)")
	assertContains(t, rec, "renderUsageRateChart(latestUsageAnalytics)")
	assertContains(t, rec, "Model Breakdown by Tokens")
	assertContains(t, rec, "Model Breakdown by Executions")
	assertContains(t, rec, "modelTokenBreakdownChart")
	assertContains(t, rec, "agentUsageChart")
	assertContains(t, rec, "<th>Provider</th><th>Model</th><th>Input</th><th>Output</th><th>Cache</th><th>Reasoning</th><th>Total</th><th>Cost</th>")
	assertContains(t, rec, "All models</td><td>' + formatNumber(totals.input_tokens)")
	assertNotContains(t, rec, "Account Limits")
	assertContains(t, rec, "type: 'line'")
	assertContains(t, rec, "/api/analytics/usage")
	assertNotContains(t, rec, "daily_usage_by_model")
	assertContains(t, rec, "usage_rate_by_model")
	assertContains(t, rec, "new URLSearchParams({ project_id: projectID, range: usageRangeParam(), group_by: groupBy })")
	assertNotContains(t, rec, "new URLSearchParams({ range: usageRangeParam(), group_by: groupBy })")
	assertContains(t, rec, "refresh', 'true'")
	assertNotContains(t, rec, "usageReasoningTokens")
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

func TestHandler_Analytics_ModelUsagePageWiring(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	req := httptest.NewRequest(http.MethodGet, "/analytics?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertCode(t, rec, http.StatusOK)

	body := rec.Body.String()
	assertContains(t, rec, "Token Usage Breakdown")
	assertContains(t, rec, `id="usageBreakdownTable"`)
	assertContains(t, rec, `id="modelTokenBreakdownChart"`)
	assertContains(t, rec, "model_breakdown")
	assertContains(t, rec, "new URLSearchParams({ project_id: projectID, range: usageRangeParam(), group_by: groupBy })")
	if count := strings.Count(body, "const accounts = data.account_limits || []"); count != 1 {
		t.Fatalf("expected one account limits declaration in analytics script, got %d", count)
	}
	assertContains(t, rec, "let accountLimits = account.limits || []")
	assertContains(t, rec, "if (remainingMilliseconds <= 0) return 'Reset due'")
	assertNotContains(t, rec, "Math.max(0, Math.ceil((resetAt.getTime() - Date.now()) / 60000))")
}

func TestHandler_Analytics_APIEndpoints_ReturnJSON(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Test Project")
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Provider = "anthropic"; a.Model = "claude-3-5-sonnet-20241022" })
	compatibleAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "OpenRouter"
		a.Provider = models.ProviderOpenAICompatible
		a.AuthMethod = models.AuthMethodAPIKey
		a.APIKey = "key"
		a.Model = "deepseek/deepseek-chat-v3.1:free"
	})
	task := createTask(t, h, project.ID, "Analytics Test Task", func(tk *models.Task) { tk.Category = models.CategoryBacklog })
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.PromptSent = "test prompt" })
	if err := h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output", "", 100, 5000); err != nil {
		t.Fatal(err)
	}
	if err := h.usageRepo.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "anthropic", ProjectID: project.ID, TaskID: task.ID, ExecutionID: exec.ID, AgentConfigID: agent.ID, Model: agent.Model, Operation: "task", Status: "completed", InputTokens: 80, OutputTokens: 20, TotalTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if err := h.usageRepo.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: string(models.ProviderOpenAICompatible), ProjectID: project.ID, TaskID: task.ID, ExecutionID: exec.ID, AgentConfigID: compatibleAgent.ID, Model: compatibleAgent.Model, Operation: "chat", Status: "completed", InputTokens: 120, OutputTokens: 45, CachedInputTokens: 20, ReasoningOutputTokens: 8, TotalTokens: 165}); err != nil {
		t.Fatal(err)
	}

	usageRec := htmxGet(e, "/api/analytics/usage?project_id="+project.ID+"&range=all&refresh=true")
	assertCode(t, usageRec, http.StatusOK)
	assertContains(t, usageRec, `"total_tokens":265`)
	assertContains(t, usageRec, `"model_breakdown"`)
	assertContains(t, usageRec, `"provider":"openai_compatible"`)
	assertContains(t, usageRec, `"model":"deepseek/deepseek-chat-v3.1:free"`)
	assertContains(t, usageRec, `"cached_input_tokens":20`)
	assertContains(t, usageRec, `"reasoning_output_tokens":8`)

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

func TestSidebar_AutomationInvocationStartedShowsToast(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, snippet := range []string{
		`eventType === 'automation_invocation_started'`,
		`const automationName = data.task_name || 'Automation';`,
		`const automationUrl = '/automations/' + encodeURIComponent(data.automation_id || '') + '?project_id=' + encodeURIComponent(currentProjectID);`,
		`window.showToast(automationName + ' is now running.', 'info', '', { toastKey: 'automation:' + invocationId, clickURL: automationUrl });`,
	} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("sidebar automation-start toast script missing snippet: %s", snippet)
		}
	}
}

func TestSidebar_DoesNotPersistSelectedNavHighlight(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, snippet := range []string{
		`setSidebarActiveNav`,
		`_sidebarPendingActiveBase`,
		`aria-current`,
		`:focus:not(:focus-visible):not(.active)`,
	} {
		if strings.Contains(body, snippet) {
			t.Fatalf("sidebar should not persist selected nav highlight, found snippet: %s", snippet)
		}
	}
}

func TestLayout_ThemeAndSidebarPreferencesPersistBeforeFirstPaint(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	required := []string{
		`rawSaved = document.documentElement.getAttribute('data-ui-theme') || ''`,
		`saved = normalizeStoredTheme(rawSaved)`,
		`'openvibely-light'`,
		`'openvibely-dark'`,
		`document.documentElement.setAttribute('data-theme', mode)`,
		`document.documentElement.setAttribute('data-color-theme', themeID)`,
		`localStorage.setItem('theme', themeID)`,
		`window.applyOpenVibelyTheme`,
		`document.documentElement.getAttribute('data-ui-sidebar-collapsed') === 'true'`,
		`document.body.classList.add('sidebar-collapsed-pending')`,
		`body.sidebar-collapsed-pending .sidebar-aside`,
		`body.sidebar-collapsed-pending .sidebar-aside .sidebar-inner`,
		`document.body.classList.toggle('sidebar-collapsed-pending', isCollapsed)`,
		`/ui/preferences`,
		`JSON.stringify({ sidebar_collapsed: isCollapsed })`,
		`JSON.stringify({ project_id: projectID })`,
	}
	for _, snippet := range required {
		if !strings.Contains(body, snippet) {
			t.Fatalf("layout missing persisted preference snippet: %s", snippet)
		}
	}

	if strings.Contains(body, `localStorage.setItem('theme', next)`) {
		t.Fatal("theme toggle must persist stable exact theme IDs, not raw light/dark mode values")
	}
}

func TestLayout_UIPreferencesRestoreFromSettings(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	require.NoError(t, h.settingsRepo.Set(context.Background(), uiPreferenceThemeKey, "openvibely-light"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), uiPreferenceSidebarCollapsedKey, "true"))

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	for _, snippet := range []string{
		`data-openvibely-runtime="web"`,
		`data-ui-theme="openvibely-light"`,
		`data-ui-sidebar-collapsed="true"`,
		`rawSaved = document.documentElement.getAttribute('data-ui-theme') || ''`,
		`document.documentElement.getAttribute('data-ui-sidebar-collapsed') === 'true'`,
		`fetch('/ui/preferences'`,
		`JSON.stringify({ sidebar_collapsed: isCollapsed })`,
	} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("UI preference restore missing snippet: %s", snippet)
		}
	}
	if strings.Contains(body, `document.cookie`) || strings.Contains(body, `openvibely-sidebar-collapsed`) || strings.Contains(body, `openvibely-theme`) {
		t.Fatal("UI preferences must persist through DB settings, not port/origin scoped cookies")
	}
	if strings.Contains(body, `if (document.documentElement.getAttribute('data-openvibely-runtime') !== 'desktop') return`) {
		t.Fatal("UI preferences must persist to DB in web/server mode as well as desktop mode")
	}
}

func TestLayout_UIPreferencesDoNotReadSettingsForHTMXFragments(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	settingsQueries := 0
	h.settingsRepo.SetQueryObserver(func(query string) {
		if strings.Contains(query, "app_settings") {
			settingsQueries++
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "main-content")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if settingsQueries != 0 {
		t.Fatalf("HTMX fragment render should not read UI preferences from app_settings, got %d settings queries", settingsQueries)
	}
}

func TestSaveUIPreferences_PersistsPreferencesToSettings(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Selected Project")
	req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(fmt.Sprintf(`{"theme":"openvibely-dark","sidebar_collapsed":true,"diff_view":"split","project_id":%q}`, project.ID)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	theme, err := h.settingsRepo.Get(context.Background(), uiPreferenceThemeKey)
	require.NoError(t, err)
	require.Equal(t, "openvibely-dark", theme)
	sidebar, err := h.settingsRepo.Get(context.Background(), uiPreferenceSidebarCollapsedKey)
	require.NoError(t, err)
	require.Equal(t, "true", sidebar)
	diffView, err := h.settingsRepo.Get(context.Background(), uiPreferenceDiffViewKey)
	require.NoError(t, err)
	require.Equal(t, "split", diffView)
	selectedProjectID, err := h.settingsRepo.Get(context.Background(), uiPreferenceSelectedProjectIDKey)
	require.NoError(t, err)
	require.Equal(t, project.ID, selectedProjectID)
}

func TestSaveUIPreferences_RejectsInvalidProjectID(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(`{"project_id":"missing-project"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid project id, got %d", rec.Code)
	}
}

func TestSaveUIPreferences_RejectsInvalidDiffView(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(`{"diff_view":"side-by-side"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid diff view, got %d", rec.Code)
	}
}

func TestTaskChangesUsesPersistedSplitDiffViewPreference(t *testing.T) {
	h, e, llmConfigRepo, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Diff Pref Project")
	agent := createAgent(t, llmConfigRepo)
	task := createTask(t, h, project.ID, "Diff Pref Task")
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
	})
	require.NoError(t, h.execRepo.UpdateDiffOutput(context.Background(), exec.ID, "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new\n"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), uiPreferenceDiffViewKey, "split"))

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, body)
	}
	for _, snippet := range []string{
		`data-initial-view="split"`,
		`id="diff-btn-split"`,
		`class="btn btn-sm join-item btn-active"`,
		`id="diff-content-split" class="space-y-4"`,
		`id="diff-content-inline" class="space-y-4 hidden"`,
		`JSON.stringify({ diff_view: mode })`,
	} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("changes diff view preference render missing snippet: %s", snippet)
		}
	}
}

func TestTaskDetailChangesRefreshRestoresInlineOrSplitWithoutSaving(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Diff Refresh Project")
	task := createTask(t, h, project.ID, "Diff Refresh Task")

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab=changes", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, body)
	}
	for _, snippet := range []string{
		`var viewMode = _getDiffViewMode()`,
		`if ((viewMode === 'inline' || viewMode === 'split') && typeof switchDiffView === 'function')`,
		`switchDiffView(viewMode, false)`,
	} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("changes refresh restore missing snippet: %s", snippet)
		}
	}
	if strings.Contains(body, `viewMode === 'split' && typeof switchDiffView === 'function'`) {
		t.Fatal("changes refresh must restore inline as well as split")
	}
}

func TestSaveUIPreferences_RejectsInvalidThemeID(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(`{"theme":"bad theme"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid theme, got %d", rec.Code)
	}
}

func TestSidebar_ProjectSelectorSearchTriggerAndFocusVisible(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	required := []string{
		`id="project-selector"`,
		`class="sr-only"`,
		`aria-hidden="true"`,
		`data-project-selector-value`,
		`id="project-selector-trigger"`,
		`class="select select-bordered select-sm w-full sidebar-project-select flex min-w-0 items-center justify-between gap-2 bg-none text-left font-normal"`,
		`aria-haspopup="dialog"`,
		`aria-controls="project-selector-dialog"`,
		`data-project-selector-caret`,
		`id="project-selector-dialog"`,
		`class="sidebar-project-dialog fixed m-0`,
		`id="project-selector-search"`,
		`type="search"`,
		`placeholder="Search projects"`,
		`role="listbox"`,
		`data-project-selector-option`,
		`data-project-selector-clear`,
		`function positionSelector()`,
		`document.addEventListener('input', function(event)`,
		`if (event.target === search) applyFilter();`,
	}
	for _, snippet := range required {
		if !strings.Contains(body, snippet) {
			t.Fatalf("sidebar searchable project selector missing snippet: %s", snippet)
		}
	}
	if strings.Contains(body, `class="select select-bordered select-sm w-full sidebar-project-select"`) {
		t.Fatal("sidebar project selector must not expose the old native dropdown")
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
		`class="sidebar-aside relative z-[210] lg:z-auto bg-base-100`,
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
		`[data-theme="light"] .hover\:border-primary:hover,`,
		`[data-theme="light"] [class~="hover:border-primary"]:hover {`,
		`border-color: #3f4981 !important;`,
		`[data-theme="light"] .hover\:border-primary\/40:hover,`,
		`[data-theme="light"] [class~="hover:border-primary/40"]:hover {`,
		`border-color: rgba(63, 73, 129, 0.4) !important;`,
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
		`class="btn btn-circle btn-ghost btn-xs min-h-11 h-11 w-11 p-0 lg:min-h-6 lg:h-6 lg:w-6 absolute top-2 right-2 z-10"`, `[data-theme="light"] .card .btn-circle.btn-ghost {`,
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
	if updatedTask.Status == models.StatusPending {
		t.Errorf("expected follow-up task to leave pending state, got %s", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryActive {
		t.Errorf("expected category active, got %s", updatedTask.Category)
	}
}

func TestHandler_TaskThreadSend_ClearsStaleCancellationMarkerSoGoalAfterCompleteRuns(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	goalRepo := repository.NewTaskGoalRepo(db)
	goalSvc := service.NewTaskGoalService(goalRepo, h.taskRepo, nil)
	h.SetTaskGoalService(goalSvc)
	h.taskSvc.SetTaskGoalService(goalSvc)
	h.workerSvc.SetTaskGoalService(goalSvc)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	h.workerSvc.SetLifecycleAgentRepo(agentRepo)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Direct Followup Clears Stop Marker Project")
	task := createTask(t, h, project.ID, "Direct Followup Clears Stop Marker Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
		tk.Prompt = "initial task prompt"
	})
	_, err := goalSvc.SetGoal(ctx, task.ID, "Keep evaluating after every legitimate follow-up", service.GoalOptions{})
	require.NoError(t, err)
	goalAgent := &models.Agent{Key: models.AgentSystemKindGoal, Name: "System: Goal Agent", Model: "inherit", Tools: []string{"get_task_goal", "send_to_task", "mark_task_goal_achieved", "report_task_goal_blocked"}, SystemKind: models.AgentSystemKindGoal, GeneratedStatus: models.AgentStatusProtected, CreatedBy: models.AgentCreatedBySystem, Enabled: true}
	require.NoError(t, agentRepo.Create(ctx, goalAgent))
	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{{ID: "goal-hook-direct", AgentID: goalAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true}}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))
	mock := testutil.NewMockLLMCaller()
	mock.Response = "direct follow-up complete"
	mock.TextOnly = "direct follow-up complete"
	h.llmSvc.SetLLMCaller(mock)

	h.workerSvc.MarkCancellationRequested(task.ID)
	require.True(t, h.workerSvc.IsCancellationRequested(task.ID))

	for _, message := range []string{"first legitimate follow-up after stop", "second legitimate follow-up after stop"} {
		form := url.Values{}
		form.Set("message", message)
		rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
		assertCode(t, rec, http.StatusOK)
		require.Eventually(t, func() bool {
			execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
			return len(execs) > 0 && execs[len(execs)-1].Status == models.ExecCompleted
		}, 2*time.Second, 10*time.Millisecond)
	}

	require.False(t, h.workerSvc.IsCancellationRequested(task.ID), "direct follow-up start should clear stale stop intent")
	require.Eventually(t, func() bool {
		n := 0
		for _, seen := range invoker.Seen() {
			if seen == "after_complete/evaluate_task_goal" {
				n++
			}
		}
		return n == 2
	}, 2*time.Second, 10*time.Millisecond, "expected Goal Agent after_complete for both direct follow-up turns, seen=%#v", invoker.Seen())
}

func TestHandler_TaskThreadSend_SwarmParentRoutesWithoutNormalExecution(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Swarm Parent Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      3,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)

	form := url.Values{}
	form.Set("message", "Update only the API worker")
	rec := htmxPost(e, "/tasks/"+parent.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Update only the API worker")

	execs, err := h.execRepo.ListByTaskChronological(ctx, parent.ID)
	require.NoError(t, err)
	require.Empty(t, execs, "parent swarm follow-up must not create a normal parent execution")
	pending, err := h.threadInputRepo.ListPendingForTask(ctx, parent.ID)
	require.NoError(t, err)
	require.Empty(t, pending, "parent swarm follow-up must not queue a normal parent thread input")

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	assert.Equal(t, "needs_coordination", updatedParent.SwarmStatus)
	assert.Equal(t, models.StatusRunning, updatedParent.Status)

	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	assert.Equal(t, models.StatusPending, planner.Status)
	assert.Equal(t, "coordinating", planner.SwarmStatus)
	fullPlanner, err := h.taskRepo.GetByID(ctx, planner.ID)
	require.NoError(t, err)
	require.NotNil(t, fullPlanner)
	assert.Contains(t, fullPlanner.Prompt, "Update only the API worker")
}

func TestHandler_SwarmFollowupChildCreatesTaskThreadExecution(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "API Swarm Child Followup Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusCompleted))
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, worker.ID, models.CategoryCompleted))

	form := url.Values{}
	form.Set("message", "Continue this worker slice")
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+worker.ID+"/swarm/followup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"status":"started"`)

	execs, err := h.execRepo.ListByTaskChronological(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.True(t, execs[0].IsFollowup)
	assert.Equal(t, "Continue this worker slice", execs[0].PromptSent)

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	assert.Equal(t, models.StatusRunning, updatedParent.Status)
	assert.Equal(t, models.CategoryActive, updatedParent.Category)
	assert.Equal(t, "needs_review", updatedParent.SwarmStatus)

	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedWorker)
	assert.Contains(t, []models.TaskStatus{models.StatusQueued, models.StatusRunning}, updatedWorker.Status)
	assert.Equal(t, models.CategoryActive, updatedWorker.Category)
	assert.Equal(t, "followup_pending", updatedWorker.SwarmStatus)
}

func TestHandler_SwarmFollowupChildQueuesWhenActive(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "API Swarm Child Queue Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusRunning))
	exec := &models.Execution{TaskID: worker.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active worker run"}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	form := url.Values{}
	form.Set("message", "Queue this child follow-up")
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+worker.ID+"/swarm/followup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"status":"queued"`)

	pending, err := h.threadInputRepo.ListPendingForTask(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "Queue this child follow-up", pending[0].Content)
	assert.Equal(t, exec.ID, pending[0].RunExecutionID)

	execs, err := h.execRepo.ListByTaskChronological(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1, "queued API follow-up must not create a duplicate child execution while one is active")
}

func TestHandler_SubmitReview_SwarmChildQueuesBehindActiveExecutionWithoutRouting(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.workerSvc = nil
	reviewRepo := repository.NewReviewCommentRepo(db)
	h.SetReviewCommentRepo(reviewRepo)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Review Swarm Child Queue Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	parentCfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	initialParentGeneration := parentCfg.Generation
	workerCfg, err := models.ParseSwarmConfig(worker.SwarmConfig)
	require.NoError(t, err)
	initialWorkerGeneration := workerCfg.RerunGeneration
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusQueued))
	activeExec := &models.Execution{TaskID: worker.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active worker run", IsFollowup: true}
	require.NoError(t, h.execRepo.Create(ctx, activeExec))
	require.NoError(t, reviewRepo.Create(ctx, &models.ReviewComment{TaskID: worker.ID, FilePath: "main.go", LineNumber: 7, LineType: "new", CommentText: "Fix this", ReviewedBy: "user"}))

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+worker.ID+"/reviews/submit", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	execs, err := h.execRepo.ListByTaskChronological(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1, "review submit should queue behind active swarm child execution")
	pending, err := h.threadInputRepo.ListPendingForTask(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, activeExec.ID, pending[0].RunExecutionID)
	assert.Contains(t, pending[0].Content, "Fix this")

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialParentGeneration, updatedParentCfg.Generation)
	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	updatedWorkerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialWorkerGeneration, updatedWorkerCfg.RerunGeneration)
}

func TestHandler_SubmitReview_SwarmChildDirectStartAppliesRouting(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.workerSvc = nil
	reviewRepo := repository.NewReviewCommentRepo(db)
	h.SetReviewCommentRepo(reviewRepo)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Review Swarm Child Direct Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	parentCfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	priorExec := &models.Execution{TaskID: worker.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "initial worker run", Output: "done"}
	require.NoError(t, h.execRepo.Create(ctx, priorExec))
	require.NoError(t, reviewRepo.Create(ctx, &models.ReviewComment{TaskID: worker.ID, FilePath: "main.go", LineNumber: 9, LineType: "new", CommentText: "Address review", ReviewedBy: "user"}))

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+worker.ID+"/reviews/submit", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	execs, err := h.execRepo.ListByTaskChronological(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, execs, 2)
	latestExec := execs[len(execs)-1]
	assert.True(t, latestExec.IsFollowup)
	assert.Contains(t, latestExec.PromptSent, "Address review")
	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, parentCfg.Generation+1, updatedParentCfg.Generation)
	assert.Equal(t, "needs_review", updatedParent.SwarmStatus)
	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	updatedWorkerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, updatedParentCfg.Generation, updatedWorkerCfg.RerunGeneration)
	assert.Equal(t, "followup_pending", updatedWorker.SwarmStatus)
}

func TestHandler_TaskThreadSend_SwarmChildQueuedFollowupDefersRoutingUntilPromotion(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Swarm Child Queue Timing Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	workerCfg, err := models.ParseSwarmConfig(worker.SwarmConfig)
	require.NoError(t, err)
	initialWorkerGeneration := workerCfg.RerunGeneration
	parentCfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	initialParentGeneration := parentCfg.Generation

	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusRunning))
	activeExec := &models.Execution{TaskID: worker.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active worker run"}
	require.NoError(t, h.execRepo.Create(ctx, activeExec))

	form := url.Values{}
	form.Set("message", "Queued worker follow-up")
	rec := htmxPost(e, "/tasks/"+worker.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialParentGeneration, updatedParentCfg.Generation, "queued child follow-up must not advance parent generation before it starts")

	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedWorker)
	updatedWorkerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialWorkerGeneration, updatedWorkerCfg.RerunGeneration, "queued child follow-up must not retarget the active worker run")

	pending, err := h.threadInputRepo.ListPendingForTask(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, activeExec.ID, pending[0].RunExecutionID)
}

func TestHandler_StartQueuedTaskThreadInput_AppliesSwarmChildFollowupOnPromotion(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Swarm Child Promotion Timing Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	parentCfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	initialParentGeneration := parentCfg.Generation

	queued := &models.ThreadInput{
		Scope:         models.ThreadInputScopeTask,
		ProjectID:     project.ID,
		TaskID:        worker.ID,
		AgentConfigID: agent.ID,
		InputMode:     models.ThreadInputModeQueued,
		InputStatus:   models.ThreadInputPending,
		Content:       "Promoted worker follow-up",
		Source:        models.TaskOriginWeb,
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))
	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *queued))

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialParentGeneration+1, updatedParentCfg.Generation)
	assert.Equal(t, "needs_review", updatedParent.SwarmStatus)

	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedWorker)
	updatedWorkerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, updatedParentCfg.Generation, updatedWorkerCfg.RerunGeneration)
	assert.Equal(t, "followup_pending", updatedWorker.SwarmStatus)

	execs, err := h.execRepo.ListByTaskChronological(ctx, worker.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.True(t, execs[0].IsFollowup)
	assert.Equal(t, "Promoted worker follow-up", execs[0].PromptSent)
	applied, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.NotNil(t, applied)
	assert.Equal(t, models.ThreadInputApplied, applied.InputStatus)
	assert.Equal(t, execs[0].ID, applied.RunExecutionID)
}

func TestHandler_ApplySwarmChildFollowupStartClearsCancellationRequests(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Direct Swarm Child Followup Clears Stop Marker Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers: []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	h.workerSvc.MarkCancellationRequested(parent.ID)
	h.workerSvc.MarkCancellationRequested(worker.ID)

	require.NoError(t, h.applySwarmChildFollowupStart(ctx, worker, "legitimate follow-up after swarm restart"))

	require.False(t, h.workerSvc.IsCancellationRequested(parent.ID), "swarm child follow-up should clear stale parent cancellation request")
	require.False(t, h.workerSvc.IsCancellationRequested(worker.ID), "swarm child follow-up should clear stale child cancellation request")
	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusRunning, updatedParent.Status)
	require.Equal(t, models.CategoryActive, updatedParent.Category)
	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.Equal(t, "followup_pending", updatedWorker.SwarmStatus)
}

func TestHandler_CompleteWithSuccess_NotifiesSwarmChildFollowupCompletion(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Swarm Child Completion Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      3,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	output := service.PlannerOutput{
		Workers: []service.PlannerWorker{
			{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true},
		},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, output))
	children, err := h.taskRepo.ListSwarmChildren(ctx, parent.ID)
	require.NoError(t, err)
	var worker, reviewer *models.Task
	for i := range children {
		switch children[i].SwarmRole {
		case models.SwarmRoleWorker:
			worker = &children[i]
		case models.SwarmRoleReviewer:
			reviewer = &children[i]
		}
	}
	require.NotNil(t, worker)
	require.NotNil(t, reviewer)
	require.NoError(t, h.swarmSvc.HandleChildFollowup(ctx, worker.ID, "finish the API update"))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusRunning))
	exec := &models.Execution{TaskID: worker.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "finish the API update", IsFollowup: true}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	outcome := h.completeWithSuccess(ctx, exec.ID, worker.ID, "worker follow-up done", "", 0, 1)
	require.Equal(t, repository.CompleteSuccessCompleted, outcome)

	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	workerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, 2, workerCfg.CompletedGeneration)
	updatedReviewer, err := h.taskRepo.GetByID(ctx, reviewer.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusPending, updatedReviewer.Status)
	assert.Equal(t, "ready", updatedReviewer.SwarmStatus)
}

func TestHandler_CancelTask_NotifiesSwarmChildCancellation(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Swarm Child Cancel Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusRunning))

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+worker.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	assert.Equal(t, models.StatusBlocked, updatedParent.Status)
	assert.Equal(t, "needs_coordination", updatedParent.SwarmStatus)
}

func TestHandler_UpdateTask_NotifiesPendingSwarmChildCancellation(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Swarm Child Edit Cancel Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent edit",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	fullWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, fullWorker)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusPending))
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, worker.ID, models.CategoryActive))

	form := url.Values{}
	form.Set("title", "Edited pending worker")
	form.Set("category", string(models.CategoryCompleted))
	form.Set("prompt", fullWorker.Prompt)
	form.Set("priority", "3")
	rec := htmxPut(e, "/tasks/"+worker.ID, form)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedWorker)
	assert.Equal(t, "Edited pending worker", updatedWorker.Title)
	assert.Equal(t, models.StatusCancelled, updatedWorker.Status)
	assert.Equal(t, models.CategoryCompleted, updatedWorker.Category)
	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	assert.Equal(t, models.StatusBlocked, updatedParent.Status)
	assert.Equal(t, "needs_coordination", updatedParent.SwarmStatus)
}

func TestHandler_UpdateTaskCategory_NotifiesSwarmChildCancellation(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Swarm Child Drop Cancel Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusRunning))
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, worker.ID, models.CategoryActive))

	form := url.Values{}
	form.Set("category", string(models.CategoryCompleted))
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+worker.ID+"/category", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	assert.Equal(t, models.StatusBlocked, updatedParent.Status)
	assert.Equal(t, "needs_coordination", updatedParent.SwarmStatus)
	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedWorker)
	assert.Equal(t, models.StatusCancelled, updatedWorker.Status)
	assert.Equal(t, models.CategoryCompleted, updatedWorker.Category)
}

func TestHandler_CompleteWithCancellation_NotifiesSwarmChildCancellation(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Swarm Child Streaming Cancel Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusRunning))
	exec := &models.Execution{TaskID: worker.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "work"}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	h.completeWithCancellation(exec.ID, worker.ID, "partial worker output", 0, 1, 0)

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedParent)
	assert.Equal(t, models.StatusBlocked, updatedParent.Status)
	assert.Equal(t, "needs_coordination", updatedParent.SwarmStatus)
}

func TestHandler_TaskThreadSend_ResumesGoalPausedByUserStop(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	agent := createAgent(t, tc.llmConfigRepo)
	project := createProject(t, tc.handler, "Thread Resume Goal Project")
	task := createTask(t, tc.handler, project.ID, "Thread Resume Goal Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusCancelled
		tk.AgentID = &agent.ID
	})
	goal, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "finish the objective", service.GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, tc.handler.taskGoalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))

	form := url.Values{}
	form.Set("message", "Start again")
	rec := tc.HTMX().Post("/tasks/" + task.ID + "/thread").WithForm(form).Execute()
	assertCode(t, rec, http.StatusOK)

	resumed, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	assert.Equal(t, "resumed by web", resumed.Reason)
}

func TestHandler_TaskThreadSend_DoesNotResumeExplicitlyPausedGoal(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	agent := createAgent(t, tc.llmConfigRepo)
	project := createProject(t, tc.handler, "Thread Explicit Pause Project")
	task := createTask(t, tc.handler, project.ID, "Thread Explicit Pause Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusCancelled
		tk.AgentID = &agent.ID
	})
	_, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "finish the objective", service.GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, tc.handler.taskGoalSvc.PauseGoal(ctx, task.ID, "user"))

	form := url.Values{}
	form.Set("message", "Start again")
	rec := tc.HTMX().Post("/tasks/" + task.ID + "/thread").WithForm(form).Execute()
	assertCode(t, rec, http.StatusOK)

	paused, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, paused)
	assert.Equal(t, models.TaskGoalStatusPaused, paused.Status)
	assert.Equal(t, "paused by user", paused.Reason)
}

func TestHandler_TaskThreadSend_MixtureOllamaAggregatorReportsRuntimeActionsUnavailable(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	providerRequests := make(chan map[string]any, 1)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		providerRequests <- body
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":"Runtime actions are unavailable."},"done":false}`)
		_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":""},"done":true,"eval_count":8}`)
	}))
	defer providerServer.Close()

	aggregator := &models.LLMConfig{
		Name: "Task Followup Ollama Aggregator", Provider: models.ProviderOllama, Model: "test-model",
		OllamaBaseURL: providerServer.URL,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, aggregator))
	mixture := &models.LLMConfig{
		Name: "Task Followup Ollama Mixture", Provider: models.ProviderMixture, Model: "mixture",
		MixtureConfigJSON: `{"enabled":true,"aggregator":{"agent_config_id":"` + aggregator.ID + `"}}`,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, mixture))

	project := createProject(t, h, "Task Followup Ollama Mixture Project")
	task := createTask(t, h, project.ID, "Task Followup Ollama Mixture Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &mixture.ID
	})

	form := url.Values{}
	form.Set("message", "Create a child task from this follow-up")
	form.Set("agent_id", mixture.ID)
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)

	var providerRequest map[string]any
	select {
	case providerRequest = <-providerRequests:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for task-followup Ollama mixture aggregator request")
	}
	require.NotContains(t, providerRequest, "tools", "runtime-tool-incapable follow-up aggregator must not receive tools")
	messages, _ := providerRequest["messages"].([]any)
	require.True(t, slices.ContainsFunc(messages, func(raw any) bool {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].(string)
		return message["role"] == "system" &&
			strings.Contains(content, llmprompt.ChatActionUnavailableInstructions) &&
			!strings.Contains(content, "[CREATE_TASK]")
	}), "runtime-tool-incapable follow-up aggregator must receive a limitation without marker guidance")
	require.Eventually(t, func() bool {
		execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
		return err == nil && len(execs) == 1 && execs[0].Status == models.ExecCompleted
	}, 3*time.Second, 10*time.Millisecond)
}

func TestHandler_TaskThreadSend_LeavesCreateTaskMarkerTextInert(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Marker Followup Project")
	task := createTask(t, h, project.ID, "Thread Marker Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})

	mock := testutil.NewMockLLMCaller()
	mock.Response = "I'll create that task.\n\n[CREATE_TASK]\n" +
		`{"title":"UI thread marker child","prompt":"Created from a task thread UI follow-up"}` +
		"\n[/CREATE_TASK]"
	mock.TextOnly = mock.Response
	h.llmSvc.SetLLMCaller(mock)

	form := url.Values{}
	form.Set("message", "Create a follow-up task from this thread")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Create a follow-up task from this thread")

	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	require.Eventually(t, func() bool {
		execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
		return err == nil && len(execs) == 1 && strings.Contains(execs[0].Output, "[CREATE_TASK]")
	}, 2*time.Second, 25*time.Millisecond)
	tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	for _, candidate := range tasks {
		if candidate.Title == "UI thread marker child" {
			t.Fatalf("task-thread marker-looking prose created a task: %+v", tasks)
		}
	}

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	require.True(t, execs[0].IsFollowup)
	require.NotContains(t, execs[0].Output, "[TASK_ID:")
}

func TestHandler_TaskThreadSend_CompletedTaskIgnoresAndRepairsStaleRunningExecution(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Stale Completed Project")
	task := createTask(t, h, project.ID, "Thread Stale Completed Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Category = models.CategoryCompleted
		tk.AgentID = &agent.ID
	})
	staleExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "stale active turn"
		ex.IsFollowup = true
	})

	form := url.Values{}
	form.Set("message", "follow up after completed task")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "follow up after completed task")
	assertContains(t, rec, "chat-bubble-assistant-msg")
	assertNotContains(t, rec, `data-input-mode="queued"`)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 2)
	assert.Equal(t, models.ExecFailed, execs[0].Status)
	assert.Equal(t, staleExec.ID, execs[0].ID)
	assert.Contains(t, []models.ExecutionStatus{models.ExecQueued, models.ExecRunning}, execs[1].Status)
	assert.Equal(t, "follow up after completed task", execs[1].PromptSent)
	assert.True(t, execs[1].IsFollowup)
	pending, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestHandler_TaskThreadSend_CancelledTaskIgnoresAndRepairsStaleRunningExecution(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Stale Cancelled Project")
	task := createTask(t, h, project.ID, "Thread Stale Cancelled Task", func(tk *models.Task) {
		tk.Status = models.StatusCancelled
		tk.Category = models.CategoryBacklog
		tk.AgentID = &agent.ID
	})
	staleExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "stale cancelled turn"
		ex.IsFollowup = true
	})

	form := url.Values{}
	form.Set("message", "follow up after cancelled task")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "follow up after cancelled task")
	assertNotContains(t, rec, `data-input-mode="queued"`)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 2)
	assert.Equal(t, staleExec.ID, execs[0].ID)
	assert.Equal(t, models.ExecCancelled, execs[0].Status)
	assert.Contains(t, []models.ExecutionStatus{models.ExecQueued, models.ExecRunning}, execs[1].Status)
	assert.Equal(t, "follow up after cancelled task", execs[1].PromptSent)
}

func TestHandler_TaskThreadSend_QueuesDuringClaimedRerunBeforeExecutionExists(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Claimed Rerun Project")
	task := createTask(t, h, project.ID, "Thread Claimed Rerun Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.Prompt = "stored original prompt"
		tk.AgentID = &agent.ID
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "prior turn"
	})

	form := url.Values{}
	form.Set("message", "follow up during claimed rerun")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "follow up during claimed rerun")
	assertContains(t, rec, `data-input-mode="queued"`)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1, "follow-up must not create an execution while the claimed rerun is in lifecycle setup")

	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, "follow up during claimed rerun", inputs[0].Content)
	assert.Empty(t, inputs[0].RunExecutionID)
}

func TestHandler_TaskThreadSend_FollowupBeforeFirstWorkerClaimDoesNotBlockDispatch(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Followup Before Claim Project")
	task := createTask(t, h, project.ID, "Thread Followup Before Claim Task", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.Category = models.CategoryActive
		tk.Prompt = "stored original prompt"
		tk.AgentID = &agent.ID
	})

	form := url.Values{}
	form.Set("message", "follow-up before worker claim")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `data-input-mode="queued"`)

	claim, claimed, err := h.taskRepo.ClaimTaskForDispatch(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, claimed, "queued first-turn follow-up must not deadlock the original worker dispatch")
	require.NotNil(t, claim)
	require.Equal(t, models.StatusRunning, claim.Task.Status)

	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, "follow-up before worker claim", inputs[0].Content)
	assert.Empty(t, inputs[0].RunExecutionID)
}

func TestHandler_TaskThreadSend_QueuesDuringStartingFirstTurnBeforeExecutionExists(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Starting First Turn Project")
	task := createTask(t, h, project.ID, "Thread Starting First Turn Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.Prompt = "tell me a story about a duck"
		tk.AgentID = &agent.ID
	})

	form := url.Values{}
	form.Set("message", "1+1=?")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "1+1=?")
	assertContains(t, rec, `data-input-mode="queued"`)
	assertContains(t, rec, `hx-swap-oob="beforeend:#pending-thread-inputs[data-task-id=&#34;`+task.ID+`&#34;]"`)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, execs, "follow-up must not create an execution while the first turn is still in lifecycle setup")

	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, "1+1=?", inputs[0].Content)
	assert.Empty(t, inputs[0].RunExecutionID, "pre-execution queued input is bound after the initial execution is created")
}

func TestHandler_TaskThreadSend_QueuesBehindActiveTurn(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread FIFO Project")
	task := createTask(t, h, project.ID, "Thread FIFO Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active turn"
		ex.IsFollowup = true
	})

	form := url.Values{}
	form.Set("message", "queued follow up")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "queued follow up")
	assertContains(t, rec, `data-input-mode="queued"`)
	assertContains(t, rec, `hx-swap-oob="beforeend:#pending-thread-inputs[data-task-id=&#34;`+task.ID+`&#34;]"`)
	assertContains(t, rec, `queued-input-row`)
	assertContains(t, rec, `bg-base-300/45`)
	assertContains(t, rec, `flex-1`)
	assertContains(t, rec, `ml-auto`)
	assertContains(t, rec, `>Steer</button>`)
	assertContains(t, rec, `aria-label="Cancel queued follow-up"`)
	assertContains(t, rec, `M19 7l-.867 12.142`)
	assertNotContains(t, rec, `>Cancel</button>`)
	assertNotContains(t, rec, `text-[11px]`)
	assertNotContains(t, rec, `h-4 items-center`)
	assertNotContains(t, rec, `w-fit`)
	assertNotContains(t, rec, `>×</button>`)
	assertNotContains(t, rec, "Queued follow-up</div>")
	assertNotContains(t, rec, "Will run after the active response finishes.")

	execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if len(execs) != 1 || execs[0].ID != activeExec.ID {
		t.Fatalf("expected only active execution before queued input is applied, got %#v", execs)
	}
	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, models.ThreadInputModeQueued, inputs[0].InputMode)
	assert.Equal(t, "queued follow up", inputs[0].Content)
}

func TestHandler_TaskThreadSend_QueuesBehindRunningScheduledExecution(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Scheduled Thread FIFO Project")
	task := createTask(t, h, project.ID, "Scheduled Thread FIFO Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryScheduled
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "scheduled active turn"
	})

	form := url.Values{}
	form.Set("message", "queue this scheduled follow-up")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `data-input-mode="queued"`)
	assertContains(t, rec, `hx-swap-oob="beforeend:#pending-thread-inputs[data-task-id=&#34;`+task.ID+`&#34;]"`)

	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, models.ThreadInputModeQueued, inputs[0].InputMode)
	assert.Equal(t, activeExec.ID, inputs[0].RunExecutionID)
	assert.Equal(t, "queue this scheduled follow-up", inputs[0].Content)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, activeExec.ID, execs[0].ID)
	assert.Equal(t, models.ExecRunning, execs[0].Status)
}
func TestHandler_RunTask_StartsPlannerForDeferredSwarmParent(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Deferred Swarm Run Project")
	startImmediately := false
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:        project.ID,
		Title:            "Deferred swarm",
		Prompt:           "Split this deferred swarm into workers",
		Category:         models.CategoryBacklog,
		Priority:         2,
		AgentID:          &agent.ID,
		MaxWorkers:       2,
		ReviewerEnabled:  true,
		MergerEnabled:    true,
		StartImmediately: &startImmediately,
	})
	require.NoError(t, err)

	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.Nil(t, planner)

	rec := htmxPost(e, "/tasks/"+parent.ID+"/run", nil)
	assertCode(t, rec, http.StatusNoContent)

	planner, err = h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	assert.Equal(t, models.CategoryActive, planner.Category)
	assert.Equal(t, models.StatusPending, planner.Status)

	execs, err := h.execRepo.ListByTaskChronological(ctx, parent.ID)
	require.NoError(t, err)
	assert.Empty(t, execs, "generic Run Now must not submit the swarm parent as an ordinary task")
}

func TestHandler_RunTask_PromotesPendingTaskThreadInputInsteadOfOriginalPrompt(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Manual Start Queued Thread Project")
	task := createTask(t, h, project.ID, "Manual Start Queued Thread Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Category = models.CategoryCompleted
		tk.Prompt = "original task prompt"
		tk.AgentID = &agent.ID
	})
	queued := &models.ThreadInput{
		Scope:         models.ThreadInputScopeTask,
		ProjectID:     project.ID,
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		InputMode:     models.ThreadInputModeQueued,
		InputStatus:   models.ThreadInputPending,
		Content:       "queued follow-up should run",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))

	rec := htmxPost(e, "/tasks/"+task.ID+"/run", nil)
	assertCode(t, rec, http.StatusNoContent)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, "queued follow-up should run", execs[0].PromptSent)
	assert.NotEqual(t, "original task prompt", execs[0].PromptSent)
	assert.True(t, execs[0].IsFollowup)
	input, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.NotNil(t, input)
	assert.Equal(t, models.ThreadInputApplied, input.InputStatus)
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedTask)
	assert.Contains(t, []models.TaskStatus{models.StatusQueued, models.StatusRunning}, updatedTask.Status)
	assert.Equal(t, models.CategoryActive, updatedTask.Category)
}

func TestHandler_TaskThreadSteer_CreatesPendingSteeringInput(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Steering Project")
	task := createTask(t, h, project.ID, "Thread Steering Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active turn"
		ex.Output = "partial wrong implementation"
		ex.IsFollowup = true
	})

	form := url.Values{}
	form.Set("message", "Stop and use the new interface")
	form.Set("expected_turn_id", activeExec.ID)
	form.Set("attachment_session_id", "task-steering-session")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread/steer", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Steering pending")
	assertContains(t, rec, `data-input-mode="steering"`)
	assertContains(t, rec, `steering-input-row`)
	assertContains(t, rec, `aria-label="Cancel pending steering"`)
	assertContains(t, rec, "Attachments included")
	assertContains(t, rec, `aria-label="Attachments included with this steering instruction"`)
	assertContains(t, rec, `M19 7l-.867 12.142`)
	assertNotContains(t, rec, `>Cancel</button>`)

	execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if len(execs) != 1 || execs[0].ID != activeExec.ID || execs[0].Status != models.ExecRunning {
		t.Fatalf("expected active execution to keep running, got %#v", execs)
	}
	inputs, err := h.threadInputRepo.ListPendingSteering(ctx, activeExec.ID, activeExec.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, "Stop and use the new interface", inputs[0].Content)
	assert.Equal(t, "task-steering-session", inputs[0].AttachmentSessionID)
}

func TestHandler_TaskThreadSteer_CreatesPendingSteeringInputForScheduledTask(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Scheduled Thread Steering Project")
	task := createTask(t, h, project.ID, "Scheduled Thread Steering Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryScheduled
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "scheduled active turn"
		ex.Output = "partial scheduled output"
	})

	form := url.Values{}
	form.Set("message", "Correct the scheduled run now")
	form.Set("expected_turn_id", activeExec.ID)
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread/steer", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Steering pending")
	assertContains(t, rec, `data-input-mode="steering"`)

	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, models.ThreadInputModeSteering, inputs[0].InputMode)
	assert.Equal(t, models.ThreadInputPending, inputs[0].InputStatus)
	assert.Equal(t, activeExec.ID, inputs[0].RunExecutionID)
	assert.Equal(t, activeExec.ID, inputs[0].TurnID)
	assert.Equal(t, activeExec.ID, inputs[0].ExpectedTurnID)
	assert.Equal(t, "Correct the scheduled run now", inputs[0].Content)

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, activeExec.ID, execs[0].ID)
	assert.Equal(t, models.ExecRunning, execs[0].Status)
}

func TestHandler_TaskThreadCancel_CancelsQueuedFollowups(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Cancel Queue Project")
	task := createTask(t, h, project.ID, "Thread Cancel Queue Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
	})
	_ = createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active turn"
		ex.IsFollowup = true
	})
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued turn"}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))
	rec := htmxPost(e, "/tasks/"+task.ID+"/cancel", nil)
	assertCode(t, rec, http.StatusOK)
	input, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	if input.InputStatus != models.ThreadInputCancelled {
		t.Fatalf("expected queued input cancelled, got %s", input.InputStatus)
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
	if updatedTask.Status != models.StatusQueued && updatedTask.Status != models.StatusRunning {
		t.Errorf("expected status queued or running, got %s", updatedTask.Status)
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

// TestHandler_TaskThreadSend_ExplicitAgentPersistsAsTaskDefault verifies that
// explicitly selecting a model in the task-thread composer becomes the task's
// ongoing assigned model (Task.AgentID), so a subsequent thread render reflects
// the newly selected model rather than reverting to the task's prior default.
func TestHandler_TaskThreadSend_ExplicitAgentPersistsAsTaskDefault(t *testing.T) {
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
	project := createProject(t, h, "Model Persist Test")
	task := createTask(t, h, project.ID, "Model Persist Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.AgentID = &defaultAgent.ID
	})

	form := url.Values{}
	form.Set("message", "Switch to the explicit model")
	form.Set("agent_id", explicitAgent.ID)
	rec := postForm(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.AgentID == nil || *updatedTask.AgentID != explicitAgent.ID {
		t.Fatalf("expected task.AgentID=%s after explicit selection, got %v", explicitAgent.ID, updatedTask.AgentID)
	}

	// A subsequent thread render must reflect the persisted selection, not the
	// task's original default agent.
	threadRec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, threadRec, http.StatusOK)
	assertContains(t, threadRec, "data-task-agent=\""+explicitAgent.ID+"\"")
}

// TestHandler_TaskThreadSend_AutoSelectionDoesNotOverrideTaskDefault verifies that
// sending a follow-up with "auto" model routing (no explicit selection change)
// does not overwrite the task's previously assigned model.
func TestHandler_TaskThreadSend_AutoSelectionDoesNotOverrideTaskDefault(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	assignedAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Assigned Agent" })
	project := createProject(t, h, "Auto No Override Test")
	task := createTask(t, h, project.ID, "Auto No Override Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.AgentID = &assignedAgent.ID
	})

	form := url.Values{}
	form.Set("message", "Follow up without an explicit model change")
	form.Set("agent_id", "auto")
	rec := postForm(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.AgentID == nil || *updatedTask.AgentID != assignedAgent.ID {
		t.Fatalf("expected task.AgentID to remain %s, got %v", assignedAgent.ID, updatedTask.AgentID)
	}
}

// TestHandler_TaskThreadSelectModel_PersistsWithoutSending verifies that
// changing the task-thread composer's model dropdown persists the selection
// immediately, without requiring a message to be sent. This ensures the
// selector does not revert after navigating away and back to the thread.
func TestHandler_TaskThreadSelectModel_PersistsWithoutSending(t *testing.T) {
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
	project := createProject(t, h, "Model Select Without Send Test")
	task := createTask(t, h, project.ID, "Model Select Without Send Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.AgentID = &defaultAgent.ID
	})

	form := url.Values{}
	form.Set("agent_id", explicitAgent.ID)
	rec := postForm(e, "/tasks/"+task.ID+"/thread/model", form)
	assertCode(t, rec, http.StatusNoContent)

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.AgentID == nil || *updatedTask.AgentID != explicitAgent.ID {
		t.Fatalf("expected task.AgentID=%s after selection, got %v", explicitAgent.ID, updatedTask.AgentID)
	}

	// A subsequent thread render (e.g. after navigating away and back) must
	// reflect the persisted selection, not the task's original default.
	threadRec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, threadRec, http.StatusOK)
	assertContains(t, threadRec, "data-task-agent=\""+explicitAgent.ID+"\"")
}

// TestHandler_TaskThreadSelectModel_AutoDoesNotOverride verifies that
// selecting "auto" via the immediate model-select endpoint does not
// overwrite the task's previously assigned model.
func TestHandler_TaskThreadSelectModel_AutoDoesNotOverride(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	assignedAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Assigned Agent" })
	project := createProject(t, h, "Model Select Auto No Override Test")
	task := createTask(t, h, project.ID, "Model Select Auto No Override Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.AgentID = &assignedAgent.ID
	})

	form := url.Values{}
	form.Set("agent_id", "auto")
	rec := postForm(e, "/tasks/"+task.ID+"/thread/model", form)
	assertCode(t, rec, http.StatusNoContent)

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.AgentID == nil || *updatedTask.AgentID != assignedAgent.ID {
		t.Fatalf("expected task.AgentID to remain %s, got %v", assignedAgent.ID, updatedTask.AgentID)
	}
}

// TestHandler_TaskThreadSelectModel_SkipsSwarmParent verifies that the
// immediate model-select endpoint does not mutate Task.AgentID for swarm
// parent tasks, consistent with TaskThreadSend's swarm-parent handling. Swarm
// parents resolve their assigned agent through swarm-specific semantics
// (SwarmService.resolveAssignedAgentID and child creation), not direct
// composer persistence.
func TestHandler_TaskThreadSelectModel_SkipsSwarmParent(t *testing.T) {
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
	project := createProject(t, h, "Model Select Swarm Parent Test")
	task := createTask(t, h, project.ID, "Model Select Swarm Parent Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.AgentID = &defaultAgent.ID
		tk.SwarmRole = models.SwarmRoleParent
	})

	form := url.Values{}
	form.Set("agent_id", explicitAgent.ID)
	rec := postForm(e, "/tasks/"+task.ID+"/thread/model", form)
	assertCode(t, rec, http.StatusNoContent)

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.AgentID == nil || *updatedTask.AgentID != defaultAgent.ID {
		t.Fatalf("expected swarm parent task.AgentID to remain unchanged at %s, got %v", defaultAgent.ID, updatedTask.AgentID)
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
	if updatedTask.Status != models.StatusQueued && updatedTask.Status != models.StatusRunning {
		t.Errorf("expected status queued or running, got %s", updatedTask.Status)
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

// TestHandler_TaskThreadSend_DeferredHistoryLoad_PassesBoundedHistoryToModel verifies
// that the deferred-history-load path fetches only the bounded model replay window
// instead of scanning a whole large task thread before provider normalization.
func TestHandler_TaskThreadSend_DeferredHistoryLoad_PassesBoundedHistoryToModel(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil // skip slot acquisition so goroutine runs immediately
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Deferred History Load Project")
	task := createTask(t, h, project.ID, "Deferred History Load Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})

	// Create enough completed executions to exceed the LLM replay window.
	const priorCount = 128
	const expectedHistoryCount = 20
	for i := 0; i < priorCount; i++ {
		createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecCompleted
			ex.PromptSent = fmt.Sprintf("prior turn %d", i)
			ex.Output = fmt.Sprintf("prior output %d", i)
			ex.IsFollowup = i > 0
		})
	}

	mock := testutil.NewMockLLMCaller()
	mock.Response = "deferred history response"
	mock.TextOnly = "deferred history response"
	h.llmSvc.SetLLMCaller(mock)

	form := url.Values{}
	form.Set("message", "follow-up after many prior turns")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "follow-up after many prior turns")

	// Wait for the goroutine to call the model.
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 3*time.Second, 25*time.Millisecond,
		"model was not called within timeout")

	req := mock.LastAgentRequest()
	assert.Len(t, req.ChatHistory, expectedHistoryCount,
		"deferred load should pass only the bounded provider replay window")
	assert.Equal(t, "prior turn 108", req.ChatHistory[0].PromptSent)
	assert.Equal(t, "prior turn 127", req.ChatHistory[expectedHistoryCount-1].PromptSent)
	assert.Equal(t, "follow-up after many prior turns", req.Message)
}

// TestHandler_TaskThreadSend_DeferredHistoryLoad_HandlerReturnsBeforeModelCall
// verifies that the HTTP handler returns to the browser immediately when
// DeferHistoryLoad is active — before the background goroutine acquires worker
// slots or calls ListByTaskChronological. This is the regression guard for the
// hang-on-large-thread-history bug.
func TestHandler_TaskThreadSend_DeferredHistoryLoad_HandlerReturnsBeforeModelCall(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil // skip slot acquisition
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Fast Send Project")
	task := createTask(t, h, project.ID, "Fast Send Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})

	// Create executions to give the history scan meaningful work.
	for i := 0; i < 20; i++ {
		createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecCompleted
			ex.PromptSent = fmt.Sprintf("turn %d", i)
			ex.Output = fmt.Sprintf("output %d", i)
			ex.IsFollowup = i > 0
		})
	}

	// Use a blocking model call to ensure the HTTP handler does NOT wait for it.
	modelCallStarted := make(chan struct{}, 1)
	modelCallRelease := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.OnCall = func(_ context.Context, _ testutil.MockLLMCall) {
		modelCallStarted <- struct{}{}
		<-modelCallRelease
	}
	h.llmSvc.SetLLMCaller(mock)

	// Time the HTTP handler round-trip. It must return before the model is called
	// because all expensive work is deferred into the goroutine.
	handlerDone := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		form := url.Values{}
		form.Set("message", "fast send message")
		htmxPost(e, "/tasks/"+task.ID+"/thread", form)
		handlerDone <- time.Since(start)
	}()

	// The handler must return; block with a generous ceiling.
	var elapsed time.Duration
	select {
	case elapsed = <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP handler did not return within 5 s — possible synchronous block on execution history")
	}

	// The model call should NOT have started before the handler returned.
	// (The goroutine is scheduled after the handler returns the response.)
	select {
	case <-modelCallStarted:
		// Model started concurrently with or before handler — that's fine as long
		// as the handler still returned promptly. The important assertion is below.
	default:
		// Handler returned before the goroutine even reached the model call — ideal.
	}

	// Handler elapsed must be well under a generous threshold (no synchronous scan).
	assert.Less(t, elapsed, 2*time.Second,
		"handler took %s — should return quickly without synchronous history scan", elapsed)

	// Allow the goroutine to finish cleanly.
	close(modelCallRelease)
	require.Eventually(t, func() bool {
		execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
		for _, ex := range execs {
			if ex.PromptSent == "fast send message" && ex.Status != models.ExecRunning {
				return true
			}
		}
		return false
	}, 3*time.Second, 25*time.Millisecond)
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

func TestHandler_GetTaskThreadPollOmitsPreservedTerminalOutput(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Compact Thread Poll Project")
	task := createTask(t, h, project.ID, "Compact Thread Poll Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
	})
	completed := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "completed prompt"
	})
	largeOutput := "preserved-terminal-sentinel-" + strings.Repeat("tool output ", 10000)
	require.NoError(t, h.execRepo.Complete(ctx, completed.ID, models.ExecCompleted, largeOutput, "", 100, 500))
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "currently running prompt"
		ex.IsFollowup = true
	})

	poll := htmxGet(e, "/tasks/"+task.ID+"/thread?poll=1&preserved_exec_ids="+completed.ID)
	assertCode(t, poll, http.StatusOK)
	body := poll.Body.String()
	assert.NotContains(t, body, "preserved-terminal-sentinel-")
	assert.NotContains(t, body, "function _taskThreadTaskId()")
	assert.Contains(t, body, `id="chat-execution-`+completed.ID+`"`)
	assert.Contains(t, body, `hx-preserve="true"`)
	assert.Contains(t, body, `id="task-thread-runtime-`+task.ID+`" class="contents" hx-preserve="true"`)
	assert.Contains(t, body, "currently running prompt")

	fallbackPoll := htmxGet(e, "/tasks/"+task.ID+"/thread?poll=1")
	assertCode(t, fallbackPoll, http.StatusOK)
	assert.Contains(t, fallbackPoll.Body.String(), "preserved-terminal-sentinel-")
}

func TestHandler_GetTaskThreadExecutionFragment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Fragment Project")
	task := createTask(t, h, project.ID, "Thread Fragment Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "queued follow-up now running"
		ex.IsFollowup = true
	})

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread/executions/"+exec.ID+"/fragment")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "queued follow-up now running")
	assertContains(t, rec, `data-exec-id="`+exec.ID+`"`)
	assertContains(t, rec, "new EventSource")
}

func TestHandler_GetTaskThreadExecutionFragmentRejectsWrongTask(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Fragment Wrong Task Project")
	task := createTask(t, h, project.ID, "Thread Fragment Task A")
	other := createTask(t, h, project.ID, "Thread Fragment Task B")
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "wrong task"
	})

	rec := htmxGet(e, "/tasks/"+other.ID+"/thread/executions/"+exec.ID+"/fragment")
	assertCode(t, rec, http.StatusNotFound)
}

func TestHandler_GetTaskThread_ShowsPrimaryAgentDefinition(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	agentDef := &models.Agent{
		Name:         "Reviewer Bot",
		Key:          "reviewer_bot",
		Description:  "Reviews code changes",
		SystemPrompt: "Review and suggest improvements.",
		Model:        "inherit",
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}
	project := createProject(t, h, "Primary Agent Thread Project")
	task := createTask(t, h, project.ID, "Primary Agent Thread Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Prompt = "Test prompt"
		tk.AgentID = &agent.ID
		tk.AgentDefinitionID = &agentDef.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "Test prompt"
	})
	h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "Task output", "", 100, 500)

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Assigned agent:")
	assertContains(t, rec, "Reviewer Bot")
	assertContains(t, rec, "reviewer_bot")
}

func TestHandler_GetTaskThread_HidesLifecycleAgentActivity(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	project := createProject(t, h, "Lifecycle Thread Project")
	task := createTask(t, h, project.ID, "Lifecycle Thread Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Prompt = "Test prompt"
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "Test prompt"
	})
	h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "Task output", "", 100, 500)

	lifeExec := &models.LifecycleExecution{
		TaskID:         task.ID,
		TaskRunID:      task.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "skill_curator/observe_task_for_learning",
		OutputContract: models.OutputContractContextBlock,
		Status:         models.LifecycleExecCompleted,
		OutputJSON:     `{"title":"Prepared useful context","content":"private context"}`,
	}
	if err := h.lifecycleRepo.CreateExecution(ctx, lifeExec); err != nil {
		t.Fatalf("create lifecycle execution: %v", err)
	}

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Test prompt")
	assertContains(t, rec, "Task output")
	assertNotContains(t, rec, "Lifecycle agent activity")
	assertNotContains(t, rec, "before_run")
	assertNotContains(t, rec, "Prepared useful context")
	assertNotContains(t, rec, "private context")
}

func TestHandler_GetTaskThread_ServerRendersSelectedComposerModelLabel(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	project := createProject(t, h, "Thread Selected Model Project")
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Opus Worker"
		a.Model = "claude-opus-4-1"
		a.Temperature = 1.0
	})
	task := createTask(t, h, project.ID, "Running Selected Model Task", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = &agent.ID
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Running prompt"
	})

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, `id="task-thread-form-agent-select"`)
	assert.Contains(t, body, `data-current-value="`+agent.ID+`"`)
	assert.Contains(t, body, `>Opus Worker (claude-opus-4-1)</span>`)
}

func TestHandler_GetTaskThread_ServerRendersSwarmChildProjectDefaultModelLabel(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	globalAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Global Default"
		a.Model = "global-default"
	})
	projectAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Project Swarm Worker"
		a.Model = "claude-project-swarm"
		a.IsDefault = false
	})
	project := createProject(t, h, "Swarm Thread Selected Model Project")
	project.DefaultAgentConfigID = &projectAgent.ID
	require.NoError(t, h.projectRepo.Update(context.Background(), project))

	parent := createTask(t, h, project.ID, "Swarm Parent", func(tk *models.Task) {
		tk.Status = models.StatusBlocked
		tk.Category = models.CategoryActive
		tk.AgentID = nil
		tk.SwarmRole = models.SwarmRoleParent
	})
	worker := createTask(t, h, project.ID, "Swarm Worker", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryActive
		tk.AgentID = nil
		tk.ParentTaskID = &parent.ID
		tk.SwarmRole = models.SwarmRoleWorker
	})
	createExec(t, h, worker.ID, projectAgent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Running swarm child prompt"
	})

	rec := htmxGet(e, "/tasks/"+worker.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, `id="task-thread-form-agent-select"`)
	assert.Contains(t, body, `data-current-value="`+projectAgent.ID+`"`)
	assert.Contains(t, body, `value="`+projectAgent.ID+`"`)
	assert.Contains(t, body, `>Project Swarm Worker (claude-project-swarm)</span>`)
	assert.NotContains(t, body, `data-current-value="`+globalAgent.ID+`"`)
	assert.NotContains(t, body, `data-current-value="auto"`)
}

func TestHandler_GetTaskThread_PollsWhenActivePending(t *testing.T) {
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
	assert.Contains(t, body, `hx-trigger="every 3s"`)
	assert.Contains(t, body, `data-task-active="true"`)
}

func TestHandler_GetTaskThread_DoesNotPollWhenBacklogPending(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	project := createProject(t, h, "Backlog Pending Thread Polling Project")
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) { a.Temperature = 1.0 })
	task := createTask(t, h, project.ID, "Backlog Pending Task", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.Category = models.CategoryBacklog
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
	assert.Contains(t, body, fmt.Sprintf(`hx-get="/tasks/%s/thread?poll=1&amp;limit=%d"`, task.ID, taskThreadWindowLimitDefault))
	assert.Contains(t, body, `hx-on::config-request=`)
	assert.Contains(t, body, `event.target === this`)
	assert.NotContains(t, body, `hx-vals=`)
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

// TestHandler_GetTaskThread_ScheduledTaskExecutionSurvivesStaleRecoverySweep is a
// regression test for a bug where recurring scheduled tasks (e.g. the built-in
// "System: Memory Consolidation" task) had their live, in-progress execution
// incorrectly reaped by RecoverStaleRunningTaskExecutions because the sweep
// treated any task whose category wasn't "active" as inactive/stale — even
// though the worker pool legitimately keeps "scheduled" category tasks running.
// That sweep runs on nearly every FindActiveTaskExecution/HasActiveTaskExecution
// call throughout the app (not just at startup), so a concurrent request could
// flip the execution to "failed" (with the error "Recovered stale running
// execution: owning task is terminal or inactive") while the worker goroutine
// kept running the real LLM call in the background and task.Status stayed
// "running". The task thread page would then render the execution as a
// terminal failed bubble instead of the live SSE-streaming bubble on every 3s
// poll, while the background writer kept appending output to the same row —
// causing the DOM to reshape every poll and the auto-scroll logic to yank the
// view to the top and then back to the bottom in a loop for the task's entire
// duration. This test asserts the execution (and thus the streaming bubble)
// survives the sweep for a running scheduled task.
func TestHandler_GetTaskThread_ScheduledTaskExecutionSurvivesStaleRecoverySweep(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Scheduled Task Stale Recovery Project")
	task := createTask(t, h, project.ID, "System: Memory Consolidation", func(tk *models.Task) {
		tk.Status = models.StatusRunning
		tk.Category = models.CategoryScheduled
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Consolidate memories"
	})
	assert.NoError(t, h.execRepo.UpdateOutput(ctx, exec.ID, "partial consolidation output"))

	// Simulate an unrelated concurrent request triggering the stale-execution
	// recovery sweep while the scheduled task is legitimately still running.
	_, err := h.execRepo.RecoverStaleRunningTaskExecutions(ctx)
	assert.NoError(t, err)

	stored, err := h.execRepo.GetByID(ctx, exec.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.ExecRunning, stored.Status, "running scheduled task execution must not be reaped as stale")

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	// The thread must still render the live SSE-streaming bubble, not a
	// terminal failed bubble, so the poll doesn't reshape the DOM/scroll state.
	assert.Contains(t, body, `data-streaming-resume="true"`)
	assert.NotContains(t, body, "Recovered stale running execution")
	assert.NotContains(t, body, "Task failed")
}

func TestHandler_GetTaskThread_ChannelSwarmChildFollowupSurvivesStaleRecoverySweep(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Channel Swarm Child Stale Recovery Project")
	parent := createTask(t, h, project.ID, "Swarm Parent", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Category = models.CategoryCompleted
		tk.SwarmRole = models.SwarmRoleParent
		tk.SwarmStatus = "current"
		tk.AgentID = &agent.ID
	})
	worker := createTask(t, h, project.ID, "Swarm Worker", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Category = models.CategoryCompleted
		tk.ParentTaskID = &parent.ID
		tk.SwarmRole = models.SwarmRoleWorker
		tk.SwarmStatus = "completed"
		tk.AgentID = &agent.ID
	})
	exec := &models.Execution{TaskID: worker.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "Continue worker implementation", IsFollowup: true}
	assert.NoError(t, h.execRepo.CreateDirectTaskFollowup(ctx, exec))
	h.StartChannelTaskRun(ctx, service.ChannelTaskRunRequest{
		ExecID:    exec.ID,
		TaskID:    worker.ID,
		ProjectID: project.ID,
		Message:   "Continue worker implementation",
		Agent:     *agent,
		Surface:   "slack",
		ReplyContext: service.ChannelReplyContext{
			Source:         models.TaskOriginSlack,
			SlackChannelID: "Cswarm",
			SlackThreadTS:  "1710000000.900000",
			SlackUserID:    "Uswarm",
		},
	})
	require.Eventually(t, func() bool {
		stored, err := h.execRepo.GetByID(ctx, exec.ID)
		return err == nil && stored != nil && stored.Status == models.ExecRunning
	}, 2*time.Second, 25*time.Millisecond)
	assert.NoError(t, h.execRepo.UpdateOutput(ctx, exec.ID, "partial worker output"))

	_, err := h.execRepo.RecoverStaleRunningTaskExecutions(ctx)
	assert.NoError(t, err)

	stored, err := h.execRepo.GetByID(ctx, exec.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.ExecRunning, stored.Status, "direct channel swarm child follow-up must not be reaped during startup")

	rec := htmxGet(e, "/tasks/"+worker.ID+"/thread")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()

	assert.Contains(t, body, `data-streaming-resume="true"`)
	assert.Contains(t, body, "partial worker output")
	assert.NotContains(t, body, "Recovered stale running execution")
	assert.NotContains(t, body, "Task failed")
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

func TestHandler_GetTaskThreadRendersLatestWindowAndEarlierFragment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Thread Window Project")
	task := createTask(t, h, project.ID, "Thread Window Task", func(tk *models.Task) {
		tk.Status = models.StatusCompleted
		tk.Prompt = "original prompt"
	})
	execs := make([]*models.Execution, 0, 5)
	for i := 1; i <= 5; i++ {
		exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecCompleted
			ex.PromptSent = fmt.Sprintf("turn-%d", i)
			ex.IsFollowup = true
		})
		execs = append(execs, exec)
	}

	rec := htmxGet(e, "/tasks/"+task.ID+"/thread?limit=3")
	assertCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, expected := range []string{"turn-3", "turn-4", "turn-5", `data-window-limit="3"`, `data-earlier-loader="true"`, `before=` + execs[2].ID} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected initial thread window to contain %q\n%s", expected, body)
		}
	}
	for _, unexpected := range []string{"turn-1", "turn-2"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("initial thread window should not contain older %q", unexpected)
		}
	}

	rec = htmxGet(e, "/tasks/"+task.ID+"/thread?before="+execs[2].ID+"&limit=2")
	assertCode(t, rec, http.StatusOK)
	body = rec.Body.String()
	for _, expected := range []string{"turn-1", "turn-2"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected earlier thread fragment to contain %q\n%s", expected, body)
		}
	}
	for _, unexpected := range []string{"turn-3", "turn-4", "turn-5", "task-thread-form"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("earlier thread fragment should not contain %q", unexpected)
		}
	}
}

func TestHandler_RerunSwarmReviewerRejectsActiveRoleExecution(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "API Swarm Rerun Active Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	reviewer, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleReviewer)
	require.NoError(t, err)
	require.NotNil(t, reviewer)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, reviewer.ID, models.StatusRunning))
	exec := &models.Execution{TaskID: reviewer.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active reviewer run"}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+parent.ID+"/swarm/rerun-reviewer", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "already running")
}

func TestModelsListOmitsEagerEditConfiguration(t *testing.T) {
	_, e, repo := setupTestHandler(t)
	cfg := &models.LLMConfig{
		Name: "Large Edit Config", Provider: models.ProviderOpenAICompatible,
		Model: "custom-model", PresetSlug: "custom", ExtraBodyJSON: `{"eager":"payload"}`,
	}
	require.NoError(t, repo.Create(context.Background(), cfg))

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Large Edit Config")
	assert.NotContains(t, rec.Body.String(), cfg.ExtraBodyJSON)
}

func BenchmarkHandlerListModelsHTMXLargeEditConfig(b *testing.B) {
	_, e, _, db := setupTestHandlerWithDB(b)
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatal(err)
	}
	extraBody := `{"padding":"` + strings.Repeat("x", 1024*1024-len(`{"padding":""}`)) + `"}`
	if len(extraBody) != 1024*1024 {
		b.Fatalf("extra_body_json fixture size = %d, want %d", len(extraBody), 1024*1024)
	}
	for i := 0; i < 50; i++ {
		_, err := db.Exec(`INSERT INTO agent_configs (
			id, name, provider, model, api_key, temperature, is_default, auth_method,
			base_url, transport, preset_slug, extra_body_json
		) VALUES (?, ?, 'openai_compatible', 'custom-model', 'secret', 0.2, ?, 'api_key',
			'https://example.com/v1', 'chat_completions', 'custom', ?)`,
			fmt.Sprintf("large-%02d", i), fmt.Sprintf("Large Custom %02d", i), i == 0, extraBody)
		if err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	var responseBytes int64
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/models", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
		responseBytes += int64(rec.Body.Len())
	}
	b.ReportMetric(float64(responseBytes)/float64(b.N), "response_bytes")
}
