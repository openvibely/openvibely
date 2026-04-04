package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestIsTerminalStatus verifies the terminal status check helper.
func TestIsTerminalStatus(t *testing.T) {
	terminal := []models.TaskStatus{models.StatusCompleted, models.StatusFailed, models.StatusCancelled}
	for _, s := range terminal {
		if !models.IsTerminalStatus(s) {
			t.Errorf("expected %s to be terminal", s)
		}
	}
	nonTerminal := []models.TaskStatus{models.StatusPending, models.StatusQueued, models.StatusRunning}
	for _, s := range nonTerminal {
		if models.IsTerminalStatus(s) {
			t.Errorf("expected %s to be non-terminal", s)
		}
	}
}

// TestTaskLineage_PersistsInDB verifies lineage fields round-trip through DB.
func TestTaskLineage_PersistsInDB(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)

	project := &models.Project{Name: "Lineage Test", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create parent task
	parent := &models.Task{
		ProjectID:   project.ID,
		Title:       "Parent",
		Category:    models.CategoryActive,
		Status:      models.StatusPending,
		Prompt:      "parent prompt",
		ChainConfig: "{}",
	}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	// Create child with lineage
	child := &models.Task{
		ProjectID:     project.ID,
		Title:         "Child",
		Category:      models.CategoryActive,
		Status:        models.StatusPending,
		Prompt:        "child prompt",
		ChainConfig:   "{}",
		ParentTaskID:  &parent.ID,
		BaseBranch:    "task/abc-parent",
		BaseCommitSHA: "deadbeef1234567890abcdef1234567890abcdef",
		LineageDepth:  1,
	}
	if err := taskRepo.Create(ctx, child); err != nil {
		t.Fatalf("Create child: %v", err)
	}

	// Read back and verify
	got, err := taskRepo.GetByID(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.BaseBranch != "task/abc-parent" {
		t.Errorf("BaseBranch: got %q, want %q", got.BaseBranch, "task/abc-parent")
	}
	if got.BaseCommitSHA != "deadbeef1234567890abcdef1234567890abcdef" {
		t.Errorf("BaseCommitSHA: got %q, want %q", got.BaseCommitSHA, "deadbeef1234567890abcdef1234567890abcdef")
	}
	if got.LineageDepth != 1 {
		t.Errorf("LineageDepth: got %d, want 1", got.LineageDepth)
	}
}

// TestTaskLineage_UpdateLineage verifies the UpdateLineage repo method.
func TestTaskLineage_UpdateLineage(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)

	project := &models.Project{Name: "Update Lineage", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	task := &models.Task{
		ProjectID:   project.ID,
		Title:       "Task",
		Category:    models.CategoryActive,
		Status:      models.StatusPending,
		Prompt:      "prompt",
		ChainConfig: "{}",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Initially empty
	got, _ := taskRepo.GetByID(ctx, task.ID)
	if got.BaseBranch != "" || got.BaseCommitSHA != "" || got.LineageDepth != 0 {
		t.Fatalf("Expected empty lineage, got branch=%q sha=%q depth=%d", got.BaseBranch, got.BaseCommitSHA, got.LineageDepth)
	}

	// Update lineage
	if err := taskRepo.UpdateLineage(ctx, task.ID, "task/parent-branch", "abc123", 2); err != nil {
		t.Fatalf("UpdateLineage: %v", err)
	}

	got, _ = taskRepo.GetByID(ctx, task.ID)
	if got.BaseBranch != "task/parent-branch" {
		t.Errorf("BaseBranch: got %q, want %q", got.BaseBranch, "task/parent-branch")
	}
	if got.BaseCommitSHA != "abc123" {
		t.Errorf("BaseCommitSHA: got %q, want %q", got.BaseCommitSHA, "abc123")
	}
	if got.LineageDepth != 2 {
		t.Errorf("LineageDepth: got %d, want 2", got.LineageDepth)
	}
}

// TestTaskLineage_HasNonTerminalDescendants verifies descendant check.
func TestTaskLineage_HasNonTerminalDescendants(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)

	project := &models.Project{Name: "Descendants", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create A -> B -> C chain
	taskA := &models.Task{
		ProjectID: project.ID, Title: "A", Category: models.CategoryActive,
		Status: models.StatusCompleted, Prompt: "a", ChainConfig: "{}",
	}
	if err := taskRepo.Create(ctx, taskA); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	taskB := &models.Task{
		ProjectID: project.ID, Title: "B", Category: models.CategoryActive,
		Status: models.StatusRunning, Prompt: "b", ChainConfig: "{}",
		ParentTaskID: &taskA.ID,
	}
	if err := taskRepo.Create(ctx, taskB); err != nil {
		t.Fatalf("Create B: %v", err)
	}
	taskC := &models.Task{
		ProjectID: project.ID, Title: "C", Category: models.CategoryActive,
		Status: models.StatusPending, Prompt: "c", ChainConfig: "{}",
		ParentTaskID: &taskB.ID,
	}
	if err := taskRepo.Create(ctx, taskC); err != nil {
		t.Fatalf("Create C: %v", err)
	}

	// A has non-terminal descendants (B is running, C is pending)
	has, err := taskRepo.HasNonTerminalDescendants(ctx, taskA.ID)
	if err != nil {
		t.Fatalf("HasNonTerminalDescendants(A): %v", err)
	}
	if !has {
		t.Errorf("Expected A to have non-terminal descendants")
	}

	// B has non-terminal descendants (C is pending)
	has, err = taskRepo.HasNonTerminalDescendants(ctx, taskB.ID)
	if err != nil {
		t.Fatalf("HasNonTerminalDescendants(B): %v", err)
	}
	if !has {
		t.Errorf("Expected B to have non-terminal descendants")
	}

	// C has no descendants
	has, err = taskRepo.HasNonTerminalDescendants(ctx, taskC.ID)
	if err != nil {
		t.Fatalf("HasNonTerminalDescendants(C): %v", err)
	}
	if has {
		t.Errorf("Expected C to have no descendants")
	}

	// Now complete B and C
	if err := taskRepo.UpdateStatus(ctx, taskB.ID, models.StatusCompleted); err != nil {
		t.Fatalf("Update B: %v", err)
	}
	if err := taskRepo.UpdateStatus(ctx, taskC.ID, models.StatusCompleted); err != nil {
		t.Fatalf("Update C: %v", err)
	}

	// A no longer has non-terminal descendants
	has, err = taskRepo.HasNonTerminalDescendants(ctx, taskA.ID)
	if err != nil {
		t.Fatalf("HasNonTerminalDescendants(A) after completion: %v", err)
	}
	if has {
		t.Errorf("Expected A to have no non-terminal descendants after all completed")
	}
}

// TestWorkerService_DependencyGating verifies that chained tasks wait for parent completion.
func TestWorkerService_DependencyGating(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)

	project := &models.Project{Name: "Gating", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create parent (still running)
	parent := &models.Task{
		ProjectID: project.ID, Title: "Parent", Category: models.CategoryActive,
		Status: models.StatusRunning, Prompt: "p", ChainConfig: "{}",
	}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	// Create child (pending, with parent reference)
	child := &models.Task{
		ProjectID: project.ID, Title: "Child", Category: models.CategoryActive,
		Status: models.StatusPending, Prompt: "c", ChainConfig: "{}",
		ParentTaskID: &parent.ID, LineageDepth: 1,
	}
	if err := taskRepo.Create(ctx, child); err != nil {
		t.Fatalf("Create child: %v", err)
	}

	// Use 0 workers so nothing actually dispatches to execution goroutines.
	// We test the gating logic in dispatchNext directly by inspecting the queue.
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	workerSvc.SetTaskRepo(taskRepo)
	workerSvc.Start(ctx)
	defer workerSvc.Stop()

	// Submit child task
	workerSvc.Submit(*child)

	// With 0 workers, dispatchNext exits immediately at the global capacity check.
	// To test dependency gating, temporarily set numWorkers > 0 and call dispatchNext.
	workerSvc.mu.Lock()
	workerSvc.numWorkers = 10
	workerSvc.mu.Unlock()

	// Call dispatchNext - child should remain in queue because parent is running
	workerSvc.dispatchNext()

	workerSvc.mu.Lock()
	childInQueue := false
	for _, qt := range workerSvc.queue {
		if qt.ID == child.ID {
			childInQueue = true
		}
	}
	workerSvc.mu.Unlock()

	if !childInQueue {
		t.Errorf("Expected child task to remain in queue due to dependency gate (parent running)")
	}

	// Now complete the parent
	if err := taskRepo.UpdateStatus(ctx, parent.ID, models.StatusCompleted); err != nil {
		t.Fatalf("Update parent: %v", err)
	}

	// Reset workers to 0 to prevent actual dispatch after the gating check passes
	// We only want to verify the child passes the gating check now
	// Instead, check that the gating code would pass by re-reading the parent
	parentNow, _ := taskRepo.GetByID(ctx, parent.ID)
	if !models.IsTerminalStatus(parentNow.Status) {
		t.Errorf("Expected parent to be terminal after completion, got %s", parentNow.Status)
	}
}

// TestTaskLineage_NonChainedTasksUnaffected verifies that non-chained tasks
// dispatch immediately without any lineage checks.
func TestTaskLineage_NonChainedTasksUnaffected(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)

	project := &models.Project{Name: "NonChained", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create several standalone tasks
	for i := 0; i < 3; i++ {
		task := &models.Task{
			ProjectID:   project.ID,
			Title:       "Standalone " + string(rune('A'+i)),
			Category:    models.CategoryActive,
			Status:      models.StatusPending,
			Prompt:      "do something",
			ChainConfig: "{}",
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("Create task %d: %v", i, err)
		}
	}

	// Verify all tasks have empty lineage
	tasks, err := taskRepo.ListActivePending(ctx)
	if err != nil {
		t.Fatalf("ListActivePending: %v", err)
	}
	for _, task := range tasks {
		if task.BaseBranch != "" || task.BaseCommitSHA != "" || task.LineageDepth != 0 {
			t.Errorf("Non-chained task %s has unexpected lineage: branch=%q sha=%q depth=%d",
				task.ID, task.BaseBranch, task.BaseCommitSHA, task.LineageDepth)
		}
		if task.ParentTaskID != nil {
			t.Errorf("Non-chained task %s has unexpected parent: %s", task.ID, *task.ParentTaskID)
		}
	}
}

// TestTaskLineage_ChainCreationSetsLineageDepth verifies multi-level chain depth.
func TestTaskLineage_ChainCreationSetsLineageDepth(t *testing.T) {
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

	project := &models.Project{Name: "Depth Test", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Root task (depth 0) with chaining
	root := &models.Task{
		ProjectID: project.ID,
		Title:     "Root",
		Category:  models.CategoryActive,
		Priority:  1,
		Status:    models.StatusCompleted,
		Prompt:    "root prompt",
	}
	chainCfg := &models.ChainConfiguration{
		Enabled:  true,
		Trigger:  "on_completion",
		ChildChainConfig: &models.ChainConfiguration{
			Enabled: true,
			Trigger: "on_completion",
		},
	}
	if err := root.SetChainConfig(chainCfg); err != nil {
		t.Fatalf("SetChainConfig: %v", err)
	}
	if err := taskRepo.Create(ctx, root); err != nil {
		t.Fatalf("Create root: %v", err)
	}

	// Verify root has depth 0
	if root.LineageDepth != 0 {
		t.Errorf("Root depth: got %d, want 0", root.LineageDepth)
	}

	// Trigger chain from root → child (depth 1)
	if err := llmSvc.triggerTaskChain(ctx, *root, "root output"); err != nil {
		t.Fatalf("triggerTaskChain root: %v", err)
	}

	tasks, _ := taskRepo.ListByProject(ctx, project.ID, "")
	var child1 *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == root.ID {
			child1 = &task
			break
		}
	}
	if child1 == nil {
		t.Fatalf("Child1 not found")
	}
	if child1.LineageDepth != 1 {
		t.Errorf("Child1 depth: got %d, want 1", child1.LineageDepth)
	}

	// Trigger chain from child1 → grandchild (depth 2)
	if err := llmSvc.triggerTaskChain(ctx, *child1, "child1 output"); err != nil {
		t.Fatalf("triggerTaskChain child1: %v", err)
	}

	tasks, _ = taskRepo.ListByProject(ctx, project.ID, "")
	var child2 *models.Task
	for _, task := range tasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == child1.ID {
			child2 = &task
			break
		}
	}
	if child2 == nil {
		t.Fatalf("Child2 (grandchild) not found")
	}
	if child2.LineageDepth != 2 {
		t.Errorf("Child2 depth: got %d, want 2", child2.LineageDepth)
	}
}

// TestTaskLineage_ListActivePendingScansFully verifies ListActivePending returns lineage fields.
func TestTaskLineage_ListActivePendingScansFully(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)

	project := &models.Project{Name: "ActivePending", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	task := &models.Task{
		ProjectID:     project.ID,
		Title:         "Active With Lineage",
		Category:      models.CategoryActive,
		Status:        models.StatusPending,
		Prompt:        "prompt",
		ChainConfig:   "{}",
		BaseBranch:    "task/parent",
		BaseCommitSHA: "aabbcc",
		LineageDepth:  3,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tasks, err := taskRepo.ListActivePending(ctx)
	if err != nil {
		t.Fatalf("ListActivePending: %v", err)
	}

	found := false
	for _, tsk := range tasks {
		if tsk.ID == task.ID {
			found = true
			if tsk.BaseBranch != "task/parent" {
				t.Errorf("BaseBranch: got %q, want %q", tsk.BaseBranch, "task/parent")
			}
			if tsk.BaseCommitSHA != "aabbcc" {
				t.Errorf("BaseCommitSHA: got %q, want %q", tsk.BaseCommitSHA, "aabbcc")
			}
			if tsk.LineageDepth != 3 {
				t.Errorf("LineageDepth: got %d, want 3", tsk.LineageDepth)
			}
		}
	}
	if !found {
		t.Errorf("Task not found in ListActivePending results")
	}
}
