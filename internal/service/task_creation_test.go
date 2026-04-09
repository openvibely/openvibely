package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestParseTaskCreations_SingleTask(t *testing.T) {
	output := `I'll create that task for you.

[CREATE_TASK]
{"title": "Task chaining", "prompt": "Implement task chaining where an Opus plan task can kick off a Sonnet code task", "category": "backlog", "priority": 2}
[/CREATE_TASK]

[STATUS: SUCCESS]`

	tasks := ParseTaskCreations(output)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "Task chaining" {
		t.Errorf("expected title 'Task chaining', got %q", tasks[0].Title)
	}
	if tasks[0].Category != "backlog" {
		t.Errorf("expected category 'backlog', got %q", tasks[0].Category)
	}
}

func TestParseTaskCreations_MultipleTasks(t *testing.T) {
	output := `Creating the requested tasks.

[CREATE_TASK]
{"title": "Plan mode for Opus", "prompt": "Implement plan mode", "category": "backlog"}
[/CREATE_TASK]

[CREATE_TASK]
{"title": "Code mode for Sonnet", "prompt": "Implement code execution mode", "category": "active", "priority": 1}
[/CREATE_TASK]

[STATUS: SUCCESS]`

	tasks := ParseTaskCreations(output)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Title != "Plan mode for Opus" {
		t.Errorf("expected first task 'Plan mode for Opus', got %q", tasks[0].Title)
	}
	if tasks[1].Category != "active" {
		t.Errorf("expected second task category 'active', got %q", tasks[1].Category)
	}
}

func TestParseTaskCreations_NoMarkers(t *testing.T) {
	output := "I completed the task successfully.\n\n[STATUS: SUCCESS]"
	tasks := ParseTaskCreations(output)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestParseTaskCreations_Defaults(t *testing.T) {
	output := `[CREATE_TASK]
{"title": "My task", "prompt": "Do something"}
[/CREATE_TASK]`

	tasks := ParseTaskCreations(output)
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

func TestParseTaskCreations_InvalidJSON(t *testing.T) {
	output := `[CREATE_TASK]
not valid json
[/CREATE_TASK]`

	tasks := ParseTaskCreations(output)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks for invalid JSON, got %d", len(tasks))
	}
}

func TestParseTaskCreations_EmptyTitle(t *testing.T) {
	output := `[CREATE_TASK]
{"title": "", "prompt": "some prompt"}
[/CREATE_TASK]`

	tasks := ParseTaskCreations(output)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks for empty title, got %d", len(tasks))
	}
}

func TestExecuteTaskCreations(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	requests := []TaskCreationRequest{
		{Title: "Sub-task One", Prompt: "Do sub-task one", Category: "backlog", Priority: 2},
		{Title: "Sub-task Two", Prompt: "Do sub-task two", Category: "backlog", Priority: 3},
	}

	summary := ExecuteTaskCreations(context.Background(), requests, project.ID, taskSvc)

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Created 2 task(s)") {
		t.Errorf("expected summary to contain 'Created 2 task(s)', got %q", summary)
	}

	// Verify summary includes task IDs for clickable links
	if !strings.Contains(summary, "[TASK_ID:") {
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
	for _, task := range tasks {
		expectedMarker := "[TASK_ID:" + task.ID + "]"
		if !strings.Contains(summary, expectedMarker) {
			t.Errorf("expected summary to contain task ID marker %q, got %q", expectedMarker, summary)
		}
	}
}

func TestExecuteTaskCreations_Empty(t *testing.T) {
	summary := ExecuteTaskCreations(context.Background(), nil, "proj1", nil)
	if summary != "" {
		t.Errorf("expected empty summary for nil requests, got %q", summary)
	}
}

// TestExecuteTaskCreations_SummaryMatchesFrontendRegex verifies that the summary
// format matches the JavaScript regex used by convertTaskLinksInMessage to convert
// [TASK_ID:xxx] markers into clickable links. If this format changes, the frontend
// JS must be updated too (and vice versa).
func TestExecuteTaskCreations_SummaryMatchesFrontendRegex(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	requests := []TaskCreationRequest{
		{Title: "My Test Task", Prompt: "Do something", Category: "backlog", Priority: 2},
	}

	createdTasks, summary := ExecuteTaskCreationsWithReturn(context.Background(), requests, project.ID, taskSvc)
	if len(createdTasks) != 1 {
		t.Fatalf("expected 1 created task, got %d", len(createdTasks))
	}
	taskID := createdTasks[0].ID

	// The frontend JS regex: /(?:-\s*)?"([^"]+)"\s*(?:\(([^)]+)\)\s*)?\[TASK_ID:([^\]]+)\]/g
	// This expects the format: - "Title" (category) [TASK_ID:id] (with optional leading dash)
	// Verify the exact expected pattern exists in the summary
	expectedPattern := fmt.Sprintf(`- "My Test Task" (backlog) [TASK_ID:%s]`, taskID)
	if !strings.Contains(summary, expectedPattern) {
		t.Errorf("summary does not match expected frontend pattern.\nExpected to contain: %s\nGot: %s", expectedPattern, summary)
	}
}

func TestTaskCreationInstructions_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"CREATE_TASK marker", "[CREATE_TASK]"},
		{"/CREATE_TASK marker", "[/CREATE_TASK]"},
		{"title field", `"title"`},
		{"prompt field", `"prompt"`},
		{"category field", `"category"`},
		{"active category", `"active"`},
		{"backlog category", `"backlog"`},
		{"MUST output marker", "MUST output the [CREATE_TASK] block"},
		{"thinking not enough", "Thinking about it is not enough"},
		{"ONLY way to create", "ONLY way to create a task"},
	}

	for _, r := range required {
		if !strings.Contains(llmprompt.TaskCreationInstructions, r.content) {
			t.Errorf("llmprompt.TaskCreationInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

func TestBuildTaskContextString(t *testing.T) {
	tasks := []models.Task{
		{ID: "abc123", Title: "Auth system", Category: models.CategoryActive, Status: models.StatusRunning, Priority: 2, Prompt: "Implement auth"},
		{ID: "def456", Title: "Fix bugs", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 3},
	}

	result := BuildTaskContextString(tasks)
	if !strings.Contains(result, "Auth system") {
		t.Errorf("expected result to contain 'Auth system', got %q", result)
	}
	if !strings.Contains(result, "[active, running") {
		t.Errorf("expected result to contain '[active, running', got %q", result)
	}
	if !strings.Contains(result, "Fix bugs") {
		t.Errorf("expected result to contain 'Fix bugs', got %q", result)
	}
	// Verify task IDs are included for editing
	if !strings.Contains(result, "[ID:abc123]") {
		t.Errorf("expected result to contain '[ID:abc123]', got %q", result)
	}
	if !strings.Contains(result, "[ID:def456]") {
		t.Errorf("expected result to contain '[ID:def456]', got %q", result)
	}
}

func TestBuildTaskContextString_Empty(t *testing.T) {
	result := BuildTaskContextString(nil)
	if !strings.Contains(result, "No tasks exist") {
		t.Errorf("expected empty message, got %q", result)
	}
}

// --- Task Edit Tests ---

func TestParseTaskEdits_SingleEdit(t *testing.T) {
	output := `I'll update that task for you.

[EDIT_TASK]
{"id": "abc123", "title": "Updated title", "priority": 3}
[/EDIT_TASK]

Done!`

	edits := ParseTaskEdits(output)
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

func TestParseTaskEdits_MultipleEdits(t *testing.T) {
	output := `Updating both tasks.

[EDIT_TASK]
{"id": "abc123", "title": "New title 1"}
[/EDIT_TASK]

[EDIT_TASK]
{"id": "def456", "category": "active", "priority": 1}
[/EDIT_TASK]`

	edits := ParseTaskEdits(output)
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

func TestParseTaskEdits_NoMarkers(t *testing.T) {
	output := "I updated the task in a different way."
	edits := ParseTaskEdits(output)
	if len(edits) != 0 {
		t.Fatalf("expected 0 edits, got %d", len(edits))
	}
}

func TestParseTaskEdits_EmptyID(t *testing.T) {
	output := `[EDIT_TASK]
{"id": "", "title": "New title"}
[/EDIT_TASK]`

	edits := ParseTaskEdits(output)
	if len(edits) != 0 {
		t.Fatalf("expected 0 edits for empty id, got %d", len(edits))
	}
}

func TestParseTaskEdits_InvalidJSON(t *testing.T) {
	output := `[EDIT_TASK]
not valid json
[/EDIT_TASK]`

	edits := ParseTaskEdits(output)
	if len(edits) != 0 {
		t.Fatalf("expected 0 edits for invalid JSON, got %d", len(edits))
	}
}

func TestExecuteTaskEdits(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

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

	requests := []TaskEditRequest{
		{ID: task.ID, Title: "Updated Title", Priority: 4},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected summary to contain 'Edited 1 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "[TASK_EDITED:") {
		t.Errorf("expected summary to contain [TASK_EDITED: marker, got %q", summary)
	}
	if !strings.Contains(summary, "title") {
		t.Errorf("expected summary to mention 'title' change, got %q", summary)
	}
	if !strings.Contains(summary, "priority") {
		t.Errorf("expected summary to mention 'priority' change, got %q", summary)
	}

	// Verify task was actually updated in database
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", updated.Title)
	}
	if updated.Priority != 4 {
		t.Errorf("expected priority 4, got %d", updated.Priority)
	}
	// Prompt should remain unchanged
	if updated.Prompt != "Original prompt" {
		t.Errorf("expected prompt to remain 'Original prompt', got %q", updated.Prompt)
	}
}

func TestExecuteTaskEdits_TaskNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	requests := []TaskEditRequest{
		{ID: "nonexistent", Title: "New Title"},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if !strings.Contains(summary, "Failed to edit 1 task(s)") {
		t.Errorf("expected failure summary, got %q", summary)
	}
	if !strings.Contains(summary, "not found") {
		t.Errorf("expected 'not found' in summary, got %q", summary)
	}
}

func TestExecuteTaskEdits_WrongProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project1 := &models.Project{Name: "Project 1"}
	if err := projectRepo.Create(context.Background(), project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	project2 := &models.Project{Name: "Project 2"}
	if err := projectRepo.Create(context.Background(), project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create task in project1
	task := &models.Task{
		ProjectID: project1.ID,
		Title:     "Task in Project 1",
		Prompt:    "Some prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Try to edit from project2 context
	requests := []TaskEditRequest{
		{ID: task.ID, Title: "Hacked Title"},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project2.ID, taskSvc, nil, "")

	if !strings.Contains(summary, "Failed to edit 1 task(s)") {
		t.Errorf("expected failure summary for wrong project, got %q", summary)
	}
	if !strings.Contains(summary, "different project") {
		t.Errorf("expected 'different project' in summary, got %q", summary)
	}
}

func TestExecuteTaskEdits_NoChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "My Task",
		Prompt:    "Do something",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Send edit with same values
	requests := []TaskEditRequest{
		{ID: task.ID, Title: "My Task"},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if !strings.Contains(summary, "no changes") {
		t.Errorf("expected 'no changes' in summary, got %q", summary)
	}
}

func TestExecuteTaskEdits_Empty(t *testing.T) {
	summary := ExecuteTaskEdits(context.Background(), nil, "proj1", nil, nil, "")
	if summary != "" {
		t.Errorf("expected empty summary for nil requests, got %q", summary)
	}
}

func TestExecuteTaskEdits_CategoryChange(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Move Me",
		Prompt:    "A task to move",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	requests := []TaskEditRequest{
		{ID: task.ID, Category: "active"},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if !strings.Contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected edit success, got %q", summary)
	}
	if !strings.Contains(summary, "category") {
		t.Errorf("expected 'category' change mention, got %q", summary)
	}

	// Verify category was updated
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.Category != models.CategoryActive {
		t.Errorf("expected category 'active', got %q", updated.Category)
	}
}

func TestParseTaskEdits_AgentID(t *testing.T) {
	output := `I'll reassign this task to a different agent.

[EDIT_TASK]
{"id": "abc123", "agent_id": "5ad73e20dc6444bc22ae7acd5c5c121f"}
[/EDIT_TASK]

Done!`

	edits := ParseTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].ID != "abc123" {
		t.Errorf("expected id 'abc123', got %q", edits[0].ID)
	}
	if edits[0].AgentID != "5ad73e20dc6444bc22ae7acd5c5c121f" {
		t.Errorf("expected agent_id '5ad73e20dc6444bc22ae7acd5c5c121f', got %q", edits[0].AgentID)
	}
}

func TestParseTaskEdits_AgentConfigID(t *testing.T) {
	output := `I'll reassign this task to a different agent.

[EDIT_TASK]
{"id": "abc123", "agent_config_id": "5ad73e20dc6444bc22ae7acd5c5c121f"}
[/EDIT_TASK]

Done!`

	edits := ParseTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].ID != "abc123" {
		t.Errorf("expected id 'abc123', got %q", edits[0].ID)
	}
	if edits[0].AgentConfigID != "5ad73e20dc6444bc22ae7acd5c5c121f" {
		t.Errorf("expected agent_config_id '5ad73e20dc6444bc22ae7acd5c5c121f', got %q", edits[0].AgentConfigID)
	}
}

func TestExecuteTaskEdits_AgentReassignment(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create agent configs for testing
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	initialAgent := &models.LLMConfig{Name: "Initial Agent", Provider: "anthropic"}
	if err := llmConfigRepo.Create(context.Background(), initialAgent); err != nil {
		t.Fatalf("failed to create initial agent: %v", err)
	}
	newAgent := &models.LLMConfig{Name: "New Agent", Provider: "anthropic"}
	if err := llmConfigRepo.Create(context.Background(), newAgent); err != nil {
		t.Fatalf("failed to create new agent: %v", err)
	}

	// Create a task with an initial agent
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Task with Agent",
		Prompt:    "Test prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
		AgentID:   &initialAgent.ID,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Reassign to a different agent
	requests := []TaskEditRequest{
		{ID: task.ID, AgentID: newAgent.ID},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected summary to contain 'Edited 1 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "agent") {
		t.Errorf("expected summary to mention 'agent' change, got %q", summary)
	}

	// Verify agent was actually updated in database
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.AgentID == nil {
		t.Fatal("expected AgentID to be set, got nil")
	}
	if *updated.AgentID != newAgent.ID {
		t.Errorf("expected agent_id %q, got %q", newAgent.ID, *updated.AgentID)
	}
}

func TestExecuteTaskEdits_AgentReassignmentNoChange(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create an agent config
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "Same Agent", Provider: "anthropic"}
	if err := llmConfigRepo.Create(context.Background(), agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a task with an agent already assigned
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Task with Agent",
		Prompt:    "Test prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Try to "reassign" to the same agent (should detect no changes)
	requests := []TaskEditRequest{
		{ID: task.ID, AgentID: agent.ID},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	// Should report "no changes" since agent_id is already set to this value
	if !strings.Contains(summary, "no changes") {
		t.Errorf("expected 'no changes' when reassigning to same agent, got %q", summary)
	}
}

func TestExecuteTaskEdits_AgentConfigIDAlias(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create agent configs for testing
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	initialAgent := &models.LLMConfig{Name: "Initial Agent", Provider: "anthropic"}
	if err := llmConfigRepo.Create(context.Background(), initialAgent); err != nil {
		t.Fatalf("failed to create initial agent: %v", err)
	}
	newAgent := &models.LLMConfig{Name: "New Agent", Provider: "anthropic"}
	if err := llmConfigRepo.Create(context.Background(), newAgent); err != nil {
		t.Fatalf("failed to create new agent: %v", err)
	}

	// Create a task with an initial agent
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Task with Agent",
		Prompt:    "Test prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
		AgentID:   &initialAgent.ID,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Reassign using agent_config_id field (alias)
	requests := []TaskEditRequest{
		{ID: task.ID, AgentConfigID: newAgent.ID},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected summary to contain 'Edited 1 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "agent") {
		t.Errorf("expected summary to mention 'agent' change, got %q", summary)
	}

	// Verify agent was actually updated in database
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.AgentID == nil {
		t.Fatal("expected AgentID to be set, got nil")
	}
	if *updated.AgentID != newAgent.ID {
		t.Errorf("expected agent_id %q (using agent_config_id alias), got %q", newAgent.ID, *updated.AgentID)
	}
}

func TestExecuteTaskEdits_AgentAssignmentFromNil(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create an agent config
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "First Agent", Provider: "anthropic"}
	if err := llmConfigRepo.Create(context.Background(), agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a task with no agent assigned (nil)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Task without Agent",
		Prompt:    "Test prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
		AgentID:   nil,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Assign an agent for the first time
	requests := []TaskEditRequest{
		{ID: task.ID, AgentID: agent.ID},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if !strings.Contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected edit success, got %q", summary)
	}
	if !strings.Contains(summary, "agent") {
		t.Errorf("expected 'agent' change mention, got %q", summary)
	}

	// Verify agent was assigned
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.AgentID == nil {
		t.Fatal("expected AgentID to be set, got nil")
	}
	if *updated.AgentID != agent.ID {
		t.Errorf("expected agent_id %q, got %q", agent.ID, *updated.AgentID)
	}
}

func TestExecuteTaskEdits_InvalidCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Invalid Cat Task",
		Prompt:    "Test",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Try invalid category - should be ignored, resulting in no changes
	requests := []TaskEditRequest{
		{ID: task.ID, Category: "invalid_category"},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, nil, "")

	if !strings.Contains(summary, "no changes") {
		t.Errorf("expected 'no changes' for invalid category, got %q", summary)
	}

	// Verify category was NOT changed
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.Category != models.CategoryBacklog {
		t.Errorf("expected category to remain 'backlog', got %q", updated.Category)
	}
}

func TestExecuteTaskEdits_WithAttachments(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Attachment Task",
		Prompt:    "Original prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a temp file to use as an attachment
	tmpDir := t.TempDir()
	uploadsDir := t.TempDir()
	tmpFile := fmt.Sprintf("%s/test-screenshot.png", tmpDir)
	if err := os.WriteFile(tmpFile, []byte("fake png data"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	requests := []TaskEditRequest{
		{ID: task.ID, Title: "Updated With Attachment", Attachments: []string{tmpFile}},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, attachmentRepo, uploadsDir)

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected summary to contain 'Edited 1 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "attachments") {
		t.Errorf("expected summary to mention attachments, got %q", summary)
	}

	// Verify task was updated
	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.Title != "Updated With Attachment" {
		t.Errorf("expected title 'Updated With Attachment', got %q", updated.Title)
	}
	// Verify prompt was updated with attachment reference
	if !strings.Contains(updated.Prompt, "test-screenshot.png") {
		t.Errorf("expected prompt to reference attachment file, got %q", updated.Prompt)
	}

	// Verify attachment record was created
	attachments, err := attachmentRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].FileName != "test-screenshot.png" {
		t.Errorf("expected attachment filename 'test-screenshot.png', got %q", attachments[0].FileName)
	}

	// Verify file was copied
	if _, err := os.Stat(attachments[0].FilePath); os.IsNotExist(err) {
		t.Error("expected attachment file to exist on disk")
	}
}

func TestExecuteTaskEdits_AttachmentsNonexistentFile(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Attachment Task",
		Prompt:    "Original prompt",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	uploadsDir := t.TempDir()
	requests := []TaskEditRequest{
		{
			ID:          task.ID,
			Title:       "Updated Title",
			Attachments: []string{"/nonexistent/file.png"},
		},
	}

	summary := ExecuteTaskEdits(context.Background(), requests, project.ID, taskSvc, attachmentRepo, uploadsDir)

	// Should still succeed (title updated) but skip the missing file
	if !strings.Contains(summary, "Edited 1 task(s)") {
		t.Errorf("expected edit success, got %q", summary)
	}
	if !strings.Contains(summary, "title") {
		t.Errorf("expected title change in summary, got %q", summary)
	}

	// No attachments should have been created
	attachments, _ := attachmentRepo.ListByTask(context.Background(), task.ID)
	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments for missing file, got %d", len(attachments))
	}
}

func TestParseTaskEdits_WithAttachments(t *testing.T) {
	output := `[EDIT_TASK]
{"id": "abc123", "attachments": ["/path/to/screenshot.png", "/path/to/doc.pdf"]}
[/EDIT_TASK]`

	edits := ParseTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if len(edits[0].Attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(edits[0].Attachments))
	}
	if edits[0].Attachments[0] != "/path/to/screenshot.png" {
		t.Errorf("expected first attachment '/path/to/screenshot.png', got %q", edits[0].Attachments[0])
	}
}

func TestChatTaskEditInstructions_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"EDIT_TASK marker", "[EDIT_TASK]"},
		{"/EDIT_TASK marker", "[/EDIT_TASK]"},
		{"id field", `"id"`},
		{"title field", `"title"`},
		{"prompt field", `"prompt"`},
		{"category field", `"category"`},
		{"priority field", `"priority"`},
		{"agent_id field", `"agent_id"`},
		{"MUST output marker", "MUST output the [EDIT_TASK] block"},
		{"model change heading", "CHANGING A TASK'S MODEL"},
		{"model change is supported", "fully supported feature"},
		{"do not modify prompt", "Do NOT modify the task's prompt to mention the model"},
		{"model change example", `"Change task X to use Opus"`},
	}

	for _, r := range required {
		if !strings.Contains(llmprompt.ChatTaskEditInstructions, r.content) {
			t.Errorf("llmprompt.ChatTaskEditInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

// --- Task Execution Tests ---

func TestParseTaskExecutions_SingleRequest(t *testing.T) {
	output := `I'll execute all feature tasks for you.

[EXECUTE_TASKS]
{"tags": ["feature"]}
[/EXECUTE_TASKS]

Done!`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 1 {
		t.Fatalf("expected 1 execution request, got %d", len(execReqs))
	}
	if len(execReqs[0].Tags) != 1 {
		t.Errorf("expected 1 tag, got %d", len(execReqs[0].Tags))
	}
	if execReqs[0].Tags[0] != "feature" {
		t.Errorf("expected tag 'feature', got %q", execReqs[0].Tags[0])
	}
}

func TestParseTaskExecutions_MultipleTagsWithPriority(t *testing.T) {
	output := `Executing high-priority bug and feature tasks.

[EXECUTE_TASKS]
{"tags": ["bug", "feature"], "min_priority": 3}
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 1 {
		t.Fatalf("expected 1 execution request, got %d", len(execReqs))
	}
	if len(execReqs[0].Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(execReqs[0].Tags))
	}
	if execReqs[0].MinPriority != 3 {
		t.Errorf("expected min_priority 3, got %d", execReqs[0].MinPriority)
	}
}

func TestParseTaskExecutions_NoMarkers(t *testing.T) {
	output := "I'll execute tasks manually without markers."
	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 0 {
		t.Fatalf("expected 0 execution requests, got %d", len(execReqs))
	}
}

func TestParseTaskExecutions_NoTagsNoPriority(t *testing.T) {
	output := `[EXECUTE_TASKS]
{"tags": []}
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 0 {
		t.Fatalf("expected 0 execution requests for empty tags and no priority, got %d", len(execReqs))
	}
}

func TestParseTaskExecutions_ByTaskID(t *testing.T) {
	output := `[EXECUTE_TASKS]
{"task_id": "abc123"}
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 1 {
		t.Fatalf("expected 1 execution request, got %d", len(execReqs))
	}
	if execReqs[0].TaskID != "abc123" {
		t.Errorf("expected task_id abc123, got %q", execReqs[0].TaskID)
	}
}

func TestParseTaskExecutions_ByTitle(t *testing.T) {
	output := `[EXECUTE_TASKS]
{"title": "Fix login bug"}
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 1 {
		t.Fatalf("expected 1 execution request, got %d", len(execReqs))
	}
	if execReqs[0].Title != "Fix login bug" {
		t.Errorf("expected title \"Fix login bug\", got %q", execReqs[0].Title)
	}
}

func TestParseTaskExecutions_PriorityOnly(t *testing.T) {
	output := `Executing all urgent tasks.

[EXECUTE_TASKS]
{"min_priority": 4}
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 1 {
		t.Fatalf("expected 1 execution request for priority-only, got %d", len(execReqs))
	}
	if len(execReqs[0].Tags) != 0 {
		t.Errorf("expected 0 tags, got %d", len(execReqs[0].Tags))
	}
	if execReqs[0].MinPriority != 4 {
		t.Errorf("expected min_priority 4, got %d", execReqs[0].MinPriority)
	}
}

func TestParseTaskExecutions_EmptyTagsWithPriority(t *testing.T) {
	output := `[EXECUTE_TASKS]
{"tags": [], "min_priority": 3}
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 1 {
		t.Fatalf("expected 1 execution request for empty tags with priority, got %d", len(execReqs))
	}
	if execReqs[0].MinPriority != 3 {
		t.Errorf("expected min_priority 3, got %d", execReqs[0].MinPriority)
	}
}

func TestParseTaskExecutions_WithIncludeCompleted(t *testing.T) {
	output := `[EXECUTE_TASKS]
{"tags": ["bug"], "include_completed": true}
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 1 {
		t.Fatalf("expected 1 execution request, got %d", len(execReqs))
	}
	if !execReqs[0].IncludeCompleted {
		t.Error("expected include_completed=true to be parsed")
	}
}

func TestParseTaskExecutions_InvalidJSON(t *testing.T) {
	output := `[EXECUTE_TASKS]
not valid json
[/EXECUTE_TASKS]`

	execReqs := ParseTaskExecutions(output)
	if len(execReqs) != 0 {
		t.Fatalf("expected 0 execution requests for invalid JSON, got %d", len(execReqs))
	}
}

func TestExecuteTaskExecutions(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil) // No workers, just testing submission
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create tasks with different tags in backlog
	featureTask1 := &models.Task{
		ProjectID: project.ID,
		Title:     "Feature A",
		Prompt:    "Build feature A",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), featureTask1); err != nil {
		t.Fatalf("failed to create feature task 1: %v", err)
	}

	featureTask2 := &models.Task{
		ProjectID: project.ID,
		Title:     "Feature B",
		Prompt:    "Build feature B",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  3,
	}
	if err := taskRepo.Create(context.Background(), featureTask2); err != nil {
		t.Fatalf("failed to create feature task 2: %v", err)
	}

	bugTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Bug Fix",
		Prompt:    "Fix critical bug",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagBug,
		Priority:  4,
	}
	if err := taskRepo.Create(context.Background(), bugTask); err != nil {
		t.Fatalf("failed to create bug task: %v", err)
	}

	// Execute all feature tasks
	requests := []TaskExecutionRequest{
		{Tags: []string{"feature"}, MinPriority: 0},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Executed 2 task(s)") {
		t.Errorf("expected summary to contain 'Executed 2 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "Feature A") {
		t.Errorf("expected summary to contain 'Feature A', got %q", summary)
	}
	if !strings.Contains(summary, "Feature B") {
		t.Errorf("expected summary to contain 'Feature B', got %q", summary)
	}
	if strings.Contains(summary, "Bug Fix") {
		t.Errorf("expected summary to NOT contain 'Bug Fix', got %q", summary)
	}

	// Verify TASK_ID markers include category for clickable link conversion
	expectedMarker1 := fmt.Sprintf("(backlog) [TASK_ID:%s]", featureTask1.ID)
	if !strings.Contains(summary, expectedMarker1) {
		t.Errorf("expected summary to contain category in TASK_ID marker %q, got %q", expectedMarker1, summary)
	}

	// Verify feature tasks were moved to active
	featureTask1Updated, _ := taskRepo.GetByID(context.Background(), featureTask1.ID)
	if featureTask1Updated.Category != models.CategoryActive {
		t.Errorf("expected feature task 1 to be moved to active, got %q", featureTask1Updated.Category)
	}

	featureTask2Updated, _ := taskRepo.GetByID(context.Background(), featureTask2.ID)
	if featureTask2Updated.Category != models.CategoryActive {
		t.Errorf("expected feature task 2 to be moved to active, got %q", featureTask2Updated.Category)
	}

	// Verify bug task remained in backlog
	bugTaskUpdated, _ := taskRepo.GetByID(context.Background(), bugTask.ID)
	if bugTaskUpdated.Category != models.CategoryBacklog {
		t.Errorf("expected bug task to remain in backlog, got %q", bugTaskUpdated.Category)
	}
}

func TestExecuteTaskExecutions_ByTaskID_ExecutesOnlyTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	target := &models.Task{
		ProjectID: project.ID,
		Title:     "Run me",
		Prompt:    "Run this task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), target); err != nil {
		t.Fatalf("failed to create target task: %v", err)
	}

	other := &models.Task{
		ProjectID: project.ID,
		Title:     "Do not run",
		Prompt:    "Leave this task alone",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), other); err != nil {
		t.Fatalf("failed to create other task: %v", err)
	}

	requests := []TaskExecutionRequest{
		{TaskID: target.ID, Tags: []string{"feature"}, MinPriority: 1},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)
	if !strings.Contains(summary, "Executed 1 task(s)") {
		t.Fatalf("expected summary to contain Executed 1 task(s), got %q", summary)
	}
	if !strings.Contains(summary, "Run me") {
		t.Fatalf("expected summary to contain target task title, got %q", summary)
	}
	if strings.Contains(summary, "Do not run") {
		t.Fatalf("expected summary to exclude non-target task, got %q", summary)
	}

	targetUpdated, _ := taskRepo.GetByID(context.Background(), target.ID)
	if targetUpdated.Category != models.CategoryActive {
		t.Errorf("expected target task to move to active, got %q", targetUpdated.Category)
	}
	otherUpdated, _ := taskRepo.GetByID(context.Background(), other.ID)
	if otherUpdated.Category != models.CategoryBacklog {
		t.Errorf("expected non-target task to remain backlog, got %q", otherUpdated.Category)
	}
}

func TestExecuteTaskExecutions_ByTitle_ExecutesOnlyTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	target := &models.Task{
		ProjectID: project.ID,
		Title:     "Exact run target",
		Prompt:    "Run this by name",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), target); err != nil {
		t.Fatalf("failed to create target task: %v", err)
	}

	other := &models.Task{
		ProjectID: project.ID,
		Title:     "Different title task",
		Prompt:    "Do not run this one",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), other); err != nil {
		t.Fatalf("failed to create other task: %v", err)
	}

	requests := []TaskExecutionRequest{
		{Title: "Exact run target"},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)
	if !strings.Contains(summary, "Executed 1 task(s)") {
		t.Fatalf("expected summary to contain Executed 1 task(s), got %q", summary)
	}
	if !strings.Contains(summary, "Exact run target") {
		t.Fatalf("expected summary to contain target task title, got %q", summary)
	}
	if strings.Contains(summary, "Different title task") {
		t.Fatalf("expected summary to exclude non-target task, got %q", summary)
	}

	targetUpdated, _ := taskRepo.GetByID(context.Background(), target.ID)
	if targetUpdated.Category != models.CategoryActive {
		t.Errorf("expected title-targeted task to move to active, got %q", targetUpdated.Category)
	}
	otherUpdated, _ := taskRepo.GetByID(context.Background(), other.ID)
	if otherUpdated.Category != models.CategoryBacklog {
		t.Errorf("expected non-target task to remain backlog, got %q", otherUpdated.Category)
	}
}

func TestExecuteTaskExecutions_WithPriorityFilter(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create feature tasks with different priorities
	lowPriorityTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Low Priority Feature",
		Prompt:    "Not urgent",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  1,
	}
	if err := taskRepo.Create(context.Background(), lowPriorityTask); err != nil {
		t.Fatalf("failed to create low priority task: %v", err)
	}

	highPriorityTask := &models.Task{
		ProjectID: project.ID,
		Title:     "High Priority Feature",
		Prompt:    "Very urgent",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  4,
	}
	if err := taskRepo.Create(context.Background(), highPriorityTask); err != nil {
		t.Fatalf("failed to create high priority task: %v", err)
	}

	// Execute only high priority feature tasks (priority >= 3)
	requests := []TaskExecutionRequest{
		{Tags: []string{"feature"}, MinPriority: 3},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if !strings.Contains(summary, "Executed 1 task(s)") {
		t.Errorf("expected summary to contain 'Executed 1 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "High Priority Feature") {
		t.Errorf("expected summary to contain 'High Priority Feature', got %q", summary)
	}
	if strings.Contains(summary, "Low Priority Feature") {
		t.Errorf("expected summary to NOT contain 'Low Priority Feature', got %q", summary)
	}

	// Verify only high priority task was moved
	highPriorityUpdated, _ := taskRepo.GetByID(context.Background(), highPriorityTask.ID)
	if highPriorityUpdated.Category != models.CategoryActive {
		t.Errorf("expected high priority task to be moved to active, got %q", highPriorityUpdated.Category)
	}

	lowPriorityUpdated, _ := taskRepo.GetByID(context.Background(), lowPriorityTask.ID)
	if lowPriorityUpdated.Category != models.CategoryBacklog {
		t.Errorf("expected low priority task to remain in backlog, got %q", lowPriorityUpdated.Category)
	}
}

func TestExecuteTaskExecutions_NoMatchingTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// No tasks with feature tag
	requests := []TaskExecutionRequest{
		{Tags: []string{"feature"}, MinPriority: 0},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if !strings.Contains(summary, "Failed") {
		t.Errorf("expected failure section in summary, got %q", summary)
	}
	if !strings.Contains(summary, "No tasks found matching") {
		t.Errorf("expected 'No tasks found matching' message, got %q", summary)
	}
	if !strings.Contains(summary, "tags=[feature]") {
		t.Errorf("expected error message to include tags, got %q", summary)
	}
}

func TestExecuteTaskExecutions_InvalidTag(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Invalid tag
	requests := []TaskExecutionRequest{
		{Tags: []string{"invalid_tag"}, MinPriority: 0},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if !strings.Contains(summary, "Failed") {
		t.Errorf("expected failure section, got %q", summary)
	}
	if !strings.Contains(summary, "No valid tags") {
		t.Errorf("expected 'No valid tags' message, got %q", summary)
	}
}

func TestExecuteTaskExecutions_Empty(t *testing.T) {
	summary := ExecuteTaskExecutions(context.Background(), nil, "proj1", nil)
	if summary != "" {
		t.Errorf("expected empty summary for nil requests, got %q", summary)
	}
}

func TestExecuteTaskExecutions_NoMatchingTasksWithPriority(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a low priority feature task
	lowPriorityTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Low Priority Feature",
		Prompt:    "Not urgent",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  1,
	}
	if err := taskRepo.Create(context.Background(), lowPriorityTask); err != nil {
		t.Fatalf("failed to create low priority task: %v", err)
	}

	// Try to execute feature tasks with priority >= 4 (should find none)
	requests := []TaskExecutionRequest{
		{Tags: []string{"feature"}, MinPriority: 4},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	// Verify error message includes both tags AND priority filter
	if !strings.Contains(summary, "Failed") {
		t.Errorf("expected failure section in summary, got %q", summary)
	}
	if !strings.Contains(summary, "No tasks found matching") {
		t.Errorf("expected 'No tasks found matching' message, got %q", summary)
	}
	if !strings.Contains(summary, "tags=[feature]") {
		t.Errorf("expected error message to include tags, got %q", summary)
	}
	if !strings.Contains(summary, "priority>=4") {
		t.Errorf("expected error message to include priority filter, got %q", summary)
	}

	// Verify the low priority task was NOT moved to active
	updated, _ := taskRepo.GetByID(context.Background(), lowPriorityTask.ID)
	if updated.Category != models.CategoryBacklog {
		t.Errorf("expected task to remain in backlog, got %q", updated.Category)
	}
}

func TestExecuteTaskExecutions_MultipleTagsWithPriorityNoMatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create low priority tasks with feature and bug tags
	featureTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Low Priority Feature",
		Prompt:    "Feature",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  2,
	}
	if err := taskRepo.Create(context.Background(), featureTask); err != nil {
		t.Fatalf("failed to create feature task: %v", err)
	}

	bugTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Low Priority Bug",
		Prompt:    "Bug fix",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagBug,
		Priority:  1,
	}
	if err := taskRepo.Create(context.Background(), bugTask); err != nil {
		t.Fatalf("failed to create bug task: %v", err)
	}

	// Try to execute feature OR bug tasks with priority >= 4 (should find none)
	requests := []TaskExecutionRequest{
		{Tags: []string{"feature", "bug"}, MinPriority: 4},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	// Verify error message is clear about the combined filter
	if !strings.Contains(summary, "Failed") {
		t.Errorf("expected failure section in summary, got %q", summary)
	}
	if !strings.Contains(summary, "No tasks found matching") {
		t.Errorf("expected 'No tasks found matching' message, got %q", summary)
	}
	if !strings.Contains(summary, "tags=[feature bug]") {
		t.Errorf("expected error message to include both tags, got %q", summary)
	}
	if !strings.Contains(summary, "priority>=4") {
		t.Errorf("expected error message to include priority filter, got %q", summary)
	}
}

func TestExecuteTaskExecutions_PriorityOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create tasks with different priorities and tags
	urgentFeature := &models.Task{
		ProjectID: project.ID,
		Title:     "Urgent Feature",
		Prompt:    "Build urgent feature",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  4,
	}
	if err := taskRepo.Create(context.Background(), urgentFeature); err != nil {
		t.Fatalf("failed to create urgent feature task: %v", err)
	}

	urgentBug := &models.Task{
		ProjectID: project.ID,
		Title:     "Urgent Bug",
		Prompt:    "Fix urgent bug",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagBug,
		Priority:  4,
	}
	if err := taskRepo.Create(context.Background(), urgentBug); err != nil {
		t.Fatalf("failed to create urgent bug task: %v", err)
	}

	urgentNoTag := &models.Task{
		ProjectID: project.ID,
		Title:     "Urgent No Tag",
		Prompt:    "Urgent task without tag",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  4,
	}
	if err := taskRepo.Create(context.Background(), urgentNoTag); err != nil {
		t.Fatalf("failed to create urgent no-tag task: %v", err)
	}

	lowPriorityTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Low Priority Task",
		Prompt:    "Not urgent",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  1,
	}
	if err := taskRepo.Create(context.Background(), lowPriorityTask); err != nil {
		t.Fatalf("failed to create low priority task: %v", err)
	}

	// Execute all urgent tasks (priority >= 4) regardless of tag
	requests := []TaskExecutionRequest{
		{MinPriority: 4},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(summary, "Executed 3 task(s)") {
		t.Errorf("expected summary to contain 'Executed 3 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "Urgent Feature") {
		t.Errorf("expected summary to contain 'Urgent Feature', got %q", summary)
	}
	if !strings.Contains(summary, "Urgent Bug") {
		t.Errorf("expected summary to contain 'Urgent Bug', got %q", summary)
	}
	if !strings.Contains(summary, "Urgent No Tag") {
		t.Errorf("expected summary to contain 'Urgent No Tag', got %q", summary)
	}
	if strings.Contains(summary, "Low Priority Task") {
		t.Errorf("expected summary to NOT contain 'Low Priority Task', got %q", summary)
	}

	// Verify all urgent tasks were moved to active
	for _, id := range []string{urgentFeature.ID, urgentBug.ID, urgentNoTag.ID} {
		updated, _ := taskRepo.GetByID(context.Background(), id)
		if updated.Category != models.CategoryActive {
			t.Errorf("expected task %s to be moved to active, got %q", id, updated.Category)
		}
	}

	// Verify low priority task remained in backlog
	lowUpdated, _ := taskRepo.GetByID(context.Background(), lowPriorityTask.ID)
	if lowUpdated.Category != models.CategoryBacklog {
		t.Errorf("expected low priority task to remain in backlog, got %q", lowUpdated.Category)
	}
}

func TestExecuteTaskExecutions_PriorityOnlyNoMatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create only low priority tasks
	lowPriorityTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Low Priority Task",
		Prompt:    "Not urgent",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		Priority:  1,
	}
	if err := taskRepo.Create(context.Background(), lowPriorityTask); err != nil {
		t.Fatalf("failed to create low priority task: %v", err)
	}

	// Try to execute tasks with priority >= 4 (should find none)
	requests := []TaskExecutionRequest{
		{MinPriority: 4},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if !strings.Contains(summary, "Failed") {
		t.Errorf("expected failure section in summary, got %q", summary)
	}
	if !strings.Contains(summary, "No tasks found matching") {
		t.Errorf("expected 'No tasks found matching' message, got %q", summary)
	}
	if !strings.Contains(summary, "priority>=4") {
		t.Errorf("expected error message to include priority filter, got %q", summary)
	}
}

func TestExecuteTaskExecutions_CompletedBacklogTasksExcludedByDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create urgent tasks with completed status in backlog (reproduces the real bug)
	completedUrgent1 := &models.Task{
		ProjectID: project.ID,
		Title:     "Completed Urgent Task 1",
		Prompt:    "Fix urgent bug",
		Category:  models.CategoryBacklog,
		Status:    models.StatusCompleted,
		Tag:       models.TagBug,
		Priority:  4,
	}
	if err := taskRepo.Create(context.Background(), completedUrgent1); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	completedUrgent2 := &models.Task{
		ProjectID: project.ID,
		Title:     "Completed Urgent Task 2",
		Prompt:    "Fix another urgent bug",
		Category:  models.CategoryBacklog,
		Status:    models.StatusCompleted,
		Tag:       models.TagBug,
		Priority:  4,
	}
	if err := taskRepo.Create(context.Background(), completedUrgent2); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Also create a pending low-priority task (should NOT be included)
	lowPriority := &models.Task{
		ProjectID: project.ID,
		Title:     "Low Priority Task",
		Prompt:    "Not urgent",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  1,
	}
	if err := taskRepo.Create(context.Background(), lowPriority); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Execute all urgent tasks (priority >= 4) — should NOT include completed ones by default
	requests := []TaskExecutionRequest{
		{MinPriority: 4},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if !strings.Contains(summary, "No tasks found matching") {
		t.Errorf("expected no matches when completed tasks are excluded by default, got %q", summary)
	}

	// Verify completed tasks were NOT moved to active or reset
	for _, id := range []string{completedUrgent1.ID, completedUrgent2.ID} {
		updated, _ := taskRepo.GetByID(context.Background(), id)
		if updated.Category != models.CategoryBacklog {
			t.Errorf("expected task %s to remain in backlog, got %q", id, updated.Category)
		}
		if updated.Status != models.StatusCompleted {
			t.Errorf("expected task %s to remain completed, got %q", id, updated.Status)
		}
	}

	// Verify low priority task remained in backlog
	lowUpdated, _ := taskRepo.GetByID(context.Background(), lowPriority.ID)
	if lowUpdated.Category != models.CategoryBacklog {
		t.Errorf("expected low priority task to remain in backlog, got %q", lowUpdated.Category)
	}
}

func TestExecuteTaskExecutions_CompletedCategoryTasksExcludedByDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task that has been completed and moved to "completed" category
	// (this is what ExecuteTaskWithAgent does after successful execution)
	bugTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Fix login bug",
		Prompt:    "Fix the login bug",
		Category:  models.CategoryCompleted, // Moved here after execution
		Status:    models.StatusCompleted,
		Priority:  2,
		Tag:       models.TagBug,
	}
	if err := taskRepo.Create(context.Background(), bugTask); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Try to execute bug tasks — should NOT include completed tasks by default
	requests := []TaskExecutionRequest{
		{Tags: []string{"bug"}, MinPriority: 0},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)

	if !strings.Contains(summary, "No tasks found matching") {
		t.Errorf("expected no matches when completed category tasks are excluded by default, got %q", summary)
	}

	// Verify task was not reactivated
	updated, err := taskRepo.GetByID(context.Background(), bugTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.Category != models.CategoryCompleted {
		t.Errorf("expected task to remain in completed category, got %q", updated.Category)
	}
	if updated.Status != models.StatusCompleted {
		t.Errorf("expected task to remain completed, got %q", updated.Status)
	}
}

func TestExecuteTaskExecutions_CompletedCategoryTasksIncludedWhenExplicitlyRequested(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	bugTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Fix login bug",
		Prompt:    "Fix the login bug",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Priority:  2,
		Tag:       models.TagBug,
	}
	if err := taskRepo.Create(context.Background(), bugTask); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	requests := []TaskExecutionRequest{
		{Tags: []string{"bug"}, IncludeCompleted: true},
	}

	summary := ExecuteTaskExecutions(context.Background(), requests, project.ID, taskSvc)
	if !strings.Contains(summary, "Executed 1 task(s)") {
		t.Errorf("expected summary to contain 'Executed 1 task(s)', got %q", summary)
	}
	if !strings.Contains(summary, "Fix login bug") {
		t.Errorf("expected summary to contain 'Fix login bug', got %q", summary)
	}

	updated, err := taskRepo.GetByID(context.Background(), bugTask.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updated.Category != models.CategoryActive {
		t.Errorf("expected task to be moved to active, got %q", updated.Category)
	}
	if updated.Status != models.StatusPending {
		t.Errorf("expected task to be reset to pending, got %q", updated.Status)
	}
}

func TestBuildModelContextString(t *testing.T) {
	configs := []models.LLMConfig{
		{ID: "abc123", Name: "Opus Agent", Model: "claude-opus-4-20250514", Provider: "anthropic", IsDefault: true},
		{ID: "def456", Name: "Sonnet Agent", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
	}

	result := BuildModelContextString(configs)

	// Should include model IDs for use in agent_id field
	if !strings.Contains(result, "[ID:abc123]") {
		t.Errorf("expected result to contain '[ID:abc123]', got %q", result)
	}
	if !strings.Contains(result, "[ID:def456]") {
		t.Errorf("expected result to contain '[ID:def456]', got %q", result)
	}
	// Should mention EDIT_TASK so the AI knows to use it
	if !strings.Contains(result, "[EDIT_TASK]") {
		t.Errorf("expected result to reference [EDIT_TASK], got %q", result)
	}
	// Should include model names
	if !strings.Contains(result, "Opus Agent") {
		t.Errorf("expected result to contain 'Opus Agent', got %q", result)
	}
	// Should mark default model
	if !strings.Contains(result, "(default)") {
		t.Errorf("expected result to contain '(default)', got %q", result)
	}
}

func TestBuildModelContextString_Empty(t *testing.T) {
	result := BuildModelContextString(nil)
	if result != "" {
		t.Errorf("expected empty string for nil configs, got %q", result)
	}
}

func TestChatTaskExecutionInstructions_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"EXECUTE_TASKS marker", "[EXECUTE_TASKS]"},
		{"/EXECUTE_TASKS marker", "[/EXECUTE_TASKS]"},
		{"task_id field", `"task_id"`},
		{"title field", `"title"`},
		{"tags field", `"tags"`},
		{"min_priority field", `"min_priority"`},
		{"include_completed field", `"include_completed"`},
		{"feature tag example", `"feature"`},
		{"bug tag example", `"bug"`},
		{"MUST output marker", "MUST output the [EXECUTE_TASKS] block"},
		{"tags optional", "optional"},
		{"priority-only example", `"min_priority": 4`},
		{"single-task example", `{"task_id": "abc123"}`},
		{"explicit completed rerun example", `"include_completed": true`},
	}

	for _, r := range required {
		if !strings.Contains(llmprompt.ChatTaskExecutionInstructions, r.content) {
			t.Errorf("llmprompt.ChatTaskExecutionInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

func TestParseTaskCreations_WithChainConfig(t *testing.T) {
	output := `I'll create a planning task that chains to implementation.

[CREATE_TASK]
{"title": "Plan the API", "prompt": "Create a detailed plan for the REST API.", "category": "active", "chain": {"enabled": true, "trigger": "on_completion", "child_title": "Implement the API", "child_prompt_prefix": "Based on the plan above, implement:", "child_category": "active"}}
[/CREATE_TASK]`

	tasks := ParseTaskCreations(output)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Chain == nil {
		t.Fatal("expected chain config to be parsed")
	}
	if !tasks[0].Chain.Enabled {
		t.Error("expected chain to be enabled")
	}
	if tasks[0].Chain.Trigger != "on_completion" {
		t.Errorf("expected trigger 'on_completion', got %q", tasks[0].Chain.Trigger)
	}
	if tasks[0].Chain.ChildTitle != "Implement the API" {
		t.Errorf("expected child title 'Implement the API', got %q", tasks[0].Chain.ChildTitle)
	}
	if tasks[0].Chain.ChildPromptPrefix != "Based on the plan above, implement:" {
		t.Errorf("expected child prompt prefix, got %q", tasks[0].Chain.ChildPromptPrefix)
	}
	if tasks[0].Chain.ChildCategory != "active" {
		t.Errorf("expected child category 'active', got %q", tasks[0].Chain.ChildCategory)
	}
}

func TestParseTaskCreations_WithNestedChainConfig(t *testing.T) {
	output := `Creating a multi-step chain.

[CREATE_TASK]
{"title": "Step 1: Research", "prompt": "Research the problem.", "category": "active", "chain": {"enabled": true, "trigger": "on_completion", "child_title": "Step 2: Design", "child_prompt_prefix": "Based on research:", "child_category": "active", "child_chain_config": {"enabled": true, "trigger": "on_completion", "child_title": "Step 3: Implement", "child_prompt_prefix": "Based on design:", "child_category": "active"}}}
[/CREATE_TASK]`

	tasks := ParseTaskCreations(output)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Chain == nil {
		t.Fatal("expected chain config")
	}
	if tasks[0].Chain.ChildChainConfig == nil {
		t.Fatal("expected nested child chain config")
	}
	if tasks[0].Chain.ChildChainConfig.ChildTitle != "Step 3: Implement" {
		t.Errorf("expected nested child title 'Step 3: Implement', got %q", tasks[0].Chain.ChildChainConfig.ChildTitle)
	}
}

func TestParseTaskCreations_WithoutChainConfig(t *testing.T) {
	output := `[CREATE_TASK]
{"title": "Simple task", "prompt": "Do something.", "category": "backlog"}
[/CREATE_TASK]`

	tasks := ParseTaskCreations(output)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Chain != nil {
		t.Error("expected no chain config for simple task")
	}
}

func TestExecuteTaskCreations_WithChainConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	ctx := context.Background()
	requests := []TaskCreationRequest{
		{
			Title:    "Plan the feature",
			Prompt:   "Create a detailed plan.",
			Category: "backlog",
			Priority: 3,
			Chain: &models.ChainConfiguration{
				Enabled:           true,
				Trigger:           "on_completion",
				ChildTitle:        "Implement the feature",
				ChildPromptPrefix: "Based on the plan:",
				ChildCategory:     "active",
			},
		},
	}

	createdTasks, summary := ExecuteTaskCreationsWithReturn(ctx, requests, "default", taskSvc)
	if len(createdTasks) != 1 {
		t.Fatalf("expected 1 created task, got %d", len(createdTasks))
	}
	if !strings.Contains(summary, "Created 1 task") {
		t.Errorf("expected summary to contain 'Created 1 task', got %q", summary)
	}
	if !strings.Contains(summary, `chains to: "Implement the feature"`) {
		t.Errorf("expected summary to show chain info, got %q", summary)
	}

	// Verify the chain config was stored on the task
	task, err := taskSvc.GetByID(ctx, createdTasks[0].ID)
	if err != nil {
		t.Fatalf("error getting task: %v", err)
	}
	chainCfg, err := task.ParseChainConfig()
	if err != nil {
		t.Fatalf("error parsing chain config: %v", err)
	}
	if !chainCfg.Enabled {
		t.Error("expected chain config to be enabled")
	}
	if chainCfg.Trigger != "on_completion" {
		t.Errorf("expected trigger 'on_completion', got %q", chainCfg.Trigger)
	}
	if chainCfg.ChildTitle != "Implement the feature" {
		t.Errorf("expected child title 'Implement the feature', got %q", chainCfg.ChildTitle)
	}
}

// TestParseTaskCreations_ToolCallChainConfig simulates the runtime tool-call path
// where JSON input is wrapped into a [CREATE_TASK] marker and parsed back.
// This is the exact flow used when the LLM calls the create_task tool in orchestrate mode.
// Regression test for: chain config must be fully preserved through the
// buildToolMarker → ParseTaskCreations roundtrip so no follow-up edit_task is needed.
func TestParseTaskCreations_ToolCallChainConfig(t *testing.T) {
	// Simulate the JSON payload a model would send via create_task tool call
	// with full chain config (as defined by the tool schema)
	toolInput := `{"title":"Compute 1+1 and save result to file","prompt":"Compute 1+1 and write the result to result.txt","category":"active","chain":{"enabled":true,"trigger":"on_completion","child_title":"Compute x+1 from parent output","child_prompt_prefix":"Read x from result.txt and compute x+1, saving the answer to final.txt","child_category":"active"}}`

	// Simulate buildToolMarker: wrap the JSON input as a CREATE_TASK marker
	marker := "[CREATE_TASK]\n" + toolInput + "\n[/CREATE_TASK]"

	tasks := ParseTaskCreations(marker)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	req := tasks[0]
	if req.Title != "Compute 1+1 and save result to file" {
		t.Errorf("title mismatch: %q", req.Title)
	}
	if req.Category != "active" {
		t.Errorf("category mismatch: %q", req.Category)
	}

	// Chain must be fully parsed — no follow-up edit needed
	if req.Chain == nil {
		t.Fatal("chain config was not parsed from tool call input — LLM would need edit_task to fix this")
	}
	if !req.Chain.Enabled {
		t.Error("chain.enabled should be true")
	}
	if req.Chain.Trigger != "on_completion" {
		t.Errorf("chain.trigger = %q, want on_completion", req.Chain.Trigger)
	}
	if req.Chain.ChildTitle != "Compute x+1 from parent output" {
		t.Errorf("chain.child_title = %q, want 'Compute x+1 from parent output'", req.Chain.ChildTitle)
	}
	if req.Chain.ChildPromptPrefix == "" {
		t.Error("chain.child_prompt_prefix should not be empty")
	}
	if req.Chain.ChildCategory != "active" {
		t.Errorf("chain.child_category = %q, want active", req.Chain.ChildCategory)
	}
}

// TestExecuteTaskCreations_ToolCallChainConfig verifies that a tool-call-style
// chain config creates both parent and blocked child in one call with no edits.
func TestExecuteTaskCreations_ToolCallChainConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	ctx := context.Background()
	requests := []TaskCreationRequest{
		{
			Title:    "Compute 1+1",
			Prompt:   "Compute 1+1 and write result to file.",
			Category: "active",
			Priority: 2,
			Chain: &models.ChainConfiguration{
				Enabled:           true,
				Trigger:           "on_completion",
				ChildTitle:        "Compute x+1 using parent output",
				ChildPromptPrefix: "Read x from result.txt and compute x+1.",
				ChildCategory:     "active",
			},
		},
	}

	createdTasks, summary := ExecuteTaskCreationsWithReturn(ctx, requests, "default", taskSvc)
	if len(createdTasks) != 1 {
		t.Fatalf("expected 1 created task, got %d", len(createdTasks))
	}

	// Summary should show chain info
	if !strings.Contains(summary, `chains to: "Compute x+1 using parent output"`) {
		t.Errorf("summary missing chain info: %q", summary)
	}

	// Verify parent task has chain config stored
	parentTask, err := taskSvc.GetByID(ctx, createdTasks[0].ID)
	if err != nil {
		t.Fatalf("get parent task: %v", err)
	}
	chainCfg, err := parentTask.ParseChainConfig()
	if err != nil {
		t.Fatalf("parse chain config: %v", err)
	}
	if !chainCfg.Enabled {
		t.Error("parent chain config should be enabled")
	}
	if chainCfg.ChildTitle != "Compute x+1 using parent output" {
		t.Errorf("child_title = %q", chainCfg.ChildTitle)
	}
	if chainCfg.ChildPromptPrefix != "Read x from result.txt and compute x+1." {
		t.Errorf("child_prompt_prefix = %q", chainCfg.ChildPromptPrefix)
	}

	// Verify blocked child was pre-created
	allTasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var blockedChild *models.Task
	for i := range allTasks {
		if allTasks[i].ParentTaskID != nil && *allTasks[i].ParentTaskID == parentTask.ID {
			blockedChild = &allTasks[i]
			break
		}
	}
	if blockedChild == nil {
		t.Fatal("expected blocked child task to be pre-created")
	}
	if blockedChild.Status != models.StatusBlocked {
		t.Errorf("blocked child status = %q, want blocked", blockedChild.Status)
	}
	if blockedChild.Title != "Compute x+1 using parent output" {
		t.Errorf("blocked child title = %q", blockedChild.Title)
	}
}

func TestParseTaskEdits_WithChainConfig(t *testing.T) {
	output := `I'll add chaining to that task.

[EDIT_TASK]
{"id": "abc123", "chain": {"enabled": true, "trigger": "on_completion", "child_title": "Deploy changes", "child_prompt_prefix": "Deploy the following:", "child_category": "active"}}
[/EDIT_TASK]`

	edits := ParseTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].ID != "abc123" {
		t.Errorf("expected id 'abc123', got %q", edits[0].ID)
	}
	if edits[0].Chain == nil {
		t.Fatal("expected chain config to be parsed")
	}
	if !edits[0].Chain.Enabled {
		t.Error("expected chain to be enabled")
	}
	if edits[0].Chain.Trigger != "on_completion" {
		t.Errorf("expected trigger 'on_completion', got %q", edits[0].Chain.Trigger)
	}
	if edits[0].Chain.ChildTitle != "Deploy changes" {
		t.Errorf("expected child title 'Deploy changes', got %q", edits[0].Chain.ChildTitle)
	}
}

func TestParseTaskEdits_DisableChain(t *testing.T) {
	output := `[EDIT_TASK]
{"id": "abc123", "chain": {"enabled": false}}
[/EDIT_TASK]`

	edits := ParseTaskEdits(output)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].Chain == nil {
		t.Fatal("expected chain config to be parsed")
	}
	if edits[0].Chain.Enabled {
		t.Error("expected chain to be disabled")
	}
}

func TestExecuteTaskEdits_WithChainConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	ctx := context.Background()

	// Create a task first
	task := &models.Task{
		ProjectID: "default",
		Title:     "Plan the feature",
		Prompt:    "Create a plan.",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
		Priority:  2,
	}
	if err := taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("error creating task: %v", err)
	}

	// Edit the task to add chaining
	edits := []TaskEditRequest{
		{
			ID: task.ID,
			Chain: &models.ChainConfiguration{
				Enabled:           true,
				Trigger:           "on_completion",
				ChildTitle:        "Implement the feature",
				ChildPromptPrefix: "Based on the plan:",
				ChildCategory:     "active",
			},
		},
	}

	summary := ExecuteTaskEdits(ctx, edits, "default", taskSvc, nil, "")
	if !strings.Contains(summary, "Edited 1 task") {
		t.Errorf("expected summary to contain 'Edited 1 task', got %q", summary)
	}
	if !strings.Contains(summary, "chain_config") {
		t.Errorf("expected summary to mention chain_config change, got %q", summary)
	}

	// Verify chain config was saved
	updated, err := taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("error getting task: %v", err)
	}
	chainCfg, err := updated.ParseChainConfig()
	if err != nil {
		t.Fatalf("error parsing chain config: %v", err)
	}
	if !chainCfg.Enabled {
		t.Error("expected chain config to be enabled")
	}
	if chainCfg.ChildTitle != "Implement the feature" {
		t.Errorf("expected child title 'Implement the feature', got %q", chainCfg.ChildTitle)
	}

	// Verify a blocked child was pre-created in the backlog for visibility
	blockedChild, err := taskRepo.FindBlockedChildByParent(ctx, task.ID)
	if err != nil {
		t.Fatalf("error finding blocked child: %v", err)
	}
	if blockedChild == nil {
		t.Fatal("expected blocked child to be pre-created when chain config is enabled via edit")
	}
	if blockedChild.Title != "Implement the feature" {
		t.Errorf("expected blocked child title 'Implement the feature', got %q", blockedChild.Title)
	}
	if blockedChild.Category != models.CategoryBacklog {
		t.Errorf("expected blocked child category=backlog, got %s", blockedChild.Category)
	}
	if blockedChild.Status != models.StatusBlocked {
		t.Errorf("expected blocked child status=blocked, got %s", blockedChild.Status)
	}
}

func TestExecuteTaskEdits_DisableChain(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	ctx := context.Background()

	// Create a task with chaining enabled
	task := &models.Task{
		ProjectID: "default",
		Title:     "Plan the feature",
		Prompt:    "Create a plan.",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
		Priority:  2,
	}
	task.SetChainConfig(&models.ChainConfiguration{
		Enabled:    true,
		Trigger:    "on_completion",
		ChildTitle: "Implement",
	})
	if err := taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("error creating task: %v", err)
	}

	// Pre-create a blocked child (simulates what ExecuteTaskEdits or handler does)
	blockedChild, _ := taskRepo.FindBlockedChildByParent(ctx, task.ID)
	if blockedChild == nil {
		// create_task path already pre-creates; if not, manually create one
		blockedChild = &models.Task{
			ProjectID:    "default",
			Title:        "Implement",
			Category:     models.CategoryBacklog,
			Priority:     2,
			Status:       models.StatusBlocked,
			Prompt:       "Waiting for parent task to complete...",
			ParentTaskID: &task.ID,
		}
		if err := taskSvc.Create(ctx, blockedChild); err != nil {
			t.Fatalf("error creating blocked child: %v", err)
		}
	}

	// Verify chain is initially enabled and blocked child exists
	created, _ := taskSvc.GetByID(ctx, task.ID)
	chainCfg, _ := created.ParseChainConfig()
	if !chainCfg.Enabled {
		t.Fatal("expected chain to be enabled initially")
	}
	existing, _ := taskRepo.FindBlockedChildByParent(ctx, task.ID)
	if existing == nil {
		t.Fatal("expected blocked child to exist before disabling chain")
	}

	// Disable chaining via edit
	edits := []TaskEditRequest{
		{
			ID:    task.ID,
			Chain: &models.ChainConfiguration{Enabled: false},
		},
	}

	summary := ExecuteTaskEdits(ctx, edits, "default", taskSvc, nil, "")
	if !strings.Contains(summary, "Edited 1 task") {
		t.Errorf("expected edit success, got %q", summary)
	}

	// Verify chain config is now disabled
	updated, _ := taskSvc.GetByID(ctx, task.ID)
	chainCfg, _ = updated.ParseChainConfig()
	if chainCfg.Enabled {
		t.Error("expected chain config to be disabled after edit")
	}

	// Verify blocked child was removed when chain was disabled
	remaining, _ := taskRepo.FindBlockedChildByParent(ctx, task.ID)
	if remaining != nil {
		t.Error("expected blocked child to be removed when chain config is disabled via edit")
	}
}

func TestBuildTaskContextWithModels_ShowsChainInfo(t *testing.T) {
	tasks := []models.Task{
		{
			ID:       "task1",
			Title:    "Plan API",
			Category: models.CategoryActive,
			Status:   models.StatusPending,
			Priority: 2,
			Prompt:   "Plan the API.",
		},
		{
			ID:       "task2",
			Title:    "Implement API",
			Category: models.CategoryBacklog,
			Status:   models.StatusPending,
			Priority: 2,
			Prompt:   "Implement the API.",
		},
	}

	// Set chain config on first task
	tasks[0].SetChainConfig(&models.ChainConfiguration{
		Enabled:    true,
		Trigger:    "on_completion",
		ChildTitle: "Implement API",
	})

	// Set parent on second task
	parentID := "task1"
	tasks[1].ParentTaskID = &parentID

	context := BuildTaskContextWithModels(tasks, nil)
	if !strings.Contains(context, "chain:on_completion") {
		t.Errorf("expected context to show chain trigger, got %q", context)
	}
	if !strings.Contains(context, `→"Implement API"`) {
		t.Errorf("expected context to show chain child title, got %q", context)
	}
	if !strings.Contains(context, "parent:task1") {
		t.Errorf("expected context to show parent task ID, got %q", context)
	}
}

func TestChatTaskChainingInstructions_Content(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"CREATE_TASK example", `[CREATE_TASK]`},
		{"EDIT_TASK example", `[EDIT_TASK]`},
		{"enabled field", `"enabled"`},
		{"trigger field", `"trigger"`},
		{"on_completion trigger", `"on_completion"`},
		{"on_planning_complete trigger", `"on_planning_complete"`},
		{"child_title field", `"child_title"`},
		{"child_prompt_prefix field", `"child_prompt_prefix"`},
		{"child_category field", `"child_category"`},
		{"sequential detection", "do X first, then Y"},
		{"plan then implement", "plan first, then implement"},
		{"disable chaining", `"enabled": false`},
		{"multi-step chains", "child_chain_config"},
		{"CRITICAL instruction", "CRITICAL"},
	}

	for _, r := range required {
		if !strings.Contains(llmprompt.ChatTaskChainingInstructions, r.content) {
			t.Errorf("llmprompt.ChatTaskChainingInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

// --- View Thread Tests ---

func TestParseViewThread_ByID(t *testing.T) {
	output := `Let me show you that task's thread history.

[VIEW_TASK_CHAT]
{"task_id": "abc123"}
[/VIEW_TASK_CHAT]`

	requests := ParseViewThread(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].TaskID != "abc123" {
		t.Errorf("expected task_id 'abc123', got %q", requests[0].TaskID)
	}
}

func TestParseViewThread_ByTitle(t *testing.T) {
	output := `[VIEW_TASK_CHAT]
{"title": "Fix login bug"}
[/VIEW_TASK_CHAT]`

	requests := ParseViewThread(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Title != "Fix login bug" {
		t.Errorf("expected title 'Fix login bug', got %q", requests[0].Title)
	}
}

func TestParseViewThread_NoMarkers(t *testing.T) {
	output := "No view markers here."
	requests := ParseViewThread(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestParseViewThread_EmptyIDAndTitle(t *testing.T) {
	output := `[VIEW_TASK_CHAT]
{"task_id": "", "title": ""}
[/VIEW_TASK_CHAT]`

	requests := ParseViewThread(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for empty id and title, got %d", len(requests))
	}
}

func TestParseViewThread_InvalidJSON(t *testing.T) {
	output := `[VIEW_TASK_CHAT]
not valid json
[/VIEW_TASK_CHAT]`

	requests := ParseViewThread(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for invalid JSON, got %d", len(requests))
	}
}

// --- Send To Task Tests ---

func TestParseSendToTask_ByID(t *testing.T) {
	output := `I'll send that message to the task.

[SEND_TO_TASK]
{"task_id": "abc123", "message": "Please add error handling"}
[/SEND_TO_TASK]`

	requests := ParseSendToTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].TaskID != "abc123" {
		t.Errorf("expected task_id 'abc123', got %q", requests[0].TaskID)
	}
	if requests[0].Message != "Please add error handling" {
		t.Errorf("expected message 'Please add error handling', got %q", requests[0].Message)
	}
}

func TestParseSendToTask_ByTitle(t *testing.T) {
	output := `[SEND_TO_TASK]
{"title": "API endpoint task", "message": "Also add pagination support"}
[/SEND_TO_TASK]`

	requests := ParseSendToTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Title != "API endpoint task" {
		t.Errorf("expected title 'API endpoint task', got %q", requests[0].Title)
	}
	if requests[0].Message != "Also add pagination support" {
		t.Errorf("expected message 'Also add pagination support', got %q", requests[0].Message)
	}
}

func TestParseSendToTask_NoMessage(t *testing.T) {
	output := `[SEND_TO_TASK]
{"task_id": "abc123", "message": ""}
[/SEND_TO_TASK]`

	requests := ParseSendToTask(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for empty message, got %d", len(requests))
	}
}

func TestParseSendToTask_NoMarkers(t *testing.T) {
	output := "No send markers here."
	requests := ParseSendToTask(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestParseSendToTask_MultipleSends(t *testing.T) {
	output := `Sending messages to both tasks.

[SEND_TO_TASK]
{"task_id": "abc123", "message": "Add tests"}
[/SEND_TO_TASK]

[SEND_TO_TASK]
{"task_id": "def456", "message": "Fix the bug"}
[/SEND_TO_TASK]`

	requests := ParseSendToTask(output)
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	if requests[0].TaskID != "abc123" {
		t.Errorf("expected first task_id 'abc123', got %q", requests[0].TaskID)
	}
	if requests[1].TaskID != "def456" {
		t.Errorf("expected second task_id 'def456', got %q", requests[1].TaskID)
	}
}

func TestParseSendToTask_InvalidJSON(t *testing.T) {
	output := `[SEND_TO_TASK]
not valid json
[/SEND_TO_TASK]`

	requests := ParseSendToTask(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for invalid JSON, got %d", len(requests))
	}
}

// --- System Prompt Instruction Tests ---

func TestChatThreadViewInstructions_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"Thread view marker", "[VIEW_TASK_CHAT]"},
		{"Thread view closing marker", "[/VIEW_TASK_CHAT]"},
		{"task_id field", `"task_id"`},
		{"title field", `"title"`},
		{"MUST output marker", "MUST output the [VIEW_TASK_CHAT] block"},
		{"show me the thread", "Show me the thread"},
		{"thinking not enough", "thinking about it or describing what you will do is NOT enough"},
		{"ONLY way", "ONLY way to view a task's thread"},
		{"concrete example", "Let me retrieve the thread history"},
		{"CRITICAL section", "CRITICAL"},
	}

	for _, r := range required {
		if !strings.Contains(llmprompt.ChatThreadViewInstructions, r.content) {
			t.Errorf("llmprompt.ChatThreadViewInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

func TestChatThreadSendInstructions_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"SEND_TO_TASK marker", "[SEND_TO_TASK]"},
		{"/SEND_TO_TASK marker", "[/SEND_TO_TASK]"},
		{"task_id field", `"task_id"`},
		{"message field", `"message"`},
		{"MUST output marker", "MUST output the [SEND_TO_TASK] block"},
		{"automatically reactivated", "automatically reactivated"},
		{"thinking not enough", "thinking about it or describing what you will do is NOT enough"},
		{"ONLY way", "ONLY way to send a message to a task"},
		{"concrete example", "I'll send that instruction to the API task"},
		{"each request needs marker", "Every follow-up request about a task"},
		{"CRITICAL section", "CRITICAL"},
	}

	for _, r := range required {
		if !strings.Contains(llmprompt.ChatThreadSendInstructions, r.content) {
			t.Errorf("llmprompt.ChatThreadSendInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

func TestChatMarkerReinforcement_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"CRITICAL REMINDER", "CRITICAL REMINDER"},
		{"CREATE_TASK reference", "[CREATE_TASK]"},
		{"EDIT_TASK reference", "[EDIT_TASK]"},
		{"EXECUTE_TASKS reference", "[EXECUTE_TASKS]"},
		{"Thread view reference", "[VIEW_TASK_CHAT]"},
		{"SEND_TO_TASK reference", "[SEND_TO_TASK]"},
		{"anti-description warning", "NEVER say"},
		{"per-request marker", "Each request requires its own marker block"},
		{"self-check instruction", "SELF-CHECK"},
	}

	for _, r := range required {
		if !strings.Contains(llmprompt.ChatMarkerReinforcement, r.content) {
			t.Errorf("llmprompt.ChatMarkerReinforcement missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

// --- DetectMissingMarkers Tests ---

func TestDetectMissingMarkers_ViewThread_MissingMarker(t *testing.T) {
	// LLM says it'll retrieve the thread but doesn't include the marker
	output := "Let me retrieve the thread history for that task."
	warnings := DetectMissingMarkers(output)
	if len(warnings) == 0 {
		t.Fatal("expected warning for missing thread view marker")
	}
	found := false
	for _, w := range warnings {
		if w.Action == "view_thread" {
			found = true
			if w.MarkerName != "[VIEW_TASK_CHAT]" {
				t.Errorf("expected marker name [VIEW_TASK_CHAT], got %q", w.MarkerName)
			}
		}
	}
	if !found {
		t.Error("expected view_thread warning, not found")
	}
}

func TestDetectMissingMarkers_ViewThread_WithMarker(t *testing.T) {
	// LLM correctly includes the marker — no warning expected
	output := `Let me retrieve the thread history for that task.

[VIEW_TASK_CHAT]
{"task_id": "abc123"}
[/VIEW_TASK_CHAT]`
	warnings := DetectMissingMarkers(output)
	for _, w := range warnings {
		if w.Action == "view_thread" {
			t.Error("should not warn when thread view marker is present")
		}
	}
}

func TestDetectMissingMarkers_SendToTask_MissingMarker(t *testing.T) {
	// LLM says it'll send a message but doesn't include the marker
	output := "I'll send that instruction to the API task."
	warnings := DetectMissingMarkers(output)
	if len(warnings) == 0 {
		t.Fatal("expected warning for missing SEND_TO_TASK marker")
	}
	found := false
	for _, w := range warnings {
		if w.Action == "send_to_task" {
			found = true
			if w.MarkerName != "[SEND_TO_TASK]" {
				t.Errorf("expected marker name [SEND_TO_TASK], got %q", w.MarkerName)
			}
		}
	}
	if !found {
		t.Error("expected send_to_task warning, not found")
	}
}

func TestDetectMissingMarkers_SendToTask_WithMarker(t *testing.T) {
	// LLM correctly includes the marker — no warning expected
	output := `I'll send that instruction to the API task.

[SEND_TO_TASK]
{"task_id": "abc123", "message": "Please add error handling"}
[/SEND_TO_TASK]`
	warnings := DetectMissingMarkers(output)
	for _, w := range warnings {
		if w.Action == "send_to_task" {
			t.Error("should not warn when SEND_TO_TASK marker is present")
		}
	}
}

func TestDetectMissingMarkers_CreateTask_MissingMarker(t *testing.T) {
	output := "I'll create that task for you now."
	warnings := DetectMissingMarkers(output)
	found := false
	for _, w := range warnings {
		if w.Action == "create_task" {
			found = true
		}
	}
	if !found {
		t.Error("expected create_task warning, not found")
	}
}

func TestDetectMissingMarkers_CreateTask_WithMarker(t *testing.T) {
	output := `I'll create that task for you now.

[CREATE_TASK]
{"title": "Test", "prompt": "Do something", "category": "backlog"}
[/CREATE_TASK]`
	warnings := DetectMissingMarkers(output)
	for _, w := range warnings {
		if w.Action == "create_task" {
			t.Error("should not warn when CREATE_TASK marker is present")
		}
	}
}

func TestDetectMissingMarkers_NoActionPhrases(t *testing.T) {
	// Normal response with no action-promising phrases
	output := "Here's a summary of your tasks:\n1. Fix bugs\n2. Add features"
	warnings := DetectMissingMarkers(output)
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for normal response, got %d", len(warnings))
	}
}

func TestDetectMissingMarkers_EmptyOutput(t *testing.T) {
	warnings := DetectMissingMarkers("")
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for empty output, got %d", len(warnings))
	}
}

func TestDetectMissingMarkers_MultipleActions_BothMissing(t *testing.T) {
	// LLM promises both view and send but includes neither marker
	output := "Let me retrieve the thread history for that task. I'll send that message to the API task."
	warnings := DetectMissingMarkers(output)
	if len(warnings) < 2 {
		t.Errorf("expected at least 2 warnings, got %d", len(warnings))
	}

	actions := make(map[string]bool)
	for _, w := range warnings {
		actions[w.Action] = true
	}
	if !actions["view_thread"] {
		t.Error("expected view_thread warning")
	}
	if !actions["send_to_task"] {
		t.Error("expected send_to_task warning")
	}
}

func TestDetectMissingMarkers_MultipleActions_OneMissing(t *testing.T) {
	// LLM promises both view and send, but only includes the view marker
	output := `Let me retrieve the thread history for that task. I'll send that message to the API task.

[VIEW_TASK_CHAT]
{"task_id": "abc123"}
[/VIEW_TASK_CHAT]`

	warnings := DetectMissingMarkers(output)
	if len(warnings) == 0 {
		t.Fatal("expected at least 1 warning for missing SEND_TO_TASK")
	}

	for _, w := range warnings {
		if w.Action == "view_thread" {
			t.Error("should not warn for view_thread when marker is present")
		}
	}

	found := false
	for _, w := range warnings {
		if w.Action == "send_to_task" {
			found = true
		}
	}
	if !found {
		t.Error("expected send_to_task warning (marker missing)")
	}
}

func TestDetectMissingMarkers_CaseInsensitive(t *testing.T) {
	// Phrases should match case-insensitively
	output := "I'LL RETRIEVE THE THREAD HISTORY FOR THAT TASK."
	warnings := DetectMissingMarkers(output)
	found := false
	for _, w := range warnings {
		if w.Action == "view_thread" {
			found = true
		}
	}
	if !found {
		t.Error("expected view_thread warning (case-insensitive match)")
	}
}

func TestDetectMissingMarkers_VariousSendPhrases(t *testing.T) {
	phrases := []string{
		"I'll send that message to the task.",
		"Let me send that to the API task.",
		"I'll tell the task to add error handling.",
		"Sending that to the task now.",
		"I'll forward that to the fix task.",
		"Let me send a message to the API task.",
		"I'll send a follow-up to the task.",
	}

	for _, phrase := range phrases {
		warnings := DetectMissingMarkers(phrase)
		found := false
		for _, w := range warnings {
			if w.Action == "send_to_task" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected send_to_task warning for phrase %q", phrase)
		}
	}
}

func TestDetectMissingMarkers_VariousViewPhrases(t *testing.T) {
	phrases := []string{
		"Let me retrieve the thread history for that task.",
		"I'll fetch the thread for the login task.",
		"Let me get the thread history for that one.",
		"I'll pull up the thread for that task.",
		"I'll show you the thread history.",
		"Let me check the task output.",
		"I'll get the execution history.",
		"Let me view the task output.",
	}

	for _, phrase := range phrases {
		warnings := DetectMissingMarkers(phrase)
		found := false
		for _, w := range warnings {
			if w.Action == "view_thread" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected view_thread warning for phrase %q", phrase)
		}
	}
}

// --- System Prompt Strengthening Tests ---

func TestChatThreadViewInstructions_ContainsCommonMistakeWarning(t *testing.T) {
	if !strings.Contains(llmprompt.ChatThreadViewInstructions, "COMMON MISTAKE") {
		t.Error("llmprompt.ChatThreadViewInstructions should contain COMMON MISTAKE section")
	}
	if !strings.Contains(llmprompt.ChatThreadViewInstructions, "DO NOT summarize from context") {
		t.Error("llmprompt.ChatThreadViewInstructions should warn against summarizing from context")
	}
}

func TestChatThreadSendInstructions_ContainsFollowUpExample(t *testing.T) {
	if !strings.Contains(llmprompt.ChatThreadSendInstructions, "COMMON MISTAKE") {
		t.Error("llmprompt.ChatThreadSendInstructions should contain COMMON MISTAKE section")
	}
	if !strings.Contains(llmprompt.ChatThreadSendInstructions, "NEW [SEND_TO_TASK] block for EACH message") {
		t.Error("llmprompt.ChatThreadSendInstructions should emphasize new marker for each message")
	}
	if !strings.Contains(llmprompt.ChatThreadSendInstructions, "also tell it to add logging") {
		t.Error("llmprompt.ChatThreadSendInstructions should contain follow-up example")
	}
}

func TestChatMarkerReinforcement_ContainsSelfCheck(t *testing.T) {
	if !strings.Contains(llmprompt.ChatMarkerReinforcement, "SELF-CHECK") {
		t.Error("llmprompt.ChatMarkerReinforcement should contain SELF-CHECK instruction")
	}
	if !strings.Contains(llmprompt.ChatMarkerReinforcement, "verify") {
		t.Error("llmprompt.ChatMarkerReinforcement SELF-CHECK should ask assistant to verify")
	}
}

// --- Schedule Context Tests ---

func TestBuildScheduleContextString_WithScheduledTasks(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.Local)
	nextRun := time.Date(2026, 3, 12, 9, 0, 0, 0, time.Local).UTC()
	lastRun := time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local).UTC()

	tasks := []models.Task{
		{ID: "task1", Title: "Daily Report", Status: models.StatusPending},
		{ID: "task2", Title: "Weekly Backup", Status: models.StatusCompleted},
	}

	scheduleMap := map[string][]models.Schedule{
		"task1": {
			{
				ID:             "sched1",
				TaskID:         "task1",
				RepeatType:     models.RepeatDaily,
				RepeatInterval: 1,
				Enabled:        true,
				NextRun:        &nextRun,
				LastRun:        &lastRun,
			},
		},
		"task2": {
			{
				ID:             "sched2",
				TaskID:         "task2",
				RepeatType:     models.RepeatWeekly,
				RepeatInterval: 1,
				Enabled:        true,
				NextRun:        &nextRun,
			},
		},
	}

	result := BuildScheduleContextString(tasks, scheduleMap, now)

	if !strings.Contains(result, "Scheduled tasks in this project:") {
		t.Errorf("expected header, got %q", result)
	}
	if !strings.Contains(result, "Daily Report") {
		t.Errorf("expected 'Daily Report' in output, got %q", result)
	}
	if !strings.Contains(result, "Weekly Backup") {
		t.Errorf("expected 'Weekly Backup' in output, got %q", result)
	}
	if !strings.Contains(result, "repeat:daily") {
		t.Errorf("expected 'repeat:daily', got %q", result)
	}
	if !strings.Contains(result, "repeat:weekly") {
		t.Errorf("expected 'repeat:weekly', got %q", result)
	}
	if !strings.Contains(result, "[enabled]") {
		t.Errorf("expected '[enabled]', got %q", result)
	}
	if !strings.Contains(result, "next_run:") {
		t.Errorf("expected 'next_run:', got %q", result)
	}
	if !strings.Contains(result, "status:pending") {
		t.Errorf("expected 'status:pending', got %q", result)
	}
	if !strings.Contains(result, "Current time:") {
		t.Errorf("expected 'Current time:' reference, got %q", result)
	}
}

func TestBuildScheduleContextString_Empty(t *testing.T) {
	now := time.Now()
	result := BuildScheduleContextString(nil, nil, now)
	if result != "" {
		t.Errorf("expected empty string for no tasks, got %q", result)
	}
}

func TestBuildScheduleContextString_NoSchedules(t *testing.T) {
	now := time.Now()
	tasks := []models.Task{
		{ID: "task1", Title: "Unscheduled Task", Status: models.StatusPending},
	}

	// Empty schedule map - no schedules exist for any task
	result := BuildScheduleContextString(tasks, map[string][]models.Schedule{}, now)
	if result != "" {
		t.Errorf("expected empty string for tasks with no schedules, got %q", result)
	}
}

func TestBuildScheduleContextString_DisabledSchedule(t *testing.T) {
	now := time.Now()
	nextRun := now.Add(24 * time.Hour)

	tasks := []models.Task{
		{ID: "task1", Title: "Paused Task", Status: models.StatusPending},
	}

	scheduleMap := map[string][]models.Schedule{
		"task1": {
			{
				ID:             "sched1",
				TaskID:         "task1",
				RepeatType:     models.RepeatDaily,
				RepeatInterval: 1,
				Enabled:        false,
				NextRun:        &nextRun,
			},
		},
	}

	result := BuildScheduleContextString(tasks, scheduleMap, now)
	if !strings.Contains(result, "[disabled]") {
		t.Errorf("expected '[disabled]' for paused schedule, got %q", result)
	}
}

func TestBuildScheduleContextString_OnceScheduleNoNextRun(t *testing.T) {
	now := time.Now()

	tasks := []models.Task{
		{ID: "task1", Title: "One-time Task", Status: models.StatusCompleted},
	}

	scheduleMap := map[string][]models.Schedule{
		"task1": {
			{
				ID:             "sched1",
				TaskID:         "task1",
				RepeatType:     models.RepeatOnce,
				RepeatInterval: 1,
				Enabled:        true,
				NextRun:        nil, // One-time schedules have nil next_run after execution
			},
		},
	}

	result := BuildScheduleContextString(tasks, scheduleMap, now)
	if !strings.Contains(result, "repeat:once") {
		t.Errorf("expected 'repeat:once', got %q", result)
	}
	if !strings.Contains(result, "next_run:none") {
		t.Errorf("expected 'next_run:none' for nil NextRun, got %q", result)
	}
}

func TestFormatRepeatPattern(t *testing.T) {
	tests := []struct {
		repeatType models.RepeatType
		interval   int
		expected   string
	}{
		{models.RepeatOnce, 1, "once"},
		{models.RepeatSeconds, 1, "every second"},
		{models.RepeatSeconds, 30, "every 30 seconds"},
		{models.RepeatMinutes, 1, "every minute"},
		{models.RepeatMinutes, 5, "every 5 minutes"},
		{models.RepeatHours, 1, "every hour"},
		{models.RepeatHours, 2, "every 2 hours"},
		{models.RepeatDaily, 1, "daily"},
		{models.RepeatDaily, 3, "every 3 days"},
		{models.RepeatWeekly, 1, "weekly"},
		{models.RepeatWeekly, 2, "every 2 weeks"},
		{models.RepeatMonthly, 1, "monthly"},
		{models.RepeatMonthly, 6, "every 6 months"},
	}

	for _, tc := range tests {
		result := FormatRepeatPattern(tc.repeatType, tc.interval)
		if result != tc.expected {
			t.Errorf("FormatRepeatPattern(%s, %d) = %q, expected %q", tc.repeatType, tc.interval, result, tc.expected)
		}
	}
}

func TestBuildChatContext_IncludesScheduleContext(t *testing.T) {
	now := time.Date(2026, 3, 11, 14, 0, 0, 0, time.Local)
	nextRun := time.Date(2026, 3, 11, 18, 0, 0, 0, time.UTC)

	tasks := []models.Task{
		{ID: "task1", Title: "Daily report", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "Generate daily report"},
		{ID: "task2", Title: "Chat task", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "user chat"},
		{ID: "task3", Title: "Backlog item", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "Do something"},
	}
	llmConfigs := []models.LLMConfig{
		{ID: "model1", Name: "Claude", Model: "claude-3", Provider: "anthropic", IsDefault: true},
	}
	schedules := []models.Schedule{
		{TaskID: "task1", Enabled: true, RepeatType: "daily", RepeatInterval: 1, NextRun: &nextRun},
	}

	result := BuildChatContext(tasks, llmConfigs, schedules, now)

	// Should include task context (non-chat tasks only)
	if !strings.Contains(result, "Daily report") {
		t.Error("expected task context to include 'Daily report'")
	}
	if !strings.Contains(result, "Backlog item") {
		t.Error("expected task context to include 'Backlog item'")
	}
	if strings.Contains(result, "Chat task") {
		t.Error("expected chat tasks to be filtered out")
	}

	// Should include model context
	if !strings.Contains(result, "Claude") {
		t.Error("expected model context to include 'Claude'")
	}

	// Should include schedule context
	if !strings.Contains(result, "Scheduled tasks") {
		t.Error("expected schedule context to be included")
	}
	if !strings.Contains(result, "daily") {
		t.Error("expected schedule context to include repeat pattern 'daily'")
	}
	if !strings.Contains(result, "next_run:") {
		t.Error("expected schedule context to include next_run time")
	}
}

func TestBuildChatContext_NoSchedules(t *testing.T) {
	now := time.Date(2026, 3, 11, 14, 0, 0, 0, time.Local)

	tasks := []models.Task{
		{ID: "task1", Title: "Some task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "Do stuff"},
	}
	llmModels := []models.LLMConfig{
		{ID: "model1", Name: "Claude", Model: "claude-3", Provider: "anthropic"},
	}

	result := BuildChatContext(tasks, llmModels, nil, now)

	// Should include task and model context but not schedule
	if !strings.Contains(result, "Some task") {
		t.Error("expected task context to include 'Some task'")
	}
	if !strings.Contains(result, "Claude") {
		t.Error("expected model context to include 'Claude'")
	}
	if strings.Contains(result, "Scheduled tasks") {
		t.Error("expected no schedule context when no schedules exist")
	}
}

func TestBuildChatContext_Empty(t *testing.T) {
	now := time.Now()
	result := BuildChatContext(nil, nil, nil, now)
	if result != "" {
		t.Errorf("expected empty context for nil inputs, got %q", result)
	}
}

func TestBuildChatContext_FiltersChatTasks(t *testing.T) {
	now := time.Now()

	tasks := []models.Task{
		{ID: "t1", Title: "Real task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "real"},
		{ID: "t2", Title: "Chat message", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "chat"},
		{ID: "t3", Title: "Another chat", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "chat2"},
	}

	result := BuildChatContext(tasks, nil, nil, now)

	if !strings.Contains(result, "Real task") {
		t.Error("expected non-chat task to be included")
	}
	if strings.Contains(result, "Chat message") || strings.Contains(result, "Another chat") {
		t.Error("expected chat tasks to be filtered out")
	}
}

func TestParseScheduleTask_ByID(t *testing.T) {
	output := `I'll schedule that for you.

[SCHEDULE_TASK]
{"task_id": "abc123", "time": "09:00", "repeat": "daily"}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].TaskID != "abc123" {
		t.Errorf("expected task_id 'abc123', got %q", requests[0].TaskID)
	}
	if requests[0].Time != "09:00" {
		t.Errorf("expected time '09:00', got %q", requests[0].Time)
	}
	if requests[0].Repeat != "daily" {
		t.Errorf("expected repeat 'daily', got %q", requests[0].Repeat)
	}
}

func TestParseScheduleTask_ByTitle(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"title": "backup task", "time": "00:00", "repeat": "daily"}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Title != "backup task" {
		t.Errorf("expected title 'backup task', got %q", requests[0].Title)
	}
}

func TestParseScheduleTask_Weekly(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"task_id": "xyz", "time": "14:30", "repeat": "weekly", "days": ["mon", "wed", "fri"]}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Repeat != "weekly" {
		t.Errorf("expected repeat 'weekly', got %q", requests[0].Repeat)
	}
	if len(requests[0].Days) != 3 {
		t.Fatalf("expected 3 days, got %d", len(requests[0].Days))
	}
	if requests[0].Days[0] != "mon" || requests[0].Days[1] != "wed" || requests[0].Days[2] != "fri" {
		t.Errorf("expected days [mon wed fri], got %v", requests[0].Days)
	}
}

func TestParseScheduleTask_NoMarkers(t *testing.T) {
	output := "No schedule markers here."
	requests := ParseScheduleTask(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestParseScheduleTask_NoTime(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"task_id": "abc123", "repeat": "daily"}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests (no time), got %d", len(requests))
	}
}

func TestParseScheduleTask_NoTaskIDOrTitle(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"time": "09:00", "repeat": "daily"}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests (no task_id or title), got %d", len(requests))
	}
}

func TestParseScheduleTask_InvalidJSON(t *testing.T) {
	output := `[SCHEDULE_TASK]
not valid json
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for invalid JSON, got %d", len(requests))
	}
}

func TestParseScheduleTask_Multiple(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"task_id": "task1", "time": "09:00", "repeat": "daily"}
[/SCHEDULE_TASK]

[SCHEDULE_TASK]
{"task_id": "task2", "time": "18:00", "repeat": "once"}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	if requests[0].TaskID != "task1" {
		t.Errorf("expected first task_id 'task1', got %q", requests[0].TaskID)
	}
	if requests[1].TaskID != "task2" {
		t.Errorf("expected second task_id 'task2', got %q", requests[1].TaskID)
	}
	if requests[1].Repeat != "once" {
		t.Errorf("expected second repeat 'once', got %q", requests[1].Repeat)
	}
}

func TestParseScheduleTask_WithInterval(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"task_id": "abc123", "time": "01:00", "repeat": "daily", "interval": 2}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].TaskID != "abc123" {
		t.Errorf("expected task_id 'abc123', got %q", requests[0].TaskID)
	}
	if requests[0].Repeat != "daily" {
		t.Errorf("expected repeat 'daily', got %q", requests[0].Repeat)
	}
	if requests[0].Interval != 2 {
		t.Errorf("expected interval 2, got %d", requests[0].Interval)
	}
}

func TestParseScheduleTask_HoursRepeat(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"task_id": "abc123", "time": "00:00", "repeat": "hours", "interval": 3}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Repeat != "hours" {
		t.Errorf("expected repeat 'hours', got %q", requests[0].Repeat)
	}
	if requests[0].Interval != 3 {
		t.Errorf("expected interval 3, got %d", requests[0].Interval)
	}
}

func TestParseScheduleTask_NoInterval_DefaultsToZero(t *testing.T) {
	output := `[SCHEDULE_TASK]
{"task_id": "abc123", "time": "09:00", "repeat": "daily"}
[/SCHEDULE_TASK]`

	requests := ParseScheduleTask(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	// When interval is not specified, JSON unmarshal defaults int to 0
	if requests[0].Interval != 0 {
		t.Errorf("expected interval 0 (not specified), got %d", requests[0].Interval)
	}
}

// --- Parse Set Personality Tests ---

func TestParseSetPersonality_Valid(t *testing.T) {
	output := `I'll change that for you.

[SET_PERSONALITY]
{"personality": "pirate_captain"}
[/SET_PERSONALITY]`

	requests := ParseSetPersonality(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Personality != "pirate_captain" {
		t.Errorf("expected personality 'pirate_captain', got %q", requests[0].Personality)
	}
}

func TestParseSetPersonality_ResetToDefault(t *testing.T) {
	output := `[SET_PERSONALITY]
{"personality": ""}
[/SET_PERSONALITY]`

	requests := ParseSetPersonality(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Personality != "" {
		t.Errorf("expected empty personality, got %q", requests[0].Personality)
	}
}

func TestParseSetPersonality_NoMarker(t *testing.T) {
	output := "No personality markers here."
	requests := ParseSetPersonality(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestParseSetPersonality_InvalidJSON(t *testing.T) {
	output := `[SET_PERSONALITY]
not valid json
[/SET_PERSONALITY]`

	requests := ParseSetPersonality(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for invalid JSON, got %d", len(requests))
	}
}

// --- Has* Marker Detection Tests ---

func TestHasListPersonalities(t *testing.T) {
	if !HasListPersonalities("Here you go.\n\n[LIST_PERSONALITIES]") {
		t.Error("expected to detect [LIST_PERSONALITIES] marker")
	}
	if HasListPersonalities("No marker here.") {
		t.Error("expected no detection without marker")
	}
}

func TestHasListModels(t *testing.T) {
	if !HasListModels("Check this out.\n\n[LIST_MODELS]") {
		t.Error("expected to detect [LIST_MODELS] marker")
	}
	if HasListModels("No marker here.") {
		t.Error("expected no detection without marker")
	}
}

func TestHasViewSettings(t *testing.T) {
	if !HasViewSettings("Here are settings.\n\n[VIEW_SETTINGS]") {
		t.Error("expected to detect [VIEW_SETTINGS] marker")
	}
	if HasViewSettings("No marker here.") {
		t.Error("expected no detection without marker")
	}
}

func TestHasProjectInfo(t *testing.T) {
	if !HasProjectInfo("Project details.\n\n[PROJECT_INFO]") {
		t.Error("expected to detect [PROJECT_INFO] marker")
	}
	if HasProjectInfo("No marker here.") {
		t.Error("expected no detection without marker")
	}
}

// --- Intent Detection Tests for New Markers ---

func TestDetectMissingMarkers_ListPersonalities_Missing(t *testing.T) {
	output := "Let me list the personalities for you."
	warnings := DetectMissingMarkers(output)

	found := false
	for _, w := range warnings {
		if w.Action == "list_personalities" {
			found = true
		}
	}
	if !found {
		t.Error("expected list_personalities warning when marker is missing")
	}
}

func TestDetectMissingMarkers_ListPersonalities_Present(t *testing.T) {
	output := "Let me list the personalities for you.\n\n[LIST_PERSONALITIES]"
	warnings := DetectMissingMarkers(output)

	for _, w := range warnings {
		if w.Action == "list_personalities" {
			t.Error("should not warn when [LIST_PERSONALITIES] marker is present")
		}
	}
}

func TestDetectMissingMarkers_SetPersonality_Missing(t *testing.T) {
	output := "I'll change the personality to pirate."
	warnings := DetectMissingMarkers(output)

	found := false
	for _, w := range warnings {
		if w.Action == "set_personality" {
			found = true
		}
	}
	if !found {
		t.Error("expected set_personality warning when marker is missing")
	}
}

func TestDetectMissingMarkers_SetPersonality_Present(t *testing.T) {
	output := "I'll change the personality.\n\n[SET_PERSONALITY]\n{\"personality\": \"pirate_captain\"}\n[/SET_PERSONALITY]"
	warnings := DetectMissingMarkers(output)

	for _, w := range warnings {
		if w.Action == "set_personality" {
			t.Error("should not warn when [SET_PERSONALITY] marker is present")
		}
	}
}

func TestDetectMissingMarkers_ListModels_Missing(t *testing.T) {
	output := "Let me list the models for you."
	warnings := DetectMissingMarkers(output)

	found := false
	for _, w := range warnings {
		if w.Action == "list_models" {
			found = true
		}
	}
	if !found {
		t.Error("expected list_models warning when marker is missing")
	}
}

func TestDetectMissingMarkers_ViewSettings_Missing(t *testing.T) {
	output := "Let me show you the settings."
	warnings := DetectMissingMarkers(output)

	found := false
	for _, w := range warnings {
		if w.Action == "view_settings" {
			found = true
		}
	}
	if !found {
		t.Error("expected view_settings warning when marker is missing")
	}
}

func TestDetectMissingMarkers_ProjectInfo_Missing(t *testing.T) {
	output := "Let me get the project info for you."
	warnings := DetectMissingMarkers(output)

	found := false
	for _, w := range warnings {
		if w.Action == "project_info" {
			found = true
		}
	}
	if !found {
		t.Error("expected project_info warning when marker is missing")
	}
}

// --- DELETE_SCHEDULE parser tests ---

func TestParseDeleteSchedule_ByScheduleID(t *testing.T) {
	output := `I'll delete that schedule.

[DELETE_SCHEDULE]
{"schedule_id": "sched123"}
[/DELETE_SCHEDULE]`

	requests := ParseDeleteSchedule(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].ScheduleID != "sched123" {
		t.Errorf("expected schedule_id 'sched123', got %q", requests[0].ScheduleID)
	}
}

func TestParseDeleteSchedule_ByTaskID(t *testing.T) {
	output := `[DELETE_SCHEDULE]
{"task_id": "task456"}
[/DELETE_SCHEDULE]`

	requests := ParseDeleteSchedule(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].TaskID != "task456" {
		t.Errorf("expected task_id 'task456', got %q", requests[0].TaskID)
	}
}

func TestParseDeleteSchedule_ByTitle(t *testing.T) {
	output := `[DELETE_SCHEDULE]
{"title": "daily backup"}
[/DELETE_SCHEDULE]`

	requests := ParseDeleteSchedule(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Title != "daily backup" {
		t.Errorf("expected title 'daily backup', got %q", requests[0].Title)
	}
}

func TestParseDeleteSchedule_NoIdentifier(t *testing.T) {
	output := `[DELETE_SCHEDULE]
{}
[/DELETE_SCHEDULE]`

	requests := ParseDeleteSchedule(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests (no identifier), got %d", len(requests))
	}
}

func TestParseDeleteSchedule_NoMarkers(t *testing.T) {
	output := "No delete schedule markers here."
	requests := ParseDeleteSchedule(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestParseDeleteSchedule_InvalidJSON(t *testing.T) {
	output := `[DELETE_SCHEDULE]
not valid json
[/DELETE_SCHEDULE]`

	requests := ParseDeleteSchedule(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests (invalid JSON), got %d", len(requests))
	}
}

// --- MODIFY_SCHEDULE parser tests ---

func TestParseModifySchedule_ChangeTime(t *testing.T) {
	output := `[MODIFY_SCHEDULE]
{"schedule_id": "sched123", "time": "14:00"}
[/MODIFY_SCHEDULE]`

	requests := ParseModifySchedule(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].ScheduleID != "sched123" {
		t.Errorf("expected schedule_id 'sched123', got %q", requests[0].ScheduleID)
	}
	if requests[0].Time != "14:00" {
		t.Errorf("expected time '14:00', got %q", requests[0].Time)
	}
}

func TestParseModifySchedule_ChangeRepeat(t *testing.T) {
	output := `[MODIFY_SCHEDULE]
{"task_id": "task456", "repeat": "weekly", "days": ["mon", "fri"]}
[/MODIFY_SCHEDULE]`

	requests := ParseModifySchedule(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Repeat != "weekly" {
		t.Errorf("expected repeat 'weekly', got %q", requests[0].Repeat)
	}
	if len(requests[0].Days) != 2 {
		t.Fatalf("expected 2 days, got %d", len(requests[0].Days))
	}
}

func TestParseModifySchedule_ChangeEnabled(t *testing.T) {
	output := `[MODIFY_SCHEDULE]
{"schedule_id": "sched123", "enabled": false}
[/MODIFY_SCHEDULE]`

	requests := ParseModifySchedule(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Enabled == nil {
		t.Fatal("expected enabled to be set")
	}
	if *requests[0].Enabled != false {
		t.Error("expected enabled to be false")
	}
}

func TestParseModifySchedule_ChangeInterval(t *testing.T) {
	output := `[MODIFY_SCHEDULE]
{"title": "backup", "interval": 3}
[/MODIFY_SCHEDULE]`

	requests := ParseModifySchedule(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Interval == nil {
		t.Fatal("expected interval to be set")
	}
	if *requests[0].Interval != 3 {
		t.Errorf("expected interval 3, got %d", *requests[0].Interval)
	}
}

func TestParseModifySchedule_NoIdentifier(t *testing.T) {
	output := `[MODIFY_SCHEDULE]
{"time": "14:00"}
[/MODIFY_SCHEDULE]`

	requests := ParseModifySchedule(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests (no identifier), got %d", len(requests))
	}
}

func TestParseModifySchedule_NoMarkers(t *testing.T) {
	output := "No modify schedule markers here."
	requests := ParseModifySchedule(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

// --- Intent detection tests for delete/modify schedule ---

func TestDetectMissingMarkers_DeleteSchedule_Missing(t *testing.T) {
	output := "I'll delete that schedule for you right away."
	warnings := DetectMissingMarkers(output)

	found := false
	for _, w := range warnings {
		if w.Action == "delete_schedule" {
			found = true
			if w.MarkerName != "[DELETE_SCHEDULE]" {
				t.Errorf("expected marker name [DELETE_SCHEDULE], got %q", w.MarkerName)
			}
		}
	}
	if !found {
		t.Error("expected delete_schedule warning when marker is missing")
	}
}

func TestDetectMissingMarkers_DeleteSchedule_Present(t *testing.T) {
	output := `I'll delete that schedule.

[DELETE_SCHEDULE]
{"schedule_id": "abc"}
[/DELETE_SCHEDULE]`

	warnings := DetectMissingMarkers(output)
	for _, w := range warnings {
		if w.Action == "delete_schedule" {
			t.Error("should not warn when DELETE_SCHEDULE marker is present")
		}
	}
}

func TestDetectMissingMarkers_ModifySchedule_Missing(t *testing.T) {
	output := "I'll modify that schedule to run at a different time."
	warnings := DetectMissingMarkers(output)

	found := false
	for _, w := range warnings {
		if w.Action == "modify_schedule" {
			found = true
			if w.MarkerName != "[MODIFY_SCHEDULE]" {
				t.Errorf("expected marker name [MODIFY_SCHEDULE], got %q", w.MarkerName)
			}
		}
	}
	if !found {
		t.Error("expected modify_schedule warning when marker is missing")
	}
}

func TestDetectMissingMarkers_ModifySchedule_Present(t *testing.T) {
	output := `I'll update the schedule.

[MODIFY_SCHEDULE]
{"schedule_id": "abc", "time": "14:00"}
[/MODIFY_SCHEDULE]`

	warnings := DetectMissingMarkers(output)
	for _, w := range warnings {
		if w.Action == "modify_schedule" {
			t.Error("should not warn when MODIFY_SCHEDULE marker is present")
		}
	}
}

// --- Alert Marker Parser Tests ---

func TestParseCreateAlert_Valid(t *testing.T) {
	output := `I'll create that alert.

[CREATE_ALERT]
{"title": "Build failing", "message": "CI pipeline has been red for 2 hours", "severity": "error"}
[/CREATE_ALERT]`

	requests := ParseCreateAlert(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Title != "Build failing" {
		t.Errorf("expected title 'Build failing', got %q", requests[0].Title)
	}
	if requests[0].Message != "CI pipeline has been red for 2 hours" {
		t.Errorf("expected message about CI pipeline, got %q", requests[0].Message)
	}
	if requests[0].Severity != "error" {
		t.Errorf("expected severity 'error', got %q", requests[0].Severity)
	}
}

func TestParseCreateAlert_WithTaskID(t *testing.T) {
	output := `[CREATE_ALERT]
{"title": "Task stuck", "severity": "warning", "task_id": "task123", "type": "task_needs_followup"}
[/CREATE_ALERT]`

	requests := ParseCreateAlert(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].TaskID != "task123" {
		t.Errorf("expected task_id 'task123', got %q", requests[0].TaskID)
	}
	if requests[0].Type != "task_needs_followup" {
		t.Errorf("expected type 'task_needs_followup', got %q", requests[0].Type)
	}
}

func TestParseCreateAlert_NoTitle(t *testing.T) {
	output := `[CREATE_ALERT]
{"message": "no title provided"}
[/CREATE_ALERT]`

	requests := ParseCreateAlert(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for missing title, got %d", len(requests))
	}
}

func TestParseCreateAlert_NoMarker(t *testing.T) {
	output := "No alert markers here."
	requests := ParseCreateAlert(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestParseCreateAlert_InvalidJSON(t *testing.T) {
	output := `[CREATE_ALERT]
not valid json
[/CREATE_ALERT]`

	requests := ParseCreateAlert(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for invalid JSON, got %d", len(requests))
	}
}

func TestParseDeleteAlert_Valid(t *testing.T) {
	output := `[DELETE_ALERT]
{"alert_id": "abc123"}
[/DELETE_ALERT]`

	requests := ParseDeleteAlert(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].AlertID != "abc123" {
		t.Errorf("expected alert_id 'abc123', got %q", requests[0].AlertID)
	}
}

func TestParseDeleteAlert_NoAlertID(t *testing.T) {
	output := `[DELETE_ALERT]
{"alert_id": ""}
[/DELETE_ALERT]`

	requests := ParseDeleteAlert(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for empty alert_id, got %d", len(requests))
	}
}

func TestParseDeleteAlert_NoMarker(t *testing.T) {
	output := "No markers here."
	requests := ParseDeleteAlert(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestParseToggleAlert_Valid(t *testing.T) {
	output := `[TOGGLE_ALERT]
{"alert_id": "xyz789"}
[/TOGGLE_ALERT]`

	requests := ParseToggleAlert(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].AlertID != "xyz789" {
		t.Errorf("expected alert_id 'xyz789', got %q", requests[0].AlertID)
	}
}

func TestParseToggleAlert_NoAlertID(t *testing.T) {
	output := `[TOGGLE_ALERT]
{"alert_id": ""}
[/TOGGLE_ALERT]`

	requests := ParseToggleAlert(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests for empty alert_id, got %d", len(requests))
	}
}

func TestParseToggleAlert_NoMarker(t *testing.T) {
	output := "No markers here."
	requests := ParseToggleAlert(output)
	if len(requests) != 0 {
		t.Fatalf("expected 0 requests, got %d", len(requests))
	}
}

func TestHasListAlerts(t *testing.T) {
	if !HasListAlerts("Show alerts.\n\n[LIST_ALERTS]") {
		t.Error("expected to detect [LIST_ALERTS] marker")
	}
	if HasListAlerts("No marker here.") {
		t.Error("expected no detection without marker")
	}
}

func TestDetectMissingMarkers_ListAlerts(t *testing.T) {
	output := "Let me check the alerts."
	warnings := DetectMissingMarkers(output)
	found := false
	for _, w := range warnings {
		if w.Action == "list_alerts" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected list_alerts warning when marker is missing")
	}
}

func TestDetectMissingMarkers_ListAlerts_Present(t *testing.T) {
	output := "Let me check the alerts.\n\n[LIST_ALERTS]"
	warnings := DetectMissingMarkers(output)
	for _, w := range warnings {
		if w.Action == "list_alerts" {
			t.Error("should not warn when LIST_ALERTS marker is present")
		}
	}
}

func TestDetectMissingMarkers_CreateAlert(t *testing.T) {
	output := "I'll create that alert for you."
	warnings := DetectMissingMarkers(output)
	found := false
	for _, w := range warnings {
		if w.Action == "create_alert" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected create_alert warning when marker is missing")
	}
}

func TestDetectMissingMarkers_CreateAlert_Present(t *testing.T) {
	output := `I'll create that alert.

[CREATE_ALERT]
{"title": "test"}
[/CREATE_ALERT]`

	warnings := DetectMissingMarkers(output)
	for _, w := range warnings {
		if w.Action == "create_alert" {
			t.Error("should not warn when CREATE_ALERT marker is present")
		}
	}
}

func TestAutoStartTasks_EnabledInModel(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ws := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, nil, ws)

	// Create project
	project := &models.Project{Name: "Test Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create agent with auto-start enabled
	agent := &models.LLMConfig{
		Name:           "Auto Start Model",
		Provider:       models.ProviderTest,
		Model:          "test-model",
		MaxTokens:      4096,
		Temperature:    0.0,
		AuthMethod:     models.AuthMethodCLI,
		AutoStartTasks: true,
	}
	agent.Provider = models.ProviderAnthropic // Set to valid provider for DB constraint
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}
	agent.Provider = models.ProviderTest // Switch back to test for in-memory check

	// Create task request without explicit category
	requests := []TaskCreationRequest{
		{
			Title:    "Test Task",
			Prompt:   "Do something",
			AgentID:  agent.ID,
			Category: "", // No category specified
			Priority: 2,
		},
	}

	// Execute task creation with agent list
	agents := []models.LLMConfig{*agent}
	createdTasks, _ := ExecuteTaskCreationsWithReturn(ctx, requests, project.ID, taskSvc, agents)

	if len(createdTasks) != 1 {
		t.Fatalf("expected 1 task created, got %d", len(createdTasks))
	}

	// Should be in "active" category due to auto-start
	if createdTasks[0].Category != models.CategoryActive {
		t.Errorf("expected category 'active', got %q", createdTasks[0].Category)
	}
}

func TestAutoStartTasks_DisabledInModel(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ws := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, nil, ws)

	// Create project
	project := &models.Project{Name: "Test Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create agent with auto-start disabled (default)
	agent := &models.LLMConfig{
		Name:           "Manual Start Model",
		Provider:       models.ProviderTest,
		Model:          "test-model",
		MaxTokens:      4096,
		Temperature:    0.0,
		AuthMethod:     models.AuthMethodCLI,
		AutoStartTasks: false,
	}
	agent.Provider = models.ProviderAnthropic // Set to valid provider for DB constraint
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}
	agent.Provider = models.ProviderTest // Switch back to test for in-memory check

	// Create task request without explicit category
	requests := []TaskCreationRequest{
		{
			Title:    "Test Task",
			Prompt:   "Do something",
			AgentID:  agent.ID,
			Category: "", // No category specified
			Priority: 2,
		},
	}

	// Execute task creation with agent list
	agents := []models.LLMConfig{*agent}
	createdTasks, _ := ExecuteTaskCreationsWithReturn(ctx, requests, project.ID, taskSvc, agents)

	if len(createdTasks) != 1 {
		t.Fatalf("expected 1 task created, got %d", len(createdTasks))
	}

	// Should be in "backlog" category due to auto-start disabled
	if createdTasks[0].Category != models.CategoryBacklog {
		t.Errorf("expected category 'backlog', got %q", createdTasks[0].Category)
	}
}

func TestAutoStartTasks_ExplicitCategoryOverrides(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ws := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, nil, ws)

	// Create project
	project := &models.Project{Name: "Test Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create agent with auto-start enabled
	agent := &models.LLMConfig{
		Name:           "Auto Start Model",
		Provider:       models.ProviderTest,
		Model:          "test-model",
		MaxTokens:      4096,
		Temperature:    0.0,
		AuthMethod:     models.AuthMethodCLI,
		AutoStartTasks: true,
	}
	agent.Provider = models.ProviderAnthropic // Set to valid provider for DB constraint
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}
	agent.Provider = models.ProviderTest // Switch back to test for in-memory check

	// Create task request with EXPLICIT backlog category
	requests := []TaskCreationRequest{
		{
			Title:    "Test Task",
			Prompt:   "Do something",
			AgentID:  agent.ID,
			Category: "backlog", // Explicit backlog should override auto-start
			Priority: 2,
		},
	}

	// Execute task creation with agent list
	agents := []models.LLMConfig{*agent}
	createdTasks, _ := ExecuteTaskCreationsWithReturn(ctx, requests, project.ID, taskSvc, agents)

	if len(createdTasks) != 1 {
		t.Fatalf("expected 1 task created, got %d", len(createdTasks))
	}

	// Should stay in "backlog" because it was explicitly set
	if createdTasks[0].Category != models.CategoryBacklog {
		t.Errorf("expected category 'backlog' (explicit), got %q", createdTasks[0].Category)
	}
}

func TestAutoStartTasks_SingleAgentAvailable(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	// Create project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a single agent with auto-start enabled
	agent := &models.LLMConfig{
		Name:           "Claude Sonnet",
		Provider:       models.ProviderAnthropic,
		Model:          "claude-sonnet-4",
		AutoStartTasks: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create task without specifying agent_id (should use the only available agent)
	requests := []TaskCreationRequest{
		{
			Title:  "Test Task",
			Prompt: "Test prompt",
			// No Category, no AgentID specified
		},
	}

	tasks, _ := ExecuteTaskCreationsWithReturn(ctx, requests, project.ID, taskSvc, []models.LLMConfig{*agent})

	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	// Task should be auto-started because the single agent has auto-start enabled
	if tasks[0].Category != models.CategoryActive {
		t.Errorf("expected category 'active', got '%s'", tasks[0].Category)
	}

	// Agent should be assigned
	if tasks[0].AgentID == nil {
		t.Errorf("expected agent to be assigned, got nil")
	} else if *tasks[0].AgentID != agent.ID {
		t.Errorf("expected agent ID %s, got %s", agent.ID, *tasks[0].AgentID)
	}
}
