package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createTestProject(t *testing.T, projectRepo *ProjectRepo) models.Project {
	t.Helper()
	p := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("creating test project: %v", err)
	}
	return *p
}

func TestAlertRepo_Create(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)

	a := &models.Alert{
		ProjectID: project.ID,
		Type:      models.AlertTaskFailed,
		Severity:  models.SeverityError,
		Title:     "Task failed",
		Message:   "Something went wrong",
	}

	err := alertRepo.Create(context.Background(), a)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.ID == "" {
		t.Fatal("expected alert ID to be set")
	}
	if a.IsRead {
		t.Fatal("expected new alert to be unread")
	}
}

func TestAlertRepo_ListByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)

	// Create two alerts
	for i := 0; i < 2; i++ {
		a := &models.Alert{
			ProjectID: project.ID,
			Type:      models.AlertTaskFailed,
			Severity:  models.SeverityError,
			Title:     "Task failed",
			Message:   "Error details",
		}
		if err := alertRepo.Create(context.Background(), a); err != nil {
			t.Fatalf("creating alert: %v", err)
		}
	}

	alerts, err := alertRepo.ListByProject(context.Background(), project.ID, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(alerts))
	}
}

func TestAlertRepo_CountUnread(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)

	// Create 3 alerts
	for i := 0; i < 3; i++ {
		a := &models.Alert{
			ProjectID: project.ID,
			Type:      models.AlertTaskFailed,
			Severity:  models.SeverityError,
			Title:     "Task failed",
		}
		if err := alertRepo.Create(context.Background(), a); err != nil {
			t.Fatalf("creating alert: %v", err)
		}
	}

	count, err := alertRepo.CountUnread(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 unread, got %d", count)
	}
}

func TestAlertRepo_MarkRead(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)

	a := &models.Alert{
		ProjectID: project.ID,
		Type:      models.AlertTaskFailed,
		Severity:  models.SeverityError,
		Title:     "Task failed",
	}
	if err := alertRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("creating alert: %v", err)
	}

	if err := alertRepo.MarkRead(context.Background(), project.ID, a.ID); err != nil {
		t.Fatalf("marking read: %v", err)
	}

	count, err := alertRepo.CountUnread(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("counting unread: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unread after marking read, got %d", count)
	}
}

func TestAlertRepo_MarkAllRead(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)

	for i := 0; i < 3; i++ {
		a := &models.Alert{
			ProjectID: project.ID,
			Type:      models.AlertTaskFailed,
			Severity:  models.SeverityError,
			Title:     "Task failed",
		}
		if err := alertRepo.Create(context.Background(), a); err != nil {
			t.Fatalf("creating alert: %v", err)
		}
	}

	if err := alertRepo.MarkAllRead(context.Background(), project.ID); err != nil {
		t.Fatalf("marking all read: %v", err)
	}

	count, err := alertRepo.CountUnread(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("counting unread: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unread after marking all read, got %d", count)
	}
}

func TestAlertRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)

	a := &models.Alert{
		ProjectID: project.ID,
		Type:      models.AlertTaskFailed,
		Severity:  models.SeverityError,
		Title:     "Task failed",
	}
	if err := alertRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("creating alert: %v", err)
	}

	if err := alertRepo.Delete(context.Background(), project.ID, a.ID); err != nil {
		t.Fatalf("deleting alert: %v", err)
	}

	alerts, err := alertRepo.ListByProject(context.Background(), project.ID, 50)
	if err != nil {
		t.Fatalf("listing alerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected 0 alerts after delete, got %d", len(alerts))
	}
}

func TestAlertRepo_ProjectIsolationAndActionableLifecycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)
	project1 := createTestProject(t, projectRepo)
	project2 := &models.Project{Name: "Other Project"}
	if err := projectRepo.Create(context.Background(), project2); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	a := &models.Alert{
		ProjectID:       project1.ID,
		Scope:           models.AlertScopeProject,
		Type:            models.AlertType("suggestion"),
		Severity:        models.SeverityInfo,
		Title:           "Scoped suggestion",
		Message:         "Summary",
		Body:            "Full review body",
		Source:          "test",
		DecisionState:   models.AlertDecisionPending,
		ProcessingState: models.AlertProcessingUnclaimed,
		Metadata:        map[string]any{"component": "alerts"},
		IdempotencyKey:  "suggestion-1",
	}
	if _, err := repo.CreateIdempotent(ctx, a); err != nil {
		t.Fatal(err)
	}
	duplicate := *a
	duplicate.ID = ""
	duplicate.Title = "Duplicate title"
	existing, err := repo.CreateIdempotent(ctx, &duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if existing.ID != a.ID {
		t.Fatalf("idempotency returned %s, want %s", existing.ID, a.ID)
	}

	if _, err := repo.GetByIDForProject(ctx, project2.ID, a.ID); err == nil {
		t.Fatal("foreign project read unexpectedly succeeded")
	}
	if err := repo.MarkRead(ctx, project2.ID, a.ID); err == nil {
		t.Fatal("foreign project mark-read unexpectedly succeeded")
	}
	if err := repo.Delete(ctx, project2.ID, a.ID); err == nil {
		t.Fatal("foreign project delete unexpectedly succeeded")
	}
	if err := repo.SetDecision(ctx, project2.ID, a.ID, models.AlertDecisionApproved); err == nil {
		t.Fatal("foreign project approval unexpectedly succeeded")
	}
	if err := repo.SetDecision(ctx, project1.ID, a.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}

	const competitors = 8
	var wg sync.WaitGroup
	wg.Add(competitors)
	results := make(chan *models.Alert, competitors)
	for i := 0; i < competitors; i++ {
		go func(i int) {
			defer wg.Done()
			claimed, _ := repo.ClaimApproved(ctx, project1.ID, a.ID, "scanner-"+string(rune('a'+i)), time.Hour)
			if claimed != nil {
				results <- claimed
			}
		}(i)
	}
	wg.Wait()
	close(results)
	claims := 0
	for range results {
		claims++
	}
	if claims != 1 {
		t.Fatalf("competing claims = %d, want 1", claims)
	}
	if _, err := db.ExecContext(ctx, `UPDATE alerts SET claim_expires_at = datetime('now', '-1 minute') WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.ClaimApproved(ctx, project1.ID, a.ID, "recovery-scanner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Claimant != "recovery-scanner" {
		t.Fatalf("stale claim claimant = %q, want recovery-scanner", recovered.Claimant)
	}
	if err := repo.ReleaseClaim(ctx, project1.ID, a.ID, "recovery-scanner"); err != nil {
		t.Fatal(err)
	}
	retried, err := repo.ClaimApproved(ctx, project1.ID, a.ID, "retry-scanner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkProcessing(ctx, project1.ID, a.ID, "retry-scanner", models.AlertProcessingFailed, "temporary failure"); err != nil {
		t.Fatal(err)
	}
	failedRetry, err := repo.ClaimApproved(ctx, project1.ID, a.ID, "failure-retry-scanner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != failedRetry.ID || failedRetry.Claimant != "failure-retry-scanner" {
		t.Fatalf("failed claim was not retryable: %+v", failedRetry)
	}
}

func TestAlertRepo_ClaimCreatesImplementationTaskIdempotently(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createTestProject(t, projectRepo)
	ctx := context.Background()

	a := &models.Alert{
		ProjectID:       project.ID,
		Scope:           models.AlertScopeProject,
		Type:            models.AlertType("suggestion"),
		Severity:        models.SeverityInfo,
		Title:           "Implement me",
		DecisionState:   models.AlertDecisionPending,
		ProcessingState: models.AlertProcessingUnclaimed,
	}
	if _, err := repo.CreateIdempotent(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDecision(ctx, project.ID, a.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimApproved(ctx, project.ID, a.ID, "scheduled-task", time.Hour); err != nil {
		t.Fatal(err)
	}

	first, err := repo.CreateImplementationTask(ctx, project.ID, a.ID, "scheduled-task", models.AlertImplementationTaskInput{
		Title: "Implement alert suggestion", Prompt: "Implement the reviewed suggestion.", Priority: 2, Tag: models.TagFeature,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateImplementationTask(ctx, project.ID, a.ID, "scheduled-task", models.AlertImplementationTaskInput{
		Title: "A duplicate must not be created", Prompt: "duplicate", Priority: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent task IDs differ: %s != %s", first.ID, second.ID)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND id = ?`, project.ID, first.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("implementation task count = %d, want 1", count)
	}
}

func TestAlertRepo_DeleteAll(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)

	project1 := createTestProject(t, projectRepo)
	project2 := &models.Project{Name: "Project 2"}
	if err := projectRepo.Create(context.Background(), project2); err != nil {
		t.Fatalf("creating project2: %v", err)
	}

	// Create 3 alerts in project1
	for i := 0; i < 3; i++ {
		a := &models.Alert{
			ProjectID: project1.ID,
			Type:      models.AlertTaskFailed,
			Severity:  models.SeverityError,
			Title:     "Task failed",
		}
		if err := alertRepo.Create(context.Background(), a); err != nil {
			t.Fatalf("creating alert for project1: %v", err)
		}
	}

	// Create 2 alerts in project2
	for i := 0; i < 2; i++ {
		a := &models.Alert{
			ProjectID: project2.ID,
			Type:      models.AlertTaskFailed,
			Severity:  models.SeverityError,
			Title:     "Task failed",
		}
		if err := alertRepo.Create(context.Background(), a); err != nil {
			t.Fatalf("creating alert for project2: %v", err)
		}
	}

	// Delete all alerts for project1
	if err := alertRepo.DeleteAll(context.Background(), project1.ID); err != nil {
		t.Fatalf("deleting all alerts: %v", err)
	}

	// Verify project1 has no alerts
	alerts1, err := alertRepo.ListByProject(context.Background(), project1.ID, 50)
	if err != nil {
		t.Fatalf("listing project1 alerts: %v", err)
	}
	if len(alerts1) != 0 {
		t.Fatalf("expected 0 alerts for project1 after delete all, got %d", len(alerts1))
	}

	// Verify project2 still has its alerts
	alerts2, err := alertRepo.ListByProject(context.Background(), project2.ID, 50)
	if err != nil {
		t.Fatalf("listing project2 alerts: %v", err)
	}
	if len(alerts2) != 2 {
		t.Fatalf("expected 2 alerts for project2, got %d", len(alerts2))
	}
}
