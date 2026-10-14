package sqlite_test

import (
	"context"
	"database/sql"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type automationConstraintFixture struct {
	db         *sql.DB
	stores     persistence.Stores
	project    models.Project
	task       models.Task
	schedule   models.Schedule
	definition *models.AutomationDefinition
	repo       persistence.AutomationStore
	taskRepo   persistence.TaskStore
	schedRepo  persistence.ScheduleStore
}

func newAutomationConstraintFixture(t *testing.T, adapterKey string) automationConstraintFixture {
	t.Helper()
	db := testutil.NewTestDB(t)
	stores := sqlite.NewStores(db, nil)
	project := automationConstraintProject(t, stores.Projects, "Runtime "+adapterKey)
	task, schedule := automationConstraintTaskAndSchedule(t, stores.Tasks, stores.Schedules, project.ID, "Runtime task")
	due := time.Now().UTC().Add(-time.Minute)
	schedule.RunAt = due.Add(-time.Hour)
	schedule.NextRun = &due
	schedule.RepeatType = models.RepeatHours
	schedule.RepeatInterval = 1
	schedule.ClearContextOnStart = true
	require.NoError(t, stores.Schedules.Update(t.Context(), &schedule))
	definition, _, err := service.NewAutomationRegistrationService(stores.Automations, service.NewAutomationAdapterRegistry()).Register(t.Context(), service.AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: adapterKey, StableKey: "native-sdlc/runtime", Resources: []models.AutomationResourceBinding{{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID}, {NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID}}})
	require.NoError(t, err)
	return automationConstraintFixture{db: db, stores: stores, project: project, task: task, schedule: schedule, definition: definition, repo: stores.Automations, taskRepo: stores.Tasks, schedRepo: stores.Schedules}
}
func automationConstraintProject(t *testing.T, repo persistence.ProjectStore, name string) models.Project {
	t.Helper()
	p := models.Project{Name: name}
	require.NoError(t, repo.Create(t.Context(), &p))
	return p
}
func automationConstraintTaskAndSchedule(t *testing.T, tasks persistence.TaskStore, schedules persistence.ScheduleStore, projectID, title string) (models.Task, models.Schedule) {
	t.Helper()
	task := models.Task{ProjectID: projectID, Title: title, Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "visible automation task"}
	require.NoError(t, tasks.Create(t.Context(), &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, schedules.Create(t.Context(), &schedule))
	return task, schedule
}
func automationConstraintNode(t *testing.T, d *models.AutomationDefinition, key string) models.AutomationNode {
	t.Helper()
	for _, n := range d.Nodes {
		if n.NodeKey == key {
			return n
		}
	}
	t.Fatalf("missing node %s", key)
	return models.AutomationNode{}
}
func TestAutomationRuntimeCompositeConstraintsAndProjectCascade(t *testing.T) {
	fixture := newAutomationConstraintFixture(t, service.AutomationAdapterNativeSDLC)
	ctx := context.Background()
	otherTask, otherSchedule := automationConstraintTaskAndSchedule(t, fixture.taskRepo, fixture.schedRepo, fixture.project.ID, "Other topology")
	otherDefinition, _, err := service.NewAutomationRegistrationService(fixture.repo, service.NewAutomationAdapterRegistry()).Register(ctx, service.AutomationRegistrationRequest{
		ProjectID: fixture.project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/other-topology",
		Resources: []models.AutomationResourceBinding{{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: otherSchedule.ID}, {NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: otherTask.ID}},
	})
	require.NoError(t, err)
	foreignNode := automationConstraintNode(t, otherDefinition, "vision_suggestions")
	_, err = fixture.db.Exec(`INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'mismatched-parent')`, fixture.project.ID, fixture.definition.Automation.ID,
		fixture.definition.Version.ID, foreignNode.ID, fixture.schedule.ID)
	require.Error(t, err, "invocation must reject a node from another automation/version")

	invocation, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	approval := automationConstraintNode(t, fixture.definition, "approval")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: approval.ID}
	item, activity, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "constraint:item", ActivityKey: "constraint:activity", ActivityType: "test", ActivityStatus: models.AutomationActivityRunning,
	})
	require.NoError(t, err)
	_, err = fixture.db.Exec(`INSERT INTO automation_work_item_positions
		(work_item_id, project_id, automation_id, version_id, node_id, state) VALUES (?, ?, ?, ?, ?, 'active')`,
		item.ID, fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, foreignNode.ID)
	require.Error(t, err, "position must reject a node from another topology")
	_, err = fixture.db.Exec(`INSERT INTO automation_transitions
		(project_id, automation_id, version_id, work_item_id, activity_id, to_node_id, event_key, state)
		VALUES (?, ?, ?, ?, ?, ?, 'constraint:mismatch', 'entered')`, fixture.project.ID, fixture.definition.Automation.ID,
		fixture.definition.Version.ID, item.ID, activity.ID, foreignNode.ID)
	require.Error(t, err, "transition must reject a node from another topology")

	llmRepo := fixture.stores.LLMConfigs
	agent := models.LLMConfig{Name: "Cascade binding model", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, llmRepo.Create(ctx, &agent))
	inputRepo := fixture.stores.ThreadInputs
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: fixture.project.ID, TaskID: fixture.task.ID,
		AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "cascade binding"}
	require.NoError(t, inputRepo.CreateQueued(ctx, &input))
	binding.WorkItemID = item.ID
	require.NoError(t, fixture.repo.(*sqlite.AutomationRepo).BindThreadInput(ctx, input.ID,
		models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, "cascade"))
	var bindingCount int
	require.NoError(t, fixture.db.QueryRow(`SELECT COUNT(*) FROM automation_thread_input_bindings WHERE thread_input_id = ?`, input.ID).Scan(&bindingCount))
	require.Equal(t, 1, bindingCount)

	require.NoError(t, fixture.stores.Projects.(*sqlite.ProjectRepo).Delete(ctx, fixture.project.ID))
	for _, table := range []string{"automations", "automation_invocations", "automation_dispatch_outbox", "automation_task_run_reservations", "automation_work_items", "automation_work_item_positions", "automation_thread_input_bindings", "automation_activities", "automation_activity_resources", "automation_transitions"} {
		var count int
		require.NoError(t, fixture.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table+" must cascade on project deletion")
	}
}

func TestAutomationRuntimeThreadInputBindingSurvivesPromotion(t *testing.T) {
	fixture := newAutomationConstraintFixture(t, service.AutomationAdapterNativeSDLC)
	ctx := context.Background()
	llmRepo := fixture.stores.LLMConfigs
	agent := models.LLMConfig{Name: "Automation queue", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmRepo.Create(ctx, &agent))
	inputRepo := fixture.stores.ThreadInputs
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: fixture.project.ID, TaskID: fixture.task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "continue automation"}
	require.NoError(t, inputRepo.CreateQueued(ctx, &input))
	inbox := automationConstraintNode(t, fixture.definition, "inbox")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: inbox.ID, InvocationID: ""}
	// A queued binding must have an invocation or work item. Create a durable work item first.
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "alert:queued", WorkItemKind: "suggestion", ActivityKey: "alert:queued:seed", ActivityType: "seed", ActivityStatus: models.AutomationActivityCompleted,
	})
	require.NoError(t, err)
	binding.WorkItemID = item.ID
	automationContext := models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}
	require.NoError(t, fixture.repo.(*sqlite.AutomationRepo).BindThreadInput(ctx, input.ID, automationContext, "queued-work"))
	promoted := models.Execution{TaskID: fixture.task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: input.Content, IsFollowup: true}
	require.NoError(t, inputRepo.(*sqlite.ThreadInputRepo).ClaimQueuedForTaskExecution(ctx, input.ID, &promoted))
	loaded, err := fixture.repo.ContextForThreadInput(ctx, fixture.project.ID, input.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Bindings, 1)
	require.Equal(t, item.ID, loaded.Bindings[0].WorkItemID)
	var activityCount int
	require.NoError(t, fixture.db.QueryRow(`SELECT COUNT(*) FROM automation_activities a JOIN automation_activity_resources ar ON ar.activity_id = a.id WHERE ar.resource_type = 'execution' AND ar.resource_id = ?`, promoted.ID).Scan(&activityCount))
	require.Equal(t, 1, activityCount)
	_, err = fixture.db.Exec(`DELETE FROM thread_inputs WHERE id = ?`, input.ID)
	require.NoError(t, err)
	var remainingBindings int
	require.NoError(t, fixture.db.QueryRow(`SELECT COUNT(*) FROM automation_thread_input_bindings WHERE thread_input_id = ?`, input.ID).Scan(&remainingBindings))
	require.Zero(t, remainingBindings, "deleting the authoritative queued input must cascade its causal bindings")

	foreignProject := automationConstraintProject(t, fixture.stores.Projects, "Foreign queued binding")
	foreignTask := models.Task{ProjectID: foreignProject.ID, Title: "Foreign queue", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, fixture.stores.Tasks.Create(ctx, &foreignTask))
	foreignInput := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: foreignProject.ID, TaskID: foreignTask.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "foreign"}
	require.NoError(t, inputRepo.CreateQueued(ctx, &foreignInput))
	require.ErrorContains(t, fixture.repo.(*sqlite.AutomationRepo).BindThreadInput(ctx, foreignInput.ID, automationContext, "foreign"), "project mismatch")
}
