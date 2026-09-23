package service

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func createHistoryInvocation(t *testing.T, fixture automationRuntimeFixture, suffix, status string) models.AutomationInvocation {
	t.Helper()
	trigger := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	var invocation models.AutomationInvocation
	err := fixture.repo.DB().QueryRow(`INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
		 occurrence_key, status, started_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, 'schedule', ?, ?, ?, CURRENT_TIMESTAMP,
		 CASE WHEN ? IN ('completed','failed','cancelled','skipped') THEN CURRENT_TIMESTAMP ELSE NULL END,
		 CASE WHEN ? = 'failed' THEN 'dispatch failed' ELSE '' END)
		RETURNING id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type,
		 trigger_resource_id, occurrence_key, scheduled_for, status, skipped_reason, started_at, completed_at,
		 created_at, updated_at, error_message`, fixture.project.ID, fixture.definition.Automation.ID,
		fixture.definition.Version.ID, trigger.ID, fixture.schedule.ID, "history-"+suffix, status, status, status).
		Scan(&invocation.ID, &invocation.ProjectID, &invocation.AutomationID, &invocation.VersionID,
			&invocation.TriggerNodeID, &invocation.TriggerResourceType, &invocation.TriggerResourceID,
			&invocation.OccurrenceKey, &invocation.ScheduledFor, &invocation.Status, &invocation.SkippedReason,
			&invocation.StartedAt, &invocation.CompletedAt, &invocation.CreatedAt, &invocation.UpdatedAt,
			&invocation.ErrorMessage)
	require.NoError(t, err)
	return invocation
}

func newAutomationWorkItemHistoryBenchFixture(tb testing.TB, rowCount int) (*sql.DB, *repository.AutomationRepo, string, string) {
	tb.Helper()
	db := testutil.NewTestDB(tb)
	projectID, automationID, versionID := createAutomationHistoryBenchDefinition(tb, db, "Automation work item history", "automation-work-items-history", "version-work-items-history")
	seedAutomationWorkItemHistoryRows(tb, db, projectID, automationID, versionID, rowCount)
	return db, repository.NewAutomationRepo(db), projectID, automationID
}

func createAutomationHistoryBenchDefinition(tb testing.TB, db *sql.DB, name, automationID, versionID string) (string, string, string) {
	tb.Helper()
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
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

func newAutomationTransitionHistoryBenchFixture(tb testing.TB, invocationCount, transitionsPerInvocation int) (*sql.DB, *repository.AutomationRepo, string, string, string, string) {
	tb.Helper()
	db := testutil.NewTestDB(tb)
	projectID, automationID, versionID := createAutomationHistoryBenchDefinition(tb, db, "Automation transition history", "automation-transition-history", "version-transition-history")
	targetInvocationID, targetWorkItemID := seedAutomationTransitionHistoryRows(tb, db, projectID, automationID, versionID, invocationCount, transitionsPerInvocation)
	return db, repository.NewAutomationRepo(db), projectID, automationID, targetInvocationID, targetWorkItemID
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

func TestAutomationHistoryStablePaginationAndProjectIsolation(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		createHistoryInvocation(t, fixture, fmt.Sprint(i), "completed")
	}
	_, err := fixture.repo.DB().Exec(`UPDATE automation_invocations SET created_at = '2026-01-02 03:04:05', started_at = '2026-01-02 03:04:05' WHERE automation_id = ?`, fixture.definition.Automation.ID)
	require.NoError(t, err)

	first, err := fixture.repo.ListAutomationInvocations(ctx, fixture.project.ID, fixture.definition.Automation.ID, 2, "")
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	second, err := fixture.repo.ListAutomationInvocations(ctx, fixture.project.ID, fixture.definition.Automation.ID, 2, first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 2)
	require.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
	require.NotEqual(t, first.Items[1].ID, second.Items[1].ID)

	foreign, err := fixture.repo.ListAutomationInvocations(ctx, "foreign-project", fixture.definition.Automation.ID, 50, "")
	require.NoError(t, err)
	require.Empty(t, foreign.Items)
	_, err = fixture.repo.ListAutomationInvocations(ctx, fixture.project.ID, fixture.definition.Automation.ID, 50, "tampered")
	require.ErrorIs(t, err, repository.ErrAutomationCursor)
}

func TestAutomationHistoryInvocationIsolationWorkItemLifetimeAndReplay(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	invocationA := createHistoryInvocation(t, fixture, "a", "completed")
	invocationB := createHistoryInvocation(t, fixture, "b", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	gate := automationNodeByKey(t, fixture.definition, "approval")

	bindingA := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocationA.ID, NodeID: producer.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{bindingA}}, Binding: bindingA,
		WorkItemKey: "alert:history", WorkItemKind: "suggestion", WorkItemTitle: "History suggestion",
		ActivityKey: "history:a", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "history:a:entered", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	bindingB := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocationB.ID, NodeID: gate.ID, WorkItemID: item.ID}
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{bindingB}}, Binding: bindingB,
		WorkItemKey: "alert:history", ActivityKey: "history:b", ActivityType: "approval", ActivityStatus: models.AutomationActivityWaiting,
		EventKey: "history:b:waiting", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_transitions SET occurred_at = CASE event_key
		WHEN 'history:a:entered' THEN '2026-01-01 00:00:00' WHEN 'history:b:waiting' THEN '2026-01-01 00:01:00' ELSE occurred_at END
		WHERE automation_id = ?`, fixture.definition.Automation.ID)
	require.NoError(t, err)

	graphService := NewAutomationGraphService(fixture.repo)
	invocationGraph, err := graphService.GetInvocationHistory(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocationA.ID, 50, "", "")
	require.NoError(t, err)
	require.NotNil(t, invocationGraph)
	require.Len(t, invocationGraph.Activities.Items, 1)
	require.Equal(t, invocationA.ID, invocationGraph.Activities.Items[0].InvocationID)
	require.Len(t, invocationGraph.Transitions.Items, 1)
	require.Equal(t, invocationA.ID, invocationGraph.Transitions.Items[0].InvocationID)
	require.Equal(t, []string{producer.ID}, invocationGraph.TouchedNodeIDs)

	workHistory, err := graphService.GetWorkItemHistory(ctx, fixture.project.ID, fixture.definition.Automation.ID, item.ID, 50, "", "")
	require.NoError(t, err)
	require.NotNil(t, workHistory)
	require.Len(t, workHistory.Activities.Items, 2)
	require.Len(t, workHistory.Transitions.Items, 2)
	require.Len(t, workHistory.Replay, 2)
	require.Equal(t, producer.ID, workHistory.Replay[0].Positions[0].NodeID)
	require.Equal(t, gate.ID, workHistory.Replay[1].Positions[0].NodeID)
	require.Equal(t, models.AutomationPositionWaiting, workHistory.Replay[1].Positions[0].State)
	metrics, err := fixture.repo.GetAutomationMetrics(ctx, fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, time.Now().UTC())
	require.NoError(t, err)
	var gateWaiting int
	for _, bottleneck := range metrics.Bottlenecks {
		if bottleneck.NodeID == gate.ID {
			gateWaiting = bottleneck.Waiting
		}
	}
	require.Equal(t, 1, gateWaiting)

	foreign, err := graphService.GetWorkItemHistory(ctx, "foreign-project", fixture.definition.Automation.ID, item.ID, 50, "", "")
	require.NoError(t, err)
	require.Nil(t, foreign)
}

func TestAutomationHistoryMetricsAndHealthUsePersistedEvents(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	invocation := createHistoryInvocation(t, fixture, "metrics", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	gate := automationNodeByKey(t, fixture.definition, "approval")
	completedNode := automationNodeByKey(t, fixture.definition, "completed")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: producer.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "metric:item", ActivityKey: "metric:producer", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "metric:entered", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	binding.WorkItemID, binding.NodeID = item.ID, gate.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "metric:item", ActivityKey: "metric:gate", ActivityType: "gate", ActivityStatus: models.AutomationActivityWaiting,
		EventKey: "metric:gate", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	binding.NodeID = completedNode.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "metric:item", ActivityKey: "metric:completed", ActivityType: "outcome", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "metric:completed", FromNodeID: gate.ID, ToNodeID: completedNode.ID, Transition: models.AutomationTransitionCompleted,
	})
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_transitions SET occurred_at = CASE event_key
		WHEN 'metric:entered' THEN '2026-01-01 00:00:00' WHEN 'metric:gate' THEN '2026-01-01 00:02:00'
		WHEN 'metric:completed' THEN '2026-01-01 00:05:00' ELSE occurred_at END`)
	require.NoError(t, err)

	metrics, err := fixture.repo.GetAutomationMetrics(ctx, fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotEmpty(t, metrics.Funnel)
	var producerConversion, producerDuration, gateDuration float64
	for _, point := range metrics.Funnel {
		if point.NodeID == producer.ID {
			producerConversion = point.ConversionPercent
		}
	}
	require.Equal(t, 100.0, producerConversion)
	for _, point := range metrics.Durations {
		if point.NodeID == producer.ID {
			producerDuration = point.AverageSeconds
		}
		if point.NodeID == gate.ID {
			gateDuration = point.AverageSeconds
		}
	}
	require.InDelta(t, 120, producerDuration, 0.1)
	require.InDelta(t, 180, gateDuration, 0.1)

	health, err := fixture.repo.RecomputeAutomationHealth(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, models.AutomationHealthHealthy, health.State)
	for i := 0; i < 3; i++ {
		createHistoryInvocation(t, fixture, fmt.Sprintf("failed-%d", i), "failed")
	}
	_, err = fixture.repo.DB().Exec(`UPDATE automation_invocations SET completed_at = '2099-01-01 00:00:00'
		WHERE automation_id = ? AND status = 'failed'`, fixture.definition.Automation.ID)
	require.NoError(t, err)
	health, err = fixture.repo.RecomputeAutomationHealth(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, models.AutomationHealthUnhealthy, health.State)
	metrics, err = fixture.repo.GetAutomationMetrics(ctx, fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, time.Now().UTC())
	require.NoError(t, err)
	var recentTriggerFailures int
	for _, failure := range metrics.Failures {
		if failure.NodeID == automationNodeByKey(t, fixture.definition, "vision_suggestions").ID {
			recentTriggerFailures = failure.Count
		}
	}
	require.Equal(t, 3, recentTriggerFailures)
	var lifecycle, storedHealth string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT lifecycle_state, health_state FROM automations WHERE id = ?`, fixture.definition.Automation.ID).Scan(&lifecycle, &storedHealth))
	require.Equal(t, "active", lifecycle)
	require.Equal(t, "unhealthy", storedHealth)
}

func TestAutomationHistoryReplayPaginationSeedsPersistedPriorState(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	invocationA := createHistoryInvocation(t, fixture, "replay-a", "completed")
	invocationB := createHistoryInvocation(t, fixture, "replay-b", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	gate := automationNodeByKey(t, fixture.definition, "approval")

	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocationA.ID, NodeID: producer.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "replay:paged", ActivityKey: "replay:paged:create", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "replay:paged:entered", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	binding.InvocationID, binding.NodeID, binding.WorkItemID = invocationB.ID, gate.ID, item.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "replay:paged", ActivityKey: "replay:paged:wait", ActivityType: "approval", ActivityStatus: models.AutomationActivityWaiting,
		EventKey: "replay:paged:waiting", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_transitions SET occurred_at = CASE event_key
		WHEN 'replay:paged:entered' THEN '2026-01-01 00:00:00' WHEN 'replay:paged:waiting' THEN '2026-01-01 00:01:00' ELSE occurred_at END
		WHERE automation_id = ?`, fixture.definition.Automation.ID)
	require.NoError(t, err)

	graphService := NewAutomationGraphService(fixture.repo)
	first, err := graphService.GetWorkItemHistory(ctx, fixture.project.ID, fixture.definition.Automation.ID, item.ID, 1, "", "")
	require.NoError(t, err)
	require.Len(t, first.Transitions.Items, 1)
	require.NotEmpty(t, first.Transitions.NextCursor)
	second, err := graphService.GetWorkItemHistory(ctx, fixture.project.ID, fixture.definition.Automation.ID, item.ID, 1, first.Transitions.NextCursor, "")
	require.NoError(t, err)
	require.Len(t, second.Replay, 1)
	require.Len(t, second.Replay[0].Positions, 1)
	require.Equal(t, gate.ID, second.Replay[0].Positions[0].NodeID)
	require.Equal(t, models.AutomationPositionWaiting, second.Replay[0].Positions[0].State)
}

func TestAutomationHistoryActivityCursorIsStableAndCollectionBound(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	invocation := createHistoryInvocation(t, fixture, "activity-pages", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: producer.ID}
	for i := 0; i < 3; i++ {
		_, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: "activity:paged", ActivityKey: fmt.Sprintf("activity:paged:%d", i), ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
			EventKey: fmt.Sprintf("activity:paged:entered:%d", i), ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
		})
		require.NoError(t, err)
	}
	_, err := fixture.repo.DB().Exec(`UPDATE automation_activities SET started_at = '2026-01-01 00:00:00' WHERE automation_id = ?`, fixture.definition.Automation.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_transitions SET occurred_at = '2026-01-01 00:00:00' WHERE automation_id = ?`, fixture.definition.Automation.ID)
	require.NoError(t, err)

	first, err := fixture.repo.ListAutomationActivities(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, "")
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	second, err := fixture.repo.ListAutomationActivities(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	_, err = fixture.repo.ListAutomationTransitions(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, first.NextCursor)
	require.ErrorIs(t, err, repository.ErrAutomationCursor)

	transitionFirst, err := fixture.repo.ListAutomationTransitions(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, "")
	require.NoError(t, err)
	require.Len(t, transitionFirst.Items, 2)
	require.NotEmpty(t, transitionFirst.NextCursor)
	transitionSecond, err := fixture.repo.ListAutomationTransitions(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, transitionFirst.NextCursor)
	require.NoError(t, err)
	require.Len(t, transitionSecond.Items, 1)
}

func TestAutomationHistoryWorkItemPaginationIsStableAndFilterBound(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: producer.ID}
	for i := 0; i < 6; i++ {
		item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: fmt.Sprintf("work-item:paged:%d", i), WorkItemKind: "test", ActivityKey: fmt.Sprintf("work-item:paged:%d:create", i), ActivityType: "producer", ActivityStatus: models.AutomationActivityRunning,
		})
		require.NoError(t, err)
		if i >= 4 {
			_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE automation_work_items SET status = 'completed', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, item.ID)
			require.NoError(t, err)
		}
	}
	_, err := fixture.repo.DB().Exec(`UPDATE automation_work_items SET created_at = '2026-01-01 00:00:00' WHERE automation_id = ?`, fixture.definition.Automation.ID)
	require.NoError(t, err)

	unfilteredFirst, err := fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "", 3, "")
	require.NoError(t, err)
	require.Len(t, unfilteredFirst.Items, 3)
	require.NotEmpty(t, unfilteredFirst.NextCursor)
	unfilteredSecond, err := fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "", 3, unfilteredFirst.NextCursor)
	require.NoError(t, err)
	require.Len(t, unfilteredSecond.Items, 3)
	combined := append(append([]models.AutomationWorkItem{}, unfilteredFirst.Items...), unfilteredSecond.Items...)
	for i := 1; i < len(combined); i++ {
		require.Greater(t, combined[i-1].ID, combined[i].ID)
	}

	first, err := fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "active", 2, "")
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	for _, item := range first.Items {
		require.Equal(t, models.AutomationWorkItemActive, item.Status)
	}
	second, err := fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "active", 2, first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 2)
	for _, item := range second.Items {
		require.Equal(t, models.AutomationWorkItemActive, item.Status)
	}
	completed, err := fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "completed", 10, "")
	require.NoError(t, err)
	require.Len(t, completed.Items, 2)
	for _, item := range completed.Items {
		require.Equal(t, models.AutomationWorkItemCompleted, item.Status)
	}
	_, err = fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "completed", 2, first.NextCursor)
	require.ErrorIs(t, err, repository.ErrAutomationCursor)
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

func TestAutomationHistoryHealthReconciliationCoversEverySavedAutomationBeyondOneBatch(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()

	for i := 0; i < 101; i++ {
		automationID := fmt.Sprintf("health-batch-automation-%03d", i)
		versionID := fmt.Sprintf("health-batch-version-%03d", i)
		_, err := fixture.repo.DB().ExecContext(ctx, `INSERT INTO automations
			(id, project_id, stable_key, name, automation_type, lifecycle_state, created_via)
			VALUES (?, ?, ?, ?, 'custom', 'active', 'web')`,
			automationID, fixture.project.ID, automationID, fmt.Sprintf("Health batch %03d", i))
		require.NoError(t, err)
		_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_versions
			(id, project_id, automation_id, version, state, source, adapter_key, schema_version, published_at)
			VALUES (?, ?, ?, 1, 'published', 'manual', 'custom', 1, CURRENT_TIMESTAMP)`,
			versionID, fixture.project.ID, automationID)
		require.NoError(t, err)
		_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE automations SET published_version_id = ? WHERE id = ?`, versionID, automationID)
		require.NoError(t, err)
	}
	_, err := fixture.repo.DB().ExecContext(ctx, `UPDATE automations SET health_evaluated_at = NULL WHERE project_id = ?`, fixture.project.ID)
	require.NoError(t, err)

	err = fixture.repo.RecomputeAutomationHealthForAll(ctx, time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC), 100)
	require.NoError(t, err)

	var saved, evaluated int
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automations
		WHERE project_id = ? AND published_version_id IS NOT NULL`, fixture.project.ID).Scan(&saved))
	require.Greater(t, saved, 100)
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automations
		WHERE project_id = ? AND published_version_id IS NOT NULL AND health_evaluated_at IS NOT NULL`, fixture.project.ID).Scan(&evaluated))
	require.Equal(t, saved, evaluated, "one reconciliation pass must not permanently starve saved Automations beyond the first batch")
}

func TestAutomationHistoryHealthIgnoresStaleFailureAfterRecentSuccesses(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)

	health, err := fixture.repo.RecomputeAutomationHealth(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Equal(t, models.AutomationHealthUnknown, health.State)

	staleFailure := createHistoryInvocation(t, fixture, "stale-failure", "failed")
	_, err = fixture.repo.DB().Exec(`UPDATE automation_invocations SET completed_at = '2025-01-01 00:00:00', updated_at = '2025-01-01 00:00:00' WHERE id = ?`, staleFailure.ID)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		completed := createHistoryInvocation(t, fixture, fmt.Sprintf("recent-success-%d", i), "completed")
		_, err = fixture.repo.DB().Exec(`UPDATE automation_invocations SET completed_at = ?, updated_at = ? WHERE id = ?`, now.Add(time.Duration(i)*time.Minute), now.Add(time.Duration(i)*time.Minute), completed.ID)
		require.NoError(t, err)
	}

	health, err = fixture.repo.RecomputeAutomationHealth(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Equal(t, models.AutomationHealthHealthy, health.State)
	var lifecycle string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT lifecycle_state FROM automations WHERE id = ?`, fixture.definition.Automation.ID).Scan(&lifecycle))
	require.Equal(t, "active", lifecycle)
}

func TestAutomationHistoryHealthStableNoChangeSkipsAutomationUpdatesAndReducesSQL(t *testing.T) {
	for _, count := range []int{100, 500} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			db, counter := testutil.NewStatementCountingTestDB(t)
			repo := repository.NewAutomationRepo(db)
			ctx := context.Background()
			now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
			fixture := seedAutomationHealthReconciliationFixture(t, db, count, now)

			require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, now, 100))
			counter.Reset()
			counter.SetEnabled(true)
			before := db.Stats()
			start := time.Now()
			require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, now.Add(time.Minute), 100))
			duration := time.Since(start)
			after := db.Stats()
			counter.SetEnabled(false)

			statements := counter.Statements()
			updates := countAutomationHealthUpdates(statements)
			baselineStatements := legacyAutomationHealthStatementCount(count)
			require.Zero(t, updates, "stable published Automations must not issue unchanged UPDATE automations statements")
			require.LessOrEqual(t, len(statements), baselineStatements/10, "stable health SQL should drop by at least 90%% versus the former per-Automation path")
			requireAutomationHealthEvaluatedCount(t, db, fixture.projectID, count)
			requireAutomationHealthUnevaluated(t, db, fixture.unpublishedAutomationID)
			t.Logf("stable count=%d duration=%s sql=%d baseline_sql=%d update_automations=%d db_wait_count=%d db_wait=%s", count, duration, len(statements), baselineStatements, updates, after.WaitCount-before.WaitCount, after.WaitDuration-before.WaitDuration)
		})
	}
}

func TestAutomationHistoryHealthChangedInputsPersistAffectedRecords(t *testing.T) {
	t.Run("blocked position", func(t *testing.T) {
		fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
		ctx := context.Background()
		now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
		completed := createHistoryInvocation(t, fixture, "blocked-baseline", "completed")
		_, err := fixture.repo.DB().ExecContext(ctx, `UPDATE automation_invocations SET completed_at = ?, updated_at = ? WHERE id = ?`, now, now, completed.ID)
		require.NoError(t, err)
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now, 100))
		requireAutomationHealthState(t, fixture.repo.DB(), fixture.definition.Automation.ID, models.AutomationHealthHealthy, "Recent triggers")

		producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
		gate := automationNodeByKey(t, fixture.definition, "approval")
		binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: completed.ID, NodeID: producer.ID}
		item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: "health:blocked", ActivityKey: "health:blocked:create", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
			EventKey: "health:blocked:create", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
		})
		require.NoError(t, err)
		binding.WorkItemID, binding.NodeID = item.ID, gate.ID
		_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: "health:blocked", ActivityKey: "health:blocked:gate", ActivityType: "approval", ActivityStatus: models.AutomationActivityFailed,
			EventKey: "health:blocked:gate", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionBlocked,
		})
		require.NoError(t, err)

		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now.Add(time.Minute), 100))
		requireAutomationHealthState(t, fixture.repo.DB(), fixture.definition.Automation.ID, models.AutomationHealthDegraded, "1 blocked or failed position")
	})

	t.Run("recent failure", func(t *testing.T) {
		fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
		ctx := context.Background()
		now := time.Date(2026, time.August, 2, 13, 0, 0, 0, time.UTC)
		completed := createHistoryInvocation(t, fixture, "failure-baseline", "completed")
		_, err := fixture.repo.DB().ExecContext(ctx, `UPDATE automation_invocations SET completed_at = ?, updated_at = ? WHERE id = ?`, now, now, completed.ID)
		require.NoError(t, err)
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now, 100))
		requireAutomationHealthState(t, fixture.repo.DB(), fixture.definition.Automation.ID, models.AutomationHealthHealthy, "Recent triggers")

		failed := createHistoryInvocation(t, fixture, "fresh-failure", "failed")
		_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE automation_invocations SET completed_at = ?, updated_at = ? WHERE id = ?`, now.Add(time.Minute), now.Add(time.Minute), failed.ID)
		require.NoError(t, err)
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now.Add(2*time.Minute), 100))
		requireAutomationHealthState(t, fixture.repo.DB(), fixture.definition.Automation.ID, models.AutomationHealthDegraded, "1 recent failed invocation")
	})

	t.Run("stale external state", func(t *testing.T) {
		fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
		ctx := context.Background()
		now := time.Date(2026, time.August, 2, 14, 0, 0, 0, time.UTC)
		completed := createHistoryInvocation(t, fixture, "external-baseline", "completed")
		_, err := fixture.repo.DB().ExecContext(ctx, `UPDATE automation_invocations SET completed_at = ?, updated_at = ? WHERE id = ?`, now, now, completed.ID)
		require.NoError(t, err)
		attachAutomationHealthPullRequest(t, fixture, completed.ID, now)
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now, 100))
		requireAutomationHealthState(t, fixture.repo.DB(), fixture.definition.Automation.ID, models.AutomationHealthHealthy, "Recent triggers")

		_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = ? WHERE task_id = ?`, now.Add(-2*repository.AutomationExternalStaleAfter).UTC().Format("2006-01-02 15:04:05"), fixture.task.ID)
		require.NoError(t, err)
		externalState, err := fixture.repo.AutomationExternalState(ctx, fixture.project.ID, fixture.definition.Automation.ID, now.Add(time.Minute).Add(-repository.AutomationExternalStaleAfter))
		require.NoError(t, err)
		require.Equal(t, 1, externalState.TrackedResources)
		require.True(t, externalState.Stale)
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now.Add(time.Minute), 100))
		requireAutomationHealthState(t, fixture.repo.DB(), fixture.definition.Automation.ID, models.AutomationHealthDegraded, "external GitHub state is stale")
	})

	t.Run("publication", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		repo := repository.NewAutomationRepo(db)
		ctx := context.Background()
		project := automationTestProject(t, repository.NewProjectRepo(db), "Health publication")
		automationID, versionID := "health-publication-automation", "health-publication-version"
		require.NoError(t, insertMinimalAutomationDefinition(ctx, db, project.ID, automationID, versionID, false))
		require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, time.Date(2026, time.August, 2, 15, 0, 0, 0, time.UTC), 100))
		requireAutomationHealthUnevaluated(t, db, automationID)

		_, err := db.ExecContext(ctx, `UPDATE automations SET published_version_id = ? WHERE id = ?`, versionID, automationID)
		require.NoError(t, err)
		require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, time.Date(2026, time.August, 2, 15, 1, 0, 0, time.UTC), 100))
		requireAutomationHealthState(t, db, automationID, models.AutomationHealthUnknown, "No terminal invocation yet")
	})
}

type automationHealthReconciliationFixture struct {
	projectID                string
	automationIDs            []string
	changePositionWorkItemID string
	changePositionNodeID     string
	unpublishedAutomationID  string
}

func seedAutomationHealthReconciliationFixture(tb testing.TB, db *sql.DB, automationCount int, now time.Time) automationHealthReconciliationFixture {
	tb.Helper()
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := models.Project{Name: fmt.Sprintf("Health reconciliation %d", automationCount)}
	require.NoError(tb, projectRepo.Create(ctx, &project))
	taskRepo := repository.NewTaskRepo(db, nil)
	pullRepo := repository.NewTaskPullRequestRepo(db)
	fixture := automationHealthReconciliationFixture{projectID: project.ID}
	for i := 0; i < automationCount; i++ {
		automationID := fmt.Sprintf("health-reconcile-%03d", i)
		versionID := fmt.Sprintf("health-reconcile-version-%03d", i)
		require.NoError(tb, insertMinimalAutomationDefinition(ctx, db, project.ID, automationID, versionID, true))
		triggerID, gateID := automationID+"-trigger", automationID+"-gate"
		fixture.automationIDs = append(fixture.automationIDs, automationID)

		activityInvocationID := fmt.Sprintf("%s-invocation-activity", automationID)
		switch i % 4 {
		case 0:
			insertAutomationHealthInvocation(tb, db, project.ID, automationID, versionID, triggerID, activityInvocationID, "completed", now.Add(time.Duration(i)*time.Second))
			if fixture.changePositionWorkItemID == "" {
				workItemID := fmt.Sprintf("%s-work-active", automationID)
				insertAutomationHealthPosition(tb, db, project.ID, automationID, versionID, gateID, workItemID, models.AutomationPositionActive)
				fixture.changePositionWorkItemID = workItemID
				fixture.changePositionNodeID = gateID
			}
		case 1:
			insertAutomationHealthInvocation(tb, db, project.ID, automationID, versionID, triggerID, activityInvocationID, "completed", now.Add(time.Duration(i)*time.Second))
			workItemID := fmt.Sprintf("%s-work-blocked", automationID)
			insertAutomationHealthPosition(tb, db, project.ID, automationID, versionID, gateID, workItemID, models.AutomationPositionBlocked)
			if fixture.changePositionWorkItemID == "" {
				fixture.changePositionWorkItemID = workItemID
				fixture.changePositionNodeID = gateID
			}
		case 2:
			for j := 0; j < 3; j++ {
				invocationID := fmt.Sprintf("%s-invocation-failed-%d", automationID, j)
				insertAutomationHealthInvocation(tb, db, project.ID, automationID, versionID, triggerID, invocationID, "failed", now.Add(time.Duration(i*10+j)*time.Second))
				if j == 0 {
					activityInvocationID = invocationID
				}
			}
		default:
			insertAutomationHealthInvocation(tb, db, project.ID, automationID, versionID, triggerID, activityInvocationID, "running", now.Add(time.Duration(i)*time.Second))
		}

		task := models.Task{ProjectID: project.ID, Title: fmt.Sprintf("Health task %03d", i), Category: models.CategoryBacklog, Priority: 1, Status: models.StatusPending, Prompt: "health fixture task"}
		require.NoError(tb, taskRepo.Create(ctx, &task))
		pull := models.TaskPullRequest{TaskID: task.ID, PRNumber: i + 1, PRURL: fmt.Sprintf("https://github.com/openvibely/openvibely/pull/%d", i+1), PRState: "open"}
		require.NoError(tb, pullRepo.Upsert(ctx, &pull))
		insertAutomationHealthActivityResources(tb, db, project.ID, automationID, versionID, triggerID, activityInvocationID, task.ID, pull.ID, i)
	}
	fixture.unpublishedAutomationID = "health-reconcile-unpublished"
	require.NoError(tb, insertMinimalAutomationDefinition(ctx, db, project.ID, fixture.unpublishedAutomationID, "health-reconcile-unpublished-version", false))
	return fixture
}

func insertMinimalAutomationDefinition(ctx context.Context, db *sql.DB, projectID, automationID, versionID string, published bool) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, automation_type, lifecycle_state, created_via)
		VALUES (?, ?, ?, ?, 'custom', 'active', 'web')`, automationID, projectID, automationID, automationID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, schema_version, published_at)
		VALUES (?, ?, ?, 1, 'published', 'manual', 'custom', 1, CURRENT_TIMESTAMP)`, versionID, projectID, automationID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_nodes
		(id, project_id, automation_id, version_id, node_key, name, node_type, role)
		VALUES (?, ?, ?, ?, 'trigger', 'Trigger', 'trigger', 'trigger'), (?, ?, ?, ?, 'gate', 'Gate', 'human_gate', 'approval')`,
		automationID+"-trigger", projectID, automationID, versionID, automationID+"-gate", projectID, automationID, versionID); err != nil {
		return err
	}
	if published {
		_, err := db.ExecContext(ctx, `UPDATE automations SET published_version_id = ? WHERE id = ?`, versionID, automationID)
		return err
	}
	return nil
}

func insertAutomationHealthInvocation(tb testing.TB, db *sql.DB, projectID, automationID, versionID, triggerID, invocationID, status string, at time.Time) {
	tb.Helper()
	var completed interface{}
	if status == "completed" || status == "failed" || status == "cancelled" || status == "skipped" {
		completed = at.UTC()
	}
	_, err := db.ExecContext(context.Background(), `INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
		 occurrence_key, status, started_at, completed_at, updated_at, error_message)
		VALUES (?, ?, ?, ?, ?, 'schedule', ?, ?, ?, ?, ?, ?, ?)`, invocationID, projectID, automationID, versionID, triggerID,
		"health-schedule", invocationID, status, at.UTC(), completed, at.UTC(), map[bool]string{true: "dispatch failed", false: ""}[status == "failed"])
	require.NoError(tb, err)
}

func insertAutomationHealthPosition(tb testing.TB, db *sql.DB, projectID, automationID, versionID, nodeID, workItemID string, state models.AutomationPositionState) {
	tb.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, work_item_key, kind, title, status)
		VALUES (?, ?, ?, ?, ?, 'health', 'Health position', ?)`, workItemID, projectID, automationID, versionID, workItemID, string(state))
	require.NoError(tb, err)
	_, err = db.ExecContext(context.Background(), `INSERT INTO automation_work_item_positions
		(work_item_id, project_id, automation_id, version_id, node_id, state)
		VALUES (?, ?, ?, ?, ?, ?)`, workItemID, projectID, automationID, versionID, nodeID, string(state))
	require.NoError(tb, err)
}

func insertAutomationHealthActivityResources(tb testing.TB, db *sql.DB, projectID, automationID, versionID, nodeID, invocationID, taskID, pullID string, index int) {
	tb.Helper()
	activityID := fmt.Sprintf("%s-activity-%03d", automationID, index)
	_, err := db.ExecContext(context.Background(), `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pull_request', 'completed')`, activityID, projectID, automationID, versionID, nodeID, invocationID, activityID)
	require.NoError(tb, err)
	_, err = db.ExecContext(context.Background(), `INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
		VALUES (?, 'task', ?, 'subject'), (?, 'pull_request', ?, 'subject')`, activityID, taskID, activityID, pullID)
	require.NoError(tb, err)
}

func attachAutomationHealthPullRequest(t *testing.T, fixture automationRuntimeFixture, invocationID string, updatedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	pullRepo := repository.NewTaskPullRequestRepo(fixture.repo.DB())
	pull := models.TaskPullRequest{TaskID: fixture.task.ID, PRNumber: 77, PRURL: "https://github.com/openvibely/openvibely/pull/77", PRState: "open"}
	require.NoError(t, pullRepo.Upsert(ctx, &pull))
	trigger := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	insertAutomationHealthActivityResources(t, fixture.repo.DB(), fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, trigger.ID, invocationID, fixture.task.ID, pull.ID, 77)
	_, err := fixture.repo.DB().ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = ? WHERE task_id = ?`, updatedAt.UTC().Format("2006-01-02 15:04:05"), fixture.task.ID)
	require.NoError(t, err)
}

func countAutomationHealthUpdates(statements []string) int {
	count := 0
	for _, statement := range statements {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statement)), "UPDATE AUTOMATIONS") {
			count++
		}
	}
	return count
}

func legacyAutomationHealthStatementCount(automationCount int) int {
	if automationCount <= 0 {
		return 1
	}
	return 4*automationCount + automationCount/100 + 1
}

func requireAutomationHealthEvaluatedCount(t *testing.T, db *sql.DB, projectID string, expected int) {
	t.Helper()
	var evaluated int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automations
		WHERE project_id = ? AND published_version_id IS NOT NULL AND health_evaluated_at IS NOT NULL`, projectID).Scan(&evaluated))
	require.Equal(t, expected, evaluated)
}

func requireAutomationHealthUnevaluated(t *testing.T, db *sql.DB, automationID string) {
	t.Helper()
	var evaluated sql.NullString
	require.NoError(t, db.QueryRow(`SELECT health_evaluated_at FROM automations WHERE id = ?`, automationID).Scan(&evaluated))
	require.False(t, evaluated.Valid)
}

func requireAutomationHealthState(t *testing.T, db *sql.DB, automationID string, expected models.AutomationHealthState, reasonContains string) {
	t.Helper()
	var state models.AutomationHealthState
	var reason string
	var evaluated sql.NullString
	require.NoError(t, db.QueryRow(`SELECT health_state, health_reason, health_evaluated_at FROM automations WHERE id = ?`, automationID).Scan(&state, &reason, &evaluated))
	require.Equal(t, expected, state)
	require.Contains(t, reason, reasonContains)
	require.True(t, evaluated.Valid)
}

type automationHealthMeasurement struct {
	duration   time.Duration
	statements int
	updates    int
	waitCount  int64
	wait       time.Duration
	allocs     uint64
	bytes      uint64
}

type automationHealthBenchmarkPath struct {
	name    string
	measure func(testing.TB, *sql.DB, *repository.AutomationRepo, time.Time) error
}

func measureAutomationHealthReconciliation(tb testing.TB, db *sql.DB, repo *repository.AutomationRepo, counter *testutil.SQLStatementCounter, now time.Time, measure func(testing.TB, *sql.DB, *repository.AutomationRepo, time.Time) error) automationHealthMeasurement {
	tb.Helper()
	var beforeMem, afterMem runtime.MemStats
	runtime.ReadMemStats(&beforeMem)
	counter.Reset()
	counter.SetEnabled(true)
	beforeStats := db.Stats()
	start := time.Now()
	require.NoError(tb, measure(tb, db, repo, now))
	duration := time.Since(start)
	afterStats := db.Stats()
	counter.SetEnabled(false)
	runtime.ReadMemStats(&afterMem)
	statements := counter.Statements()
	return automationHealthMeasurement{
		duration: duration, statements: len(statements), updates: countAutomationHealthUpdates(statements),
		waitCount: afterStats.WaitCount - beforeStats.WaitCount, wait: afterStats.WaitDuration - beforeStats.WaitDuration,
		allocs: afterMem.Mallocs - beforeMem.Mallocs, bytes: afterMem.TotalAlloc - beforeMem.TotalAlloc,
	}
}

func measureCurrentAutomationHealthPath(tb testing.TB, _ *sql.DB, repo *repository.AutomationRepo, now time.Time) error {
	tb.Helper()
	return repo.RecomputeAutomationHealthForAll(context.Background(), now, 100)
}

func medianAndP95(durations []time.Duration) (time.Duration, time.Duration) {
	if len(durations) == 0 {
		return 0, 0
	}
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p95Index := (len(sorted)*95 + 99) / 100
	if p95Index < 1 {
		p95Index = 1
	}
	return sorted[len(sorted)/2], sorted[p95Index-1]
}

func BenchmarkAutomationHealthReconciliation(b *testing.B) {
	paths := []automationHealthBenchmarkPath{
		{name: "current", measure: measureCurrentAutomationHealthPath},
	}
	for _, count := range []int{1, 10, 100, 500} {
		for _, path := range paths {
			b.Run(fmt.Sprintf("%s/stable/%d", path.name, count), func(b *testing.B) {
				db, counter := testutil.NewStatementCountingTestDB(b)
				repo := repository.NewAutomationRepo(db)
				now := time.Now().UTC()
				seedAutomationHealthReconciliationFixture(b, db, count, now)
				require.NoError(b, repo.RecomputeAutomationHealthForAll(context.Background(), now, 100))
				measurementTime := now.Add(time.Minute)
				b.ReportAllocs()
				durations := make([]time.Duration, 0, b.N)
				totalStatements, totalUpdates := 0, 0
				var totalWaitCount int64
				var totalWait time.Duration
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					measurement := measureAutomationHealthReconciliation(b, db, repo, counter, measurementTime, path.measure)
					durations = append(durations, measurement.duration)
					totalStatements += measurement.statements
					totalUpdates += measurement.updates
					totalWaitCount += measurement.waitCount
					totalWait += measurement.wait
				}
				b.StopTimer()
				median, p95 := medianAndP95(durations)
				b.ReportMetric(float64(median.Nanoseconds()), "median-ns/op")
				b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/op")
				b.ReportMetric(float64(totalStatements)/float64(b.N), "sql/op")
				b.ReportMetric(float64(totalUpdates)/float64(b.N), "update_automations/op")
				b.ReportMetric(float64(totalWaitCount)/float64(b.N), "db_wait_count/op")
				b.ReportMetric(float64(totalWait.Nanoseconds())/float64(b.N), "db_wait_ns/op")
			})
			b.Run(fmt.Sprintf("%s/changed-position/%d", path.name, count), func(b *testing.B) {
				db, counter := testutil.NewStatementCountingTestDB(b)
				repo := repository.NewAutomationRepo(db)
				now := time.Now().UTC()
				fixture := seedAutomationHealthReconciliationFixture(b, db, count, now)
				require.NoError(b, repo.RecomputeAutomationHealthForAll(context.Background(), now, 100))
				measurementTime := now.Add(time.Minute)
				b.ReportAllocs()
				durations := make([]time.Duration, 0, b.N)
				totalStatements, totalUpdates := 0, 0
				var totalWaitCount int64
				var totalWait time.Duration
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					state := models.AutomationPositionActive
					if i%2 == 0 {
						state = models.AutomationPositionBlocked
					}
					b.StopTimer()
					_, err := db.ExecContext(context.Background(), `UPDATE automation_work_item_positions SET state = ? WHERE work_item_id = ? AND node_id = ?`, string(state), fixture.changePositionWorkItemID, fixture.changePositionNodeID)
					require.NoError(b, err)
					b.StartTimer()
					measurement := measureAutomationHealthReconciliation(b, db, repo, counter, measurementTime, path.measure)
					durations = append(durations, measurement.duration)
					totalStatements += measurement.statements
					totalUpdates += measurement.updates
					totalWaitCount += measurement.waitCount
					totalWait += measurement.wait
				}
				b.StopTimer()
				median, p95 := medianAndP95(durations)
				b.ReportMetric(float64(median.Nanoseconds()), "median-ns/op")
				b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/op")
				b.ReportMetric(float64(totalStatements)/float64(b.N), "sql/op")
				b.ReportMetric(float64(totalUpdates)/float64(b.N), "update_automations/op")
				b.ReportMetric(float64(totalWaitCount)/float64(b.N), "db_wait_count/op")
				b.ReportMetric(float64(totalWait.Nanoseconds())/float64(b.N), "db_wait_ns/op")
			})
		}
	}
}
