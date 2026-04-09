package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func getDefaultProjectID(t *testing.T, db interface {
	QueryRow(string, ...any) interface{ Scan(...any) error }
}) string {
	t.Helper()
	// The migration seeds a default project
	return "default"
}

func TestTaskRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		//Description: "A test task",
		Category: models.CategoryActive,
		Priority: 1,
		Prompt:   "Do something",
		Status:   models.StatusPending,
	}

	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected ID to be set")
	}

	got, err := repo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected task, got nil")
	}
	if got.Title != "Test Task" {
		t.Errorf("expected Title=Test Task, got %q", got.Title)
	}
	if got.Category != models.CategoryActive {
		t.Errorf("expected Category=active, got %q", got.Category)
	}
	if got.Status != models.StatusPending {
		t.Errorf("expected Status=pending, got %q", got.Status)
	}
}

func TestTaskRepo_ListByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in different categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	// List all
	all, err := repo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("ListByProject all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(all))
	}

	// List by category
	active, err := repo.ListByProject(ctx, "default", "active")
	if err != nil {
		t.Fatalf("ListByProject active: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active tasks, got %d", len(active))
	}

	backlog, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(backlog) != 1 {
		t.Errorf("expected 1 backlog task, got %d", len(backlog))
	}
}

func TestTaskRepo_ListByProject_OrderingFIFO(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with the same priority to test FIFO ordering by created_at
	task1 := &models.Task{ProjectID: "default", Title: "First Task", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Second Task", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Third Task", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// List tasks in the active category
	tasks, err := repo.ListByProject(ctx, "default", "active")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Verify FIFO ordering (oldest first)
	if tasks[0].Title != "First Task" {
		t.Errorf("expected first task to be 'First Task', got %q", tasks[0].Title)
	}
	if tasks[1].Title != "Second Task" {
		t.Errorf("expected second task to be 'Second Task', got %q", tasks[1].Title)
	}
	if tasks[2].Title != "Third Task" {
		t.Errorf("expected third task to be 'Third Task', got %q", tasks[2].Title)
	}

	// Verify created_at timestamps are in ascending order
	if tasks[0].CreatedAt.After(tasks[1].CreatedAt) {
		t.Error("expected tasks[0].CreatedAt <= tasks[1].CreatedAt")
	}
	if tasks[1].CreatedAt.After(tasks[2].CreatedAt) {
		t.Error("expected tasks[1].CreatedAt <= tasks[2].CreatedAt")
	}
}

func TestTaskRepo_ListActivePending(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create various tasks
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Pending", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Running", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog Pending", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	pending, err := repo.ListActivePending(ctx)
	if err != nil {
		t.Fatalf("ListActivePending: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 active+pending task, got %d", len(pending))
	}
	if len(pending) > 0 && pending[0].Title != "Active Pending" {
		t.Errorf("expected Active Pending, got %q", pending[0].Title)
	}
}

// TestTaskRepo_ListActivePending_WithChainConfig verifies that ListActivePending
// correctly scans parent_task_id and chain_config columns. A prior bug had a
// SELECT/Scan mismatch (12 columns selected, 14 scan targets) that caused
// ListActivePending to always error, breaking the scheduler's checkActiveTasks()
// safety net and preventing tasks from transitioning pending → running.
func TestTaskRepo_ListActivePending_WithChainConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a parent task
	parent := &models.Task{
		ProjectID: "default",
		Title:     "Parent Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "parent prompt",
	}
	if err := repo.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	// Create a child task with chain config and parent_task_id
	child := &models.Task{
		ProjectID:    "default",
		Title:        "Child Task",
		Category:     models.CategoryActive,
		Status:       models.StatusPending,
		Prompt:       "child prompt",
		ParentTaskID: &parent.ID,
		ChainConfig:  `{"enabled":true,"trigger":"on_completion"}`,
	}
	if err := repo.Create(ctx, child); err != nil {
		t.Fatalf("Create child: %v", err)
	}

	// ListActivePending must not error (the bug caused scan mismatch here)
	tasks, err := repo.ListActivePending(ctx)
	if err != nil {
		t.Fatalf("ListActivePending: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 active pending tasks, got %d", len(tasks))
	}

	// Find the child task and verify chain fields are populated
	var foundChild *models.Task
	for i := range tasks {
		if tasks[i].Title == "Child Task" {
			foundChild = &tasks[i]
			break
		}
	}
	if foundChild == nil {
		t.Fatal("child task not found in ListActivePending results")
	}
	if foundChild.ParentTaskID == nil || *foundChild.ParentTaskID != parent.ID {
		t.Errorf("expected ParentTaskID=%s, got %v", parent.ID, foundChild.ParentTaskID)
	}
	if foundChild.ChainConfig != `{"enabled":true,"trigger":"on_completion"}` {
		t.Errorf("expected ChainConfig to be set, got %q", foundChild.ChainConfig)
	}
}

// TestTaskRepo_ListByCategory_WithChainConfig verifies ListByCategory correctly
// scans parent_task_id and chain_config columns (same mismatch bug as ListActivePending).
func TestTaskRepo_ListByCategory_WithChainConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID:   "default",
		Title:       "Chained Backlog Task",
		Category:    models.CategoryBacklog,
		Status:      models.StatusPending,
		Prompt:      "test",
		ChainConfig: `{"enabled":true,"trigger":"on_completion"}`,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tasks, err := repo.ListByCategory(ctx, models.CategoryBacklog)
	if err != nil {
		t.Fatalf("ListByCategory: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].ChainConfig != `{"enabled":true,"trigger":"on_completion"}` {
		t.Errorf("expected ChainConfig preserved, got %q", tasks[0].ChainConfig)
	}
}

func TestTaskRepo_UpdateStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Status Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	if err := repo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got.Status != models.StatusRunning {
		t.Errorf("expected Status=running, got %q", got.Status)
	}
}

func TestTaskRepo_UpdateCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Category Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	if err := repo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got.Category != models.CategoryBacklog {
		t.Errorf("expected Category=backlog, got %q", got.Category)
	}
}

func TestTaskRepo_CountByProjectAndCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "A1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "A2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "B1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	counts, err := repo.CountByProjectAndCategory(ctx, "default")
	if err != nil {
		t.Fatalf("CountByProjectAndCategory: %v", err)
	}
	if counts["active"] != 2 {
		t.Errorf("expected active=2, got %d", counts["active"])
	}
	if counts["backlog"] != 1 {
		t.Errorf("expected backlog=1, got %d", counts["backlog"])
	}
}

func TestTaskRepo_CountPendingByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create a second project
	project2 := &models.Project{Name: "Project2", RepoPath: "/tmp/test2"}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("Create project2: %v", err)
	}

	// Create active+pending tasks for default project (should be counted as queue)
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "P1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "P1b", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Create backlog+pending task (should NOT be counted - not in active queue)
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "P2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	// Create a running task for default project (should not be counted)
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "R1", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})

	// Create active+pending task for project2
	repo.Create(ctx, &models.Task{ProjectID: project2.ID, Title: "P3", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Create completed task for project2 (should not be counted)
	repo.Create(ctx, &models.Task{ProjectID: project2.ID, Title: "C1", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Create scheduled+pending task for project2 (should NOT be counted - not in active queue)
	repo.Create(ctx, &models.Task{ProjectID: project2.ID, Title: "S1", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "p"})

	counts, err := repo.CountPendingByProject(ctx)
	if err != nil {
		t.Fatalf("CountPendingByProject: %v", err)
	}

	// Should count only active+pending tasks for default project (2, not 3)
	if counts["default"] != 2 {
		t.Errorf("expected default=2, got %d", counts["default"])
	}

	// Should count 1 active+pending task for project2 (not scheduled or completed)
	if counts[project2.ID] != 1 {
		t.Errorf("expected project2=1, got %d", counts[project2.ID])
	}

	// Should not include projects with no active+pending tasks in the map
	if len(counts) != 2 {
		t.Errorf("expected 2 projects in map, got %d", len(counts))
	}
}

func TestTaskRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "ToDelete", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	if err := repo.Delete(ctx, task.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestTaskRepo_GetByID_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	got, err := repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent")
	}
}

func TestTaskRepo_ClaimTask_PendingSucceeds(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Claim Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	claimed, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if !claimed {
		t.Error("expected claim to succeed for pending task")
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got.Status != models.StatusRunning {
		t.Errorf("expected status=running after claim, got %q", got.Status)
	}
}

func TestTaskRepo_ClaimTask_AlreadyRunningFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Running Claim", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"}
	repo.Create(ctx, task)

	claimed, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed {
		t.Error("expected claim to fail for running task")
	}
}

func TestTaskRepo_ClaimTask_CompletedFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Completed Claim", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	repo.Create(ctx, task)

	claimed, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed {
		t.Error("expected claim to fail for completed task")
	}
}

func TestTaskRepo_ClaimTask_DoubleClaimFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Double Claim", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	// First claim succeeds
	claimed1, _ := repo.ClaimTask(ctx, task.ID)
	if !claimed1 {
		t.Fatal("expected first claim to succeed")
	}

	// Second claim fails (task is now running)
	claimed2, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed2 {
		t.Error("expected second claim to fail (task already claimed)")
	}
}

func TestTaskRepo_MoveCompletedActiveToCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create various tasks
	completedActive1 := &models.Task{ProjectID: "default", Title: "Completed Active 1", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	completedActive2 := &models.Task{ProjectID: "default", Title: "Completed Active 2", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	pendingActive := &models.Task{ProjectID: "default", Title: "Pending Active", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	completedBacklog := &models.Task{ProjectID: "default", Title: "Completed Backlog", Category: models.CategoryBacklog, Status: models.StatusCompleted, Prompt: "p"}
	alreadyCompleted := &models.Task{ProjectID: "default", Title: "Already Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}

	repo.Create(ctx, completedActive1)
	repo.Create(ctx, completedActive2)
	repo.Create(ctx, pendingActive)
	repo.Create(ctx, completedBacklog)
	repo.Create(ctx, alreadyCompleted)

	// Move completed active tasks to completed category
	count, err := repo.MoveCompletedActiveToCompleted(ctx)
	if err != nil {
		t.Fatalf("MoveCompletedActiveToCompleted: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks moved, got %d", count)
	}

	// Verify the tasks were moved
	task1, _ := repo.GetByID(ctx, completedActive1.ID)
	if task1.Category != models.CategoryCompleted {
		t.Errorf("expected task1 category=completed, got %q", task1.Category)
	}

	task2, _ := repo.GetByID(ctx, completedActive2.ID)
	if task2.Category != models.CategoryCompleted {
		t.Errorf("expected task2 category=completed, got %q", task2.Category)
	}

	// Verify other tasks were not affected
	pendingTask, _ := repo.GetByID(ctx, pendingActive.ID)
	if pendingTask.Category != models.CategoryActive {
		t.Errorf("expected pending task still in active, got %q", pendingTask.Category)
	}

	backlogTask, _ := repo.GetByID(ctx, completedBacklog.ID)
	if backlogTask.Category != models.CategoryBacklog {
		t.Errorf("expected backlog task still in backlog, got %q", backlogTask.Category)
	}

	completedTask, _ := repo.GetByID(ctx, alreadyCompleted.ID)
	if completedTask.Category != models.CategoryCompleted {
		t.Errorf("expected completed task still in completed, got %q", completedTask.Category)
	}
}

func TestTaskRepo_MoveCompletedActiveToCompleted_NoCompletedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create only non-completed or non-active tasks
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Pending Active", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Running Active", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})

	// Should move 0 tasks
	count, err := repo.MoveCompletedActiveToCompleted(ctx)
	if err != nil {
		t.Fatalf("MoveCompletedActiveToCompleted: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks moved, got %d", count)
	}
}

func TestTaskRepo_Create_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Duplicate Task",
		//Description: "First task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	if err := repo.Create(ctx, task1); err != nil {
		t.Fatalf("Create first task: %v", err)
	}

	// Attempt to create another task with the same title in the same project
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Duplicate Task",
		//Description: "Second task",
		Category: models.CategoryBacklog,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	err := repo.Create(ctx, task2)
	if err != ErrDuplicateTask {
		t.Errorf("expected ErrDuplicateTask, got %v", err)
	}
}

func TestTaskRepo_Create_SameTitleDifferentProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create a second project
	project2 := &models.Project{
		Name: "Project 2",
		//Description: "Second project",
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create task in first project
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Same Title",
		//Description: "First task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	if err := repo.Create(ctx, task1); err != nil {
		t.Fatalf("Create first task: %v", err)
	}

	// Create task with same title in second project - should succeed
	task2 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Same Title",
		//Description: "Second task",
		Category: models.CategoryBacklog,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	if err := repo.Create(ctx, task2); err != nil {
		t.Errorf("expected no error for same title in different project, got %v", err)
	}
}

func TestTaskRepo_Update_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create two tasks with different titles
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Task 1",
		//Description: "First task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	repo.Create(ctx, task1)

	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Task 2",
		//Description: "Second task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	repo.Create(ctx, task2)

	// Try to update task2 to have the same title as task1
	task2.Title = "Task 1"
	err := repo.Update(ctx, task2)
	if err != ErrDuplicateTask {
		t.Errorf("expected ErrDuplicateTask, got %v", err)
	}
}

func TestTaskRepo_Update_SameTitleAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Original Title",
		//Description: "Original description",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	repo.Create(ctx, task)

	// Update other fields but keep the same title - should succeed
	task.Priority = 5
	if err := repo.Update(ctx, task); err != nil {
		t.Errorf("expected no error when updating task with same title, got %v", err)
	}

	// Verify the update worked
	got, _ := repo.GetByID(ctx, task.ID)
	if got.Priority != 5 {
		t.Errorf("expected priority=5, got %d", got.Priority)
	}
}

func TestTaskRepo_DeleteAllCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create tasks in various categories and statuses
	completedTask1 := &models.Task{ProjectID: project1.ID, Title: "Completed 1", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}
	completedTask2 := &models.Task{ProjectID: project1.ID, Title: "Completed 2", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}
	activeTask := &models.Task{ProjectID: project1.ID, Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	backlogTask := &models.Task{ProjectID: project1.ID, Title: "Backlog Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	// Create completed task in a different project (should NOT be deleted)
	completedTaskProject2 := &models.Task{ProjectID: project2.ID, Title: "Completed Other Project", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}

	repo.Create(ctx, completedTask1)
	repo.Create(ctx, completedTask2)
	repo.Create(ctx, activeTask)
	repo.Create(ctx, backlogTask)
	repo.Create(ctx, completedTaskProject2)

	// Delete all completed tasks for project1
	count, err := repo.DeleteAllCompleted(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllCompleted: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify completed tasks from project1 were deleted
	task1, _ := repo.GetByID(ctx, completedTask1.ID)
	if task1 != nil {
		t.Error("expected completed task 1 to be deleted")
	}

	task2, _ := repo.GetByID(ctx, completedTask2.ID)
	if task2 != nil {
		t.Error("expected completed task 2 to be deleted")
	}

	// Verify other tasks from project1 were not affected
	activeTaskResult, _ := repo.GetByID(ctx, activeTask.ID)
	if activeTaskResult == nil {
		t.Error("expected active task to still exist")
	}

	backlogTaskResult, _ := repo.GetByID(ctx, backlogTask.ID)
	if backlogTaskResult == nil {
		t.Error("expected backlog task to still exist")
	}

	// Verify completed task from project2 was NOT deleted
	taskProject2, _ := repo.GetByID(ctx, completedTaskProject2.ID)
	if taskProject2 == nil {
		t.Error("expected completed task from project2 to still exist")
	}
}

func TestTaskRepo_DeleteAllCompleted_NoCompletedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	// Should delete 0 tasks
	count, err := repo.DeleteAllCompleted(ctx, "default")
	if err != nil {
		t.Fatalf("DeleteAllCompleted: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks deleted, got %d", count)
	}
}

func TestTaskRepo_DeleteAllBacklog(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create backlog tasks for project1
	backlogTask1 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 1",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	backlogTask2 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 2",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask1); err != nil {
		t.Fatalf("failed to create backlog task 1: %v", err)
	}
	if err := repo.Create(ctx, backlogTask2); err != nil {
		t.Fatalf("failed to create backlog task 2: %v", err)
	}

	// Create a backlog task for project2 (should not be deleted)
	backlogTask3 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Backlog Task 3",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask3); err != nil {
		t.Fatalf("failed to create backlog task 3: %v", err)
	}

	// Create tasks in other categories for project1 (should not be deleted)
	activeTask := &models.Task{
		ProjectID: project1.ID,
		Title:     "Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, activeTask); err != nil {
		t.Fatalf("failed to create active task: %v", err)
	}

	// Delete all backlog tasks for project1
	count, err := repo.DeleteAllBacklog(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllBacklog: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify backlog tasks from project1 are deleted
	task, _ := repo.GetByID(ctx, backlogTask1.ID)
	if task != nil {
		t.Error("expected backlog task 1 from project1 to be deleted")
	}
	task, _ = repo.GetByID(ctx, backlogTask2.ID)
	if task != nil {
		t.Error("expected backlog task 2 from project1 to be deleted")
	}

	// Verify backlog task from project2 still exists
	task, _ = repo.GetByID(ctx, backlogTask3.ID)
	if task == nil {
		t.Error("expected backlog task from project2 to still exist")
	}

	// Verify active task from project1 still exists
	task, _ = repo.GetByID(ctx, activeTask.ID)
	if task == nil {
		t.Error("expected active task from project1 to still exist")
	}
}

func TestTaskRepo_DeleteAllBacklog_NoBacklogTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Should delete 0 tasks
	count, err := repo.DeleteAllBacklog(ctx, "default")
	if err != nil {
		t.Fatalf("DeleteAllBacklog: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks deleted, got %d", count)
	}
}

func TestTaskRepo_ActivateAllBacklog(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create backlog tasks for project1
	backlogTask1 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 1",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	backlogTask2 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 2",
		Category:  models.CategoryBacklog,
		Status:    models.StatusCompleted, // Test that status is reset to pending
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask1); err != nil {
		t.Fatalf("failed to create backlog task 1: %v", err)
	}
	if err := repo.Create(ctx, backlogTask2); err != nil {
		t.Fatalf("failed to create backlog task 2: %v", err)
	}

	// Create a backlog task for project2 (should not be activated)
	backlogTask3 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Backlog Task 3",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask3); err != nil {
		t.Fatalf("failed to create backlog task 3: %v", err)
	}

	// Activate all backlog tasks for project1
	count, err := repo.ActivateAllBacklog(ctx, project1.ID)
	if err != nil {
		t.Fatalf("ActivateAllBacklog: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks activated, got %d", count)
	}

	// Verify backlog tasks from project1 are now active with pending status
	task, _ := repo.GetByID(ctx, backlogTask1.ID)
	if task == nil {
		t.Fatal("expected backlog task 1 from project1 to exist")
	}
	if task.Category != models.CategoryActive {
		t.Errorf("expected task 1 category to be active, got %s", task.Category)
	}
	if task.Status != models.StatusPending {
		t.Errorf("expected task 1 status to be pending, got %s", task.Status)
	}

	task, _ = repo.GetByID(ctx, backlogTask2.ID)
	if task == nil {
		t.Fatal("expected backlog task 2 from project1 to exist")
	}
	if task.Category != models.CategoryActive {
		t.Errorf("expected task 2 category to be active, got %s", task.Category)
	}
	if task.Status != models.StatusPending {
		t.Errorf("expected task 2 status to be pending (reset from completed), got %s", task.Status)
	}

	// Verify backlog task from project2 is still backlog
	task, _ = repo.GetByID(ctx, backlogTask3.ID)
	if task == nil {
		t.Fatal("expected backlog task from project2 to exist")
	}
	if task.Category != models.CategoryBacklog {
		t.Errorf("expected task 3 category to still be backlog, got %s", task.Category)
	}
}

func TestTaskRepo_ActivateAllBacklog_NoBacklogTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Should activate 0 tasks
	count, err := repo.ActivateAllBacklog(ctx, "default")
	if err != nil {
		t.Fatalf("ActivateAllBacklog: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks activated, got %d", count)
	}
}

func TestTaskRepo_ActivateAllBacklog_SkipsBlockedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a parent task (needed for foreign key on parent_task_id)
	parentTask := &models.Task{ProjectID: "default", Title: "Parent Task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"}
	if err := repo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task: %v", err)
	}

	// Create a normal backlog task and a blocked backlog task
	normalTask := &models.Task{ProjectID: "default", Title: "Normal Backlog", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	blockedTask := &models.Task{ProjectID: "default", Title: "Blocked Child", Category: models.CategoryBacklog, Status: models.StatusBlocked, Prompt: "waiting", ParentTaskID: &parentTask.ID}
	if err := repo.Create(ctx, normalTask); err != nil {
		t.Fatalf("Create normal task: %v", err)
	}
	if err := repo.Create(ctx, blockedTask); err != nil {
		t.Fatalf("Create blocked task: %v", err)
	}

	count, err := repo.ActivateAllBacklog(ctx, "default")
	if err != nil {
		t.Fatalf("ActivateAllBacklog: %v", err)
	}
	// Only the normal task should be activated; blocked task stays in backlog
	if count != 1 {
		t.Errorf("expected 1 task activated (skipping blocked), got %d", count)
	}

	// Verify blocked child is still in backlog with blocked status
	bt, _ := repo.GetByID(ctx, blockedTask.ID)
	if bt == nil {
		t.Fatal("blocked task not found after ActivateAllBacklog")
	}
	if bt.Category != models.CategoryBacklog {
		t.Errorf("expected blocked child category=backlog, got %s", bt.Category)
	}
	if bt.Status != models.StatusBlocked {
		t.Errorf("expected blocked child status=blocked, got %s", bt.Status)
	}
}

func TestTaskRepo_ReorderTask_MoveDown(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in order: Task 0, Task 1, Task 2, Task 3
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// Move Task 1 (position 1) to position 3 (moving down)
	// Expected order after: Task 0, Task 2, Task 3, Task 1
	if err := repo.ReorderTask(ctx, task1.ID, 3); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify the new order
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(tasks))
	}

	expectedOrder := []string{"Task 0", "Task 2", "Task 3", "Task 1"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		if task.DisplayOrder != i {
			t.Errorf("%s: expected DisplayOrder=%d, got %d", task.Title, i, task.DisplayOrder)
		}
	}
}

func TestTaskRepo_ReorderTask_MoveUp(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in order: Task 0, Task 1, Task 2, Task 3
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// Move Task 3 (position 3) to position 1 (moving up)
	// Expected order after: Task 0, Task 3, Task 1, Task 2
	if err := repo.ReorderTask(ctx, task3.ID, 1); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify the new order
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(tasks))
	}

	expectedOrder := []string{"Task 0", "Task 3", "Task 1", "Task 2"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		if task.DisplayOrder != i {
			t.Errorf("%s: expected DisplayOrder=%d, got %d", task.Title, i, task.DisplayOrder)
		}
	}
}

func TestTaskRepo_ReorderTask_MoveToFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)

	// Move Task 2 to position 0 (first)
	// Expected order: Task 2, Task 0, Task 1
	if err := repo.ReorderTask(ctx, task2.ID, 0); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	expectedOrder := []string{"Task 2", "Task 0", "Task 1"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ReorderTask_MoveToLast(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)

	// Move Task 0 to position 2 (last)
	// Expected order: Task 1, Task 2, Task 0
	if err := repo.ReorderTask(ctx, task0.ID, 2); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	expectedOrder := []string{"Task 1", "Task 2", "Task 0"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ReorderTask_LastTaskToEnd(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// Move the last task (Task 3 at position 3) to position 3 (same position)
	// This simulates dragging the last task to the end of the dropzone
	// With the bug, this would create a gap (position 4), but should stay at position 3
	if err := repo.ReorderTask(ctx, task3.ID, 3); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify order is unchanged
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(tasks))
	}

	expectedOrder := []string{"Task 0", "Task 1", "Task 2", "Task 3"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		// CRITICAL: Verify display_order values are sequential (no gaps)
		if task.DisplayOrder != i {
			t.Errorf("%s: expected DisplayOrder=%d, got %d (gap detected!)", task.Title, i, task.DisplayOrder)
		}
	}
}

func TestTaskRepo_ReorderTask_NoChange(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)

	// Move Task 0 to position 0 (same position)
	if err := repo.ReorderTask(ctx, task0.ID, 0); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify order unchanged
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if tasks[0].Title != "Task 0" || tasks[1].Title != "Task 1" {
		t.Error("expected order to remain unchanged")
	}
}

func TestTaskRepo_ReorderTask_OnlyCategoryTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in different categories
	backlog0 := &models.Task{ProjectID: "default", Title: "Backlog 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	backlog1 := &models.Task{ProjectID: "default", Title: "Backlog 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	active0 := &models.Task{ProjectID: "default", Title: "Active 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	active1 := &models.Task{ProjectID: "default", Title: "Active 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, backlog0)
	repo.Create(ctx, backlog1)
	repo.Create(ctx, active0)
	repo.Create(ctx, active1)

	// Reorder backlog1 to position 0
	if err := repo.ReorderTask(ctx, backlog1.ID, 0); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify only backlog tasks were affected
	backlogTasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(backlogTasks) != 2 {
		t.Fatalf("expected 2 backlog tasks, got %d", len(backlogTasks))
	}
	if backlogTasks[0].Title != "Backlog 1" {
		t.Errorf("expected Backlog 1 first, got %q", backlogTasks[0].Title)
	}

	// Verify active tasks unchanged
	activeTasks, _ := repo.ListByProject(ctx, "default", "active")
	if len(activeTasks) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(activeTasks))
	}
	if activeTasks[0].Title != "Active 0" || activeTasks[1].Title != "Active 1" {
		t.Error("active tasks should not be affected by backlog reorder")
	}
}

func TestTaskRepo_ReorderTask_NonexistentTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	err := repo.ReorderTask(ctx, "nonexistent-id", 0)
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestTaskRepo_Create_AssignsDisplayOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in sequence
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)

	// Verify display orders are assigned sequentially per category
	got0, _ := repo.GetByID(ctx, task0.ID)
	if got0.DisplayOrder != 0 {
		t.Errorf("task0: expected DisplayOrder=0, got %d", got0.DisplayOrder)
	}

	got1, _ := repo.GetByID(ctx, task1.ID)
	if got1.DisplayOrder != 1 {
		t.Errorf("task1: expected DisplayOrder=1, got %d", got1.DisplayOrder)
	}

	// task2 is in a different category, should also start at 0
	got2, _ := repo.GetByID(ctx, task2.ID)
	if got2.DisplayOrder != 0 {
		t.Errorf("task2: expected DisplayOrder=0 (different category), got %d", got2.DisplayOrder)
	}
}

func TestTaskRepo_UpdateCategory_AssignsNewDisplayOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks: 2 in backlog, 1 in active
	backlog0 := &models.Task{ProjectID: "default", Title: "Backlog 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	backlog1 := &models.Task{ProjectID: "default", Title: "Backlog 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	active0 := &models.Task{ProjectID: "default", Title: "Active 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, backlog0)
	repo.Create(ctx, backlog1)
	repo.Create(ctx, active0)

	// Move backlog1 to active - should get display_order = 1 (after active0)
	if err := repo.UpdateCategory(ctx, backlog1.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	got, _ := repo.GetByID(ctx, backlog1.ID)
	if got.DisplayOrder != 1 {
		t.Errorf("expected DisplayOrder=1 after moving to active, got %d", got.DisplayOrder)
	}

	// Verify it appears in correct order
	activeTasks, _ := repo.ListByProject(ctx, "default", "active")
	if len(activeTasks) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(activeTasks))
	}
	if activeTasks[0].Title != "Active 0" || activeTasks[1].Title != "Backlog 1" {
		t.Error("tasks not in expected order after category change")
	}
}

func TestTaskRepo_TaskTags(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Test creating task with feature tag
	featureTask := &models.Task{
		ProjectID: "default",
		Title:     "Feature Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Implement feature",
		Tag:       models.TagFeature,
	}
	if err := repo.Create(ctx, featureTask); err != nil {
		t.Fatalf("Create feature task: %v", err)
	}

	// Test creating task with bug tag
	bugTask := &models.Task{
		ProjectID: "default",
		Title:     "Bug Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Fix bug",
		Tag:       models.TagBug,
	}
	if err := repo.Create(ctx, bugTask); err != nil {
		t.Fatalf("Create bug task: %v", err)
	}

	// Test creating task with no tag
	noTagTask := &models.Task{
		ProjectID: "default",
		Title:     "No Tag Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Do something",
		Tag:       models.TagNone,
	}
	if err := repo.Create(ctx, noTagTask); err != nil {
		t.Fatalf("Create no tag task: %v", err)
	}

	// Verify tags are saved correctly
	gotFeature, err := repo.GetByID(ctx, featureTask.ID)
	if err != nil {
		t.Fatalf("GetByID feature: %v", err)
	}
	if gotFeature.Tag != models.TagFeature {
		t.Errorf("expected Tag=feature, got %q", gotFeature.Tag)
	}

	gotBug, err := repo.GetByID(ctx, bugTask.ID)
	if err != nil {
		t.Fatalf("GetByID bug: %v", err)
	}
	if gotBug.Tag != models.TagBug {
		t.Errorf("expected Tag=bug, got %q", gotBug.Tag)
	}

	gotNoTag, err := repo.GetByID(ctx, noTagTask.ID)
	if err != nil {
		t.Fatalf("GetByID no tag: %v", err)
	}
	if gotNoTag.Tag != models.TagNone {
		t.Errorf("expected Tag='', got %q", gotNoTag.Tag)
	}

	// Test updating task tag
	featureTask.Tag = models.TagBug
	if err := repo.Update(ctx, featureTask); err != nil {
		t.Fatalf("Update tag: %v", err)
	}

	gotUpdated, err := repo.GetByID(ctx, featureTask.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if gotUpdated.Tag != models.TagBug {
		t.Errorf("expected Tag=bug after update, got %q", gotUpdated.Tag)
	}

	// Verify tags are returned in list operations
	allTasks, err := repo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(allTasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(allTasks))
	}

	// Count tasks by tag
	bugCount := 0
	featureCount := 0
	noTagCount := 0
	for _, task := range allTasks {
		switch task.Tag {
		case models.TagBug:
			bugCount++
		case models.TagFeature:
			featureCount++
		case models.TagNone:
			noTagCount++
		}
	}
	if bugCount != 2 {
		t.Errorf("expected 2 bug tasks, got %d", bugCount)
	}
	if featureCount != 0 {
		t.Errorf("expected 0 feature tasks, got %d", featureCount)
	}
	if noTagCount != 1 {
		t.Errorf("expected 1 no-tag task, got %d", noTagCount)
	}
}

// TestTaskRepo_ReorderTask_NonContiguousPositions tests reordering when display_order values have gaps
// This simulates the scenario in the Active column where Running/Queued tasks have non-contiguous positions
func TestTaskRepo_ReorderTask_NonContiguousPositions(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with contiguous positions
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task4 := &models.Task{ProjectID: "default", Title: "Task 4", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0) // display_order = 0
	repo.Create(ctx, task1) // display_order = 1
	repo.Create(ctx, task2) // display_order = 2
	repo.Create(ctx, task3) // display_order = 3
	repo.Create(ctx, task4) // display_order = 4

	// Delete task2 to create a gap (display_order will be: 0, 1, 3, 4)
	repo.Delete(ctx, task2.ID)

	// Now simulate dragging task0 to the end (position after task4)
	// The frontend fix should send position = task4.display_order + 1 = 5
	if err := repo.ReorderTask(ctx, task0.ID, 5); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Expected order: Task 1, Task 3, Task 4, Task 0
	tasks, _ := repo.ListByProject(ctx, "default", "active")
	expectedOrder := []string{"Task 1", "Task 3", "Task 4", "Task 0"}
	if len(tasks) != len(expectedOrder) {
		t.Fatalf("expected %d tasks, got %d", len(expectedOrder), len(tasks))
	}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}

	// Verify task0 moved to the end
	if tasks[len(tasks)-1].Title != "Task 0" {
		t.Errorf("expected Task 0 at end, got %q", tasks[len(tasks)-1].Title)
	}
}

// TestTaskRepo_ReorderTask_SubZoneScenario tests the specific bug where tasks in a sub-zone
// (like Queued in Active column) couldn't be reordered to the end properly
func TestTaskRepo_ReorderTask_SubZoneScenario(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Simulate Running tasks (display_order 0, 1) and Queued tasks (display_order 2, 3, 4)
	running0 := &models.Task{ProjectID: "default", Title: "Running 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	running1 := &models.Task{ProjectID: "default", Title: "Running 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	queued0 := &models.Task{ProjectID: "default", Title: "Queued 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	queued1 := &models.Task{ProjectID: "default", Title: "Queued 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	queued2 := &models.Task{ProjectID: "default", Title: "Queued 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, running0) // display_order = 0
	repo.Create(ctx, running1) // display_order = 1
	repo.Create(ctx, queued0)  // display_order = 2
	repo.Create(ctx, queued1)  // display_order = 3
	repo.Create(ctx, queued2)  // display_order = 4

	// Drag Queued 0 (position 2) to the end of the Queued sub-zone
	// The frontend fix should send position = queued2.display_order + 1 = 5
	// Previously, it would send taskCards.length = 2 (only counting queued1 and queued2 after filtering)
	// which would result in no change since oldPosition=2, newPosition=2
	if err := repo.ReorderTask(ctx, queued0.ID, 5); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Expected order: Running 0, Running 1, Queued 1, Queued 2, Queued 0
	tasks, _ := repo.ListByProject(ctx, "default", "active")
	expectedOrder := []string{"Running 0", "Running 1", "Queued 1", "Queued 2", "Queued 0"}
	if len(tasks) != len(expectedOrder) {
		t.Fatalf("expected %d tasks, got %d", len(expectedOrder), len(tasks))
	}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_DeleteAllChat(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create an agent for executions
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-3-sonnet-20240229",
		APIKey:    "test-key",
		IsDefault: true,
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create chat tasks in project1
	chatTask1 := &models.Task{ProjectID: project1.ID, Title: "Chat 1", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "hello"}
	chatTask2 := &models.Task{ProjectID: project1.ID, Title: "Chat 2", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "world"}
	// Create non-chat task in project1 (should NOT be deleted)
	activeTask := &models.Task{ProjectID: project1.ID, Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	// Create chat task in project2 (should NOT be deleted)
	chatTaskProject2 := &models.Task{ProjectID: project2.ID, Title: "Chat Other", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "other"}

	repo.Create(ctx, chatTask1)
	repo.Create(ctx, chatTask2)
	repo.Create(ctx, activeTask)
	repo.Create(ctx, chatTaskProject2)

	// Create executions for chat tasks (should be cascade-deleted)
	exec1 := &models.Execution{TaskID: chatTask1.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "hello"}
	exec2 := &models.Execution{TaskID: chatTask2.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "world"}
	execRepo.Create(ctx, exec1)
	execRepo.Create(ctx, exec2)

	// Delete all chat tasks for project1
	count, err := repo.DeleteAllChat(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllChat: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify chat tasks from project1 were deleted
	task1, _ := repo.GetByID(ctx, chatTask1.ID)
	if task1 != nil {
		t.Error("expected chat task 1 to be deleted")
	}
	task2, _ := repo.GetByID(ctx, chatTask2.ID)
	if task2 != nil {
		t.Error("expected chat task 2 to be deleted")
	}

	// Verify executions were cascade-deleted
	e1, _ := execRepo.GetByID(ctx, exec1.ID)
	if e1 != nil {
		t.Error("expected execution 1 to be cascade-deleted")
	}
	e2, _ := execRepo.GetByID(ctx, exec2.ID)
	if e2 != nil {
		t.Error("expected execution 2 to be cascade-deleted")
	}

	// Verify active task was NOT deleted
	activeResult, _ := repo.GetByID(ctx, activeTask.ID)
	if activeResult == nil {
		t.Error("expected active task to still exist")
	}

	// Verify chat task from project2 was NOT deleted
	otherResult, _ := repo.GetByID(ctx, chatTaskProject2.ID)
	if otherResult == nil {
		t.Error("expected chat task from project2 to still exist")
	}
}

func TestTaskRepo_DeleteAllChat_NoChatTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Should delete 0 tasks
	count, err := repo.DeleteAllChat(ctx, "default")
	if err != nil {
		t.Fatalf("DeleteAllChat: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks deleted, got %d", count)
	}
}

func TestTaskRepo_ListByProjectWithSort_TitleAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create backlog tasks with different titles
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zebra Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Apple Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Mango Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})

	// Create active task to ensure it's not affected
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Fetch with title ascending sort
	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	// Filter backlog tasks
	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	if len(backlogTasks) != 3 {
		t.Fatalf("expected 3 backlog tasks, got %d", len(backlogTasks))
	}

	expectedOrder := []string{"Apple Task", "Mango Task", "Zebra Task"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_TitleDesc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zebra Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Apple Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Mango Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_desc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	expectedOrder := []string{"Zebra Task", "Mango Task", "Apple Task"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_PriorityDesc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with different priorities
	task1 := &models.Task{ProjectID: "default", Title: "Low Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 1}
	task2 := &models.Task{ProjectID: "default", Title: "High Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 3}
	task3 := &models.Task{ProjectID: "default", Title: "Urgent Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 4}
	task4 := &models.Task{ProjectID: "default", Title: "Normal Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)
	repo.Create(ctx, task4)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "priority_desc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	if len(backlogTasks) != 4 {
		t.Fatalf("expected 4 backlog tasks, got %d", len(backlogTasks))
	}

	expectedPriorities := []int{4, 3, 2, 1}
	for i, task := range backlogTasks {
		if task.Priority != expectedPriorities[i] {
			t.Errorf("position %d: expected priority %d, got %d", i, expectedPriorities[i], task.Priority)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_PriorityAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task1 := &models.Task{ProjectID: "default", Title: "Low Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 1}
	task2 := &models.Task{ProjectID: "default", Title: "High Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 3}
	task3 := &models.Task{ProjectID: "default", Title: "Urgent Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 4}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "priority_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	expectedPriorities := []int{1, 3, 4}
	for i, task := range backlogTasks {
		if task.Priority != expectedPriorities[i] {
			t.Errorf("position %d: expected priority %d, got %d", i, expectedPriorities[i], task.Priority)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_CreatedDesc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in sequence
	task1 := &models.Task{ProjectID: "default", Title: "First", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task2 := &models.Task{ProjectID: "default", Title: "Second", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task3 := &models.Task{ProjectID: "default", Title: "Third", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "created_desc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	// Newest first
	expectedOrder := []string{"Third", "Second", "First"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_CreatedAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task1 := &models.Task{ProjectID: "default", Title: "First", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task2 := &models.Task{ProjectID: "default", Title: "Second", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task3 := &models.Task{ProjectID: "default", Title: "Third", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "created_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	// Oldest first
	expectedOrder := []string{"First", "Second", "Third"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_OnlyBacklogCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in different categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog Z", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog A", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Z", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active A", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	// Fetch with sorting (should only affect backlog)
	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	// Separate tasks by category
	var backlogTasks, activeTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		} else if task.Category == models.CategoryActive {
			activeTasks = append(activeTasks, task)
		}
	}

	// Backlog should be sorted by title
	if len(backlogTasks) != 2 {
		t.Fatalf("expected 2 backlog tasks, got %d", len(backlogTasks))
	}
	if backlogTasks[0].Title != "Backlog A" || backlogTasks[1].Title != "Backlog Z" {
		t.Errorf("backlog tasks not sorted correctly: got %q, %q", backlogTasks[0].Title, backlogTasks[1].Title)
	}

	// Active tasks should maintain default order (display_order, created_at)
	if len(activeTasks) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(activeTasks))
	}
	// Active tasks are created in order Z, A so they should appear in that order (by display_order)
	if activeTasks[0].Title != "Active Z" || activeTasks[1].Title != "Active A" {
		t.Errorf("active tasks affected by backlog sort: got %q, %q", activeTasks[0].Title, activeTasks[1].Title)
	}
}

func TestTaskRepo_ListByProjectWithSort_CaseInsensitive(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with different case
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "apple", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Banana", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "CHERRY", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	// Should be sorted case-insensitively
	expectedOrder := []string{"apple", "Banana", "CHERRY"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_BacklogCategoryFilter(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zebra", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Apple", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	// Fetch only backlog category with sorting
	tasks, err := repo.ListByProjectWithSort(ctx, "default", "backlog", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (backlog only), got %d", len(tasks))
	}

	// Should be sorted
	expectedOrder := []string{"Apple", "Zebra"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		if task.Category != models.CategoryBacklog {
			t.Errorf("expected only backlog tasks, got %s", task.Category)
		}
	}
}

func TestTaskRepo_ListByProjectWithCategorySorts_CompletedTitleAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zulu Done", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Alpha Done", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Mike Done", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithCategorySorts(ctx, "default", "", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithCategorySorts: %v", err)
	}

	var completedTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryCompleted {
			completedTasks = append(completedTasks, task)
		}
	}

	if len(completedTasks) != 3 {
		t.Fatalf("expected 3 completed tasks, got %d", len(completedTasks))
	}

	expectedOrder := []string{"Alpha Done", "Mike Done", "Zulu Done"}
	for i, task := range completedTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithCategorySorts_BacklogAndCompletedIndependent(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog Z", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 1})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog A", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 4})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed Z", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed A", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 3})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Z", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active A", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithCategorySorts(ctx, "default", "", "priority_desc", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithCategorySorts: %v", err)
	}

	var backlogTasks, completedTasks, activeTasks []models.Task
	for _, task := range tasks {
		switch task.Category {
		case models.CategoryBacklog:
			backlogTasks = append(backlogTasks, task)
		case models.CategoryCompleted:
			completedTasks = append(completedTasks, task)
		case models.CategoryActive:
			activeTasks = append(activeTasks, task)
		}
	}

	if len(backlogTasks) != 2 || len(completedTasks) != 2 || len(activeTasks) != 2 {
		t.Fatalf("unexpected category counts: backlog=%d completed=%d active=%d", len(backlogTasks), len(completedTasks), len(activeTasks))
	}

	if backlogTasks[0].Title != "Backlog A" || backlogTasks[1].Title != "Backlog Z" {
		t.Errorf("backlog sort mismatch: got %q, %q", backlogTasks[0].Title, backlogTasks[1].Title)
	}
	if completedTasks[0].Title != "Completed A" || completedTasks[1].Title != "Completed Z" {
		t.Errorf("completed sort mismatch: got %q, %q", completedTasks[0].Title, completedTasks[1].Title)
	}
	// Active tasks should remain in default display_order sequence.
	if activeTasks[0].Title != "Active Z" || activeTasks[1].Title != "Active A" {
		t.Errorf("active tasks affected by category sorts: got %q, %q", activeTasks[0].Title, activeTasks[1].Title)
	}
}

func TestTaskRepo_CountRunningByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with different statuses across projects
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Running 1", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Running 2", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Pending 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Create a second project with tasks
	projRepo := NewProjectRepo(db)
	p2 := &models.Project{Name: "Project 2"}
	projRepo.Create(ctx, p2)
	repo.Create(ctx, &models.Task{ProjectID: p2.ID, Title: "P2 Running", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: p2.ID, Title: "P2 Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Count running for default project
	count, err := repo.CountRunningByProject(ctx, "default")
	if err != nil {
		t.Fatalf("CountRunningByProject: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 running tasks for default project, got %d", count)
	}

	// Count running for project 2
	count, err = repo.CountRunningByProject(ctx, p2.ID)
	if err != nil {
		t.Fatalf("CountRunningByProject p2: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 running task for project 2, got %d", count)
	}

	// Count total running
	total, err := repo.CountRunningTotal(ctx)
	if err != nil {
		t.Fatalf("CountRunningTotal: %v", err)
	}
	if total != 3 {
		t.Errorf("expected 3 total running tasks, got %d", total)
	}
}

func TestTaskRepo_ListActivePending_PriorityOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with different priorities
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Low Priority", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Urgent", Category: models.CategoryActive, Status: models.StatusPending, Priority: 4, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Normal", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "High Priority", Category: models.CategoryActive, Status: models.StatusPending, Priority: 3, Prompt: "p"})

	pending, err := repo.ListActivePending(ctx)
	if err != nil {
		t.Fatalf("ListActivePending: %v", err)
	}
	if len(pending) != 4 {
		t.Fatalf("expected 4 pending tasks, got %d", len(pending))
	}

	// Should be ordered by priority DESC (urgent first)
	expectedOrder := []string{"Urgent", "High Priority", "Normal", "Low Priority"}
	for i, task := range pending {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_SearchByTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with various titles
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Fix login bug", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Add authentication", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Login page redesign", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})
	// Chat tasks should be excluded
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Chat about login", Category: models.CategoryChat, Status: models.StatusPending, Prompt: "p"})

	// Search for "login" - should find 2 non-chat tasks
	results, err := repo.SearchByTitle(ctx, "default", "login")
	if err != nil {
		t.Fatalf("SearchByTitle: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'login', got %d", len(results))
	}
	// Results should not include chat task
	for _, r := range results {
		if r.Category == models.CategoryChat {
			t.Errorf("expected no chat tasks in results, got category=%s title=%q", r.Category, r.Title)
		}
	}

	// Search for "auth" - should find 1
	authResults, err := repo.SearchByTitle(ctx, "default", "auth")
	if err != nil {
		t.Fatalf("SearchByTitle auth: %v", err)
	}
	if len(authResults) != 1 {
		t.Fatalf("expected 1 result for 'auth', got %d", len(authResults))
	}
	if authResults[0].Title != "Add authentication" {
		t.Errorf("expected 'Add authentication', got %q", authResults[0].Title)
	}

	// Search for non-existent title - should find 0
	noResults, err := repo.SearchByTitle(ctx, "default", "nonexistent")
	if err != nil {
		t.Fatalf("SearchByTitle nonexistent: %v", err)
	}
	if len(noResults) != 0 {
		t.Errorf("expected 0 results for 'nonexistent', got %d", len(noResults))
	}
}

func TestTaskRepo_SearchByTitle_ExactMatchFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks where one has an exact title match
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Fix something with login", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "login", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Login page fixes", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	results, err := repo.SearchByTitle(ctx, "default", "login")
	if err != nil {
		t.Fatalf("SearchByTitle: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	// Exact match should be first (case-insensitive)
	if results[0].Title != "login" {
		t.Errorf("expected exact match 'login' first, got %q", results[0].Title)
	}
}

func TestTaskRepo_ListStaleQueuedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a stale queued task (active + queued, old updated_at)
	staleTask := &models.Task{
		ProjectID: "default",
		Title:     "Stale Queued",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, staleTask); err != nil {
		t.Fatalf("Create stale task: %v", err)
	}
	if err := repo.UpdateStatus(ctx, staleTask.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus stale task: %v", err)
	}
	// Set updated_at to 15 minutes ago
	_, err := db.ExecContext(ctx,
		`UPDATE tasks SET updated_at = datetime('now', '-15 minutes') WHERE id = ?`, staleTask.ID)
	if err != nil {
		t.Fatalf("Set stale updated_at: %v", err)
	}

	// Create a recent queued task (should NOT be found)
	recentTask := &models.Task{
		ProjectID: "default",
		Title:     "Recent Queued",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, recentTask); err != nil {
		t.Fatalf("Create recent task: %v", err)
	}
	if err := repo.UpdateStatus(ctx, recentTask.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus recent task: %v", err)
	}

	// Create a pending task (should NOT be found — wrong status)
	pendingTask := &models.Task{
		ProjectID: "default",
		Title:     "Pending Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, pendingTask); err != nil {
		t.Fatalf("Create pending task: %v", err)
	}

	// Query for stale tasks with 10-minute threshold
	staleTasks, err := repo.ListStaleQueuedTasks(ctx, 10*time.Minute)
	if err != nil {
		t.Fatalf("ListStaleQueuedTasks: %v", err)
	}

	if len(staleTasks) != 1 {
		t.Fatalf("expected 1 stale queued task, got %d", len(staleTasks))
	}
	if staleTasks[0].ID != staleTask.ID {
		t.Errorf("expected stale task ID=%s, got %s", staleTask.ID, staleTasks[0].ID)
	}
}
