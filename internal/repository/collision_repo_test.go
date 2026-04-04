package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupCollisionTest(t *testing.T) (*CollisionRepo, *ProjectRepo, *TaskRepo, string, string, string) {
	t.Helper()
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	collisionRepo := NewCollisionRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	// Create a project
	project := &models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create two tasks
	taskA := &models.Task{
		ProjectID: project.ID,
		Title:     "Task A",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Add user authentication",
	}
	if err := taskRepo.Create(ctx, taskA); err != nil {
		t.Fatalf("failed to create task A: %v", err)
	}

	taskB := &models.Task{
		ProjectID: project.ID,
		Title:     "Task B",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "Add user profile page",
	}
	if err := taskRepo.Create(ctx, taskB); err != nil {
		t.Fatalf("failed to create task B: %v", err)
	}

	return collisionRepo, projectRepo, taskRepo, project.ID, taskA.ID, taskB.ID
}

func TestCollisionRepo_CreateAndGetImpactAnalysis(t *testing.T) {
	repo, _, _, projectID, taskID, _ := setupCollisionTest(t)
	ctx := context.Background()

	ia := &models.ImpactAnalysis{
		TaskID:             taskID,
		ProjectID:          projectID,
		FilesImpacted:      `["handler.go","service.go"]`,
		APIsImpacted:       `["/api/users"]`,
		SchemasImpacted:    `["users.email"]`,
		ComponentsImpacted: `["UserService"]`,
		ImpactSummary:      "Modifies user authentication flow",
		Confidence:         0.8,
		AnalysisModel:      "claude-sonnet-4",
	}

	if err := repo.CreateImpactAnalysis(ctx, ia); err != nil {
		t.Fatalf("CreateImpactAnalysis failed: %v", err)
	}

	if ia.ID == "" {
		t.Error("expected ID to be set after creation")
	}

	// Retrieve
	got, err := repo.GetImpactAnalysisByTaskID(ctx, taskID)
	if err != nil {
		t.Fatalf("GetImpactAnalysisByTaskID failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected impact analysis, got nil")
	}
	if got.TaskID != taskID {
		t.Errorf("expected task_id %q, got %q", taskID, got.TaskID)
	}
	if got.Confidence != 0.8 {
		t.Errorf("expected confidence 0.8, got %f", got.Confidence)
	}
	if got.ImpactSummary != "Modifies user authentication flow" {
		t.Errorf("unexpected summary: %q", got.ImpactSummary)
	}

	// Non-existent task
	missing, err := repo.GetImpactAnalysisByTaskID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error for missing task: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for non-existent task")
	}
}

func TestCollisionRepo_ListImpactAnalysesByProject(t *testing.T) {
	repo, _, taskRepo, projectID, taskAID, taskBID := setupCollisionTest(t)
	ctx := context.Background()

	// Update tasks to pending status (they already are from setup)
	// Create analyses for both tasks
	ia1 := &models.ImpactAnalysis{
		TaskID:        taskAID,
		ProjectID:     projectID,
		FilesImpacted: `["auth.go"]`,
		Confidence:    0.7,
	}
	if err := repo.CreateImpactAnalysis(ctx, ia1); err != nil {
		t.Fatalf("CreateImpactAnalysis 1 failed: %v", err)
	}

	ia2 := &models.ImpactAnalysis{
		TaskID:        taskBID,
		ProjectID:     projectID,
		FilesImpacted: `["profile.go"]`,
		Confidence:    0.6,
	}
	if err := repo.CreateImpactAnalysis(ctx, ia2); err != nil {
		t.Fatalf("CreateImpactAnalysis 2 failed: %v", err)
	}

	// List should return both (tasks are pending, join with tasks table)
	// But we need tasks to be in pending or running status
	// Tasks are created as pending in setupCollisionTest, good
	analyses, err := repo.ListImpactAnalysesByProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListImpactAnalysesByProject failed: %v", err)
	}
	if len(analyses) != 2 {
		t.Errorf("expected 2 analyses, got %d", len(analyses))
	}

	// Complete task A - should exclude its analysis from list
	if err := taskRepo.UpdateStatus(ctx, taskAID, models.StatusCompleted); err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}

	analyses, err = repo.ListImpactAnalysesByProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListImpactAnalysesByProject after completion failed: %v", err)
	}
	if len(analyses) != 1 {
		t.Errorf("expected 1 analysis after task completion, got %d", len(analyses))
	}
}

func TestCollisionRepo_CreateAndGetConflictPrediction(t *testing.T) {
	repo, _, _, projectID, taskAID, taskBID := setupCollisionTest(t)
	ctx := context.Background()

	cp := &models.ConflictPrediction{
		ProjectID:          projectID,
		TaskAID:            taskAID,
		TaskBID:            taskBID,
		ConflictType:       models.ConflictTypeFile,
		Severity:           models.SeverityHigh,
		Description:        "Both tasks modify handler.go",
		OverlappingResources: `["handler.go"]`,
		ResolutionStrategy: "Execute sequentially",
		Status:             models.ConflictDetected,
	}

	if err := repo.CreateConflictPrediction(ctx, cp); err != nil {
		t.Fatalf("CreateConflictPrediction failed: %v", err)
	}

	if cp.ID == "" {
		t.Error("expected ID to be set")
	}

	// Retrieve
	got, err := repo.GetConflictPrediction(ctx, cp.ID)
	if err != nil {
		t.Fatalf("GetConflictPrediction failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected conflict prediction, got nil")
	}
	if got.Severity != models.SeverityHigh {
		t.Errorf("expected severity high, got %q", got.Severity)
	}
}

func TestCollisionRepo_ListActiveConflicts(t *testing.T) {
	repo, _, _, projectID, taskAID, taskBID := setupCollisionTest(t)
	ctx := context.Background()

	// Create two conflicts
	cp1 := &models.ConflictPrediction{
		ProjectID:    projectID,
		TaskAID:      taskAID,
		TaskBID:      taskBID,
		ConflictType: models.ConflictTypeFile,
		Severity:     models.SeverityCritical,
		Status:       models.ConflictDetected,
	}
	if err := repo.CreateConflictPrediction(ctx, cp1); err != nil {
		t.Fatalf("create cp1: %v", err)
	}

	cp2 := &models.ConflictPrediction{
		ProjectID:    projectID,
		TaskAID:      taskAID,
		TaskBID:      taskBID,
		ConflictType: models.ConflictTypeAPI,
		Severity:     models.SeverityLow,
		Status:       models.ConflictDetected,
	}
	if err := repo.CreateConflictPrediction(ctx, cp2); err != nil {
		t.Fatalf("create cp2: %v", err)
	}

	// List active
	conflicts, err := repo.ListActiveConflicts(ctx, projectID)
	if err != nil {
		t.Fatalf("ListActiveConflicts: %v", err)
	}
	if len(conflicts) != 2 {
		t.Errorf("expected 2 active conflicts, got %d", len(conflicts))
	}
	// Should be ordered by severity (critical first)
	if len(conflicts) >= 2 && conflicts[0].Severity != models.SeverityCritical {
		t.Errorf("expected first conflict to be critical, got %q", conflicts[0].Severity)
	}

	// Resolve one
	if err := repo.UpdateConflictStatus(ctx, cp1.ID, models.ConflictResolved); err != nil {
		t.Fatalf("UpdateConflictStatus: %v", err)
	}

	conflicts, err = repo.ListActiveConflicts(ctx, projectID)
	if err != nil {
		t.Fatalf("ListActiveConflicts after resolve: %v", err)
	}
	if len(conflicts) != 1 {
		t.Errorf("expected 1 active conflict after resolve, got %d", len(conflicts))
	}
}

func TestCollisionRepo_ExistingConflict(t *testing.T) {
	repo, _, _, projectID, taskAID, taskBID := setupCollisionTest(t)
	ctx := context.Background()

	// No conflict exists yet
	exists, err := repo.ExistingConflict(ctx, taskAID, taskBID, models.ConflictTypeFile)
	if err != nil {
		t.Fatalf("ExistingConflict: %v", err)
	}
	if exists {
		t.Error("expected no existing conflict")
	}

	// Create one
	cp := &models.ConflictPrediction{
		ProjectID:    projectID,
		TaskAID:      taskAID,
		TaskBID:      taskBID,
		ConflictType: models.ConflictTypeFile,
		Severity:     models.SeverityMedium,
		Status:       models.ConflictDetected,
	}
	if err := repo.CreateConflictPrediction(ctx, cp); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Now exists
	exists, err = repo.ExistingConflict(ctx, taskAID, taskBID, models.ConflictTypeFile)
	if err != nil {
		t.Fatalf("ExistingConflict: %v", err)
	}
	if !exists {
		t.Error("expected existing conflict")
	}

	// Also detect when task order is reversed
	exists, err = repo.ExistingConflict(ctx, taskBID, taskAID, models.ConflictTypeFile)
	if err != nil {
		t.Fatalf("ExistingConflict reversed: %v", err)
	}
	if !exists {
		t.Error("expected existing conflict with reversed task order")
	}

	// Different type should not exist
	exists, err = repo.ExistingConflict(ctx, taskAID, taskBID, models.ConflictTypeAPI)
	if err != nil {
		t.Fatalf("ExistingConflict different type: %v", err)
	}
	if exists {
		t.Error("expected no conflict for different type")
	}
}

func TestCollisionRepo_ConflictHistory(t *testing.T) {
	repo, _, _, projectID, taskAID, taskBID := setupCollisionTest(t)
	ctx := context.Background()

	ch := &models.ConflictHistory{
		ProjectID:    projectID,
		TaskAID:      taskAID,
		TaskBID:      taskBID,
		WasPredicted: true,
		ConflictType: "file",
		ActualFiles:  `["handler.go"]`,
		Resolution:   "Tasks were serialized",
		ImpactScore:  0.6,
	}
	if err := repo.CreateConflictHistory(ctx, ch); err != nil {
		t.Fatalf("CreateConflictHistory: %v", err)
	}

	if ch.ID == "" {
		t.Error("expected ID to be set")
	}

	// List history
	history, err := repo.ListConflictHistory(ctx, projectID, 10)
	if err != nil {
		t.Fatalf("ListConflictHistory: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("expected 1 history record, got %d", len(history))
	}

	// Check accuracy
	predicted, total, err := repo.PredictionAccuracy(ctx, projectID)
	if err != nil {
		t.Fatalf("PredictionAccuracy: %v", err)
	}
	if predicted != 1 {
		t.Errorf("expected 1 predicted, got %d", predicted)
	}
	if total != 1 {
		t.Errorf("expected 1 total, got %d", total)
	}
}

func TestCollisionRepo_Recommendations(t *testing.T) {
	repo, _, _, projectID, _, _ := setupCollisionTest(t)
	ctx := context.Background()

	rec := &models.ExecutionOrderRecommendation{
		ProjectID:     projectID,
		TaskIDs:       `["task1","task2"]`,
		Reasoning:     "Task 1 before Task 2 to prevent file conflicts",
		ConflictCount: 1,
		BatchGroups:   `[["task1"],["task2"]]`,
		Status:        models.RecommendationPending,
		ExpiresAt:     time.Now().UTC().Add(1 * time.Hour),
	}
	if err := repo.CreateRecommendation(ctx, rec); err != nil {
		t.Fatalf("CreateRecommendation: %v", err)
	}

	if rec.ID == "" {
		t.Error("expected ID to be set")
	}

	// Get latest
	got, err := repo.GetLatestRecommendation(ctx, projectID)
	if err != nil {
		t.Fatalf("GetLatestRecommendation: %v", err)
	}
	if got == nil {
		t.Fatal("expected recommendation, got nil")
	}
	if got.ConflictCount != 1 {
		t.Errorf("expected conflict_count 1, got %d", got.ConflictCount)
	}

	// Accept it
	if err := repo.UpdateRecommendationStatus(ctx, rec.ID, models.RecommendationAccepted); err != nil {
		t.Fatalf("UpdateRecommendationStatus: %v", err)
	}

	// Now latest pending should be nil
	got, err = repo.GetLatestRecommendation(ctx, projectID)
	if err != nil {
		t.Fatalf("GetLatestRecommendation after accept: %v", err)
	}
	if got != nil {
		t.Error("expected nil after accepting recommendation")
	}
}

func TestCollisionRepo_ListConflictsForTask(t *testing.T) {
	repo, _, _, projectID, taskAID, taskBID := setupCollisionTest(t)
	ctx := context.Background()

	cp := &models.ConflictPrediction{
		ProjectID:    projectID,
		TaskAID:      taskAID,
		TaskBID:      taskBID,
		ConflictType: models.ConflictTypeSchema,
		Severity:     models.SeverityCritical,
		Status:       models.ConflictDetected,
	}
	if err := repo.CreateConflictPrediction(ctx, cp); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Search from task A
	conflicts, err := repo.ListConflictsForTask(ctx, taskAID)
	if err != nil {
		t.Fatalf("ListConflictsForTask A: %v", err)
	}
	if len(conflicts) != 1 {
		t.Errorf("expected 1 conflict for task A, got %d", len(conflicts))
	}

	// Search from task B (should also find it)
	conflicts, err = repo.ListConflictsForTask(ctx, taskBID)
	if err != nil {
		t.Fatalf("ListConflictsForTask B: %v", err)
	}
	if len(conflicts) != 1 {
		t.Errorf("expected 1 conflict for task B, got %d", len(conflicts))
	}

	// Non-existent task
	conflicts, err = repo.ListConflictsForTask(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("ListConflictsForTask nonexistent: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts for nonexistent task, got %d", len(conflicts))
	}
}
