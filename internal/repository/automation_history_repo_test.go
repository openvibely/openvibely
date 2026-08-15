package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAutomationHistoryPagesReplayMetricsAndHealth(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{
		"trigger": "trigger",
		"task":    "task",
		"review":  "human_gate",
		"done":    "completed",
	})
	repo := NewAutomationRepo(db)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_edges
		(id, project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, display_order)
		VALUES
		('edge-trigger-task', ?, ?, ?, ?, ?, 'trigger-task', 0),
		('edge-task-review', ?, ?, ?, ?, ?, 'task-review', 1),
		('edge-review-done', ?, ?, ?, ?, ?, 'review-done', 2),
		('edge-trigger-review', ?, ?, ?, ?, ?, 'trigger-review', 3)`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"], fixture.Nodes["task"],
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"], fixture.Nodes["review"],
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["review"], fixture.Nodes["done"],
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"], fixture.Nodes["review"]); err != nil {
		t.Fatalf("insert edges: %v", err)
	}

	insertAutomationHistoryInvocation(t, db, fixture, "inv-1", fixture.Nodes["trigger"], "completed", now.Add(-3*time.Hour), now.Add(-2*time.Hour), false)
	insertAutomationHistoryInvocation(t, db, fixture, "inv-2", fixture.Nodes["trigger"], "failed", now.Add(-2*time.Hour), now.Add(-90*time.Minute), false)
	insertAutomationHistoryInvocation(t, db, fixture, "inv-3", fixture.Nodes["trigger"], "completed", now.Add(-time.Hour), now.Add(-30*time.Minute), false)
	insertAutomationHistoryWorkItem(t, db, fixture, "work-1", "feature", "Feature", "completed", now.Add(-2*time.Hour), now.Add(-time.Hour))
	insertAutomationHistoryWorkItem(t, db, fixture, "work-2", "bug", "Bug", "blocked", now.Add(-time.Hour), time.Time{})
	insertAutomationHistoryActivity(t, db, fixture, "activity-1", "inv-1", "work-1", fixture.Nodes["task"], "task_execution", "completed", now.Add(-110*time.Minute), now.Add(-100*time.Minute), "")
	insertAutomationHistoryActivity(t, db, fixture, "activity-2", "inv-2", "work-2", fixture.Nodes["review"], "human_gate", "failed", now.Add(-80*time.Minute), now.Add(-70*time.Minute), "needs attention")
	insertAutomationHistoryTransition(t, db, fixture, "trans-1", "work-1", "inv-1", "activity-1", "", fixture.Nodes["task"], "edge-trigger-task", "entered", now.Add(-109*time.Minute))
	insertAutomationHistoryTransition(t, db, fixture, "trans-2", "work-1", "inv-1", "activity-1", fixture.Nodes["task"], fixture.Nodes["review"], "edge-task-review", "waiting", now.Add(-100*time.Minute))
	insertAutomationHistoryTransition(t, db, fixture, "trans-3", "work-1", "inv-1", "activity-1", fixture.Nodes["review"], fixture.Nodes["done"], "edge-review-done", "completed", now.Add(-90*time.Minute))
	insertAutomationHistoryTransition(t, db, fixture, "trans-4", "work-2", "inv-2", "activity-2", "", fixture.Nodes["review"], "edge-trigger-review", "blocked", now.Add(-70*time.Minute))
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_work_item_positions
		(project_id, automation_id, version_id, work_item_id, node_id, state)
		VALUES (?, ?, ?, 'work-2', ?, 'blocked')`, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["review"]); err != nil {
		t.Fatalf("insert position: %v", err)
	}

	invocations, err := repo.ListAutomationInvocations(ctx, fixture.ProjectID, fixture.AutomationID, 2, "")
	if err != nil {
		t.Fatalf("ListAutomationInvocations: %v", err)
	}
	if len(invocations.Items) != 2 || invocations.NextCursor == "" || invocations.Items[0].ID != "inv-3" {
		t.Fatalf("unexpected invocation page: %#v", invocations)
	}
	if _, err := repo.ListAutomationInvocations(ctx, fixture.ProjectID, fixture.AutomationID, 2, "not-base64"); !errors.Is(err, ErrAutomationCursor) {
		t.Fatalf("expected invalid cursor error, got %v", err)
	}
	invocation, err := repo.GetAutomationInvocation(ctx, fixture.ProjectID, fixture.AutomationID, "inv-2")
	if err != nil || invocation == nil || invocation.Status != models.AutomationInvocationFailed {
		t.Fatalf("GetAutomationInvocation = %#v, %v", invocation, err)
	}
	nodeIDs, err := repo.ListAutomationInvocationNodeIDs(ctx, fixture.ProjectID, fixture.AutomationID, "inv-1", 10)
	if err != nil || strings.Join(nodeIDs, ",") != fixture.Nodes["done"]+","+fixture.Nodes["review"]+","+fixture.Nodes["task"] {
		t.Fatalf("ListAutomationInvocationNodeIDs = %v, %v", nodeIDs, err)
	}

	workItems, err := repo.ListAutomationWorkItems(ctx, fixture.ProjectID, fixture.AutomationID, "blocked", 1, "")
	if err != nil || len(workItems.Items) != 1 || workItems.Items[0].ID != "work-2" {
		t.Fatalf("ListAutomationWorkItems = %#v, %v", workItems, err)
	}
	if _, err := repo.ListAutomationWorkItems(ctx, fixture.ProjectID, fixture.AutomationID, "bogus", 1, ""); !errors.Is(err, ErrAutomationWorkItemStatus) {
		t.Fatalf("expected invalid work item status error, got %v", err)
	}
	workItem, err := repo.GetAutomationWorkItem(ctx, fixture.ProjectID, fixture.AutomationID, "work-1")
	if err != nil || workItem == nil || workItem.Title != "Feature" {
		t.Fatalf("GetAutomationWorkItem = %#v, %v", workItem, err)
	}

	activities, err := repo.ListAutomationActivities(ctx, fixture.ProjectID, fixture.AutomationID, "inv-1", "work-1", 1, "")
	if err != nil || len(activities.Items) != 1 || activities.Items[0].ID != "activity-1" {
		t.Fatalf("ListAutomationActivities = %#v, %v", activities, err)
	}
	transitions, err := repo.ListAutomationTransitions(ctx, fixture.ProjectID, fixture.AutomationID, "inv-1", "work-1", 10, "")
	if err != nil || len(transitions.Items) != 3 {
		t.Fatalf("ListAutomationTransitions = %#v, %v", transitions, err)
	}
	replay := ReplayAutomationTransitions(transitions.Items)
	if len(replay) != 3 || len(replay[1].Positions) != 1 || replay[1].Positions[0].State != models.AutomationPositionWaiting {
		t.Fatalf("ReplayAutomationTransitions = %#v", replay)
	}
	cursor := encodeAutomationCursor(automationCursorKind("transitions", fixture.AutomationID, "", "work-1"), transitions.Items[1].OccurredAt, transitions.Items[1].ID)
	continuedReplay, err := repo.ReplayAutomationTransitionPage(ctx, fixture.ProjectID, fixture.AutomationID, "work-1", cursor, transitions.Items[2:])
	if err != nil || len(continuedReplay) != 1 || len(continuedReplay[0].Positions) != 0 {
		t.Fatalf("ReplayAutomationTransitionPage = %#v, %v", continuedReplay, err)
	}

	definition, err := repo.GetDefinitionVersion(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID)
	if err != nil || definition == nil || len(definition.Nodes) != 4 {
		t.Fatalf("GetDefinitionVersion = %#v, %v", definition, err)
	}
	metrics, err := repo.GetAutomationMetrics(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, now)
	if err != nil {
		t.Fatalf("GetAutomationMetrics: %v", err)
	}
	if len(metrics.Funnel) != 4 || len(metrics.Durations) == 0 || len(metrics.Failures) < 1 || len(metrics.Bottlenecks) != 1 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
	health, err := repo.RecomputeAutomationHealth(ctx, fixture.ProjectID, fixture.AutomationID, now)
	if err != nil || health.State != models.AutomationHealthDegraded || !strings.Contains(health.Reason, "blocked") {
		t.Fatalf("RecomputeAutomationHealth = %#v, %v", health, err)
	}
	if err := repo.RecomputeAutomationHealthForAll(ctx, now, 1); err != nil {
		t.Fatalf("RecomputeAutomationHealthForAll: %v", err)
	}
}

func TestAutomationHistoryCursorHelpersAndHealthNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAutomationRepo(db)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	if automationPageLimit(-1) != 50 || automationPageLimit(500) != 50 || automationPageLimit(7) != 7 {
		t.Fatal("automationPageLimit did not clamp as expected")
	}
	kind := automationCursorKind("things", "auto", "status")
	cursor := encodeAutomationCursor(kind, now, "row-1")
	decoded, err := decodeAutomationCursor(kind, cursor)
	if err != nil || decoded == nil || decoded.ID != "row-1" || !decoded.Time.Equal(now) {
		t.Fatalf("decodeAutomationCursor = %#v, %v", decoded, err)
	}
	if _, err := decodeAutomationCursor("other", cursor); !errors.Is(err, ErrAutomationCursor) {
		t.Fatalf("expected cursor kind error, got %v", err)
	}
	if _, err := repo.RecomputeAutomationHealth(context.Background(), "missing", "missing", now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing automation error, got %v", err)
	}
}

func insertAutomationHistoryInvocation(t *testing.T, db SQLExecutor, fixture automationLiveCountsFixture, id, nodeID, status string, started, completed time.Time, scheduled bool) {
	t.Helper()
	scheduledFor := any(nil)
	if scheduled {
		scheduledFor = sqliteTestTime(started.Add(-time.Hour))
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
		 occurrence_key, scheduled_for, status, started_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, ?, 'schedule', ?, ?, ?, ?, ?, ?, ?)`,
		id, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, nodeID, "schedule-"+id, "occurrence-"+id,
		scheduledFor, status, sqliteTestTime(started), sqliteTestTime(completed), "error-"+id); err != nil {
		t.Fatalf("insert invocation %s: %v", id, err)
	}
}

func insertAutomationHistoryWorkItem(t *testing.T, db SQLExecutor, fixture automationLiveCountsFixture, id, kind, title, status string, created, completed time.Time) {
	t.Helper()
	completedAt := any(nil)
	if !completed.IsZero() {
		completedAt = sqliteTestTime(completed)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, origin_invocation_id, work_item_key, kind, title, status, created_at, completed_at)
		VALUES (?, ?, ?, ?, 'inv-1', ?, ?, ?, ?, ?, ?)`,
		id, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, "key-"+id, kind, title, status, sqliteTestTime(created), completedAt); err != nil {
		t.Fatalf("insert work item %s: %v", id, err)
	}
}

func insertAutomationHistoryActivity(t *testing.T, db SQLExecutor, fixture automationLiveCountsFixture, id, invocationID, workItemID, nodeID, activityType, status string, started, completed time.Time, message string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, invocation_id, work_item_id,
		 activity_key, activity_type, status, started_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, nodeID, invocationID, workItemID,
		"activity-key-"+id, activityType, status, sqliteTestTime(started), sqliteTestTime(completed), message); err != nil {
		t.Fatalf("insert activity %s: %v", id, err)
	}
}

func insertAutomationHistoryTransition(t *testing.T, db SQLExecutor, fixture automationLiveCountsFixture, id, workItemID, invocationID, activityID, fromNodeID, toNodeID, edgeID, state string, occurred time.Time) {
	t.Helper()
	from := any(nil)
	if fromNodeID != "" {
		from = fromNodeID
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO automation_transitions
		(id, project_id, automation_id, version_id, work_item_id, invocation_id, activity_id,
		 from_node_id, to_node_id, edge_id, event_key, state, metadata_json, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?)`,
		id, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, workItemID, invocationID, activityID,
		from, toNodeID, edgeID, "event-"+id, state, sqliteTestTime(occurred)); err != nil {
		t.Fatalf("insert transition %s: %v", id, err)
	}
}

func sqliteTestTime(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05")
}
