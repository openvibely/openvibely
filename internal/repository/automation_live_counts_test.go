package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

const (
	automationLiveCountsProjectID    = "automation-live-counts-project"
	automationLiveCountsAutomationID = "automation-live-counts-auto"
	automationLiveCountsVersionID    = "automation-live-counts-version"
)

type automationLiveCountsFixture struct {
	ProjectID    string
	AutomationID string
	VersionID    string
	Nodes        map[string]string
}

func TestAutomationRepoLiveNodeCountsUsesProjectedActivityStateSemantics(t *testing.T) {
	db := testutil.NewTestDB(t)
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{
		"activity":      "agent_task",
		"work":          "agent_task",
		"task":          "agent_task",
		"invocation":    "trigger",
		"input":         "agent_task",
		"position":      "agent_task",
		"github_inbox":  "github_inbox",
		"recent":        "agent_task",
		"priority":      "agent_task",
		"old_completed": "agent_task",
	})
	repo := NewAutomationRepo(db)
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-24 * time.Hour)

	insertAutomationLiveCountsInvocation(t, db, "activity-invocation", fixture, fixture.Nodes["activity"], "completed")
	_, _, err := repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding: models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID,
			NodeID: fixture.Nodes["activity"], InvocationID: "activity-invocation"},
		ActivityKey: "activity-only-running", ActivityType: "activity_only", ActivityStatus: models.AutomationActivityRunning,
	})
	if err != nil {
		t.Fatalf("record activity-only identity: %v", err)
	}

	workItem, _, err := repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["work"]},
		WorkItemKey: "work-identity", ActivityKey: "work-identity-running", ActivityType: "work", ActivityStatus: models.AutomationActivityRunning,
	})
	if err != nil {
		t.Fatalf("record initial work identity activity: %v", err)
	}
	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding: models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID,
			NodeID: fixture.Nodes["work"], WorkItemID: workItem.ID},
		ActivityKey: "work-identity-failed", ActivityType: "work", ActivityStatus: models.AutomationActivityFailed,
	})
	if err != nil {
		t.Fatalf("record latest work identity activity: %v", err)
	}

	insertAutomationLiveCountsTask(t, db, "task-identity-task", fixture.ProjectID)
	insertAutomationLiveCountsInvocation(t, db, "task-invocation-one", fixture, fixture.Nodes["task"], "completed")
	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding: models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID,
			NodeID: fixture.Nodes["task"], InvocationID: "task-invocation-one"},
		ActivityKey: "task-identity-running", ActivityType: "task", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: "task-identity-task", Relation: "subject"}},
	})
	if err != nil {
		t.Fatalf("record initial task identity activity: %v", err)
	}
	insertAutomationLiveCountsInvocation(t, db, "task-invocation-two", fixture, fixture.Nodes["task"], "completed")
	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding: models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID,
			NodeID: fixture.Nodes["task"], InvocationID: "task-invocation-two"},
		ActivityKey: "task-identity-completed", ActivityType: "task", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: "task-identity-task", Relation: "subject"}},
	})
	if err != nil {
		t.Fatalf("record latest task identity activity: %v", err)
	}

	insertAutomationLiveCountsInvocation(t, db, "fallback-invocation", fixture, fixture.Nodes["invocation"], "running")
	insertAutomationLiveCountsInvocation(t, db, "covered-invocation", fixture, fixture.Nodes["invocation"], "running")
	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding: models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID,
			NodeID: fixture.Nodes["invocation"], InvocationID: "covered-invocation"},
		ActivityKey: "covered-invocation-activity", ActivityType: "invocation", ActivityStatus: models.AutomationActivityRunning,
	})
	if err != nil {
		t.Fatalf("record covered invocation activity: %v", err)
	}

	inputWorkItem, _, err := repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["input"]},
		WorkItemKey: "input-work", ActivityKey: "input-work-completed", ActivityType: "input", ActivityStatus: models.AutomationActivityCompleted,
	})
	if err != nil {
		t.Fatalf("record input work item: %v", err)
	}
	insertAutomationLiveCountsPendingInput(t, db, "pending-input", fixture, fixture.Nodes["input"], inputWorkItem.ID)

	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["position"]},
		WorkItemKey: "position-work", EventKey: "position-work-entered", ToNodeID: fixture.Nodes["position"], Transition: models.AutomationTransitionEntered,
	})
	if err != nil {
		t.Fatalf("record active work-item position: %v", err)
	}
	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["github_inbox"]},
		WorkItemKey: "github-inbox-work", EventKey: "github-inbox-entered", ToNodeID: fixture.Nodes["github_inbox"], Transition: models.AutomationTransitionEntered,
	})
	if err != nil {
		t.Fatalf("record active github inbox position: %v", err)
	}

	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["recent"]},
		WorkItemKey: "recent-work", EventKey: "recent-work-completed", ToNodeID: fixture.Nodes["recent"], Transition: models.AutomationTransitionCompleted,
	})
	if err != nil {
		t.Fatalf("record recent completed transition: %v", err)
	}
	_, _, err = repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["old_completed"]},
		WorkItemKey: "old-completed-work", ActivityKey: "old-completed-activity", ActivityType: "old", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "old-completed-transition", ToNodeID: fixture.Nodes["old_completed"], Transition: models.AutomationTransitionCompleted,
	})
	if err != nil {
		t.Fatalf("record old completed transition: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_activities SET completed_at = datetime('now', '-25 hours') WHERE activity_key = 'old-completed-activity'`); err != nil {
		t.Fatalf("age completed activity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_live_activity_states SET completed_at = datetime('now', '-25 hours') WHERE activity_id IN (SELECT id FROM automation_activities WHERE activity_key = 'old-completed-activity')`); err != nil {
		t.Fatalf("age projected completed activity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_transitions SET occurred_at = datetime('now', '-25 hours') WHERE event_key = 'old-completed-transition'`); err != nil {
		t.Fatalf("age completed transition: %v", err)
	}

	priorityWorkItem, _, err := repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["priority"]},
		WorkItemKey: "priority-work", ActivityKey: "priority-recent", ActivityType: "priority", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "priority-completed", ToNodeID: fixture.Nodes["priority"], Transition: models.AutomationTransitionCompleted,
	})
	if err != nil {
		t.Fatalf("record priority completed activity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_work_item_positions
		(work_item_id, project_id, automation_id, version_id, node_id, state)
		VALUES (?, ?, ?, ?, ?, 'failed')`, priorityWorkItem.ID, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["priority"]); err != nil {
		t.Fatalf("insert priority failed position: %v", err)
	}

	counts, activeInvocations, activeWorkItems, err := repo.LiveNodeCounts(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, cutoff)
	if err != nil {
		t.Fatalf("LiveNodeCounts: %v", err)
	}
	if activeInvocations != 2 {
		t.Fatalf("active invocations = %d, want 2", activeInvocations)
	}
	if activeWorkItems < 1 {
		t.Fatalf("active work items = %d, want at least one", activeWorkItems)
	}
	assertAutomationNodeCounts(t, counts[fixture.Nodes["activity"]], models.AutomationNodeCounts{Running: 1})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["work"]], models.AutomationNodeCounts{Failed: 1})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["task"]], models.AutomationNodeCounts{CompletedRecently: 1})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["invocation"]], models.AutomationNodeCounts{Running: 2})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["input"]], models.AutomationNodeCounts{Running: 1})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["position"]], models.AutomationNodeCounts{Running: 1})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["github_inbox"]], models.AutomationNodeCounts{})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["recent"]], models.AutomationNodeCounts{CompletedRecently: 1})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["old_completed"]], models.AutomationNodeCounts{})
	assertAutomationNodeCounts(t, counts[fixture.Nodes["priority"]], models.AutomationNodeCounts{Failed: 1})
}

func TestAutomationRepoLiveNodeCountsQueryPlanAvoidsFullActivityHistoryRanking(t *testing.T) {
	db := testutil.NewTestDB(t)
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"node": "agent_task", "trigger": "trigger"})
	seedAutomationLiveCountsHistory(t, db, fixture, 100_000)
	plan := explainAutomationLiveNodeCountsPlan(t, db, fixture, time.Now().UTC().Add(-24*time.Hour))
	upperPlan := strings.ToUpper(plan)
	if strings.Contains(upperPlan, "RANKED_ACTIVITIES") || strings.Contains(upperPlan, "MATERIALIZE RANKED") || strings.Contains(upperPlan, "ROW_NUMBER") {
		t.Fatalf("live node count plan materializes ranked activities: %s", plan)
	}
	if strings.Contains(upperPlan, "USE TEMP B-TREE FOR ORDER BY") || strings.Contains(upperPlan, "USE TEMP B-TREE FOR LAST") {
		t.Fatalf("live node count plan sorts activity history: %s", plan)
	}
	if strings.Contains(upperPlan, "SCAN A") || strings.Contains(upperPlan, "SCAN AUTOMATION_ACTIVITIES") {
		t.Fatalf("live node count plan scans full automation_activities history: %s", plan)
	}
	if !strings.Contains(plan, "automation_live_activity_states") {
		t.Fatalf("live node count plan does not read projected live activity states: %s", plan)
	}
}

func BenchmarkAutomationLiveNodeCounts100kHistoricalActivities(b *testing.B) {
	db := testutil.NewTestDB(b)
	fixture := seedAutomationLiveCountsDefinition(b, db, map[string]string{"node": "agent_task", "trigger": "trigger"})
	seedAutomationLiveCountsHistory(b, db, fixture, 100_000)
	repo := NewAutomationRepo(db)
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	if counts, _, _, err := repo.LiveNodeCounts(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, cutoff); err != nil {
		b.Fatalf("warm LiveNodeCounts: %v", err)
	} else if counts[fixture.Nodes["node"]].Running != 3 || counts[fixture.Nodes["node"]].CompletedRecently != 1 || counts[fixture.Nodes["trigger"]].Running != 1 {
		b.Fatalf("warm counts = %#v", counts)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counts, _, _, err := repo.LiveNodeCounts(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, cutoff)
		if err != nil {
			b.Fatal(err)
		}
		if counts[fixture.Nodes["node"]].Running != 3 || counts[fixture.Nodes["node"]].CompletedRecently != 1 || counts[fixture.Nodes["trigger"]].Running != 1 {
			b.Fatalf("counts = %#v", counts)
		}
	}
}

func seedAutomationLiveCountsDefinition(tb testing.TB, db *sql.DB, nodeRoles map[string]string) automationLiveCountsFixture {
	tb.Helper()
	ctx := context.Background()
	fixture := automationLiveCountsFixture{ProjectID: automationLiveCountsProjectID, AutomationID: automationLiveCountsAutomationID, VersionID: automationLiveCountsVersionID, Nodes: make(map[string]string, len(nodeRoles))}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Automation live counts', '', '')`, fixture.ProjectID); err != nil {
		tb.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, automation_type, lifecycle_state, published_version_id)
		VALUES (?, ?, 'automation-live-counts', 'Automation live counts', 'custom', 'active', ?)`, fixture.AutomationID, fixture.ProjectID, fixture.VersionID); err != nil {
		tb.Fatalf("insert automation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, published_at)
		VALUES (?, ?, ?, 1, 'published', 'manual', 'custom', datetime('now'))`, fixture.VersionID, fixture.ProjectID, fixture.AutomationID); err != nil {
		tb.Fatalf("insert automation version: %v", err)
	}
	for key, role := range nodeRoles {
		nodeID := "automation-live-counts-node-" + key
		fixture.Nodes[key] = nodeID
		nodeType := "agent_task"
		if role == "trigger" || role == "github_inbox" {
			nodeType = "trigger"
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO automation_nodes
			(id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, key, "Automation live counts "+key, nodeType, role); err != nil {
			tb.Fatalf("insert automation node %s: %v", key, err)
		}
	}
	return fixture
}

func seedAutomationLiveCountsHistory(tb testing.TB, db *sql.DB, fixture automationLiveCountsFixture, historicalActivities int) {
	tb.Helper()
	ctx := context.Background()
	insertAutomationLiveCountsInvocation(tb, db, "automation-live-counts-history-invocation", fixture, fixture.Nodes["trigger"], "completed")
	if _, err := db.ExecContext(ctx, `WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
		INSERT INTO automation_activities
			(id, project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status, started_at, completed_at)
		SELECT 'automation-live-history-activity-' || printf('%06d', n), ?, ?, ?, ?, 'automation-live-counts-history-invocation',
			'automation-live-history-' || printf('%06d', n), 'history', 'completed', datetime('now', '-48 hours'), datetime('now', '-48 hours')
		FROM seq`, historicalActivities, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["node"]); err != nil {
		tb.Fatalf("insert historical automation activities: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_live_activity_states
		(project_id, automation_id, version_id, node_id, state_key, activity_id, invocation_id, activity_status, completed_at, activity_rowid)
		SELECT project_id, automation_id, version_id, node_id, 'activity:' || id, id, invocation_id, status, completed_at, rowid
		FROM automation_activities WHERE automation_id = ? AND version_id = ?`, fixture.AutomationID, fixture.VersionID); err != nil {
		tb.Fatalf("insert historical live activity states: %v", err)
	}
	for i := 0; i < 3; i++ {
		insertAutomationLiveCountsInvocation(tb, db, fmt.Sprintf("automation-live-counts-active-%d", i), fixture, fixture.Nodes["node"], "completed")
		if _, err := db.ExecContext(ctx, `INSERT INTO automation_activities
			(id, project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'active', 'running')`,
			fmt.Sprintf("automation-live-active-activity-%d", i), fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["node"],
			fmt.Sprintf("automation-live-counts-active-%d", i), fmt.Sprintf("automation-live-active-%d", i)); err != nil {
			tb.Fatalf("insert active activity %d: %v", i, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_live_activity_states
		(project_id, automation_id, version_id, node_id, state_key, activity_id, invocation_id, activity_status, completed_at, activity_rowid)
		SELECT project_id, automation_id, version_id, node_id, 'activity:' || id, id, invocation_id, status, completed_at, rowid
		FROM automation_activities WHERE activity_key LIKE 'automation-live-active-%'`); err != nil {
		tb.Fatalf("insert active live activity states: %v", err)
	}
	insertAutomationLiveCountsInvocation(tb, db, "automation-live-counts-open-invocation", fixture, fixture.Nodes["trigger"], "running")
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, work_item_key, kind, status)
		VALUES ('automation-live-counts-recent-work', ?, ?, ?, 'automation-live-counts-recent-work', 'work', 'completed')`, fixture.ProjectID, fixture.AutomationID, fixture.VersionID); err != nil {
		tb.Fatalf("insert recent work item: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_transitions
		(id, project_id, automation_id, version_id, work_item_id, to_node_id, event_key, state, occurred_at)
		VALUES ('automation-live-counts-recent-transition', ?, ?, ?, 'automation-live-counts-recent-work', ?, 'automation-live-counts-recent-transition', 'completed', datetime('now'))`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["node"]); err != nil {
		tb.Fatalf("insert recent transition: %v", err)
	}
}

func insertAutomationLiveCountsInvocation(tb testing.TB, db *sql.DB, id string, fixture automationLiveCountsFixture, triggerNodeID, status string) {
	tb.Helper()
	completed := "NULL"
	if status == "completed" || status == "failed" || status == "cancelled" || status == "skipped" {
		completed = "datetime('now')"
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, 'schedule', ?, ?, ?, datetime('now'), `+completed+`)`,
		id, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, triggerNodeID, "schedule-"+id, "occurrence-"+id, status); err != nil {
		tb.Fatalf("insert automation invocation %s: %v", id, err)
	}
}

func insertAutomationLiveCountsTask(tb testing.TB, db *sql.DB, id, projectID string) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
		VALUES (?, ?, ?, 'completed', 2, 'completed', 'prompt')`, id, projectID, id); err != nil {
		tb.Fatalf("insert task %s: %v", id, err)
	}
}

func insertAutomationLiveCountsPendingInput(tb testing.TB, db *sql.DB, id string, fixture automationLiveCountsFixture, nodeID, workItemID string) {
	tb.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO thread_inputs
		(id, scope, project_id, input_mode, input_status, content, queue_position)
		VALUES (?, 'task_thread', ?, 'queued', 'pending', 'continue', 1)`, id, fixture.ProjectID); err != nil {
		tb.Fatalf("insert thread input %s: %v", id, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_thread_input_bindings
		(thread_input_id, project_id, automation_id, version_id, node_id, work_item_id, binding_key)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, nodeID, workItemID, "binding-"+id); err != nil {
		tb.Fatalf("insert automation thread input binding %s: %v", id, err)
	}
}

func assertAutomationNodeCounts(tb testing.TB, got, want models.AutomationNodeCounts) {
	tb.Helper()
	if got != want {
		tb.Fatalf("node counts = %+v, want %+v", got, want)
	}
}

func explainAutomationLiveNodeCountsPlan(tb testing.TB, db *sql.DB, fixture automationLiveCountsFixture, cutoff time.Time) string {
	tb.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+liveNodeCountsSQL,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, cutoff.UTC(),
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, cutoff.UTC())
	if err != nil {
		tb.Fatalf("explain live node counts: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatalf("scan explain row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("explain rows: %v", err)
	}
	return strings.Join(details, " | ")
}
