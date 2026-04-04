package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestParseChatTaskCreations_SingleTask(t *testing.T) {
	output := `Sure, I'll create that task for you.

[CREATE_TASK]
{"title": "Set up authentication", "prompt": "Implement user authentication using JWT tokens", "category": "backlog", "priority": 3}
[/CREATE_TASK]

Done!`

	tasks := parseChatTaskCreations(output)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "Set up authentication" {
		t.Errorf("expected title 'Set up authentication', got %q", tasks[0].Title)
	}
	if tasks[0].Prompt != "Implement user authentication using JWT tokens" {
		t.Errorf("expected prompt 'Implement user authentication using JWT tokens', got %q", tasks[0].Prompt)
	}
	if tasks[0].Category != "backlog" {
		t.Errorf("expected category 'backlog', got %q", tasks[0].Category)
	}
	if tasks[0].Priority != 3 {
		t.Errorf("expected priority 3, got %d", tasks[0].Priority)
	}
}

func TestParseChatTaskCreations_MultipleTasks(t *testing.T) {
	output := `I'll create all three tasks for you now.

[CREATE_TASK]
{"title": "Add login page", "prompt": "Create a login page with email/password fields", "category": "active"}
[/CREATE_TASK]

[CREATE_TASK]
{"title": "Add user registration", "prompt": "Create user registration form with validation", "category": "backlog", "priority": 1}
[/CREATE_TASK]

[CREATE_TASK]
{"title": "Fix password reset", "prompt": "Debug and fix the password reset flow", "category": "backlog"}
[/CREATE_TASK]

All three tasks have been created!`

	tasks := parseChatTaskCreations(output)
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Title != "Add login page" {
		t.Errorf("expected first task 'Add login page', got %q", tasks[0].Title)
	}
	if tasks[0].Category != "active" {
		t.Errorf("expected first task category 'active', got %q", tasks[0].Category)
	}
	if tasks[1].Priority != 1 {
		t.Errorf("expected second task priority 1, got %d", tasks[1].Priority)
	}
	if tasks[2].Title != "Fix password reset" {
		t.Errorf("expected third task 'Fix password reset', got %q", tasks[2].Title)
	}
}

func TestParseChatTaskCreations_NoMarkers(t *testing.T) {
	output := "Here are some tasks I'd suggest:\n1. Add auth\n2. Fix bugs\n\nWould you like me to create these?"

	tasks := parseChatTaskCreations(output)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestParseChatTaskCreations_InvalidJSON(t *testing.T) {
	output := `[CREATE_TASK]
not valid json
[/CREATE_TASK]`

	tasks := parseChatTaskCreations(output)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks for invalid JSON, got %d", len(tasks))
	}
}

func TestParseChatTaskCreations_EmptyTitle(t *testing.T) {
	output := `[CREATE_TASK]
{"title": "", "prompt": "some prompt"}
[/CREATE_TASK]`

	tasks := parseChatTaskCreations(output)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks for empty title, got %d", len(tasks))
	}
}

func TestParseChatTaskCreations_Defaults(t *testing.T) {
	output := `[CREATE_TASK]
{"title": "My task", "prompt": "Do something"}
[/CREATE_TASK]`

	tasks := parseChatTaskCreations(output)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	// Category defaulting now happens in ExecuteTaskCreationsWithReturn, not in parsing
	if tasks[0].Category != "" {
		t.Errorf("expected empty category (not yet defaulted), got %q", tasks[0].Category)
	}
	if tasks[0].Priority != 2 {
		t.Errorf("expected default priority 2, got %d", tasks[0].Priority)
	}
}

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

func TestParseChatTaskEdits_SingleEdit(t *testing.T) {
	output := `I'll update that task for you.

[EDIT_TASK]
{"id": "abc123", "title": "Updated title", "priority": 3}
[/EDIT_TASK]

Done!`

	edits := parseChatTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].ID != "abc123" {
		t.Errorf("expected id 'abc123', got %q", edits[0].ID)
	}
	if edits[0].Title != "Updated title" {
		t.Errorf("expected title 'Updated title', got %q", edits[0].Title)
	}
	if edits[0].Priority != 3 {
		t.Errorf("expected priority 3, got %d", edits[0].Priority)
	}
}

func TestParseChatTaskEdits_MultipleEdits(t *testing.T) {
	output := `Updating both tasks.

[EDIT_TASK]
{"id": "abc123", "title": "New title"}
[/EDIT_TASK]

[EDIT_TASK]
{"id": "def456", "category": "active"}
[/EDIT_TASK]`

	edits := parseChatTaskEdits(output)
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(edits))
	}
	if edits[0].ID != "abc123" {
		t.Errorf("expected first edit id 'abc123', got %q", edits[0].ID)
	}
	if edits[1].Category != "active" {
		t.Errorf("expected second edit category 'active', got %q", edits[1].Category)
	}
}

func TestParseChatTaskEdits_NoMarkers(t *testing.T) {
	output := "I can help you edit that task. Which fields would you like to change?"
	edits := parseChatTaskEdits(output)
	if len(edits) != 0 {
		t.Fatalf("expected 0 edits, got %d", len(edits))
	}
}

func TestParseChatTaskEdits_EmptyID(t *testing.T) {
	output := `[EDIT_TASK]
{"id": "", "title": "New title"}
[/EDIT_TASK]`

	edits := parseChatTaskEdits(output)
	if len(edits) != 0 {
		t.Fatalf("expected 0 edits for empty id, got %d", len(edits))
	}
}

func TestParseChatTaskEdits_InvalidJSON(t *testing.T) {
	output := `[EDIT_TASK]
not valid json
[/EDIT_TASK]`

	edits := parseChatTaskEdits(output)
	if len(edits) != 0 {
		t.Fatalf("expected 0 edits for invalid JSON, got %d", len(edits))
	}
}

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

func TestParseChatTaskEdits_MixedWithCreates(t *testing.T) {
	output := `I'll create a new task and edit the existing one.

[CREATE_TASK]
{"title": "New Task", "prompt": "Do something new", "category": "backlog"}
[/CREATE_TASK]

[EDIT_TASK]
{"id": "existing123", "priority": 1}
[/EDIT_TASK]`

	// Parse creates
	creates := parseChatTaskCreations(output)
	if len(creates) != 1 {
		t.Fatalf("expected 1 create, got %d", len(creates))
	}
	if creates[0].Title != "New Task" {
		t.Errorf("expected create title 'New Task', got %q", creates[0].Title)
	}

	// Parse edits
	edits := parseChatTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].ID != "existing123" {
		t.Errorf("expected edit id 'existing123', got %q", edits[0].ID)
	}
}

// --- Chat Attachment Edit Tests ---

func TestParseChatTaskEdits_WithAttachments(t *testing.T) {
	output := `I'll add the screenshot to that task.

[EDIT_TASK]
{"id": "abc123", "attachments": ["chat"]}
[/EDIT_TASK]

Done!`

	edits := parseChatTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].ID != "abc123" {
		t.Errorf("expected id 'abc123', got %q", edits[0].ID)
	}
	if len(edits[0].Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(edits[0].Attachments))
	}
	if edits[0].Attachments[0] != "chat" {
		t.Errorf("expected attachment 'chat', got %q", edits[0].Attachments[0])
	}
}

func TestParseChatTaskEdits_WithAttachmentsAndOtherFields(t *testing.T) {
	output := `[EDIT_TASK]
{"id": "abc123", "title": "Updated title", "attachments": ["chat"]}
[/EDIT_TASK]`

	edits := parseChatTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Title != "Updated title" {
		t.Errorf("expected title 'Updated title', got %q", edits[0].Title)
	}
	if len(edits[0].Attachments) != 1 || edits[0].Attachments[0] != "chat" {
		t.Errorf("expected attachments=['chat'], got %v", edits[0].Attachments)
	}
}

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

func TestProcessChatTaskEdits_EndToEndWithChatAttachments(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create project
	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create agent config (required for execution FK)
	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 4096,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a task to edit
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Login Fix",
		Prompt:    "Fix the login bug",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a chat execution
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Add this screenshot to the login fix task",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// Create a temp file and chat attachment
	tmpDir := t.TempDir()
	chatDir := filepath.Join(tmpDir, "chat", exec.ID)
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatalf("failed to create chat dir: %v", err)
	}
	testFilePath := filepath.Join(chatDir, "error_screenshot.png")
	if err := os.WriteFile(testFilePath, []byte("fake-png-data"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	chatAtt := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "error_screenshot.png",
		FilePath:    testFilePath,
		MediaType:   "image/png",
		FileSize:    13,
	}
	if err := h.chatAttachmentRepo.Create(ctx, chatAtt); err != nil {
		t.Fatalf("failed to create chat attachment: %v", err)
	}

	// Override uploadsDir
	origUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = origUploadsDir }()

	// Simulate AI output with [EDIT_TASK] and "chat" attachment
	aiOutput := fmt.Sprintf(`I'll add the screenshot to the login fix task.

[EDIT_TASK]
{"id": "%s", "attachments": ["chat"]}
[/EDIT_TASK]`, task.ID)

	// Process the full edit flow
	result := h.processChatTaskEdits(ctx, exec.ID, project.ID, aiOutput)

	// Verify chat attachment copy info (since only "chat" attachment was specified,
	// the edit request is skipped but attachments are still copied)
	if !contains(result, "chat attachment(s) copied to task") {
		t.Errorf("expected chat attachment copy info in output, got %q", result)
	}

	// Should NOT contain "Failed to edit" since chat-only requests are filtered out
	if contains(result, "Failed to edit") {
		t.Errorf("should not report failure for chat-only attachment edits, got %q", result)
	}

	// Verify attachment appears in task's attachment list
	attachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list task attachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 task attachment, got %d", len(attachments))
	}
	if attachments[0].FileName != "error_screenshot.png" {
		t.Errorf("expected filename 'error_screenshot.png', got %q", attachments[0].FileName)
	}
}


func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustParseTime: " + err.Error())
	}
	return t
}

func TestProcessChatScheduleTask_Daily(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create project
	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create agent config (required for execution FK)
	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 4096,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a task to schedule
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Daily Backup",
		Prompt:    "Run the daily backup",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := fmt.Sprintf(`I'll schedule that for you.

[SCHEDULE_TASK]
{"task_id": "%s", "time": "09:00", "repeat": "daily"}
[/SCHEDULE_TASK]`, task.ID)

	result := h.processChatScheduleTask(ctx, "exec1", project.ID, output)

	// Verify output contains schedule summary
	if !contains(result, "Schedule Results") {
		t.Error("expected output to contain 'Schedule Results'")
	}
	if !contains(result, "Scheduled task") {
		t.Error("expected output to contain 'Scheduled task'")
	}
	if !contains(result, "daily") {
		t.Error("expected output to contain 'daily'")
	}

	// Verify task was moved to scheduled category
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updatedTask.Category != models.CategoryScheduled {
		t.Errorf("expected task category 'scheduled', got %q", updatedTask.Category)
	}

	// Verify schedule was created
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatDaily {
		t.Errorf("expected repeat type 'daily', got %q", schedules[0].RepeatType)
	}
	if schedules[0].RepeatInterval != 1 {
		t.Errorf("expected repeat interval 1, got %d", schedules[0].RepeatInterval)
	}
	if !schedules[0].Enabled {
		t.Error("expected schedule to be enabled")
	}
}

func TestProcessChatScheduleTask_Weekly(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

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
		Title:     "Weekly Report",
		Prompt:    "Generate weekly report",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := fmt.Sprintf(`[SCHEDULE_TASK]
{"task_id": "%s", "time": "14:30", "repeat": "weekly", "days": ["mon", "fri"]}
[/SCHEDULE_TASK]`, task.ID)

	result := h.processChatScheduleTask(ctx, "exec2", project.ID, output)

	if !contains(result, "weekly on mon, fri") {
		t.Errorf("expected output to contain 'weekly on mon, fri', got: %s", result)
	}

	// Verify schedule was created with weekly type
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatWeekly {
		t.Errorf("expected repeat type 'weekly', got %q", schedules[0].RepeatType)
	}
}

func TestProcessChatScheduleTask_ByTitle(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

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
		Title:     "Nightly Cleanup",
		Prompt:    "Clean up old files",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := `[SCHEDULE_TASK]
{"title": "Nightly Cleanup", "time": "00:00", "repeat": "daily"}
[/SCHEDULE_TASK]`

	result := h.processChatScheduleTask(ctx, "exec3", project.ID, output)

	if !contains(result, "Scheduled task") {
		t.Error("expected output to contain 'Scheduled task'")
	}
	if !contains(result, "Nightly Cleanup") {
		t.Error("expected output to contain task title")
	}

	// Verify task moved to scheduled
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updatedTask.Category != models.CategoryScheduled {
		t.Errorf("expected category 'scheduled', got %q", updatedTask.Category)
	}
}

func TestProcessChatScheduleTask_InvalidTime(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

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
		Title:     "Bad Time Task",
		Prompt:    "test",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := fmt.Sprintf(`[SCHEDULE_TASK]
{"task_id": "%s", "time": "25:00", "repeat": "daily"}
[/SCHEDULE_TASK]`, task.ID)

	result := h.processChatScheduleTask(ctx, "exec4", project.ID, output)

	if !contains(result, "Invalid time") {
		t.Errorf("expected output to contain 'Invalid time', got: %s", result)
	}

	// Verify no schedule was created
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) != 0 {
		t.Errorf("expected 0 schedules, got %d", len(schedules))
	}
}

func TestProcessChatScheduleTask_TaskNotFound(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	output := `[SCHEDULE_TASK]
{"task_id": "nonexistent", "time": "09:00", "repeat": "daily"}
[/SCHEDULE_TASK]`

	result := h.processChatScheduleTask(ctx, "exec5", project.ID, output)

	if !contains(result, "Could not find task") {
		t.Errorf("expected output to contain 'Could not find task', got: %s", result)
	}
}

func TestProcessChatScheduleTask_Once(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

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
		Title:     "One-time Job",
		Prompt:    "Run once",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := fmt.Sprintf(`[SCHEDULE_TASK]
{"task_id": "%s", "time": "18:00", "repeat": "once"}
[/SCHEDULE_TASK]`, task.ID)

	result := h.processChatScheduleTask(ctx, "exec6", project.ID, output)

	if !contains(result, "once") {
		t.Errorf("expected output to contain 'once', got: %s", result)
	}

	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatOnce {
		t.Errorf("expected repeat type 'once', got %q", schedules[0].RepeatType)
	}
}

func TestProcessChatScheduleTask_WithInterval(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

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
		Title:     "Analyze Data",
		Prompt:    "Run data analysis",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := fmt.Sprintf(`[SCHEDULE_TASK]
{"task_id": "%s", "time": "01:00", "repeat": "daily", "interval": 2}
[/SCHEDULE_TASK]`, task.ID)

	result := h.processChatScheduleTask(ctx, "exec-interval", project.ID, output)

	if !contains(result, "Schedule Results") {
		t.Error("expected output to contain 'Schedule Results'")
	}
	if !contains(result, "every 2 days") {
		t.Errorf("expected output to contain 'every 2 days', got: %s", result)
	}

	// Verify schedule was created with correct interval
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatDaily {
		t.Errorf("expected repeat type 'daily', got %q", schedules[0].RepeatType)
	}
	if schedules[0].RepeatInterval != 2 {
		t.Errorf("expected repeat interval 2, got %d", schedules[0].RepeatInterval)
	}
}

func TestProcessChatScheduleTask_HoursRepeat(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

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
		Title:     "Health Check",
		Prompt:    "Run health check",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := fmt.Sprintf(`[SCHEDULE_TASK]
{"task_id": "%s", "time": "00:00", "repeat": "hours", "interval": 3}
[/SCHEDULE_TASK]`, task.ID)

	result := h.processChatScheduleTask(ctx, "exec-hours", project.ID, output)

	if !contains(result, "Schedule Results") {
		t.Error("expected output to contain 'Schedule Results'")
	}
	if !contains(result, "every 3 hours") {
		t.Errorf("expected output to contain 'every 3 hours', got: %s", result)
	}

	// Verify schedule was created with hours repeat type and interval
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatHours {
		t.Errorf("expected repeat type 'hours', got %q", schedules[0].RepeatType)
	}
	if schedules[0].RepeatInterval != 3 {
		t.Errorf("expected repeat interval 3, got %d", schedules[0].RepeatInterval)
	}
}

func TestProcessChatScheduleTask_NoMarkers(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No schedule markers here."
	result := h.processChatScheduleTask(ctx, "exec7", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

// --- List Personalities Tests ---

func TestProcessChatListPersonalities(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "Here are the available personalities.\n\n[LIST_PERSONALITIES]"
	result := h.processChatListPersonalities(ctx, "exec1", "project1", output)

	if !contains(result, "Available Personalities") {
		t.Error("expected output to contain 'Available Personalities'")
	}
	if !contains(result, "Sarcastic Engineer") {
		t.Error("expected output to contain 'Sarcastic Engineer'")
	}
	if !contains(result, "Zen Debugger") {
		t.Error("expected output to contain 'Zen Debugger'")
	}
	if !contains(result, "Current personality") {
		t.Error("expected output to contain 'Current personality'")
	}
}

func TestProcessChatListPersonalities_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatListPersonalities(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

// --- Set Personality Tests ---

func TestProcessChatSetPersonality(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := `I'll change the personality for you.

[SET_PERSONALITY]
{"personality": "pirate_captain"}
[/SET_PERSONALITY]`

	result := h.processChatSetPersonality(ctx, "exec1", "project1", output)

	if !contains(result, "Personality Settings") {
		t.Error("expected output to contain 'Personality Settings'")
	}
	if !contains(result, "Pirate Captain") {
		t.Error("expected output to contain 'Pirate Captain'")
	}
	if !contains(result, "pirate_captain") {
		t.Error("expected output to contain 'pirate_captain'")
	}

	// Verify setting was stored
	val, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil {
		t.Fatalf("failed to get personality setting: %v", err)
	}
	if val != "pirate_captain" {
		t.Errorf("expected personality setting 'pirate_captain', got %q", val)
	}
}

func TestProcessChatSetPersonality_Invalid(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := `[SET_PERSONALITY]
{"personality": "nonexistent_personality"}
[/SET_PERSONALITY]`

	result := h.processChatSetPersonality(ctx, "exec1", "project1", output)

	if !contains(result, "Unknown personality") {
		t.Errorf("expected output to contain 'Unknown personality', got: %s", result)
	}
}

func TestProcessChatSetPersonality_ResetToDefault(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	// First set a personality
	if err := h.settingsRepo.Set(ctx, "personality", "pirate_captain"); err != nil {
		t.Fatalf("failed to set personality: %v", err)
	}

	// Reset to default
	output := `[SET_PERSONALITY]
{"personality": ""}
[/SET_PERSONALITY]`

	result := h.processChatSetPersonality(ctx, "exec1", "project1", output)

	if !contains(result, "Personality Settings") {
		t.Error("expected output to contain 'Personality Settings'")
	}

	val, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil {
		t.Fatalf("failed to get personality setting: %v", err)
	}
	if val != "" {
		t.Errorf("expected personality to be reset to empty, got %q", val)
	}
}

func TestProcessChatSetPersonality_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatSetPersonality(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

// --- List Models Tests ---

func TestProcessChatListModels(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a model config
	agent := &models.LLMConfig{
		Name:     "Test Model",
		Provider: "anthropic",
		Model:    "claude-3-5-sonnet-20241022",
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	output := "Let me show you the models.\n\n[LIST_MODELS]"
	result := h.processChatListModels(ctx, "exec1", "project1", output)

	if !contains(result, "Configured Models") {
		t.Error("expected output to contain 'Configured Models'")
	}
	if !contains(result, "Test Model") {
		t.Error("expected output to contain 'Test Model'")
	}
	if !contains(result, "anthropic") {
		t.Error("expected output to contain 'anthropic'")
	}
}

func TestProcessChatListModels_DefaultModel(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "[LIST_MODELS]"
	result := h.processChatListModels(ctx, "exec1", "project1", output)

	// Migrations create a default model, so expect at least that
	if !contains(result, "Configured Models") {
		t.Errorf("expected output to contain 'Configured Models', got: %s", result)
	}
}

func TestProcessChatListModels_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatListModels(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

// --- View Settings Tests ---

func TestProcessChatViewSettings(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Set up some settings
	if err := h.settingsRepo.Set(ctx, "personality", "zen_debugger"); err != nil {
		t.Fatalf("failed to set personality: %v", err)
	}

	agent := &models.LLMConfig{
		Name:      "Sonnet",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	output := "Here are the settings.\n\n[VIEW_SETTINGS]"
	result := h.processChatViewSettings(ctx, "exec1", "project1", output)

	if !contains(result, "App Settings") {
		t.Error("expected output to contain 'App Settings'")
	}
	if !contains(result, "zen_debugger") {
		t.Error("expected output to contain 'zen_debugger'")
	}
	if !contains(result, "Configured models") {
		t.Error("expected output to contain 'Configured models'")
	}
	if !contains(result, "Sonnet") {
		t.Error("expected output to contain 'Sonnet'")
	}
	if !contains(result, "Global max workers") {
		t.Error("expected output to contain 'Global max workers'")
	}
	if !contains(result, "Per-project worker limits") {
		t.Error("expected output to contain 'Per-project worker limits'")
	}
	if !contains(result, "Per-model worker pools") {
		t.Error("expected output to contain 'Per-model worker pools'")
	}
}

func TestProcessChatViewSettings_WithWorkerConfig(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Set global workers
	if err := h.workerRepo.SetMaxWorkers(ctx, 5); err != nil {
		t.Fatalf("failed to set max workers: %v", err)
	}

	// Create a project with per-project worker limit
	maxW := 3
	project := &models.Project{Name: "Limited Project", MaxWorkers: &maxW}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a model with per-model worker pool
	agent := &models.LLMConfig{
		Name:          "Opus",
		Provider:      "anthropic",
		Model:         "claude-opus-4-20250514",
		IsDefault:     true,
		MaxWorkers:    2,
		WorkerTimeout: 300,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	output := "Settings:\n[VIEW_SETTINGS]"
	result := h.processChatViewSettings(ctx, "exec1", project.ID, output)

	if !contains(result, "Global max workers:** 5") {
		t.Errorf("expected global max workers=5, got: %s", result)
	}
	if !contains(result, "Limited Project: 3") {
		t.Errorf("expected per-project limit for Limited Project, got: %s", result)
	}
	if !contains(result, "Opus: max_workers=2, timeout=300s") {
		t.Errorf("expected per-model pool for Opus, got: %s", result)
	}
}

func TestProcessChatViewSettings_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatViewSettings(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

// --- Project Info Tests ---

func TestProcessChatProjectInfo(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "My Project", Description: "A test project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create some tasks with unique names
	taskDefs := []struct {
		title    string
		category models.TaskCategory
	}{
		{"Active Task One", models.CategoryActive},
		{"Active Task Two", models.CategoryActive},
		{"Backlog Task One", models.CategoryBacklog},
	}
	for _, td := range taskDefs {
		task := &models.Task{
			ProjectID: project.ID,
			Title:     td.title,
			Prompt:    "test",
			Category:  td.category,
			Status:    models.StatusPending,
			Priority:  2,
		}
		if err := h.taskSvc.Create(ctx, task); err != nil {
			t.Fatalf("failed to create task: %v", err)
		}
	}

	output := "Let me get the project details.\n\n[PROJECT_INFO]"
	result := h.processChatProjectInfo(ctx, "exec1", project.ID, output)

	if !contains(result, "Project Info") {
		t.Error("expected output to contain 'Project Info'")
	}
	if !contains(result, "My Project") {
		t.Error("expected output to contain 'My Project'")
	}
	if !contains(result, "A test project") {
		t.Error("expected output to contain project description")
	}
	if !contains(result, "Total tasks") {
		t.Error("expected output to contain 'Total tasks'")
	}
}

func TestProcessChatProjectInfo_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatProjectInfo(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

func TestProcessChatProjectInfo_ProjectNotFound(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "[PROJECT_INFO]"
	result := h.processChatProjectInfo(ctx, "exec1", "nonexistent", output)

	if !contains(result, "Error retrieving project details") {
		t.Errorf("expected output to contain error message, got: %s", result)
	}
}

// --- DELETE_SCHEDULE handler tests ---

func TestProcessChatDeleteSchedule_ByTaskID(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Scheduled Task", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a schedule for the task
	schedule := &models.Schedule{TaskID: task.ID, RunAt: mustParseTime("2026-03-15T09:00:00Z"), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	output := fmt.Sprintf(`[DELETE_SCHEDULE]
{"task_id": "%s"}
[/DELETE_SCHEDULE]`, task.ID)

	result := h.processChatDeleteSchedule(ctx, "exec1", project.ID, output)

	if !contains(result, "Schedule Delete Results") {
		t.Error("expected output to contain 'Schedule Delete Results'")
	}
	if !contains(result, "Deleted schedule") {
		t.Error("expected output to contain 'Deleted schedule'")
	}

	// Verify schedule was deleted
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) != 0 {
		t.Errorf("expected 0 schedules after delete, got %d", len(schedules))
	}

	// Verify task was moved back to backlog
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Errorf("expected task category 'backlog' after schedule delete, got %q", updatedTask.Category)
	}
}

func TestProcessChatDeleteSchedule_ByScheduleID(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Scheduled Task", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{TaskID: task.ID, RunAt: mustParseTime("2026-03-15T09:00:00Z"), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	output := fmt.Sprintf(`[DELETE_SCHEDULE]
{"schedule_id": "%s"}
[/DELETE_SCHEDULE]`, schedule.ID)

	result := h.processChatDeleteSchedule(ctx, "exec2", project.ID, output)

	if !contains(result, "Deleted schedule") {
		t.Errorf("expected output to contain 'Deleted schedule', got: %s", result)
	}
}

func TestProcessChatDeleteSchedule_TaskNotFound(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	output := `[DELETE_SCHEDULE]
{"task_id": "nonexistent"}
[/DELETE_SCHEDULE]`

	result := h.processChatDeleteSchedule(ctx, "exec3", project.ID, output)

	if !contains(result, "Could not find schedule") {
		t.Errorf("expected error message, got: %s", result)
	}
}

func TestProcessChatDeleteSchedule_NoSchedule(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "No Schedule Task", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	output := fmt.Sprintf(`[DELETE_SCHEDULE]
{"task_id": "%s"}
[/DELETE_SCHEDULE]`, task.ID)

	result := h.processChatDeleteSchedule(ctx, "exec4", project.ID, output)

	if !contains(result, "no schedules found") {
		t.Errorf("expected 'no schedules found' message, got: %s", result)
	}
}

func TestProcessChatDeleteSchedule_NoMarkers(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No delete markers here."
	result := h.processChatDeleteSchedule(ctx, "exec5", "project1", output)

	if result != output {
		t.Errorf("expected unchanged output when no markers, got different output")
	}
}

// --- MODIFY_SCHEDULE handler tests ---

func TestProcessChatModifySchedule_ChangeTime(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Modify Task", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{TaskID: task.ID, RunAt: mustParseTime("2026-03-15T09:00:00Z"), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	output := fmt.Sprintf(`[MODIFY_SCHEDULE]
{"task_id": "%s", "time": "14:30"}
[/MODIFY_SCHEDULE]`, task.ID)

	result := h.processChatModifySchedule(ctx, "exec1", project.ID, output)

	if !contains(result, "Schedule Modify Results") {
		t.Error("expected output to contain 'Schedule Modify Results'")
	}
	if !contains(result, "Updated schedule") {
		t.Error("expected output to contain 'Updated schedule'")
	}
	if !contains(result, "time→14:30") {
		t.Errorf("expected output to contain 'time→14:30', got: %s", result)
	}

	// Verify schedule was updated
	updatedSchedule, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to get schedule: %v", err)
	}
	localTime := updatedSchedule.RunAt.Local()
	if localTime.Hour() != 14 || localTime.Minute() != 30 {
		t.Errorf("expected time 14:30, got %02d:%02d", localTime.Hour(), localTime.Minute())
	}
}

func TestProcessChatModifySchedule_ChangeRepeat(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Repeat Task", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{TaskID: task.ID, RunAt: mustParseTime("2026-03-15T09:00:00Z"), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	output := fmt.Sprintf(`[MODIFY_SCHEDULE]
{"task_id": "%s", "repeat": "weekly"}
[/MODIFY_SCHEDULE]`, task.ID)

	result := h.processChatModifySchedule(ctx, "exec2", project.ID, output)

	if !contains(result, "repeat→weekly") {
		t.Errorf("expected output to contain 'repeat→weekly', got: %s", result)
	}

	updatedSchedule, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to get schedule: %v", err)
	}
	if updatedSchedule.RepeatType != models.RepeatWeekly {
		t.Errorf("expected repeat type 'weekly', got %q", updatedSchedule.RepeatType)
	}
}

func TestProcessChatModifySchedule_DisableSchedule(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Disable Task", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{TaskID: task.ID, RunAt: mustParseTime("2026-03-15T09:00:00Z"), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	output := fmt.Sprintf(`[MODIFY_SCHEDULE]
{"schedule_id": "%s", "enabled": false}
[/MODIFY_SCHEDULE]`, schedule.ID)

	result := h.processChatModifySchedule(ctx, "exec3", project.ID, output)

	if !contains(result, "enabled→false") {
		t.Errorf("expected output to contain 'enabled→false', got: %s", result)
	}

	updatedSchedule, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to get schedule: %v", err)
	}
	if updatedSchedule.Enabled {
		t.Error("expected schedule to be disabled")
	}
}

func TestProcessChatModifySchedule_InvalidTime(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Invalid Time Task", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{TaskID: task.ID, RunAt: mustParseTime("2026-03-15T09:00:00Z"), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	output := fmt.Sprintf(`[MODIFY_SCHEDULE]
{"task_id": "%s", "time": "25:99"}
[/MODIFY_SCHEDULE]`, task.ID)

	result := h.processChatModifySchedule(ctx, "exec4", project.ID, output)

	if !contains(result, "Invalid time") {
		t.Errorf("expected error about invalid time, got: %s", result)
	}
}

func TestProcessChatModifySchedule_NoChanges(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	agent := &models.LLMConfig{Name: "test-agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022", MaxTokens: 4096}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "No Changes Task", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{TaskID: task.ID, RunAt: mustParseTime("2026-03-15T09:00:00Z"), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	output := fmt.Sprintf(`[MODIFY_SCHEDULE]
{"task_id": "%s"}
[/MODIFY_SCHEDULE]`, task.ID)

	result := h.processChatModifySchedule(ctx, "exec5", project.ID, output)

	if !contains(result, "No changes specified") {
		t.Errorf("expected 'No changes specified' message, got: %s", result)
	}
}

func TestProcessChatModifySchedule_NoMarkers(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No modify markers here."
	result := h.processChatModifySchedule(ctx, "exec6", "project1", output)

	if result != output {
		t.Errorf("expected unchanged output when no markers, got different output")
	}
}

func TestProcessChatModifySchedule_TaskNotFound(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	output := `[MODIFY_SCHEDULE]
{"task_id": "nonexistent", "time": "14:00"}
[/MODIFY_SCHEDULE]`

	result := h.processChatModifySchedule(ctx, "exec7", project.ID, output)

	if !contains(result, "Could not find schedule") {
		t.Errorf("expected error message, got: %s", result)
	}
}

// --- Alert Marker Handler Tests ---

func TestProcessChatListAlerts(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create an alert first
	a := &models.Alert{
		ProjectID: project.ID,
		Type:      models.AlertTaskFailed,
		Severity:  models.SeverityError,
		Title:     "Test task failed",
		Message:   "Something went wrong",
	}
	if err := h.alertSvc.Create(ctx, a); err != nil {
		t.Fatalf("failed to create alert: %v", err)
	}

	output := "Here are your alerts.\n\n[LIST_ALERTS]"
	result := h.processChatListAlerts(ctx, "exec1", project.ID, output)

	if !contains(result, "Alert Results") {
		t.Error("expected output to contain 'Alert Results'")
	}
	if !contains(result, "Test task failed") {
		t.Error("expected output to contain alert title 'Test task failed'")
	}
	if !contains(result, "task_failed") {
		t.Error("expected output to contain alert type")
	}
	if !contains(result, "error") {
		t.Error("expected output to contain alert severity")
	}
}

func TestProcessChatListAlerts_Empty(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	output := "Show me alerts.\n\n[LIST_ALERTS]"
	result := h.processChatListAlerts(ctx, "exec1", project.ID, output)

	if !contains(result, "No alerts found") {
		t.Error("expected output to contain 'No alerts found'")
	}
}

func TestProcessChatListAlerts_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatListAlerts(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

func TestProcessChatCreateAlert(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	output := `I'll create that alert.

[CREATE_ALERT]
{"title": "Deploy reminder", "message": "Remember to deploy by EOD", "severity": "warning"}
[/CREATE_ALERT]`

	result := h.processChatCreateAlert(ctx, "exec1", project.ID, output)

	if !contains(result, "Alert Create Results") {
		t.Error("expected output to contain 'Alert Create Results'")
	}
	if !contains(result, "Deploy reminder") {
		t.Error("expected output to contain alert title")
	}

	// Verify alert was created
	alerts, err := h.alertSvc.ListByProject(ctx, project.ID, 100)
	if err != nil {
		t.Fatalf("failed to list alerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Title != "Deploy reminder" {
		t.Errorf("expected title 'Deploy reminder', got %q", alerts[0].Title)
	}
	if alerts[0].Severity != models.SeverityWarning {
		t.Errorf("expected severity 'warning', got %q", alerts[0].Severity)
	}
}

func TestProcessChatCreateAlert_InvalidSeverity(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	output := `[CREATE_ALERT]
{"title": "Test", "severity": "critical"}
[/CREATE_ALERT]`

	result := h.processChatCreateAlert(ctx, "exec1", project.ID, output)

	if !contains(result, "Invalid severity") {
		t.Errorf("expected invalid severity error, got: %s", result)
	}
}

func TestProcessChatCreateAlert_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatCreateAlert(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

func TestProcessChatDeleteAlert(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create an alert first
	a := &models.Alert{
		ProjectID: project.ID,
		Type:      models.AlertCustom,
		Severity:  models.SeverityInfo,
		Title:     "To be deleted",
		Message:   "This will be removed",
	}
	if err := h.alertSvc.Create(ctx, a); err != nil {
		t.Fatalf("failed to create alert: %v", err)
	}

	output := fmt.Sprintf(`[DELETE_ALERT]
{"alert_id": "%s"}
[/DELETE_ALERT]`, a.ID)

	result := h.processChatDeleteAlert(ctx, "exec1", project.ID, output)

	if !contains(result, "Alert Delete Results") {
		t.Error("expected output to contain 'Alert Delete Results'")
	}
	if !contains(result, "Deleted alert") {
		t.Error("expected output to contain 'Deleted alert'")
	}

	// Verify alert was deleted
	alerts, err := h.alertSvc.ListByProject(ctx, project.ID, 100)
	if err != nil {
		t.Fatalf("failed to list alerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts after deletion, got %d", len(alerts))
	}
}

func TestProcessChatDeleteAlert_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatDeleteAlert(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}

func TestProcessChatToggleAlert(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create an unread alert
	a := &models.Alert{
		ProjectID: project.ID,
		Type:      models.AlertCustom,
		Severity:  models.SeverityInfo,
		Title:     "Unread alert",
	}
	if err := h.alertSvc.Create(ctx, a); err != nil {
		t.Fatalf("failed to create alert: %v", err)
	}

	output := fmt.Sprintf(`[TOGGLE_ALERT]
{"alert_id": "%s"}
[/TOGGLE_ALERT]`, a.ID)

	result := h.processChatToggleAlert(ctx, "exec1", project.ID, output)

	if !contains(result, "Alert Toggle Results") {
		t.Error("expected output to contain 'Alert Toggle Results'")
	}
	if !contains(result, "Marked alert") {
		t.Error("expected output to contain 'Marked alert'")
	}

	// Verify alert was marked as read
	unreadCount, err := h.alertSvc.CountUnread(ctx, project.ID)
	if err != nil {
		t.Fatalf("failed to count unread: %v", err)
	}
	if unreadCount != 0 {
		t.Errorf("expected 0 unread alerts, got %d", unreadCount)
	}
}

func TestProcessChatToggleAlert_NoMarker(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	output := "No marker here."
	result := h.processChatToggleAlert(ctx, "exec1", "project1", output)

	if result != output {
		t.Errorf("expected output to be unchanged, got: %s", result)
	}
}
