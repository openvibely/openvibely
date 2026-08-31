package service

import (
	"context"
	"database/sql"
	"fmt"
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

func setAutomationWorkItemHistoryIndexes(tb testing.TB, db *sql.DB, candidate bool) {
	tb.Helper()
	if !candidate {
		_, err := db.Exec(`DROP INDEX IF EXISTS idx_automation_work_items_history;
			DROP INDEX IF EXISTS idx_automation_work_items_history_status;`)
		require.NoError(tb, err)
		return
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_automation_work_items_history
			ON automation_work_items(project_id, automation_id, created_at DESC, id DESC);
		CREATE INDEX IF NOT EXISTS idx_automation_work_items_history_status
			ON automation_work_items(project_id, automation_id, status, created_at DESC, id DESC);`)
	require.NoError(tb, err)
}

func BenchmarkAutomationWorkItemsHistoryQuery(b *testing.B) {
	for _, tc := range []struct {
		name      string
		status    string
		candidate bool
	}{
		{name: "BaselineUnfilteredTempSort", candidate: false},
		{name: "IndexedUnfiltered", candidate: true},
		{name: "BaselineStatusTempSort", status: "active", candidate: false},
		{name: "IndexedStatus", status: "active", candidate: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			db, repo, projectID, automationID := newAutomationWorkItemHistoryBenchFixture(b, 10000)
			setAutomationWorkItemHistoryIndexes(b, db, tc.candidate)
			plan := explainAutomationWorkItemsHistoryPlan(b, db, projectID, automationID, tc.status, false)
			if tc.candidate {
				require.NotContains(b, plan, "USE TEMP B-TREE FOR ORDER BY")
			} else {
				require.Contains(b, plan, "USE TEMP B-TREE FOR ORDER BY")
			}
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

func setAutomationTransitionInvocationIndex(tb testing.TB, db *sql.DB, candidate bool) {
	tb.Helper()
	if !candidate {
		_, err := db.Exec(`DROP INDEX IF EXISTS idx_automation_transitions_invocation;`)
		require.NoError(tb, err)
		return
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_automation_transitions_invocation
		ON automation_transitions(project_id, automation_id, invocation_id, occurred_at, id);`)
	require.NoError(tb, err)
}

func BenchmarkAutomationTransitionHistoryQuery(b *testing.B) {
	for _, tc := range []struct {
		name       string
		invocation bool
		candidate  bool
	}{
		{name: "BaselineInvocationTempSort", invocation: true, candidate: false},
		{name: "IndexedInvocation", invocation: true, candidate: true},
		{name: "IndexedWorkItem", candidate: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			db, repo, projectID, automationID, invocationID, workItemID := newAutomationTransitionHistoryBenchFixture(b, 500, 20)
			setAutomationTransitionInvocationIndex(b, db, tc.candidate)
			planInvocationID := ""
			planWorkItemID := workItemID
			if tc.invocation {
				planInvocationID = invocationID
				planWorkItemID = ""
			}
			plan := explainAutomationTransitionsHistoryPlan(b, db, projectID, automationID, planInvocationID, planWorkItemID, false)
			if tc.invocation && tc.candidate {
				require.Contains(b, plan, "idx_automation_transitions_invocation")
				require.NotContains(b, plan, "USE TEMP B-TREE FOR ORDER BY")
			}
			if tc.invocation && !tc.candidate {
				require.Contains(b, plan, "USE TEMP B-TREE FOR ORDER BY")
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
