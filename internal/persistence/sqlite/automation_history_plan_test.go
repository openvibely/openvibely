package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func newAutomationWorkItemHistoryBenchFixture(tb testing.TB, rowCount int) (*sql.DB, persistence.AutomationStore, string, string) {
	tb.Helper()
	db := testutil.NewTestDB(tb)
	projectID, automationID, versionID := createAutomationHistoryBenchDefinition(tb, db, "Automation work item history", "automation-work-items-history", "version-work-items-history")
	seedAutomationWorkItemHistoryRows(tb, db, projectID, automationID, versionID, rowCount)
	return db, sqlite.NewStores(db, nil).Automations, projectID, automationID
}

func createAutomationHistoryBenchDefinition(tb testing.TB, db *sql.DB, name, automationID, versionID string) (string, string, string) {
	tb.Helper()
	ctx := context.Background()
	projectRepo := sqlite.NewStores(db, nil).Projects
	project := models.Project{Name: name}
	require.NoError(tb, projectRepo.Create(ctx, &project))
	_, err := db.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, automation_type, lifecycle_state, created_via)
		VALUES (?, ?, ?, ?, 'custom', 'active', 'web')`, automationID, project.ID, automationID, name)
	require.NoError(tb, err)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, schema_version, published_at)
		VALUES (?, ?, ?, 1, 'published', 'manual', 'custom', 1, CURRENT_TIMESTAMP)`, versionID, project.ID, automationID)
	require.NoError(tb, err)
	_, err = db.ExecContext(ctx, `UPDATE automations SET published_version_id = ? WHERE id = ?`, versionID, automationID)
	require.NoError(tb, err)
	return project.ID, automationID, versionID
}

func seedAutomationWorkItemHistoryRows(tb testing.TB, db *sql.DB, projectID, automationID, versionID string, rowCount int) {
	tb.Helper()
	if rowCount == 0 {
		return
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(tb, err)
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, work_item_key, kind, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'test', ?, ?, ?, ?)`)
	require.NoError(tb, err)
	defer stmt.Close()
	statuses := []string{"active", "waiting", "completed", "failed"}
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < rowCount; i++ {
		id := fmt.Sprintf("history-work-item-%06d", i)
		createdAt := base.Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05")
		_, err = stmt.ExecContext(ctx, id, projectID, automationID, versionID, "history:key:"+id, "History "+id, statuses[i%len(statuses)], createdAt, createdAt)
		require.NoError(tb, err)
	}
	require.NoError(tb, tx.Commit())
}

func newAutomationTransitionHistoryBenchFixture(tb testing.TB, invocationCount, transitionsPerInvocation int) (*sql.DB, persistence.AutomationStore, string, string, string, string) {
	tb.Helper()
	db := testutil.NewTestDB(tb)
	projectID, automationID, versionID := createAutomationHistoryBenchDefinition(tb, db, "Automation transition history", "automation-transition-history", "version-transition-history")
	targetInvocationID, targetWorkItemID := seedAutomationTransitionHistoryRows(tb, db, projectID, automationID, versionID, invocationCount, transitionsPerInvocation)
	return db, sqlite.NewStores(db, nil).Automations, projectID, automationID, targetInvocationID, targetWorkItemID
}

func seedAutomationTransitionHistoryRows(tb testing.TB, db *sql.DB, projectID, automationID, versionID string, invocationCount, transitionsPerInvocation int) (string, string) {
	tb.Helper()
	require.Positive(tb, invocationCount)
	require.Positive(tb, transitionsPerInvocation)
	ctx := context.Background()
	nodeID := "transition-history-node"
	_, err := db.ExecContext(ctx, `INSERT INTO automation_nodes
		(id, project_id, automation_id, version_id, node_key, name, node_type, role)
		VALUES (?, ?, ?, ?, 'transition-history-node', 'Transition history node', 'agent_task', 'task')`, nodeID, projectID, automationID, versionID)
	require.NoError(tb, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(tb, err)
	invocationStmt, err := tx.PrepareContext(ctx, `INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at)
		VALUES (?, ?, ?, ?, ?, 'schedule', ?, ?, 'completed', ?)`)
	require.NoError(tb, err)
	defer invocationStmt.Close()
	workItemStmt, err := tx.PrepareContext(ctx, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, origin_invocation_id, work_item_key, kind, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'test', ?, 'completed', ?, ?)`)
	require.NoError(tb, err)
	defer workItemStmt.Close()
	transitionStmt, err := tx.PrepareContext(ctx, `INSERT INTO automation_transitions
		(id, project_id, automation_id, version_id, work_item_id, invocation_id, to_node_id, event_key, state, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'entered', ?)`)
	require.NoError(tb, err)
	defer transitionStmt.Close()
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for invocationIndex := 0; invocationIndex < invocationCount; invocationIndex++ {
		invocationID := fmt.Sprintf("history-invocation-%06d", invocationIndex)
		workItemID := fmt.Sprintf("history-transition-work-item-%06d", invocationIndex)
		startedAt := base.Add(time.Duration(invocationIndex) * time.Minute).Format("2006-01-02 15:04:05")
		_, err = invocationStmt.ExecContext(ctx, invocationID, projectID, automationID, versionID, nodeID, "schedule-history", "transition-history:"+invocationID, startedAt)
		require.NoError(tb, err)
		_, err = workItemStmt.ExecContext(ctx, workItemID, projectID, automationID, versionID, invocationID, "transition-history:"+workItemID, "History "+workItemID, startedAt, startedAt)
		require.NoError(tb, err)
		for transitionIndex := 0; transitionIndex < transitionsPerInvocation; transitionIndex++ {
			transitionID := fmt.Sprintf("history-transition-%06d-%03d", invocationIndex, transitionIndex)
			occurredAt := base.Add(time.Duration(transitionIndex) * time.Second).Format("2006-01-02 15:04:05")
			_, err = transitionStmt.ExecContext(ctx, transitionID, projectID, automationID, versionID, workItemID, invocationID, nodeID, "transition-history:"+transitionID, occurredAt)
			require.NoError(tb, err)
		}
	}
	require.NoError(tb, tx.Commit())
	targetInvocationIndex := invocationCount / 2
	return fmt.Sprintf("history-invocation-%06d", targetInvocationIndex), fmt.Sprintf("history-transition-work-item-%06d", targetInvocationIndex)
}

func explainAutomationWorkItemsHistoryPlan(tb testing.TB, db *sql.DB, projectID, automationID, status string, withCursor bool) string {
	tb.Helper()
	query := `SELECT id, project_id, automation_id, origin_version_id, COALESCE(origin_invocation_id, ''),
		COALESCE(parent_work_item_id, ''), work_item_key, kind, title, status, created_at, updated_at, completed_at
		FROM automation_work_items WHERE project_id = ? AND automation_id = ?`
	args := []any{projectID, automationID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if withCursor {
		query += ` AND (datetime(created_at) < datetime(?) OR (datetime(created_at) = datetime(?) AND id < ?))`
		args = append(args, "2026-01-01 00:30:00", "2026-01-01 00:30:00", "history-work-item-001800")
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, 21)
	return explainAutomationHistoryPlan(tb, db, query, args...)
}

func explainAutomationTransitionsHistoryPlan(tb testing.TB, db *sql.DB, projectID, automationID, invocationID, workItemID string, withCursor bool) string {
	tb.Helper()
	query := `SELECT id, project_id, automation_id, version_id, work_item_id, COALESCE(invocation_id, ''),
		COALESCE(activity_id, ''), COALESCE(from_node_id, ''), to_node_id, COALESCE(edge_id, ''),
		event_key, state, metadata_json, occurred_at FROM automation_transitions
		WHERE project_id = ? AND automation_id = ?`
	args := []any{projectID, automationID}
	if invocationID != "" {
		query += ` AND invocation_id = ?`
		args = append(args, invocationID)
	}
	if workItemID != "" {
		query += ` AND work_item_id = ?`
		args = append(args, workItemID)
	}
	if withCursor {
		query += ` AND (datetime(occurred_at) > datetime(?) OR (datetime(occurred_at) = datetime(?) AND id > ?))`
		args = append(args, "2026-01-01 00:00:10", "2026-01-01 00:00:10", "history-transition-000250-010")
	}
	query += ` ORDER BY occurred_at, id LIMIT ?`
	args = append(args, 21)
	return explainAutomationHistoryPlan(tb, db, query, args...)
}

func explainAutomationHistoryPlan(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(tb, err)
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(tb, rows.Scan(&id, &parent, &unused, &detail))
		details = append(details, detail)
	}
	require.NoError(tb, rows.Err())
	return strings.Join(details, "; ")
}

func assertAutomationHistoryPlan(t testing.TB, plan, wantIndex string) {
	t.Helper()
	require.Contains(t, plan, wantIndex)
	require.NotContains(t, plan, "USE TEMP B-TREE FOR ORDER BY")
}

func TestAutomationHistoryWorkItemQueryPlanUsesCreatedAtIndexes(t *testing.T) {
	db, _, projectID, automationID := newAutomationWorkItemHistoryBenchFixture(t, 2000)

	assertAutomationHistoryPlan(t,
		explainAutomationWorkItemsHistoryPlan(t, db, projectID, automationID, "", false),
		"idx_automation_work_items_history")
	assertAutomationHistoryPlan(t,
		explainAutomationWorkItemsHistoryPlan(t, db, projectID, automationID, "", true),
		"idx_automation_work_items_history")
	assertAutomationHistoryPlan(t,
		explainAutomationWorkItemsHistoryPlan(t, db, projectID, automationID, "active", false),
		"idx_automation_work_items_history_status")
	assertAutomationHistoryPlan(t,
		explainAutomationWorkItemsHistoryPlan(t, db, projectID, automationID, "active", true),
		"idx_automation_work_items_history_status")
}

func TestAutomationHistoryTransitionQueryPlanUsesInvocationIndex(t *testing.T) {
	db, _, projectID, automationID, invocationID, workItemID := newAutomationTransitionHistoryBenchFixture(t, 500, 4)

	invocationPlan := explainAutomationTransitionsHistoryPlan(t, db, projectID, automationID, invocationID, "", false)
	assertAutomationHistoryPlan(t, invocationPlan, "idx_automation_transitions_invocation")
	require.Contains(t, invocationPlan, "invocation_id=?")
	invocationCursorPlan := explainAutomationTransitionsHistoryPlan(t, db, projectID, automationID, invocationID, "", true)
	assertAutomationHistoryPlan(t, invocationCursorPlan, "idx_automation_transitions_invocation")
	require.Contains(t, invocationCursorPlan, "invocation_id=?")
	assertAutomationHistoryPlan(t,
		explainAutomationTransitionsHistoryPlan(t, db, projectID, automationID, "", workItemID, false),
		"idx_automation_transitions_work_item")
	assertAutomationHistoryPlan(t,
		explainAutomationTransitionsHistoryPlan(t, db, projectID, automationID, "", workItemID, true),
		"idx_automation_transitions_work_item")
}

func BenchmarkAutomationWorkItemsHistoryQuery(b *testing.B) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{name: "Unfiltered"},
		{name: "Status", status: "active"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			db, repo, projectID, automationID := newAutomationWorkItemHistoryBenchFixture(b, 10000)
			plan := explainAutomationWorkItemsHistoryPlan(b, db, projectID, automationID, tc.status, false)
			require.NotContains(b, plan, "USE TEMP B-TREE FOR ORDER BY")
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, err := repo.ListAutomationWorkItems(ctx, projectID, automationID, tc.status, 20, "")
				require.NoError(b, err)
				require.Len(b, page.Items, 20)
				require.NotEmpty(b, page.NextCursor)
			}
		})
	}
}

func BenchmarkAutomationTransitionHistoryQuery(b *testing.B) {
	for _, tc := range []struct {
		name       string
		invocation bool
	}{
		{name: "Invocation", invocation: true},
		{name: "WorkItem"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			db, repo, projectID, automationID, invocationID, workItemID := newAutomationTransitionHistoryBenchFixture(b, 500, 20)
			planInvocationID := ""
			planWorkItemID := workItemID
			if tc.invocation {
				planInvocationID = invocationID
				planWorkItemID = ""
			}
			plan := explainAutomationTransitionsHistoryPlan(b, db, projectID, automationID, planInvocationID, planWorkItemID, false)
			if tc.invocation {
				require.Contains(b, plan, "idx_automation_transitions_invocation")
				require.NotContains(b, plan, "USE TEMP B-TREE FOR ORDER BY")
			}
			if !tc.invocation {
				require.Contains(b, plan, "idx_automation_transitions_work_item")
				require.NotContains(b, plan, "USE TEMP B-TREE FOR ORDER BY")
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				queryInvocationID := ""
				queryWorkItemID := workItemID
				if tc.invocation {
					queryInvocationID = invocationID
					queryWorkItemID = ""
				}
				page, err := repo.ListAutomationTransitions(ctx, projectID, automationID, queryInvocationID, queryWorkItemID, 20, "")
				require.NoError(b, err)
				require.Len(b, page.Items, 20)
				require.Empty(b, page.NextCursor)
			}
		})
	}
}
