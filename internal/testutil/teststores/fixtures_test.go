package teststores_test

import (
	"context"
	"errors"
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
