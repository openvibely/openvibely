package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createServiceGoalTestProject(t *testing.T, ctx context.Context, db *sql.DB) *models.Project {
	t.Helper()
	project := &models.Project{Name: "Goal Service Project", RepoPath: t.TempDir()}
	if err := repository.NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func TestTaskGoalService_ValidationPauseResumeClear(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createServiceGoalTestProject(t, ctx, db)
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "Goal svc", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)

	if _, err := svc.SetGoal(ctx, task.ID, "   ", GoalOptions{}); !errors.Is(err, ErrTaskGoalEmpty) {
		t.Fatalf("empty goal error = %v", err)
	}
	if _, err := svc.SetGoal(ctx, task.ID, strings.Repeat("x", MaxTaskGoalLength+1), GoalOptions{}); !errors.Is(err, ErrTaskGoalTooLong) {
		t.Fatalf("long goal error = %v", err)
	}
	goal, err := svc.SetGoal(ctx, task.ID, " All checks pass ", GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if goal.Objective != "All checks pass" || goal.Status != models.TaskGoalStatusActive {
		t.Fatalf("goal = %+v", goal)
	}
	if err := svc.PauseGoal(ctx, task.ID, "test"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused, _ := svc.GetGoal(ctx, task.ID)
	if paused.Status != models.TaskGoalStatusPaused || paused.Objective != goal.Objective {
		t.Fatalf("paused goal = %+v", paused)
	}
	if _, err := svc.RecordBlockedReport(ctx, task.ID, paused.GoalID, "blocked", "blocked"); !errors.Is(err, ErrTaskGoalStaleUpdate) {
		t.Fatalf("blocked report on paused goal error = %v", err)
	}
	if _, err := svc.MarkAchieved(ctx, task.ID, paused.GoalID, "done"); !errors.Is(err, ErrTaskGoalStaleUpdate) {
		t.Fatalf("mark achieved on paused goal error = %v", err)
	}
	if err := svc.ResumeGoal(ctx, task.ID, "test"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := svc.RecordBlockedReport(ctx, task.ID, paused.GoalID, "blocked", "blocked"); err != nil {
		t.Fatalf("blocked report: %v", err)
	}
	if err := svc.PauseGoal(ctx, task.ID, "test"); err != nil {
		t.Fatalf("pause with audit: %v", err)
	}
	paused, _ = svc.GetGoal(ctx, task.ID)
	if paused.BlockerCount == 0 {
		t.Fatalf("expected blocker audit before resume, got %+v", paused)
	}
	if err := svc.ResumeGoal(ctx, task.ID, "test"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumed, _ := svc.GetGoal(ctx, task.ID)
	if resumed.Status != models.TaskGoalStatusActive || resumed.BlockerCount != 0 || resumed.GoalID != paused.GoalID {
		t.Fatalf("resumed goal = %+v", resumed)
	}
	if err := svc.ResumeGoal(ctx, task.ID, "test"); !errors.Is(err, ErrTaskGoalNotPaused) {
		t.Fatalf("resume active error = %v", err)
	}
	if _, err := svc.MarkAchieved(ctx, task.ID, "stale-goal-id", "done"); !errors.Is(err, ErrTaskGoalStaleUpdate) {
		t.Fatalf("stale achieved error = %v", err)
	}
	achieved, err := svc.MarkAchieved(ctx, task.ID, resumed.GoalID, "done")
	if err != nil {
		t.Fatalf("mark achieved before reactivation: %v", err)
	}
	if achieved.Status != models.TaskGoalStatusAchieved || achieved.AchievedAt == nil {
		t.Fatalf("achieved goal = %+v", achieved)
	}
	reactivated, err := svc.ReactivateAchievedGoal(ctx, task.ID, "web")
	if err != nil {
		t.Fatalf("reactivate achieved: %v", err)
	}
	if reactivated == nil || reactivated.Status != models.TaskGoalStatusActive || reactivated.GoalID != achieved.GoalID || reactivated.AchievedAt != nil {
		t.Fatalf("reactivated goal = %+v", reactivated)
	}
	reactivatedAgain, err := svc.ReactivateAchievedGoal(ctx, task.ID, "web")
	if err != nil {
		t.Fatalf("reactivate active goal: %v", err)
	}
	if reactivatedAgain != nil {
		t.Fatalf("expected no-op reactivating non-achieved goal, got %+v", reactivatedAgain)
	}
	if err := svc.ClearGoal(ctx, task.ID, "test"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared, _ := svc.GetGoal(ctx, task.ID)
	if cleared.Status != models.TaskGoalStatusCleared {
		t.Fatalf("cleared goal = %+v", cleared)
	}
	if _, err := svc.MarkAchieved(ctx, task.ID, cleared.GoalID, "done"); !errors.Is(err, ErrTaskGoalStaleUpdate) {
		t.Fatalf("mark achieved on cleared goal error = %v", err)
	}
	if _, err := svc.RecordBlockedReport(ctx, task.ID, cleared.GoalID, "blocked", "blocked"); !errors.Is(err, ErrTaskGoalStaleUpdate) {
		t.Fatalf("blocked report on cleared goal error = %v", err)
	}
}

func TestTaskGoalService_UserStopPausePreservesGoalForResume(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createServiceGoalTestProject(t, ctx, db)
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "User Stop Goal", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	goal, err := svc.SetGoal(ctx, task.ID, "Keep working until done", GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}

	if err := svc.PauseActiveGoalStoppedByUser(ctx, task.ID); err != nil {
		t.Fatalf("pause after user stop: %v", err)
	}
	paused, err := svc.GetGoal(ctx, task.ID)
	if err != nil {
		t.Fatalf("get paused goal: %v", err)
	}
	if paused.Status != models.TaskGoalStatusPaused || paused.GoalID != goal.GoalID || paused.Reason != "stopped by user" {
		t.Fatalf("paused after user stop = %+v", paused)
	}

	if err := svc.ResumeGoal(ctx, task.ID, "user"); err != nil {
		t.Fatalf("resume user-stopped goal: %v", err)
	}
	resumed, err := svc.GetGoal(ctx, task.ID)
	if err != nil {
		t.Fatalf("get resumed goal: %v", err)
	}
	if resumed.Status != models.TaskGoalStatusActive || resumed.GoalID != goal.GoalID || resumed.Objective != goal.Objective {
		t.Fatalf("resumed user-stopped goal = %+v", resumed)
	}
}

func TestTaskGoalService_ResumeGoalStoppedByUserPositive(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createServiceGoalTestProject(t, ctx, db)
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "Resume Stopped", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	goal, err := svc.SetGoal(ctx, task.ID, "Keep working until done", GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := svc.PauseActiveGoalStoppedByUser(ctx, task.ID); err != nil {
		t.Fatalf("pause after user stop: %v", err)
	}

	// Build up blocker audit so we can prove the resume clears it.
	if _, rerr := svc.repo.UpdateStatus(ctx, task.ID, goal.GoalID, models.TaskGoalStatusPaused, "stopped by user", false); rerr != nil {
		t.Fatalf("prime paused state: %v", rerr)
	}

	resumed, err := svc.ResumeGoalStoppedByUser(ctx, task.ID, "web")
	if err != nil {
		t.Fatalf("resume stopped-by-user goal: %v", err)
	}
	if resumed == nil {
		t.Fatalf("expected eligible goal to resume, got nil")
	}
	if resumed.Status != models.TaskGoalStatusActive || resumed.GoalID != goal.GoalID {
		t.Fatalf("resumed goal = %+v", resumed)
	}
	if resumed.BlockerCount != 0 || resumed.BlockerKey != "" || resumed.AchievedAt != nil {
		t.Fatalf("expected cleared audit state after resume, got %+v", resumed)
	}

	current, _ := svc.GetGoal(ctx, task.ID)
	if current.Status != models.TaskGoalStatusActive {
		t.Fatalf("persisted status = %s", current.Status)
	}
}

func TestTaskGoalService_ResumeGoalStoppedByUserNoOpOnConcurrentClear(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createServiceGoalTestProject(t, ctx, db)
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "Concurrent Clear", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	repo := repository.NewTaskGoalRepo(db)
	svc := NewTaskGoalService(repo, taskRepo, nil)
	goal, err := svc.SetGoal(ctx, task.ID, "Keep working until done", GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := svc.PauseActiveGoalStoppedByUser(ctx, task.ID); err != nil {
		t.Fatalf("pause after user stop: %v", err)
	}

	// Simulate a concurrent clear that lands after resume eligibility is observed
	// but before the conditional write, by clearing directly at the repository.
	if err := repo.Clear(ctx, task.ID, "cleared by user"); err != nil {
		t.Fatalf("concurrent clear: %v", err)
	}

	// The conditional resume must be a benign no-op and must not error.
	resumed, err := svc.ResumeGoalStoppedByUser(ctx, task.ID, "web")
	if err != nil {
		t.Fatalf("expected no error on changed-state no-op, got %v", err)
	}
	if resumed != nil {
		t.Fatalf("expected no-op resume, got %+v", resumed)
	}

	current, _ := svc.GetGoal(ctx, task.ID)
	if current.Status != models.TaskGoalStatusCleared {
		t.Fatalf("expected goal to remain cleared, got %s", current.Status)
	}
	if current.Reason != "cleared by user" {
		t.Fatalf("expected clear reason preserved, got %q", current.Reason)
	}
	if current.GoalID != goal.GoalID {
		t.Fatalf("goal_id changed unexpectedly, got %q", current.GoalID)
	}
}

func TestTaskGoalService_ResumeGoalStoppedByUserNoOpOnPauseWithOtherReason(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createServiceGoalTestProject(t, ctx, db)
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "Other Pause Reason", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	repo := repository.NewTaskGoalRepo(db)
	svc := NewTaskGoalService(repo, taskRepo, nil)
	goal, err := svc.SetGoal(ctx, task.ID, "Keep working until done", GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := svc.PauseActiveGoalStoppedByUser(ctx, task.ID); err != nil {
		t.Fatalf("pause after user stop: %v", err)
	}

	// A concurrent explicit pause with a different reason must not be resumed.
	if _, rerr := repo.UpdateStatus(ctx, task.ID, goal.GoalID, models.TaskGoalStatusPaused, "paused by user", false); rerr != nil {
		t.Fatalf("re-pause with other reason: %v", rerr)
	}

	resumed, err := svc.ResumeGoalStoppedByUser(ctx, task.ID, "web")
	if err != nil {
		t.Fatalf("expected no error on ineligible state, got %v", err)
	}
	if resumed != nil {
		t.Fatalf("expected no-op resume, got %+v", resumed)
	}

	current, _ := svc.GetGoal(ctx, task.ID)
	if current.Status != models.TaskGoalStatusPaused || current.Reason != "paused by user" {
		t.Fatalf("expected paused-by-user preserved, got status=%s reason=%q", current.Status, current.Reason)
	}
}

func TestTaskGoalService_ResumeBlockedGoalResetsBlockerCount(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createServiceGoalTestProject(t, ctx, db)
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "Blocked Resume", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)

	goal, err := svc.SetGoal(ctx, task.ID, "Ship the feature", GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}

	// Report blockers until the goal transitions to blocked status (threshold = 3).
	for i := 1; i <= 3; i++ {
		updated, rerr := svc.RecordBlockedReport(ctx, task.ID, goal.GoalID, "ci-failure", "CI is red")
		if rerr != nil {
			t.Fatalf("RecordBlockedReport %d: %v", i, rerr)
		}
		if i < 3 && updated.Status != models.TaskGoalStatusActive {
			t.Fatalf("expected active before threshold at report %d, got %s", i, updated.Status)
		}
	}

	blocked, _ := svc.GetGoal(ctx, task.ID)
	if blocked.Status != models.TaskGoalStatusBlocked || blocked.BlockerCount != 3 {
		t.Fatalf("expected blocked with count=3 before resume, got status=%s count=%d", blocked.Status, blocked.BlockerCount)
	}

	// Resuming a blocked goal must succeed and clear blocker audit state.
	if err := svc.ResumeGoal(ctx, task.ID, "user"); err != nil {
		t.Fatalf("ResumeGoal on blocked goal: %v", err)
	}

	resumed, _ := svc.GetGoal(ctx, task.ID)
	if resumed.Status != models.TaskGoalStatusActive {
		t.Fatalf("expected active after resume, got %s", resumed.Status)
	}
	if resumed.BlockerCount != 0 {
		t.Fatalf("expected blocker_count=0 after resume, got %d", resumed.BlockerCount)
	}
	if resumed.BlockerKey != "" {
		t.Fatalf("expected blocker_key='' after resume, got %q", resumed.BlockerKey)
	}
	if resumed.GoalID != goal.GoalID {
		t.Fatalf("expected same goal_id after resume, got %q (want %q)", resumed.GoalID, goal.GoalID)
	}

	// A second ResumeGoal on the now-active goal must return ErrTaskGoalNotPaused.
	if err := svc.ResumeGoal(ctx, task.ID, "user"); !errors.Is(err, ErrTaskGoalNotPaused) {
		t.Fatalf("expected ErrTaskGoalNotPaused resuming active goal, got %v", err)
	}
}
