package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createGoalTestProject(t *testing.T, ctx context.Context, db *sql.DB) *models.Project {
	t.Helper()
	project := &models.Project{Name: "Goal Project", RepoPath: t.TempDir()}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func createGoalTestTask(t *testing.T, ctx context.Context, taskRepo *TaskRepo, projectID string) *models.Task {
	t.Helper()
	task := &models.Task{ProjectID: projectID, Title: "Goal task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func TestTaskGoalRepo_BlockedAuditAndStaleGoalGuard(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createGoalTestProject(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	task := createGoalTestTask(t, ctx, taskRepo, project.ID)
	repo := NewTaskGoalRepo(db)

	goal := &models.TaskGoal{TaskID: task.ID, GoalID: "goal-1", Objective: "All tests pass", Status: models.TaskGoalStatusActive}
	if err := repo.CreateOrReplace(ctx, goal); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	for i := 1; i <= 2; i++ {
		updated, err := repo.RecordBlockedReport(ctx, task.ID, goal.GoalID, "missing_creds", "missing credentials")
		if err != nil {
			t.Fatalf("blocked report %d: %v", i, err)
		}
		if updated.Status != models.TaskGoalStatusActive || updated.BlockerCount != i {
			t.Fatalf("report %d status/count = %s/%d", i, updated.Status, updated.BlockerCount)
		}
	}
	updated, err := repo.RecordBlockedReport(ctx, task.ID, goal.GoalID, "missing_creds", "missing credentials")
	if err != nil {
		t.Fatalf("third blocked report: %v", err)
	}
	if updated.Status != models.TaskGoalStatusBlocked || updated.BlockerCount != 3 {
		t.Fatalf("third report status/count = %s/%d", updated.Status, updated.BlockerCount)
	}

	replacement := &models.TaskGoal{TaskID: task.ID, GoalID: "goal-2", Objective: "New goal", Status: models.TaskGoalStatusActive}
	if err := repo.CreateOrReplace(ctx, replacement); err != nil {
		t.Fatalf("replace goal: %v", err)
	}
	stale, err := repo.MarkAchieved(ctx, task.ID, "goal-1", "old result")
	if err != nil {
		t.Fatalf("stale achieved: %v", err)
	}
	if stale != nil {
		t.Fatalf("stale update returned goal: %+v", stale)
	}
	current, err := repo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get current: %v", err)
	}
	if current.GoalID != "goal-2" || current.Status != models.TaskGoalStatusActive || current.BlockerCount != 0 {
		t.Fatalf("current goal not preserved/reset: %+v", current)
	}
}

func TestTaskGoalRepo_ResumeStoppedByUserConditional(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createGoalTestProject(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	task := createGoalTestTask(t, ctx, taskRepo, project.ID)
	repo := NewTaskGoalRepo(db)

	goal := &models.TaskGoal{TaskID: task.ID, GoalID: "goal-1", Objective: "Ship it", Status: models.TaskGoalStatusActive}
	if err := repo.CreateOrReplace(ctx, goal); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, err := repo.UpdateStatus(ctx, task.ID, goal.GoalID, models.TaskGoalStatusPaused, "stopped by user", false); err != nil {
		t.Fatalf("pause stopped by user: %v", err)
	}

	// Positive: eligible paused/stopped-by-user goal resumes and clears audit.
	resumed, err := repo.ResumeStoppedByUser(ctx, task.ID, goal.GoalID, "stopped by user", "resumed by web")
	if err != nil {
		t.Fatalf("resume stopped by user: %v", err)
	}
	if resumed == nil || resumed.Status != models.TaskGoalStatusActive || resumed.GoalID != goal.GoalID {
		t.Fatalf("resumed goal = %+v", resumed)
	}
	if resumed.AchievedAt != nil || resumed.BlockerCount != 0 || resumed.BlockerKey != "" {
		t.Fatalf("expected cleared audit after resume, got %+v", resumed)
	}

	// Changed state (cleared) between read and write must be a no-op.
	if _, err := repo.UpdateStatus(ctx, task.ID, goal.GoalID, models.TaskGoalStatusPaused, "stopped by user", false); err != nil {
		t.Fatalf("re-pause stopped by user: %v", err)
	}
	if err := repo.Clear(ctx, task.ID, "cleared by user"); err != nil {
		t.Fatalf("clear goal: %v", err)
	}
	noop, err := repo.ResumeStoppedByUser(ctx, task.ID, goal.GoalID, "stopped by user", "resumed by web")
	if err != nil {
		t.Fatalf("conditional resume on cleared goal: %v", err)
	}
	if noop != nil {
		t.Fatalf("expected nil no-op on cleared goal, got %+v", noop)
	}
	current, err := repo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get current: %v", err)
	}
	if current.Status != models.TaskGoalStatusCleared || current.Reason != "cleared by user" {
		t.Fatalf("expected cleared goal preserved, got %+v", current)
	}

	// Paused with a different reason must also be a no-op.
	if _, err := repo.UpdateStatus(ctx, task.ID, goal.GoalID, models.TaskGoalStatusPaused, "paused by user", false); err != nil {
		t.Fatalf("pause with other reason: %v", err)
	}
	noop2, err := repo.ResumeStoppedByUser(ctx, task.ID, goal.GoalID, "stopped by user", "resumed by web")
	if err != nil {
		t.Fatalf("conditional resume on other-reason pause: %v", err)
	}
	if noop2 != nil {
		t.Fatalf("expected nil no-op on other-reason pause, got %+v", noop2)
	}
	current, _ = repo.GetByTaskID(ctx, task.ID)
	if current.Status != models.TaskGoalStatusPaused || current.Reason != "paused by user" {
		t.Fatalf("expected other-reason pause preserved, got %+v", current)
	}
}

func TestTaskRepo_ImmediateTransactionCommitUsesCallerContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx, cancel := context.WithCancel(context.Background())

	err := repo.withImmediateTx(ctx, func(sqlExecutor) error {
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("commit succeeded after the caller context was canceled")
	}
}

func TestTaskRepo_CreateWithGoalCommitDoesNotPanic(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createGoalTestProject(t, ctx, db)
	repo := NewTaskRepo(db, nil)
	goalRepo := NewTaskGoalRepo(db)

	task := &models.Task{ProjectID: project.ID, Title: "Commit goal", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	goal := &models.TaskGoal{GoalID: "goal-commit", Objective: "commit both rows", Status: models.TaskGoalStatusActive}
	var createErr error
	if didPanic := func() (didPanic bool) {
		defer func() {
			didPanic = recover() != nil
		}()
		createErr = repo.CreateWithGoal(ctx, task, goal)
		return false
	}(); didPanic {
		t.Fatal("CreateWithGoal panicked while committing the task and goal transaction")
	}
	if createErr != nil {
		t.Fatalf("create with goal: %v", createErr)
	}
	storedTask, err := repo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if storedTask == nil {
		t.Fatal("committed task was not found")
	}
	storedGoal, err := goalRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get goal: %v", err)
	}
	if storedGoal == nil || storedGoal.GoalID != "goal-commit" {
		t.Fatalf("committed goal = %+v", storedGoal)
	}
}

func TestTaskRepo_CreateWithGoalAtomic(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createGoalTestProject(t, ctx, db)
	repo := NewTaskRepo(db, nil)
	goalRepo := NewTaskGoalRepo(db)

	task := &models.Task{ProjectID: project.ID, Title: "Atomic goal", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	goal := &models.TaskGoal{GoalID: "goal-atomic", Objective: "done", Status: models.TaskGoalStatusActive}
	if err := repo.CreateWithGoal(ctx, task, goal); err != nil {
		t.Fatalf("create with goal: %v", err)
	}
	stored, err := goalRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get goal: %v", err)
	}
	if stored == nil || stored.Objective != "done" || stored.GoalID != "goal-atomic" {
		t.Fatalf("stored goal = %+v", stored)
	}
}
