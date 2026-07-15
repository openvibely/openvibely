package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramService_NotificationLifecycleRuntimeUsesPersistedChannelTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Telegram Notification Lifecycle"}
	require.NoError(t, projectRepo.Create(ctx, project))
	caller := &models.Task{ProjectID: project.ID, Title: "Telegram chat", Prompt: "process", Category: models.CategoryChat, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	alert, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, Type: "suggestion", Title: "Telegram suggestion", Severity: models.SeverityInfo})
	require.NoError(t, err)
	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved))

	svc := &TelegramService{taskRepo: taskRepo, alertSvc: alertSvc}
	rt := svc.buildTelegramActionToolRuntimeForTask(project.ID, caller.ID, 12345, 67890, nil)
	output, handled, isErr, err := rt.Executor(ctx, "claim_alert", json.RawMessage(`{"alert_id":"`+alert.ID+`"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, caller.ID)
}

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

func TestTelegramServiceStartIsIdempotent(t *testing.T) {
	client := &telegramStubClient{blockUpdates: make(chan struct{})}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, client)
	require.NoError(t, err)

	svc := &TelegramService{bot: bot}
	t.Cleanup(func() {
		client.unblock()
		svc.Stop()
	})

	svc.Start()
	svc.Start()

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&client.activeUpdates) == 1
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&client.maxActiveUpdates))
	assert.True(t, svc.IsRunning())
}

func TestTelegramServiceWaitsForStoppedPollerBeforeRestart(t *testing.T) {
	client := &telegramStubClient{blockUpdates: make(chan struct{})}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, client)
	require.NoError(t, err)

	svc := &TelegramService{bot: bot}
	defer func() {
		client.unblock()
		svc.lifecycleOpMu.Lock()
		require.True(t, svc.stopLocked(true))
		svc.lifecycleOpMu.Unlock()
	}()

	svc.Start()
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&client.activeUpdates) == 1
	}, time.Second, 10*time.Millisecond)

	svc.Stop()
	assert.False(t, svc.IsRunning())

	svc.Start()
	assert.Equal(t, int32(1), atomic.LoadInt32(&client.maxActiveUpdates), "must not start a second poller while the stopped poller is still draining")

	stopped := make(chan bool, 1)
	go func() {
		svc.lifecycleOpMu.Lock()
		stopped <- svc.stopLocked(true)
		svc.lifecycleOpMu.Unlock()
	}()

	select {
	case <-stopped:
		t.Fatal("blocking stop returned before the old long-poll request drained")
	case <-time.After(50 * time.Millisecond):
	}

	client.unblock()
	assert.True(t, <-stopped)
}

func TestTelegramServiceConflictClassifierMatchesTypedTelegramError(t *testing.T) {
	err := &tgbotapi.Error{Code: http.StatusConflict, Message: "Conflict: webhook is already configured"}
	assert.True(t, isTelegramConflictError(err), "409 conflicts must be classified by typed error code, not only by one getUpdates message substring")
}

func TestTelegramServiceGetUpdatesConflictDoesNotUseLibraryRetryLoop(t *testing.T) {
	client := &telegramStubClient{
		blockUpdates:       make(chan struct{}),
		getUpdatesResponse: `{"ok":false,"error_code":409,"description":"Conflict: webhook is already configured"}`,
	}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, client)
	require.NoError(t, err)

	svc := &TelegramService{bot: bot}
	t.Cleanup(func() {
		client.unblock()
		svc.lifecycleOpMu.Lock()
		require.True(t, svc.stopLocked(true))
		svc.lifecycleOpMu.Unlock()
	})

	svc.Start()
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&client.activeUpdates) == 1
	}, time.Second, 10*time.Millisecond)
	client.releaseOne()
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&client.activeUpdates) == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&client.maxActiveUpdates), "conflict retries must not create overlapping long-poll requests")
	assert.True(t, svc.IsRunning())
}

func TestTelegramServiceConcurrentUpdateTokenIsSerialized(t *testing.T) {
	var globalActiveUpdates int32
	var globalMaxActiveUpdates int32
	started := make(chan string, 10)
	clientsMu := sync.Mutex{}
	clients := make(map[string]*telegramStubClient)

	newBot := func(token string) (*tgbotapi.BotAPI, error) {
		client := &telegramStubClient{
			blockUpdates:           make(chan struct{}),
			releaseUpdates:         make(chan struct{}),
			globalActiveUpdates:    &globalActiveUpdates,
			globalMaxActiveUpdates: &globalMaxActiveUpdates,
			updatesStarted:         started,
		}
		clientsMu.Lock()
		clients[token] = client
		clientsMu.Unlock()
		return tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, client)
	}

	initialBot, err := newBot("initial-token")
	require.NoError(t, err)
	svc := &TelegramService{bot: initialBot, newBotAPI: newBot}
	defer func() {
		clientsMu.Lock()
		for _, client := range clients {
			client.unblock()
		}
		clientsMu.Unlock()
		svc.lifecycleOpMu.Lock()
		require.True(t, svc.stopLocked(true))
		svc.lifecycleOpMu.Unlock()
	}()

	svc.Start()
	waitForTelegramPoller(t, started, "initial-token")

	releaseClient := func(token string) {
		t.Helper()
		clientsMu.Lock()
		client := clients[token]
		clientsMu.Unlock()
		require.NotNil(t, client, "missing client for %s", token)
		client.releaseOne()
	}

	type updateResult struct {
		token string
		err   error
	}
	results := make(chan updateResult, 2)
	go func() { results <- updateResult{token: "token-a", err: svc.UpdateToken("token-a")} }()
	go func() { results <- updateResult{token: "token-b", err: svc.UpdateToken("token-b")} }()

	select {
	case result := <-results:
		t.Fatalf("UpdateToken(%s) returned before the initial poller drained: %v", result.token, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseClient("initial-token")
	completedUpdates := 0
	lastCompletedToken := ""
	for completedUpdates < 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			completedUpdates++
			lastCompletedToken = result.token
		case path := <-started:
			for _, token := range []string{"token-a", "token-b"} {
				if strings.Contains(path, "/bot"+token+"/getUpdates") {
					releaseClient(token)
				}
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent UpdateToken calls to finish")
		}
		assert.LessOrEqual(t, atomic.LoadInt32(&globalMaxActiveUpdates), int32(1), "concurrent token updates must not overlap long-poll requests")
	}

	svc.lifecycleMu.Lock()
	currentToken := ""
	if svc.bot != nil {
		currentToken = svc.bot.Token
	}
	svc.lifecycleMu.Unlock()
	require.Equal(t, lastCompletedToken, currentToken, "current bot should reflect the last completed token update")
	waitForTelegramPoller(t, started, currentToken)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&globalActiveUpdates) == 1
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&globalMaxActiveUpdates), "concurrent token updates must not overlap long-poll requests")
	assert.True(t, svc.IsRunning())
}

func waitForTelegramPoller(t *testing.T, started <-chan string, tokens ...string) string {
	t.Helper()
	wanted := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		wanted[token] = struct{}{}
	}
	deadline := time.After(time.Second)
	for {
		select {
		case path := <-started:
			for token := range wanted {
				if strings.Contains(path, "/bot"+token+"/getUpdates") {
					return token
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for telegram poller for one of %v", tokens)
		}
	}
}

type telegramStubClient struct {
	blockUpdates           chan struct{}
	releaseUpdates         chan struct{}
	getUpdatesResponse     string
	activeUpdates          int32
	maxActiveUpdates       int32
	globalActiveUpdates    *int32
	globalMaxActiveUpdates *int32
	updatesStarted         chan string
	closeOnce              sync.Once
}

func (c *telegramStubClient) unblock() {
	c.closeOnce.Do(func() { close(c.blockUpdates) })
}

func (c *telegramStubClient) releaseOne() {
	if c.releaseUpdates == nil {
		c.unblock()
		return
	}
	c.releaseUpdates <- struct{}{}
}

func updateMaxInt32(max *int32, value int32) {
	if max == nil {
		return
	}
	for {
		current := atomic.LoadInt32(max)
		if value <= current || atomic.CompareAndSwapInt32(max, current, value) {
			return
		}
	}
}

func (c *telegramStubClient) Do(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/getMe") {
		return telegramJSONResponse(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Test","username":"test_bot"}}`), nil
	}
	if strings.Contains(req.URL.Path, "/getUpdates") {
		active := atomic.AddInt32(&c.activeUpdates, 1)
		updateMaxInt32(&c.maxActiveUpdates, active)
		if c.globalActiveUpdates != nil {
			globalActive := atomic.AddInt32(c.globalActiveUpdates, 1)
			updateMaxInt32(c.globalMaxActiveUpdates, globalActive)
			defer atomic.AddInt32(c.globalActiveUpdates, -1)
		}
		defer atomic.AddInt32(&c.activeUpdates, -1)
		if c.updatesStarted != nil {
			select {
			case c.updatesStarted <- req.URL.Path:
			default:
			}
		}
		releasedOne := false
		select {
		case <-c.blockUpdates:
		case <-c.releaseUpdates:
			releasedOne = true
		case <-req.Context().Done():
		}
		if releasedOne {
			time.Sleep(25 * time.Millisecond)
		}
		body := c.getUpdatesResponse
		if body == "" {
			body = `{"ok":true,"result":[]}`
		}
		return telegramJSONResponse(body), nil
	}
	return telegramJSONResponse(`{"ok":true,"result":{}}`), nil
}

func telegramJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// Helper to create a TelegramService for testing (no real bot connection)
func closedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.NewTestDB(t)
	require.NoError(t, db.Close())
	return db
}

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
	swarmSvc := NewSwarmService(taskSvc, taskRepo, execRepo, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)

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
		threadInputRepo:    repository.NewThreadInputRepo(db),
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
	agentRepo := repository.NewAgentRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	taskSvc.SetAgentRepo(agentRepo)

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

	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, agent))

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
		agentRepo:     agentRepo,
		userProjects:  make(map[int64]string),
	}

	context_ := buildChannelChatContext(ctx, channelChatContextOptions{
		Platform:      "telegram",
		ProjectID:     project.ID,
		TaskSvc:       svc.taskSvc,
		LLMConfigRepo: svc.llmConfigRepo,
		AgentRepo:     svc.agentRepo,
	})
	assert.Contains(t, context_, "Fix login bug")
	assert.NotContains(t, context_, "Chat message")
	assert.Contains(t, context_, "Available Agent definitions")
	assert.Contains(t, context_, `Name: "Reviewer"`)
}

func TestChannelChatSelectAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	ctx := context.Background()

	// Should return a model (the default one from migration)
	agent, err := selectChannelChatAgent(ctx, llmConfigRepo, "hello world", false)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
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

	swarmOutput, handled, isErr, err := rt.Executor(ctx, "create_swarm_task", json.RawMessage(`{"title":"Telegram Swarm Created","prompt":"Split this work","category":"backlog"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, swarmOutput, "Created swarm task: Telegram Swarm Created")

	tasks, err = taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	var swarmParent *models.Task
	for i := range tasks {
		if tasks[i].Title == "Telegram Swarm Created" {
			swarmParent = &tasks[i]
			break
		}
	}
	require.NotNil(t, swarmParent)
	require.Equal(t, models.SwarmRoleParent, swarmParent.SwarmRole)
	require.Equal(t, models.TaskOriginTelegram, swarmParent.CreatedVia)
	require.Equal(t, int64(12345), swarmParent.TelegramChatID)

	finalOutput := collector.appendToOutput("Done.")
	require.Contains(t, finalOutput, "[TASK_ID:")
	require.Contains(t, finalOutput, swarmParent.ID)
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
	require.Contains(t, output, `"notifications":[]`)
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
	llmConfigRepo := repository.NewLLMConfigRepo(db)

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

	completeExecution := channelCompletionFunc("telegram", execRepo, taskRepo, nil, nil)

	completeExecution(ctx, exec.ID, task.ID, "response output", "", 100, 1000)

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
	llmConfigRepo := repository.NewLLMConfigRepo(db)

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

	promotedProject := ""
	completeExecution := channelCompletionFunc("telegram", execRepo, taskRepo, nil, func(projectID string) { promotedProject = projectID })

	completeExecution(ctx, exec.ID, task.ID, "", "something went wrong", 0, 500)

	// Verify execution was failed
	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecFailed, updatedExec.Status)
	assert.Equal(t, "something went wrong", updatedExec.ErrorMessage)

	// Verify task status
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusFailed, updatedTask.Status)
	assert.Equal(t, project.ID, promotedProject, "failed chat executions should still promote queued follow-ups")
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

	workDir := resolveChannelChatWorkDir(ctx, projectRepo, project.ID)
	assert.Equal(t, "/tmp/test/repo", workDir)

	// Non-existent project
	workDir = resolveChannelChatWorkDir(ctx, projectRepo, "nonexistent")
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

	linked, err := svc.linkAttachmentsToExecution(ctx, exec.ID, attachments)
	require.NoError(t, err)
	require.Len(t, linked, 1)

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
	linked, err := svc.linkAttachmentsToExecution(ctx, exec.ID, chatAttachments)
	require.NoError(t, err)
	require.Len(t, linked, 1)
	chatAttachments = linked

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

func TestTelegramService_LinkAttachments_RollsBackPartialLinksOnFailure(t *testing.T) {
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
		Title:     "Test rollback task",
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

	firstDir := t.TempDir()
	firstPath := filepath.Join(firstDir, "first.txt")
	require.NoError(t, os.WriteFile(firstPath, []byte("first"), 0644))
	missingPath := filepath.Join(t.TempDir(), "missing.txt")

	linked, err := svc.linkAttachmentsToExecution(ctx, exec.ID, []models.ChatAttachment{
		{FileName: "first.txt", FilePath: firstPath, MediaType: "text/plain", FileSize: 5},
		{FileName: "missing.txt", FilePath: missingPath, MediaType: "text/plain", FileSize: 7},
	})
	require.Error(t, err)
	require.Nil(t, linked)

	dbAttachments, err := svc.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
	require.NoError(t, err)
	require.Empty(t, dbAttachments, "partial attachment DB rows should be rolled back")

	execDir := filepath.Join(telegramUploadsDir, "chat", exec.ID)
	entries, readErr := os.ReadDir(execDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		require.NoError(t, readErr)
	}
	require.Empty(t, entries, "partial attachment files should be removed after rollback")
}

func TestChannelChatSelectAgent_WithImages(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	ctx := context.Background()

	// Should return a model even with hasImages=true (uses vision-aware selection)
	agent, err := selectChannelChatAgent(ctx, llmConfigRepo, "analyze this image", true)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
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

	payload, err := json.Marshal(SendToTaskRequest{TaskID: task.ID, Message: "Also add error handling for empty passwords"})
	require.NoError(t, err)

	var runnerReq ChannelTaskRunRequest
	runnerCalled := false
	svc.SetChannelTaskRunner(func(_ context.Context, req ChannelTaskRunRequest) {
		runnerCalled = true
		runnerReq = req
	})

	handlers := svc.telegramActionHandlers(project.ID, 12345, 67890, nil)
	result, err := handlers["send_to_task"](ctx, payload)
	require.NoError(t, err)

	// Should contain confirmation message
	assert.Contains(t, result, "Sent message to task")
	assert.Contains(t, result, "Fix login bug")

	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusQueued, updatedTask.Status)
	assert.NotEqual(t, models.TaskOriginTelegram, updatedTask.CreatedVia, "channel send_to_task must not rewrite target task origin")
	assert.Equal(t, int64(0), updatedTask.TelegramChatID)

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
	assert.True(t, runnerCalled, "shared channel task runner should process Telegram task follow-ups when wired")
	assert.Equal(t, task.ID, runnerReq.TaskID)
	assert.Equal(t, project.ID, runnerReq.ProjectID)
	assert.Equal(t, chatcontrol.SurfaceTelegram, runnerReq.Surface)
	assert.Equal(t, models.TaskOriginTelegram, runnerReq.ReplyContext.Source)
	assert.Equal(t, int64(12345), runnerReq.ReplyContext.TelegramChatID)
}

func TestTelegramService_ProcessSendToTask_QueuesWhenTaskActive(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()
	project := &models.Project{Name: "Test Project", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))
	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	task := &models.Task{Title: "Running task", Prompt: "work", Category: models.CategoryActive, Status: models.StatusRunning, ProjectID: project.ID, AgentID: &agents[0].ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agents[0].ID, Status: models.ExecRunning, PromptSent: "active", IsFollowup: true}
	require.NoError(t, svc.execRepo.Create(ctx, active))
	payload, err := json.Marshal(SendToTaskRequest{TaskID: task.ID, Message: "queued follow-up"})
	require.NoError(t, err)
	handlers := svc.telegramActionHandlers(project.ID, 12345, 67890, nil)
	result, err := handlers["send_to_task"](ctx, payload)
	require.NoError(t, err)
	assert.Contains(t, result, "Queued message to task")
	inputs, err := svc.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Equal(t, "queued follow-up", inputs[0].Content)
	assert.Equal(t, active.ID, inputs[0].RunExecutionID)
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.NotEqual(t, models.TaskOriginTelegram, updatedTask.CreatedVia, "queued channel follow-up must not hijack active task reply origin")
	assert.Equal(t, int64(0), updatedTask.TelegramChatID)
	assert.Equal(t, models.TaskOriginTelegram, inputs[0].Source)
	assert.Equal(t, int64(12345), inputs[0].TelegramChatID)
}

func TestTelegramService_ProcessSendToTask_QueuesDuringStartingFirstTurnBeforeExecutionExists(t *testing.T) {
	svc, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()
	project := &models.Project{Name: "Starting First Turn Project", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))
	agents, err := svc.llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	task := &models.Task{Title: "Starting task", Prompt: "tell me a story about a duck", Category: models.CategoryActive, Status: models.StatusPending, ProjectID: project.ID, AgentID: &agents[0].ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	runnerCalled := false
	svc.SetChannelTaskRunner(func(context.Context, ChannelTaskRunRequest) {
		runnerCalled = true
	})

	payload, err := json.Marshal(SendToTaskRequest{TaskID: task.ID, Message: "1+1=?"})
	require.NoError(t, err)
	handlers := svc.telegramActionHandlers(project.ID, 12345, 67890, nil)
	result, err := handlers["send_to_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, result, "Queued message to task")
	require.False(t, runnerCalled, "pre-execution first-turn send must not start a follow-up runner")
	execs, err := svc.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, execs)
	inputs, err := svc.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, "1+1=?", inputs[0].Content)
	require.Empty(t, inputs[0].RunExecutionID)
	require.Equal(t, models.TaskOriginTelegram, inputs[0].Source)
	require.Equal(t, int64(12345), inputs[0].TelegramChatID)
}

func TestTelegramService_ResolveTaskReference_ByID(t *testing.T) {
	_, projectRepo, taskRepo := newTestTelegramService(t)
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
	found, err := resolveChannelTaskReference(ctx, taskRepo, project.ID, task.ID, "")
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
	assert.Equal(t, "My Task", found.Title)
}

func TestTelegramService_ResolveTaskReference_ByTitle(t *testing.T) {
	_, projectRepo, taskRepo := newTestTelegramService(t)
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
	found, err := resolveChannelTaskReference(ctx, taskRepo, project.ID, "", "authentication")
	require.NoError(t, err)
	assert.Equal(t, task.ID, found.ID)
}

func TestTelegramService_ResolveTaskReference_NotFound(t *testing.T) {
	_, projectRepo, taskRepo := newTestTelegramService(t)
	ctx := context.Background()

	project := &models.Project{
		Name:      "Test Project",
		RepoPath:  "/tmp/test",
		IsDefault: true,
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	// ID not found
	_, err := resolveChannelTaskReference(ctx, taskRepo, project.ID, "nonexistent", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Title not found
	_, err = resolveChannelTaskReference(ctx, taskRepo, project.ID, "", "zzz no match zzz")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no task found")

	// Neither provided
	_, err = resolveChannelTaskReference(ctx, taskRepo, project.ID, "", "")
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

// --- App Settings Tests ---

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

func TestBuildChannelProjectActionHandlersListProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Beta", RepoPath: "/tmp/beta", Description: "Second project"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{ProjectID: project1.ID, ProjectRepo: projectRepo})
	result, err := handlers["list_projects"](ctx, nil)
	require.NoError(t, err)
	assert.Contains(t, result, "Available Projects")
	assert.Contains(t, result, "Alpha")
	assert.Contains(t, result, "Beta")
	assert.Contains(t, result, "Second project")
	assert.Contains(t, result, "<- current")
}

func TestBuildChannelProjectActionHandlersSwitchProjectCallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Beta", RepoPath: "/tmp/beta"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	var switchedProjectID string
	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID:   project1.ID,
		ProjectRepo: projectRepo,
		SwitchProject: func(_ context.Context, project *models.Project) error {
			switchedProjectID = project.ID
			return nil
		},
	})
	result, err := handlers["switch_project"](ctx, json.RawMessage(`{"project":"Beta"}`))
	require.NoError(t, err)
	assert.Contains(t, result, "Switched to project: Beta")
	assert.Equal(t, project2.ID, switchedProjectID)
}

func TestBuildChannelProjectActionHandlersSwitchProjectInvalidProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))

	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{ProjectID: project1.ID, ProjectRepo: projectRepo})
	result, err := handlers["switch_project"](ctx, json.RawMessage(`{"project":"NonExistent"}`))
	require.NoError(t, err)
	assert.Contains(t, result, "Project not found")
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

func TestTelegramService_ProcessIncomingMessage_QueuesWhenChatActive(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{Name: "Telegram Queue Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	activeTask := &models.Task{ProjectID: project.ID, Title: "active", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active", AgentID: &agent.ID, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))

	var sent []string
	svc := &TelegramService{
		projectRepo:     projectRepo,
		llmConfigRepo:   llmConfigRepo,
		taskRepo:        taskRepo,
		execRepo:        execRepo,
		threadInputRepo: threadInputRepo,
		sendMessageFunc: func(chatID int64, text string) { sent = append(sent, text) },
		userProjects:    map[int64]string{7: project.ID},
	}

	svc.handleChatMessage(&tgbotapi.Message{
		From: &tgbotapi.User{ID: 7},
		Chat: &tgbotapi.Chat{ID: 42},
		Text: "follow up from telegram",
	})

	inputs, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, models.ThreadInputModeQueued, inputs[0].InputMode)
	require.Equal(t, activeExec.ID, inputs[0].RunExecutionID)
	require.Equal(t, models.TaskOriginTelegram, inputs[0].Source)
	require.Equal(t, int64(42), inputs[0].TelegramChatID)
	require.Contains(t, strings.Join(sent, "\n"), "Queued")

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1, "queued channel follow-up must not create a second chat task immediately")
}

func runTelegramQueueChatIngressForTest(ctx context.Context, svc *TelegramService, projectID, activeExecID, text string, chatID int64, chatAttachments []models.ChatAttachment) bool {
	return runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:        "telegram",
		ProjectID:       projectID,
		Message:         text,
		Source:          models.TaskOriginTelegram,
		Surface:         chatcontrol.SurfaceTelegram,
		HasAttachments:  len(chatAttachments) > 0,
		ThreadInputRepo: svc.threadInputRepo,
		LLMConfigRepo:   svc.llmConfigRepo,
		ChatBroadcaster: svc.chatBroadcaster,
		UploadsDir:      telegramUploadsDir,
		DownloadAttachments: func(context.Context) (channelChatIngressDownloadResult, error) {
			attCtx, imgAtts := channelChatAttachmentContextAndImages(chatAttachments, telegramMaxTextFileSize)
			return channelChatIngressDownloadResult{AttachmentContext: attCtx, ImageAttachments: imgAtts, ChatAttachments: chatAttachments}, nil
		},
		SavePendingAttachments: svc.saveChatAttachmentsToPendingSession,
		FindActiveExecution: func(context.Context, string) (*models.Execution, error) {
			return &models.Execution{ID: activeExecID}, nil
		},
		NewQueuedInput: func() *models.ThreadInput { return &models.ThreadInput{TelegramChatID: chatID} },
		OnAttachmentStoreFailed: func(context.Context, string) {
			svc.sendMessage(ctx, chatID, "Error queueing your attachment. Please try again.")
		},
		OnQueueFailure: func(context.Context) { svc.sendMessage(ctx, chatID, "Error queueing your message. Please try again.") },
		OnQueued: func(context.Context) {
			svc.sendMessage(ctx, chatID, "Queued. I'll send this after the current response finishes.")
		},
	})
}

func TestTelegramService_ProcessIncomingMessage_QueuesTelegramAttachmentWhenChatActive(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	oldUploadsDir := telegramUploadsDir
	telegramUploadsDir = t.TempDir()
	t.Cleanup(func() { telegramUploadsDir = oldUploadsDir })

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{Name: "Telegram Attachment Queue Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)
	activeTask := &models.Task{ProjectID: project.ID, Title: "active", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active", AgentID: &agent.ID, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))

	fileBody := "queued telegram attachment"
	tmpDir := t.TempDir()
	sourcePath := filepath.Join(tmpDir, "test.txt")
	require.NoError(t, os.WriteFile(sourcePath, []byte(fileBody), 0644))

	var sent []string
	svc := &TelegramService{
		projectRepo:     projectRepo,
		llmConfigRepo:   llmConfigRepo,
		taskRepo:        taskRepo,
		execRepo:        execRepo,
		threadInputRepo: threadInputRepo,
		sendMessageFunc: func(chatID int64, text string) { sent = append(sent, text) },
		userProjects:    map[int64]string{7: project.ID},
	}

	require.True(t, runTelegramQueueChatIngressForTest(ctx, svc, project.ID, activeExec.ID, "follow up with attachment", 42, []models.ChatAttachment{{
		FileName:  "test.txt",
		FilePath:  sourcePath,
		MediaType: "text/plain",
		FileSize:  int64(len(fileBody)),
	}}))

	inputs, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.NotEmpty(t, inputs[0].AttachmentSessionID)
	entries, err := os.ReadDir(filepath.Join(telegramUploadsDir, "chat", "pending", inputs[0].AttachmentSessionID))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	staged, err := os.ReadFile(filepath.Join(telegramUploadsDir, "chat", "pending", inputs[0].AttachmentSessionID, entries[0].Name()))
	require.NoError(t, err)
	require.Equal(t, fileBody, string(staged))
	require.Contains(t, strings.Join(sent, "\n"), "Queued")
}

func TestTelegramService_QueueChatInputCleansPendingAttachmentsWhenQueueInsertFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	oldUploadsDir := telegramUploadsDir
	telegramUploadsDir = t.TempDir()
	t.Cleanup(func() { telegramUploadsDir = oldUploadsDir })

	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	queueDB := testutil.NewTestDB(t)
	threadInputRepo := repository.NewThreadInputRepo(queueDB)
	require.NoError(t, queueDB.Close())

	fileBody := "queued telegram attachment"
	tmpDir := t.TempDir()
	sourcePath := filepath.Join(tmpDir, "test.txt")
	require.NoError(t, os.WriteFile(sourcePath, []byte(fileBody), 0644))

	var sent []string
	svc := &TelegramService{
		llmConfigRepo:   llmConfigRepo,
		threadInputRepo: threadInputRepo,
		sendMessageFunc: func(chatID int64, text string) { sent = append(sent, text) },
	}

	require.True(t, runTelegramQueueChatIngressForTest(ctx, svc, "project-1", "exec-1", "follow up with attachment", 42, []models.ChatAttachment{{
		FileName:  "test.txt",
		FilePath:  sourcePath,
		MediaType: "text/plain",
		FileSize:  int64(len(fileBody)),
	}}))

	pendingRoot := filepath.Join(telegramUploadsDir, "chat", "pending")
	entries, err := os.ReadDir(pendingRoot)
	if err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	require.Empty(t, entries, "expected staged pending attachments to be cleaned up after queue insert failure")
	require.Contains(t, strings.Join(sent, "\n"), "Error queueing your message")
}

func TestTelegramService_HandleChatMessageCleansDownloadedAttachmentOnPostDownloadFailures(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(t *testing.T, db *sql.DB, projectID string, svc *TelegramService)
		wantMessage string
	}{
		{
			name: "agent selection fails",
			configure: func(t *testing.T, db *sql.DB, projectID string, svc *TelegramService) {
				modelDB := testutil.NewTestDB(t)
				svc.llmConfigRepo = repository.NewLLMConfigRepo(modelDB)
				require.NoError(t, modelDB.Close())
			},
			wantMessage: "Error selecting model",
		},
		{
			name: "active chat lookup fails",
			configure: func(t *testing.T, db *sql.DB, projectID string, svc *TelegramService) {
				svc.execRepo = repository.NewExecutionRepo(closedTestDB(t))
			},
			wantMessage: "Error checking active chat response",
		},
		{
			name: "task creation fails",
			configure: func(t *testing.T, db *sql.DB, projectID string, svc *TelegramService) {
				svc.taskRepo = repository.NewTaskRepo(closedTestDB(t), nil)
			},
			wantMessage: "Error processing your message",
		},
		{
			name: "execution creation fails",
			configure: func(t *testing.T, db *sql.DB, projectID string, svc *TelegramService) {
				_, err := db.Exec(`CREATE TRIGGER telegram_test_fail_execution_insert BEFORE INSERT ON executions BEGIN SELECT RAISE(FAIL, 'forced execution create failure'); END;`)
				require.NoError(t, err)
			},
			wantMessage: "Error processing your message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			projectRepo := repository.NewProjectRepo(db)
			taskRepo := repository.NewTaskRepo(db, nil)
			execRepo := repository.NewExecutionRepo(db)
			llmConfigRepo := repository.NewLLMConfigRepo(db)

			project := &models.Project{Name: "Telegram Attachment Cleanup Project"}
			require.NoError(t, projectRepo.Create(ctx, project))

			var sent []string
			svc := &TelegramService{
				projectRepo:     projectRepo,
				llmConfigRepo:   llmConfigRepo,
				taskRepo:        taskRepo,
				execRepo:        execRepo,
				sendMessageFunc: func(chatID int64, text string) { sent = append(sent, text) },
				userProjects:    map[int64]string{7: project.ID},
			}
			tt.configure(t, db, project.ID, svc)

			var apiServer *httptest.Server
			apiServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/botTESTTOKEN/getMe":
					_, _ = w.Write([]byte(`{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"TestBot","username":"test_bot"}}`))
				case "/botTESTTOKEN/getFile":
					_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"file-1","file_unique_id":"unique-1","file_path":"documents/test.txt"}}`))
				case "/file/botTESTTOKEN/documents/test.txt":
					_, _ = w.Write([]byte("downloaded telegram attachment"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer apiServer.Close()

			bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("TESTTOKEN", apiServer.URL+"/bot%s/%s")
			require.NoError(t, err)
			svc.bot = bot

			oldTransport := http.DefaultTransport
			apiServerURL := apiServer.URL
			http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.telegram.org" && strings.HasPrefix(req.URL.Path, "/file/botTESTTOKEN/") {
					rewritten := req.Clone(req.Context())
					rewritten.URL.Scheme = "http"
					rewritten.URL.Host = strings.TrimPrefix(apiServerURL, "http://")
					return oldTransport.RoundTrip(rewritten)
				}
				return oldTransport.RoundTrip(req)
			})
			t.Cleanup(func() { http.DefaultTransport = oldTransport })

			tempRoot := t.TempDir()
			oldTempDir := os.Getenv("TMPDIR")
			require.NoError(t, os.Setenv("TMPDIR", tempRoot))
			t.Cleanup(func() {
				if oldTempDir == "" {
					require.NoError(t, os.Unsetenv("TMPDIR"))
				} else {
					require.NoError(t, os.Setenv("TMPDIR", oldTempDir))
				}
			})

			svc.handleChatMessage(&tgbotapi.Message{
				From:    &tgbotapi.User{ID: 7},
				Chat:    &tgbotapi.Chat{ID: 42},
				Caption: "summarize this file",
				Document: &tgbotapi.Document{
					FileID:   "file-1",
					FileName: "test.txt",
					FileSize: 32,
					MimeType: "text/plain",
				},
			})

			entries, err := os.ReadDir(tempRoot)
			require.NoError(t, err)
			require.Empty(t, entries, "downloaded Telegram attachment temp directories should be cleaned up on %s", tt.name)
			require.Contains(t, strings.Join(sent, "\n"), tt.wantMessage)
		})
	}
}

func TestTelegramService_HandleChatMessage_UsesSharedChannelChatRunner(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	project := &models.Project{Name: "Telegram Shared Runner Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	var got *ChannelChatRunRequest
	svc := &TelegramService{
		projectRepo:     projectRepo,
		llmConfigRepo:   llmConfigRepo,
		taskSvc:         taskSvc,
		taskRepo:        taskRepo,
		execRepo:        execRepo,
		sendMessageFunc: func(chatID int64, text string) {},
		userProjects:    map[int64]string{7: project.ID},
	}
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) {
		got = &req
	})

	svc.handleChatMessage(&tgbotapi.Message{
		From: &tgbotapi.User{ID: 7},
		Chat: &tgbotapi.Chat{ID: 42},
		Text: "start from telegram",
	})

	require.NotNil(t, got, "Telegram chat should use the shared steering-aware runner when wired")
	workerSvc.cancelMu.Lock()
	_, serviceRegisteredCancel := workerSvc.cancelFuncs[got.TaskID]
	workerSvc.cancelMu.Unlock()
	require.False(t, serviceRegisteredCancel, "Telegram service must not register and immediately deregister cancellation when the shared runner owns the run")
	require.NotEmpty(t, got.ExecID)
	require.NotEmpty(t, got.TaskID)
	require.Equal(t, project.ID, got.ProjectID)
	require.Equal(t, "start from telegram", got.Message)
	require.Equal(t, agent.ID, got.Agent.ID)
	require.Equal(t, chatcontrol.SurfaceTelegram, got.Surface)
	createdExec, err := execRepo.GetByID(ctx, got.ExecID)
	require.NoError(t, err)
	require.NotNil(t, createdExec)
	require.Equal(t, models.ExecRunning, createdExec.Status)
}

func TestTelegramService_HandleChatMessageSniffsOctetStreamImageForVisionModel(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	project := &models.Project{Name: "Telegram Octet Image Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	defaultAgent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, defaultAgent)
	defaultAgent.Provider = models.ProviderAnthropic
	defaultAgent.AuthMethod = models.AuthMethodCLI
	defaultAgent.Model = "claude-sonnet-4-5"
	defaultAgent.IsDefault = true
	require.NoError(t, llmConfigRepo.Update(ctx, defaultAgent))
	visionAgent := &models.LLMConfig{
		Name:       "Vision Sonnet",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-key",
		Model:      "claude-3-5-sonnet-20241022",
	}
	require.NoError(t, llmConfigRepo.Create(ctx, visionAgent))

	oldUploadsDir := telegramUploadsDir
	telegramUploadsDir = t.TempDir()
	t.Cleanup(func() { telegramUploadsDir = oldUploadsDir })

	var got *ChannelChatRunRequest
	svc := &TelegramService{
		projectRepo:        projectRepo,
		llmConfigRepo:      llmConfigRepo,
		taskSvc:            taskSvc,
		taskRepo:           taskRepo,
		execRepo:           execRepo,
		chatAttachmentRepo: chatAttachmentRepo,
		sendMessageFunc:    func(chatID int64, text string) {},
		userProjects:       map[int64]string{7: project.ID},
	}
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) {
		got = &req
	})

	var apiServer *httptest.Server
	apiServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/botTESTTOKEN/getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"TestBot","username":"test_bot"}}`))
		case "/botTESTTOKEN/getFile":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"file-1","file_unique_id":"unique-1","file_path":"documents/screenshot.bin"}}`))
		case "/file/botTESTTOKEN/documents/screenshot.bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(slackTestPNGBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("TESTTOKEN", apiServer.URL+"/bot%s/%s")
	require.NoError(t, err)
	svc.bot = bot

	oldTransport := http.DefaultTransport
	apiServerURL := apiServer.URL
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.telegram.org" && strings.HasPrefix(req.URL.Path, "/file/botTESTTOKEN/") {
			rewritten := req.Clone(req.Context())
			rewritten.URL.Scheme = "http"
			rewritten.URL.Host = strings.TrimPrefix(apiServerURL, "http://")
			return oldTransport.RoundTrip(rewritten)
		}
		return oldTransport.RoundTrip(req)
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	svc.handleChatMessage(&tgbotapi.Message{
		From:    &tgbotapi.User{ID: 7},
		Chat:    &tgbotapi.Chat{ID: 42},
		Caption: "what is this screenshot?",
		Document: &tgbotapi.Document{
			FileID:   "file-1",
			FileName: "screenshot.bin",
			FileSize: len(slackTestPNGBytes),
			MimeType: "application/octet-stream",
		},
	})

	require.NotNil(t, got, "Telegram chat should invoke the shared runner")
	require.Len(t, got.ImageAttachments, 1)
	require.Equal(t, "image/png", got.ImageAttachments[0].MediaType)
	require.Equal(t, visionAgent.ID, got.Agent.ID)
	require.NotContains(t, got.SystemContext, string(slackTestPNGBytes), "sniffed image bytes must not be embedded as text context")
	persistedExec, err := execRepo.GetByID(ctx, got.ExecID)
	require.NoError(t, err)
	require.NotNil(t, persistedExec)
	require.Equal(t, visionAgent.ID, persistedExec.AgentConfigID)
	persistedAttachments, err := chatAttachmentRepo.ListByExecution(ctx, got.ExecID)
	require.NoError(t, err)
	require.Len(t, persistedAttachments, 1)
	require.Equal(t, "image/png", persistedAttachments[0].MediaType)
}

func TestTelegramService_IsRichMessagesV2EnabledDefaultsTrueAndFalseOnlyExplicit(t *testing.T) {
	ctx := context.Background()
	svc := &TelegramService{}
	require.True(t, svc.IsRichMessagesV2Enabled(ctx))

	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	svc.settingsRepo = settingsRepo
	require.True(t, svc.IsRichMessagesV2Enabled(ctx))

	require.NoError(t, settingsRepo.Set(ctx, TelegramSettingRichMessagesV2, ""))
	require.True(t, svc.IsRichMessagesV2Enabled(ctx))

	require.NoError(t, settingsRepo.Set(ctx, TelegramSettingRichMessagesV2, "FALSE"))
	require.False(t, svc.IsRichMessagesV2Enabled(ctx))

	require.NoError(t, settingsRepo.Set(ctx, TelegramSettingRichMessagesV2, "true"))
	require.True(t, svc.IsRichMessagesV2Enabled(ctx))
}

func TestTelegramService_RichMarkdownPayloadAndNormalization(t *testing.T) {
	payload := telegramRichMarkdownPayload("\r\n## Heading\r\n\r\n| A | B |\r\n")
	require.Equal(t, "## Heading\n\n| A | B |", payload.Markdown)
	require.Empty(t, payload.HTML)
}

func TestTelegramService_EscapeTelegramMarkdownV2EscapesRequiredCharacters(t *testing.T) {
	input := `\_*[]()~` + "`" + `>#+-=|{}.!`
	want := "\\\\\\_\\*\\[\\]\\(\\)\\~\\`\\>\\#\\+\\-\\=\\|\\{\\}\\.\\!"
	require.Equal(t, want, escapeTelegramMarkdownV2(input))
}

func TestTelegramService_SendMessageRichEnabledUsesSendRichMessage(t *testing.T) {
	var endpoint string
	var params tgbotapi.Params
	svc := &TelegramService{
		makeRequestFunc: func(gotEndpoint string, gotParams tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoint = gotEndpoint
			params = gotParams
			return &tgbotapi.APIResponse{Ok: true}, nil
		},
	}

	svc.sendMessage(context.Background(), 42, "## Rich\n\n| A | B |")

	require.Equal(t, "sendRichMessage", endpoint)
	require.Equal(t, "42", params["chat_id"])
	require.Contains(t, params["rich_message"], `"markdown":"## Rich`)
	require.NotContains(t, params["rich_message"], `"html"`)
}

func TestTelegramService_SendMessageRichDisabledUsesMarkdownV2Fallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), TelegramSettingRichMessagesV2, "false"))
	var sent []tgbotapi.MessageConfig
	svc := &TelegramService{
		settingsRepo: settingsRepo,
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			msg, ok := c.(tgbotapi.MessageConfig)
			require.True(t, ok)
			sent = append(sent, msg)
			return tgbotapi.Message{}, nil
		},
	}

	svc.sendMessage(context.Background(), 42, "Hello *world*!")

	require.Len(t, sent, 1)
	require.Equal(t, "MarkdownV2", sent[0].ParseMode)
	require.Equal(t, `Hello \*world\*\!`, sent[0].Text)
}

func TestTelegramService_SendMessageRichFallbackSendsMarkdownV2ThenPlain(t *testing.T) {
	var sent []tgbotapi.MessageConfig
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			require.Equal(t, "sendRichMessage", endpoint)
			return nil, fmt.Errorf("Bad Request: method not found")
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			msg, ok := c.(tgbotapi.MessageConfig)
			require.True(t, ok)
			sent = append(sent, msg)
			if len(sent) == 1 {
				return tgbotapi.Message{}, fmt.Errorf("can't parse entities")
			}
			return tgbotapi.Message{}, nil
		},
	}

	svc.sendMessage(context.Background(), 42, "Hello *world*!")

	require.Len(t, sent, 2)
	require.Equal(t, "MarkdownV2", sent[0].ParseMode)
	require.Equal(t, "", sent[1].ParseMode)
	require.Equal(t, "Hello *world*!", sent[1].Text)
}

func TestTelegramService_SendMessageRichAmbiguousErrorDoesNotFallback(t *testing.T) {
	sendCalled := false
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			require.Equal(t, "sendRichMessage", endpoint)
			return nil, fmt.Errorf("Post \"https://api.telegram.org\": EOF")
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			sendCalled = true
			return tgbotapi.Message{}, nil
		},
	}

	svc.sendMessage(context.Background(), 42, "could have been delivered")

	require.False(t, sendCalled, "ambiguous rich send errors should not fallback and risk duplicate output")
}

func TestTelegramService_EditMessageRichUsesEditMessageTextRichMessage(t *testing.T) {
	var endpoint string
	var params tgbotapi.Params
	svc := &TelegramService{
		makeRequestFunc: func(gotEndpoint string, gotParams tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoint = gotEndpoint
			params = gotParams
			return &tgbotapi.APIResponse{Ok: true}, nil
		},
	}

	svc.editMessage(context.Background(), 42, 99, "final **answer**")

	require.Equal(t, "editMessageText", endpoint)
	require.Equal(t, "42", params["chat_id"])
	require.Equal(t, "99", params["message_id"])
	require.Contains(t, params["rich_message"], `"markdown":"final **answer**"`)
}

func TestTelegramService_SendRichMessageDraftUsesNonZeroDraftID(t *testing.T) {
	var endpoint string
	var params tgbotapi.Params
	svc := &TelegramService{
		makeRequestFunc: func(gotEndpoint string, gotParams tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoint = gotEndpoint
			params = gotParams
			return &tgbotapi.APIResponse{Ok: true}, nil
		},
	}

	draftID := newTelegramRichDraftID()
	sent, err := svc.sendRichMessageDraft(context.Background(), 42, draftID, "partial")

	require.NoError(t, err)
	require.True(t, sent)
	require.Equal(t, "sendRichMessageDraft", endpoint)
	require.NotEqual(t, "0", params["draft_id"])
	require.Equal(t, strconv.Itoa(draftID), params["draft_id"])
	require.Contains(t, params["rich_message"], `"markdown":"partial"`)
}

func TestTelegramService_StreamEditTerminalOutputDoesNotAppendGenerating(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Legacy Terminal Stream"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	task := &models.Task{ProjectID: project.ID, Title: "chat", Prompt: "hi", Category: models.CategoryChat, Status: models.StatusRunning, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "hi"}
	require.NoError(t, execRepo.Create(ctx, exec))
	require.NoError(t, execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "final answer", "", 0, 0))

	edited := make(chan string, 1)
	svc := &TelegramService{
		execRepo: execRepo,
		editMessageFunc: func(chatID int64, messageID int, text string) {
			edited <- text
		},
	}
	oldInterval := telegramStreamInterval
	telegramStreamInterval = time.Millisecond
	t.Cleanup(func() { telegramStreamInterval = oldInterval })
	svc.streamEditUpdatesToTelegram(ctx, 42, 99, exec.ID, nil)

	select {
	case text := <-edited:
		require.Equal(t, "final answer", text)
		require.NotContains(t, text, "Generating")
	case <-time.After(time.Second):
		t.Fatal("expected terminal stream edit")
	}
}

func TestTelegramService_StreamRichDraftFallbackUsesEditLoop(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Rich Draft Fallback"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	task := &models.Task{ProjectID: project.ID, Title: "chat", Prompt: "hi", Category: models.CategoryChat, Status: models.StatusRunning, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "hi"}
	require.NoError(t, execRepo.Create(ctx, exec))
	require.NoError(t, execRepo.UpdateOutput(ctx, exec.ID, "partial output"))

	var richDraftCalled bool
	var edited []string
	editObserved := make(chan struct{})
	svc := &TelegramService{
		execRepo: execRepo,
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			require.Equal(t, "sendRichMessageDraft", endpoint)
			richDraftCalled = true
			return nil, fmt.Errorf("Bad Request: method not found")
		},
		editMessageFunc: func(chatID int64, messageID int, text string) {
			edited = append(edited, text)
			select {
			case editObserved <- struct{}{}:
			default:
			}
		},
	}
	done := svc.beginTelegramPreview(42, 99)
	oldInterval := telegramStreamInterval
	telegramStreamInterval = time.Millisecond
	t.Cleanup(func() { telegramStreamInterval = oldInterval })
	go svc.streamUpdatesToTelegram(ctx, 42, 99, exec.ID, done)
	select {
	case <-editObserved:
		svc.finishTelegramPreview(42, 99)
	case <-time.After(time.Second):
		t.Fatal("expected rich draft fallback edit")
	}

	require.True(t, richDraftCalled)
	require.NotEmpty(t, edited)
	require.Contains(t, edited[0], "partial output")
}

func TestTelegramService_HandleChatMessage_StartsTelegramStreamWithBackgroundContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, TelegramSettingRichMessagesV2, "false"))

	project := &models.Project{Name: "Telegram Stream Context Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	streamEdited := make(chan struct{}, 1)
	svc := &TelegramService{
		projectRepo:   projectRepo,
		llmConfigRepo: llmConfigRepo,
		taskSvc:       taskSvc,
		taskRepo:      taskRepo,
		execRepo:      execRepo,
		settingsRepo:  settingsRepo,
		userProjects:  map[int64]string{7: project.ID},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			if _, ok := c.(tgbotapi.MessageConfig); ok {
				return tgbotapi.Message{MessageID: 99}, nil
			}
			return tgbotapi.Message{}, nil
		},
		editMessageFunc: func(chatID int64, messageID int, text string) {
			if strings.Contains(text, "streamed output") {
				select {
				case streamEdited <- struct{}{}:
				default:
				}
			}
		},
	}
	oldInterval := telegramStreamInterval
	telegramStreamInterval = time.Millisecond
	t.Cleanup(func() { telegramStreamInterval = oldInterval })
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) {
		go func() {
			require.Eventually(t, func() bool {
				return execRepo.UpdateOutput(context.Background(), req.ExecID, "streamed output") == nil
			}, time.Second, time.Millisecond)
		}()
	})

	svc.handleChatMessage(&tgbotapi.Message{
		From: &tgbotapi.User{ID: 7},
		Chat: &tgbotapi.Chat{ID: 42},
		Text: "start streaming context test",
	})

	select {
	case <-streamEdited:
	case <-time.After(time.Second):
		t.Fatal("expected Telegram stream preview to continue after handleChatMessage returns")
	}
}

func TestTelegramService_SendChatResponse_SendsChatTaskOutput(t *testing.T) {
	var sent []string
	svc := &TelegramService{sendMessageFunc: func(chatID int64, text string) { sent = append(sent, text) }}
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "hello from queued chat", "")

	require.Equal(t, []string{"hello from queued chat"}, sent)
}

func TestTelegramService_SendChatResponse_EditsInitialAckMessage(t *testing.T) {
	var sent []string
	var edited []string
	svc := &TelegramService{
		sendMessageFunc: func(chatID int64, text string) { sent = append(sent, text) },
		editMessageFunc: func(chatID int64, messageID int, text string) {
			edited = append(edited, fmt.Sprintf("%d:%d:%s", chatID, messageID, text))
		},
	}
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "hello from initial telegram chat", "", 99)

	require.Empty(t, sent)
	require.Equal(t, []string{"42:99:hello from initial telegram chat"}, edited)
}

func TestTelegramService_SendChatResponse_RichEditFailureSendsFinalRichMessageAndClearsPlaceholder(t *testing.T) {
	var endpoints []string
	var clearedPlaceholder string
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoints = append(endpoints, endpoint)
			require.Equal(t, "42", params["chat_id"])
			require.Contains(t, params["rich_message"], "hello from initial telegram chat")
			if endpoint == "editMessageText" {
				require.Equal(t, "99", params["message_id"])
				return nil, fmt.Errorf("Bad Request: rich_message unsupported")
			}
			require.Equal(t, "sendRichMessage", endpoint)
			return &tgbotapi.APIResponse{Ok: true}, nil
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			edit, ok := c.(tgbotapi.EditMessageTextConfig)
			require.True(t, ok, "expected placeholder cleanup edit after final rich send")
			require.Equal(t, int64(42), edit.ChatID)
			require.Equal(t, 99, edit.MessageID)
			clearedPlaceholder = edit.Text
			return tgbotapi.Message{MessageID: 99}, nil
		},
	}
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "hello from initial telegram chat", "", 99)

	require.Equal(t, []string{"editMessageText", "sendRichMessage"}, endpoints)
	require.Equal(t, "✅ Response sent.", clearedPlaceholder)
}

func TestTelegramService_SendChatResponse_RichDraftStateSurvivesTerminalStreamUntilFinalDelivery(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Rich Draft Terminal Preview"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	taskModel := &models.Task{ProjectID: project.ID, Title: "chat", Prompt: "hi", Category: models.CategoryChat, Status: models.StatusRunning, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, taskModel))
	exec := &models.Execution{TaskID: taskModel.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "hi"}
	require.NoError(t, execRepo.Create(ctx, exec))
	require.NoError(t, execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "final rich response", "", 0, 0))

	streamExited := make(chan struct{})
	var endpoints []string
	var clearedPlaceholder string
	svc := &TelegramService{
		execRepo: execRepo,
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoints = append(endpoints, endpoint)
			require.Equal(t, "42", params["chat_id"])
			require.Contains(t, params["rich_message"], "final rich response")
			switch endpoint {
			case "sendRichMessageDraft", "sendRichMessage":
				return &tgbotapi.APIResponse{Ok: true}, nil
			case "editMessageText":
				t.Fatal("final delivery must remember the visible draft and avoid editing the placeholder with final content")
			}
			return nil, fmt.Errorf("unexpected endpoint %s", endpoint)
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			edit, ok := c.(tgbotapi.EditMessageTextConfig)
			require.True(t, ok, "expected placeholder cleanup edit after final rich send")
			require.Equal(t, int64(42), edit.ChatID)
			require.Equal(t, 99, edit.MessageID)
			clearedPlaceholder = edit.Text
			return tgbotapi.Message{MessageID: 99}, nil
		},
	}
	done := svc.beginTelegramPreview(42, 99)
	oldInterval := telegramStreamInterval
	telegramStreamInterval = time.Millisecond
	t.Cleanup(func() { telegramStreamInterval = oldInterval })
	go func() {
		svc.streamUpdatesToTelegram(ctx, 42, 99, exec.ID, done)
		close(streamExited)
	}()
	select {
	case <-streamExited:
	case <-time.After(time.Second):
		t.Fatal("expected rich draft stream to exit after terminal execution")
	}

	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}
	svc.SendChatResponse(context.Background(), task, "final rich response", "", 99)

	require.Equal(t, []string{"sendRichMessageDraft", "sendRichMessage"}, endpoints)
	require.Equal(t, "✅ Response sent.", clearedPlaceholder)
}

func TestTelegramService_SendChatResponse_RichDraftPersistsFinalRichMessageBeforeClearingPlaceholder(t *testing.T) {
	var endpoints []string
	var clearedPlaceholder string
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoints = append(endpoints, endpoint)
			require.Equal(t, "42", params["chat_id"])
			require.Contains(t, params["rich_message"], "final rich response")
			switch endpoint {
			case "sendRichMessage":
				return &tgbotapi.APIResponse{Ok: true}, nil
			case "sendRichMessageDraft":
				t.Fatal("final delivery must persist with sendRichMessage, not another draft update")
			case "editMessageText":
				t.Fatal("final delivery must not edit the placeholder with final content when a rich draft is already visible")
			}
			return nil, fmt.Errorf("unexpected endpoint %s", endpoint)
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			edit, ok := c.(tgbotapi.EditMessageTextConfig)
			require.True(t, ok, "expected placeholder cleanup edit after final rich send")
			require.Equal(t, int64(42), edit.ChatID)
			require.Equal(t, 99, edit.MessageID)
			clearedPlaceholder = edit.Text
			return tgbotapi.Message{MessageID: 99}, nil
		},
	}
	done := svc.beginTelegramPreview(42, 99)
	require.True(t, svc.withActiveTelegramPreview(42, 99, done, func(state *telegramPreviewState) bool {
		state.richDraftID = 123
		state.richDraftVisible = true
		return true
	}))
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			svc.finishTelegramPreview(42, 99)
		}
	})
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "final rich response", "", 99)

	require.Equal(t, []string{"sendRichMessage"}, endpoints)
	require.Equal(t, "✅ Response sent.", clearedPlaceholder)
}

func TestTelegramService_SendChatResponse_RichDraftSendsFinalRichMessageInsteadOfEditingPlaceholder(t *testing.T) {
	var endpoints []string
	var clearedPlaceholder string
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoints = append(endpoints, endpoint)
			require.Equal(t, "42", params["chat_id"])
			require.Contains(t, params["rich_message"], "final rich response")
			switch endpoint {
			case "editMessageText":
				t.Fatal("final delivery must not edit the placeholder with final content when a rich draft is already visible")
			case "sendRichMessageDraft":
				t.Fatal("final delivery must persist with sendRichMessage, not another draft update")
			case "sendRichMessage":
				return &tgbotapi.APIResponse{Ok: true}, nil
			}
			return nil, fmt.Errorf("unexpected endpoint %s", endpoint)
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			edit, ok := c.(tgbotapi.EditMessageTextConfig)
			require.True(t, ok, "expected placeholder cleanup edit after final rich send")
			require.Equal(t, int64(42), edit.ChatID)
			require.Equal(t, 99, edit.MessageID)
			clearedPlaceholder = edit.Text
			return tgbotapi.Message{MessageID: 99}, nil
		},
	}
	done := svc.beginTelegramPreview(42, 99)
	require.True(t, svc.withActiveTelegramPreview(42, 99, done, func(state *telegramPreviewState) bool {
		state.richDraftID = 123
		state.richDraftVisible = true
		return true
	}))
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			svc.finishTelegramPreview(42, 99)
		}
	})
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "final rich response", "", 99)

	require.Equal(t, []string{"sendRichMessage"}, endpoints)
	require.Equal(t, "✅ Response sent.", clearedPlaceholder)
}

func TestTelegramService_SendChatResponse_RichDraftFinalSendRejectionSendsLegacyFallbackAndClearsPlaceholder(t *testing.T) {
	var endpoints []string
	var fallbackMessage string
	var clearedPlaceholder string
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoints = append(endpoints, endpoint)
			require.Equal(t, "42", params["chat_id"])
			require.Contains(t, params["rich_message"], "final rich response")
			switch endpoint {
			case "editMessageText":
				t.Fatal("final delivery must not edit the placeholder with final content when a rich draft is already visible")
			case "sendRichMessageDraft":
				t.Fatal("final delivery must persist with sendRichMessage, not another draft update")
			case "sendRichMessage":
				return nil, fmt.Errorf("Bad Request: rich_message unsupported")
			}
			return nil, fmt.Errorf("unexpected endpoint %s", endpoint)
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			switch msg := c.(type) {
			case tgbotapi.MessageConfig:
				require.Equal(t, int64(42), msg.ChatID)
				require.Equal(t, "MarkdownV2", msg.ParseMode)
				fallbackMessage = msg.Text
			case tgbotapi.EditMessageTextConfig:
				require.Equal(t, int64(42), msg.ChatID)
				require.Equal(t, 99, msg.MessageID)
				clearedPlaceholder = msg.Text
			default:
				t.Fatalf("unexpected Telegram config type %T", c)
			}
			return tgbotapi.Message{}, nil
		},
	}
	done := svc.beginTelegramPreview(42, 99)
	require.True(t, svc.withActiveTelegramPreview(42, 99, done, func(state *telegramPreviewState) bool {
		state.richDraftID = 123
		state.richDraftVisible = true
		return true
	}))
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			svc.finishTelegramPreview(42, 99)
		}
	})
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "final rich response", "", 99)

	require.Equal(t, []string{"sendRichMessage"}, endpoints)
	require.Equal(t, `final rich response`, fallbackMessage)
	require.Equal(t, "✅ Response sent.", clearedPlaceholder)
}

func TestTelegramService_SendChatResponse_AmbiguousFinalRichEditErrorDoesNotSendOrEditFallback(t *testing.T) {
	var endpoints []string
	legacyEditCalled := false
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoints = append(endpoints, endpoint)
			require.Equal(t, "editMessageText", endpoint)
			return nil, fmt.Errorf("Post \"https://api.telegram.org\": EOF")
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			legacyEditCalled = true
			return tgbotapi.Message{}, nil
		},
	}
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "hello from initial telegram chat", "", 99)

	require.Equal(t, []string{"editMessageText"}, endpoints)
	require.False(t, legacyEditCalled, "ambiguous final rich edit errors should not send or edit fallback and risk duplicate final output")
}

func TestTelegramService_SendChatResponse_AmbiguousFinalRichSendErrorDoesNotEditFallback(t *testing.T) {
	var endpoints []string
	legacyEditCalled := false
	svc := &TelegramService{
		makeRequestFunc: func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
			endpoints = append(endpoints, endpoint)
			if endpoint == "editMessageText" {
				return nil, fmt.Errorf("Bad Request: rich_message unsupported")
			}
			return nil, fmt.Errorf("Post \"https://api.telegram.org\": EOF")
		},
		sendConfigFunc: func(c tgbotapi.Chattable) (tgbotapi.Message, error) {
			legacyEditCalled = true
			return tgbotapi.Message{}, nil
		},
	}
	task := models.Task{ID: "task-1", Category: models.CategoryChat, CreatedVia: models.TaskOriginTelegram, TelegramChatID: 42}

	svc.SendChatResponse(context.Background(), task, "hello from initial telegram chat", "", 99)

	require.Equal(t, []string{"editMessageText", "sendRichMessage"}, endpoints)
	require.False(t, legacyEditCalled, "ambiguous final rich send errors should not edit fallback and risk duplicate final output")
}

// TestTelegramService_GoalTools_SetGetClearPauseResume verifies that the Telegram
// goal tool handlers call TaskGoalService and return JSON results, not errors.
// This is a regression test: before the fix, all goal tool handlers were stubs
// that always returned "task goal tools are unavailable on Telegram".
func TestTelegramService_GoalTools_SetGetClearPauseResume(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	taskGoalRepo := repository.NewTaskGoalRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	goalSvc := NewTaskGoalService(taskGoalRepo, taskRepo, nil)

	project := &models.Project{Name: "Telegram Goal Test", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "goal-test-task",
		Prompt:    "do something",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	svc := &TelegramService{
		taskSvc:      taskSvc,
		taskRepo:     taskRepo,
		taskGoalSvc:  goalSvc,
		userProjects: make(map[int64]string),
	}

	taskIDJSON, err := json.Marshal(map[string]string{"task_id": task.ID, "goal": "All tests pass"})
	require.NoError(t, err)

	// set_task_goal
	handlers := svc.telegramActionHandlers(project.ID, 12345, 12345, nil)
	out, err := handlers["set_task_goal"](ctx, taskIDJSON)
	require.NoError(t, err, "set_task_goal must not return an error on Telegram")
	require.Contains(t, out, "ok")
	require.Contains(t, out, "All tests pass")

	// get_task_goal
	getInput, _ := json.Marshal(map[string]string{"task_id": task.ID})
	out, err = handlers["get_task_goal"](ctx, getInput)
	require.NoError(t, err, "get_task_goal must not return an error on Telegram")
	require.Contains(t, out, "All tests pass")

	// pause_task_goal
	out, err = handlers["pause_task_goal"](ctx, getInput)
	require.NoError(t, err, "pause_task_goal must not return an error on Telegram")
	require.Contains(t, out, "paused")

	// resume_task_goal
	out, err = handlers["resume_task_goal"](ctx, getInput)
	require.NoError(t, err, "resume_task_goal must not return an error on Telegram")
	require.Contains(t, out, "active")

	// clear_task_goal
	out, err = handlers["clear_task_goal"](ctx, getInput)
	require.NoError(t, err, "clear_task_goal must not return an error on Telegram")
	require.Contains(t, out, "ok")
}

// TestTelegramService_GoalTools_UnavailableWithoutService verifies that when
// taskGoalSvc is nil (not yet wired), goal tools return a descriptive error
// rather than panicking.
func TestTelegramService_GoalTools_UnavailableWithoutService(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	project := &models.Project{Name: "Telegram Goal Nil Svc", RepoPath: "/tmp/test"}
	require.NoError(t, projectRepo.Create(ctx, project))

	svc := &TelegramService{
		taskSvc:      taskSvc,
		taskRepo:     taskRepo,
		taskGoalSvc:  nil, // intentionally nil
		userProjects: make(map[int64]string),
	}

	handlers := svc.telegramActionHandlers(project.ID, 12345, 12345, nil)
	input, _ := json.Marshal(map[string]string{"task_id": "any"})
	for _, name := range []string{"set_task_goal", "clear_task_goal", "get_task_goal", "pause_task_goal", "resume_task_goal", "mark_task_goal_achieved", "report_task_goal_blocked"} {
		_, err := handlers[name](ctx, input)
		require.Error(t, err, "expected error when taskGoalSvc is nil for handler %s", name)
		require.Contains(t, err.Error(), "task goal service unavailable", "handler %s should report service unavailable", name)
	}
}

// TestTelegramService_GoalTools_MarkAchievedReportBlocked verifies that
// mark_task_goal_achieved and report_task_goal_blocked are callable from Telegram
// (previously blocked by the unavailable stub).
func TestTelegramService_GoalTools_MarkAchievedReportBlocked(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	taskGoalRepo := repository.NewTaskGoalRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	goalSvc := NewTaskGoalService(taskGoalRepo, taskRepo, nil)

	project := &models.Project{Name: "Telegram Goal Achieved", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "mark-achieved-task",
		Prompt:    "do something",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	goal, err := goalSvc.SetGoal(ctx, task.ID, "ship it", GoalOptions{Actor: "test"})
	require.NoError(t, err)

	svc := &TelegramService{
		taskSvc:      taskSvc,
		taskRepo:     taskRepo,
		taskGoalSvc:  goalSvc,
		userProjects: make(map[int64]string),
	}

	handlers := svc.telegramActionHandlers(project.ID, 12345, 12345, nil)

	achievedInput, _ := json.Marshal(map[string]string{
		"task_id": task.ID,
		"goal_id": goal.GoalID,
		"reason":  "done",
	})
	out, err := handlers["mark_task_goal_achieved"](ctx, achievedInput)
	require.NoError(t, err, "mark_task_goal_achieved must work on Telegram when goal service is wired")
	require.Contains(t, out, "achieved")
}

func TestTelegramService_SendOutboundMessage_AppliesThreadIDAndSplits(t *testing.T) {
	svc := &TelegramService{}
	var sent []tgbotapi.Params
	svc.makeRequestFunc = func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
		require.Equal(t, "sendMessage", endpoint)
		sent = append(sent, params)
		return &tgbotapi.APIResponse{Ok: true}, nil
	}
	res := svc.SendOutboundMessage(context.Background(), -100123, 42, strings.Repeat("a", maxMessageLength+10))
	require.True(t, res.OK)
	require.Len(t, sent, 2)
	for _, params := range sent {
		require.Equal(t, "42", params["message_thread_id"])
		require.Equal(t, "-100123", params["chat_id"])
	}
}

func TestTelegramService_RuntimeSwitchProject_PersistsToRepo(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	telegramUserProjectRepo := repository.NewTelegramUserProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)

	ctx := context.Background()

	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Beta", RepoPath: "/tmp/beta"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	// telegramAuthRepo=nil means checkAuthorization always returns true.
	svc := &TelegramService{
		taskSvc:                 taskSvc,
		projectRepo:             projectRepo,
		llmConfigRepo:           llmConfigRepo,
		taskRepo:                taskRepo,
		execRepo:                execRepo,
		chatAttachmentRepo:      chatAttachmentRepo,
		settingsRepo:            settingsRepo,
		scheduleRepo:            scheduleRepo,
		threadInputRepo:         repository.NewThreadInputRepo(db),
		telegramUserProjectRepo: telegramUserProjectRepo,
		userProjects:            make(map[int64]string),
	}

	const userID = int64(12345)

	rt := svc.buildTelegramActionToolRuntime(project1.ID, userID, userID, nil)
	require.NotNil(t, rt)

	// Execute switch_project via the runtime tool executor.
	output, handled, isErr, err := rt.Executor(ctx, "switch_project", json.RawMessage(`{"project":"Beta"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, "Beta")

	// Assert persistence: the repo row must exist for userID → project2.
	savedID, err := telegramUserProjectRepo.GetUserProject(ctx, "12345")
	require.NoError(t, err)
	require.Equal(t, project2.ID, savedID, "switch_project must persist selection to telegram_user_projects")

	// Assert getActiveProject reflects the change after a simulated restart (empty in-memory cache).
	svc2 := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: telegramUserProjectRepo,
		userProjects:            make(map[int64]string),
	}
	require.Equal(t, project2.ID, svc2.getActiveProject(userID),
		"getActiveProject must return the newly-persisted project on next session")
}
