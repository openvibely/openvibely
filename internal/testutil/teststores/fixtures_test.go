package teststores_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
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

func TestHistoryIOCounterMeasuresPhysicalReads(t *testing.T) {
	env := teststores.New(t, teststores.WithIOCounting())
	ctx := context.Background()
	env.IO.SetEnabled(true)
	_, err := env.Executions.ListByTaskChronologicalLimit(ctx, "missing-task", 20)
	require.NoError(t, err)
	require.Equal(t, 1, env.IO.Snapshot().ExecutionHistoryReads)
	_, err = env.Executions.ListByTaskChronologicalLimit(ctx, "missing-task", 20)
	require.NoError(t, err)
	require.Equal(t, 2, env.IO.Snapshot().ExecutionHistoryReads)
	env.IO.SetEnabled(false)
	_, err = env.Executions.ListByTaskChronologicalLimit(ctx, "missing-task", 20)
	require.NoError(t, err)
	require.Equal(t, 2, env.IO.Snapshot().ExecutionHistoryReads)
	env.IO.Reset()
	require.Zero(t, env.IO.Snapshot().ExecutionHistoryReads)
}

func TestGitHubAuthorizationCounterMeasuresPhysicalReads(t *testing.T) {
	env := teststores.New(t, teststores.WithIOCounting())
	ctx := context.Background()
	require.NoError(t, env.GitHubAuth.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "alice", Permission: "triage"}))
	env.IO.Reset()
	env.IO.SetEnabled(true)
	for range 2 {
		authorized, err := env.GitHubAuth.IsActorAuthorized(ctx, "ALICE")
		require.NoError(t, err)
		require.True(t, authorized)
	}
	env.IO.SetEnabled(false)
	require.Equal(t, 2, env.IO.Snapshot().GitHubAuthorizationReads)
	require.Equal(t, 2, env.IO.Snapshot().Requests)
	_, err := env.GitHubAuth.IsActorAuthorized(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, 2, env.IO.Snapshot().GitHubAuthorizationReads)
	env.IO.Reset()
	require.Zero(t, env.IO.Snapshot().GitHubAuthorizationReads)
}

func TestRecoveryFixturesShareBackendAndRestoreIntegrity(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		var options []teststores.Option
		if production {
			name = "production"
			options = append(options, teststores.WithProductionTopology())
		}
		t.Run(name, func(t *testing.T) {
			env := teststores.New(t, options...)
			ctx := context.Background()
			project := models.Project{Name: "Legacy model reference"}
			require.NoError(t, env.Projects.Create(ctx, &project))
			require.NoError(t, env.Fixtures.SetStaleProjectDefaultModel(ctx, project.ID, "missing-model"))
			loaded, err := env.Projects.GetByID(ctx, project.ID)
			require.NoError(t, err)
			require.NotNil(t, loaded)
			require.NotNil(t, loaded.DefaultAgentConfigID)
			require.Equal(t, "missing-model", *loaded.DefaultAgentConfigID)
			orphan := models.Task{ProjectID: "missing-project", Title: "Must reject", Category: models.CategoryBacklog, Status: models.StatusPending}
			require.Error(t, env.Tasks.Create(ctx, &orphan), "fixture must restore referential integrity")
			task := models.Task{ProjectID: project.ID, Title: "Retry worker", Category: models.CategoryBacklog, Status: models.StatusPending}
			require.NoError(t, env.Fixtures.SetTaskCreationFailure(ctx, task.Title, true))
			require.ErrorContains(t, env.Tasks.Create(ctx, &task), "forced task creation failure")
			saved, err := env.Tasks.ListByProject(ctx, project.ID, "")
			require.NoError(t, err)
			require.Empty(t, saved)
			require.NoError(t, env.Fixtures.SetTaskCreationFailure(ctx, task.Title, false))
			require.NoError(t, env.Tasks.Create(ctx, &task))
			saved, err = env.Tasks.ListByProject(ctx, project.ID, "")
			require.NoError(t, err)
			require.Len(t, saved, 1)
		})
	}
}

func TestTelegramAuthorizationFixturesAndPhysicalReads(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			options := []teststores.Option{teststores.WithIOCounting()}
			if production {
				options = append(options, teststores.WithProductionTopology())
			}
			env := teststores.New(t, options...)
			ctx := t.Context()
			require.NoError(t, env.Fixtures.SeedTelegramAuthorizedUsers(ctx, []models.TelegramAuthorizedUser{{ProjectID: "default", TelegramUserID: 71, DisplayName: "authorized", AddedBy: "test"}}))
			env.IO.Reset()
			env.IO.SetEnabled(true)
			for i := 0; i < 2; i++ {
				allowed, err := env.TelegramAuth.IsAuthorized(ctx, "default", 71, "")
				require.NoError(t, err)
				require.True(t, allowed)
			}
			env.IO.SetEnabled(false)
			measured := env.IO.Snapshot()
			require.Equal(t, 2, measured.TelegramAuthorizationReads)
			require.Equal(t, 2, measured.Requests)
			_, err := env.TelegramAuth.IsAuthorized(ctx, "default", 71, "")
			require.NoError(t, err)
			require.Equal(t, measured, env.IO.Snapshot())
			env.IO.Reset()
			require.Zero(t, env.IO.Snapshot().TelegramAuthorizationReads)
			other := teststores.New(t)
			allowed, err := other.TelegramAuth.IsAuthorized(ctx, "default", 71, "")
			require.NoError(t, err)
			require.False(t, allowed)
		})
	}
}

func TestSettingsAndProjectIOCountsPhysicalReads(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			require.NoError(t, env.Settings.SetMany(ctx, map[string]string{"first": "one", "second": "two"}))
			env.IO.SetEnabled(true)
			for i := 0; i < 2; i++ {
				values, err := env.Settings.GetMany(ctx, []string{"first", "second"})
				require.NoError(t, err)
				require.Equal(t, "one", values["first"])
			}
			_, err := env.Projects.List(ctx)
			require.NoError(t, err)
			env.IO.SetEnabled(false)
			measured := env.IO.Snapshot()
			require.Equal(t, 2, measured.SettingsSnapshotReads)
			require.Equal(t, 1, measured.ProjectCatalogReads)
			require.Equal(t, 3, measured.Requests)
			_, err = env.Projects.List(ctx)
			require.NoError(t, err)
			require.Equal(t, measured, env.IO.Snapshot())
			env.IO.Reset()
			require.Zero(t, env.IO.Snapshot())
		})
	}
}

func TestProjectDeletionFaultRollsBackAndCanBeRemoved(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var options []teststores.Option
			if production {
				options = append(options, teststores.WithProductionTopology())
			}
			env := teststores.New(t, options...)
			ctx := t.Context()
			project := &models.Project{Name: "Deletion rollback"}
			require.NoError(t, env.Projects.Create(ctx, project))
			task := &models.Task{ProjectID: project.ID, Title: "Retained task", Category: models.CategoryBacklog, Status: models.StatusPending}
			require.NoError(t, env.Tasks.Create(ctx, task))
			require.NoError(t, env.Fixtures.SetProjectDeletionFailure(ctx, project.ID, true))
			_, deleted, err := env.Projects.DeleteWithCleanupManifest(ctx, project.ID, nil)
			require.Error(t, err)
			require.False(t, deleted)
			saved, err := env.Tasks.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.NotNil(t, saved)
			require.NoError(t, env.Fixtures.SetProjectDeletionFailure(ctx, project.ID, false))
			_, deleted, err = env.Projects.DeleteWithCleanupManifest(ctx, project.ID, nil)
			require.NoError(t, err)
			require.True(t, deleted)
			saved, err = env.Tasks.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Nil(t, saved)
		})
	}
}

func TestSettingsPhysicalReadCounters(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			options := []teststores.Option{teststores.WithIOCounting()}
			if production {
				options = append(options, teststores.WithProductionTopology())
			}
			env := teststores.New(t, options...)
			ctx := t.Context()
			require.NoError(t, env.Settings.Set(ctx, "counter-first", "first"))
			require.NoError(t, env.Settings.Set(ctx, "counter-second", "second"))
			env.IO.Reset()
			env.IO.SetEnabled(true)
			_, err := env.Settings.GetMany(ctx, []string{"counter-first", "counter-second"})
			require.NoError(t, err)
			_, err = env.Settings.Get(ctx, "counter-first")
			require.NoError(t, err)
			env.IO.SetEnabled(false)
			stats := env.IO.Snapshot()
			require.Equal(t, 1, stats.SettingsSnapshotReads)
			require.Equal(t, 1, stats.SettingsSingleReads)
			require.Equal(t, 2, stats.Requests)
			_, err = env.Settings.Get(ctx, "counter-first")
			require.NoError(t, err)
			require.Equal(t, stats, env.IO.Snapshot())
			env.IO.Reset()
			require.Zero(t, env.IO.Snapshot().SettingsSingleReads)
		})
	}
}

func TestSlackFixtureFaultsAndPhysicalReads(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			require.NoError(t, env.Fixtures.SeedSlackAuthorizedUsers(ctx, []models.SlackAuthorizedUser{{ProjectID: "default", SlackUserID: "U1", DisplayName: "one", AddedBy: "test"}}))
			env.IO.Reset()
			env.IO.SetEnabled(true)
			for i := 0; i < 2; i++ {
				ok, err := env.SlackAuth.IsAuthorized(ctx, "default", "U1")
				require.NoError(t, err)
				require.True(t, ok)
			}
			env.IO.SetEnabled(false)
			measured := env.IO.Snapshot()
			require.Equal(t, 2, measured.Requests)
			require.Equal(t, 2, measured.SlackAuthorizationReads)
			_, err := env.SlackAuth.IsAuthorized(ctx, "default", "U1")
			require.NoError(t, err)
			require.Equal(t, measured, env.IO.Snapshot())
			env.IO.Reset()
			require.Zero(t, env.IO.Snapshot().SlackAuthorizationReads)
			require.NoError(t, env.SlackUserProjects.SetUserProject(ctx, "T1", "U1", "default"))
			require.NoError(t, env.Fixtures.SetSlackProjectSelectionUnavailable(ctx, true))
			require.Error(t, env.SlackUserProjects.SetUserProject(ctx, "T1", "U1", "default"))
			require.NoError(t, env.Fixtures.SetSlackProjectSelectionUnavailable(ctx, false))
			selected, err := env.SlackUserProjects.GetUserProject(ctx, "T1", "U1")
			require.NoError(t, err)
			require.Equal(t, "default", selected)
			require.NoError(t, env.Settings.Set(ctx, "failing-setting", "original"))
			require.NoError(t, env.Fixtures.FailSettingWrites(ctx, "failing-setting"))
			require.Error(t, env.Settings.SetMany(ctx, map[string]string{"other-setting": "changed", "failing-setting": "replacement"}))
			values, err := env.Settings.GetMany(ctx, []string{"other-setting", "failing-setting"})
			require.NoError(t, err)
			require.Empty(t, values["other-setting"])
			require.Equal(t, "original", values["failing-setting"])
			other := teststores.New(t)
			allowed, err := other.SlackAuth.IsAuthorized(ctx, "default", "U1")
			require.NoError(t, err)
			require.False(t, allowed)
		})
	}
}

func TestCatalogAndInboundFaultFixturesShareBackend(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			require.NoError(t, env.Fixtures.SeedProjects(ctx, []models.Project{{ID: "fixture-project", Name: "fixture"}}))
			project, err := env.Projects.GetByID(ctx, "fixture-project")
			require.NoError(t, err)
			require.NotNil(t, project)
			require.Error(t, env.Fixtures.SeedProjects(ctx, []models.Project{{ID: "rolled-back-project", Name: "rollback"}, {ID: project.ID, Name: "duplicate"}}))
			missing, err := env.Projects.GetByID(ctx, "rolled-back-project")
			require.NoError(t, err)
			require.Nil(t, missing)
			require.NoError(t, env.Fixtures.SetTaskPromptCreationFailure(ctx, "reject 'this'", true))
			task := models.Task{ProjectID: project.ID, Title: "retry", Prompt: "please reject 'this' now", Category: models.CategoryBacklog, Status: models.StatusPending}
			require.Error(t, env.Tasks.Create(ctx, &task))
			tasks, err := env.Tasks.ListByProject(ctx, project.ID, "")
			require.NoError(t, err)
			require.Empty(t, tasks)
			require.NoError(t, env.Fixtures.SetTaskPromptCreationFailure(ctx, "", false))
			require.NoError(t, env.Tasks.Create(ctx, &task))
			require.NoError(t, env.Fixtures.SetQueuedInputCreationFailure(ctx, "reject 'this'", true))
			input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, Content: "reject 'this'", InputMode: models.ThreadInputModeQueued}
			require.Error(t, env.ThreadInputs.CreateQueued(ctx, &input))
			require.NoError(t, env.Fixtures.SetQueuedInputCreationFailure(ctx, "", false))
			require.NoError(t, env.ThreadInputs.CreateQueued(ctx, &input))
			stored, err := env.ThreadInputs.GetByID(ctx, input.ID)
			require.NoError(t, err)
			require.NotNil(t, stored)
			other := teststores.New(t, opts...)
			missing, err = other.Projects.GetByID(ctx, project.ID)
			require.NoError(t, err)
			require.Nil(t, missing)
		})
	}
}

func TestContextIOCountsPhysicalReadsAndWrites(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			task := models.Task{ProjectID: "default", Title: "measured context", Category: models.CategoryBacklog, Status: models.StatusPending}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			env.IO.Reset()
			env.IO.SetEnabled(true)
			for i := 0; i < 2; i++ {
				_, err := env.Tasks.ListChatContextByProject(ctx, "default")
				require.NoError(t, err)
				_, err = env.LLMConfigs.ListChatSelectionOptions(ctx)
				require.NoError(t, err)
				require.NoError(t, env.Fixtures.SetTaskDisplayOrder(ctx, task.ID, i))
			}
			env.IO.SetEnabled(false)
			stats := env.IO.Snapshot()
			require.Equal(t, 2, stats.TaskContextReads)
			require.Equal(t, 2, stats.ModelReads)
			require.Equal(t, 2, stats.ModelSelectionReads)
			require.Equal(t, 2, stats.TaskWrites)
			_, err := env.Tasks.ListChatContextByProject(ctx, "default")
			require.NoError(t, err)
			require.Equal(t, stats, env.IO.Snapshot())
			env.IO.Reset()
			require.Zero(t, env.IO.Snapshot().TaskContextReads)
		})
	}
}

func TestChannelSelectionSnapshotSharesStoresAndIsolation(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			before, err := env.Fixtures.ChannelProjectSelections(ctx)
			require.NoError(t, err)
			require.Zero(t, before)
			require.NoError(t, env.DiscordUserProjects.SetUserProject(ctx, "discord-user", "default"))
			require.NoError(t, env.SlackUserProjects.SetUserProject(ctx, "team", "slack-user", "default"))
			require.NoError(t, env.TelegramUserProjects.SetUserProject(ctx, "71", "default"))
			require.NoError(t, env.EmailSenderProjects.SetSenderProject(ctx, "test@example.com", "default"))
			counts, err := env.Fixtures.ChannelProjectSelections(ctx)
			require.NoError(t, err)
			require.Equal(t, storefixture.ChannelProjectSelectionCounts{Discord: 1, Slack: 1, Telegram: 1, Email: 1}, counts)
			other := teststores.New(t)
			empty, err := other.Fixtures.ChannelProjectSelections(ctx)
			require.NoError(t, err)
			require.Zero(t, empty)
		})
	}
}

func TestUnreadableProjectDetailsPreserveSelectorIdentity(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			project := models.Project{Name: "Selector survives damaged details"}
			require.NoError(t, env.Projects.Create(ctx, &project))
			before, err := env.Projects.ListSelectorOptions(ctx)
			require.NoError(t, err)
			require.NoError(t, env.Fixtures.MakeProjectDetailsUnreadable(ctx, project.ID))
			_, err = env.Projects.GetByID(ctx, project.ID)
			require.Error(t, err)
			after, err := env.Projects.ListSelectorOptions(ctx)
			require.NoError(t, err)
			require.Equal(t, before, after)
			other := teststores.New(t, opts...)
			_, err = other.Projects.GetByID(ctx, "default")
			require.NoError(t, err)
		})
	}
}

func TestFailLatestActiveInvocationPreservesScopeAndTerminalHistory(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			for _, id := range []string{"target", "other"} {
				require.NoError(t, env.Fixtures.SeedAutomation(ctx, models.Automation{ID: id, ProjectID: "default", StableKey: id, Name: id, AutomationType: "custom", LifecycleState: "active"}))
				require.NoError(t, env.Fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: id + "-version", ProjectID: "default", AutomationID: id, Version: 1, State: "published", Source: "manual", AdapterKey: "custom"}))
				require.NoError(t, env.Fixtures.SeedAutomationNode(ctx, models.AutomationNode{ID: id + "-node", ProjectID: "default", AutomationID: id, VersionID: id + "-version", NodeKey: "trigger", Name: "Trigger", NodeType: "trigger", Role: "schedule"}))
			}
			for _, v := range []struct{ id, automation, status string }{{"older", "target", "running"}, {"newer", "target", "claimed"}, {"terminal", "target", "completed"}, {"foreign", "other", "running"}} {
				require.NoError(t, env.Fixtures.SeedAutomationInvocation(ctx, models.AutomationInvocation{ID: v.id, ProjectID: "default", AutomationID: v.automation, VersionID: v.automation + "-version", TriggerNodeID: v.automation + "-node", TriggerResourceType: "manual", TriggerResourceID: "test", OccurrenceKey: v.id, Status: models.AutomationInvocationStatus(v.status)}))
			}
			_, err := env.Fixtures.FailLatestActiveAutomationInvocation(ctx, "missing-project", "target")
			require.Error(t, err)
			id, err := env.Fixtures.FailLatestActiveAutomationInvocation(ctx, "default", "target")
			require.NoError(t, err)
			require.Equal(t, "newer", id)
			for _, v := range []struct{ id, automation, status string }{{"older", "target", "running"}, {"newer", "target", "failed"}, {"terminal", "target", "completed"}, {"foreign", "other", "running"}} {
				row, err := env.Automations.GetAutomationInvocation(ctx, "default", v.automation, v.id)
				require.NoError(t, err)
				require.NotNil(t, row)
				require.Equal(t, v.status, string(row.Status))
				if v.id == "newer" {
					require.NotNil(t, row.CompletedAt)
				}
			}
		})
	}
}

func TestLegacyAutomationActivityFixtureIsAtomic(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			require.NoError(t, env.Fixtures.SeedAutomation(ctx, models.Automation{ID: "history", ProjectID: "default", StableKey: "history", Name: "History", AutomationType: "custom", LifecycleState: "active"}))
			require.NoError(t, env.Fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: "version", ProjectID: "default", AutomationID: "history", Version: 1, State: "published", Source: "manual", AdapterKey: "custom"}))
			require.NoError(t, env.Fixtures.SeedAutomationNode(ctx, models.AutomationNode{ID: "node", ProjectID: "default", AutomationID: "history", VersionID: "version", NodeKey: "task", Name: "Task", NodeType: "agent_task", Role: "task", ConfigJSON: "{}"}))
			require.NoError(t, env.Fixtures.SeedAutomationInvocation(ctx, models.AutomationInvocation{ID: "invocation", ProjectID: "default", AutomationID: "history", VersionID: "version", TriggerNodeID: "node", TriggerResourceType: "manual", TriggerResourceID: "resource", OccurrenceKey: "occurrence", Status: "completed"}))
			activity := models.AutomationActivity{InvocationID: "invocation", ID: "activity", ProjectID: "default", AutomationID: "history", VersionID: "version", NodeID: "node", ActivityKey: "activity", ActivityType: "pull_request", Status: models.AutomationActivityCompleted}
			resource := models.AutomationActivityResource{ResourceType: "pull_request", ResourceID: "legacy-id", Relation: "subject"}
			require.Error(t, env.Fixtures.SeedAutomationActivity(ctx, activity, []models.AutomationActivityResource{resource, resource}))
			page, err := env.Automations.ListAutomationActivities(ctx, "default", "history", "", "", 10, "")
			require.NoError(t, err)
			require.Empty(t, page.Items)
			require.NoError(t, env.Fixtures.SeedAutomationActivity(ctx, activity, []models.AutomationActivityResource{resource}))
			page, err = env.Automations.ListAutomationActivities(ctx, "default", "history", "", "", 10, "")
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
		})
	}
}

func TestSuspendedBackendIOHonorsCancellationAndResumes(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := context.Background()
			require.NoError(t, env.Settings.Set(ctx, "suspension-test", "before"))
			release, err := env.SuspendIO(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, release()) })
			for _, write := range []bool{false, true} {
				blocked, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
				if write {
					err = env.Settings.Set(blocked, "suspension-test", "blocked")
				} else {
					_, err = env.Settings.Get(blocked, "suspension-test")
				}
				cancel()
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			// Waiting to suspend an already occupied backend must also honor cancellation.
			blocked, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
			_, err = env.SuspendIO(blocked)
			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NoError(t, release())
			require.NoError(t, release())
			value, err := env.Settings.Get(ctx, "suspension-test")
			require.NoError(t, err)
			require.Equal(t, "before", value)
			require.NoError(t, env.Settings.Set(ctx, "suspension-test", "after"))
			value, err = env.Settings.Get(ctx, "suspension-test")
			require.NoError(t, err)
			require.Equal(t, "after", value)
		})
	}
}

func TestChannelRuntimeCounterMeasuresBackendReads(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			env.IO.SetEnabled(true)
			_, err := env.LLMConfigs.ListRuntimeSummaries(ctx)
			require.NoError(t, err)
			_, err = env.LLMConfigs.GetRuntimeSummary(ctx, "", "missing")
			require.NoError(t, err)
			_, err = env.LLMConfigs.ListTaskCreationSelectionOptions(ctx)
			require.NoError(t, err)
			_, err = env.Executions.CountByTask(ctx, "missing")
			require.NoError(t, err)
			_, err = env.Executions.ListByTaskChronologicalPage(ctx, "missing", 0, 3)
			require.NoError(t, err)
			_, err = env.Projects.List(ctx)
			require.NoError(t, err)
			_, err = env.Agents.ListSelectableReferencesForProject(ctx, "default", 50)
			require.NoError(t, err)
			env.IO.SetEnabled(false)
			stats := env.IO.Snapshot()
			require.Equal(t, 1, stats.ModelRuntimeListReads)
			require.Equal(t, 1, stats.ModelRuntimeLookupReads)
			require.Equal(t, 1, stats.ModelCreationSelectionReads)
			require.Equal(t, 3, stats.ModelReads)
			require.Equal(t, 1, stats.ExecutionCountReads)
			require.Equal(t, 1, stats.ExecutionPageReads)
			require.Equal(t, 1, stats.ProjectReads)
			require.Equal(t, 1, stats.AgentReads)
			require.Equal(t, 7, stats.Requests)
		})
	}
}

func TestProjectAutomationStateIncludesUnpublishedAndChatResources(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			project := models.Project{Name: "state scope"}
			require.NoError(t, env.Projects.Create(ctx, &project))
			empty, err := env.Fixtures.ProjectAutomationState(ctx, project.ID)
			require.NoError(t, err)
			require.Equal(t, storefixture.ProjectAutomationState{}, empty)
			require.NoError(t, env.Fixtures.SeedAutomation(ctx, models.Automation{ID: "unpublished", ProjectID: project.ID, StableKey: "unpublished", Name: "Unpublished", AutomationType: "custom", LifecycleState: "active"}))
			task := models.Task{ProjectID: project.ID, Title: "chat", Category: models.CategoryChat, Status: models.StatusPending, Priority: 2}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatOnce, RepeatInterval: 1}
			require.NoError(t, env.Schedules.Create(ctx, &schedule))
			got, err := env.Fixtures.ProjectAutomationState(ctx, project.ID)
			require.NoError(t, err)
			require.Equal(t, storefixture.ProjectAutomationState{Automations: 1, Tasks: 1, Schedules: 1}, got)
			foreign, err := env.Fixtures.ProjectAutomationState(ctx, "missing")
			require.NoError(t, err)
			require.Equal(t, empty, foreign)
			require.NoError(t, env.Tasks.Delete(ctx, task.ID))
			got, err = env.Fixtures.ProjectAutomationState(ctx, project.ID)
			require.NoError(t, err)
			require.Equal(t, storefixture.ProjectAutomationState{Automations: 1}, got)
		})
	}
}

func TestAutomationFixtureOriginAndLifecycleScopes(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			project := models.Project{Name: "Other scope"}
			require.NoError(t, env.Projects.Create(ctx, &project))
			for _, pid := range []string{"default", project.ID} {
				task := models.Task{ProjectID: pid, Title: "origin", Category: models.CategoryBacklog, Status: models.StatusPending}
				require.NoError(t, env.Tasks.Create(ctx, &task))
				require.NoError(t, env.Fixtures.SetTaskCreationOrigin(ctx, task.ID, "legacy-origin"))
				stored, err := env.Tasks.GetByID(ctx, task.ID)
				require.NoError(t, err)
				require.Equal(t, "legacy-origin", stored.CreatedVia)
			}
			n, err := env.Fixtures.CountTasksByCreationOrigin(ctx, "", "legacy-origin")
			require.NoError(t, err)
			require.Equal(t, 2, n)
			n, err = env.Fixtures.CountTasksByCreationOrigin(ctx, project.ID, "legacy-origin")
			require.NoError(t, err)
			require.Equal(t, 1, n)
			a := models.Automation{ID: "lifecycle-snapshot", ProjectID: "default", StableKey: "snapshot", Name: "Snapshot", AutomationType: "custom", LifecycleState: models.AutomationActive}
			require.NoError(t, env.Fixtures.SeedAutomation(ctx, a))
			require.NoError(t, env.Fixtures.SetAutomationLifecycleSnapshot(ctx, project.ID, a.ID, models.AutomationPaused))
			stored, err := env.Automations.GetByStableKey(ctx, "default", a.StableKey)
			require.NoError(t, err)
			require.Equal(t, models.AutomationActive, stored.LifecycleState)
			require.NoError(t, env.Fixtures.SetAutomationLifecycleSnapshot(ctx, "default", a.ID, models.AutomationPaused))
			stored, err = env.Automations.GetByStableKey(ctx, "default", a.StableKey)
			require.NoError(t, err)
			require.Equal(t, models.AutomationPaused, stored.LifecycleState)
		})
	}
}

func TestTaskDiscoveryWorkloadPreservesPayloadAndOrdering(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			other := models.Project{Name: "Other discovery project"}
			require.NoError(t, env.Projects.Create(ctx, &other))
			for _, projectID := range []string{"default", other.ID} {
				require.NoError(t, env.Fixtures.SeedTaskDiscoveryWorkload(ctx, storefixture.TaskDiscoveryWorkload{ProjectID: projectID, Count: 40, PromptBytes: 1024, ConfigurationBytes: 128}))
				rows, total, err := env.Tasks.ListTasksForDiscovery(ctx, projectID, persistence.TaskDiscoveryFilter{Limit: 50})
				require.NoError(t, err)
				require.Equal(t, 38, total)
				require.Len(t, rows, 38)
				require.Equal(t, "Discovery benchmark task 00039", rows[0].Title)
				for i, row := range rows {
					require.Equal(t, models.CategoryActive, row.Category)
					if i > 0 {
						require.True(t, rows[i-1].UpdatedAt.After(row.UpdatedAt))
					}
					full, err := env.Tasks.GetByID(ctx, row.ID)
					require.NoError(t, err)
					require.Equal(t, projectID, full.ProjectID)
					require.Len(t, full.Prompt, 1024)
					require.Len(t, full.ChainConfig, 142)
					require.Len(t, full.SwarmConfig, 142)
				}
			}
		})
	}
}

func TestExecutionHistoryFixturePersistsExactStateAndRollsBack(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			task := models.Task{ProjectID: "default", Title: "History fixture", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			agent := models.LLMConfig{Name: "History model", Provider: models.ProviderTest, Model: "test"}
			require.NoError(t, env.LLMConfigs.Create(ctx, &agent))
			started := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
			completed := started.Add(time.Second)
			entry := storefixture.ExecutionHistoryEntry{ID: "history-valid", TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecFailed, PromptSent: "prompt", Output: "output", ErrorMessage: "failure", TokensUsed: 64, DurationMs: 1500, StartedAt: started, CompletedAt: &completed}
			invalid := entry
			invalid.ID = "history-invalid"
			invalid.TaskID = "missing-task"
			require.Error(t, env.Fixtures.SeedExecutionHistory(ctx, []storefixture.ExecutionHistoryEntry{entry, invalid}))
			rows, err := env.Executions.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Empty(t, rows)
			require.NoError(t, env.Fixtures.SeedExecutionHistory(ctx, []storefixture.ExecutionHistoryEntry{entry}))
			got, err := env.Executions.GetByID(ctx, entry.ID)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, entry.Status, got.Status)
			require.Equal(t, entry.PromptSent, got.PromptSent)
			require.Equal(t, entry.Output, got.Output)
			require.Equal(t, entry.ErrorMessage, got.ErrorMessage)
			require.Equal(t, entry.TokensUsed, got.TokensUsed)
			require.Equal(t, entry.DurationMs, got.DurationMs)
			require.True(t, started.Equal(got.StartedAt))
			require.NotNil(t, got.CompletedAt)
			require.True(t, completed.Equal(*got.CompletedAt))
			stored, err := env.Tasks.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, models.StatusPending, stored.Status)
		})
	}
}

func TestHistoryIOCountersDistinguishBoundedAndUnboundedReads(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			task := models.Task{ProjectID: "default", Title: "History instrumentation", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			before, err := env.Tasks.GetByID(ctx, task.ID)
			require.NoError(t, err)
			payload := storefixture.TaskPayload{Prompt: "changed instructions", ChainConfig: "legacy chain", SwarmConfig: "legacy swarm"}
			require.NoError(t, env.Fixtures.SetTaskPayload(ctx, task.ID, payload))
			after, err := env.Tasks.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, payload.Prompt, after.Prompt)
			require.Equal(t, payload.ChainConfig, after.ChainConfig)
			require.Equal(t, payload.SwarmConfig, after.SwarmConfig)
			require.Equal(t, before.UpdatedAt, after.UpdatedAt)
			require.Equal(t, before.Status, after.Status)
			for _, bounded := range []bool{true, false} {
				env.IO.Reset()
				env.IO.SetEnabled(true)
				if bounded {
					_, err = env.Executions.ListByTaskChronologicalLimit(ctx, task.ID, 3)
				} else {
					_, err = env.Executions.ListByTaskChronological(ctx, task.ID)
				}
				env.IO.SetEnabled(false)
				require.NoError(t, err)
				stats := env.IO.Snapshot()
				require.Equal(t, 1, stats.Requests)
				if bounded {
					require.Equal(t, 1, stats.ExecutionWindowReads)
					require.Zero(t, stats.UnboundedExecutionHistoryReads)
				} else {
					require.Equal(t, 1, stats.UnboundedExecutionHistoryReads)
					require.Zero(t, stats.ExecutionWindowReads)
				}
			}
		})
	}
}

func TestModelIOCountersDistinguishCompactAndFullCatalogs(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := context.Background()
			for _, tc := range []struct {
				name                    string
				read                    func(context.Context) ([]models.LLMConfig, error)
				picker, selection, full int
			}{
				{"picker", env.LLMConfigs.ListPickerOptions, 1, 0, 0},
				{"selection", env.LLMConfigs.ListChatSelectionOptions, 0, 1, 0},
				{"full", env.LLMConfigs.List, 0, 0, 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					env.IO.Reset()
					env.IO.SetEnabled(true)
					_, err := tc.read(ctx)
					env.IO.SetEnabled(false)
					require.NoError(t, err)
					stats := env.IO.Snapshot()
					require.Equal(t, 1, stats.Requests)
					require.Equal(t, 1, stats.ModelCatalogReads)
					require.Equal(t, tc.picker, stats.ModelPickerReads)
					require.Equal(t, tc.selection, stats.ModelSelectionReads)
					require.Equal(t, tc.full, stats.ModelFullCatalogReads)
				})
			}
		})
	}
}

func TestContentionObservationsUsePhysicalBackendCapacity(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.Equal(t, 1, env.Contention().Reads.Capacity)
			require.Equal(t, 1, env.Contention().Writes.Capacity)
			requested, acquired := 0, 0
			env.ObserveSettingsReads(func() { requested++ }, func() { acquired++ })
			_, err := env.Settings.Get(ctx, "fixture_missing_setting")
			require.NoError(t, err)
			require.Equal(t, 1, requested)
			require.Equal(t, 1, acquired)
			env.ObserveSettingsReads(nil, nil)
			release, err := env.HoldProjectRead(ctx)
			require.NoError(t, err)
			defer release()
			require.Equal(t, 1, env.Contention().Reads.InUse)
			before := env.Contention().Reads
			finished := make(chan error, 1)
			go func() { _, err := env.Projects.GetIdentityByID(ctx, "default"); finished <- err }()
			require.Eventually(t, func() bool { return env.Contention().Reads.WaitCount > before.WaitCount }, time.Second, time.Millisecond)
			select {
			case err := <-finished:
				t.Fatalf("competing read completed before release: %v", err)
			default:
			}
			require.NoError(t, release())
			require.NoError(t, <-finished)
			require.Greater(t, env.Contention().Reads.WaitDuration, before.WaitDuration)
			calls := 0
			env.IO.ObserveRequests(func(context.Context) { calls++ })
			_, err = env.Projects.GetIdentityByID(ctx, "default")
			require.NoError(t, err)
			env.IO.ObserveRequests(nil)
			require.Equal(t, 1, calls, "observation is active at the driver even without counting enabled")
		})
	}
}

func TestBulkHistoryFixturesShareStateAndRollbackInvalidReferences(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := context.Background()
			require.NoError(t, env.Fixtures.SeedPendingTasks(ctx, []storefixture.PendingTask{{ID: "fixture-pending", ProjectID: "default", Title: "fixture pending", Category: models.CategoryActive}}))
			require.NoError(t, env.Fixtures.SeedExecutionHistory(ctx, []storefixture.ExecutionHistoryEntry{{ID: "fixture-no-model", TaskID: "fixture-pending", Status: models.ExecRunning, StartedAt: time.Now().UTC()}}))
			executions, err := env.Executions.ListByTask(ctx, "fixture-pending")
			require.NoError(t, err)
			require.Len(t, executions, 1)
			require.Empty(t, executions[0].AgentConfigID)
			err = env.Fixtures.SeedPendingTasks(ctx, []storefixture.PendingTask{{ID: "must-rollback", ProjectID: "default", Title: "must rollback", Category: models.CategoryActive}, {ID: "invalid-project", ProjectID: "missing-project", Title: "invalid", Category: models.CategoryActive}})
			require.Error(t, err)
			task, err := env.Tasks.GetByID(ctx, "must-rollback")
			require.NoError(t, err)
			require.Nil(t, task)
			_, err = env.Fixtures.SeedTaskFrequencyWorkload(ctx, "default", "missing-model", 2)
			require.Error(t, err)
			tasks, err := env.Tasks.ListByProject(ctx, "default", "")
			require.NoError(t, err)
			require.Len(t, tasks, 1, "failed workload must roll back its task as well as execution")
			model := models.LLMConfig{Name: "fixture model", Provider: models.ProviderTest, Model: "test"}
			require.NoError(t, env.LLMConfigs.Create(ctx, &model))
			ids, err := env.Fixtures.SeedTaskFrequencyWorkload(ctx, "default", model.ID, 3)
			require.NoError(t, err)
			require.Len(t, ids, 3)
			rows, err := env.Executions.GetMostFrequentTasks(ctx, "default", 0)
			require.NoError(t, err)
			require.Len(t, rows, 4)
			require.NoError(t, env.Fixtures.ClearProjects(ctx))
			projects, err := env.Projects.List(ctx)
			require.NoError(t, err)
			require.Empty(t, projects)
		})
	}
}

func TestAutomationHealthAndProjectionFixturesShareBackendState(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			a := models.Automation{ID: "inventory", ProjectID: "default", StableKey: "inventory", Name: "Inventory", AutomationType: "custom", LifecycleState: "active"}
			require.NoError(t, env.Fixtures.SeedAutomation(ctx, a))
			require.NoError(t, env.Fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: "inventory-version", ProjectID: a.ProjectID, AutomationID: a.ID, Version: 1, State: "published", Source: "manual", AdapterKey: "custom"}))
			require.NoError(t, env.Fixtures.SetAutomationPublishedVersion(ctx, a.ID, "inventory-version"))
			for _, id := range []string{"source", "target"} {
				require.NoError(t, env.Fixtures.SeedAutomationNode(ctx, models.AutomationNode{ID: id, ProjectID: a.ProjectID, AutomationID: a.ID, VersionID: "inventory-version", NodeKey: id, Name: id, NodeType: "agent_task", Role: "task", ConfigJSON: "{}"}))
			}
			require.NoError(t, env.Fixtures.SeedAutomationEdge(ctx, models.AutomationEdge{ProjectID: a.ProjectID, AutomationID: a.ID, VersionID: "inventory-version", SourceNodeID: "source", TargetNodeID: "target", EdgeKey: "next", DisplayOrder: 7, ConditionJSON: "{}"}))
			w := models.AutomationWorkItem{ID: "position-work", ProjectID: a.ProjectID, AutomationID: a.ID, OriginVersionID: "inventory-version", WorkItemKey: "position-work", Kind: "health", Title: "Position", Status: "active"}
			pos := models.AutomationWorkItemPosition{WorkItemID: w.ID, ProjectID: a.ProjectID, AutomationID: a.ID, VersionID: "inventory-version", NodeID: "source", State: models.AutomationPositionActive}
			require.NoError(t, env.Fixtures.SeedAutomationWorkItemPosition(ctx, w, pos))
			w.ID = "rollback-work"
			w.WorkItemKey = w.ID
			pos.WorkItemID = w.ID
			pos.NodeID = "missing-node"
			require.Error(t, env.Fixtures.SeedAutomationWorkItemPosition(ctx, w, pos))
			missing, err := env.Automations.GetAutomationWorkItem(ctx, a.ProjectID, a.ID, w.ID)
			require.NoError(t, err)
			require.Nil(t, missing)
			require.NoError(t, env.Fixtures.SetAutomationPositionState(ctx, "position-work", "source", models.AutomationPositionBlocked))
			health, err := env.Automations.RecomputeAutomationHealth(ctx, a.ProjectID, a.ID, time.Now().UTC())
			require.NoError(t, err)
			require.Equal(t, models.AutomationHealthDegraded, health.State)
			env.IO.Reset()
			env.IO.SetEnabled(true)
			require.NoError(t, env.Fixtures.SetAutomationHealth(ctx, a.ID, models.AutomationHealthHealthy, "observed"))
			definition, err := env.Automations.GetDefinition(ctx, a.ProjectID, a.ID)
			env.IO.SetEnabled(false)
			require.NoError(t, err)
			require.NotNil(t, definition)
			stats := env.IO.Snapshot()
			require.Equal(t, 1, stats.AutomationHealthWrites)
			require.Equal(t, 1, stats.AutomationVersionReads)
			require.Equal(t, 1, stats.AutomationDefinitionNodeReads)
			require.Equal(t, 1, stats.AutomationEdgeReads)
			require.Equal(t, 7, definition.Edges[0].DisplayOrder)
			activity := models.AutomationActivity{ID: "inventory-activity", ProjectID: a.ProjectID, AutomationID: a.ID, VersionID: "inventory-version", NodeID: "source", WorkItemID: "position-work", ActivityKey: "inventory-activity", ActivityType: "claim_notification", Status: models.AutomationActivityRunning}
			require.NoError(t, env.Fixtures.SeedAutomationActivity(ctx, activity, []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: "inventory-alert", Relation: "subject"}}))
			n, err := env.Fixtures.CountAutomationActivities(ctx, a.ID, "claim_notification", models.AutomationActivityRunning)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			for _, kind := range []string{"other", "claim_notification"} {
				n, err = env.Fixtures.CountAutomationActivities(ctx, a.ID, kind, models.AutomationActivityCompleted)
				require.NoError(t, err)
				require.Zero(t, n)
			}
			n, err = env.Fixtures.CountAutomationActivities(ctx, "foreign", "claim_notification", models.AutomationActivityRunning)
			require.NoError(t, err)
			require.Zero(t, n)
			n, err = env.Fixtures.CountAutomationWorkItems(ctx, a.ID, "position-work")
			require.NoError(t, err)
			require.Equal(t, 1, n)
			n, err = env.Fixtures.CountAutomationWorkItems(ctx, a.ID, "rollback-work")
			require.NoError(t, err)
			require.Zero(t, n)
			n, err = env.Fixtures.CountAutomationResourceLinks(ctx, "alert", "inventory-alert")
			require.NoError(t, err)
			require.Equal(t, 1, n)
			n, err = env.Fixtures.CountAutomationResourceLinks(ctx, "task", "inventory-alert")
			require.NoError(t, err)
			require.Zero(t, n)
			binding := models.AutomationBinding{AutomationID: a.ID, VersionID: "inventory-version", NodeID: "target"}
			_, _, err = env.Automations.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{Context: models.AutomationContext{ProjectID: a.ProjectID}, Binding: binding, WorkItemKey: "transition-work", ActivityKey: "transition-activity", ActivityType: "test", ActivityStatus: models.AutomationActivityCompleted, EventKey: "transition-event", FromNodeID: "source", ToNodeID: "target", Transition: models.AutomationTransitionEntered})
			require.NoError(t, err)
			for _, key := range []string{"", "next"} {
				n, err = env.Fixtures.CountAutomationTransitions(ctx, a.ID, key)
				require.NoError(t, err)
				require.Equal(t, 1, n)
			}
			n, err = env.Fixtures.CountAutomationTransitions(ctx, a.ID, "missing-edge")
			require.NoError(t, err)
			require.Zero(t, n)
			n, err = env.Fixtures.CountAutomationTransitions(ctx, "foreign", "")
			require.NoError(t, err)
			require.Zero(t, n)
		})
	}
}

func TestProjectionInventoryPreservesHistoryAndScope(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			require.NoError(t, env.Fixtures.SeedAutomation(ctx, models.Automation{ID: "history", ProjectID: "default", StableKey: "history", Name: "History", AutomationType: "custom", LifecycleState: "active"}))
			require.NoError(t, env.Fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: "history-v", ProjectID: "default", AutomationID: "history", Version: 1, State: "published", Source: "manual", AdapterKey: "custom"}))
			require.NoError(t, env.Fixtures.SetAutomationPublishedVersion(ctx, "history", "history-v"))
			for _, id := range []string{"from", "to"} {
				require.NoError(t, env.Fixtures.SeedAutomationNode(ctx, models.AutomationNode{ID: id, ProjectID: "default", AutomationID: "history", VersionID: "history-v", NodeKey: id, Name: id, NodeType: "agent_task", Role: "task", ConfigJSON: "{}"}))
			}
			require.NoError(t, env.Fixtures.SeedAutomationEdge(ctx, models.AutomationEdge{ProjectID: "default", AutomationID: "history", VersionID: "history-v", SourceNodeID: "from", TargetNodeID: "to", EdgeKey: "next", ConditionJSON: "{}"}))
			w := models.AutomationWorkItem{ID: "work", ProjectID: "default", AutomationID: "history", OriginVersionID: "history-v", WorkItemKey: "work-key", Kind: "test", Title: "Work", Status: models.AutomationWorkItemActive}
			require.NoError(t, env.Fixtures.SeedAutomationWorkItem(ctx, w))
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			pos := models.AutomationWorkItemPosition{WorkItemID: w.ID, ProjectID: w.ProjectID, AutomationID: w.AutomationID, VersionID: w.OriginVersionID, NodeID: "from", State: models.AutomationPositionWaiting, EnteredAt: at, UpdatedAt: at}
			require.NoError(t, env.Fixtures.SeedAutomationPosition(ctx, pos))
			require.NoError(t, env.Fixtures.SeedAutomationPosition(ctx, pos))
			n, err := env.Fixtures.CountMatchingPositions(ctx, storefixture.PositionMatch{ProjectID: "default", AutomationID: "history", VersionID: "history-v", WorkItemID: w.ID, NodeID: "from", NodeKey: "from", State: "waiting", WorkItemKey: w.WorkItemKey})
			require.NoError(t, err)
			require.Equal(t, 1, n)
			for _, m := range []storefixture.PositionMatch{{ProjectID: "foreign"}, {VersionID: "old"}, {NodeKey: "to"}, {WorkItemKey: "missing"}, {State: "active"}} {
				n, err = env.Fixtures.CountMatchingPositions(ctx, m)
				require.NoError(t, err)
				require.Zero(t, n)
			}
			n, err = env.Fixtures.CountMatchingWorkItems(ctx, storefixture.WorkItemMatch{ID: w.ID, ProjectID: w.ProjectID, AutomationID: w.AutomationID, VersionID: w.OriginVersionID, Key: w.WorkItemKey, Kind: w.Kind, Status: "active"})
			require.NoError(t, err)
			require.Equal(t, 1, n)
			for _, m := range []storefixture.WorkItemMatch{{ProjectID: "foreign"}, {VersionID: "old"}, {Key: "missing"}, {Status: "completed"}} {
				n, err = env.Fixtures.CountMatchingWorkItems(ctx, m)
				require.NoError(t, err)
				require.Zero(t, n)
			}
			// More than two history pages: exact counts must not depend on pagination or equal-time cursors.
			for i := 0; i < 105; i++ {
				id := fmt.Sprintf("history-%03d", i)
				a := models.AutomationActivity{ID: id, ProjectID: "default", AutomationID: "history", VersionID: "history-v", NodeID: "from", WorkItemID: w.ID, ActivityKey: id, ActivityType: "task_execution", Status: models.AutomationActivityFailed, StartedAt: at, CompletedAt: &at, ErrorMessage: "preserved failure"}
				require.NoError(t, env.Fixtures.SeedAutomationActivity(ctx, a, []models.AutomationActivityResource{{ResourceType: "task", ResourceID: "historic-task", Relation: "subject"}}))
			}
			m := storefixture.ActivityMatch{ProjectID: "default", AutomationID: "history", VersionID: "history-v", Type: "task_execution", Status: "failed", HasWorkItem: true, WorkItemID: w.ID}
			n, err = env.Fixtures.CountMatchingActivities(ctx, m)
			require.NoError(t, err)
			require.Equal(t, 105, n)
			links, err := env.Fixtures.AutomationActivityResources(ctx, m)
			require.NoError(t, err)
			require.Len(t, links, 105)
			for _, bad := range []storefixture.ActivityMatch{{ProjectID: "foreign"}, {VersionID: "old"}, {Key: "missing"}, {Status: "completed"}, {WorkItemID: "missing"}} {
				n, err = env.Fixtures.CountMatchingActivities(ctx, bad)
				require.NoError(t, err)
				require.Zero(t, n)
				links, err = env.Fixtures.AutomationActivityResources(ctx, bad)
				require.NoError(t, err)
				require.Empty(t, links)
			}
			page, err := env.Automations.ListAutomationActivities(ctx, "default", "history", "", "", 1, "")
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			require.True(t, page.Items[0].StartedAt.Equal(at))
			require.NotNil(t, page.Items[0].CompletedAt)
			require.True(t, page.Items[0].CompletedAt.Equal(at))
			require.Equal(t, "preserved failure", page.Items[0].ErrorMessage)
			status, err := env.Fixtures.AutomationActivityStatus(ctx, "history-000")
			require.NoError(t, err)
			require.Equal(t, models.AutomationActivityFailed, status)
			require.NoError(t, env.Fixtures.SetLiveAutomationActivity(ctx, "history-000", ""))
			binding := models.AutomationBinding{AutomationID: "history", VersionID: "history-v", NodeID: "to", WorkItemID: w.ID}
			_, _, err = env.Automations.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{Context: models.AutomationContext{ProjectID: "default"}, Binding: binding, ActivityKey: "handoff", ActivityType: "handoff", ActivityStatus: models.AutomationActivityCompleted, EventKey: "handoff", FromNodeID: "from", ToNodeID: "to", Transition: models.AutomationTransitionEntered})
			require.NoError(t, err)
			n, err = env.Fixtures.CountMatchingTransitions(ctx, storefixture.TransitionMatch{ProjectID: "default", AutomationID: "history", VersionID: "history-v", WorkItemID: w.ID, EdgeKey: "next", State: "entered"})
			require.NoError(t, err)
			require.Equal(t, 1, n)
			for _, bad := range []storefixture.TransitionMatch{{ProjectID: "foreign"}, {VersionID: "old"}, {EdgeKey: "missing"}, {State: "blocked"}} {
				n, err = env.Fixtures.CountMatchingTransitions(ctx, bad)
				require.NoError(t, err)
				require.Zero(t, n)
			}
		})
	}
}

func TestReservedAutomationTaskFixtureRollsBackAndScopes(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			var opts []teststores.Option
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			before, err := env.Fixtures.AutomationState(ctx)
			require.NoError(t, err)
			require.Error(t, env.Fixtures.SeedReservedAutomationTask(ctx, "default", "missing", "schedule"))
			after, err := env.Fixtures.AutomationState(ctx)
			require.NoError(t, err)
			require.Equal(t, before, after)
			task := models.Task{ProjectID: "default", Title: "Reserved", Prompt: "work", Category: models.CategoryScheduled, Status: models.StatusQueued, Priority: 2}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC(), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
			require.NoError(t, env.Schedules.Create(ctx, &schedule))
			require.NoError(t, env.Fixtures.SeedReservedAutomationTask(ctx, "default", task.ID, schedule.ID))
			n, err := env.Fixtures.CountTaskAutomationReservations(ctx, []string{task.ID})
			require.NoError(t, err)
			require.Equal(t, 1, n)
			n, err = env.Fixtures.CountTaskAutomationReservations(ctx, []string{"foreign"})
			require.NoError(t, err)
			require.Zero(t, n)
			n, err = env.Fixtures.CountTaskThreadInputs(ctx, task.ID)
			require.NoError(t, err)
			require.Zero(t, n)
		})
	}
}

func TestPerformanceProjectionCountersAndProjectLimits(t *testing.T) {
	for _, production := range []bool{false, true} {
		name := "isolated"
		if production {
			name = "production"
		}
		t.Run(name, func(t *testing.T) {
			opts := []teststores.Option{teststores.WithIOCounting()}
			if production {
				opts = append(opts, teststores.WithProductionTopology())
			}
			env := teststores.New(t, opts...)
			ctx := t.Context()
			require.NoError(t, env.Fixtures.ClearProjects(ctx))
			zero, three := 0, 3
			require.NoError(t, env.Fixtures.SeedProjects(ctx, []models.Project{{ID: "unlimited", Name: "Unlimited"}, {ID: "zero", Name: "Zero", MaxWorkers: &zero}, {ID: "limited", Name: "Limited", MaxWorkers: &three}}))
			projects, err := env.Projects.ListWorkerCapacityProjects(ctx)
			require.NoError(t, err)
			require.Len(t, projects, 3)
			for _, p := range projects {
				switch p.ID {
				case "unlimited":
					require.Nil(t, p.MaxWorkers)
				case "zero":
					require.NotNil(t, p.MaxWorkers)
					require.Equal(t, 0, *p.MaxWorkers)
				case "limited":
					require.NotNil(t, p.MaxWorkers)
					require.Equal(t, 3, *p.MaxWorkers)
				}
			}
			model := models.LLMConfig{Name: "Counter fixture", Provider: models.ProviderTest, Model: "test", MaxWorkers: 3}
			require.NoError(t, env.LLMConfigs.Create(ctx, &model))
			env.IO.Reset()
			env.IO.SetEnabled(true)
			_, err = env.LLMConfigs.ListChatSelectionOptions(ctx)
			require.NoError(t, err)
			_, err = env.LLMConfigs.ListVisionSelectionOptions(ctx)
			require.NoError(t, err)
			_, err = env.LLMConfigs.GetByID(ctx, model.ID)
			require.NoError(t, err)
			_, err = env.LLMConfigs.ListBadgeOptions(ctx)
			require.NoError(t, err)
			_, err = env.LLMConfigs.ListWorkerCapacities(ctx)
			require.NoError(t, err)
			_, err = env.Agents.ListTaskUIOptions(ctx)
			require.NoError(t, err)
			_, err = env.Projects.ListWorkerCapacityProjects(ctx)
			require.NoError(t, err)
			_, err = env.Tasks.CountPendingByProject(ctx)
			require.NoError(t, err)
			env.IO.SetEnabled(false)
			stats := env.IO.Snapshot()
			require.Equal(t, 8, stats.Requests)
			require.Equal(t, 3, stats.NewTaskSelectionReads)
			require.Equal(t, 2, stats.ModelSelectionReads)
			require.Equal(t, 1, stats.ModelDetailReads)
			require.Equal(t, 1, stats.ModelBadgeReads)
			require.Equal(t, 1, stats.ModelWorkerReads)
			require.Equal(t, 1, stats.AgentTaskUIReads)
			require.Equal(t, 1, stats.ProjectCapacityReads)
			require.Equal(t, 1, stats.PendingProjectCountReads)
			// A full catalog or project read must not be mistaken for the compact projection.
			env.IO.Reset()
			env.IO.SetEnabled(true)
			_, err = env.LLMConfigs.List(ctx)
			require.NoError(t, err)
			_, err = env.Projects.List(ctx)
			require.NoError(t, err)
			env.IO.SetEnabled(false)
			stats = env.IO.Snapshot()
			require.Zero(t, stats.ModelSelectionReads)
			require.Zero(t, stats.ModelBadgeReads)
			require.Zero(t, stats.ProjectCapacityReads)
			require.Equal(t, 1, stats.ProjectOtherOrderedReads)
		})
	}
}
