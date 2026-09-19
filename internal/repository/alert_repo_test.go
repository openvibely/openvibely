package repository

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
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

func TestAlertRepo_CompletedProcessingRequiresImplementationTaskHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAlertRepo(db)
	project := createTestProject(t, NewProjectRepo(db))
	ctx := context.Background()
	alert := &models.Alert{
		ProjectID:       project.ID,
		Title:           "Completion requires implementation",
		DecisionState:   models.AlertDecisionPending,
		ProcessingState: models.AlertProcessingUnclaimed,
	}
	if _, err := repo.CreateIdempotent(ctx, alert); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	const claimant = "scheduled-inbox"
	if _, err := repo.ClaimApproved(ctx, project.ID, alert.ID, claimant, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkProcessing(ctx, project.ID, alert.ID, claimant, models.AlertProcessingCompleted, "done without implementation"); err == nil {
		t.Fatal("completed processing without an implementation task unexpectedly succeeded")
	}
	if err := repo.MarkProcessing(ctx, project.ID, alert.ID, claimant, models.AlertProcessingFailed, "implementation creation failed"); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkProcessing(ctx, project.ID, alert.ID, claimant, models.AlertProcessingCompleted, "done after pre-link failure"); err == nil {
		t.Fatal("failed processing without an implementation task was completed")
	}
	stored, err := repo.GetByIDForProject(ctx, project.ID, alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProcessingState != models.AlertProcessingFailed || stored.ImplementationTaskID != nil {
		t.Fatalf("stored alert = %#v, want failed with no implementation task", stored)
	}
}

func TestAlertRepo_ImplementationTaskLinkedFilterSurvivesTaskDeletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAlertRepo(db)
	project := createTestProject(t, NewProjectRepo(db))
	ctx := context.Background()

	alert := &models.Alert{
		ProjectID:       project.ID,
		Scope:           models.AlertScopeProject,
		Type:            models.AlertType("suggestion"),
		Severity:        models.SeverityInfo,
		Title:           "Historically linked implementation",
		DecisionState:   models.AlertDecisionPending,
		ProcessingState: models.AlertProcessingUnclaimed,
	}
	if _, err := repo.CreateIdempotent(ctx, alert); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	const claimant = "scheduled-inbox"
	if _, err := repo.ClaimApproved(ctx, project.ID, alert.ID, claimant, time.Hour); err != nil {
		t.Fatal(err)
	}
	implementationTask, err := repo.CreateImplementationTask(ctx, project.ID, alert.ID, claimant, models.AlertImplementationTaskInput{
		Title: "Implement historical alert", Prompt: "Implement the approved notification.", Priority: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkProcessing(ctx, project.ID, alert.ID, claimant, models.AlertProcessingFailed, "execution failed after linkage"); err != nil {
		t.Fatal(err)
	}
	if err := NewTaskRepo(db, nil).Delete(ctx, implementationTask.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByIDForProject(ctx, project.ID, alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImplementationTaskID != nil {
		t.Fatalf("task deletion retained live implementation task ID %v", *stored.ImplementationTaskID)
	}

	linked := true
	linkedAlerts, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{ImplementationTaskLinked: &linked, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(linkedAlerts) != 1 || linkedAlerts[0].ID != alert.ID {
		t.Fatalf("historically linked alerts = %#v, want %s", linkedAlerts, alert.ID)
	}
	linked = false
	unlinkedAlerts, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{ImplementationTaskLinked: &linked, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(unlinkedAlerts) != 0 {
		t.Fatalf("not-linked filter returned historically linked alerts: %#v", unlinkedAlerts)
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

	var selectedModelID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM agent_configs ORDER BY is_default DESC, name ASC LIMIT 1`).Scan(&selectedModelID); err != nil {
		t.Fatal(err)
	}
	first, err := repo.CreateImplementationTask(ctx, project.ID, a.ID, "scheduled-task", models.AlertImplementationTaskInput{
		Title: "Implement alert suggestion", Prompt: "Implement the reviewed suggestion.", Priority: 2, Tag: models.TagFeature, AgentID: selectedModelID,
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
	if first.AgentID == nil || *first.AgentID != selectedModelID {
		t.Fatalf("implementation task agent_id = %v, want %s", first.AgentID, selectedModelID)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND id = ?`, project.ID, first.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("implementation task count = %d, want 1", count)
	}
}

func TestAlertRepo_CompetingImplementationTaskCreationCreatesAtMostOneTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAlertRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createTestProject(t, projectRepo)
	ctx := context.Background()
	alert := &models.Alert{ProjectID: project.ID, Scope: models.AlertScopeProject, Type: "suggestion", Severity: models.SeverityInfo,
		Title: "Concurrent implementation", DecisionState: models.AlertDecisionPending, ProcessingState: models.AlertProcessingUnclaimed}
	if _, err := repo.CreateIdempotent(ctx, alert); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimApproved(ctx, project.ID, alert.ID, "scheduled-task", time.Hour); err != nil {
		t.Fatal(err)
	}

	const competitors = 8
	var wg sync.WaitGroup
	wg.Add(competitors)
	ids := make(chan string, competitors)
	errs := make(chan error, competitors)
	for i := 0; i < competitors; i++ {
		go func() {
			defer wg.Done()
			task, err := repo.CreateImplementationTask(ctx, project.ID, alert.ID, "scheduled-task", models.AlertImplementationTaskInput{
				Title: "Implement once", Prompt: "implement the approved work", Priority: 2,
			})
			if err != nil {
				errs <- err
				return
			}
			ids <- task.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("competing implementation task creation failed: %v", err)
	}
	var implementationID string
	for id := range ids {
		if implementationID == "" {
			implementationID = id
		}
		if id != implementationID {
			t.Fatalf("competing calls returned different task IDs: %s and %s", implementationID, id)
		}
	}
	if implementationID == "" {
		t.Fatal("no implementation task returned")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND title = ?`, project.ID, "Implement once").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("competing scans created %d implementation tasks, want 1", count)
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

type alertAutomationInvalidationFixture struct {
	automationRepo *AutomationRepo
	broadcaster    *events.Broadcaster
	automationID   string
	versionID      string
	producerNodeID string
}

func setupAlertAutomationInvalidationFixture(t *testing.T, db *sql.DB, projectID string) alertAutomationInvalidationFixture {
	t.Helper()
	ctx := context.Background()
	automationID := NewID()
	versionID := NewID()
	producerNodeID := NewID()
	notificationNodeID := NewID()
	approvalNodeID := NewID()
	inboxNodeID := NewID()
	implementationNodeID := NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, lifecycle_state, published_version_id)
		VALUES (?, ?, ?, 'Alert lifecycle automation', 'active', ?)`, automationID, projectID, automationID, versionID); err != nil {
		t.Fatalf("creating automation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, published_at)
		VALUES (?, ?, ?, 1, 'published', 'manual', 'native_sdlc', CURRENT_TIMESTAMP)`, versionID, projectID, automationID); err != nil {
		t.Fatalf("creating automation version: %v", err)
	}
	nodes := []struct {
		id, key, name, nodeType, role string
	}{
		{producerNodeID, "producer", "Producer", "agent_task", "finder"},
		{notificationNodeID, "notification", "Create notification", "action", "create_notification"},
		{approvalNodeID, "approval", "Approve notification", "human_gate", "native_approval"},
		{inboxNodeID, "inbox", "Native inbox", "action", "native_inbox"},
		{implementationNodeID, "implementation", "Implementation", "agent_task", "implementation"},
	}
	for _, node := range nodes {
		if _, err := db.ExecContext(ctx, `INSERT INTO automation_nodes
			(id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, node.id, projectID, automationID, versionID, node.key, node.name, node.nodeType, node.role); err != nil {
			t.Fatalf("creating automation node %s: %v", node.key, err)
		}
	}
	edges := []struct {
		key, from, to, condition string
	}{
		{"producer-notification", producerNodeID, notificationNodeID, `{}`},
		{"notification-approval", notificationNodeID, approvalNodeID, `{}`},
		{"approval-inbox", approvalNodeID, inboxNodeID, `{"state":"approved"}`},
		{"inbox-implementation", inboxNodeID, implementationNodeID, `{}`},
	}
	for i, edge := range edges {
		if _, err := db.ExecContext(ctx, `INSERT INTO automation_edges
			(project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, condition_json, display_order)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, projectID, automationID, versionID, edge.from, edge.to, edge.key, edge.condition, i); err != nil {
			t.Fatalf("creating automation edge %s: %v", edge.key, err)
		}
	}
	broadcaster := events.NewBroadcaster()
	automationRepo := NewAutomationRepo(db)
	automationRepo.SetBroadcaster(broadcaster)
	return alertAutomationInvalidationFixture{
		automationRepo: automationRepo,
		broadcaster:    broadcaster,
		automationID:   automationID,
		versionID:      versionID,
		producerNodeID: producerNodeID,
	}
}

func createAutomationBackedActionableAlert(t *testing.T, repo *AlertRepo, fixture alertAutomationInvalidationFixture, projectID, title string) *models.Alert {
	t.Helper()
	a := &models.Alert{
		ProjectID:       projectID,
		Scope:           models.AlertScopeProject,
		Type:            models.AlertType("suggestion"),
		Severity:        models.SeverityInfo,
		Title:           title,
		Message:         "Summary",
		Body:            "Full body",
		Source:          "test",
		DecisionState:   models.AlertDecisionPending,
		ProcessingState: models.AlertProcessingUnclaimed,
		Metadata:        map[string]any{},
		IdempotencyKey:  title,
		AutomationContext: &models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{{
			AutomationID: fixture.automationID,
			VersionID:    fixture.versionID,
			NodeID:       fixture.producerNodeID,
		}}},
	}
	created, err := repo.CreateIdempotent(context.Background(), a)
	if err != nil {
		t.Fatalf("creating automation-backed alert: %v", err)
	}
	return created
}

func expectAutomationInvalidation(t *testing.T, sub events.Subscriber, want events.TaskEventType, fixture alertAutomationInvalidationFixture, projectID string) events.TaskEvent {
	t.Helper()
	select {
	case event := <-sub:
		if event.Type != want {
			t.Fatalf("event type = %s, want %s", event.Type, want)
		}
		if event.ProjectID != projectID || event.AutomationID != fixture.automationID || event.VersionID != fixture.versionID {
			t.Fatalf("event binding = project %q automation %q version %q, want project %q automation %q version %q", event.ProjectID, event.AutomationID, event.VersionID, projectID, fixture.automationID, fixture.versionID)
		}
		if event.WorkItemID == "" || event.NodeID == "" {
			t.Fatalf("event missing work item/node binding: %+v", event)
		}
		return event
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s invalidation", want)
	}
	return events.TaskEvent{}
}

func assertNoAutomationInvalidation(t *testing.T, sub events.Subscriber) {
	t.Helper()
	select {
	case event := <-sub:
		t.Fatalf("unexpected invalidation after failed transaction: %+v", event)
	default:
	}
}

func TestAlertRepo_AutomationInvalidationsAfterCommittedLifecycleMutations(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := createTestProject(t, NewProjectRepo(db))
	fixture := setupAlertAutomationInvalidationFixture(t, db, project.ID)
	repo := NewAlertRepo(db)
	repo.SetAutomationRepo(fixture.automationRepo)
	sub, err := fixture.broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribing to automation events: %v", err)
	}
	defer fixture.broadcaster.Unsubscribe(sub)
	ctx := context.Background()

	alert := createAutomationBackedActionableAlert(t, repo, fixture, project.ID, "lifecycle-invalidation")
	expectAutomationInvalidation(t, sub, events.AutomationWorkItemUpdated, fixture, project.ID)
	if err := repo.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionPending); err == nil {
		t.Fatal("invalid pending decision unexpectedly succeeded")
	}
	assertNoAutomationInvalidation(t, sub)
	if err := repo.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatalf("approving alert: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationTransitionCreated, fixture, project.ID)
	if _, err := repo.ClaimApproved(ctx, project.ID, alert.ID, "scanner", time.Hour); err != nil {
		t.Fatalf("claiming alert: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationWorkItemUpdated, fixture, project.ID)
	if err := repo.ReleaseClaim(ctx, project.ID, alert.ID, "scanner"); err != nil {
		t.Fatalf("releasing claim: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationWorkItemUpdated, fixture, project.ID)
	if _, err := repo.ClaimApproved(ctx, project.ID, alert.ID, "scanner", time.Hour); err != nil {
		t.Fatalf("reclaiming alert: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationWorkItemUpdated, fixture, project.ID)

	task := &models.Task{ProjectID: project.ID, Title: "Explicit implementation", Category: models.CategoryBacklog, Priority: 2, Status: models.StatusPending, Prompt: "Implement", ChainConfig: "{}", SwarmConfig: "{}"}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatalf("creating explicit implementation task: %v", err)
	}
	if err := repo.LinkImplementationTask(ctx, project.ID, alert.ID, "scanner", task.ID); err != nil {
		t.Fatalf("linking implementation task: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationResourceLinked, fixture, project.ID)
	if err := repo.MarkProcessing(ctx, project.ID, alert.ID, "scanner", models.AlertProcessingCompleted, "done"); err != nil {
		t.Fatalf("marking processing complete: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationTransitionCreated, fixture, project.ID)
}

func TestAlertRepo_CreateImplementationTaskPublishesResourceLinkedForNewAndExistingLink(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := createTestProject(t, NewProjectRepo(db))
	fixture := setupAlertAutomationInvalidationFixture(t, db, project.ID)
	repo := NewAlertRepo(db)
	repo.SetAutomationRepo(fixture.automationRepo)
	sub, err := fixture.broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribing to automation events: %v", err)
	}
	defer fixture.broadcaster.Unsubscribe(sub)
	ctx := context.Background()

	alert := createAutomationBackedActionableAlert(t, repo, fixture, project.ID, "create-implementation-invalidation")
	expectAutomationInvalidation(t, sub, events.AutomationWorkItemUpdated, fixture, project.ID)
	if err := repo.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatalf("approving alert: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationTransitionCreated, fixture, project.ID)
	if _, err := repo.ClaimApproved(ctx, project.ID, alert.ID, "scanner", time.Hour); err != nil {
		t.Fatalf("claiming alert: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationWorkItemUpdated, fixture, project.ID)
	first, err := repo.CreateImplementationTask(ctx, project.ID, alert.ID, "scanner", models.AlertImplementationTaskInput{
		Title: "Generated implementation", Prompt: "Implement approved work", Priority: 2,
	})
	if err != nil {
		t.Fatalf("creating implementation task: %v", err)
	}
	expectAutomationInvalidation(t, sub, events.AutomationResourceLinked, fixture, project.ID)
	second, err := repo.CreateImplementationTask(ctx, project.ID, alert.ID, "scanner", models.AlertImplementationTaskInput{
		Title: "Duplicate implementation", Prompt: "Duplicate should not create", Priority: 2,
	})
	if err != nil {
		t.Fatalf("reusing implementation task: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent implementation task = %s, want %s", second.ID, first.ID)
	}
	expectAutomationInvalidation(t, sub, events.AutomationResourceLinked, fixture, project.ID)
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND title = ?`, project.ID, "Generated implementation").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("implementation tasks created = %d, want 1", count)
	}
}

func TestAlertRepoListFilteredSummariesOmitFullDetailColumnsAndGetHydrates(t *testing.T) {
	if strings.Contains(alertSummarySelectColumns, "body") {
		t.Fatalf("summary projection must not select body: %s", alertSummarySelectColumns)
	}
	if strings.Contains(alertSummarySelectColumns, "metadata_json") {
		t.Fatalf("summary projection must not select metadata_json: %s", alertSummarySelectColumns)
	}

	db := testutil.NewTestDB(t)
	repo := NewAlertRepo(db)
	project := createTestProject(t, NewProjectRepo(db))
	ctx := context.Background()
	task := &models.Task{ProjectID: project.ID, Title: "Summary source task", Prompt: "run", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatalf("creating source task: %v", err)
	}
	alert := &models.Alert{
		ProjectID:       project.ID,
		TaskID:          &task.ID,
		Type:            models.AlertType("runtime_summary"),
		Severity:        models.SeverityWarning,
		Title:           "Runtime summary projection",
		Message:         "Short triage message",
		Body:            strings.Repeat("full body ", 2048),
		Source:          "runtime-test",
		Metadata:        map[string]any{"component": "alerts", "payload": strings.Repeat("metadata ", 1024)},
		DecisionState:   models.AlertDecisionPending,
		ProcessingState: models.AlertProcessingUnclaimed,
	}
	if err := repo.Create(ctx, alert); err != nil {
		t.Fatalf("creating alert: %v", err)
	}

	summaries, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("listing summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summary count = %d, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.ID != alert.ID || summary.ProjectID != project.ID || summary.Type != alert.Type || summary.Severity != alert.Severity || summary.Title != alert.Title || summary.Message != alert.Message || summary.Source != alert.Source {
		t.Fatalf("summary did not preserve triage fields: %#v", summary)
	}
	if summary.TaskID == nil || *summary.TaskID != task.ID {
		t.Fatalf("summary task link = %#v, want %s", summary.TaskID, task.ID)
	}
	if summary.DecisionState != models.AlertDecisionPending || summary.ProcessingState != models.AlertProcessingUnclaimed {
		t.Fatalf("summary lifecycle states = %s/%s", summary.DecisionState, summary.ProcessingState)
	}
	if summary.CreatedAt.IsZero() || summary.UpdatedAt.IsZero() {
		t.Fatalf("summary timestamps must be populated: created=%v updated=%v", summary.CreatedAt, summary.UpdatedAt)
	}

	detail, err := repo.GetByIDForProject(ctx, project.ID, alert.ID)
	if err != nil {
		t.Fatalf("getting alert detail: %v", err)
	}
	if detail.Body != alert.Body {
		t.Fatalf("detail body was not hydrated")
	}
	if detail.Metadata["component"] != "alerts" {
		t.Fatalf("detail metadata was not hydrated: %#v", detail.Metadata)
	}
}

func insertAlertListOwnerAutomation(t *testing.T, db *sql.DB, projectID, automationID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state) VALUES (?, ?, ?, ?, 'active')`, automationID, projectID, automationID, automationID); err != nil {
		t.Fatalf("creating automation %s: %v", automationID, err)
	}
}

func insertAlertListOwnerAlert(t *testing.T, db *sql.DB, projectID, id, title, automationID string, createdAt string, decision models.AlertDecisionState, processing models.AlertProcessingState, source string, read bool, linked bool) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO alerts
		(id, project_id, scope, type, severity, title, message, body, source, metadata_json, decision_state, processing_state, is_read, implementation_task_was_linked, created_at, updated_at)
		VALUES (?, ?, 'project', 'suggestion', 'info', ?, 'summary', 'body', ?, '{}', ?, ?, ?, ?, ?, ?)`,
		id, projectID, title, source, decision, processing, read, linked, createdAt, createdAt); err != nil {
		t.Fatalf("creating alert %s: %v", id, err)
	}
	if automationID == "" {
		return
	}
	if _, err := db.Exec(`INSERT INTO automation_artifact_mailbox_owners
		(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
		VALUES (?, ?, 'alert', ?, 'producer', 'notify', 'approve', 'inbox')`, projectID, automationID, id); err != nil {
		t.Fatalf("creating owner for %s: %v", id, err)
	}
}

func TestAlertRepo_ListFilteredSummariesAutomationOwnedPathPreservesFiltersAndPagination(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := createTestProject(t, NewProjectRepo(db))
	const targetAutomation = "alert-list-target-automation"
	const otherAutomation = "alert-list-other-automation"
	insertAlertListOwnerAutomation(t, db, project.ID, targetAutomation)
	insertAlertListOwnerAutomation(t, db, project.ID, otherAutomation)

	insertAlertListOwnerAlert(t, db, project.ID, "target-old", "target old", targetAutomation, "2026-01-01 00:00:01", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "target-mid", "target mid", targetAutomation, "2026-01-01 00:00:02", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "target-new", "target new", targetAutomation, "2026-01-01 00:00:03", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "other-newer", "other hidden", otherAutomation, "2026-01-01 00:10:00", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "target-claimed", "claimed hidden", targetAutomation, "2026-01-01 00:09:00", models.AlertDecisionApproved, models.AlertProcessingClaimed, "finder", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "target-rejected", "rejected hidden", targetAutomation, "2026-01-01 00:08:00", models.AlertDecisionRejected, models.AlertProcessingUnclaimed, "finder", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "target-source", "source hidden", targetAutomation, "2026-01-01 00:07:00", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "other-source", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "target-read", "read hidden", targetAutomation, "2026-01-01 00:06:00", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", true, false)
	insertAlertListOwnerAlert(t, db, project.ID, "target-linked", "linked hidden", targetAutomation, "2026-01-01 00:05:00", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, true)
	insertAlertListOwnerAlert(t, db, project.ID, "unowned-newest", "unowned hidden", "", "2026-01-01 00:11:00", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)

	unlinked := false
	unread := false
	filter := models.AlertListFilter{
		DecisionState:            models.AlertDecisionApproved,
		ProcessingState:          models.AlertProcessingUnclaimed,
		Source:                   "finder",
		Read:                     &unread,
		ImplementationTaskLinked: &unlinked,
		AutomationInboxBindings:  []models.AutomationBinding{{AutomationID: targetAutomation}},
		Limit:                    2,
	}
	repo := NewAlertRepo(db)
	firstPage, err := repo.ListFilteredSummaries(context.Background(), project.ID, filter)
	if err != nil {
		t.Fatal(err)
	}
	if got := alertSummaryIDs(firstPage); strings.Join(got, ",") != "target-new,target-mid" {
		t.Fatalf("first page IDs = %v, want target-new,target-mid", got)
	}
	filter.Offset = 2
	secondPage, err := repo.ListFilteredSummaries(context.Background(), project.ID, filter)
	if err != nil {
		t.Fatal(err)
	}
	if got := alertSummaryIDs(secondPage); strings.Join(got, ",") != "target-old" {
		t.Fatalf("second page IDs = %v, want target-old", got)
	}
}

func TestAlertRepo_ListFilteredSummariesAutomationOwnedPathDeduplicatesBindingsAndOwners(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := createTestProject(t, NewProjectRepo(db))
	const automationID = "alert-list-duplicate-automation"
	const secondAutomationID = "alert-list-second-automation"
	insertAlertListOwnerAutomation(t, db, project.ID, automationID)
	insertAlertListOwnerAutomation(t, db, project.ID, secondAutomationID)
	insertAlertListOwnerAlert(t, db, project.ID, "dedup-alert", "dedupe", automationID, "2026-01-01 00:00:01", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)
	insertAlertListOwnerAlert(t, db, project.ID, "second-alert", "second automation", secondAutomationID, "2026-01-01 00:00:02", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)
	if _, err := db.Exec(`INSERT INTO automation_artifact_mailbox_owners
		(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
		VALUES (?, ?, 'alert', 'dedup-alert', 'producer-2', 'notify', 'approve', 'inbox')`, project.ID, automationID); err != nil {
		t.Fatalf("creating duplicate owner path: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO automation_artifact_mailbox_owners
		(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
		VALUES (?, ?, 'alert', 'dedup-alert', 'producer', 'notify', 'approve', 'inbox')`, project.ID, secondAutomationID); err != nil {
		t.Fatalf("creating second Automation owner path: %v", err)
	}

	repo := NewAlertRepo(db)
	results, err := repo.ListFilteredSummaries(context.Background(), project.ID, models.AlertListFilter{
		DecisionState:           models.AlertDecisionApproved,
		ProcessingState:         models.AlertProcessingUnclaimed,
		AutomationInboxBindings: []models.AutomationBinding{{AutomationID: automationID}, {AutomationID: automationID}, {AutomationID: secondAutomationID}},
		Limit:                   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := alertSummaryIDs(results); strings.Join(got, ",") != "second-alert,dedup-alert" {
		t.Fatalf("IDs = %v, want second-alert,dedup-alert without duplicates", got)
	}
}

func TestAlertRepo_ListFilteredSummariesAutomationOwnedPathEmptyOwnershipDoesNotFallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := createTestProject(t, NewProjectRepo(db))
	const targetAutomation = "alert-list-empty-automation"
	insertAlertListOwnerAutomation(t, db, project.ID, targetAutomation)
	insertAlertListOwnerAlert(t, db, project.ID, "unowned-match", "unowned matching alert", "", "2026-01-01 00:00:01", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)

	repo := NewAlertRepo(db)
	results, err := repo.ListFilteredSummaries(context.Background(), project.ID, models.AlertListFilter{
		DecisionState:           models.AlertDecisionApproved,
		ProcessingState:         models.AlertProcessingUnclaimed,
		AutomationInboxBindings: []models.AutomationBinding{{AutomationID: targetAutomation}},
		Limit:                   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("empty ownership returned %v, want no fallback results", alertSummaryIDs(results))
	}
}

func TestAlertRepo_AutomationOwnedSummaryQueryStartsFromMailboxOwners(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := createTestProject(t, NewProjectRepo(db))
	const targetAutomation = "alert-list-plan-automation"
	insertAlertListOwnerAutomation(t, db, project.ID, targetAutomation)
	insertAlertListOwnerAlert(t, db, project.ID, "owned-plan-alert", "owned", targetAutomation, "2026-01-01 00:00:01", models.AlertDecisionApproved, models.AlertProcessingUnclaimed, "finder", false, false)

	query, args := buildAlertSummaryListQuery(project.ID, normalizeAlertListFilter(models.AlertListFilter{
		DecisionState:           models.AlertDecisionApproved,
		ProcessingState:         models.AlertProcessingUnclaimed,
		AutomationInboxBindings: []models.AutomationBinding{{AutomationID: targetAutomation}},
		Limit:                   10,
	}))
	plan := alertBenchExplain(t, db, query, args...)
	ownerPos := strings.Index(plan, "automation_artifact_mailbox_owners")
	if ownerPos < 0 {
		ownerPos = strings.Index(plan, "sqlite_autoindex_automation_artifact_mailbox_owners")
	}
	alertPos := strings.Index(plan, "SEARCH alerts")
	if ownerPos < 0 || alertPos < 0 || ownerPos > alertPos {
		t.Fatalf("plan = %s, want owner lookup before alert join", plan)
	}
	if strings.Contains(plan, "CORRELATED") {
		t.Fatalf("plan = %s, want no correlated ownership subquery", plan)
	}

	unscopedQuery, _ := buildAlertSummaryListQuery(project.ID, normalizeAlertListFilter(models.AlertListFilter{Limit: 10}))
	if strings.Contains(unscopedQuery, "automation_artifact_mailbox_owners") {
		t.Fatalf("unscoped summary query unexpectedly uses mailbox owners: %s", unscopedQuery)
	}
}

func alertSummaryIDs(alerts []models.AlertSummary) []string {
	ids := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		ids = append(ids, alert.ID)
	}
	return ids
}

func TestAlertRepo_GetByIdempotencyKeyScopesToProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	alertRepo := NewAlertRepo(db)
	projectA := createTestProject(t, projectRepo)
	projectB := &models.Project{Name: "Other Alert Project"}
	if err := projectRepo.Create(ctx, projectB); err != nil {
		t.Fatal(err)
	}
	alert := &models.Alert{ProjectID: projectA.ID, Type: "task_needs_followup", Severity: "warning", Title: "Needs followup", Message: "message", IdempotencyKey: "dedupe-key"}
	if err := alertRepo.Create(ctx, alert); err != nil {
		t.Fatalf("Create: %v", err)
	}
	loaded, err := alertRepo.GetByIdempotencyKey(ctx, projectA.ID, "dedupe-key")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey: %v", err)
	}
	if loaded.ID != alert.ID || loaded.ProjectID != projectA.ID {
		t.Fatalf("unexpected loaded alert: %#v", loaded)
	}
	if _, err := alertRepo.GetByIdempotencyKey(ctx, projectB.ID, "dedupe-key"); err == nil || !strings.Contains(err.Error(), "alert not found") {
		t.Fatalf("expected project-scoped not found, got %v", err)
	}
}
