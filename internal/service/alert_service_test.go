package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAlertService_CreateTaskFailedAlert(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := repository.NewAlertRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	alertSvc := NewAlertService(alertRepo, nil)

	// Create a project
	p := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("creating project: %v", err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: p.ID,
		Title:     "My Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Get default agent for execution
	agent, _ := llmConfigRepo.GetDefault(context.Background())
	if agent == nil {
		t.Fatal("expected default agent")
	}

	// Create an execution
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecFailed,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(context.Background(), exec); err != nil {
		t.Fatalf("creating execution: %v", err)
	}

	err := alertSvc.CreateTaskFailedAlert(context.Background(), p.ID, task.ID, exec.ID, task.Title, "LLM call failed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	alerts, err := alertSvc.ListByProject(context.Background(), p.ID, 50)
	if err != nil {
		t.Fatalf("listing alerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Title != "Task failed: My Task" {
		t.Fatalf("unexpected title: %q", alerts[0].Title)
	}
	if alerts[0].Message != "LLM call failed" {
		t.Fatalf("unexpected message: %q", alerts[0].Message)
	}
	if alerts[0].Severity != models.SeverityError {
		t.Fatalf("unexpected severity: %q", alerts[0].Severity)
	}
}

func TestAlertService_CountUnread(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := repository.NewAlertRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	alertSvc := NewAlertService(alertRepo, nil)

	p := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("creating project: %v", err)
	}

	// Create 2 alerts without task/execution references
	for i := 0; i < 2; i++ {
		a := &models.Alert{
			ProjectID: p.ID,
			Type:      models.AlertTaskFailed,
			Severity:  models.SeverityError,
			Title:     "Task failed",
			Message:   "error",
		}
		if err := alertSvc.Create(context.Background(), a); err != nil {
			t.Fatalf("creating alert: %v", err)
		}
	}

	count, err := alertSvc.CountUnread(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("counting unread: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 unread, got %d", count)
	}

	// Mark all read
	if err := alertSvc.MarkAllRead(context.Background(), p.ID); err != nil {
		t.Fatalf("marking all read: %v", err)
	}

	count, err = alertSvc.CountUnread(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("counting unread: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unread after mark all read, got %d", count)
	}
}

func TestAlertService_PublishesEventOnCreate(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := repository.NewAlertRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	broadcaster := events.NewBroadcaster()
	alertSvc := NewAlertService(alertRepo, broadcaster)

	// Subscribe to events
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	// Create a project
	p := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("creating project: %v", err)
	}

	// Create an alert
	a := &models.Alert{
		ProjectID: p.ID,
		Type:      models.AlertTaskFailed,
		Severity:  models.SeverityError,
		Title:     "Task failed",
		Message:   "error",
	}
	if err := alertSvc.Create(context.Background(), a); err != nil {
		t.Fatalf("creating alert: %v", err)
	}

	// Wait for event
	select {
	case event := <-sub:
		if event.Type != events.AlertCreated {
			t.Fatalf("expected AlertCreated event, got %v", event.Type)
		}
		if event.ProjectID != p.ID {
			t.Fatalf("expected project_id=%s, got %s", p.ID, event.ProjectID)
		}
		if event.AlertID != a.ID {
			t.Fatalf("expected alert_id=%s, got %s", a.ID, event.AlertID)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for alert_created event")
	}
}

func TestAlertService_DeleteAll(t *testing.T) {
	db := testutil.NewTestDB(t)
	alertRepo := repository.NewAlertRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	alertSvc := NewAlertService(alertRepo, nil)

	p := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("creating project: %v", err)
	}

	// Create 5 alerts
	for i := 0; i < 5; i++ {
		a := &models.Alert{
			ProjectID: p.ID,
			Type:      models.AlertTaskFailed,
			Severity:  models.SeverityError,
			Title:     "Task failed",
			Message:   "error",
		}
		if err := alertSvc.Create(context.Background(), a); err != nil {
			t.Fatalf("creating alert: %v", err)
		}
	}

	// Verify we have 5 alerts
	alerts, err := alertSvc.ListByProject(context.Background(), p.ID, 50)
	if err != nil {
		t.Fatalf("listing alerts: %v", err)
	}
	if len(alerts) != 5 {
		t.Fatalf("expected 5 alerts, got %d", len(alerts))
	}

	// Delete all
	if err := alertSvc.DeleteAll(context.Background(), p.ID); err != nil {
		t.Fatalf("deleting all alerts: %v", err)
	}

	// Verify all alerts are gone
	alerts, err = alertSvc.ListByProject(context.Background(), p.ID, 50)
	if err != nil {
		t.Fatalf("listing alerts after delete all: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected 0 alerts after delete all, got %d", len(alerts))
	}

	// Verify count is zero
	count, err := alertSvc.CountUnread(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("counting unread: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unread after delete all, got %d", count)
	}
}
