package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestContext holds all the test dependencies and helpers for handler tests
type TestContext struct {
	t              *testing.T
	handler        *Handler
	echo           *echo.Echo
	db             *sql.DB
	llmConfigRepo  *repository.LLMConfigRepo
	projectRepo    *repository.ProjectRepo
	taskRepo       *repository.TaskRepo
	execRepo       *repository.ExecutionRepo
	scheduleRepo   *repository.ScheduleRepo
	workerRepo     *repository.WorkerRepo
	attachmentRepo *repository.AttachmentRepo
	alertRepo      *repository.AlertRepo
	settingsRepo   *repository.SettingsRepo
}

// NewTestContext creates a new test context with all dependencies initialized
func NewTestContext(t *testing.T) *TestContext {
	t.Helper()
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
	settingsRepo := repository.NewSettingsRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc,
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo,
		attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, nil, nil)

	e := echo.New()
	h.RegisterRoutes(e)

	return &TestContext{
		t:              t,
		handler:        h,
		echo:           e,
		db:             db,
		llmConfigRepo:  llmConfigRepo,
		projectRepo:    projectRepo,
		taskRepo:       taskRepo,
		execRepo:       execRepo,
		scheduleRepo:   scheduleRepo,
		workerRepo:     workerRepo,
		attachmentRepo: attachmentRepo,
		alertRepo:      alertRepo,
		settingsRepo:   settingsRepo,
	}
}

// HTTPClient provides fluent HTTP request building and execution for tests
type HTTPClient struct {
	tc     *TestContext
	method string
	path   string
	form   url.Values
	htmx   bool
}

// HTMX creates a new HTMX-enabled HTTP client
func (tc *TestContext) HTMX() *HTTPClient {
	return &HTTPClient{tc: tc, htmx: true}
}

// HTTP creates a new regular HTTP client
func (tc *TestContext) HTTP() *HTTPClient {
	return &HTTPClient{tc: tc, htmx: false}
}

// Get sets up a GET request
func (c *HTTPClient) Get(path string) *HTTPClient {
	c.method = http.MethodGet
	c.path = path
	return c
}

// Post sets up a POST request
func (c *HTTPClient) Post(path string) *HTTPClient {
	c.method = http.MethodPost
	c.path = path
	return c
}

// Put sets up a PUT request
func (c *HTTPClient) Put(path string) *HTTPClient {
	c.method = http.MethodPut
	c.path = path
	return c
}

// Patch sets up a PATCH request
func (c *HTTPClient) Patch(path string) *HTTPClient {
	c.method = http.MethodPatch
	c.path = path
	return c
}

// Delete sets up a DELETE request
func (c *HTTPClient) Delete(path string) *HTTPClient {
	c.method = http.MethodDelete
	c.path = path
	return c
}

// WithForm adds form data to the request
func (c *HTTPClient) WithForm(form url.Values) *HTTPClient {
	c.form = form
	return c
}

// Execute performs the request and returns the response recorder
func (c *HTTPClient) Execute() *httptest.ResponseRecorder {
	var body string
	if c.form != nil && (c.method == http.MethodPost || c.method == http.MethodPut || c.method == http.MethodPatch) {
		body = c.form.Encode()
	}

	req := httptest.NewRequest(c.method, c.path, strings.NewReader(body))
	if c.htmx {
		req.Header.Set("HX-Request", "true")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	rec := httptest.NewRecorder()
	c.tc.echo.ServeHTTP(rec, req)
	return rec
}

// ProjectBuilder provides fluent project creation for tests
type ProjectBuilder struct {
	tc      *TestContext
	project *models.Project
}

// CreateProject creates a new project builder
func (tc *TestContext) CreateProject() *ProjectBuilder {
	return &ProjectBuilder{
		tc:      tc,
		project: &models.Project{},
	}
}

// WithName sets the project name
func (b *ProjectBuilder) WithName(name string) *ProjectBuilder {
	b.project.Name = name
	return b
}

// Build creates the project and returns it
func (b *ProjectBuilder) Build() *models.Project {
	b.tc.t.Helper()
	if b.project.Name == "" {
		b.project.Name = "Test Project"
	}
	if err := b.tc.handler.projectSvc.Create(context.Background(), b.project); err != nil {
		b.tc.t.Fatalf("create project: %v", err)
	}
	return b.project
}

// LLMConfigBuilder provides fluent LLM config creation for tests
type LLMConfigBuilder struct {
	tc     *TestContext
	config *models.LLMConfig
}

// CreateLLMConfig creates a new LLM config builder
func (tc *TestContext) CreateLLMConfig() *LLMConfigBuilder {
	return &LLMConfigBuilder{
		tc: tc,
		config: &models.LLMConfig{
			Provider:  models.ProviderTest,
			Model:     "claude-sonnet-4-5",
			MaxTokens: 4096,
			IsDefault: true,
		},
	}
}

// WithName sets the config name
func (b *LLMConfigBuilder) WithName(name string) *LLMConfigBuilder {
	b.config.Name = name
	return b
}

// WithProvider sets the provider
func (b *LLMConfigBuilder) WithProvider(provider models.LLMProvider) *LLMConfigBuilder {
	b.config.Provider = provider
	return b
}

// WithModel sets the model
func (b *LLMConfigBuilder) WithModel(model string) *LLMConfigBuilder {
	b.config.Model = model
	return b
}

// WithAPIKey sets the API key
func (b *LLMConfigBuilder) WithAPIKey(key string) *LLMConfigBuilder {
	b.config.APIKey = key
	return b
}

// AsDefault marks the config as default
func (b *LLMConfigBuilder) AsDefault() *LLMConfigBuilder {
	b.config.IsDefault = true
	return b
}

// Build creates the LLM config and returns it
func (b *LLMConfigBuilder) Build() *models.LLMConfig {
	b.tc.t.Helper()
	if b.config.Name == "" {
		b.config.Name = "Test Agent"
	}
	if err := b.tc.llmConfigRepo.Create(context.Background(), b.config); err != nil {
		b.tc.t.Fatalf("create llm config: %v", err)
	}
	return b.config
}

// TaskBuilder provides fluent task creation for tests
type TaskBuilder struct {
	tc   *TestContext
	task *models.Task
}

// CreateTask creates a new task builder
func (tc *TestContext) CreateTask(projectID string) *TaskBuilder {
	return &TaskBuilder{
		tc: tc,
		task: &models.Task{
			ProjectID: projectID,
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    "test prompt",
		},
	}
}

// WithTitle sets the task title
func (b *TaskBuilder) WithTitle(title string) *TaskBuilder {
	b.task.Title = title
	return b
}

// WithPrompt sets the task prompt
func (b *TaskBuilder) WithPrompt(prompt string) *TaskBuilder {
	b.task.Prompt = prompt
	return b
}

// WithStatus sets the task status
func (b *TaskBuilder) WithStatus(status models.TaskStatus) *TaskBuilder {
	b.task.Status = status
	return b
}

// WithCategory sets the task category
func (b *TaskBuilder) WithCategory(category models.TaskCategory) *TaskBuilder {
	b.task.Category = category
	return b
}

// WithPriority sets the task priority
func (b *TaskBuilder) WithPriority(priority int) *TaskBuilder {
	b.task.Priority = priority
	return b
}

// WithTag sets the task tag
func (b *TaskBuilder) WithTag(tag models.TaskTag) *TaskBuilder {
	b.task.Tag = tag
	return b
}

// Build creates the task and returns it
func (b *TaskBuilder) Build() *models.Task {
	b.tc.t.Helper()
	if b.task.Title == "" {
		b.task.Title = "Test Task"
	}
	if err := b.tc.handler.taskSvc.Create(context.Background(), b.task); err != nil {
		b.tc.t.Fatalf("create task: %v", err)
	}
	return b.task
}

// ScheduleBuilder provides fluent schedule creation for tests
type ScheduleBuilder struct {
	tc       *TestContext
	schedule *models.Schedule
}

// CreateSchedule creates a new schedule builder
func (tc *TestContext) CreateSchedule(taskID string) *ScheduleBuilder {
	return &ScheduleBuilder{
		tc: tc,
		schedule: &models.Schedule{
			TaskID:         taskID,
			RepeatType:     models.RepeatOnce,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}
}

// WithRunAt sets the schedule run time
func (b *ScheduleBuilder) WithRunAt(runAt time.Time) *ScheduleBuilder {
	b.schedule.RunAt = runAt
	return b
}

// WithRepeatType sets the repeat type
func (b *ScheduleBuilder) WithRepeatType(repeatType models.RepeatType) *ScheduleBuilder {
	b.schedule.RepeatType = repeatType
	return b
}

// WithRepeatInterval sets the repeat interval
func (b *ScheduleBuilder) WithRepeatInterval(interval int) *ScheduleBuilder {
	b.schedule.RepeatInterval = interval
	return b
}

// Disabled marks the schedule as disabled
func (b *ScheduleBuilder) Disabled() *ScheduleBuilder {
	b.schedule.Enabled = false
	return b
}

// Build creates the schedule and returns it
func (b *ScheduleBuilder) Build() *models.Schedule {
	b.tc.t.Helper()
	if b.schedule.RunAt.IsZero() {
		b.schedule.RunAt = time.Now().Add(time.Hour)
	}
	if err := b.tc.scheduleRepo.Create(context.Background(), b.schedule); err != nil {
		b.tc.t.Fatalf("create schedule: %v", err)
	}
	return b.schedule
}

// ExecutionBuilder provides fluent execution creation for tests
type ExecutionBuilder struct {
	tc   *TestContext
	exec *models.Execution
}

// CreateExecution creates a new execution builder
func (tc *TestContext) CreateExecution(taskID, agentID string) *ExecutionBuilder {
	return &ExecutionBuilder{
		tc: tc,
		exec: &models.Execution{
			TaskID:        taskID,
			AgentConfigID: agentID,
			Status:        models.ExecRunning,
		},
	}
}

// WithStatus sets the execution status
func (b *ExecutionBuilder) WithStatus(status models.ExecutionStatus) *ExecutionBuilder {
	b.exec.Status = status
	return b
}

// WithOutput sets the execution output
func (b *ExecutionBuilder) WithOutput(output string) *ExecutionBuilder {
	b.exec.Output = output
	return b
}

// WithError sets the execution error
func (b *ExecutionBuilder) WithError(err string) *ExecutionBuilder {
	b.exec.ErrorMessage = err
	return b
}

// WithPromptSent sets the prompt sent
func (b *ExecutionBuilder) WithPromptSent(prompt string) *ExecutionBuilder {
	b.exec.PromptSent = prompt
	return b
}

// Build creates the execution and returns it
func (b *ExecutionBuilder) Build() *models.Execution {
	b.tc.t.Helper()
	if err := b.tc.execRepo.Create(context.Background(), b.exec); err != nil {
		b.tc.t.Fatalf("create execution: %v", err)
	}
	return b.exec
}

// Assertions provides fluent assertion methods for test responses
type Assertions struct {
	t   *testing.T
	rec *httptest.ResponseRecorder
}

// Assert creates a new assertions helper
func (tc *TestContext) Assert(rec *httptest.ResponseRecorder) *Assertions {
	return &Assertions{t: tc.t, rec: rec}
}

// StatusCode checks that the response has the expected status code
func (a *Assertions) StatusCode(want int) *Assertions {
	a.t.Helper()
	if a.rec.Code != want {
		a.t.Fatalf("expected status %d, got %d; body=%s", want, a.rec.Code, a.rec.Body.String())
	}
	return a
}

// Contains checks that the response body contains the given string
func (a *Assertions) Contains(substr string) *Assertions {
	a.t.Helper()
	if !strings.Contains(a.rec.Body.String(), substr) {
		a.t.Errorf("expected body to contain %q", substr)
	}
	return a
}

// NotContains checks that the response body does NOT contain the given string
func (a *Assertions) NotContains(substr string) *Assertions {
	a.t.Helper()
	if strings.Contains(a.rec.Body.String(), substr) {
		a.t.Errorf("expected body to NOT contain %q", substr)
	}
	return a
}

// Header checks that the response has the expected header value
func (a *Assertions) Header(key, value string) *Assertions {
	a.t.Helper()
	got := a.rec.Header().Get(key)
	if got != value {
		a.t.Errorf("expected header %s=%q, got %q", key, value, got)
	}
	return a
}

// Location checks that the response has the expected Location header (for redirects)
func (a *Assertions) Location(path string) *Assertions {
	a.t.Helper()
	return a.Header("Location", path)
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}