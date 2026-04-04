package repository

import (
	"context"
	"testing"

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

	if err := alertRepo.MarkRead(context.Background(), a.ID); err != nil {
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

	if err := alertRepo.Delete(context.Background(), a.ID); err != nil {
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
