package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/util"
)

func TestBacklogService_GetDashboard(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Dashboard Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create some backlog tasks
	for i := 0; i < 3; i++ {
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

	// Get dashboard
	data, err := svc.GetDashboard(ctx, project.ID)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}

	if data == nil {
		t.Fatal("expected dashboard data")
	}

	// Stats should reflect 3 backlog tasks
	if data.Stats.BacklogSize != 3 {
		t.Errorf("expected backlog size 3, got %d", data.Stats.BacklogSize)
	}
}

func TestBacklogService_ApplySuggestion_Reprioritize(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Apply Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Reprioritize Me",
		Prompt:    "Test",
		Category:  models.CategoryBacklog,
		Priority:  1,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create and approve suggestion
	newPriority := 4
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionReprioritize,
		Status:            models.SuggestionApproved,
		Title:             "Increase priority",
		TaskID:            &task.ID,
		SuggestedPriority: &newPriority,
		SuggestedSubtasks: "[]",
		Confidence:        0.9,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Apply
	if err := svc.ApplySuggestion(ctx, s.ID); err != nil {
		t.Fatalf("apply suggestion: %v", err)
	}

	// Verify task priority changed
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.Priority != 4 {
		t.Errorf("expected priority 4, got %d", updated.Priority)
	}

	// Verify suggestion status is applied
	applied, _ := backlogRepo.GetSuggestion(ctx, s.ID)
	if applied.Status != models.SuggestionApplied {
		t.Errorf("expected status applied, got %s", applied.Status)
	}
}

func TestBacklogService_ApplySuggestion_QuickWin(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Quick Win Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create backlog task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Quick Win Task",
		Prompt:    "Simple fix",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create and approve quick win suggestion
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionQuickWin,
		Status:            models.SuggestionApproved,
		Title:             "Activate quick win",
		TaskID:            &task.ID,
		SuggestedSubtasks: "[]",
		Confidence:        0.95,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Apply
	if err := svc.ApplySuggestion(ctx, s.ID); err != nil {
		t.Fatalf("apply suggestion: %v", err)
	}

	// Verify task moved to active
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.Category != models.CategoryActive {
		t.Errorf("expected category active, got %s", updated.Category)
	}
}

func TestBacklogService_ApplySuggestion_Obsolete(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Obsolete Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create backlog task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Obsolete Task",
		Prompt:    "Already done",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create and approve obsolete suggestion
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionObsolete,
		Status:            models.SuggestionApproved,
		Title:             "Task is obsolete",
		TaskID:            &task.ID,
		SuggestedSubtasks: "[]",
		Confidence:        0.8,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Apply
	if err := svc.ApplySuggestion(ctx, s.ID); err != nil {
		t.Fatalf("apply suggestion: %v", err)
	}

	// Verify task moved to completed
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.Category != models.CategoryCompleted {
		t.Errorf("expected category completed, got %s", updated.Category)
	}
}

func TestBacklogService_ApplySuggestion_PendingAutoApproves(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project + task
	project := &models.Project{Name: "Auto Approve Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Auto approve task",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create pending suggestion linked to task
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		TaskID:            &task.ID,
		Type:              models.SuggestionQuickWin,
		Status:            models.SuggestionPending,
		Title:             "Pending suggestion",
		SuggestedSubtasks: "[]",
		Confidence:        0.7,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Apply pending suggestion - should auto-approve and succeed
	if err := svc.ApplySuggestion(ctx, s.ID); err != nil {
		t.Fatalf("expected pending suggestion to auto-approve, got error: %v", err)
	}
}

func TestBacklogService_ApplySuggestion_RejectedFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Rejected Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create rejected suggestion
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionQuickWin,
		Status:            models.SuggestionRejected,
		Title:             "Rejected suggestion",
		SuggestedSubtasks: "[]",
		Confidence:        0.7,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Try to apply rejected - should fail
	err := svc.ApplySuggestion(ctx, s.ID)
	if err == nil {
		t.Fatal("expected error when applying rejected suggestion")
	}
}

func TestBacklogService_ApplySuggestion_Schedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Schedule Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create approved schedule suggestion
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionSchedule,
		Status:            models.SuggestionApproved,
		Title:             "Implement feature X",
		Description:       "This feature will improve user experience",
		SuggestedSubtasks: "[]",
		Confidence:        0.85,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Apply the suggestion
	if err := svc.ApplySuggestion(ctx, s.ID); err != nil {
		t.Fatalf("apply suggestion: %v", err)
	}

	// Verify suggestion is marked as applied
	updated, err := backlogRepo.GetSuggestion(ctx, s.ID)
	if err != nil {
		t.Fatalf("get suggestion: %v", err)
	}
	if updated.Status != models.SuggestionApplied {
		t.Errorf("expected status applied, got %s", updated.Status)
	}

	// Verify new task was created
	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}

	// Verify task has correct properties
	task := tasks[0]
	if task.Title != "Implement feature X" {
		t.Errorf("expected title 'Implement feature X', got '%s'", task.Title)
	}
	if task.Prompt != "This feature will improve user experience" {
		t.Errorf("expected prompt 'This feature will improve user experience', got '%s'", task.Prompt)
	}
	if task.Category != models.CategoryBacklog {
		t.Errorf("expected category backlog, got %s", task.Category)
	}
	if task.Priority != 2 {
		t.Errorf("expected priority 2, got %d", task.Priority)
	}
}

func TestBacklogService_ApplySuggestion_Stale(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Stale Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a stale task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Old task",
		Prompt:    "Test",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create approved stale suggestion for the task
	taskID := task.ID
	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionStale,
		Status:            models.SuggestionApproved,
		Title:             "Mark old task as completed",
		TaskID:            &taskID,
		SuggestedSubtasks: "[]",
		Confidence:        0.9,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}

	// Apply the suggestion
	if err := svc.ApplySuggestion(ctx, s.ID); err != nil {
		t.Fatalf("apply suggestion: %v", err)
	}

	// Verify task is marked as completed
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.Category != models.CategoryCompleted {
		t.Errorf("expected category completed, got %s", updated.Category)
	}
}

func TestBacklogService_SnapshotHealth(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	// Create project
	project := &models.Project{Name: "Health Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create some backlog tasks
	for i := 0; i < 10; i++ {
		task := &models.Task{
			ProjectID: project.ID,
			Title:     fmt.Sprintf("Health Task %d", i),
			Prompt:    "Test",
			Category:  models.CategoryBacklog,
			Priority:  2,
			Status:    models.StatusPending,
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	// Snapshot health
	if err := svc.SnapshotHealth(ctx, project.ID); err != nil {
		t.Fatalf("snapshot health: %v", err)
	}

	// Verify snapshot was created
	health, err := backlogRepo.GetLatestHealth(ctx, project.ID)
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if health == nil {
		t.Fatal("expected health snapshot")
	}
	if health.TotalTasks != 10 {
		t.Errorf("expected 10 total tasks, got %d", health.TotalTasks)
	}
	if health.HealthScore <= 0 || health.HealthScore > 100 {
		t.Errorf("expected health score 0-100, got %f", health.HealthScore)
	}
}

func TestComputeHealthScore(t *testing.T) {
	// Perfect backlog: small, young, no stale, few high priority, good velocity
	score := computeHealthScore(10, 5, 0, 2, 3.0)
	if score < 90 {
		t.Errorf("expected score >= 90 for healthy backlog, got %f", score)
	}

	// Bad backlog: large, old, many stale, many high priority, no velocity
	score = computeHealthScore(100, 60, 40, 15, 0)
	if score > 30 {
		t.Errorf("expected score <= 30 for unhealthy backlog, got %f", score)
	}

	// Score should be clamped to 0-100
	score = computeHealthScore(0, 0, 0, 0, 100.0)
	if score > 100 {
		t.Errorf("expected score <= 100, got %f", score)
	}

	score = computeHealthScore(200, 100, 100, 50, 0)
	if score < 0 {
		t.Errorf("expected score >= 0, got %f", score)
	}
}

func TestExtractJSON(t *testing.T) {
	t.Run("json in markdown fences", func(t *testing.T) {
		response := "Here is the analysis:\n```json\n{\"suggestions\": []}\n```\nDone."
		result := util.ExtractJSONObject(response)
		if result != `{"suggestions": []}` {
			t.Errorf("unexpected result: %q", result)
		}
	})

	t.Run("raw json", func(t *testing.T) {
		response := `{"suggestions": [{"type": "stale"}]}`
		result := util.ExtractJSONObject(response)
		if result != response {
			t.Errorf("unexpected result: %q", result)
		}
	})

	t.Run("json embedded in text", func(t *testing.T) {
		response := "Analysis complete. {\"suggestions\": []} End of report."
		result := util.ExtractJSONObject(response)
		if result != `{"suggestions": []}` {
			t.Errorf("unexpected result: %q", result)
		}
	})

	t.Run("no json", func(t *testing.T) {
		response := "No structured output"
		result := util.ExtractJSONObject(response)
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})
}

func TestBacklogService_UpdateAndDeleteSuggestion(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	backlogRepo := repository.NewBacklogRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	project := &models.Project{Name: "CRUD Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	s := &models.BacklogSuggestion{
		ProjectID:         project.ID,
		Type:              models.SuggestionStale,
		Status:            models.SuggestionPending,
		Title:             "Test Suggestion",
		SuggestedSubtasks: "[]",
		Confidence:        0.5,
	}
	if err := backlogRepo.CreateSuggestion(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update status
	if err := svc.UpdateSuggestionStatus(ctx, s.ID, models.SuggestionRejected); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ := backlogRepo.GetSuggestion(ctx, s.ID)
	if got.Status != models.SuggestionRejected {
		t.Errorf("expected rejected, got %s", got.Status)
	}

	// Delete
	if err := svc.DeleteSuggestion(ctx, s.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = backlogRepo.GetSuggestion(ctx, s.ID)
	if got != nil {
		t.Error("expected nil after deletion")
	}
}
