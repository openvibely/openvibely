package handler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestExecuteChatTaskCreations(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := service.NewWorkerService(nil, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	// Create a project first
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	requests := []service.TaskCreationRequest{
		{Title: "Task One", Prompt: "Do task one", Category: "backlog", Priority: 2},
		{Title: "Task Two", Prompt: "Do task two", Category: "active", Priority: 1},
	}

	summary := executeChatTaskCreations(context.Background(), requests, project.ID, taskSvc)

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !contains(summary, "Created 2 task(s)") {
		t.Errorf("expected summary to contain 'Created 2 task(s)', got %q", summary)
	}
	if !contains(summary, "Task One") {
		t.Errorf("expected summary to contain 'Task One', got %q", summary)
	}
	if !contains(summary, "Task Two") {
		t.Errorf("expected summary to contain 'Task Two', got %q", summary)
	}

	// Verify summary includes task IDs for clickable links
	if !contains(summary, "[TASK_ID:") {
		t.Errorf("expected summary to contain [TASK_ID: markers for clickable links, got %q", summary)
	}

	// Verify tasks were actually created in the database
	tasks, err := taskRepo.ListByProject(context.Background(), project.ID, "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks in DB, got %d", len(tasks))
	}

	// Verify each task ID appears in the summary
	for i := range tasks {
		expectedMarker := "[TASK_ID:" + tasks[i].ID + "]"
		if !contains(summary, expectedMarker) {
			t.Errorf("expected summary to contain task ID marker %q, got %q", expectedMarker, summary)
		}
	}

	// Verify task details
	var taskOne, taskTwo *models.Task
	for i := range tasks {
		switch tasks[i].Title {
		case "Task One":
			taskOne = &tasks[i]
		case "Task Two":
			taskTwo = &tasks[i]
		}
	}
	if taskOne == nil || taskTwo == nil {
		t.Fatal("expected both tasks to be found in DB")
	}
	if taskOne.Category != models.CategoryBacklog {
		t.Errorf("expected Task One category 'backlog', got %q", taskOne.Category)
	}
	if taskTwo.Category != models.CategoryActive {
		t.Errorf("expected Task Two category 'active', got %q", taskTwo.Category)
	}
	if taskOne.Prompt != "Do task one" {
		t.Errorf("expected Task One prompt 'Do task one', got %q", taskOne.Prompt)
	}
}

func TestDeferActiveTasksWithAttachments_DuplicateTitlesRetainRequestIdentity(t *testing.T) {
	h := &Handler{}
	requests := []service.TaskCreationRequest{
		{Title: "Same title", Category: "active"},
		{Title: "Same title", Category: "backlog"},
	}
	deferred := h.deferActiveTasksWithAttachments(requests, []models.ChatAttachment{{ID: "attachment"}}, nil)
	if !deferred[0] || deferred[1] || len(deferred) != 1 {
		t.Fatalf("expected only the active request index to be deferred, got %#v", deferred)
	}
}

func TestExecuteChatTaskCreations_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := service.NewWorkerService(nil, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task first
	existing := &models.Task{
		ProjectID: project.ID,
		Title:     "Existing Task",
		Prompt:    "Already exists",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(context.Background(), existing); err != nil {
		t.Fatalf("failed to create existing task: %v", err)
	}

	// Try to create a duplicate
	requests := []service.TaskCreationRequest{
		{Title: "Existing Task", Prompt: "Duplicate", Category: "backlog", Priority: 2},
	}

	summary := executeChatTaskCreations(context.Background(), requests, project.ID, taskSvc)

	if !contains(summary, "Failed to create 1 task(s)") {
		t.Errorf("expected failure summary, got %q", summary)
	}
}

func TestExecuteChatTaskCreations_InvalidCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := service.NewWorkerService(nil, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Category should default to backlog for invalid values
	requests := []service.TaskCreationRequest{
		{Title: "Task Invalid Category", Prompt: "Test", Category: "invalid", Priority: 2},
	}

	summary := executeChatTaskCreations(context.Background(), requests, project.ID, taskSvc)
	if !contains(summary, "Created 1 task(s)") {
		t.Errorf("expected success with defaulted category, got %q", summary)
	}

	// Verify category was defaulted to backlog
	tasks, _ := taskRepo.ListByProject(context.Background(), project.ID, "backlog")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 backlog task, got %d", len(tasks))
	}
}

func TestExecuteChatTaskCreations_Empty(t *testing.T) {
	summary := executeChatTaskCreations(context.Background(), nil, "proj1", nil)
	if summary != "" {
		t.Errorf("expected empty summary for nil requests, got %q", summary)
	}
}

func TestBuildTaskContextString(t *testing.T) {
	tasks := []models.Task{
		{ID: "abc123", Title: "Auth system", Category: models.CategoryActive, Status: models.StatusRunning, Priority: 2, Prompt: "Implement auth"},
		{ID: "def456", Title: "Fix bugs", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 3, Prompt: ""},
	}

	result := buildTaskContextString(tasks)
	if !contains(result, "Auth system") {
		t.Errorf("expected result to contain 'Auth system', got %q", result)
	}
	if !contains(result, "[active, running") {
		t.Errorf("expected result to contain '[active, running', got %q", result)
	}
	if !contains(result, "Fix bugs") {
		t.Errorf("expected result to contain 'Fix bugs', got %q", result)
	}
	if !contains(result, "Prompt: Implement auth") {
		t.Errorf("expected result to contain full prompt on its own line, got %q", result)
	}
	// Verify IDs are included
	if !contains(result, "[ID:abc123]") {
		t.Errorf("expected result to contain '[ID:abc123]', got %q", result)
	}
}

func TestBuildTaskContextString_FullPromptIncluded(t *testing.T) {
	// Prompts up to 500 chars should be included in full
	longPrompt := "Fix the agent deletion functionality. When a user clicks delete on an agent configuration, the backend should remove the agent from the database and update the UI. Currently the delete button sends the request but the backend handler returns an error because the agent ID is not being passed correctly through the HTMX request. Check the agent_handler.go DeleteAgent method and verify the route parameter extraction."
	tasks := []models.Task{
		{Title: "agent delete fix", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: longPrompt},
	}

	result := buildTaskContextString(tasks)
	// The full prompt should be included (it's under 500 chars)
	if !contains(result, longPrompt) {
		t.Errorf("expected full prompt to be included for prompts under 500 chars, got %q", result)
	}
	if !contains(result, "Prompt: ") {
		t.Errorf("expected 'Prompt: ' prefix, got %q", result)
	}
}

func TestBuildTaskContextString_VeryLongPromptTruncated(t *testing.T) {
	// Prompts over 500 chars should be truncated
	longPrompt := ""
	for i := 0; i < 60; i++ {
		longPrompt += "word word "
	}
	// longPrompt is 600 chars
	tasks := []models.Task{
		{Title: "big task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: longPrompt},
	}

	result := buildTaskContextString(tasks)
	if !contains(result, "...") {
		t.Errorf("expected truncation marker '...' for long prompts, got %q", result)
	}
	// Should contain the first 500 chars
	if !contains(result, longPrompt[:500]) {
		t.Errorf("expected first 500 chars of prompt to be included")
	}
}

func TestBuildTaskContextString_Empty(t *testing.T) {
	result := buildTaskContextString(nil)
	if !contains(result, "No tasks exist") {
		t.Errorf("expected empty message, got %q", result)
	}
}

func TestBuildTaskContextString_ExcludesChatTasks(t *testing.T) {
	// Simulate what ChatSend does: filter out chat tasks before building context
	allTasks := []models.Task{
		{Title: "agent delete fix", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "Agent delete does not work"},
		{Title: "Chat 21:48:10.680: What tasks are in backlog", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "What tasks are in backlog"},
		{Title: "schedule scroll jump", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "Fix scroll jumping"},
		{Title: "Chat 21:49:00.000: Tell me about agent delete", Category: models.CategoryChat, Status: models.StatusPending, Prompt: "Tell me about agent delete"},
	}

	// Filter out chat tasks (same logic as ChatSend handler)
	var nonChatTasks []models.Task
	for _, t := range allTasks {
		if t.Category != models.CategoryChat {
			nonChatTasks = append(nonChatTasks, t)
		}
	}

	result := buildTaskContextString(nonChatTasks)

	// Should include real tasks
	if !contains(result, "agent delete fix") {
		t.Errorf("expected 'agent delete fix' in context, got %q", result)
	}
	if !contains(result, "schedule scroll jump") {
		t.Errorf("expected 'schedule scroll jump' in context, got %q", result)
	}
	// Should NOT include chat tasks
	if contains(result, "Chat 21:48") {
		t.Errorf("expected chat tasks to be excluded, but found them in %q", result)
	}
	if contains(result, "Chat 21:49") {
		t.Errorf("expected chat tasks to be excluded, but found them in %q", result)
	}
}

// --- Task Edit Tests ---

func TestExecuteChatTaskEdits(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := service.NewWorkerService(nil, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task to edit
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Original Title",
		Prompt:    "Original prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	requests := []service.TaskEditRequest{
		{ID: task.ID, Title: "Updated Title", Prompt: "Updated prompt"},
	}

	summary := executeChatTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected summary to contain 'Edited 1 task(s)', got %q", summary)
	}
	if !contains(summary, "[TASK_EDITED:") {
		t.Errorf("expected summary to contain [TASK_EDITED: marker, got %q", summary)
	}

	// Verify task was actually updated
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", updated.Title)
	}
	if updated.Prompt != "Updated prompt" {
		t.Errorf("expected prompt 'Updated prompt', got %q", updated.Prompt)
	}
}

func TestExecuteChatTaskEdits_TaskNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := service.NewWorkerService(nil, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	requests := []service.TaskEditRequest{
		{ID: "nonexistent", Title: "New Title"},
	}

	summary := executeChatTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if !contains(summary, "Failed to edit 1 task(s)") {
		t.Errorf("expected failure summary, got %q", summary)
	}
}

func TestExecuteChatTaskEdits_Empty(t *testing.T) {
	summary := executeChatTaskEdits(context.Background(), nil, "proj1", nil, nil, "")
	if summary != "" {
		t.Errorf("expected empty summary for nil requests, got %q", summary)
	}
}

// --- Chat Attachment Edit Tests ---

func TestProcessChatAttachmentsForEdits(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create an agent config (required for execution FK)
	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 4096,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a task to attach files to
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Target Task",
		Prompt:    "Original prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a chat execution to associate attachments with
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Add this screenshot to the task",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// Create a temp file to simulate a chat attachment
	tmpDir := t.TempDir()
	chatDir := filepath.Join(tmpDir, "chat", exec.ID)
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatalf("failed to create chat dir: %v", err)
	}
	testFilePath := filepath.Join(chatDir, "screenshot.png")
	if err := os.WriteFile(testFilePath, []byte("fake-image-data"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create chat attachment record in database
	chatAtt := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "screenshot.png",
		FilePath:    testFilePath,
		MediaType:   "image/png",
		FileSize:    15,
	}
	if err := h.chatAttachmentRepo.Create(ctx, chatAtt); err != nil {
		t.Fatalf("failed to create chat attachment: %v", err)
	}

	// Set up the task upload directory (override uploadsDir for test)
	origUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = origUploadsDir }()

	// Create edit requests with "chat" attachment keyword
	editRequests := []service.TaskEditRequest{
		{ID: task.ID, Attachments: []string{"chat"}},
	}

	// Process chat attachments
	totalCopied, chatOnlyIDs := h.processChatAttachmentsForEdits(ctx, exec.ID, editRequests)

	// Verify attachments were copied
	if totalCopied != 1 {
		t.Fatalf("expected 1 attachment copied, got %d", totalCopied)
	}

	// Verify the task was tracked as having "chat" keyword
	if !chatOnlyIDs[task.ID] {
		t.Errorf("expected task ID to be tracked in chatOnlyIDs")
	}

	// Verify "chat" keyword was removed from the request
	if len(editRequests[0].Attachments) != 0 {
		t.Errorf("expected 'chat' keyword to be removed from attachments, got %v", editRequests[0].Attachments)
	}

	// Verify task attachment record was created in database
	attachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list task attachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 task attachment, got %d", len(attachments))
	}
	if attachments[0].FileName != "screenshot.png" {
		t.Errorf("expected filename 'screenshot.png', got %q", attachments[0].FileName)
	}
	if attachments[0].MediaType != "image/png" {
		t.Errorf("expected media type 'image/png', got %q", attachments[0].MediaType)
	}

	// Verify file was copied to task directory
	destPath := filepath.Join(tmpDir, "tasks", task.ID, "screenshot.png")
	if _, err := os.Stat(destPath); os.IsNotExist(err) {
		t.Errorf("expected file to be copied to %s, but it doesn't exist", destPath)
	}

	// Verify task prompt was updated with file reference
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}
	if !contains(updatedTask.Prompt, "Attached files from chat") {
		t.Errorf("expected task prompt to contain 'Attached files from chat', got %q", updatedTask.Prompt)
	}
	if !contains(updatedTask.Prompt, "screenshot.png") {
		t.Errorf("expected task prompt to contain 'screenshot.png', got %q", updatedTask.Prompt)
	}
}

func TestProcessChatAttachmentsForEdits_NoChatKeyword(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	// Edit requests without "chat" keyword should not trigger attachment copying
	editRequests := []service.TaskEditRequest{
		{ID: "abc123", Title: "New Title"},
	}

	totalCopied, _ := h.processChatAttachmentsForEdits(ctx, "some-exec-id", editRequests)
	if totalCopied != 0 {
		t.Errorf("expected 0 attachments copied when no 'chat' keyword, got %d", totalCopied)
	}
}

func TestProcessChatAttachmentsForEdits_NoChatAttachments(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create project and agent config
	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 4096,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Target Task",
		Prompt:    "Original prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Add screenshot to task",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// "chat" keyword but no actual chat attachments on the execution
	editRequests := []service.TaskEditRequest{
		{ID: task.ID, Attachments: []string{"chat"}},
	}

	totalCopied, _ := h.processChatAttachmentsForEdits(ctx, exec.ID, editRequests)
	if totalCopied != 0 {
		t.Errorf("expected 0 when no chat attachments exist, got %d", totalCopied)
	}

	// "chat" should still be removed
	if len(editRequests[0].Attachments) != 0 {
		t.Errorf("expected 'chat' keyword removed even with no attachments, got %v", editRequests[0].Attachments)
	}
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustParseTime: " + err.Error())
	}
	return t
}
