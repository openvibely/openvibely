package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestBacklogRepo_SuggestionCRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := NewBacklogRepo(db)
	projectRepo := NewProjectRepo(db)

	// Create project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a task for linking
	taskRepo := NewTaskRepo(db, nil)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Prompt:    "Test prompt",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create suggestion
	priority := 3
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionReprioritize,
		Status:            models.SuggestionPending,
		Title:             "Increase priority",
		Description:       "This task is now more relevant",
		TaskID:            &task.ID,
		SuggestedPriority: &priority,
		SuggestedSubtasks: "[]",
		Reasoning:         "Recent activity makes this more important",
		Confidence:        0.85,
	}

	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}
	if s.ID == "" {
		t.Fatal("expected suggestion ID to be set")
	}

	// Get suggestion
	got, err := backlogRepo.GetSuggestion(ctx, s.ID)
	if err != nil {
		t.Fatalf("get suggestion: %v", err)
	}
	if got == nil {
		t.Fatal("expected suggestion, got nil")
	}
	if got.Title != "Increase priority" {
		t.Errorf("expected title 'Increase priority', got %q", got.Title)
	}
	if got.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", got.Confidence)
	}

	// List by project
	suggestions, err := backlogRepo.ListByProject(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list by project: %v", err)
	}
	if len(suggestions) != 1 {
		t.Errorf("expected 1 suggestion, got %d", len(suggestions))
	}

	// List by status
	pendingSuggestions, err := backlogRepo.ListByStatus(ctx, project.ID, models.SuggestionPending, 10)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(pendingSuggestions) != 1 {
		t.Errorf("expected 1 pending suggestion, got %d", len(pendingSuggestions))
	}

	// List by type
	reprioritizeSuggestions, err := backlogRepo.ListByType(ctx, project.ID, models.SuggestionReprioritize, 10)
	if err != nil {
		t.Fatalf("list by type: %v", err)
	}
	if len(reprioritizeSuggestions) != 1 {
		t.Errorf("expected 1 reprioritize suggestion, got %d", len(reprioritizeSuggestions))
	}

	// Update status
	if err := backlogRepo.UpdateSuggestionStatus(ctx, s.ID, models.SuggestionApproved); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = backlogRepo.GetSuggestion(ctx, s.ID)
	if got.Status != models.SuggestionApproved {
		t.Errorf("expected status approved, got %s", got.Status)
	}

	// Update to applied (should set applied_at)
	if err := backlogRepo.UpdateSuggestionStatus(ctx, s.ID, models.SuggestionApplied); err != nil {
		t.Fatalf("update to applied: %v", err)
	}
	got, _ = backlogRepo.GetSuggestion(ctx, s.ID)
	if got.AppliedAt == nil {
		t.Error("expected applied_at to be set")
	}

	// Count by status
	counts, err := backlogRepo.CountByStatus(ctx, project.ID)
	if err != nil {
		t.Fatalf("count by status: %v", err)
	}
	if counts["applied"] != 1 {
		t.Errorf("expected 1 applied, got %d", counts["applied"])
	}

	// Delete suggestion
	if err := backlogRepo.DeleteSuggestion(ctx, s.ID); err != nil {
		t.Fatalf("delete suggestion: %v", err)
	}
	got, _ = backlogRepo.GetSuggestion(ctx, s.ID)
	if got != nil {
		t.Error("expected suggestion to be deleted")
	}
}

func TestBacklogRepo_ExistingSuggestion(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := NewBacklogRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	project := &models.Project{Name: "Test Dedup"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Prompt:    "Test",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// No suggestions yet
	exists, err := backlogRepo.ExistingSuggestion(ctx, project.ID, task.ID, models.SuggestionObsolete)
	if err != nil {
		t.Fatalf("existing check: %v", err)
	}
	if exists {
		t.Error("expected no existing suggestion")
	}

	// Create a pending suggestion
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionObsolete,
		Status:            models.SuggestionPending,
		Title:             "Task may be obsolete",
		TaskID:            &task.ID,
		SuggestedSubtasks: "[]",
		Confidence:        0.7,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Now should exist
	exists, err = backlogRepo.ExistingSuggestion(ctx, project.ID, task.ID, models.SuggestionObsolete)
	if err != nil {
		t.Fatalf("existing check: %v", err)
	}
	if !exists {
		t.Error("expected existing suggestion")
	}

	// Different type should not match
	exists, _ = backlogRepo.ExistingSuggestion(ctx, project.ID, task.ID, models.SuggestionDecompose)
	if exists {
		t.Error("expected no match for different type")
	}

	// Reject it - should no longer match
	backlogRepo.UpdateSuggestionStatus(ctx, s.ID, models.SuggestionRejected)
	exists, _ = backlogRepo.ExistingSuggestion(ctx, project.ID, task.ID, models.SuggestionObsolete)
	if exists {
		t.Error("expected no match after rejection")
	}
}

func TestBacklogRepo_HealthSnapshot(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := NewBacklogRepo(db)
	projectRepo := NewProjectRepo(db)

	project := &models.Project{Name: "Test Health"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create health snapshot
	h := &models.BacklogHealthSnapshot{
		ProjectID:          project.ID,
		TotalTasks:         25,
		AvgAgeDays:         12.5,
		StaleCount:         3,
		HighPriorityCount:  5,
		CompletionVelocity: 2.1,
		BottleneckTags:     `["feature"]`,
		HealthScore:        75.0,
		Details:            "{}",
	}
	if err := backlogRepo.CreateHealthSnapshot(ctx, h); err != nil {
		t.Fatalf("create health snapshot: %v", err)
	}
	if h.ID == "" {
		t.Fatal("expected ID to be set")
	}

	// Get latest
	latest, err := backlogRepo.GetLatestHealth(ctx, project.ID)
	if err != nil {
		t.Fatalf("get latest health: %v", err)
	}
	if latest == nil {
		t.Fatal("expected health snapshot")
	}
	if latest.TotalTasks != 25 {
		t.Errorf("expected 25 total tasks, got %d", latest.TotalTasks)
	}
	if latest.HealthScore != 75.0 {
		t.Errorf("expected health score 75.0, got %f", latest.HealthScore)
	}

	// List history
	history, err := backlogRepo.ListHealthSnapshots(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list health: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("expected 1 snapshot, got %d", len(history))
	}
}

func TestBacklogRepo_AnalysisReport(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := NewBacklogRepo(db)
	projectRepo := NewProjectRepo(db)

	project := &models.Project{Name: "Test Reports"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create report
	report := &models.BacklogAnalysisReport{
		ProjectID:     project.ID,
		ReportDate:    "2026-02-28",
		Summary:       "Backlog analysis complete. 5 suggestions generated.",
		SuggestionIDs: `["abc","def"]`,
		Stats:         `{"analyzed":10}`,
	}
	if err := backlogRepo.CreateReport(ctx, report); err != nil {
		t.Fatalf("create report: %v", err)
	}
	if report.ID == "" {
		t.Fatal("expected ID to be set")
	}

	// Get latest
	latest, err := backlogRepo.GetLatestReport(ctx, project.ID)
	if err != nil {
		t.Fatalf("get latest report: %v", err)
	}
	if latest == nil {
		t.Fatal("expected report")
	}
	if latest.Summary != "Backlog analysis complete. 5 suggestions generated." {
		t.Errorf("unexpected summary: %q", latest.Summary)
	}

	// List reports
	reports, err := backlogRepo.ListReports(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(reports) != 1 {
		t.Errorf("expected 1 report, got %d", len(reports))
	}
}

func TestBacklogRepo_AggregateQueries(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := NewBacklogRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	project := &models.Project{Name: "Test Aggregates"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create backlog tasks
	for i := 0; i < 5; i++ {
		task := &models.Task{
			ProjectID: project.ID,
			Title:     fmt.Sprintf("Backlog Task %d", i),
			Prompt:    "Test",
			Category:  models.CategoryBacklog,
			Priority:  2,
			Status:    models.StatusPending,
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	// Create a high priority backlog task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Urgent Backlog",
		Prompt:    "Urgent",
		Category:  models.CategoryBacklog,
		Priority:  4,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Count backlog tasks
	count, err := backlogRepo.CountBacklogTasks(ctx, project.ID)
	if err != nil {
		t.Fatalf("count backlog: %v", err)
	}
	if count != 6 {
		t.Errorf("expected 6 backlog tasks, got %d", count)
	}

	// Count high priority
	highCount, err := backlogRepo.CountHighPriorityBacklog(ctx, project.ID)
	if err != nil {
		t.Fatalf("count high priority: %v", err)
	}
	if highCount != 1 {
		t.Errorf("expected 1 high priority, got %d", highCount)
	}

	// Avg age (should be very small since just created)
	avgAge, err := backlogRepo.AvgBacklogAgeDays(ctx, project.ID)
	if err != nil {
		t.Fatalf("avg age: %v", err)
	}
	if avgAge > 1 {
		t.Errorf("expected avg age < 1 day, got %f", avgAge)
	}

	// Stale count (none should be stale since just created)
	staleCount, err := backlogRepo.CountStaleTasks(ctx, project.ID)
	if err != nil {
		t.Fatalf("stale count: %v", err)
	}
	if staleCount != 0 {
		t.Errorf("expected 0 stale tasks, got %d", staleCount)
	}

	// Completion velocity
	velocity, err := backlogRepo.CompletionVelocity(ctx, project.ID)
	if err != nil {
		t.Fatalf("velocity: %v", err)
	}
	if velocity != 0 {
		t.Errorf("expected 0 velocity, got %f", velocity)
	}

	// Get backlog tasks for analysis
	tasks, err := backlogRepo.GetBacklogTasksForAnalysis(ctx, project.ID)
	if err != nil {
		t.Fatalf("get backlog tasks: %v", err)
	}
	if len(tasks) != 6 {
		t.Errorf("expected 6 tasks for analysis, got %d", len(tasks))
	}
}

func TestBacklogRepo_GetNonExistent(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	backlogRepo := NewBacklogRepo(db)

	// Get non-existent suggestion
	s, err := backlogRepo.GetSuggestion(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s != nil {
		t.Error("expected nil for non-existent suggestion")
	}

	// Get latest health for non-existent project
	h, err := backlogRepo.GetLatestHealth(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h != nil {
		t.Error("expected nil for non-existent health")
	}

	// Get latest report for non-existent project
	r, err := backlogRepo.GetLatestReport(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != nil {
		t.Error("expected nil for non-existent report")
	}
}
