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
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func createHistoryInvocation(t *testing.T, fixture automationRuntimeFixture, suffix, status string) models.AutomationInvocation {
	t.Helper()
	trigger := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	now := time.Now().UTC()
	invocation := models.AutomationInvocation{ID: persistence.NewID(), ProjectID: fixture.project.ID, AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, TriggerNodeID: trigger.ID, TriggerResourceType: "schedule", TriggerResourceID: fixture.schedule.ID, OccurrenceKey: "history-" + suffix, Status: models.AutomationInvocationStatus(status), StartedAt: &now}
	switch status {
	case "completed", "failed", "cancelled", "skipped":
		invocation.CompletedAt = &now
	}
	if status == "failed" {
		invocation.ErrorMessage = "dispatch failed"
	}
	require.NoError(t, fixture.stores.Fixtures.SeedAutomationInvocation(t.Context(), invocation))
	stored, err := fixture.repo.GetAutomationInvocation(t.Context(), invocation.ProjectID, invocation.AutomationID, invocation.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	invocation = *stored

	return invocation
}

func TestAutomationHistoryStablePaginationAndProjectIsolation(t *testing.T) {
	t.Run("isolated", func(t *testing.T) { testAutomationHistoryStablePaginationAndProjectIsolation(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationHistoryStablePaginationAndProjectIsolation(t, teststores.WithProductionTopology())
	})
}

func testAutomationHistoryStablePaginationAndProjectIsolation(t *testing.T, opts ...teststores.Option) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC, opts...)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		invocation := createHistoryInvocation(t, fixture, fmt.Sprint(i), "completed")
		require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationStart(ctx, invocation.ID, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
	}

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
	require.ErrorIs(t, err, persistence.ErrAutomationCursor)
}

func TestAutomationHistoryInvocationIsolationWorkItemLifetimeAndReplay(t *testing.T) {
	t.Run("isolated", func(t *testing.T) { testAutomationHistoryInvocationIsolationWorkItemLifetimeAndReplay(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationHistoryInvocationIsolationWorkItemLifetimeAndReplay(t, teststores.WithProductionTopology())
	})
}

func testAutomationHistoryInvocationIsolationWorkItemLifetimeAndReplay(t *testing.T, opts ...teststores.Option) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC, opts...)
	ctx := context.Background()
	invocationA := createHistoryInvocation(t, fixture, "a", "completed")
	invocationB := createHistoryInvocation(t, fixture, "b", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	gate := automationNodeByKey(t, fixture.definition, "approval")

	bindingA := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocationA.ID, NodeID: producer.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{bindingA}}, Binding: bindingA,
		WorkItemKey: "alert:history", WorkItemKind: "suggestion", WorkItemTitle: "History suggestion",
		ActivityKey: "history:a", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "history:a:entered", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	bindingB := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocationB.ID, NodeID: gate.ID, WorkItemID: item.ID}
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{bindingB}}, Binding: bindingB,
		WorkItemKey: "alert:history", ActivityKey: "history:b", ActivityType: "approval", ActivityStatus: models.AutomationActivityWaiting,
		EventKey: "history:b:waiting", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, "history:a:entered", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, "history:b:waiting", time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)))

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
	t.Run("isolated", func(t *testing.T) { testAutomationHistoryMetricsAndHealthUsePersistedEvents(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationHistoryMetricsAndHealthUsePersistedEvents(t, teststores.WithProductionTopology())
	})
}

func testAutomationHistoryMetricsAndHealthUsePersistedEvents(t *testing.T, opts ...teststores.Option) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC, opts...)
	ctx := context.Background()
	invocation := createHistoryInvocation(t, fixture, "metrics", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	gate := automationNodeByKey(t, fixture.definition, "approval")
	completedNode := automationNodeByKey(t, fixture.definition, "completed")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: producer.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "metric:item", ActivityKey: "metric:producer", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "metric:entered", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	binding.WorkItemID, binding.NodeID = item.ID, gate.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "metric:item", ActivityKey: "metric:gate", ActivityType: "gate", ActivityStatus: models.AutomationActivityWaiting,
		EventKey: "metric:gate", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	binding.NodeID = completedNode.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "metric:item", ActivityKey: "metric:completed", ActivityType: "outcome", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "metric:completed", FromNodeID: gate.ID, ToNodeID: completedNode.ID, Transition: models.AutomationTransitionCompleted,
	})
	require.NoError(t, err)
	require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, "metric:entered", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, "metric:gate", time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC)))
	require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, "metric:completed", time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)))

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
		invocation := createHistoryInvocation(t, fixture, fmt.Sprintf("failed-%d", i), "failed")
		require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationCompletionTime(ctx, invocation.ID, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)))
	}

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
	stored := storedAutomation(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID)
	require.Equal(t, models.AutomationActive, stored.LifecycleState)
	require.Equal(t, models.AutomationHealthUnhealthy, stored.HealthState)
}

func TestAutomationHistoryReplayPaginationSeedsPersistedPriorState(t *testing.T) {
	t.Run("isolated", func(t *testing.T) { testAutomationHistoryReplayPaginationSeedsPersistedPriorState(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationHistoryReplayPaginationSeedsPersistedPriorState(t, teststores.WithProductionTopology())
	})
}

func testAutomationHistoryReplayPaginationSeedsPersistedPriorState(t *testing.T, opts ...teststores.Option) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC, opts...)
	ctx := context.Background()
	invocationA := createHistoryInvocation(t, fixture, "replay-a", "completed")
	invocationB := createHistoryInvocation(t, fixture, "replay-b", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	gate := automationNodeByKey(t, fixture.definition, "approval")

	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocationA.ID, NodeID: producer.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "replay:paged", ActivityKey: "replay:paged:create", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "replay:paged:entered", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	binding.InvocationID, binding.NodeID, binding.WorkItemID = invocationB.ID, gate.ID, item.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "replay:paged", ActivityKey: "replay:paged:wait", ActivityType: "approval", ActivityStatus: models.AutomationActivityWaiting,
		EventKey: "replay:paged:waiting", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, "replay:paged:entered", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, "replay:paged:waiting", time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)))

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
	t.Run("isolated", func(t *testing.T) { testAutomationHistoryActivityCursorIsStableAndCollectionBound(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationHistoryActivityCursorIsStableAndCollectionBound(t, teststores.WithProductionTopology())
	})
}

func testAutomationHistoryActivityCursorIsStableAndCollectionBound(t *testing.T, opts ...teststores.Option) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC, opts...)
	ctx := context.Background()
	invocation := createHistoryInvocation(t, fixture, "activity-pages", "completed")
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: producer.ID}
	for i := 0; i < 3; i++ {
		_, activity, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: "activity:paged", ActivityKey: fmt.Sprintf("activity:paged:%d", i), ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
			EventKey: fmt.Sprintf("activity:paged:entered:%d", i), ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
		})
		require.NoError(t, err)
		require.NoError(t, fixture.stores.Fixtures.SetAutomationActivityStart(ctx, activity.ID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
		require.NoError(t, fixture.stores.Fixtures.SetAutomationTransitionTime(ctx, fixture.definition.Automation.ID, fmt.Sprintf("activity:paged:entered:%d", i), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	}

	first, err := fixture.repo.ListAutomationActivities(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, "")
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	second, err := fixture.repo.ListAutomationActivities(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, first.NextCursor)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	_, err = fixture.repo.ListAutomationTransitions(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, first.NextCursor)
	require.ErrorIs(t, err, persistence.ErrAutomationCursor)

	transitionFirst, err := fixture.repo.ListAutomationTransitions(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, "")
	require.NoError(t, err)
	require.Len(t, transitionFirst.Items, 2)
	require.NotEmpty(t, transitionFirst.NextCursor)
	transitionSecond, err := fixture.repo.ListAutomationTransitions(ctx, fixture.project.ID, fixture.definition.Automation.ID, invocation.ID, "", 2, transitionFirst.NextCursor)
	require.NoError(t, err)
	require.Len(t, transitionSecond.Items, 1)
}

func TestAutomationHistoryWorkItemPaginationIsStableAndFilterBound(t *testing.T) {
	t.Run("isolated", func(t *testing.T) { testAutomationHistoryWorkItemPaginationIsStableAndFilterBound(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationHistoryWorkItemPaginationIsStableAndFilterBound(t, teststores.WithProductionTopology())
	})
}

func testAutomationHistoryWorkItemPaginationIsStableAndFilterBound(t *testing.T, opts ...teststores.Option) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC, opts...)
	ctx := context.Background()
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: producer.ID}
	for i := 0; i < 6; i++ {
		item, _, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: fmt.Sprintf("work-item:paged:%d", i), WorkItemKind: "test", ActivityKey: fmt.Sprintf("work-item:paged:%d:create", i), ActivityType: "producer", ActivityStatus: models.AutomationActivityRunning,
		})
		require.NoError(t, err)
		require.NoError(t, fixture.stores.Fixtures.SetAutomationWorkItemHistory(ctx, item.ID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), i >= 4))
	}

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
	require.ErrorIs(t, err, persistence.ErrAutomationCursor)
}

func TestAutomationHistoryHealthReconciliationCoversEverySavedAutomationBeyondOneBatch(t *testing.T) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
	ctx := t.Context()
	ids := []string{fixture.definition.Automation.ID}
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("health-batch-automation-%03d", i)
		versionID := fmt.Sprintf("health-batch-version-%03d", i)
		require.NoError(t, fixture.stores.Fixtures.SeedAutomation(ctx, models.Automation{ID: id, ProjectID: fixture.project.ID, StableKey: id, Name: fmt.Sprintf("Health batch %03d", i), AutomationType: "custom", LifecycleState: "active", CreatedVia: "web", PublishedVersionID: &versionID}))
		require.NoError(t, fixture.stores.Fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: versionID, ProjectID: fixture.project.ID, AutomationID: id, Version: 1, State: "published", Source: "manual", AdapterKey: "custom", PublishedAt: &now}))
		ids = append(ids, id)
	}
	for _, id := range ids {
		require.NoError(t, fixture.stores.Fixtures.SetAutomationHealthEvaluationTime(ctx, id, nil))
	}
	require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now, 100))
	cards, err := fixture.repo.ListPortfolioCards(ctx, fixture.project.ID)
	require.NoError(t, err)
	require.Len(t, cards, len(ids))
	require.Greater(t, len(cards), 100)
	for _, card := range cards {
		definition, err := fixture.repo.GetDefinition(ctx, fixture.project.ID, card.Automation.ID)
		require.NoError(t, err)
		require.NotNil(t, definition)
		require.NotNil(t, definition.Automation.PublishedVersionID)
		require.NotNil(t, definition.Automation.HealthEvaluatedAt, "one reconciliation pass must not starve saved Automations beyond the first batch: %s", card.Automation.ID)
	}
}

func TestAutomationHistoryHealthIgnoresStaleFailureAfterRecentSuccesses(t *testing.T) {
	fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)

	health, err := fixture.repo.RecomputeAutomationHealth(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Equal(t, models.AutomationHealthUnknown, health.State)

	staleFailure := createHistoryInvocation(t, fixture, "stale-failure", "failed")
	require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationCompletionTime(ctx, staleFailure.ID, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)))
	for i := 0; i < 3; i++ {
		completed := createHistoryInvocation(t, fixture, fmt.Sprintf("recent-success-%d", i), "completed")
		require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationCompletionTime(ctx, completed.ID, now.Add(time.Duration(i)*time.Minute)))
	}

	health, err = fixture.repo.RecomputeAutomationHealth(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Equal(t, models.AutomationHealthHealthy, health.State)
	require.Equal(t, models.AutomationActive, storedAutomation(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID).LifecycleState)
}

func TestAutomationHistoryHealthStableNoChangeSkipsAutomationUpdatesAndReducesSQL(t *testing.T) {
	for _, count := range []int{100, 500} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			db, counter := testutil.NewStatementCountingTestDB(t)
			repo := teststores.Of(db).Automations
			ctx := context.Background()
			now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
			fixture := seedAutomationHealthReconciliationFixture(t, db, count, now)

			require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, now, 100))
			counter.Reset()
			counter.SetEnabled(true)
			require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, now.Add(time.Minute), 100))
			counter.SetEnabled(false)

			statements := counter.Statements()
			updates := countAutomationHealthUpdates(statements)
			baselineStatements := legacyAutomationHealthStatementCount(count)
			require.Zero(t, updates, "stable published Automations must not issue unchanged UPDATE automations statements")
			require.LessOrEqual(t, len(statements), baselineStatements/10, "stable health SQL should drop by at least 90%% versus the former per-Automation path")
			requireAutomationHealthEvaluatedCount(t, db, fixture.projectID, count)
			requireAutomationHealthUnevaluated(t, repo, fixture.projectID, fixture.unpublishedAutomationID)
		})
	}
}

func TestAutomationHistoryHealthChangedInputsPersistAffectedRecords(t *testing.T) {
	t.Run("blocked position", func(t *testing.T) {
		fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
		ctx := context.Background()
		now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
		completed := createHistoryInvocation(t, fixture, "blocked-baseline", "completed")
		require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationCompletionTime(ctx, completed.ID, now))
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now, 100))
		requireAutomationHealthState(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID, models.AutomationHealthHealthy, "Recent triggers")

		producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
		gate := automationNodeByKey(t, fixture.definition, "approval")
		binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: completed.ID, NodeID: producer.ID}
		item, _, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: "health:blocked", ActivityKey: "health:blocked:create", ActivityType: "producer", ActivityStatus: models.AutomationActivityCompleted,
			EventKey: "health:blocked:create", ToNodeID: producer.ID, Transition: models.AutomationTransitionEntered,
		})
		require.NoError(t, err)
		binding.WorkItemID, binding.NodeID = item.ID, gate.ID
		_, _, err = fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: "health:blocked", ActivityKey: "health:blocked:gate", ActivityType: "approval", ActivityStatus: models.AutomationActivityFailed,
			EventKey: "health:blocked:gate", FromNodeID: producer.ID, ToNodeID: gate.ID, Transition: models.AutomationTransitionBlocked,
		})
		require.NoError(t, err)

		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now.Add(time.Minute), 100))
		requireAutomationHealthState(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID, models.AutomationHealthDegraded, "1 blocked or failed position")
	})

	t.Run("recent failure", func(t *testing.T) {
		fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
		ctx := context.Background()
		now := time.Date(2026, time.August, 2, 13, 0, 0, 0, time.UTC)
		completed := createHistoryInvocation(t, fixture, "failure-baseline", "completed")
		require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationCompletionTime(ctx, completed.ID, now))
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now, 100))
		requireAutomationHealthState(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID, models.AutomationHealthHealthy, "Recent triggers")

		failed := createHistoryInvocation(t, fixture, "fresh-failure", "failed")
		require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationCompletionTime(ctx, failed.ID, now.Add(time.Minute)))
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now.Add(2*time.Minute), 100))
		requireAutomationHealthState(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID, models.AutomationHealthDegraded, "1 recent failed invocation")
	})

	t.Run("stale external state", func(t *testing.T) {
		fixture := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
		ctx := context.Background()
		now := time.Date(2026, time.August, 2, 14, 0, 0, 0, time.UTC)
		completed := createHistoryInvocation(t, fixture, "external-baseline", "completed")
		require.NoError(t, fixture.stores.Fixtures.SetAutomationInvocationCompletionTime(ctx, completed.ID, now))
		attachAutomationHealthPullRequest(t, fixture, completed.ID, now)
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now, 100))
		requireAutomationHealthState(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID, models.AutomationHealthHealthy, "Recent triggers")

		require.NoError(t, fixture.stores.Fixtures.SetTaskPullRequestUpdatedAt(ctx, fixture.task.ID, now.Add(-2*persistence.AutomationExternalStaleAfter)))
		externalState, err := fixture.repo.AutomationExternalState(ctx, fixture.project.ID, fixture.definition.Automation.ID, now.Add(time.Minute).Add(-persistence.AutomationExternalStaleAfter))
		require.NoError(t, err)
		require.Equal(t, 1, externalState.TrackedResources)
		require.True(t, externalState.Stale)
		require.NoError(t, fixture.repo.RecomputeAutomationHealthForAll(ctx, now.Add(time.Minute), 100))
		requireAutomationHealthState(t, fixture.repo, fixture.project.ID, fixture.definition.Automation.ID, models.AutomationHealthDegraded, "external GitHub state is stale")
	})

	t.Run("publication", func(t *testing.T) {
		db := teststores.New(t)
		repo := db.Automations
		ctx := context.Background()
		project := automationTestProject(t, db.Projects, "Health publication")
		automationID, versionID := "health-publication-automation", "health-publication-version"
		require.NoError(t, db.Fixtures.SeedAutomation(ctx, models.Automation{ID: automationID, ProjectID: project.ID, StableKey: automationID, Name: automationID, AutomationType: "custom", LifecycleState: "active", CreatedVia: "web"}))
		now := time.Now().UTC()
		require.NoError(t, db.Fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: versionID, ProjectID: project.ID, AutomationID: automationID, Version: 1, State: "published", Source: "manual", AdapterKey: "custom", PublishedAt: &now}))
		for _, n := range []models.AutomationNode{{ID: automationID + "-trigger", NodeKey: "trigger", Name: "Trigger", NodeType: "trigger", Role: "trigger"}, {ID: automationID + "-gate", NodeKey: "gate", Name: "Gate", NodeType: "human_gate", Role: "approval"}} {
			n.ProjectID = project.ID
			n.AutomationID = automationID
			n.VersionID = versionID
			require.NoError(t, db.Fixtures.SeedAutomationNode(ctx, n))
		}
		require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, time.Date(2026, time.August, 2, 15, 0, 0, 0, time.UTC), 100))
		requireAutomationHealthUnevaluated(t, repo, project.ID, automationID)

		require.NoError(t, db.Fixtures.SetAutomationPublishedVersion(ctx, automationID, versionID))
		require.NoError(t, repo.RecomputeAutomationHealthForAll(ctx, time.Date(2026, time.August, 2, 15, 1, 0, 0, time.UTC), 100))
		requireAutomationHealthState(t, repo, project.ID, automationID, models.AutomationHealthUnknown, "No terminal invocation yet")
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
	projectRepo := teststores.Of(db).Projects
	project := models.Project{Name: fmt.Sprintf("Health reconciliation %d", automationCount)}
	require.NoError(tb, projectRepo.Create(ctx, &project))
	taskRepo := teststores.Of(db).Tasks
	pullRepo := teststores.Of(db).TaskPullRequests
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
	ctx := t.Context()
	pull := models.TaskPullRequest{TaskID: fixture.task.ID, PRNumber: 77, PRURL: "https://github.com/openvibely/openvibely/pull/77", PRState: "open"}
	require.NoError(t, fixture.stores.TaskPullRequests.Upsert(ctx, &pull))
	trigger := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	activityID := fixture.definition.Automation.ID + "-activity-077"
	require.NoError(t, fixture.stores.Fixtures.SeedAutomationActivity(ctx, models.AutomationActivity{ID: activityID, ProjectID: fixture.project.ID, AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: trigger.ID, InvocationID: invocationID, ActivityKey: activityID, ActivityType: "pull_request", Status: models.AutomationActivityCompleted}, []models.AutomationActivityResource{{ResourceType: "task", ResourceID: fixture.task.ID, Relation: "subject"}, {ResourceType: "pull_request", ResourceID: pull.ID, Relation: "subject"}}))

	require.NoError(t, fixture.stores.Fixtures.SetTaskPullRequestUpdatedAt(ctx, fixture.task.ID, updatedAt))
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

func storedAutomation(t *testing.T, repo persistence.AutomationStore, projectID, automationID string) models.Automation {
	t.Helper()
	definition, err := repo.GetDefinition(context.Background(), projectID, automationID)
	require.NoError(t, err)
	require.NotNil(t, definition)
	return definition.Automation
}

func requireAutomationHealthUnevaluated(t *testing.T, repo persistence.AutomationStore, projectID, automationID string) {
	t.Helper()
	require.Nil(t, storedAutomation(t, repo, projectID, automationID).HealthEvaluatedAt)
}

func requireAutomationHealthState(t *testing.T, repo persistence.AutomationStore, projectID, automationID string, expected models.AutomationHealthState, reasonContains string) {
	t.Helper()
	automation := storedAutomation(t, repo, projectID, automationID)
	require.Equal(t, expected, automation.HealthState)
	require.Contains(t, automation.HealthReason, reasonContains)
	require.NotNil(t, automation.HealthEvaluatedAt)
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
	measure func(testing.TB, *sql.DB, persistence.AutomationStore, time.Time) error
}

func measureAutomationHealthReconciliation(tb testing.TB, db *sql.DB, repo persistence.AutomationStore, counter *testutil.SQLStatementCounter, now time.Time, measure func(testing.TB, *sql.DB, persistence.AutomationStore, time.Time) error) automationHealthMeasurement {
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

func measureCurrentAutomationHealthPath(tb testing.TB, _ *sql.DB, repo persistence.AutomationStore, now time.Time) error {
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
				repo := teststores.Of(db).Automations
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
				repo := teststores.Of(db).Automations
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
