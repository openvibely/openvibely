package teststores_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestFixturesShareStoreStateWithoutApplyingTransitions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []teststores.Option
	}{
		{name: "isolated"}, {name: "production", options: []teststores.Option{teststores.WithProductionTopology()}},
	} {
		t.Run(tc.name, func(t *testing.T) { testFixtureSharing(t, tc.options...) })
	}
}
func testFixtureSharing(t *testing.T, options ...teststores.Option) {
	env := teststores.New(t, options...)
	ctx := context.Background()
	task := models.Task{ProjectID: "default", Title: "Fixture state", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := env.Tasks.Create(ctx, &task); err != nil {
		t.Fatal(err)
	}
	execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "fixture"}
	if err := env.Executions.Create(ctx, &execution); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := env.Fixtures.SetExecutionStartedAt(ctx, execution.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := env.Fixtures.SetTaskUpdatedAt(ctx, task.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := env.Fixtures.SetTaskCompletedAt(ctx, task.ID, at); err != nil {
		t.Fatal(err)
	}
	stored, err := env.Tasks.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.StatusPending || !stored.UpdatedAt.Equal(at) || stored.CompletedAt == nil || !stored.CompletedAt.Equal(at) {
		t.Fatalf("fixture timestamps or task status changed unexpectedly: %+v", stored)
	}
	run, err := env.Executions.GetByID(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.StartedAt.Equal(at) || run.Status != models.ExecRunning {
		t.Fatalf("unexpected execution state: %+v", run)
	}
	if err := env.Fixtures.SetExecutionStartedAt(ctx, "missing", at); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("missing execution: %v", err)
	}
	count, err := env.Fixtures.CountAutomations(ctx)
	if err != nil || count != 0 {
		t.Fatalf("automation count: %d, %v", count, err)
	}
	other := teststores.New(t, options...)
	absent, err := other.Tasks.GetByID(ctx, task.ID)
	if err != nil || absent != nil {
		t.Fatalf("test databases are not isolated: %v, %v", absent, err)
	}
}

func TestIOCounterMeasuresBackendRequestsWithinStoreCalls(t *testing.T) {
	env := teststores.New(t, teststores.WithIOCounting())
	ctx := context.Background()
	task := models.Task{ProjectID: "default", Title: "Measured task", Category: models.CategoryBacklog, Status: models.StatusPending}
	env.IO.Reset()
	env.IO.SetEnabled(true)
	if err := env.Tasks.Create(ctx, &task); err != nil {
		t.Fatal(err)
	}
	env.IO.SetEnabled(false)
	if got := env.IO.Snapshot(); got.Requests <= 1 {
		t.Fatalf("counter must capture the multiple backend requests within Create, got %+v", got)
	}
	env.IO.Reset()
	env.IO.SetEnabled(true)
	if _, err := env.Tasks.GetByID(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	env.IO.SetEnabled(false)
	measured := env.IO.Snapshot()
	if measured.Requests != 1 || measured.ReadTextBytes < len(task.Title) {
		t.Fatalf("expected one actual read with measured payload, got %+v", measured)
	}
	if _, err := env.Tasks.GetByID(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if got := env.IO.Snapshot(); got != measured {
		t.Fatalf("disabled counter changed: %+v", got)
	}
	env.IO.Reset()
	if got := env.IO.Snapshot(); got.Requests != 0 || got.ReadTextBytes != 0 {
		t.Fatalf("reset retained measurements: %+v", got)
	}
}

func TestSkillEventFixturePreservesIdentityAndFilters(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	project := models.Project{Name: "Other skill event project"}
	if err := env.Projects.Create(ctx, &project); err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []string{"default", project.ID} {
		event := models.SkillAnalyticsEvent{ProjectID: projectID, ThreadID: "hook-execution", SkillHandle: "observe", SkillScope: models.SkillScopeAgentOwned, EventType: models.SkillEventSelected, Source: models.SkillEventSourceLifecycleHook, Surface: models.SkillSurfaceLifecycleHook}
		if err := env.SkillAnalytics.RecordEvent(ctx, &event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := env.Fixtures.SkillEvents(ctx, storefixture.SkillEventFilter{ProjectID: project.ID, SkillHandle: "observe", EventType: models.SkillEventSelected, Source: models.SkillEventSourceLifecycleHook, Surface: models.SkillSurfaceLifecycleHook, SkillScope: models.SkillScopeAgentOwned})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ProjectID != project.ID || events[0].ThreadID != "hook-execution" || events[0].ID == "" {
		t.Fatalf("unexpected persisted skill evidence: %+v", events)
	}
	absent, err := env.Fixtures.SkillEvents(ctx, storefixture.SkillEventFilter{ProjectID: project.ID, SkillHandle: "missing"})
	if err != nil || len(absent) != 0 {
		t.Fatalf("missing event filter: %+v, %v", absent, err)
	}
}

func TestRetainedScheduleFixtureRollsBackAndSharesStoreState(t *testing.T) {
	for _, production := range []bool{false, true} {
		t.Run(map[bool]string{false: "isolated", true: "production"}[production], func(t *testing.T) {
			var options []teststores.Option
			if production {
				options = append(options, teststores.WithProductionTopology())
			}
			env := teststores.New(t, options...)
			ctx := context.Background()
			a := models.Automation{ID: "retained", ProjectID: "default", StableKey: "retained", Name: "Retained", AutomationType: "custom", LifecycleState: "active"}
			require.NoError(t, env.Fixtures.SeedAutomation(ctx, a))
			v := models.AutomationVersion{ID: "retained-version", ProjectID: a.ProjectID, AutomationID: a.ID, Version: 2, State: "draft", Source: "bootstrap", AdapterKey: "custom"}
			require.NoError(t, env.Fixtures.SeedAutomationVersion(ctx, v))
			n := models.AutomationNode{ID: "retained-trigger", ProjectID: a.ProjectID, AutomationID: a.ID, VersionID: v.ID, NodeKey: "schedule", Name: "Schedule", NodeType: "trigger", Role: "schedule"}
			require.Error(t, env.Fixtures.SeedRetainedScheduleOwnership(ctx, n, "missing-schedule"))
			graph, err := env.Automations.GetDefinitionVersion(ctx, a.ProjectID, a.ID, v.ID)
			require.NoError(t, err)
			require.NotNil(t, graph)
			require.Empty(t, graph.Nodes)
			require.Empty(t, graph.Resources)
			task := models.Task{ProjectID: a.ProjectID, Title: "retained", Category: models.CategoryScheduled, Status: models.StatusPending}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatOnce, RepeatInterval: 1}
			require.NoError(t, env.Schedules.Create(ctx, &schedule))
			require.NoError(t, env.Fixtures.SeedRetainedScheduleOwnership(ctx, n, schedule.ID))
			owner, err := env.Automations.GetTriggerOwner(ctx, schedule.ID)
			require.NoError(t, err)
			require.NotNil(t, owner)
			require.Equal(t, v.ID, owner.VersionID)
			require.Equal(t, n.ID, owner.NodeID)
			graph, err = env.Automations.GetDefinitionVersion(ctx, a.ProjectID, a.ID, v.ID)
			require.NoError(t, err)
			require.Len(t, graph.Nodes, 1)
			require.Len(t, graph.Resources, 1)
			require.Equal(t, schedule.ID, graph.Resources[0].ResourceID)
		})
	}
}
