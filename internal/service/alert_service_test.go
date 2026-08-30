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

func TestAlertService_GetByIDPreservesMarkdownDetailAndProjectScope(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	alertRepo := repository.NewAlertRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	alertSvc := NewAlertService(alertRepo, nil)

	project := &models.Project{Name: "Markdown detail project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	foreign := &models.Project{Name: "Foreign detail project"}
	if err := projectRepo.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}

	rawBody := "# Heading\r\n\r\n**emphasis**\r\n\r\n```text\r\nline 1\r\nline 2\r\n```"
	alert := &models.Alert{
		ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityInfo,
		Title: "Markdown notification", Body: rawBody, Source: "test",
		Metadata:      map[string]any{"attempt": float64(2)},
		DecisionState: models.AlertDecisionPending, ProcessingState: models.AlertProcessingUnclaimed,
	}
	if err := alertSvc.Create(ctx, alert); err != nil {
		t.Fatal(err)
	}

	detail, err := alertSvc.GetByID(ctx, project.ID, alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || detail.Body != rawBody {
		t.Fatalf("detail body changed: got %#v, want %q", detail, rawBody)
	}
	if detail.Metadata["attempt"] != float64(2) {
		t.Fatalf("detail metadata changed: %#v", detail.Metadata)
	}

	summaries, err := alertSvc.ListSummariesByProject(ctx, project.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != alert.ID {
		t.Fatalf("unexpected compact summaries: %#v", summaries)
	}

	foreignDetail, err := alertSvc.GetByID(ctx, foreign.ID, alert.ID)
	if err == nil || foreignDetail != nil {
		t.Fatalf("foreign project detail unexpectedly visible: detail=%#v err=%v", foreignDetail, err)
	}
}

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

func TestAlertService_PublishesProjectScopedEventsForLifecycleMutations(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Lifecycle Events"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	caller := &models.Task{ProjectID: project.ID, Title: "Scanner", Prompt: "scan", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	if err := taskRepo.Create(ctx, caller); err != nil {
		t.Fatal(err)
	}
	broadcaster := events.NewBroadcaster()
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer broadcaster.Unsubscribe(sub)
	svc := NewAlertService(repository.NewAlertRepo(db), broadcaster)
	alert, err := svc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, Type: "suggestion", Title: "Lifecycle", Severity: models.SeverityInfo})
	if err != nil {
		t.Fatal(err)
	}

	expectEvent := func() {
		t.Helper()
		select {
		case event := <-sub:
			if event.Type != events.AlertCreated || event.ProjectID != project.ID || event.AlertID != alert.ID {
				t.Fatalf("unexpected alert invalidation event: %+v", event)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for lifecycle invalidation")
		}
	}
	expectEvent() // creation

	if err := svc.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if _, err := svc.ClaimApproved(ctx, project.ID, alert.ID, caller.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if err := svc.ReleaseClaim(ctx, project.ID, alert.ID, caller.ID); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if _, err := svc.ClaimApproved(ctx, project.ID, alert.ID, caller.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	implementation := &models.Task{ProjectID: project.ID, Title: "Implementation", Prompt: "implement", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	if err := taskRepo.Create(ctx, implementation); err != nil {
		t.Fatal(err)
	}
	if err := svc.LinkImplementationTask(ctx, project.ID, alert.ID, caller.ID, implementation.ID); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if err := svc.MarkProcessing(ctx, project.ID, alert.ID, caller.ID, models.AlertProcessingCompleted, "done"); err != nil {
		t.Fatal(err)
	}
	expectEvent()

	failed, err := svc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, Type: "suggestion", Title: "Failure", Severity: models.SeverityInfo})
	if err != nil {
		t.Fatal(err)
	}
	alert = failed
	expectEvent()
	if err := svc.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if _, err := svc.ClaimApproved(ctx, project.ID, alert.ID, caller.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if err := svc.MarkProcessing(ctx, project.ID, alert.ID, caller.ID, models.AlertProcessingFailed, "retry"); err != nil {
		t.Fatal(err)
	}
	expectEvent()

	createdTaskAlert, err := svc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, Type: "suggestion", Title: "Atomic task", Severity: models.SeverityInfo})
	if err != nil {
		t.Fatal(err)
	}
	alert = createdTaskAlert
	expectEvent()
	if err := svc.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if _, err := svc.ClaimApproved(ctx, project.ID, alert.ID, caller.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	expectEvent()
	if _, err := svc.CreateImplementationTask(ctx, project.ID, alert.ID, caller.ID, models.AlertImplementationTaskInput{Title: "Atomic implementation", Prompt: "implement", Priority: 2}); err != nil {
		t.Fatal(err)
	}
	expectEvent()
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
