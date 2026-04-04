package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramService_ParseTaskID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "simple task ID",
			input:   "abc123",
			want:    "abc123",
			wantErr: false,
		},
		{
			name:    "task ID with backticks",
			input:   "`abc123`",
			want:    "abc123",
			wantErr: false,
		},
		{
			name:    "task ID with spaces",
			input:   "  abc123  ",
			want:    "abc123",
			wantErr: false,
		},
		{
			name:    "empty task ID",
			input:   "",
			want:    "",
			wantErr: true,
		},
		{
			name:    "only backticks",
			input:   "``",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTaskID(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestTelegramService_FormatTaskID(t *testing.T) {
	tests := []struct {
		name   string
		taskID string
		want   string
	}{
		{
			name:   "simple task ID",
			taskID: "abc123",
			want:   "`abc123`",
		},
		{
			name:   "UUID-like task ID",
			taskID: "550e8400-e29b-41d4-a716-446655440000",
			want:   "`550e8400-e29b-41d4-a716-446655440000`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTaskID(tt.taskID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTelegramService_SplitMessage(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		maxLen int
		want   int // expected number of chunks
	}{
		{
			name:   "short message",
			text:   "Hello",
			maxLen: 100,
			want:   1,
		},
		{
			name:   "exact length",
			text:   "12345",
			maxLen: 5,
			want:   1,
		},
		{
			name:   "split by lines",
			text:   "Line 1\nLine 2\nLine 3",
			maxLen: 10,
			want:   3,
		},
		{
			name:   "long single line",
			text:   "1234567890",
			maxLen: 5,
			want:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitMessage(tt.text, tt.maxLen)
			assert.Equal(t, tt.want, len(chunks))

			// Verify all chunks are within max length
			for i, chunk := range chunks {
				assert.LessOrEqual(t, len(chunk), tt.maxLen,
					"chunk %d exceeds max length: %d > %d", i, len(chunk), tt.maxLen)
			}
		})
	}
}

func TestTelegramService_GetStatusIcon(t *testing.T) {
	tests := []struct {
		status models.TaskStatus
		want   string
	}{
		{models.StatusPending, "⏳"},
		{models.StatusRunning, "🔄"},
		{models.StatusCompleted, "✅"},
		{models.StatusFailed, "❌"},
		{models.StatusCancelled, "🚫"},
		{"unknown", "❓"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := getStatusIcon(tt.status)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTelegramService_GetActiveProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	// Create test projects
	ctx := context.Background()
	defaultProject := &models.Project{
		Name:      "Default Project",
		RepoPath:  "/tmp/default",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, defaultProject))

	otherProject := &models.Project{
		Name:      "Other Project",
		RepoPath:  "/tmp/other",
		IsDefault: false,
	}
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	// Create minimal telegram service (without bot connection)
	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}

	// Test getting active project for new user (should return a default project)
	userID := int64(12345)
	projectID := svc.getActiveProject(userID)
	assert.NotEmpty(t, projectID, "should return a project ID")

	// Test getting active project for user with stored preference
	userID2 := int64(67890)
	svc.userProjects[userID2] = otherProject.ID
	projectID2 := svc.getActiveProject(userID2)
	assert.Equal(t, otherProject.ID, projectID2)
}

func TestTelegramService_EscapeTelegramCommands(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no slashes",
			input: "Hello World",
			want:  "Hello World",
		},
		{
			name:  "slash command in title",
			input: "Refactor task chat to reuse /chat implementation",
			want:  "Refactor task chat to reuse /\u200Bchat implementation",
		},
		{
			name:  "multiple slashes",
			input: "Add /api/projects endpoint for /chrome extension",
			want:  "Add /\u200Bapi/\u200Bprojects endpoint for /\u200Bchrome extension",
		},
		{
			name:  "slash at start",
			input: "/start the process",
			want:  "/\u200Bstart the process",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeTelegramCommands(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTelegramService_IsHexID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid 32-char hex", "b184292efa07a415fc59967a5fc328d1", true},
		{"valid all lowercase", "0123456789abcdef0123456789abcdef", true},
		{"short string", "abc123", false},
		{"project name", "openvibely", false},
		{"mixed case name", "MyProject", false},
		{"uppercase hex (not valid - IDs are lowercase)", "B184292EFA07A415FC59967A5FC328D1", false},
		{"too short hex", "b184292efa07a415", false},
		{"too long hex", "b184292efa07a415fc59967a5fc328d1aa", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHexID(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTelegramService_NewTelegramService_EmptyToken(t *testing.T) {
	svc, err := NewTelegramService("", nil, nil, nil, nil, nil, nil, nil, nil, nil)
	assert.Error(t, err)
	assert.Nil(t, svc)
	assert.Contains(t, err.Error(), "token is empty")
}

func TestTelegramService_NewTelegramService_InvalidToken(t *testing.T) {
	// Invalid token should fail bot creation
	svc, err := NewTelegramService("invalid-token", nil, nil, nil, nil, nil, nil, nil, nil, nil)
	assert.Error(t, err)
	assert.Nil(t, svc)
	assert.Contains(t, err.Error(), "failed to create telegram bot")
}

// Helper to create a TelegramService for testing (no real bot connection)
func newTestTelegramService(t *testing.T) (*TelegramService, *repository.ProjectRepo, *repository.TaskRepo) {
	t.Helper()
	oldUploadsDir := telegramUploadsDir
	telegramUploadsDir = t.TempDir()
	t.Cleanup(func() {
		telegramUploadsDir = oldUploadsDir
	})

	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	settingsRepo := repository.NewSettingsRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)

	svc := &TelegramService{
		taskSvc:            taskSvc,
		projectRepo:        projectRepo,
		llmConfigRepo:      llmConfigRepo,
		taskRepo:           taskRepo,
		execRepo:           execRepo,
		chatAttachmentRepo: chatAttachmentRepo,
		settingsRepo:       settingsRepo,
		scheduleRepo:       scheduleRepo,
		userProjects:       make(map[int64]string),
	}
	return svc, projectRepo, taskRepo
}

func TestTelegramService_FilterChatHistory(t *testing.T) {
	// Test filtering excludes current execution and running executions
	executions := []models.Execution{
		{ID: "exec1", Status: models.ExecCompleted, PromptSent: "hello", Output: "world"},
		{ID: "exec2", Status: models.ExecRunning, PromptSent: "in progress"},
		{ID: "exec3", Status: models.ExecCompleted, PromptSent: "done", Output: "result"},
		{ID: "exec4", Status: models.ExecFailed, PromptSent: "fail", ErrorMessage: "oops"},
	}

	// Filter with exec3 as current
	result := filterTelegramChatHistory(executions, "exec3")
	assert.Len(t, result, 2) // exec1 (completed) + exec4 (failed)
	assert.Equal(t, "exec1", result[0].ID)
	assert.Equal(t, "exec4", result[1].ID)

	// Empty input returns non-nil empty slice
	result = filterTelegramChatHistory([]models.Execution{}, "any")
	assert.NotNil(t, result)
	assert.Empty(t, result)
}

func TestTelegramService_BuildChatContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	ctx := context.Background()
	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Create a regular task
	task := &models.Task{
		Title:     "Fix login bug",
		Prompt:    "Fix it",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
		ProjectID: project.ID,
	}
	require.NoError(t, taskSvc.Create(ctx, task))

	// Create a chat task (should be excluded from context)
	chatTask := &models.Task{
		Title:     "Chat message",
		Prompt:    "Hello",
		Category:  models.CategoryChat,
		Status:    models.StatusCompleted,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, chatTask))

	svc := &TelegramService{
		taskSvc:       taskSvc,
		projectRepo:   projectRepo,
		llmConfigRepo: llmConfigRepo,
		taskRepo:      taskRepo,
		execRepo:      execRepo,
		userProjects:  make(map[int64]string),
	}

	context_ := svc.buildChatContext(ctx, project.ID)
	assert.Contains(t, context_, "Fix login bug")
	assert.NotContains(t, context_, "Chat message")
}

func TestTelegramService_AutoSelectAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := &TelegramService{
		llmConfigRepo: llmConfigRepo,
		userProjects:  make(map[int64]string),
	}

	ctx := context.Background()

	// Should return a model (the default one from migration)
	agent, err := svc.autoSelectAgent(ctx, "hello world", false)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
}

func TestTelegramService_ProcessChatMarkers_NoMarkers(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	// No markers — output should be unchanged
	output := "Here's a list of your tasks"
	result := svc.processChatMarkers(ctx, "exec123", "proj123", output, 12345, 12345)
	assert.Equal(t, output, result)
}

func TestTelegramService_ProcessChatMarkers_CreateTask(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	output := `Sure, I'll create that task for you.

[CREATE_TASK]
{"title": "Fix login bug", "prompt": "The login form crashes on empty input", "category": "backlog"}
[/CREATE_TASK]`

	result := svc.processChatMarkers(ctx, "exec123", project.ID, output, 12345, 12345)

	// Should have created the task and appended a summary
	assert.Contains(t, result, "Fix login bug")

	// Verify task was created
	tasks, err := svc.taskSvc.ListByProject(ctx, project.ID, "backlog")
	require.NoError(t, err)

	var found bool
	for _, task := range tasks {
		if task.Title == "Fix login bug" {
			found = true
			assert.Equal(t, "The login form crashes on empty input", task.Prompt)
			break
		}
	}
	assert.True(t, found, "task should have been created from marker")
}

func TestTelegramService_RuntimeCreateTaskTool_SetsTelegramOrigin(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Tool Runtime Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	collector := newChannelActionSummaryCollector()
	rt := svc.buildTelegramActionToolRuntime(project.ID, 12345, 12345, collector)
	require.NotNil(t, rt)

	output, handled, isErr, err := rt.Executor(ctx, "create_task", json.RawMessage(`{"title":"Telegram Tool Created","prompt":"Do it","category":"backlog"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, "Created 1 task(s):")

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)

	var created *models.Task
	for i := range tasks {
		if tasks[i].Title == "Telegram Tool Created" {
			created = &tasks[i]
			break
		}
	}
	require.NotNil(t, created)
	require.Equal(t, models.TaskOriginTelegram, created.CreatedVia)
	require.Equal(t, int64(12345), created.TelegramChatID)

	finalOutput := collector.appendToOutput("Done.")
	require.Contains(t, finalOutput, "[TASK_ID:")
}

func TestTelegramService_RuntimeListAlertsTool_Handled(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{Name: "Telegram Alerts Runtime", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	alertRepo := repository.NewAlertRepo(testutil.NewTestDB(t))
	svc.alertSvc = NewAlertService(alertRepo, nil)

	rt := svc.buildTelegramActionToolRuntime(project.ID, 12345, 12345, nil)
	require.NotNil(t, rt)

	output, handled, isErr, err := rt.Executor(ctx, "list_alerts", json.RawMessage(`{}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, "No alerts found")
}

func TestTelegramService_RuntimeExecutorHandlesAllDefinedTools(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{Name: "Telegram Full Runtime", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	alertRepo := repository.NewAlertRepo(testutil.NewTestDB(t))
	svc.alertSvc = NewAlertService(alertRepo, nil)

	rt := svc.buildTelegramActionToolRuntime(project.ID, 12345, 12345, nil)
	require.NotNil(t, rt)

	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceTelegram, true)
	require.NotEmpty(t, defs)

	for _, d := range defs {
		_, handled, _, _ := rt.Executor(ctx, d.Name, json.RawMessage(`{}`))
		require.Truef(t, handled, "tool should be handled by telegram runtime executor: %s", d.Name)
	}

	handlers := svc.telegramActionHandlers(project.ID, 12345, 12345, nil)
	require.NoError(t, chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceTelegram, true, handlers))
}

func TestTelegramService_CompleteExecution_Success(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	ctx := context.Background()
	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Get the default agent config from migration
	agents, err := llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	agentID := agents[0].ID

	task := &models.Task{
		Title:     "Test task",
		Prompt:    "Test",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecRunning,
		PromptSent:    "test message",
	}
	require.NoError(t, execRepo.Create(ctx, exec))

	svc := &TelegramService{
		taskSvc:      taskSvc,
		taskRepo:     taskRepo,
		execRepo:     execRepo,
		userProjects: make(map[int64]string),
	}

	svc.completeExecution(ctx, exec.ID, task.ID, "response output", "", 100, 1000)

	// Verify execution was completed
	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecCompleted, updatedExec.Status)

	// Verify task status
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, updatedTask.Status)
}

func TestTelegramService_CompleteExecution_Failure(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	ctx := context.Background()
	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Get the default agent config from migration
	agents, err := llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	agentID := agents[0].ID

	task := &models.Task{
		Title:     "Test task",
		Prompt:    "Test",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecRunning,
		PromptSent:    "test message",
	}
	require.NoError(t, execRepo.Create(ctx, exec))

	svc := &TelegramService{
		taskSvc:      taskSvc,
		taskRepo:     taskRepo,
		execRepo:     execRepo,
		userProjects: make(map[int64]string),
	}

	svc.completeExecution(ctx, exec.ID, task.ID, "", "something went wrong", 0, 500)

	// Verify execution was failed
	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecFailed, updatedExec.Status)
	assert.Equal(t, "something went wrong", updatedExec.ErrorMessage)

	// Verify task status
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusFailed, updatedTask.Status)
}

func TestTelegramService_ResolveWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test/repo",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}

	workDir := svc.resolveWorkDir(ctx, project.ID)
	assert.Equal(t, "/tmp/test/repo", workDir)

	// Non-existent project
	workDir = svc.resolveWorkDir(ctx, "nonexistent")
	assert.Equal(t, "", workDir)
}

func TestTelegramService_HandleStart_SetsDefaultProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}

	userID := int64(42)
	response := svc.handleStart(userID)

	assert.Contains(t, response, "Welcome to *OpenVibely*")
	// Should have set a project for the user (could be the migration's seeded default)
	assert.NotEmpty(t, svc.userProjects[userID])
	// Response should mention the project name and chat instructions
	assert.Contains(t, response, "Just send me any message")
}

func TestTelegramService_HandleProject_ListProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{
		Name:      "Project A",
		RepoPath:  "/tmp/a",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project1))

	project2 := &models.Project{
		Name:     "Project B",
		RepoPath: "/tmp/b",
	}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}

	userID := int64(123)
	svc.userProjects[userID] = project1.ID

	// Call with no arguments should list projects
	response := svc.handleProject(userID, "")

	assert.Contains(t, response, "*Current project:* Project A")
	assert.Contains(t, response, "Project A")
	assert.Contains(t, response, "Project B")
	assert.Contains(t, response, "← _current_")
}

func TestTelegramService_HandleProject_SwitchToValidProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{
		Name:      "Project A",
		RepoPath:  "/tmp/a",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project1))

	project2 := &models.Project{
		Name:     "Project B",
		RepoPath: "/tmp/b",
	}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}

	userID := int64(456)
	svc.userProjects[userID] = project1.ID

	// Switch to Project B
	response := svc.handleProject(userID, "Project B")

	assert.Contains(t, response, "Switched to project: *Project B*")
	assert.Equal(t, project2.ID, svc.userProjects[userID])
}

func TestTelegramService_HandleProject_SwitchToInvalidProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{
		Name:      "Project A",
		RepoPath:  "/tmp/a",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project1))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}

	userID := int64(789)
	svc.userProjects[userID] = project1.ID

	// Try to switch to non-existent project
	response := svc.handleProject(userID, "NonExistent")

	assert.Contains(t, response, "Project not found")
	assert.Contains(t, response, "Project A")
	// Should still be on Project A
	assert.Equal(t, project1.ID, svc.userProjects[userID])
}

func TestTelegramService_HandleProject_CaseInsensitiveSwitch(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project := &models.Project{
		Name:      "MyProject",
		RepoPath:  "/tmp/myproject",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}

	userID := int64(999)

	// Switch using lowercase version of project name
	response := svc.handleProject(userID, "myproject")

	assert.Contains(t, response, "Switched to project: *MyProject*")
	assert.Equal(t, project.ID, svc.userProjects[userID])
}

func TestTelegramService_ExtractTelegramAttachment_Photo(t *testing.T) {
	msg := &tgbotapi.Message{
		Photo: []tgbotapi.PhotoSize{
			{FileID: "small_photo", FileSize: 1000, Width: 100, Height: 100},
			{FileID: "large_photo", FileSize: 5000, Width: 800, Height: 600},
		},
		Caption: "Look at this photo",
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "large_photo", fileID) // Should use largest photo
	assert.Contains(t, fileName, "photo_")
	assert.Contains(t, fileName, ".jpg")
	assert.Equal(t, 5000, fileSize)
	assert.Equal(t, "image/jpeg", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_Document(t *testing.T) {
	msg := &tgbotapi.Message{
		Document: &tgbotapi.Document{
			FileID:   "doc_file_id",
			FileName: "report.pdf",
			FileSize: 12345,
			MimeType: "application/pdf",
		},
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "doc_file_id", fileID)
	assert.Equal(t, "report.pdf", fileName)
	assert.Equal(t, 12345, fileSize)
	assert.Equal(t, "application/pdf", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_Audio(t *testing.T) {
	msg := &tgbotapi.Message{
		Audio: &tgbotapi.Audio{
			FileID:   "audio_file_id",
			FileName: "song.mp3",
			FileSize: 9876,
			MimeType: "audio/mpeg",
		},
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "audio_file_id", fileID)
	assert.Equal(t, "song.mp3", fileName)
	assert.Equal(t, 9876, fileSize)
	assert.Equal(t, "audio/mpeg", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_Video(t *testing.T) {
	msg := &tgbotapi.Message{
		Video: &tgbotapi.Video{
			FileID:   "video_file_id",
			FileName: "clip.mp4",
			FileSize: 50000,
			MimeType: "video/mp4",
		},
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "video_file_id", fileID)
	assert.Equal(t, "clip.mp4", fileName)
	assert.Equal(t, 50000, fileSize)
	assert.Equal(t, "video/mp4", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_Voice(t *testing.T) {
	msg := &tgbotapi.Message{
		Voice: &tgbotapi.Voice{
			FileID:   "voice_file_id",
			FileSize: 3000,
			MimeType: "audio/ogg",
		},
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "voice_file_id", fileID)
	assert.Equal(t, "voice.ogg", fileName)
	assert.Equal(t, 3000, fileSize)
	assert.Equal(t, "audio/ogg", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_VideoNote(t *testing.T) {
	msg := &tgbotapi.Message{
		VideoNote: &tgbotapi.VideoNote{
			FileID:   "videonote_file_id",
			FileSize: 7500,
		},
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "videonote_file_id", fileID)
	assert.Equal(t, "video_note.mp4", fileName)
	assert.Equal(t, 7500, fileSize)
	assert.Equal(t, "video/mp4", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_Sticker(t *testing.T) {
	msg := &tgbotapi.Message{
		Sticker: &tgbotapi.Sticker{
			FileID:   "sticker_file_id",
			FileSize: 2000,
		},
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "sticker_file_id", fileID)
	assert.Equal(t, "sticker.webp", fileName)
	assert.Equal(t, 2000, fileSize)
	assert.Equal(t, "image/webp", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_NoAttachment(t *testing.T) {
	msg := &tgbotapi.Message{
		Text: "Just a text message",
	}

	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "", fileID)
	assert.Equal(t, "", fileName)
	assert.Equal(t, 0, fileSize)
	assert.Equal(t, "", mimeType)
}

func TestTelegramService_ExtractTelegramAttachment_DocumentNoName(t *testing.T) {
	msg := &tgbotapi.Message{
		Document: &tgbotapi.Document{
			FileID:   "doc_file_id",
			FileSize: 100,
		},
	}

	fileID, fileName, _, mimeType := extractTelegramAttachment(msg)
	assert.Equal(t, "doc_file_id", fileID)
	assert.Equal(t, "document", fileName)                 // default name
	assert.Equal(t, "application/octet-stream", mimeType) // default mime
}

func TestTelegramService_IsTelegramImageFile(t *testing.T) {
	tests := []struct {
		mimeType string
		want     bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/bmp", false},
		{"application/pdf", false},
		{"audio/mpeg", false},
		{"text/plain", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			got := isTelegramImageFile(tt.mimeType)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTelegramService_MoveOrCopyFile(t *testing.T) {
	// Create a temp source file
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "test.txt")
	require.NoError(t, os.WriteFile(srcPath, []byte("hello world"), 0644))

	// Move to destination
	dstDir := t.TempDir()
	dstPath := filepath.Join(dstDir, "test.txt")
	err := moveOrCopyFile(srcPath, dstPath)
	assert.NoError(t, err)

	// Destination should exist with correct content
	content, err := os.ReadFile(dstPath)
	assert.NoError(t, err)
	assert.Equal(t, "hello world", string(content))

	// Source should no longer exist
	_, err = os.Stat(srcPath)
	assert.True(t, os.IsNotExist(err))
}

func TestTelegramService_LinkAttachmentsToExecution(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Get default agent config from migration
	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)

	// Create a task and execution
	task := &models.Task{
		Title:     "Test task",
		Prompt:    "Test",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		ProjectID: project.ID,
	}
	require.NoError(t, svc.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agents[0].ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	require.NoError(t, svc.execRepo.Create(ctx, exec))

	// Create a temp file to act as an attachment
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test_doc.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("test content"), 0644))

	attachments := []models.ChatAttachment{
		{
			FileName:  "test_doc.txt",
			FilePath:  tmpFile,
			MediaType: "text/plain",
			FileSize:  12,
		},
	}

	svc.linkAttachmentsToExecution(ctx, exec.ID, attachments)

	// Verify attachment record was created in DB
	dbAttachments, err := svc.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
	assert.NoError(t, err)
	assert.Len(t, dbAttachments, 1)
	assert.Equal(t, "test_doc.txt", dbAttachments[0].FileName)
	assert.Equal(t, "text/plain", dbAttachments[0].MediaType)
	assert.Equal(t, exec.ID, dbAttachments[0].ExecutionID)
}

func TestTelegramService_LinkAttachments_UpdatesImagePaths(t *testing.T) {
	// This test verifies that after linkAttachmentsToExecution moves files from
	// the temp directory to uploads/chat/{execID}/, the imageAttachments paths
	// are updated to match. Without this fix, imageAttachments would still point
	// to the deleted temp directory, causing callAnthropicChat to silently skip
	// the image (os.ReadFile fails on nonexistent path).
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)

	task := &models.Task{
		Title:     "Test image task",
		Prompt:    "Analyze this image",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		ProjectID: project.ID,
	}
	require.NoError(t, svc.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agents[0].ID,
		Status:        models.ExecRunning,
		PromptSent:    "Analyze this image",
	}
	require.NoError(t, svc.execRepo.Create(ctx, exec))

	// Create a temp file simulating a downloaded Telegram photo
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "photo_12345.jpg")
	testImageData := []byte("fake-jpeg-data-for-testing")
	require.NoError(t, os.WriteFile(tmpFile, testImageData, 0644))

	absPath, err := filepath.Abs(tmpFile)
	require.NoError(t, err)

	// Simulate what downloadAndSaveTelegramAttachment returns:
	// imageAttachments and chatAttachments both point to the temp path
	imageAttachments := []models.Attachment{
		{
			FileName:  "photo_12345.jpg",
			FilePath:  absPath,
			MediaType: "image/jpeg",
			FileSize:  int64(len(testImageData)),
		},
	}
	chatAttachments := []models.ChatAttachment{
		{
			FileName:  "photo_12345.jpg",
			FilePath:  absPath,
			MediaType: "image/jpeg",
			FileSize:  int64(len(testImageData)),
		},
	}

	// linkAttachmentsToExecution moves files and updates chatAttachments paths
	svc.linkAttachmentsToExecution(ctx, exec.ID, chatAttachments)

	// chatAttachments should now have the new path
	assert.NotEqual(t, absPath, chatAttachments[0].FilePath, "chatAttachments path should be updated after link")
	assert.Contains(t, chatAttachments[0].FilePath, exec.ID, "chatAttachments path should contain exec ID")

	// Before the fix: imageAttachments still has the old temp path
	assert.Equal(t, absPath, imageAttachments[0].FilePath, "imageAttachments should still have old path before sync")

	// Apply the same path sync logic used in handleChatMessage
	for i := range imageAttachments {
		for _, ca := range chatAttachments {
			if ca.FileName == imageAttachments[i].FileName {
				imageAttachments[i].FilePath = ca.FilePath
				break
			}
		}
	}

	// After the fix: imageAttachments should have the new path
	assert.Equal(t, chatAttachments[0].FilePath, imageAttachments[0].FilePath,
		"imageAttachments path should match chatAttachments after sync")

	// Verify the file actually exists at the new path (critical for callAnthropicChat)
	data, readErr := os.ReadFile(imageAttachments[0].FilePath)
	assert.NoError(t, readErr, "image file should be readable at the updated path")
	assert.Equal(t, testImageData, data, "file content should match original")
}

func TestTelegramService_AutoSelectAgent_WithImages(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := &TelegramService{
		llmConfigRepo: llmConfigRepo,
		userProjects:  make(map[int64]string),
	}

	ctx := context.Background()

	// Should return a model even with hasImages=true (uses vision-aware selection)
	agent, err := svc.autoSelectAgent(ctx, "analyze this image", true)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
}

func TestTelegramService_ProcessViewThread(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	agentID := agents[0].ID

	// Create a task with execution history
	task := &models.Task{
		Title:     "Fix login bug",
		Prompt:    "Fix the login form crash",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecRunning,
		PromptSent:    "Fix the login form crash",
	}
	require.NoError(t, svc.execRepo.Create(ctx, exec))
	require.NoError(t, svc.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "Fixed the null pointer in login handler", "", 100, 1000))

	// Simulate LLM output with VIEW_TASK_CHAT marker
	output := fmt.Sprintf(`Let me retrieve the execution output for that task.

[VIEW_TASK_CHAT]
{"task_id": "%s"}
[/VIEW_TASK_CHAT]`, task.ID)

	result := svc.processViewThread(ctx, "chat-exec-123", project.ID, output)

	// Should contain the task's thread transcript
	assert.Contains(t, result, "Thread history for task")
	assert.Contains(t, result, "Fix login bug")
	assert.Contains(t, result, "Fixed the null pointer in login handler")
}

func TestTelegramService_ProcessViewThread_ByTitle(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	agentID := agents[0].ID

	task := &models.Task{
		Title:     "API endpoint refactor",
		Prompt:    "Refactor the API endpoints",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecRunning,
		PromptSent:    "Refactor the API endpoints",
	}
	require.NoError(t, svc.execRepo.Create(ctx, exec))
	require.NoError(t, svc.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "Refactored 5 endpoints to use middleware", "", 100, 1000))

	// Search by title instead of ID
	output := `Let me look up that task.

[VIEW_TASK_CHAT]
{"title": "API endpoint"}
[/VIEW_TASK_CHAT]`

	result := svc.processViewThread(ctx, "chat-exec-456", project.ID, output)

	assert.Contains(t, result, "API endpoint refactor")
	assert.Contains(t, result, "Refactored 5 endpoints to use middleware")
}

func TestTelegramService_ProcessViewThread_NotFound(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	output := `[VIEW_TASK_CHAT]
{"task_id": "nonexistent"}
[/VIEW_TASK_CHAT]`

	result := svc.processViewThread(ctx, "chat-exec-789", project.ID, output)
	assert.Contains(t, result, "Could not find task")
}

func TestTelegramService_ProcessChatMarkers_ViewThread(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	agentID := agents[0].ID

	// Create a running task with execution output
	task := &models.Task{
		Title:     "Deploy service",
		Prompt:    "Deploy the service to production",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecRunning,
		PromptSent:    "Deploy the service to production",
	}
	require.NoError(t, svc.execRepo.Create(ctx, exec))
	require.NoError(t, svc.execRepo.UpdateOutput(ctx, exec.ID, "Building Docker image... Running tests..."))

	// Create a chat execution to track the marker processing
	chatExec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecRunning,
		PromptSent:    "What's the status of the deploy task?",
	}
	require.NoError(t, svc.execRepo.Create(ctx, chatExec))

	// Full processChatMarkers call with VIEW_TASK_CHAT marker
	output := fmt.Sprintf(`Let me retrieve the execution output for the running task.

[VIEW_TASK_CHAT]
{"task_id": "%s"}
[/VIEW_TASK_CHAT]`, task.ID)

	result := svc.processChatMarkers(ctx, chatExec.ID, project.ID, output, 12345, 12345)

	// Should contain the task's execution history
	assert.Contains(t, result, "Deploy service")
	assert.Contains(t, result, "Building Docker image")
}

func TestTelegramService_ProcessSendToTask(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)

	// Create a completed task that will be reactivated
	task := &models.Task{
		Title:     "Fix login bug",
		Prompt:    "Fix the login form crash",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		ProjectID: project.ID,
		AgentID:   &agents[0].ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	output := fmt.Sprintf(`I'll send that instruction to the task.

[SEND_TO_TASK]
{"task_id": "%s", "message": "Also add error handling for empty passwords"}
[/SEND_TO_TASK]`, task.ID)

	// Note: processSendToTask spawns a goroutine that needs llmSvc,
	// but we can still test the marker parsing and execution creation.
	// We set llmSvc to nil so the goroutine will fail, but the execution record will exist.
	result := svc.processSendToTask(ctx, "chat-exec-send", project.ID, output)

	// Should contain confirmation message
	assert.Contains(t, result, "Sent message to task")
	assert.Contains(t, result, "Fix login bug")

	// Task should have been reactivated to running
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusRunning, updatedTask.Status)

	// A follow-up execution should have been created
	executions, err := svc.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	var foundFollowup bool
	for _, e := range executions {
		if e.IsFollowup && e.PromptSent == "Also add error handling for empty passwords" {
			foundFollowup = true
			break
		}
	}
	assert.True(t, foundFollowup, "follow-up execution should have been created")
}

func TestTelegramService_ResolveTaskReference_ByID(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		Title:     "My Task",
		Prompt:    "Do something",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	// Resolve by ID
	found, err := svc.resolveTaskReference(ctx, project.ID, task.ID, "")
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
	assert.Equal(t, "My Task", found.Title)
}

func TestTelegramService_ResolveTaskReference_ByTitle(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		Title:     "Fix authentication system",
		Prompt:    "Fix auth bugs",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		ProjectID: project.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	// Resolve by title search
	found, err := svc.resolveTaskReference(ctx, project.ID, "", "authentication")
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
}

func TestTelegramService_ResolveTaskReference_NotFound(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	// ID not found
	_, err := svc.resolveTaskReference(ctx, project.ID, "nonexistent", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Title not found
	_, err = svc.resolveTaskReference(ctx, project.ID, "", "zzz no match zzz")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no task found")

	// Neither provided
	_, err = svc.resolveTaskReference(ctx, project.ID, "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no task_id or title provided")
}

func TestTelegramService_FormatThreadTranscript(t *testing.T) {
	task := &models.Task{
		ID:       "task123",
		Title:    "Build API",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Prompt:   "Build the API endpoints",
		Priority: 2,
	}

	executions := []models.Execution{
		{
			ID:         "exec1",
			PromptSent: "Build the API endpoints",
			Output:     "Created 3 new API endpoints",
			Status:     models.ExecCompleted,
		},
		{
			ID:         "exec2",
			PromptSent: "Also add authentication",
			Output:     "Added JWT auth middleware",
			Status:     models.ExecCompleted,
			IsFollowup: true,
		},
	}

	transcript := formatThreadTranscript(task, executions, 0, 0)

	assert.Contains(t, transcript, "Build API")
	assert.Contains(t, transcript, "[TASK_ID:task123]")
	assert.Contains(t, transcript, "Created 3 new API endpoints")
	assert.Contains(t, transcript, "Also add authentication")
	assert.Contains(t, transcript, "Added JWT auth middleware")
}

func TestTelegramService_FormatThreadTranscript_Empty(t *testing.T) {
	task := &models.Task{
		ID:       "task456",
		Title:    "Empty Task",
		Status:   models.StatusPending,
		Category: models.CategoryBacklog,
	}

	transcript := formatThreadTranscript(task, []models.Execution{}, 0, 0)
	assert.Contains(t, transcript, "No execution history found")
}

func TestTelegramService_FormatThreadTranscript_Pagination(t *testing.T) {
	task := &models.Task{
		ID:       "task-page",
		Title:    "Paginated Task",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Priority: 1,
	}

	// Create executions with large output to exceed budget
	var executions []models.Execution
	largeOutput := strings.Repeat("X", 20*1024) // 20KB each
	for i := 0; i < 10; i++ {
		executions = append(executions, models.Execution{
			ID:         fmt.Sprintf("exec-%d", i),
			PromptSent: fmt.Sprintf("step %d", i),
			Output:     largeOutput,
			Status:     models.ExecCompleted,
			IsFollowup: i > 0,
		})
	}

	transcript := formatThreadTranscript(task, executions, 0, 0)
	assert.Contains(t, transcript, "Total executions: 10")
	assert.Contains(t, transcript, "Transcript size limit reached")
	assert.Contains(t, transcript, "offset")
}

func TestTelegramService_FormatThreadTranscript_OffsetLimit(t *testing.T) {
	task := &models.Task{
		ID:       "task-ol",
		Title:    "Offset Limit Task",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Priority: 1,
	}

	executions := []models.Execution{
		{ID: "e0", PromptSent: "msg0", Output: "out0", Status: models.ExecCompleted},
		{ID: "e1", PromptSent: "msg1", Output: "out1", Status: models.ExecCompleted, IsFollowup: true},
		{ID: "e2", PromptSent: "msg2", Output: "out2", Status: models.ExecCompleted, IsFollowup: true},
	}

	transcript := formatThreadTranscript(task, executions, 1, 1)
	assert.Contains(t, transcript, "msg1")
	assert.Contains(t, transcript, "out1")
	assert.NotContains(t, transcript, "msg0")
	assert.NotContains(t, transcript, "msg2")
	assert.Contains(t, transcript, "Showing executions 2–2 of 3")
}

func TestTelegramService_SelectDefaultAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := &TelegramService{
		llmConfigRepo: llmConfigRepo,
		userProjects:  make(map[int64]string),
	}

	ctx := context.Background()
	agent, err := svc.selectDefaultAgent(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
}

func TestTelegramService_BuildTelegramTaskChatContext(t *testing.T) {
	// With history
	ctx := buildTelegramTaskChatContext("My Task", true)
	assert.Contains(t, ctx, "continuing work")
	assert.Contains(t, ctx, "My Task")
	assert.Contains(t, ctx, "do NOT restart")

	// Without history
	ctx = buildTelegramTaskChatContext("My Task", false)
	assert.Contains(t, ctx, "starting work")
}

func TestTelegramService_CheckAuthorization(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	telegramAuthRepo := repository.NewTelegramAuthRepo(db)
	bgCtx := context.Background()

	// Create a project
	project := &models.Project{Name: "Auth Test"}
	require.NoError(t, projectRepo.Create(bgCtx, project))

	svc := &TelegramService{
		projectRepo:      projectRepo,
		telegramAuthRepo: telegramAuthRepo,
		userProjects:     make(map[int64]string),
	}

	t.Run("NoAuthRepo", func(t *testing.T) {
		// When telegramAuthRepo is nil, everyone is allowed
		svcNoAuth := &TelegramService{
			projectRepo:  projectRepo,
			userProjects: make(map[int64]string),
		}
		assert.True(t, svcNoAuth.checkAuthorization(999, "anyone", project.ID))
	})

	t.Run("NoUsersConfigured_DenyAll", func(t *testing.T) {
		// No authorized users configured → deny by default
		assert.False(t, svc.checkAuthorization(999, "anyone", project.ID))
	})

	t.Run("NoProjectSelected_DenyAll", func(t *testing.T) {
		// No project selected and no users configured → deny by default
		assert.False(t, svc.checkAuthorization(999, "anyone", ""))
	})

	t.Run("AuthorizedByUserID", func(t *testing.T) {
		u := &models.TelegramAuthorizedUser{
			ProjectID:      project.ID,
			TelegramUserID: 111,
			DisplayName:    "User 111",
			AddedBy:        "test",
		}
		require.NoError(t, telegramAuthRepo.Create(bgCtx, u))

		assert.True(t, svc.checkAuthorization(111, "", project.ID))
		assert.False(t, svc.checkAuthorization(999, "", project.ID))
	})

	t.Run("AuthorizedByUsername", func(t *testing.T) {
		u := &models.TelegramAuthorizedUser{
			ProjectID:        project.ID,
			TelegramUserID:   0,
			TelegramUsername: "bob",
			DisplayName:      "@bob",
			AddedBy:          "test",
		}
		require.NoError(t, telegramAuthRepo.Create(bgCtx, u))

		// User with matching username is authorized
		assert.True(t, svc.checkAuthorization(222, "bob", project.ID))

		// User with non-matching username is not authorized
		assert.False(t, svc.checkAuthorization(333, "eve", project.ID))
	})

	t.Run("NoProjectSelected_AuthorizedAnywhere", func(t *testing.T) {
		// User 111 was added to the project above — should be authorized even without project selected
		assert.True(t, svc.checkAuthorization(111, "", ""))
	})

	t.Run("BackfillOnUsernameAuth", func(t *testing.T) {
		// After username-based auth, user ID should be backfilled
		got, err := telegramAuthRepo.IsAuthorized(bgCtx, project.ID, 222, "")
		require.NoError(t, err)
		// Should now be authorized by user ID alone (after backfill from previous test)
		assert.True(t, got)
	})

	t.Run("AuthorizedInDifferentProject_FallbackAllows", func(t *testing.T) {
		// User is authorized in "Auth Test" project but checking against a different project.
		// The fallback to IsAuthorizedAnywhere should allow them.
		otherProject := &models.Project{Name: "Other Project"}
		require.NoError(t, projectRepo.Create(bgCtx, otherProject))

		// User 111 is authorized in the first project — should pass via fallback
		assert.True(t, svc.checkAuthorization(111, "", otherProject.ID))
		// User 222 (bob, backfilled) is authorized in the first project — should pass via fallback
		assert.True(t, svc.checkAuthorization(222, "bob", otherProject.ID))
		// Unknown user should still be denied
		assert.False(t, svc.checkAuthorization(999, "unknown", otherProject.ID))
	})

	t.Run("NilAuthRepoBypassesAllChecks", func(t *testing.T) {
		// This is the root cause of the bug: when TelegramService is created on-the-fly
		// via the settings handler without calling SetTelegramAuthRepo, the auth repo
		// is nil and checkAuthorization always returns true (allows everyone).
		svcNoAuth := &TelegramService{
			projectRepo:  projectRepo,
			userProjects: make(map[int64]string),
			// telegramAuthRepo is intentionally nil — simulating on-the-fly creation
		}

		// Even though authorized users exist for this project, nil repo lets everyone through
		assert.True(t, svcNoAuth.checkAuthorization(999, "hacker", project.ID),
			"nil auth repo causes full bypass — this was the bug")

		// With the repo properly set, unauthorized user should be blocked
		svcWithAuth := &TelegramService{
			projectRepo:      projectRepo,
			telegramAuthRepo: telegramAuthRepo,
			userProjects:     make(map[int64]string),
		}
		assert.False(t, svcWithAuth.checkAuthorization(999, "hacker", project.ID),
			"with auth repo set, unauthorized user must be blocked")
	})
}

// --- App Settings Marker Parity Tests ---
// These tests verify that Telegram's processChatMarkers handles the same
// app settings markers as the /chat page's processChatResponse.

func TestTelegramService_ProcessChatMarkers_ListPersonalities(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := "Let me show you the personalities.\n\n[LIST_PERSONALITIES]"
	result := svc.processChatMarkers(ctx, "exec1", "proj1", output, 12345, 12345)

	assert.Contains(t, result, "Available Personalities")
	assert.Contains(t, result, "Pirate Captain")
	assert.Contains(t, result, "Current personality:")
}

func TestTelegramService_ProcessChatMarkers_ListPersonalities_NoMarker(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := "No marker here."
	result := svc.processListPersonalities(ctx, "exec1", "proj1", output)
	assert.Equal(t, output, result)
}

func TestTelegramService_ProcessChatMarkers_SetPersonality(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := `I'll change the personality for you.

[SET_PERSONALITY]
{"personality": "pirate_captain"}
[/SET_PERSONALITY]`

	result := svc.processChatMarkers(ctx, "exec1", "proj1", output, 12345, 12345)

	assert.Contains(t, result, "Personality Settings")
	assert.Contains(t, result, "Pirate Captain")
	assert.Contains(t, result, "pirate_captain")

	// Verify setting was stored
	val, err := svc.settingsRepo.Get(ctx, "personality")
	require.NoError(t, err)
	assert.Equal(t, "pirate_captain", val)
}

func TestTelegramService_ProcessChatMarkers_SetPersonality_Invalid(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := `[SET_PERSONALITY]
{"personality": "nonexistent_personality"}
[/SET_PERSONALITY]`

	result := svc.processSetPersonality(ctx, "exec1", "proj1", output)
	assert.Contains(t, result, "Unknown personality")
}

func TestTelegramService_ProcessChatMarkers_ListModels(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	// Create a model config
	agent := &models.LLMConfig{
		Name:     "Test Model",
		Provider: "anthropic",
		Model:    "claude-3-5-sonnet-20241022",
	}
	require.NoError(t, svc.llmConfigRepo.Create(ctx, agent))

	output := "Let me show you the models.\n\n[LIST_MODELS]"
	result := svc.processChatMarkers(ctx, "exec1", "proj1", output, 12345, 12345)

	assert.Contains(t, result, "Configured Models")
	assert.Contains(t, result, "Test Model")
	assert.Contains(t, result, "anthropic")
}

func TestTelegramService_ProcessChatMarkers_ListModels_NoMarker(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := "No marker here."
	result := svc.processListModels(ctx, "exec1", "proj1", output)
	assert.Equal(t, output, result)
}

func TestTelegramService_ProcessChatMarkers_ViewSettings(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	// Set up personality
	require.NoError(t, svc.settingsRepo.Set(ctx, "personality", "zen_debugger"))

	// Create a model
	agent := &models.LLMConfig{
		Name:      "Sonnet",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	require.NoError(t, svc.llmConfigRepo.Create(ctx, agent))

	output := "Here are the settings.\n\n[VIEW_SETTINGS]"
	result := svc.processChatMarkers(ctx, "exec1", "proj1", output, 12345, 12345)

	assert.Contains(t, result, "App Settings")
	assert.Contains(t, result, "zen_debugger")
	assert.Contains(t, result, "Configured models")
	assert.Contains(t, result, "Sonnet")
	assert.Contains(t, result, "Per-project worker limits")
	assert.Contains(t, result, "Per-model worker pools")
}

func TestTelegramService_ProcessChatMarkers_ViewSettings_NoMarker(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := "No marker here."
	result := svc.processViewSettings(ctx, "exec1", "proj1", output)
	assert.Equal(t, output, result)
}

func TestTelegramService_ProcessChatMarkers_ProjectInfo(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "My Project", Description: "A test project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Create some tasks
	for _, td := range []struct {
		title    string
		category models.TaskCategory
	}{
		{"Active Task One", models.CategoryActive},
		{"Active Task Two", models.CategoryActive},
		{"Backlog Task One", models.CategoryBacklog},
	} {
		task := &models.Task{
			ProjectID: project.ID,
			Title:     td.title,
			Prompt:    "test",
			Category:  td.category,
			Status:    models.StatusPending,
			Priority:  2,
		}
		require.NoError(t, svc.taskSvc.Create(ctx, task))
	}

	output := "Let me get the project details.\n\n[PROJECT_INFO]"
	result := svc.processChatMarkers(ctx, "exec1", project.ID, output, 12345, 12345)

	assert.Contains(t, result, "Project Info")
	assert.Contains(t, result, "My Project")
	assert.Contains(t, result, "A test project")
	assert.Contains(t, result, "Total tasks")
}

func TestTelegramService_ProcessChatMarkers_ProjectInfo_NotFound(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := "[PROJECT_INFO]"
	result := svc.processProjectInfo(ctx, "exec1", "nonexistent", output)

	assert.Contains(t, result, "Error retrieving project details")
}

func TestTelegramService_ProcessChatMarkers_ProjectInfo_NoMarker(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := "No marker here."
	result := svc.processProjectInfo(ctx, "exec1", "proj1", output)
	assert.Equal(t, output, result)
}

func TestTelegramService_ProcessChatMarkers_ViewSettings_WithWorkerConfig(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	// Create a project with per-project worker limit
	maxW := 3
	project := &models.Project{Name: "Limited Project", MaxWorkers: &maxW}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Create a model with per-model worker pool
	agent := &models.LLMConfig{
		Name:          "Opus",
		Provider:      "anthropic",
		Model:         "claude-opus-4-20250514",
		IsDefault:     true,
		MaxWorkers:    2,
		WorkerTimeout: 300,
	}
	require.NoError(t, svc.llmConfigRepo.Create(ctx, agent))

	output := "Settings:\n[VIEW_SETTINGS]"
	result := svc.processViewSettings(ctx, "exec1", project.ID, output)

	assert.Contains(t, result, "Limited Project: 3")
	assert.Contains(t, result, "Opus: max_workers=2, timeout=300s")
}

func TestTelegramService_HandleProject_PersistsSelection(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{
		Name:      "Project A",
		RepoPath:  "/tmp/a",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project1))

	project2 := &models.Project{
		Name:     "Project B",
		RepoPath: "/tmp/b",
	}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}

	userID := int64(456)
	svc.userProjects[userID] = project1.ID

	// Switch to Project B
	response := svc.handleProject(userID, "Project B")
	assert.Contains(t, response, "Switched to project: *Project B*")

	// Verify it was persisted to DB
	savedProjectID, err := userProjectRepo.GetUserProject(ctx, "456")
	require.NoError(t, err)
	assert.Equal(t, project2.ID, savedProjectID)
}

func TestTelegramService_GetActiveProject_LoadsFromDB(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	ctx := context.Background()
	defaultProject := &models.Project{
		Name:      "Default Project",
		RepoPath:  "/tmp/default",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, defaultProject))

	otherProject := &models.Project{
		Name:     "Other Project",
		RepoPath: "/tmp/other",
	}
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	// Pre-save user's project choice in DB (simulating a previous session)
	require.NoError(t, userProjectRepo.SetUserProject(ctx, "12345", otherProject.ID))

	// Create service with empty in-memory cache (simulating restart)
	svc := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}

	// Should load from DB, not default to Default Project
	projectID := svc.getActiveProject(12345)
	assert.Equal(t, otherProject.ID, projectID)

	// Should now be cached in memory
	assert.Equal(t, otherProject.ID, svc.userProjects[12345])
}

func TestTelegramService_GetActiveProject_FallsBackToDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	// No saved preference — new user
	svc := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}

	// Should fall back to the seeded default project (migration 003 seeds id="default")
	projectID := svc.getActiveProject(99999)
	assert.NotEmpty(t, projectID, "should return some project ID")
	assert.Equal(t, "default", projectID, "should return the seeded default project")
}

func TestTelegramService_HandleStart_PersistsDefaultProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	svc := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}

	userID := int64(42)
	response := svc.handleStart(userID)

	assert.Contains(t, response, "Welcome to *OpenVibely*")
	assert.NotEmpty(t, svc.userProjects[userID])

	// Verify it was persisted
	ctx := context.Background()
	savedProjectID, err := userProjectRepo.GetUserProject(ctx, "42")
	require.NoError(t, err)
	assert.Equal(t, svc.userProjects[userID], savedProjectID)
}

func TestTelegramService_ProjectPersistence_AcrossRestarts(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{
		Name:      "Default",
		RepoPath:  "/tmp/default",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project1))

	project2 := &models.Project{
		Name:     "Custom",
		RepoPath: "/tmp/custom",
	}
	require.NoError(t, projectRepo.Create(ctx, project2))

	userID := int64(555)

	// Simulate first session: user switches to project2
	svc1 := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}
	svc1.userProjects[userID] = project1.ID
	response := svc1.handleProject(userID, "Custom")
	assert.Contains(t, response, "Switched to project: *Custom*")

	// Simulate restart: new service instance with empty in-memory cache
	svc2 := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}

	// Should restore project2 from DB
	projectID := svc2.getActiveProject(userID)
	assert.Equal(t, project2.ID, projectID)
}

// TestTelegramService_IsSendResponsesEnabled tests the send-responses setting logic.
func TestTelegramService_IsSendResponsesEnabled(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	svc := &TelegramService{
		settingsRepo: settingsRepo,
		userProjects: make(map[int64]string),
	}

	// Default (no setting): enabled
	assert.True(t, svc.IsSendResponsesEnabled(ctx))

	// Explicitly enabled
	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))
	assert.True(t, svc.IsSendResponsesEnabled(ctx))

	// Explicitly disabled
	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "false"))
	assert.False(t, svc.IsSendResponsesEnabled(ctx))

	// Re-enabled
	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))
	assert.True(t, svc.IsSendResponsesEnabled(ctx))
}

// TestTelegramService_IsSendResponsesEnabled_NilSettingsRepo tests default when no repo.
func TestTelegramService_IsSendResponsesEnabled_NilSettingsRepo(t *testing.T) {
	svc := &TelegramService{
		userProjects: make(map[int64]string),
	}
	assert.True(t, svc.IsSendResponsesEnabled(context.Background()))
}

// TestTelegramService_SendTaskCompletionNotification_SkipsWebTasks verifies that
// tasks created via web never get Telegram notifications (regardless of setting).
func TestTelegramService_SendTaskCompletionNotification_SkipsWebTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	// Enable send-responses
	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))

	svc := &TelegramService{
		settingsRepo: settingsRepo,
		userProjects: make(map[int64]string),
		// bot is nil — if it tries to send, it will panic; that's our canary
	}

	// A task created via web should never trigger notification
	webTask := models.Task{
		ID:             "web-task-1",
		Title:          "Web Task",
		CreatedVia:     models.TaskOriginWeb,
		TelegramChatID: 0,
		Category:       models.CategoryActive,
	}
	// Should not panic (won't try to send because CreatedVia != "telegram")
	svc.SendTaskCompletionNotification(ctx, webTask, "some output", "")
}

// TestTelegramService_SendTaskCompletionNotification_SkipsWhenDisabled verifies
// that no notification is sent when the setting is disabled, even for Telegram tasks.
func TestTelegramService_SendTaskCompletionNotification_SkipsWhenDisabled(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	// Disable send-responses
	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "false"))

	svc := &TelegramService{
		settingsRepo: settingsRepo,
		userProjects: make(map[int64]string),
		// bot is nil — if it tries to send, it will panic; that's our canary
	}

	// A task created via Telegram but with setting disabled
	telegramTask := models.Task{
		ID:             "tg-task-1",
		Title:          "Telegram Task",
		CreatedVia:     models.TaskOriginTelegram,
		TelegramChatID: 12345,
		Category:       models.CategoryActive,
	}
	// Should not panic (won't try to send because setting is disabled)
	svc.SendTaskCompletionNotification(ctx, telegramTask, "some output", "")
}

// TestTelegramService_SendTaskCompletionNotification_SkipsChatTasks verifies
// that chat-category tasks don't trigger notifications (they already get a direct response).
func TestTelegramService_SendTaskCompletionNotification_SkipsChatTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	// Enable send-responses
	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))

	svc := &TelegramService{
		settingsRepo: settingsRepo,
		userProjects: make(map[int64]string),
		// bot is nil — if it tries to send, it will panic
	}

	// Chat tasks created via Telegram should not trigger notifications
	chatTask := models.Task{
		ID:             "chat-task-1",
		Title:          "Telegram Chat",
		CreatedVia:     models.TaskOriginTelegram,
		TelegramChatID: 12345,
		Category:       models.CategoryChat,
	}
	// Should not panic (won't try to send because it's a chat task)
	svc.SendTaskCompletionNotification(ctx, chatTask, "some output", "")
}

// TestTelegramService_SendTaskCompletionNotification_SkipsZeroChatID verifies
// that tasks with no chat ID don't trigger notifications.
func TestTelegramService_SendTaskCompletionNotification_SkipsZeroChatID(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))

	svc := &TelegramService{
		settingsRepo: settingsRepo,
		userProjects: make(map[int64]string),
		// bot is nil — if it tries to send, it will panic
	}

	task := models.Task{
		ID:             "task-1",
		Title:          "Task with zero chat ID",
		CreatedVia:     models.TaskOriginTelegram,
		TelegramChatID: 0, // zero = no chat ID
		Category:       models.CategoryActive,
	}
	svc.SendTaskCompletionNotification(ctx, task, "output", "")
}

func TestTelegramService_SendTaskCompletionNotification_HydratesFromDB_AndSends(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))

	project := &models.Project{Name: "Hydrate Notification", RepoPath: "/tmp/hydrate-notification", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	persistedTask := &models.Task{
		ProjectID:      project.ID,
		Title:          "Persisted Telegram Task",
		Prompt:         "Do work",
		Category:       models.CategoryActive,
		Priority:       1,
		Status:         models.StatusPending,
		CreatedVia:     models.TaskOriginTelegram,
		TelegramChatID: 77889911,
	}
	require.NoError(t, taskRepo.Create(ctx, persistedTask))

	sentCount := 0
	var sentChatID int64
	var sentText string

	svc := &TelegramService{
		taskRepo:     taskRepo,
		settingsRepo: settingsRepo,
		sendMessageFunc: func(chatID int64, text string) {
			sentCount++
			sentChatID = chatID
			sentText = text
		},
		userProjects: make(map[int64]string),
	}

	staleInMemoryTask := models.Task{
		ID:             persistedTask.ID,
		Title:          "Stale Task",
		CreatedVia:     models.TaskOriginWeb,
		TelegramChatID: 0,
		Category:       models.CategoryBacklog,
	}

	svc.SendTaskCompletionNotification(ctx, staleInMemoryTask, "completed output", "")

	assert.Equal(t, 1, sentCount)
	assert.Equal(t, persistedTask.TelegramChatID, sentChatID)
	assert.Contains(t, sentText, "Persisted Telegram Task")
}

func TestTelegramService_SendTaskCompletionNotification_HydrationTaskNotFound_Skips(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))

	sentCount := 0
	svc := &TelegramService{
		taskRepo:     taskRepo,
		settingsRepo: settingsRepo,
		sendMessageFunc: func(chatID int64, text string) {
			sentCount++
		},
		userProjects: make(map[int64]string),
	}

	staleTask := models.Task{
		ID:             "missing-task-id",
		Title:          "Missing",
		CreatedVia:     models.TaskOriginWeb,
		TelegramChatID: 0,
		Category:       models.CategoryActive,
	}

	svc.SendTaskCompletionNotification(ctx, staleTask, "output", "")

	assert.Equal(t, 0, sentCount)
}

func TestTelegramService_SendTaskCompletionNotification_HydrationDBUnavailable_Skips(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))

	sentCount := 0
	svc := &TelegramService{
		settingsRepo: settingsRepo,
		sendMessageFunc: func(chatID int64, text string) {
			sentCount++
		},
		userProjects: make(map[int64]string),
	}

	staleTask := models.Task{
		ID:             "stale-task-id",
		Title:          "Stale",
		CreatedVia:     models.TaskOriginWeb,
		TelegramChatID: 0,
		Category:       models.CategoryActive,
	}

	svc.SendTaskCompletionNotification(ctx, staleTask, "output", "")

	assert.Equal(t, 0, sentCount)
}

// TestTelegramService_TaskOriginPropagation verifies that tasks created via the
// processChatMarkers flow get their Telegram origin set correctly.
func TestTelegramService_TaskOriginPropagation(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{Name: "Origin Test", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	chatID := int64(99887766)

	// Simulate processChatMarkers with a CREATE_TASK marker
	output := `Here's your task:
[CREATE_TASK]
{"title": "Fix origin tracking", "prompt": "Fix the origin tracking bug", "category": "backlog", "priority": 3}
[/CREATE_TASK]`

	_ = svc.processChatMarkers(ctx, "exec1", project.ID, output, chatID, 12345)

	// Verify the created task has the correct origin
	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	assert.Equal(t, models.TaskOriginTelegram, tasks[0].CreatedVia)
	assert.Equal(t, chatID, tasks[0].TelegramChatID)
	assert.Equal(t, "Fix origin tracking", tasks[0].Title)
}

// TestTelegramService_WebCreatedTasksNeverGetNotifications is an end-to-end test verifying
// that tasks created via the web UI never trigger Telegram notifications, regardless of settings.
func TestTelegramService_WebCreatedTasksNeverGetNotifications(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	// Enable send-responses
	require.NoError(t, settingsRepo.Set(ctx, "telegram_send_responses", "true"))

	svc := &TelegramService{
		settingsRepo: settingsRepo,
		userProjects: make(map[int64]string),
	}

	// Simulate a web-created task with various states
	for _, origin := range []string{models.TaskOriginWeb, ""} {
		for _, category := range []models.TaskCategory{models.CategoryActive, models.CategoryBacklog, models.CategoryScheduled} {
			task := models.Task{
				ID:             fmt.Sprintf("web-%s-%s", origin, category),
				Title:          "Web Task",
				CreatedVia:     origin,
				TelegramChatID: 0,
				Category:       category,
			}
			// Should not try to send (bot is nil, would panic if it did)
			svc.SendTaskCompletionNotification(ctx, task, "output", "")
			svc.SendTaskCompletionNotification(ctx, task, "", "error msg")
		}
	}
}

// TestTelegramService_TaskOriginInDB verifies that task origin fields persist correctly
// through create and read cycles.
func TestTelegramService_TaskOriginInDB(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	project := &models.Project{Name: "DB Test", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	// Create a task with Telegram origin
	task := &models.Task{
		ProjectID:      project.ID,
		Title:          "Telegram Task",
		Prompt:         "Test prompt",
		Status:         models.StatusPending,
		Category:       models.CategoryBacklog,
		CreatedVia:     models.TaskOriginTelegram,
		TelegramChatID: 12345678,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	// Read it back
	loaded, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TaskOriginTelegram, loaded.CreatedVia)
	assert.Equal(t, int64(12345678), loaded.TelegramChatID)

	// Create a web task (default origin)
	webTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Web Task",
		Prompt:    "Web prompt",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
	}
	require.NoError(t, taskRepo.Create(ctx, webTask))

	loadedWeb, err := taskRepo.GetByID(ctx, webTask.ID)
	require.NoError(t, err)
	assert.Equal(t, "", loadedWeb.CreatedVia) // default empty string
	assert.Equal(t, int64(0), loadedWeb.TelegramChatID)

	// Test UpdateTelegramOrigin
	require.NoError(t, taskRepo.UpdateTelegramOrigin(ctx, webTask.ID, 99998888))
	loadedUpdated, err := taskRepo.GetByID(ctx, webTask.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TaskOriginTelegram, loadedUpdated.CreatedVia)
	assert.Equal(t, int64(99998888), loadedUpdated.TelegramChatID)
}

// --- Project Listing and Switching Tests ---

func TestTelegramService_IsProjectListRequest(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"list projects", true},
		{"List Projects", true},
		{"show projects", true},
		{"show all projects", true},
		{"show my projects", true},
		{"my projects", true},
		{"available projects", true},
		{"what projects do I have", true},
		{"which projects are there", true},
		{"list all projects please", true},
		{"create a task", false},
		{"hello", false},
		{"project info", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isProjectListRequest(strings.ToLower(strings.TrimSpace(tt.input)))
			assert.Equal(t, tt.want, got, "isProjectListRequest(%q)", tt.input)
		})
	}
}

func TestTelegramService_ExtractProjectSwitchTarget(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"switch to project Alpha", "alpha"},
		{"switch project to Beta", "beta"},
		{"change to project Gamma", "gamma"},
		{"change project to Delta", "delta"},
		{"use project Epsilon", "epsilon"},
		{"set project to Zeta", "zeta"},
		{"select project Eta", "eta"},
		{"switch project My Cool Project", "my cool project"},
		{"Switch To Project Alpha", "alpha"},
		{"create a task", ""},
		{"list projects", ""},
		{"hello", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractProjectSwitchTarget(strings.ToLower(strings.TrimSpace(tt.input)))
			assert.Equal(t, tt.want, got, "extractProjectSwitchTarget(%q)", tt.input)
		})
	}
}

func TestTelegramService_HandleNaturalLanguageProjectCommand_List(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{Name: "Project Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Project Beta", RepoPath: "/tmp/beta"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}
	svc.userProjects[42] = project1.ID

	response, handled := svc.handleNaturalLanguageProjectCommand(42, "list projects")
	assert.True(t, handled, "should handle 'list projects'")
	assert.Contains(t, response, "Project Alpha")
	assert.Contains(t, response, "Project Beta")
	assert.Contains(t, response, "← _current_")
}

func TestTelegramService_HandleNaturalLanguageProjectCommand_Switch(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{Name: "Project Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Project Beta", RepoPath: "/tmp/beta"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}
	svc.userProjects[42] = project1.ID

	response, handled := svc.handleNaturalLanguageProjectCommand(42, "switch to project Project Beta")
	assert.True(t, handled, "should handle 'switch to project X'")
	assert.Contains(t, response, "Switched to project: *Project Beta*")
	assert.Equal(t, project2.ID, svc.userProjects[42], "should have updated active project")
}

func TestTelegramService_HandleNaturalLanguageProjectCommand_NotHandled(t *testing.T) {
	svc := &TelegramService{
		userProjects: make(map[int64]string),
	}

	response, handled := svc.handleNaturalLanguageProjectCommand(42, "create a task to fix login")
	assert.False(t, handled)
	assert.Equal(t, "", response)
}

func TestTelegramService_ProcessListProjects_Marker(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Beta", RepoPath: "/tmp/beta", Description: "Second project"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	output := "Here are your projects.\n\n[LIST_PROJECTS]"
	result := svc.processListProjects(ctx, "exec1", project1.ID, output)

	assert.Contains(t, result, "Available Projects")
	assert.Contains(t, result, "Alpha")
	assert.Contains(t, result, "Beta")
	assert.Contains(t, result, "Second project")
	assert.Contains(t, result, "← _current_")
}

func TestTelegramService_ProcessListProjects_NoMarker(t *testing.T) {
	svc, _, _ := newTestTelegramService(t)
	ctx := context.Background()

	output := "No marker here."
	result := svc.processListProjects(ctx, "exec1", "proj1", output)
	assert.Equal(t, output, result)
}

func TestTelegramService_ProcessSwitchProject_Marker(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Beta", RepoPath: "/tmp/beta"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}
	svc.userProjects[42] = project1.ID

	output := "I'll switch your project.\n\n[SWITCH_PROJECT]\n{\"project\": \"Beta\"}\n[/SWITCH_PROJECT]"
	result := svc.processSwitchProject(ctx, "exec1", project1.ID, output, 42)

	assert.Contains(t, result, "Switched to project: **Beta**")
	assert.Equal(t, project2.ID, svc.userProjects[42])

	// Verify persisted
	savedID, err := userProjectRepo.GetUserProject(ctx, "42")
	require.NoError(t, err)
	assert.Equal(t, project2.ID, savedID)
}

func TestTelegramService_ProcessSwitchProject_InvalidProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))

	svc := &TelegramService{
		projectRepo:  projectRepo,
		userProjects: make(map[int64]string),
	}
	svc.userProjects[42] = project1.ID

	output := "[SWITCH_PROJECT]\n{\"project\": \"NonExistent\"}\n[/SWITCH_PROJECT]"
	result := svc.processSwitchProject(ctx, "exec1", project1.ID, output, 42)

	assert.Contains(t, result, "Project not found")
	assert.Equal(t, project1.ID, svc.userProjects[42], "should remain on original project")
}

func TestTelegramService_ProcessChatMarkers_ListProjects(t *testing.T) {
	svc, projectRepo, _ := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{Name: "Main Project", RepoPath: "/tmp/main", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	output := "Let me show your projects.\n\n[LIST_PROJECTS]"
	result := svc.processChatMarkers(ctx, "exec1", project.ID, output, 12345, 12345)

	assert.Contains(t, result, "Available Projects")
	assert.Contains(t, result, "Main Project")
}

func TestTelegramService_ProcessChatMarkers_SwitchProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{Name: "First", RepoPath: "/tmp/first", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Second", RepoPath: "/tmp/second"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
		// Need repos for processChatMarkers which calls other marker processors
		llmConfigRepo: repository.NewLLMConfigRepo(db),
		taskRepo:      repository.NewTaskRepo(db, nil),
		execRepo:      repository.NewExecutionRepo(db),
		taskSvc:       NewTaskService(repository.NewTaskRepo(db, nil), repository.NewAttachmentRepo(db), NewWorkerService(nil, 0, projectRepo)),
		scheduleRepo:  repository.NewScheduleRepo(db),
		settingsRepo:  repository.NewSettingsRepo(db),
	}
	svc.userProjects[42] = project1.ID

	output := "[SWITCH_PROJECT]\n{\"project\": \"Second\"}\n[/SWITCH_PROJECT]"
	result := svc.processChatMarkers(ctx, "exec1", project1.ID, output, 12345, 42)

	assert.Contains(t, result, "Switched to project: **Second**")
	assert.Equal(t, project2.ID, svc.userProjects[42])
}

func TestTelegramService_SwitchProjectThenFollowupUsesNewProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)

	ctx := context.Background()
	project1 := &models.Project{Name: "Project One", RepoPath: "/tmp/one", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Project Two", RepoPath: "/tmp/two"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	svc := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}

	userID := int64(100)

	// Initially on project 1
	svc.userProjects[userID] = project1.ID
	assert.Equal(t, project1.ID, svc.getActiveProject(userID))

	// Switch via natural language
	response, handled := svc.handleNaturalLanguageProjectCommand(userID, "switch to project Project Two")
	assert.True(t, handled)
	assert.Contains(t, response, "Switched to project: *Project Two*")

	// Follow-up should use new project context
	assert.Equal(t, project2.ID, svc.getActiveProject(userID))

	// Verify persistence across restart
	svc2 := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
	}
	assert.Equal(t, project2.ID, svc2.getActiveProject(userID))
}

func TestTelegramService_HasListProjects(t *testing.T) {
	assert.True(t, HasListProjects("Show projects.\n\n[LIST_PROJECTS]"))
	assert.False(t, HasListProjects("No marker here."))
}

func TestTelegramService_ParseSwitchProject(t *testing.T) {
	output := `Switching now.

[SWITCH_PROJECT]
{"project": "My Project"}
[/SWITCH_PROJECT]`

	requests := ParseSwitchProject(output)
	require.Len(t, requests, 1)
	assert.Equal(t, "My Project", requests[0].Project)
}

func TestTelegramService_ParseSwitchProject_Empty(t *testing.T) {
	requests := ParseSwitchProject("No marker here.")
	assert.Nil(t, requests)
}

func TestTelegramService_ParseSwitchProject_EmptyProjectName(t *testing.T) {
	output := `[SWITCH_PROJECT]
{"project": ""}
[/SWITCH_PROJECT]`
	requests := ParseSwitchProject(output)
	assert.Empty(t, requests)
}
