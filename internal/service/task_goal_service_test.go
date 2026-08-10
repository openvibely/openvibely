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

func TestTaskGoalService_ClearBlockedReportClearsOnlyMatchingBlocker(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createServiceGoalTestProject(t, ctx, db)
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "PR Blocker Goal", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "prompt", Priority: 2}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	goal, err := svc.SetGoal(ctx, task.ID, "Publish the PR", GoalOptions{Actor: "test"})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.RecordBlockedReport(ctx, task.ID, goal.GoalID, GitHubPRPublicationBlockerKey, "PR publication failed"); err != nil {
			t.Fatalf("record publication blocker: %v", err)
		}
	}
	blocked, err := svc.GetGoal(ctx, task.ID)
	if err != nil {
		t.Fatalf("get blocked goal: %v", err)
	}
	if blocked.Status != models.TaskGoalStatusBlocked || blocked.BlockerKey != GitHubPRPublicationBlockerKey {
		t.Fatalf("blocked goal = %+v", blocked)
	}
	cleared, err := svc.ClearBlockedReport(ctx, task.ID, GitHubPRPublicationBlockerKey, "GitHub PR publication succeeded with PR #77")
	if err != nil {
		t.Fatalf("clear matching blocker: %v", err)
	}
	if cleared == nil || cleared.Status != models.TaskGoalStatusActive || cleared.BlockerKey != "" || cleared.BlockerCount != 0 {
		t.Fatalf("cleared goal = %+v", cleared)
	}
	if cleared.Reason != "GitHub PR publication succeeded with PR #77" {
		t.Fatalf("clear reason = %q", cleared.Reason)
	}
	if _, err := svc.RecordBlockedReport(ctx, task.ID, goal.GoalID, "different-blocker", "Different blocker"); err != nil {
		t.Fatalf("record different blocker: %v", err)
	}
	unchanged, err := svc.ClearBlockedReport(ctx, task.ID, GitHubPRPublicationBlockerKey, "should not clear")
	if err != nil {
		t.Fatalf("clear nonmatching blocker: %v", err)
	}
	if unchanged != nil {
		t.Fatalf("expected no-op for nonmatching blocker, got %+v", unchanged)
	}
	latest, err := svc.GetGoal(ctx, task.ID)
	if err != nil {
		t.Fatalf("get latest goal: %v", err)
	}
	if latest.BlockerKey != "different-blocker" || latest.BlockerCount != 1 {
		t.Fatalf("nonmatching blocker was cleared: %+v", latest)
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
