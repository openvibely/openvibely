package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestLLMService_TriggerTaskChain(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	// Create repos and services
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Create worker service with 0 workers (no actual execution)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	llmSvc.taskSvc = taskSvc

	// Create a test project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test project for chaining",
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project failed: %v", err)
	}
	projectID := project.ID

	// Create a parent task with chaining enabled
	parentTask := &models.Task{
		ProjectID: projectID,
		Title:     "Parent Task (Planning)",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Create a plan for implementing feature X",
	}

	// Set chain config
	chainConfig := &models.ChainConfiguration{
		Enabled:       true,
		Trigger:       "on_completion",
		ChildCategory: "backlog",
	}
	if err := parentTask.SetChainConfig(chainConfig); err != nil {
		t.Fatalf("SetChainConfig failed: %v", err)
	}

	// Create the parent task
	if err := taskRepo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task failed: %v", err)
	}

	// Simulate task completion output
	parentOutput := "Here's the plan:\n1. Design the API\n2. Implement the backend\n3. Add tests\n\n[STATUS: SUCCESS]"

	// Trigger chain
	if err := llmSvc.triggerTaskChain(ctx, *parentTask, parentOutput); err != nil {
		t.Fatalf("triggerTaskChain failed: %v", err)
	}

	// Verify child task was created
	tasks, err := taskRepo.ListByProject(ctx, projectID, "")
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}

	// Should have 2 tasks now (parent + child)
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	// Find the child task
	var childTask *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == parentTask.ID {
			childTask = &task
			break
		}
	}

	if childTask == nil {
		t.Fatalf("Child task not found")
	}

	// Verify child task properties
	if childTask.Category != models.CategoryBacklog {
		t.Errorf("Expected child task category=backlog, got %s", childTask.Category)
	}
	if childTask.ParentTaskID == nil || *childTask.ParentTaskID != parentTask.ID {
		t.Errorf("Expected child task parent_task_id=%s, got %v", parentTask.ID, childTask.ParentTaskID)
	}
	if childTask.Priority != parentTask.Priority {
		t.Errorf("Expected child task priority=%d, got %d", parentTask.Priority, childTask.Priority)
	}

	// Verify child task prompt contains the plan (without [STATUS:] marker)
	expectedPrompt := "Here's the plan:\n1. Design the API\n2. Implement the backend\n3. Add tests"
	if childTask.Prompt != expectedPrompt {
		t.Errorf("Expected child task prompt=%q, got %q", expectedPrompt, childTask.Prompt)
	}
}

func TestLLMService_TriggerTaskChain_DefaultsChildCategoryToParentAndAutoSubmits(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	llmSvc.taskSvc = taskSvc

	project := &models.Project{Name: "Test", Description: "Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project failed: %v", err)
	}

	parentTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Chain math tasks: compute 1+1 then compute x+1",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Compute 1+1",
	}
	chainConfig := &models.ChainConfiguration{
		Enabled:       true,
		Trigger:       "on_completion",
		ChildTitle:    "Compute x+1 using parent output",
		ChildCategory: "", // Same as parent
	}
	if err := parentTask.SetChainConfig(chainConfig); err != nil {
		t.Fatalf("SetChainConfig failed: %v", err)
	}
	if err := taskRepo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task failed: %v", err)
	}

	if err := llmSvc.triggerTaskChain(ctx, *parentTask, "x=2\n[STATUS: SUCCESS]"); err != nil {
		t.Fatalf("triggerTaskChain failed: %v", err)
	}

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	var childTask *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == parentTask.ID {
			childTask = &task
			break
		}
	}
	if childTask == nil {
		t.Fatalf("Child task not found")
	}

	if childTask.Category != models.CategoryActive {
		t.Fatalf("Expected child category to inherit active parent, got %s", childTask.Category)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != childTask.ID {
			t.Fatalf("Expected submitted task id=%s, got %s", childTask.ID, submitted.ID)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("Expected child task auto-submission to worker pool")
	}
}

func TestLLMService_TriggerTaskChain_ChildTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	llmSvc.taskSvc = taskSvc

	project := &models.Project{Name: "Test", Description: "Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project failed: %v", err)
	}

	parentTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Discovery",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Discover features",
	}
	chainConfig := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "Selection Phase",
		ChildPromptPrefix: "Select the best feature from:",
		ChildCategory:     "active",
	}
	if err := parentTask.SetChainConfig(chainConfig); err != nil {
		t.Fatalf("SetChainConfig failed: %v", err)
	}
	if err := taskRepo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task failed: %v", err)
	}

	parentOutput := "Feature A, Feature B, Feature C"
	if err := llmSvc.triggerTaskChain(ctx, *parentTask, parentOutput); err != nil {
		t.Fatalf("triggerTaskChain failed: %v", err)
	}

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	var childTask *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == parentTask.ID {
			childTask = &task
			break
		}
	}
	if childTask == nil {
		t.Fatalf("Child task not found")
	}

	if childTask.Title != "Selection Phase" {
		t.Errorf("Expected child title=%q, got %q", "Selection Phase", childTask.Title)
	}

	expectedPrompt := "Select the best feature from:\n\nFeature A, Feature B, Feature C"
	if childTask.Prompt != expectedPrompt {
		t.Errorf("Expected child prompt=%q, got %q", expectedPrompt, childTask.Prompt)
	}

	if childTask.Category != models.CategoryActive {
		t.Errorf("Expected child category=active, got %s", childTask.Category)
	}
}

func TestLLMService_TriggerTaskChain_MultiLevel(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	llmSvc.taskSvc = taskSvc

	project := &models.Project{Name: "Test", Description: "Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project failed: %v", err)
	}

	// Create a 3-level chain: Discovery → Selection → Design
	parentTask := &models.Task{
		ProjectID: project.ID,
		Title:     "[Auto Build] Discovery",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Discover features",
	}
	chainConfig := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Selection",
		ChildPromptPrefix: "Select the best feature:",
		ChildCategory:     "active",
		ChildChainConfig: &models.ChainConfiguration{
			Enabled:           true,
			Trigger:           "on_completion",
			ChildTitle:        "[Auto Build] Design",
			ChildPromptPrefix: "Design the architecture:",
			ChildCategory:     "active",
		},
	}
	if err := parentTask.SetChainConfig(chainConfig); err != nil {
		t.Fatalf("SetChainConfig failed: %v", err)
	}
	if err := taskRepo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task failed: %v", err)
	}

	// Step 1: Trigger chain from discovery
	discoveryOutput := "Feature A, Feature B"
	if err := llmSvc.triggerTaskChain(ctx, *parentTask, discoveryOutput); err != nil {
		t.Fatalf("triggerTaskChain (discovery) failed: %v", err)
	}

	tasks, _ := taskRepo.ListByProject(ctx, project.ID, "")
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks after discovery chain, got %d", len(tasks))
	}

	var selectionTask *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == parentTask.ID {
			selectionTask = &task
			break
		}
	}
	if selectionTask == nil {
		t.Fatalf("Selection task not found")
	}

	if selectionTask.Title != "[Auto Build] Selection" {
		t.Errorf("Expected selection title=%q, got %q", "[Auto Build] Selection", selectionTask.Title)
	}

	// Verify the selection task has a chain config for the next level
	selConfig, err := selectionTask.ParseChainConfig()
	if err != nil {
		t.Fatalf("ParseChainConfig on selection task failed: %v", err)
	}
	if !selConfig.Enabled {
		t.Fatalf("Expected selection task chain config to be enabled")
	}
	if selConfig.ChildTitle != "[Auto Build] Design" {
		t.Errorf("Expected selection child_title=%q, got %q", "[Auto Build] Design", selConfig.ChildTitle)
	}

	// Step 2: Trigger chain from selection → should create design task
	selectionOutput := "Selected: Feature A"
	if err := llmSvc.triggerTaskChain(ctx, *selectionTask, selectionOutput); err != nil {
		t.Fatalf("triggerTaskChain (selection) failed: %v", err)
	}

	tasks, _ = taskRepo.ListByProject(ctx, project.ID, "")
	if len(tasks) != 3 {
		t.Fatalf("Expected 3 tasks after selection chain, got %d", len(tasks))
	}

	var designTask *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == selectionTask.ID {
			designTask = &task
			break
		}
	}
	if designTask == nil {
		t.Fatalf("Design task not found")
	}

	if designTask.Title != "[Auto Build] Design" {
		t.Errorf("Expected design title=%q, got %q", "[Auto Build] Design", designTask.Title)
	}

	expectedDesignPrompt := "Design the architecture:\n\nSelected: Feature A"
	if designTask.Prompt != expectedDesignPrompt {
		t.Errorf("Expected design prompt=%q, got %q", expectedDesignPrompt, designTask.Prompt)
	}

	// Design task should NOT chain further
	designConfig, err := designTask.ParseChainConfig()
	if err != nil {
		t.Fatalf("ParseChainConfig on design task failed: %v", err)
	}
	if designConfig.Enabled {
		t.Errorf("Expected design task chain config to be disabled")
	}
}

func TestCleanOutputForChain(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text unchanged",
			input:    "Here are the feature proposals:\n1. Feature A\n2. Feature B",
			expected: "Here are the feature proposals:\n1. Feature A\n2. Feature B",
		},
		{
			name:     "strips status marker",
			input:    "Feature proposals\n\n[STATUS: SUCCESS]",
			expected: "Feature proposals",
		},
		{
			name:     "strips thinking block",
			input:    "\n[Thinking]\nI need to analyze the project...\n[/Thinking]\nHere are the features: A, B, C",
			expected: "Here are the features: A, B, C",
		},
		{
			name:     "strips multiple thinking blocks",
			input:    "\n[Thinking]\nFirst thought\n[/Thinking]\nSome text\n[Thinking]\nSecond thought\n[/Thinking]\nFinal output",
			expected: "Some text\nFinal output",
		},
		{
			name:     "strips tool markers",
			input:    "Start\n[Using tool: bash]\nMiddle\n[Using tool: Read]\nEnd",
			expected: "Start\nMiddle\nEnd",
		},
		{
			name:     "strips all markers combined",
			input:    "\n[Thinking]\nLet me analyze...\n[/Thinking]\n[Using tool: bash]\nFeature A\n[Using tool: Read]\nFeature B\n[STATUS: SUCCESS]",
			expected: "Feature A\nFeature B",
		},
		{
			name:     "empty after stripping",
			input:    "\n[Thinking]\nJust thinking\n[/Thinking]\n[STATUS: SUCCESS]",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanOutputForChain(tt.input)
			if got != tt.expected {
				t.Errorf("cleanOutputForChain()\ngot:  %q\nwant: %q", got, tt.expected)
			}
		})
	}
}

func TestLLMService_TriggerTaskChain_TextOnlyOutput(t *testing.T) {
	// Simulates the primary path: textOnlyOutput from TextString() contains
	// only response text (no thinking, no tool markers, no status markers).
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	llmSvc.taskSvc = taskSvc

	project := &models.Project{Name: "Test", Description: "Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project failed: %v", err)
	}

	parentTask := &models.Task{
		ProjectID: project.ID,
		Title:     "[Auto Build] Discovery",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Discover features",
	}
	chainConfig := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Selection",
		ChildPromptPrefix: "Select the best feature:",
		ChildCategory:     "active",
	}
	if err := parentTask.SetChainConfig(chainConfig); err != nil {
		t.Fatalf("SetChainConfig failed: %v", err)
	}
	if err := taskRepo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task failed: %v", err)
	}

	// textOnlyOutput contains only the response text — no thinking, no markers
	textOnlyOutput := "Here are the proposals:\n1. Feature A\n2. Feature B\n3. Feature C"

	if err := llmSvc.triggerTaskChain(ctx, *parentTask, textOnlyOutput); err != nil {
		t.Fatalf("triggerTaskChain failed: %v", err)
	}

	tasks, _ := taskRepo.ListByProject(ctx, project.ID, "")
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	var childTask *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == parentTask.ID {
			childTask = &task
			break
		}
	}
	if childTask == nil {
		t.Fatalf("Child task not found")
	}

	expectedPrompt := "Select the best feature:\n\nHere are the proposals:\n1. Feature A\n2. Feature B\n3. Feature C"
	if childTask.Prompt != expectedPrompt {
		t.Errorf("Expected child prompt=%q, got %q", expectedPrompt, childTask.Prompt)
	}
}

func TestLLMService_TriggerTaskChain_CleansThinkingBlocks(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	llmSvc.taskSvc = taskSvc

	project := &models.Project{Name: "Test", Description: "Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project failed: %v", err)
	}

	parentTask := &models.Task{
		ProjectID: project.ID,
		Title:     "[Auto Build] Discovery",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Discover features",
	}
	chainConfig := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Selection",
		ChildPromptPrefix: "Select the best feature:",
		ChildCategory:     "active",
	}
	if err := parentTask.SetChainConfig(chainConfig); err != nil {
		t.Fatalf("SetChainConfig failed: %v", err)
	}
	if err := taskRepo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task failed: %v", err)
	}

	// Simulate output with thinking blocks, tool markers, and status marker
	parentOutput := "\n[Thinking]\nThe user wants me to discover features for the project...\nLet me analyze the codebase.\n[/Thinking]\n[Using tool: bash]\n[Using tool: Read]\nHere are the proposals:\n1. Feature A\n2. Feature B\n3. Feature C\n\n[STATUS: SUCCESS]"

	if err := llmSvc.triggerTaskChain(ctx, *parentTask, parentOutput); err != nil {
		t.Fatalf("triggerTaskChain failed: %v", err)
	}

	tasks, _ := taskRepo.ListByProject(ctx, project.ID, "")
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	var childTask *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == parentTask.ID {
			childTask = &task
			break
		}
	}
	if childTask == nil {
		t.Fatalf("Child task not found")
	}

	// The child prompt should contain ONLY the clean proposals with prefix, no thinking or tool markers
	expectedPrompt := "Select the best feature:\n\nHere are the proposals:\n1. Feature A\n2. Feature B\n3. Feature C"
	if childTask.Prompt != expectedPrompt {
		t.Errorf("Expected child prompt=%q, got %q", expectedPrompt, childTask.Prompt)
	}
}

func TestLLMService_TriggerTaskChain_Disabled(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	llmSvc.taskSvc = taskSvc

	// Create a test project
	project := &models.Project{
		Name:        "Test Project 2",
		Description: "Test project for chaining disabled",
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project failed: %v", err)
	}
	projectID := project.ID

	// Create a parent task with chaining disabled
	parentTask := &models.Task{
		ProjectID:   projectID,
		Title:       "Parent Task",
		Category:    models.CategoryActive,
		Priority:    2,
		Status:      models.StatusPending,
		Prompt:      "Do something",
		ChainConfig: "{}", // Empty config = disabled
	}

	if err := taskRepo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task failed: %v", err)
	}

	parentOutput := "Task completed successfully\n[STATUS: SUCCESS]"

	// Trigger chain (should do nothing)
	if err := llmSvc.triggerTaskChain(ctx, *parentTask, parentOutput); err != nil {
		t.Fatalf("triggerTaskChain failed: %v", err)
	}

	// Verify no child task was created
	tasks, err := taskRepo.ListByProject(ctx, projectID, "")
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}

	// Should still have only 1 task (parent)
	if len(tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(tasks))
	}
}
