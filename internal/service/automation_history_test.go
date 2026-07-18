package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/stretchr/testify/require"
)

func createHistoryInvocation(t *testing.T, fixture automationRuntimeFixture, suffix, status string) models.AutomationInvocation {
	t.Helper()
	trigger := automationNodeByKey(t, fixture.definition, "suggestion_trigger")
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
	producer := automationNodeByKey(t, fixture.definition, "suggestion_producer")
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
	producer := automationNodeByKey(t, fixture.definition, "suggestion_producer")
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
		if failure.NodeID == automationNodeByKey(t, fixture.definition, "suggestion_trigger").ID {
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
	producer := automationNodeByKey(t, fixture.definition, "suggestion_producer")
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
	producer := automationNodeByKey(t, fixture.definition, "suggestion_producer")
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
	producer := automationNodeByKey(t, fixture.definition, "suggestion_producer")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: producer.ID}
	for i := 0; i < 4; i++ {
		_, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: fmt.Sprintf("work-item:paged:%d", i), WorkItemKind: "test", ActivityKey: fmt.Sprintf("work-item:paged:%d:create", i), ActivityType: "producer", ActivityStatus: models.AutomationActivityRunning,
		})
		require.NoError(t, err)
	}
	_, err := fixture.repo.DB().Exec(`UPDATE automation_work_items SET created_at = '2026-01-01 00:00:00' WHERE automation_id = ?`, fixture.definition.Automation.ID)
	require.NoError(t, err)

	first, err := fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "active", 2, "")
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	second, err := fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "active", 2, first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 2)
	_, err = fixture.repo.ListAutomationWorkItems(ctx, fixture.project.ID, fixture.definition.Automation.ID, "completed", 2, first.NextCursor)
	require.ErrorIs(t, err, repository.ErrAutomationCursor)
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
