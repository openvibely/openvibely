package repository

import (
	"context"
	"database/sql"
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
