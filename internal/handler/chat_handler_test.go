package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_Chat(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Test full page load
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, ">Chat<") {
		t.Error("expected 'Chat' heading in response")
	}
	if !strings.Contains(body, "Test Agent") {
		t.Error("expected agent name in response")
	}
}

func TestHandler_Chat_HTMX(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Test HTMX request (just content)
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, ">Chat<") {
		t.Error("expected 'Chat' heading in HTMX response")
	}
	// HTMX response should not contain full page structure
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("HTMX response should not contain full page structure")
	}
}

func TestHandler_Chat_LayoutNoFixedHeights(t *testing.T) {
	// Regression test: chat-messages must not have fixed min-height/max-height
	// that cause the plan-complete prompt to push the composer out of view.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()

	// The chat-messages container must use flex layout (min-h-0) and NOT have
	// fixed min-height/max-height style attributes that break flex overflow.
	assert.Contains(t, body, `id="chat-messages"`, "chat-messages container must exist")
	assert.NotContains(t, body, "min-height: 500px", "chat-messages must not have fixed min-height")
	assert.NotContains(t, body, "max-height: 700px", "chat-messages must not have fixed max-height")

	// Verify the plan-complete prompt is rendered (hidden by default)
	assert.Contains(t, body, `id="chat-plan-complete-prompt"`, "plan-complete prompt must exist")

	// The chat-messages div should have min-h-0 for proper flex overflow
	assert.Contains(t, body, "min-h-0", "chat-messages must have min-h-0 for flex overflow")
}

func TestHandler_Chat_ModeSelectorBorderlessStyleMatchesModelSelector(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `.chat-model-select,`)
	assert.Contains(t, body, `.chat-mode-select`)
	assert.Contains(t, body, `.chat-model-select:focus,`)
	assert.Contains(t, body, `.chat-mode-select:focus`)
	assert.Contains(t, body, `border: none !important;`)
	assert.Contains(t, body, `box-shadow: none !important;`)
}

func TestHandler_Chat_LightModeToolCallContrastStyles(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `[data-theme="light"] .stream-tool-summary .tool-name-text`)
	assert.Contains(t, body, `color: var(--ov-l-text-strong);`)
	assert.Contains(t, body, `[data-theme="light"] .stream-tool-summary .tool-name-secondary`)
	assert.Contains(t, body, `color: var(--ov-l-text-muted);`)
	assert.Contains(t, body, `[data-theme="light"] .stream-tool-summary .tool-spinner`)
	assert.Contains(t, body, `[data-theme="light"] .stream-tool-body {`)
	assert.Contains(t, body, `border-color: transparent;`)
	assert.Contains(t, body, `background: transparent;`)
	assert.Contains(t, body, `[data-theme="light"] .stream-tool-body-row {`)
	assert.Contains(t, body, `border-top-color: transparent;`)
	assert.Contains(t, body, `[data-theme="light"] .stream-tool-body-content {`)
	assert.Contains(t, body, `background: transparent;`)
	assert.Contains(t, body, `border: none;`)
	assert.Contains(t, body, `[data-theme="light"] .stream-tool-body-content pre`)
	assert.Contains(t, body, `border: 1px solid var(--ov-l-border);`)
	assert.Contains(t, body, `border-radius: 5px;`)
	assert.Contains(t, body, `[data-theme="light"] .tool-status-done`)
	assert.Contains(t, body, `color: var(--ov-l-success);`)
	assert.Contains(t, body, `[data-theme="light"] .tool-status-error`)
	assert.Contains(t, body, `color: var(--ov-l-error);`)
}

func TestHandler_Chat_TaskCreateResultRowStyling(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `.ov-task-result-link {`)
	assert.Contains(t, body, `text-decoration-thickness: 1px;`)
	assert.Contains(t, body, `.ov-task-result-link:focus-visible`)
	assert.Contains(t, body, `.ov-task-result-start-btn {`)
	assert.Contains(t, body, `font-size: 0.75rem;`)
	assert.Contains(t, body, `min-height: 1.9rem;`)
	assert.Contains(t, body, `.ov-task-result-start-btn:disabled,`)
	assert.Contains(t, body, `[data-theme="light"] .ov-task-result-start-btn {`)
	assert.Contains(t, body, `startBtn.className = 'btn btn-xs ov-task-result-start-btn gap-1';`)
	assert.Contains(t, body, `? 'ov-task-result-link ov-task-result-link--tool cursor-pointer'`)
	assert.NotContains(t, body, `? 'text-primary underline decoration-2 underline-offset-2 hover:decoration-2 cursor-pointer font-medium'`)
	assert.NotContains(t, body, `startBtn.className = 'btn btn-xs btn-primary gap-1';`)
}

func TestHandler_ChatSend(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create form data
	form := url.Values{}
	form.Set("message", "Hello, agent!")
	form.Set("agent_id", agent.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true") // Simulate HTMX request
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	// Should contain the user message
	if !strings.Contains(body, "Hello, agent!") {
		t.Error("expected user message in response")
	}
	// Should contain both user and assistant chat bubbles
	if !strings.Contains(body, "chat-bubble-user-msg") && !strings.Contains(body, "chat-bubble-assistant-msg") {
		t.Error("expected chat bubbles in response")
	}
}

func TestHandler_ChatSend_EmptyMessage(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	// Create form data with empty message
	form := url.Values{}
	form.Set("message", "")
	form.Set("agent_id", "test-agent-id")

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandler_ChatSend_NoModelsConfiguredHTMX(t *testing.T) {
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

	form := url.Values{}
	form.Set("message", "Hello without model")
	form.Set("agent_id", "auto")

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
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
	if !strings.Contains(hxTrigger, noModelsConfiguredLinkText) {
		t.Fatalf("expected HX-Trigger to contain models link text %q, got %q", noModelsConfiguredLinkText, hxTrigger)
	}

	chatHistory, err := h.execRepo.ListChatHistory(ctx, "default", 50)
	if err != nil {
		t.Fatalf("failed to list chat history: %v", err)
	}
	if len(chatHistory) != 0 {
		t.Fatalf("expected no chat history entries when no models configured, got %d", len(chatHistory))
	}
}

func TestHandler_ChatMessageWithExec_GeneratesCorrectHTML(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create form data
	form := url.Values{}
	form.Set("message", "Test message")
	form.Set("agent_id", agent.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Should contain data-exec-id attribute
	if !strings.Contains(body, "data-exec-id=") {
		t.Error("expected data-exec-id attribute in streaming message div")
	}

	// Should contain SSE connection code
	if !strings.Contains(body, "EventSource") {
		t.Error("expected EventSource code for SSE streaming")
	}

	// Should contain the SSE endpoint path
	if !strings.Contains(body, "/events/chat/") {
		t.Error("expected SSE endpoint path in JavaScript")
	}

	// Should read exec ID from data attribute
	if !strings.Contains(body, "getAttribute('data-exec-id')") {
		t.Error("expected JavaScript to read data-exec-id attribute")
	}
}

func TestHandler_ChatSend_CreatesTaskWithChatCategory(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create form data
	form := url.Values{}
	form.Set("message", "Test chat message")
	form.Set("agent_id", agent.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify the task was created with CategoryChat
	tasks, err := h.taskSvc.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}

	// Find the chat task
	var chatTask *models.Task
	for _, task := range tasks {
		if strings.HasPrefix(task.Title, "Chat ") {
			chatTask = &task
			break
		}
	}

	if chatTask == nil {
		t.Fatal("expected chat task to be created")
	}

	if chatTask.Category != models.CategoryChat {
		t.Errorf("expected chat task category to be %s, got %s", models.CategoryChat, chatTask.Category)
	}

	// Verify that CategoryChat is not in AllCategories (so it won't appear in kanban board)
	for _, cat := range models.AllCategories {
		if cat == models.CategoryChat {
			t.Error("CategoryChat should not be in AllCategories to prevent it from appearing in kanban board")
		}
	}
}

func TestWriteSSEData_SingleLine(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	writeSSEData(c, "Hello world")

	body := rec.Body.String()
	expected := "data: Hello world\n\n"
	if body != expected {
		t.Errorf("expected %q, got %q", expected, body)
	}
}

func TestWriteSSEData_MultiLine(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	writeSSEData(c, "Line 1\nLine 2\nLine 3")

	body := rec.Body.String()
	expected := "data: Line 1\ndata: Line 2\ndata: Line 3\n\n"
	if body != expected {
		t.Errorf("expected %q, got %q", expected, body)
	}
}

func TestWriteSSEData_EmptyString(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	writeSSEData(c, "")

	body := rec.Body.String()
	expected := "data: \n\n"
	if body != expected {
		t.Errorf("expected %q, got %q", expected, body)
	}
}

func TestWriteSSEData_TrailingNewline(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	writeSSEData(c, "Content with trailing newline\n")

	body := rec.Body.String()
	// "Content with trailing newline\n" splits into ["Content with trailing newline", ""]
	expected := "data: Content with trailing newline\ndata: \n\n"
	if body != expected {
		t.Errorf("expected %q, got %q", expected, body)
	}
}

func TestHandler_Chat_LoadsRunningExecutions(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a chat task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Chat: Test message",
		Prompt:    "Test message",
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a running execution with partial output
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Test message",
		Output:        "This is a partial response...",
	}
	err = h.execRepo.Create(ctx, exec)
	if err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// Simulate partial output being written
	err = h.execRepo.UpdateOutput(ctx, exec.ID, "This is a partial response...")
	if err != nil {
		t.Fatalf("failed to update output: %v", err)
	}

	// Load the chat page
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Should show the user message
	if !strings.Contains(body, "Test message") {
		t.Error("expected user message in chat history")
	}

	// Should show the partial response
	if !strings.Contains(body, "This is a partial response...") {
		t.Error("expected partial response in chat history")
	}

	// Should reconnect SSE stream for running execution
	if !strings.Contains(body, "data-exec-id") {
		t.Error("expected data-exec-id for running execution")
	}

	// Should contain SSE reconnection code
	if !strings.Contains(body, "EventSource") {
		t.Error("expected EventSource for SSE reconnection")
	}

	// Should track initial length to avoid duplication
	if !strings.Contains(body, "data-initial-length") {
		t.Error("expected data-initial-length attribute to track partial content")
	}
	if !strings.Contains(body, `id="streaming-dots-resume-`+exec.ID+`"`) {
		t.Error("expected streaming resume dots container for running execution")
	}
	if !strings.Contains(body, `id="streaming-dots-resume-`+exec.ID+`" class="flex items-center gap-1 mt-2 opacity-40"`) {
		t.Error("expected streaming resume dots to be visible for partial running output")
	}
	if strings.Contains(body, `id="streaming-dots-resume-`+exec.ID+`" class="hidden`) {
		t.Error("expected streaming resume dots not to be hidden when partial output exists")
	}

	// Loading placeholder should not render literal text status message.
	if strings.Contains(body, "Thinking...") {
		t.Error("expected running placeholder to avoid literal 'Thinking...' text")
	}
	if !strings.Contains(body, "ov-loading-dots ov-loading-dots-sm") {
		t.Error("expected running placeholder to keep loading dots indicator")
	}
	if !strings.Contains(body, "class=\"block h-5\" aria-hidden=\"true\"") {
		t.Error("expected running placeholder to preserve layout spacer height")
	}
}

func TestHandler_ChatSend_StoresMessageInCorrectProject(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a second project (in addition to the default one)
	project2 := &models.Project{
		Name:        "Test Project 2",
		Description: "Second test project",
		RepoPath:    "/tmp/test2",
		IsDefault:   false,
	}
	err := h.projectSvc.Create(ctx, project2)
	if err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create an agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err = llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Send a chat message with project_id=project2.ID
	form := url.Values{}
	form.Set("message", "Message for project 2")
	form.Set("agent_id", agent.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+project2.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify that the chat task was created in project2
	tasks, err := h.taskRepo.ListByProject(ctx, project2.ID, "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}

	var chatTask *models.Task
	for i := range tasks {
		if tasks[i].Category == models.CategoryChat {
			chatTask = &tasks[i]
			break
		}
	}

	if chatTask == nil {
		t.Fatal("expected chat task to be created in project2")
	}

	if chatTask.ProjectID != project2.ID {
		t.Errorf("expected task to be in project2 (%s), got %s", project2.ID, chatTask.ProjectID)
	}

	if chatTask.Prompt != "Message for project 2" {
		t.Errorf("expected prompt 'Message for project 2', got %q", chatTask.Prompt)
	}

	// Verify the chat history for project2 includes this message
	chatHistory, err := h.execRepo.ListChatHistory(ctx, project2.ID, 50)
	if err != nil {
		t.Fatalf("failed to list chat history: %v", err)
	}

	if len(chatHistory) != 1 {
		t.Errorf("expected 1 chat message in project2, got %d", len(chatHistory))
	}

	if len(chatHistory) > 0 && chatHistory[0].PromptSent != "Message for project 2" {
		t.Errorf("expected chat history to contain 'Message for project 2', got %q", chatHistory[0].PromptSent)
	}

	// Verify the default project does NOT have this message
	defaultProjects, err := h.projectSvc.List(ctx)
	if err != nil || len(defaultProjects) == 0 {
		t.Fatalf("failed to get default project: %v", err)
	}
	defaultProject := defaultProjects[0] // First project is default due to ORDER BY is_default DESC

	defaultChatHistory, err := h.execRepo.ListChatHistory(ctx, defaultProject.ID, 50)
	if err != nil {
		t.Fatalf("failed to list default project chat history: %v", err)
	}

	for _, exec := range defaultChatHistory {
		if exec.PromptSent == "Message for project 2" {
			t.Error("message should NOT be in default project's chat history")
		}
	}
}

func TestHandler_ClearChat(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Get default project
	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create chat tasks with executions
	chatTask1 := &models.Task{
		ProjectID: projectID,
		Title:     "Chat 1: hello",
		Prompt:    "hello",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, chatTask1)
	if err != nil {
		t.Fatalf("failed to create chat task 1: %v", err)
	}

	exec1 := &models.Execution{
		TaskID:        chatTask1.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "hello",
	}
	err = h.execRepo.Create(ctx, exec1)
	if err != nil {
		t.Fatalf("failed to create execution 1: %v", err)
	}

	chatTask2 := &models.Task{
		ProjectID: projectID,
		Title:     "Chat 2: world",
		Prompt:    "world",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, chatTask2)
	if err != nil {
		t.Fatalf("failed to create chat task 2: %v", err)
	}

	exec2 := &models.Execution{
		TaskID:        chatTask2.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "world",
	}
	err = h.execRepo.Create(ctx, exec2)
	if err != nil {
		t.Fatalf("failed to create execution 2: %v", err)
	}

	// Verify chat history exists
	history, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	if len(history) != 2 {
		t.Fatalf("expected 2 chat messages before clear, got %d", len(history))
	}

	// Call ClearChat endpoint
	req := httptest.NewRequest(http.MethodDelete, "/chat/history?project_id="+projectID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify chat history is empty
	history, _ = h.execRepo.ListChatHistory(ctx, projectID, 50)
	if len(history) != 0 {
		t.Errorf("expected 0 chat messages after clear, got %d", len(history))
	}

	// Verify the response contains empty chat content
	body := rec.Body.String()
	if !strings.Contains(body, ">Chat<") {
		t.Error("expected chat page heading in response")
	}
	if !strings.Contains(body, "Start a conversation") {
		t.Error("expected empty state message in response")
	}
}

func TestHandler_ClearChat_OnlyDeletesCurrentProject(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Get default project and create a second project
	projects, _ := h.projectSvc.List(ctx)
	project1ID := projects[0].ID

	project2 := &models.Project{
		Name:        "Project 2",
		Description: "Second project",
		RepoPath:    "/tmp/test2",
	}
	err = h.projectSvc.Create(ctx, project2)
	if err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create chat task in project1
	chatTask1 := &models.Task{
		ProjectID: project1ID,
		Title:     "Chat P1: hello",
		Prompt:    "hello",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	h.taskRepo.Create(ctx, chatTask1)
	h.execRepo.Create(ctx, &models.Execution{
		TaskID:        chatTask1.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "hello",
	})

	// Create chat task in project2
	chatTask2 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Chat P2: world",
		Prompt:    "world",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	h.taskRepo.Create(ctx, chatTask2)
	h.execRepo.Create(ctx, &models.Execution{
		TaskID:        chatTask2.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "world",
	})

	// Clear chat for project1 only
	req := httptest.NewRequest(http.MethodDelete, "/chat/history?project_id="+project1ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify project1 chat history is empty
	history1, _ := h.execRepo.ListChatHistory(ctx, project1ID, 50)
	if len(history1) != 0 {
		t.Errorf("expected 0 chat messages in project1 after clear, got %d", len(history1))
	}

	// Verify project2 chat history is NOT affected
	history2, _ := h.execRepo.ListChatHistory(ctx, project2.ID, 50)
	if len(history2) != 1 {
		t.Errorf("expected 1 chat message in project2 after clear, got %d", len(history2))
	}
}

func TestHandler_ChatSend_FiltersRunningExecutionsFromHistory(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Get default project
	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create a completed chat exchange (this should be included in history)
	completedTask := &models.Task{
		ProjectID: projectID,
		Title:     "Chat: Completed question",
		Prompt:    "What is Go?",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, completedTask)
	if err != nil {
		t.Fatalf("failed to create completed task: %v", err)
	}
	completedExec := &models.Execution{
		TaskID:        completedTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "What is Go?",
		Output:        "Go is a programming language.",
	}
	err = h.execRepo.Create(ctx, completedExec)
	if err != nil {
		t.Fatalf("failed to create completed execution: %v", err)
	}

	// Create a RUNNING chat exchange (this should be EXCLUDED from history)
	// This simulates a previous message still being processed by the agent
	runningTask := &models.Task{
		ProjectID: projectID,
		Title:     "Chat: Running question",
		Prompt:    "What is COBOL?",
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, runningTask)
	if err != nil {
		t.Fatalf("failed to create running task: %v", err)
	}
	runningExec := &models.Execution{
		TaskID:        runningTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "What is COBOL?",
		Output:        "", // No output yet - still running
	}
	err = h.execRepo.Create(ctx, runningExec)
	if err != nil {
		t.Fatalf("failed to create running execution: %v", err)
	}

	// Send a new message - the running execution should be filtered from history
	form := url.Values{}
	form.Set("message", "Tell me about task chaining")
	form.Set("agent_id", agent.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+projectID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the full chat history includes all 3 executions (completed + running + new)
	allHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	if len(allHistory) != 3 {
		t.Errorf("expected 3 total executions in DB, got %d", len(allHistory))
	}

	// Verify the running execution still exists (it should not be deleted)
	runningFound := false
	for _, exec := range allHistory {
		if exec.Status == models.ExecRunning && exec.PromptSent == "What is COBOL?" {
			runningFound = true
		}
	}
	if !runningFound {
		t.Error("running execution should still exist in DB")
	}
}

func TestHandler_ClearChat_CancelsRunningGoroutines(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Get default project
	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create a running chat task (simulates agent still processing)
	runningTask := &models.Task{
		ProjectID: projectID,
		Title:     "Chat: Running question",
		Prompt:    "What is COBOL?",
		Status:    models.StatusRunning,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, runningTask)
	if err != nil {
		t.Fatalf("failed to create running task: %v", err)
	}

	runningExec := &models.Execution{
		TaskID:        runningTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "What is COBOL?",
	}
	err = h.execRepo.Create(ctx, runningExec)
	if err != nil {
		t.Fatalf("failed to create running execution: %v", err)
	}

	// Register a cancel function for the running task (simulates what
	// processAgentResponseStreaming does when it starts)
	cancelled := false
	h.workerSvc.RegisterCancel(runningTask.ID, func() {
		cancelled = true
	})

	// Call ClearChat
	req := httptest.NewRequest(http.MethodDelete, "/chat/history?project_id="+projectID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify the cancel function was called
	if !cancelled {
		t.Error("expected running chat task's cancel function to be called during ClearChat")
	}

	// Verify all chat history is deleted
	history, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	if len(history) != 0 {
		t.Errorf("expected 0 chat messages after clear, got %d", len(history))
	}
}

func TestTaskRepo_ListRunningChatTaskIDs(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Get default project
	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create a running chat task
	runningChat := &models.Task{
		ProjectID: projectID,
		Title:     "Chat: Running",
		Prompt:    "running question",
		Status:    models.StatusRunning,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	h.taskRepo.Create(ctx, runningChat)

	// Create a pending chat task
	pendingChat := &models.Task{
		ProjectID: projectID,
		Title:     "Chat: Pending",
		Prompt:    "pending question",
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	h.taskRepo.Create(ctx, pendingChat)

	// Create a completed chat task (should NOT be returned)
	completedChat := &models.Task{
		ProjectID: projectID,
		Title:     "Chat: Completed",
		Prompt:    "completed question",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	h.taskRepo.Create(ctx, completedChat)

	// Create a running non-chat task (should NOT be returned)
	runningActive := &models.Task{
		ProjectID: projectID,
		Title:     "Active running task",
		Prompt:    "do something",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
		AgentID:   &agent.ID,
	}
	h.taskRepo.Create(ctx, runningActive)

	// Query running chat task IDs
	ids, err := h.taskRepo.ListRunningChatTaskIDs(ctx, projectID)
	if err != nil {
		t.Fatalf("ListRunningChatTaskIDs failed: %v", err)
	}

	// Should return running + pending chat tasks only
	if len(ids) != 2 {
		t.Errorf("expected 2 running/pending chat tasks, got %d", len(ids))
	}

	// Verify the IDs match the running and pending chat tasks
	foundRunning := false
	foundPending := false
	for _, id := range ids {
		if id == runningChat.ID {
			foundRunning = true
		}
		if id == pendingChat.ID {
			foundPending = true
		}
	}
	if !foundRunning {
		t.Error("expected running chat task ID in results")
	}
	if !foundPending {
		t.Error("expected pending chat task ID in results")
	}
}

func TestHandler_Chat_TaskLinksConvertedOnHTMXNavigation(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Create a chat task with a completed execution that has task ID markers
	task := &models.Task{
		ProjectID: "default",
		Title:     "Chat: Create a task",
		Prompt:    "Create a task for me",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task)
	require.NoError(t, err)

	// Create an execution and complete it with task ID markers in the output
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Create a task for me",
	}
	err = h.execRepo.Create(ctx, exec)
	require.NoError(t, err)

	outputWithMarkers := "I created a task for you.\n\n---\nCreated 1 task(s):\n- \"Build API endpoint\" (backlog) [TASK_ID:abc123def456]"
	err = h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, outputWithMarkers, "", 100, 1000)
	require.NoError(t, err)

	// Test HTMX navigation (simulates clicking Chat in sidebar)
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()

	// The output should contain the task ID marker text (for JS to convert)
	assert.Contains(t, body, "[TASK_ID:abc123def456]")
	assert.Contains(t, body, "Build API endpoint")

	// The JavaScript should use setTimeout for immediate execution (not DOMContentLoaded)
	// This ensures task links are converted even on HTMX navigation
	assert.Contains(t, body, "setTimeout(function()")
	assert.Contains(t, body, "convertTaskLinksInMessage(bubble)")
	assert.Contains(t, body, "convertTaskEditLinksInMessage(bubble)")

	// Should NOT rely on DOMContentLoaded for converting links (it doesn't fire on HTMX swap)
	assert.NotContains(t, body, "document.addEventListener('DOMContentLoaded'")
	assert.NotContains(t, body, `document.addEventListener("DOMContentLoaded"`)

	// Should guard against duplicate event listener registration on repeated HTMX navigations
	assert.Contains(t, body, "_chatSwapHandlerAttached")
}

func TestHandler_Chat_PlanModeRefreshPreservesToolCallHistoryRendering(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// First chat turn with persisted tool-call markers in assistant output.
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Chat: First turn",
		Prompt:    "first",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task1)
	require.NoError(t, err)

	exec1 := &models.Execution{
		TaskID:        task1.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Inspect repository",
	}
	err = h.execRepo.Create(ctx, exec1)
	require.NoError(t, err)
	err = h.execRepo.Complete(ctx, exec1.ID, models.ExecCompleted, "[Using tool: read_file | /tmp/a.go]\n[Tool read_file done]\npackage main\n[/Tool]\nSummary", "", 10, 50)
	require.NoError(t, err)

	// Second turn confirms chronological order is preserved and not duplicated on reload.
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Chat: Second turn",
		Prompt:    "second",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task2)
	require.NoError(t, err)

	exec2 := &models.Execution{
		TaskID:        task2.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Follow up",
	}
	err = h.execRepo.Create(ctx, exec2)
	require.NoError(t, err)
	err = h.execRepo.Complete(ctx, exec2.ID, models.ExecCompleted, "Second response", "", 8, 40)
	require.NoError(t, err)

	// Simulate hard refresh render path (full page request).
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Refresh rendering path must preserve persisted tool-call history cards.
	assert.NotContains(t, body, "function shouldHidePlanToolCard(seg)")

	// Server history payload includes both turns once and in chronological order.
	firstIdx := strings.Index(body, "Inspect repository")
	secondIdx := strings.Index(body, "Follow up")
	require.NotEqual(t, -1, firstIdx)
	require.NotEqual(t, -1, secondIdx)
	assert.Less(t, firstIdx, secondIdx, "chat history should remain chronological after reload")
	assert.Equal(t, 1, strings.Count(body, "Inspect repository"), "first turn should not be duplicated on refresh")
	assert.Equal(t, 1, strings.Count(body, "Follow up"), "second turn should not be duplicated on refresh")
}

func TestHandler_Chat_NoLegacyDialogContainer(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Create a chat task with task creation output
	task := &models.Task{
		ProjectID: "default",
		Title:     "Chat: Create tasks",
		Prompt:    "Create some tasks",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task)
	require.NoError(t, err)

	// Create an execution with task markers
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "Create some tasks",
		Output:        "Created tasks:\n- \"Task One\" (active) [TASK_ID:task1]\n- \"Task Two\" (updated: title, priority) [TASK_EDITED:task2]",
	}
	err = h.execRepo.Create(ctx, exec)
	require.NoError(t, err)

	// Get the chat page
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Verify no legacy dialog container (task detail is now full page)
	assert.NotContains(t, body, `id="task-dialog-container"`)
	assert.NotContains(t, body, `task_detail_modal`)
	assert.NotContains(t, body, `_taskDialogDismissed`)

	// Verify task link conversion JS still exists (navigates to full page now)
	assert.Contains(t, body, `convertTaskLinksInMessage`)
}

func TestIsImageFile(t *testing.T) {
	tests := []struct {
		filename string
		expected bool
	}{
		{"photo.png", true},
		{"photo.PNG", true},
		{"photo.jpg", true},
		{"photo.jpeg", true},
		{"photo.gif", true},
		{"photo.webp", true},
		{"document.txt", false},
		{"data.json", false},
		{"code.go", false},
		{"archive.zip", false},
		{"document.pdf", false},
		{"image.svg", false}, // SVG not supported by Anthropic multimodal API
		{"noext", false},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			result := isImageFile(tc.filename)
			assert.Equal(t, tc.expected, result, "isImageFile(%q)", tc.filename)
		})
	}
}

func TestMediaTypeFromExtension(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"photo.png", "image/png"},
		{"photo.PNG", "image/png"},
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"photo.gif", "image/gif"},
		{"photo.webp", "image/webp"},
		{"document.txt", "text/plain"},
		{"data.json", "application/json"},
		{"config.yaml", "text/x-yaml"},
		{"main.go", "text/x-go"},
		{"script.py", "text/x-python"},
		{"noext", "text/plain"},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			result := mediaTypeFromExtension(tc.filename)
			assert.Equal(t, tc.expected, result, "mediaTypeFromExtension(%q)", tc.filename)
		})
	}
}

func TestProcessAttachments_ImageFilesReturnedAsSeparateAttachments(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create necessary DB records (chat_attachments has FK to executions)
	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-3-sonnet-20240229", APIKey: "test-key",
		MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	task := &models.Task{
		ProjectID: projects[0].ID, Title: "Chat: test",
		Prompt: "test", Status: models.StatusPending,
		Category: models.CategoryChat, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	// Set up temporary uploads directory
	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "test-session-123"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))

	// Create a fake PNG file (binary content)
	pngContent := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG magic bytes
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "screenshot.png"), pngContent, 0644))

	// Create a text file
	textContent := "Hello, this is a test file."
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "notes.txt"), []byte(textContent), 0644))

	textContext, imageAttachments, err := h.processAttachments(ctx, sessionID, exec.ID)
	require.NoError(t, err)

	// Text file should be included in text context
	assert.Contains(t, textContext, "notes.txt")
	assert.Contains(t, textContext, textContent)

	// Image file should NOT be in text context (this was the bug - binary content was injected as text)
	assert.NotContains(t, textContext, "screenshot.png")

	// Image file should be returned as a separate attachment for multimodal handling
	require.Len(t, imageAttachments, 1)
	assert.Equal(t, "screenshot.png", imageAttachments[0].FileName)
	assert.Equal(t, "image/png", imageAttachments[0].MediaType)
}

func TestProcessAttachments_LargeTextFileNotIncludedInline(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create necessary DB records
	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-3-sonnet-20240229", APIKey: "test-key",
		MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	task := &models.Task{
		ProjectID: projects[0].ID, Title: "Chat: large test",
		Prompt: "test", Status: models.StatusPending,
		Category: models.CategoryChat, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "test-session-large"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))

	// Create a text file larger than maxTextAttachmentSize
	largeContent := make([]byte, maxTextAttachmentSize+1)
	for i := range largeContent {
		largeContent[i] = 'x'
	}
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "large.txt"), largeContent, 0644))

	textContext, imageAttachments, err := h.processAttachments(ctx, sessionID, exec.ID)
	require.NoError(t, err)

	// Large file should be mentioned but content should NOT be included
	assert.Contains(t, textContext, "large.txt")
	assert.Contains(t, textContext, "too large to include inline")
	assert.NotContains(t, textContext, string(largeContent[:50]))

	// No image attachments
	assert.Empty(t, imageAttachments)
}

func TestProcessAttachments_NoSession(t *testing.T) {
	h, _, _ := setupTestHandler(t)

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	ctx := context.Background()
	textContext, imageAttachments, err := h.processAttachments(ctx, "nonexistent-session", "exec-id")
	require.NoError(t, err)
	assert.Empty(t, textContext)
	assert.Nil(t, imageAttachments)
}

func TestProcessAttachments_MultipleImageTypes(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create necessary DB records
	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-3-sonnet-20240229", APIKey: "test-key",
		MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	task := &models.Task{
		ProjectID: projects[0].ID, Title: "Chat: multi img",
		Prompt: "test", Status: models.StatusPending,
		Category: models.CategoryChat, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "test-session-multi"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))

	// Create various image files
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "photo.jpg"), []byte{0xFF, 0xD8, 0xFF}, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "icon.gif"), []byte("GIF89a"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "banner.webp"), []byte("RIFF"), 0644))

	textContext, imageAttachments, err := h.processAttachments(ctx, sessionID, exec.ID)
	require.NoError(t, err)

	// No text context for image-only attachments
	assert.Empty(t, textContext)

	// All 3 should be returned as image attachments
	require.Len(t, imageAttachments, 3)

	// Verify correct media types
	mediaTypes := map[string]string{}
	for _, att := range imageAttachments {
		mediaTypes[att.FileName] = att.MediaType
	}
	assert.Equal(t, "image/jpeg", mediaTypes["photo.jpg"])
	assert.Equal(t, "image/gif", mediaTypes["icon.gif"])
	assert.Equal(t, "image/webp", mediaTypes["banner.webp"])
}

func TestCopyChatAttachmentsToTask(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create necessary DB records
	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-3-sonnet-20240229", APIKey: "test-key",
		MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)

	// Create a chat task
	chatTask := &models.Task{
		ProjectID: projects[0].ID, Title: "Chat: test",
		Prompt: "test", Status: models.StatusPending,
		Category: models.CategoryChat, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))

	// Create execution for chat
	exec := &models.Execution{
		TaskID: chatTask.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create chat attachments
	chatDir := filepath.Join(tmpDir, "chat", exec.ID)
	require.NoError(t, os.MkdirAll(chatDir, 0755))

	// Create test files
	testFile1 := filepath.Join(chatDir, "test1.txt")
	testFile2 := filepath.Join(chatDir, "test2.png")
	require.NoError(t, os.WriteFile(testFile1, []byte("test content 1"), 0644))
	require.NoError(t, os.WriteFile(testFile2, []byte{0x89, 0x50, 0x4E, 0x47}, 0644))

	// Create chat attachment records
	chatAtt1 := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "test1.txt",
		FilePath:    testFile1,
		MediaType:   "text/plain",
		FileSize:    14,
	}
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, chatAtt1))

	chatAtt2 := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "test2.png",
		FilePath:    testFile2,
		MediaType:   "image/png",
		FileSize:    4,
	}
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, chatAtt2))

	// Create a regular task to copy attachments to
	regularTask := &models.Task{
		ProjectID: projects[0].ID, Title: "Task created from chat",
		Prompt: "test task", Status: models.StatusPending,
		Category: models.CategoryBacklog, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, regularTask))

	// Copy attachments from chat execution to task
	copiedCount, err := h.copyChatAttachmentsToTask(ctx, exec.ID, regularTask.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, copiedCount)

	// Verify task attachments were created
	taskAttachments, err := h.attachmentRepo.ListByTask(ctx, regularTask.ID)
	require.NoError(t, err)
	require.Len(t, taskAttachments, 2)

	// Verify files were copied
	taskDir := filepath.Join(tmpDir, "tasks", regularTask.ID)
	assert.FileExists(t, filepath.Join(taskDir, "test1.txt"))
	assert.FileExists(t, filepath.Join(taskDir, "test2.png"))

	// Verify file contents
	content1, err := os.ReadFile(filepath.Join(taskDir, "test1.txt"))
	require.NoError(t, err)
	assert.Equal(t, "test content 1", string(content1))

	// Verify attachment metadata
	byFilename := make(map[string]models.Attachment)
	for _, att := range taskAttachments {
		byFilename[att.FileName] = att
	}

	assert.Equal(t, regularTask.ID, byFilename["test1.txt"].TaskID)
	assert.Equal(t, "text/plain", byFilename["test1.txt"].MediaType)
	assert.Equal(t, int64(14), byFilename["test1.txt"].FileSize)

	assert.Equal(t, regularTask.ID, byFilename["test2.png"].TaskID)
	assert.Equal(t, "image/png", byFilename["test2.png"].MediaType)
	assert.Equal(t, int64(4), byFilename["test2.png"].FileSize)

	// Verify task prompt was updated with absolute file paths
	updatedTask, err := h.taskRepo.GetByID(ctx, regularTask.ID)
	require.NoError(t, err)
	assert.Contains(t, updatedTask.Prompt, "[Attached files from chat:")
	assert.Contains(t, updatedTask.Prompt, "test1.txt (path: ")
	assert.Contains(t, updatedTask.Prompt, "test2.png (path: ")
	// Verify paths are absolute (start with tmpDir since uploadsDir was set to tmpDir)
	assert.Contains(t, updatedTask.Prompt, filepath.Join(tmpDir, "tasks", regularTask.ID, "test1.txt"))
	assert.Contains(t, updatedTask.Prompt, filepath.Join(tmpDir, "tasks", regularTask.ID, "test2.png"))

	// Verify attachment file paths stored in DB are absolute
	assert.True(t, filepath.IsAbs(byFilename["test1.txt"].FilePath), "test1.txt file path should be absolute, got: %s", byFilename["test1.txt"].FilePath)
	assert.True(t, filepath.IsAbs(byFilename["test2.png"].FilePath), "test2.png file path should be absolute, got: %s", byFilename["test2.png"].FilePath)
}

func TestCopyChatAttachmentsToTask_AbsolutePathsAccessible(t *testing.T) {
	// This test verifies the core fix: attachment file paths are absolute
	// so they can be accessed by the task execution agent even when it
	// runs from a different working directory (e.g., the project's repo path).
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-3-sonnet-20240229", APIKey: "test-key",
		MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)

	chatTask := &models.Task{
		ProjectID: projects[0].ID, Title: "Chat: screenshot test",
		Prompt: "What do you see in the screenshot?", Status: models.StatusPending,
		Category: models.CategoryChat, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))

	exec := &models.Execution{
		TaskID: chatTask.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "What do you see in the screenshot?",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create a chat attachment (simulating a screenshot upload)
	chatDir := filepath.Join(tmpDir, "chat", exec.ID)
	require.NoError(t, os.MkdirAll(chatDir, 0755))

	screenshotPath := filepath.Join(chatDir, "screenshot.png")
	screenshotData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG magic bytes
	require.NoError(t, os.WriteFile(screenshotPath, screenshotData, 0644))

	chatAtt := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "screenshot.png",
		FilePath:    screenshotPath,
		MediaType:   "image/png",
		FileSize:    int64(len(screenshotData)),
	}
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, chatAtt))

	// Create a task that was spawned from the chat conversation
	task := &models.Task{
		ProjectID: projects[0].ID, Title: "Analyze screenshot",
		Prompt: "Analyze the attached screenshot", Status: models.StatusPending,
		Category: models.CategoryActive, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	// Copy attachments from chat to task
	copiedCount, err := h.copyChatAttachmentsToTask(ctx, exec.ID, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, copiedCount)

	// Verify the file was copied and exists at an absolute path
	taskAttachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, taskAttachments, 1)

	att := taskAttachments[0]
	// The file path should be absolute
	assert.True(t, filepath.IsAbs(att.FilePath), "attachment path should be absolute, got: %s", att.FilePath)

	// The file should exist and be readable at the stored path
	data, err := os.ReadFile(att.FilePath)
	require.NoError(t, err, "should be able to read file at stored path: %s", att.FilePath)
	assert.Equal(t, screenshotData, data)

	// Verify task prompt includes absolute path
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Contains(t, updatedTask.Prompt, att.FilePath, "task prompt should contain the absolute file path")
}

func TestCopyChatAttachmentsToTask_NoAttachments(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create necessary DB records
	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-3-sonnet-20240229", APIKey: "test-key",
		MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)

	// Create a chat task
	chatTask := &models.Task{
		ProjectID: projects[0].ID, Title: "Chat: test",
		Prompt: "test", Status: models.StatusPending,
		Category: models.CategoryChat, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))

	// Create execution for chat (no attachments)
	exec := &models.Execution{
		TaskID: chatTask.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	// Create a regular task
	regularTask := &models.Task{
		ProjectID: projects[0].ID, Title: "Task created from chat",
		Prompt: "test task", Status: models.StatusPending,
		Category: models.CategoryBacklog, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, regularTask))

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Try to copy (should be 0 since no attachments exist)
	copiedCount, err := h.copyChatAttachmentsToTask(ctx, exec.ID, regularTask.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, copiedCount)

	// Verify no task attachments were created
	taskAttachments, err := h.attachmentRepo.ListByTask(ctx, regularTask.ID)
	require.NoError(t, err)
	assert.Empty(t, taskAttachments)
}

// TestCopyChatAttachmentsToTask_DeferredActivation verifies the fix for the race
// condition where a task created from chat with category "active" would start
// executing before attachments were copied. The fix creates the task as "backlog"
// first, copies attachments, then activates it via UpdateCategory.
func TestCopyChatAttachmentsToTask_DeferredActivation(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-3-sonnet-20240229", APIKey: "test-key",
		MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)

	// Create a chat task and execution
	chatTask := &models.Task{
		ProjectID: projects[0].ID, Title: "Chat: deferred test",
		Prompt: "test", Status: models.StatusPending,
		Category: models.CategoryChat, AgentID: &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))

	exec := &models.Execution{
		TaskID: chatTask.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create a chat attachment (simulating user uploading screenshot in chat)
	chatDir := filepath.Join(tmpDir, "chat", exec.ID)
	require.NoError(t, os.MkdirAll(chatDir, 0755))
	testFile := filepath.Join(chatDir, "screenshot.png")
	require.NoError(t, os.WriteFile(testFile, []byte{0x89, 0x50, 0x4E, 0x47}, 0644))

	chatAtt := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "screenshot.png",
		FilePath:    testFile,
		MediaType:   "image/png",
		FileSize:    4,
	}
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, chatAtt))

	// Step 1: Simulate deferred creation — task originally wanted "active" but
	// is created as "backlog" to prevent auto-submission before attachments are copied.
	deferredTask := &models.Task{
		ProjectID: projects[0].ID, Title: "Deferred active task",
		Prompt: "update styling based on screenshot", Status: models.StatusPending,
		Category: models.CategoryBacklog, // Temporarily backlog (was "active")
		AgentID:  &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, deferredTask))

	// Verify task is in backlog (not yet executing)
	task, err := h.taskRepo.GetByID(ctx, deferredTask.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CategoryBacklog, task.Category)

	// Step 2: Copy attachments while task is still in backlog
	copiedCount, err := h.copyChatAttachmentsToTask(ctx, exec.ID, deferredTask.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, copiedCount)

	// Verify attachments were copied BEFORE activation
	taskAttachments, err := h.attachmentRepo.ListByTask(ctx, deferredTask.ID)
	require.NoError(t, err)
	require.Len(t, taskAttachments, 1)
	assert.Equal(t, "screenshot.png", taskAttachments[0].FileName)

	// Verify the task prompt includes the attachment reference
	task, err = h.taskRepo.GetByID(ctx, deferredTask.ID)
	require.NoError(t, err)
	assert.Contains(t, task.Prompt, "[Attached files from chat:")
	assert.Contains(t, task.Prompt, "screenshot.png (path: ")

	// Verify the file exists at the expected location
	taskDir := filepath.Join(tmpDir, "tasks", deferredTask.ID)
	assert.FileExists(t, filepath.Join(taskDir, "screenshot.png"))

	// Step 3: Now activate the task (this would trigger submission to worker pool)
	err = h.taskSvc.UpdateCategory(ctx, deferredTask.ID, models.CategoryActive)
	require.NoError(t, err)

	// Verify task is now active with attachments already in place
	task, err = h.taskRepo.GetByID(ctx, deferredTask.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CategoryActive, task.Category)

	// The key invariant: when the task starts executing, attachments are already there
	taskAttachments, err = h.attachmentRepo.ListByTask(ctx, deferredTask.ID)
	require.NoError(t, err)
	assert.Len(t, taskAttachments, 1, "attachments should be present when task becomes active")
}

// TestHandler_Chat_FullPageVsHTMXPartial verifies that HTMX requests return partial content
// while non-HTMX requests return full page with layout. This difference is why the server
// must set Vary: HX-Request and Cache-Control: no-store (on partials) to prevent browsers
// from serving a cached HTMX partial when a full page is needed (e.g., duplicating a tab).
func TestHandler_Chat_FullPageVsHTMXPartial(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  models.ProviderTest,
		Model:     "claude-3-sonnet-20240229",
		APIKey:    "test-key",
		MaxTokens: 4096,
		IsDefault: true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Full page load (no HX-Request) — e.g., opening URL directly or duplicating a tab
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	fullBody := rec.Body.String()

	// HTMX request — e.g., clicking sidebar link
	req2 := httptest.NewRequest(http.MethodGet, "/chat", nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	htmxBody := rec2.Body.String()

	// Full page must include the complete HTML structure with CSS/JS
	assert.Contains(t, fullBody, "<!doctype html>", "full page load must include doctype")
	assert.Contains(t, fullBody, "daisyui", "full page load must include CSS framework")
	assert.Contains(t, fullBody, "htmx.org", "full page load must include HTMX script")

	// HTMX partial must NOT include the full page structure
	assert.NotContains(t, htmxBody, "<!doctype html>", "HTMX partial must not include doctype")
	assert.NotContains(t, htmxBody, "<html", "HTMX partial must not include html tag")

	// Both must contain the chat content
	assert.Contains(t, fullBody, ">Chat<")
	assert.Contains(t, htmxBody, ">Chat<")
}

// TestHandler_ChatSend_WithImageAttachment_AnthropicAgent verifies that sending a message
// with an image attachment renders the user message when an Anthropic API agent is available.
func TestHandler_ChatSend_WithImageAttachment_AnthropicAgent(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create an Anthropic agent (supports vision)
	agent := &models.LLMConfig{
		Name:        "Claude Sonnet (Vision)",
		Provider:    models.ProviderTest,
		Model:       "claude-3-5-sonnet-20241022",
		APIKey:      "test-api-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	// Set up a temporary uploads directory with a pending image
	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "test-session-123"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "screenshot.png"), []byte("fake-png-data"), 0644))

	// Send message with attachment_session_id
	form := url.Values{}
	form.Set("message", "Analyze this screenshot")
	form.Set("agent_id", "auto")
	form.Set("attachment_session_id", sessionID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "should return 200 OK when Anthropic agent is available")

	body := rec.Body.String()
	// User message text must be visible in the response
	assert.Contains(t, body, "Analyze this screenshot", "user message must be in response")
	// User message bubble must be rendered
	assert.Contains(t, body, "chat-bubble-user-msg", "user message bubble must be in response")
	// Assistant streaming placeholder must be rendered
	assert.Contains(t, body, "chat-bubble-assistant-msg", "assistant bubble must be in response")
	// Attachment indicator should be present (badge showing count)
	assert.Contains(t, body, "Attachments", "attachment section should be in response")
}

// TestHandler_ChatSend_WithImageAttachment_CLIAgentOnly verifies that sending a message
// with an image attachment still works (falls back gracefully) when only a Claude CLI
// agent is available. This test catches the regression where the handler returned 400
// instead of falling back to the available agent.
func TestHandler_ChatSend_WithImageAttachment_CLIAgentOnly(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create ONLY a Claude CLI agent (no vision support)
	agent := &models.LLMConfig{
		Name:        "Claude Max CLI",
		Provider:    models.ProviderTest,
		Model:       "claude-sonnet-4-20250514",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	// Set up a temporary uploads directory with a pending image
	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "test-session-456"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "photo.jpg"), []byte("fake-jpg-data"), 0644))

	// Send message with attachment and auto agent selection
	form := url.Values{}
	form.Set("message", "What is in this photo?")
	form.Set("agent_id", "auto")
	form.Set("attachment_session_id", sessionID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Must return 200 OK — NOT 400. The handler should fall back to the available
	// agent rather than rejecting the request.
	assert.Equal(t, http.StatusOK, rec.Code, "should fall back to CLI agent, not return 400")

	body := rec.Body.String()
	// User message must be displayed
	assert.Contains(t, body, "What is in this photo?", "user message must be in response")
	assert.Contains(t, body, "chat-bubble-user-msg", "user message bubble must be rendered")
	assert.Contains(t, body, "chat-bubble-assistant-msg", "assistant streaming placeholder must be rendered")
}

// TestHandler_ChatSend_WithTextAttachment verifies that sending a message with a
// non-image attachment works correctly (no vision filtering needed).
func TestHandler_ChatSend_WithTextAttachment(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Claude Max CLI",
		Provider:    models.ProviderTest,
		Model:       "claude-sonnet-4-20250514",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "test-session-789"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "notes.txt"), []byte("Some text content"), 0644))

	form := url.Values{}
	form.Set("message", "Review this file")
	form.Set("agent_id", "auto")
	form.Set("attachment_session_id", sessionID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "text attachment should work with any agent")

	body := rec.Body.String()
	assert.Contains(t, body, "Review this file", "user message must be in response")
	assert.Contains(t, body, "chat-bubble-user-msg", "user message bubble must be rendered")
}

// TestHandler_ChatSend_AfterRequestOnlyClearsOnSuccess verifies that the
// hx-on::after-request handler in the chat form only clears input on success.
func TestHandler_ChatSend_AfterRequestOnlyClearsOnSuccess(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  models.ProviderTest,
		Model:     "claude-3-sonnet-20240229",
		APIKey:    "test-key",
		MaxTokens: 4096,
		IsDefault: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	// Load the chat page to check the form's hx-on attribute
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	// The after-request handler must check event.detail.successful before clearing
	assert.Contains(t, body, "event.detail.successful", "after-request handler must check successful before clearing input")
	// Must NOT unconditionally clear (old bug: cleared even on failed requests)
	assert.NotContains(t, body, `hx-on::after-request="document.getElementById(&#39;message-input&#39;).value = &#39;&#39;; window.chatClearAttachments();"`,
		"after-request handler must not unconditionally clear input")
}

func TestHandler_ChatSend_BypassesProjectCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a project with max_workers=1
	project := &models.Project{
		Name: "Test Project",
	}
	maxWorkers := 1
	project.MaxWorkers = &maxWorkers
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)

	// Create a default agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err = llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Wire projectRepo into workerSvc so project limits are enforced
	h.workerSvc.SetProjectRepo(h.projectRepo)

	// Acquire the project's worker slot to simulate task workers at capacity
	acquired := h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, acquired, "should acquire project slot")
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	// Chat should still work even when project capacity is full
	form := url.Values{}
	form.Set("message", "Hello, agent!")
	form.Set("agent_id", agent.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Chat bypasses task worker limits — should succeed (200 OK)
	assert.Equal(t, http.StatusOK, rec.Code, "chat should not be blocked by task worker capacity")
}

func TestHandler_ChatSend_BypassesModelCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name: "Test Project",
	}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)

	// Create an agent with max_workers=1
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
		MaxWorkers:  1,
	}
	err = llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Wire llmConfigRepo into workerSvc so model limits are enforced
	h.workerSvc.SetLLMConfigRepo(h.llmConfigRepo)

	// Acquire the model's worker slot to simulate task workers at capacity
	acquired := h.workerSvc.TryAcquireModelSlot(agent.ID)
	require.True(t, acquired, "should acquire model slot")
	defer h.workerSvc.ReleaseModelSlot(agent.ID)

	// Chat should still work even when model capacity is full
	form := url.Values{}
	form.Set("message", "Hello, agent!")
	form.Set("agent_id", agent.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Chat bypasses task worker limits — should succeed (200 OK)
	assert.Equal(t, http.StatusOK, rec.Code, "chat should not be blocked by model worker capacity")
}

// TestHandler_ChatSend_RespondsWhileTasksAtMaxCapacity verifies the core fix:
// interactive chat (/chat) must respond even when both per-project and per-model
// task worker slots are fully saturated.
func TestHandler_ChatSend_RespondsWhileTasksAtMaxCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{Name: "Saturated Project", MaxWorkers: &maxWorkers}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)

	// Create an agent with max_workers=1
	agent := &models.LLMConfig{
		Name:        "Saturated Model",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
		MaxWorkers:  1,
	}
	err = llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Wire repos into workerSvc so limits are enforced
	h.workerSvc.SetProjectRepo(h.projectRepo)
	h.workerSvc.SetLLMConfigRepo(h.llmConfigRepo)

	// Saturate BOTH project and model slots (simulating tasks at max capacity)
	acquired := h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, acquired, "should acquire project slot")
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	acquired = h.workerSvc.TryAcquireModelSlot(agent.ID)
	require.True(t, acquired, "should acquire model slot")
	defer h.workerSvc.ReleaseModelSlot(agent.ID)

	// Verify capacity is truly full
	assert.False(t, h.workerSvc.HasProjectCapacity(project.ID), "project should be at capacity")
	assert.False(t, h.workerSvc.HasModelCapacity(agent.ID), "model should be at capacity")

	// Send multiple chat messages — all should succeed despite full capacity
	for i := 0; i < 3; i++ {
		form := url.Values{}
		form.Set("message", "Chat while tasks are running")
		form.Set("agent_id", agent.ID)

		req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+project.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, "chat %d should succeed despite full task capacity", i)
	}
}

// TestHandler_TaskThreadSend_QueuesWhenAtCapacity verifies that task follow-up thread
// messages are accepted and queued when workers are at capacity (not rejected).
// Interactive chat bypasses limits entirely; task follow-ups queue and wait for slots.
func TestHandler_TaskThreadSend_QueuesWhenAtCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	maxWorkers := 1
	project := &models.Project{Name: "Limit Project", MaxWorkers: &maxWorkers}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)
	h.workerSvc.SetProjectRepo(h.projectRepo)

	agent := &models.LLMConfig{
		Name:        "Limited Model",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
		MaxWorkers:  1,
	}
	err = llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)
	h.workerSvc.SetLLMConfigRepo(h.llmConfigRepo)

	// Create a completed task that can receive follow-ups
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Task for followup",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task)
	require.NoError(t, err)

	// Saturate project slot
	acquired := h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, acquired)
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	// Task follow-up thread should be ACCEPTED (queued for processing when slot opens)
	form := url.Values{}
	form.Set("message", "Follow up on task")
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/thread", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "task follow-up should be accepted and queued")

	// Message should be saved in an execution record
	execs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
	assert.Equal(t, 1, len(execs), "execution record should be created for queued follow-up")
	if len(execs) > 0 {
		assert.Equal(t, "Follow up on task", execs[0].PromptSent)
		assert.True(t, execs[0].IsFollowup)
	}

	// Task should be moved to queued/active (will transition to running once worker slots acquired)
	updatedTask, _ := h.taskSvc.GetByID(ctx, task.ID)
	assert.Equal(t, models.StatusQueued, updatedTask.Status)
	assert.Equal(t, models.CategoryActive, updatedTask.Category)
}

// TestHandler_Chat_ReconnectPreservesProjectID verifies that the SSE reconnection
// and chat_response_done handlers include project_id in their HTMX refresh calls.
// Bug: returning to browser after focus/blur caused /chat to reload without project_id,
// which silently switched to the Default project.
func TestHandler_Chat_ReconnectPreservesProjectID(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a second (non-default) project
	project2 := &models.Project{
		Name:        "My Custom Project",
		Description: "Non-default project",
		RepoPath:    "/tmp/custom",
		IsDefault:   false,
	}
	err := h.projectSvc.Create(ctx, project2)
	require.NoError(t, err)

	// Create an agent
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  models.ProviderTest,
		Model:     "test-model",
		APIKey:    "test-key",
		MaxTokens: 4096,
		IsDefault: true,
	}
	err = llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Request chat page for the non-default project (HTMX partial)
	req := httptest.NewRequest(http.MethodGet, "/chat?project_id="+project2.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// The rendered HTML must have a chat-specific root with the requested project ID.
	assert.Contains(t, body, `id="chat-page-root"`,
		"chat content must render with a page-specific root")
	assert.Contains(t, body, `id="chat-page-root" class="h-full flex flex-col" data-project-id="`+project2.ID+`"`,
		"chat root must have the requested project ID")

	// The SSE reconnect and response_done refresh calls must include project_id
	// and be scoped to the chat root element only.
	assert.Contains(t, body, `'/chat?project_id=' + encodeURIComponent(pid)`,
		"SSE reconnect must include project_id in HTMX refresh URL")
	assert.Contains(t, body, `document.getElementById('chat-page-root')`,
		"chat refresh must target only the chat root")
	assert.NotContains(t, body, `document.querySelector('[data-project-id]')`,
		"chat page must not use generic project-id selector for refresh logic")

	// Verify there is no bare '/chat' AJAX call that would lose project context.
	assert.NotContains(t, body, `htmx.ajax('GET', '/chat',`,
		"must not have bare /chat AJAX calls without project_id")
	assert.Contains(t, body, `window.addEventListener('sse-chat-live-event', handleSharedChatLiveEvent);`,
		"chat page must listen to shared live chat events")
}

func TestHandler_Chat_PlanCompletionPrompt_CentralizedEvaluator(t *testing.T) {
	// Verify the centralized evaluatePlanCompletionPrompt function exists and
	// implements all required guards (streaming, mode, plan marker).
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Centralized evaluator function must exist
	assert.Contains(t, body, "window.evaluatePlanCompletionPrompt = function(completedText)",
		"centralized evaluatePlanCompletionPrompt must be defined")

	// Guard 1: never show mid-stream
	assert.Contains(t, body, "if (window._chatStreamInProgress)",
		"evaluator must check _chatStreamInProgress flag")

	// Guard 2: only show in plan mode
	assert.Contains(t, body, "window.currentChatModeValue() !== 'plan'",
		"evaluator must check plan mode")

	// Guard 3: plan marker detection
	assert.Contains(t, body, "window.planModeHasProposedPlan(text)",
		"evaluator must check for proposed_plan marker")

	// Reconnect/hydration scans that temporarily return empty text should preserve
	// an already-earned CTA, while explicit completion events still control clearing.
	assert.Contains(t, body, "var fromCompletedEvent = (typeof completedText === 'string');",
		"evaluator must distinguish completion events from DOM-scan fallback")
	assert.Contains(t, body, "if (!fromCompletedEvent && !text && window._chatPlanPromptLatched)",
		"empty reconnect/history scans must preserve latched CTA visibility")
	assert.Contains(t, body, "window._chatPlanPromptLatched = true",
		"show helper must latch CTA visibility across tab refocus")
	assert.Contains(t, body, "window._chatPlanPromptLatched = false",
		"hide helper must clear CTA latch when state genuinely changes")

	// handlePlanModeCompletion must clear streaming flag and delegate
	assert.Contains(t, body, "window._chatStreamInProgress = false",
		"handlePlanModeCompletion must clear streaming flag on completion")
	assert.Contains(t, body, "window.evaluatePlanCompletionPrompt(rawText)",
		"handlePlanModeCompletion must delegate to centralized evaluator")
}

func TestHandler_Chat_PlanCompletionPrompt_StreamingFlagOnSend(t *testing.T) {
	// Verify that user send sets _chatStreamInProgress = true and hides prompt
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// In the beforeRequest handler for chat-form, streaming flag must be set
	// before hiding the prompt
	beforeReqIdx := strings.Index(body, "triggerEl.id === 'chat-form'")
	require.NotEqual(t, -1, beforeReqIdx, "beforeRequest chat-form check must exist")

	streamFlagIdx := strings.Index(body[beforeReqIdx:], "_chatStreamInProgress = true")
	require.NotEqual(t, -1, streamFlagIdx, "streaming flag must be set on send")

	hideIdx := strings.Index(body[beforeReqIdx:], "hidePlanCompletionPrompt")
	require.NotEqual(t, -1, hideIdx, "prompt must be hidden on send")
}

func TestHandler_Chat_PlanCompletionPrompt_LatestResponseOnly(t *testing.T) {
	// Verify _getLatestCompletedAssistantText returns only the latest
	// completed assistant text, not older messages with plan markers.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// The _getLatestCompletedAssistantText function must exist
	assert.Contains(t, body, "window._getLatestCompletedAssistantText = function()",
		"_getLatestCompletedAssistantText helper must be defined")

	// It must walk newest -> oldest and return on the first non-empty completed
	// bubble rather than continuing to scan older bubbles
	assert.Contains(t, body, "// This is the latest completed assistant response",
		"helper must return on first match, not scan all history")
	assert.Contains(t, body, "return raw;",
		"helper must return raw text of latest completed response")

	// It must skip actively-streaming bubbles (loading dots visible)
	assert.Contains(t, body, "var thinkingDots = bubble.querySelector('.ov-loading-dots')",
		"helper must detect streaming-in-progress via loading dots")
	assert.Contains(t, body, "var streamDotsContainer = bubble.querySelector('[id^=\"streaming-dots-\"]')",
		"helper must detect shared streaming dots containers")

	// It must include live SSE bubbles that store raw text on data-raw-content
	// without the .chat-stream-content class.
	assert.Contains(t, body, "[data-raw-content][data-exec-id]",
		"helper must read raw content from live SSE assistant bubbles")
}

func TestHandler_Chat_PlanCompletionPrompt_ModeSelectorPreference(t *testing.T) {
	// Verify currentChatModeValue prefers visible select over hidden input
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// currentChatModeValue must check select BEFORE hidden input for robustness
	fnStart := strings.Index(body, "window.currentChatModeValue = function()")
	require.NotEqual(t, -1, fnStart, "currentChatModeValue must exist")
	fnBody := body[fnStart : fnStart+1200]

	selectIdx := strings.Index(fnBody, "modeSelect")
	inputIdx := strings.Index(fnBody, "modeInput")
	require.NotEqual(t, -1, selectIdx)
	require.NotEqual(t, -1, inputIdx)
	assert.Less(t, selectIdx, inputIdx,
		"currentChatModeValue must check visible select before hidden input")
	assert.Contains(t, fnBody, "persistedMode",
		"currentChatModeValue should use persisted mode during hydration windows")
	assert.Contains(t, fnBody, "modeSelect.dataset",
		"currentChatModeValue should gate select value usage on hydration state")
}

func TestHandler_Chat_PlanCompletionPrompt_ModeRestoreReevaluatesOnHydration(t *testing.T) {
	// Regression: after blur/focus reconnect HTMX swap, mode restoration from localStorage
	// must re-evaluate plan prompt from durable assistant history so CTA does not disappear.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	modeBlockIdx := strings.Index(body, "if (modeSelect && modeInput) {")
	require.NotEqual(t, -1, modeBlockIdx, "mode selector wiring must exist")
	modeBlock := body[modeBlockIdx : modeBlockIdx+1600]

	assert.Contains(t, modeBlock, "function reevaluatePlanPrompt()",
		"mode selector wiring must define prompt reevaluation helper")
	assert.Contains(t, modeBlock, "if (window.evaluatePlanCompletionPrompt) window.evaluatePlanCompletionPrompt();",
		"mode restore must trigger centralized plan prompt evaluator")
	assert.Contains(t, modeBlock, "modeInput.value = savedMode;",
		"mode restore must sync hidden mode input")
	assert.Contains(t, modeBlock, "modeSelect.dataset.hydrated = 'false';",
		"mode restore must mark selector as not hydrated before persisted restore")
	assert.Contains(t, modeBlock, "modeSelect.dataset.hydrated = 'true';",
		"mode restore must mark selector as hydrated after persisted restore")
	assert.Contains(t, modeBlock, "reevaluatePlanPrompt();",
		"mode restore/change must re-evaluate CTA visibility from history")

	changeIdx := strings.Index(modeBlock, "modeSelect.addEventListener('change', function() {")
	require.NotEqual(t, -1, changeIdx, "mode change handler must exist")
	changeBody := modeBlock[changeIdx:]
	assert.Contains(t, changeBody, "modeInput.value = this.value;",
		"mode change must keep hidden input synchronized")
	assert.Contains(t, changeBody, "reevaluatePlanPrompt();",
		"mode change must re-evaluate plan CTA visibility")
}

func TestHandler_Chat_PlanCompletionPrompt_ChatResponseDoneFallback(t *testing.T) {
	// Verify chat_response_done SSE handler invokes plan completion evaluation
	// as fallback when per-exec done event is missed.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Shared live-event chat_response_done branch must invoke evaluatePlanCompletionPrompt.
	doneHandlerIdx := strings.Index(body, "if (eventType === 'chat_response_done') {")
	require.NotEqual(t, -1, doneHandlerIdx, "chat_response_done handler must exist")
	doneHandlerBody := body[doneHandlerIdx : doneHandlerIdx+1500]

	assert.Contains(t, doneHandlerBody, "evaluatePlanCompletionPrompt",
		"chat_response_done must invoke evaluatePlanCompletionPrompt as fallback")
	assert.Contains(t, doneHandlerBody, "_chatStreamInProgress = false",
		"chat_response_done must clear streaming flag")
	assert.Contains(t, doneHandlerBody, "data.completed_output",
		"chat_response_done fallback should use completed_output when present")
	assert.Contains(t, doneHandlerBody, "syncCompletedOutputToBubble(data.exec_id, data.completed_output)",
		"chat_response_done should reconcile the live bubble with completed_output")
}

func TestHandler_Chat_PlanCompletionPrompt_NewMessageSetsStreamingFlag(t *testing.T) {
	// Verify chat_new_message SSE handler sets streaming flag and hides prompt,
	// and guards against duplicate stream bubbles for the same exec_id.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Shared live-event chat_new_message branch must set streaming flag.
	newMsgIdx := strings.Index(body, "if (eventType === 'chat_new_message') {")
	require.NotEqual(t, -1, newMsgIdx, "chat_new_message handler must exist")
	newMsgBody := body[newMsgIdx : newMsgIdx+2000]

	assert.Contains(t, newMsgBody, "_chatStreamInProgress = true",
		"chat_new_message must set streaming flag")
	assert.Contains(t, newMsgBody, "hidePlanCompletionPrompt",
		"chat_new_message must hide prompt when new stream starts")
	assert.Contains(t, newMsgBody, "document.querySelector('[data-exec-id=\"' + data.exec_id + '\"]')",
		"chat_new_message must guard against duplicate exec bubbles already in DOM")
}

func TestHandler_Chat_LiveStreamingUsesRenderStreamingContent(t *testing.T) {
	// Live SSE bubble stream updates should use renderStreamingContent, matching
	// thread behavior, rather than raw textContent assignment.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	onMsgIdx := strings.Index(body, "eventSource.onmessage = function(event)")
	require.NotEqual(t, -1, onMsgIdx, "live streaming onmessage handler must exist")
	onMsgBody := body[onMsgIdx : onMsgIdx+2400]

	assert.Contains(t, body, "function renderBufferedOutput(force)",
		"live streaming should define a batched render helper")
	assert.Contains(t, body, "if (!window.renderStreamingContent)",
		"batched render helper should provide text fallback when renderer is unavailable")
	assert.Contains(t, body, "window.renderStreamingContent(contentDiv, textBuffer)",
		"batched render helper should render via shared renderer")
	assert.Contains(t, onMsgBody, "renderBufferedOutput(false)",
		"live streaming should batch per-chunk rendering to keep UI responsive")
}

func TestHandler_Chat_PlanCompletionPrompt_StreamDoneEvaluatesBeforeTransforms(t *testing.T) {
	// Regression test for refresh-only prompt behavior:
	// done handler must invoke plan completion evaluation before transform helpers
	// so prompt state updates even if transform code throws.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	doneIdx := strings.Index(body, "eventSource.addEventListener('done', function()")
	require.NotEqual(t, -1, doneIdx, "stream done handler must exist")
	doneBody := body[doneIdx : doneIdx+2000]

	handleIdx := strings.Index(doneBody, "handlePlanModeCompletion")
	convertIdx := strings.Index(doneBody, "convertTaskLinksInMessage")
	require.NotEqual(t, -1, handleIdx, "done handler must call handlePlanModeCompletion")
	require.NotEqual(t, -1, convertIdx, "done handler must include transform calls")
	assert.Less(t, handleIdx, convertIdx,
		"done handler must evaluate plan completion before transforms")
	assert.Contains(t, doneBody, "catch (err)",
		"done handler should guard transforms to avoid blocking prompt evaluation")
}

func TestHandler_Chat_PlanCompletionPrompt_SwapDoesNotClearStreamingFlag(t *testing.T) {
	// Verify that HTMX swap does NOT clear stream-in-progress for web sends.
	// Stream state must only transition on stream done / chat_response_done.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// In the afterSwap handler for chat messages, stream flag should NOT be cleared.
	swapIdx := strings.Index(body, "isChatMessagesSwap || isChatRootSwap")
	require.NotEqual(t, -1, swapIdx, "afterSwap chat check must exist")
	swapBody := body[swapIdx : swapIdx+1000]

	assert.Contains(t, swapBody, "_chatWebSendInProgress = false",
		"HTMX swap should clear only web send dedupe flag")
	assert.Contains(t, swapBody, "if (!window._chatStreamInProgress && window.maybeShowPlanCompletionPromptFromHistory)",
		"afterSwap should guard prompt recovery while stream is active")
	assert.NotContains(t, swapBody, "_chatStreamInProgress = false",
		"HTMX swap must not clear stream-in-progress flag")
}

func TestHandler_Chat_PlanCompletionPrompt_MultiTurnRegression(t *testing.T) {
	// Regression test: plan turn shows prompt, subsequent non-plan turn hides it.
	// The history evaluator must use ONLY the latest completed assistant message.
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Turn 1: assistant response with proposed_plan
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Chat: Plan turn",
		Prompt:    "plan something",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task1)
	require.NoError(t, err)
	exec1 := &models.Execution{
		TaskID:        task1.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "plan something",
	}
	err = h.execRepo.Create(ctx, exec1)
	require.NoError(t, err)
	err = h.execRepo.Complete(ctx, exec1.ID, models.ExecCompleted,
		"Here is the plan:\n<proposed_plan>\nStep 1: Do X\n</proposed_plan>", "", 10, 50)
	require.NoError(t, err)

	// Turn 2: assistant response WITHOUT proposed_plan (non-plan follow-up)
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Chat: Non-plan turn",
		Prompt:    "do something else",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	err = h.taskRepo.Create(ctx, task2)
	require.NoError(t, err)
	exec2 := &models.Execution{
		TaskID:        task2.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "do something else",
	}
	err = h.execRepo.Create(ctx, exec2)
	require.NoError(t, err)
	err = h.execRepo.Complete(ctx, exec2.ID, models.ExecCompleted,
		"Done, no plan here.", "", 8, 40)
	require.NoError(t, err)

	// Render chat page
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// The latest assistant bubble must be "Done, no plan here." (Turn 2)
	// The evaluator must NOT scan back to Turn 1 and show the prompt.
	// Verify both turns are rendered in order
	planIdx := strings.Index(body, "proposed_plan")
	noPlanIdx := strings.Index(body, "Done, no plan here.")
	require.NotEqual(t, -1, planIdx, "plan turn must be rendered")
	require.NotEqual(t, -1, noPlanIdx, "non-plan turn must be rendered")
	assert.Less(t, planIdx, noPlanIdx, "plan turn must come before non-plan turn")

	// The evaluator function must return the latest text and NOT walk older bubbles
	// We verify this by checking the function structure returns on first match
	assert.Contains(t, body, "// This is the latest completed assistant response — return it",
		"evaluator must return on first (newest) completed bubble, not scan all history")
}

func TestHandler_Chat_PlanCompletionPrompt_StreamErrorClearsFlag(t *testing.T) {
	// Verify that per-exec stream error/onerror handlers (in ChatBubbleStreaming)
	// clear _chatStreamInProgress and re-evaluate the plan prompt. Without this,
	// the flag stays stuck true after a streaming failure and blocks prompt evaluation.
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Trigger a chat send to get the ChatBubbleStreaming component rendered
	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	form := url.Values{}
	form.Set("message", "test plan message")
	form.Set("project_id", projectID)
	form.Set("chat_mode", "plan")

	req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+projectID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()

	// The ChatBubbleStreaming component's error handler must clear streaming flag
	// and invoke evaluator for non-thread (chat) contexts.
	// The handler body with isThread branch can be ~1200 chars.
	errIdx := strings.Index(body, "eventSource.addEventListener('error'")
	require.NotEqual(t, -1, errIdx, "streaming bubble must have error event listener")
	errEnd := errIdx + 1200
	if errEnd > len(body) {
		errEnd = len(body)
	}
	errBody := body[errIdx:errEnd]

	assert.Contains(t, errBody, "_chatStreamInProgress = false",
		"streaming bubble error handler must clear _chatStreamInProgress for chat context")
	assert.Contains(t, errBody, "evaluatePlanCompletionPrompt",
		"streaming bubble error handler must re-evaluate plan prompt for chat context")

	// onerror handler too
	oeIdx := strings.Index(body, "eventSource.onerror")
	require.NotEqual(t, -1, oeIdx, "streaming bubble must have onerror handler")
	oeEnd := oeIdx + 1200
	if oeEnd > len(body) {
		oeEnd = len(body)
	}
	oeBody := body[oeIdx:oeEnd]

	assert.Contains(t, oeBody, "_chatStreamInProgress = false",
		"streaming bubble onerror must clear _chatStreamInProgress for chat context")
	assert.Contains(t, oeBody, "evaluatePlanCompletionPrompt",
		"streaming bubble onerror must re-evaluate plan prompt for chat context")
}

func TestHandler_Chat_ReconnectRefreshSkipsWhileActiveStream(t *testing.T) {
	// Visibility reconnect should not force a full chat outerHTML refresh while
	// a local streaming bubble is still active.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	onConnectIdx := strings.Index(body, "var handleSharedLiveConnected = function(event) {")
	require.NotEqual(t, -1, onConnectIdx, "visibility reconnect handler must exist")
	onConnectBody := body[onConnectIdx : onConnectIdx+1400]

	assert.Contains(t, onConnectBody, "window._chatStreamInProgress && hasActiveChatStream()",
		"reconnect handler must detect active stream before triggering refresh")
	assert.Contains(t, onConnectBody, "return;",
		"reconnect handler should early-return when active stream is present")
}

func TestHandler_Chat_PlanCompletionPrompt_ChatResponseDoneCompletedOutput(t *testing.T) {
	// Verify chat_response_done handler uses completed_output from event payload
	// for both content reconciliation and plan-completion evaluation.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Shared live-event chat_response_done branch must pass completed_output to evaluator.
	doneHandlerIdx := strings.Index(body, "if (eventType === 'chat_response_done') {")
	require.NotEqual(t, -1, doneHandlerIdx, "chat_response_done handler must exist")
	doneHandlerBody := body[doneHandlerIdx : doneHandlerIdx+2200]

	syncIdx := strings.Index(doneHandlerBody, "syncCompletedOutputToBubble(data.exec_id, data.completed_output)")
	flagIdx := strings.Index(doneHandlerBody, "_chatStreamInProgress = false")
	evalIdx := strings.Index(doneHandlerBody, "evaluatePlanCompletionPrompt(data.completed_output)")

	require.NotEqual(t, -1, syncIdx, "chat_response_done must reconcile visible bubble with completed_output")
	require.NotEqual(t, -1, flagIdx, "chat_response_done must clear stream-in-progress flag")
	require.NotEqual(t, -1, evalIdx, "chat_response_done must evaluate plan prompt with completed_output")
	assert.Less(t, syncIdx, evalIdx,
		"chat_response_done should reconcile bubble content before evaluating completion prompt")
}

func TestHandler_Chat_AssistantBubbleRehydrationOnLoadAndSwap(t *testing.T) {
	// Regression: hard refresh must rehydrate assistant bubbles from raw content
	// so tool-call cards remain visible (same behavior as nav away/back).
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Contains(t, body, "if (window.cleanAssistantMessages) window.cleanAssistantMessages(chatMessages);",
		"chat transforms must rehydrate assistant bubbles from data-raw-content before marker cleaning")
	assert.Contains(t, body, "window.rehydrateChatAssistantBubbles = function()",
		"chat page must expose explicit rehydration helper")
	assert.Contains(t, body, "if (window.rehydrateChatAssistantBubbles) window.rehydrateChatAssistantBubbles();",
		"chat page must schedule initial rehydration pass after load")
	assert.Contains(t, body, "if (!window._chatStreamInProgress && window.maybeShowPlanCompletionPromptFromHistory)",
		"afterSwap prompt recovery must not run while an active stream is still in progress")
}

func TestHandler_Chat_PlanCompletionPrompt_ModeOrchestrateSuppresses(t *testing.T) {
	// When mode is orchestrate (not plan), prompt must stay hidden even if
	// latest assistant response contains <proposed_plan>.
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// The evaluator checks mode before marker detection
	evalIdx := strings.Index(body, "window.evaluatePlanCompletionPrompt = function(completedText)")
	require.NotEqual(t, -1, evalIdx, "evaluator function must exist")
	evalBody := body[evalIdx : evalIdx+800]

	modeCheckIdx := strings.Index(evalBody, "currentChatModeValue() !== 'plan'")
	markerCheckIdx := strings.Index(evalBody, "planModeHasProposedPlan")
	require.NotEqual(t, -1, modeCheckIdx, "evaluator must check mode")
	require.NotEqual(t, -1, markerCheckIdx, "evaluator must check marker")
	assert.Less(t, modeCheckIdx, markerCheckIdx,
		"mode check must precede marker check — orchestrate mode hides prompt before scanning content")
}
