package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSaveCurrentGraphWritesGraphRowsAndUsesNodeIDsForResourceBindings(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationGraphTestProject(t, db, "Saved graph shared writer")
	repo := NewAutomationRepo(db)
	ctx := context.Background()
	runAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Shared Writer Save",
		Description:    "saved graph path",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{
			{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "schedule", Config: map[string]any{"repeat_type": "daily"}, Position: &models.AutomationDraftPoint{X: 12.5, Y: 34.25}},
			{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Run the saved graph task."}, Position: &models.AutomationDraftPoint{X: 98, Y: 76}},
		},
		Edges: []models.AutomationDraftEdge{{Key: "schedule_to_task", From: "schedule", To: "task", Label: "then", Condition: map[string]any{"if": "ready"}}},
	}

	definition, runnable, err := repo.SaveCurrentGraph(ctx, AutomationSaveWrite{
		ProjectID:    project.ID,
		AutomationID: NewID(),
		GraphID:      NewID(),
		Candidate:    candidate,
		Tasks: []AutomationSaveTask{{
			NodeKey: "task", Title: "Shared writer task", Prompt: "Run the saved graph task.",
			Category: models.CategoryActive, Priority: 2,
		}},
		Schedules: []AutomationSaveSchedule{{
			NodeKey: "schedule", TaskNodeKey: "task", RunAt: runAt, RepeatType: models.RepeatDaily,
			RepeatInterval: 1, Enabled: true, ClearContextOnStart: true,
		}},
	})
	require.NoError(t, err)
	require.Len(t, runnable, 1)
	require.Len(t, definition.Nodes, 2)
	require.Len(t, definition.Edges, 1)
	require.Len(t, definition.Resources, 2)

	var scheduleNodeID, taskNodeID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM automation_nodes WHERE version_id = ? AND node_key = 'schedule' AND config_json = ? AND position_x = 12.5 AND position_y = 34.25`, definition.Version.ID, `{"repeat_type":"daily"}`).Scan(&scheduleNodeID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM automation_nodes WHERE version_id = ? AND node_key = 'task' AND config_json = ? AND position_x = 98 AND position_y = 76`, definition.Version.ID, `{"prompt":"Run the saved graph task."}`).Scan(&taskNodeID))
	require.Equal(t, 1, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_edges WHERE version_id = ? AND edge_key = 'schedule_to_task' AND source_node_id = ? AND target_node_id = ? AND condition_json = ? AND display_order = 0`, definition.Version.ID, scheduleNodeID, taskNodeID, `{"if":"ready"}`))
	require.Equal(t, 1, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_definition_resources WHERE version_id = ? AND node_id = ? AND resource_type = 'schedule'`, definition.Version.ID, scheduleNodeID))
	require.Equal(t, 1, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_definition_resources WHERE version_id = ? AND node_id = ? AND resource_type = 'task'`, definition.Version.ID, taskNodeID))
	require.Equal(t, 1, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_trigger_owners WHERE version_id = ? AND node_id = ?`, definition.Version.ID, scheduleNodeID))
	require.Equal(t, 1, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_graph_metadata WHERE version_id = ? AND automation_id = ?`, definition.Version.ID, definition.Automation.ID))
}

func TestSaveCurrentGraphReconcilesDeletedOwnedResourcesAndRejectsForeignReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationGraphTestProject(t, db, "Deleted resource recovery")
	otherProject := automationGraphTestProject(t, db, "Foreign resource recovery")
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	scheduleRepo := NewScheduleRepo(db)
	ctx := context.Background()
	automationID := NewID()
	candidate := models.AutomationDraftCandidate{
		SchemaVersion: 1, Name: "Deleted resource recovery", Description: "recover one retained trigger", AutomationType: "custom", AdapterKey: "custom",
		Nodes: []models.AutomationDraftNode{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "fixed_schedule",
			Config:   map[string]any{"prompt": "Run the retained trigger.", "category": "active", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true},
			Position: &models.AutomationDraftPoint{X: 12, Y: 24}}},
		Edges: []models.AutomationDraftEdge{},
	}
	write := func(graphID, expectedGraphID, existingTaskID, existingScheduleID string) AutomationSaveWrite {
		return AutomationSaveWrite{ProjectID: project.ID, AutomationID: automationID, GraphID: graphID, ExpectedCurrentGraphID: expectedGraphID, Candidate: candidate,
			Tasks: []AutomationSaveTask{{NodeKey: "trigger", ExistingTaskID: existingTaskID, Title: "Trigger", Prompt: "Run the retained trigger.", Category: models.CategoryActive, Priority: 2}},
			Schedules: []AutomationSaveSchedule{{NodeKey: "trigger", ExistingScheduleID: existingScheduleID, TaskNodeKey: "trigger", RunAt: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC), RepeatType: models.RepeatDaily,
				RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}}}
	}

	first, _, err := repo.SaveCurrentGraph(ctx, write(NewID(), "", "", ""))
	require.NoError(t, err)
	oldTaskID := automationGraphResourceID(t, first, "trigger", "task")
	oldScheduleID := automationGraphResourceID(t, first, "trigger", "schedule")
	require.NoError(t, scheduleRepo.Delete(ctx, oldScheduleID))
	require.NoError(t, taskRepo.Delete(ctx, oldTaskID))

	_, err = db.ExecContext(ctx, `CREATE TRIGGER fail_deleted_resource_recovery_schedule
		BEFORE INSERT ON schedules BEGIN SELECT RAISE(ABORT, 'injected recovery schedule failure'); END`)
	require.NoError(t, err)
	_, _, err = repo.SaveCurrentGraph(ctx, write(NewID(), first.Version.ID, oldTaskID, oldScheduleID))
	require.ErrorContains(t, err, "injected recovery schedule failure")
	_, err = db.ExecContext(ctx, `DROP TRIGGER fail_deleted_resource_recovery_schedule`)
	require.NoError(t, err)

	current, err := repo.GetDefinition(ctx, project.ID, automationID)
	require.NoError(t, err)
	require.Equal(t, first.Version.ID, current.Version.ID, "a failed recovery must retain the current graph")
	require.Equal(t, 1, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_versions WHERE automation_id = ?`, automationID))
	require.Zero(t, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND created_via = ?`, project.ID, AutomationCompilerTaskCreatedVia(automationID, "trigger")))

	recovered, _, err := repo.SaveCurrentGraph(ctx, write(NewID(), first.Version.ID, oldTaskID, oldScheduleID))
	require.NoError(t, err)
	recoveredTaskID := automationGraphResourceID(t, recovered, "trigger", "task")
	recoveredScheduleID := automationGraphResourceID(t, recovered, "trigger", "schedule")
	require.NotEqual(t, oldTaskID, recoveredTaskID)
	require.NotEqual(t, oldScheduleID, recoveredScheduleID)

	foreignTask := &models.Task{ProjectID: otherProject.ID, Title: "Foreign task", Prompt: "must not be adopted", Category: models.CategoryActive, Priority: 2, Status: models.StatusPending}
	require.NoError(t, taskRepo.Create(ctx, foreignTask))
	foreignSchedule := &models.Schedule{TaskID: foreignTask.ID, RunAt: time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, foreignSchedule))

	_, _, err = repo.SaveCurrentGraph(ctx, write(NewID(), recovered.Version.ID, foreignTask.ID, recoveredScheduleID))
	require.ErrorContains(t, err, "not owned by this Automation")
	_, _, err = repo.SaveCurrentGraph(ctx, write(NewID(), recovered.Version.ID, recoveredTaskID, foreignSchedule.ID))
	require.ErrorContains(t, err, "not owned by this Automation")
	current, err = repo.GetDefinition(ctx, project.ID, automationID)
	require.NoError(t, err)
	require.Equal(t, recovered.Version.ID, current.Version.ID, "invalid resource references must not replace the current graph")
	require.Equal(t, recoveredTaskID, automationGraphResourceID(t, current, "trigger", "task"))
	require.Equal(t, recoveredScheduleID, automationGraphResourceID(t, current, "trigger", "schedule"))

	repeated, _, err := repo.SaveCurrentGraph(ctx, write(NewID(), recovered.Version.ID, recoveredTaskID, recoveredScheduleID))
	require.NoError(t, err)
	require.Equal(t, recoveredTaskID, automationGraphResourceID(t, repeated, "trigger", "task"))
	require.Equal(t, recoveredScheduleID, automationGraphResourceID(t, repeated, "trigger", "schedule"))
	require.Equal(t, 1, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM tasks WHERE project_id = ? AND created_via = ?`, project.ID, AutomationCompilerTaskCreatedVia(automationID, "trigger")))
}

func TestSaveCurrentGraphRejectsOwnedScheduleRetargeting(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationGraphTestProject(t, db, "Owned schedule retargeting")
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	ctx := context.Background()
	automationID := NewID()
	candidate := models.AutomationDraftCandidate{
		SchemaVersion: 1, Name: "Owned schedule retargeting", Description: "reject a repointed schedule", AutomationType: "custom", AdapterKey: "custom",
		Nodes: []models.AutomationDraftNode{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "fixed_schedule",
			Config:   map[string]any{"prompt": "Run the retained trigger.", "category": "active", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true},
			Position: &models.AutomationDraftPoint{X: 12, Y: 24}}},
	}
	first, _, err := repo.SaveCurrentGraph(ctx, AutomationSaveWrite{
		ProjectID: project.ID, AutomationID: automationID, GraphID: NewID(), Candidate: candidate,
		Tasks: []AutomationSaveTask{{NodeKey: "trigger", Title: "Trigger", Prompt: "Run the retained trigger.", Category: models.CategoryActive, Priority: 2}},
		Schedules: []AutomationSaveSchedule{{NodeKey: "trigger", TaskNodeKey: "trigger", RunAt: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC), RepeatType: models.RepeatDaily,
			RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}},
	})
	require.NoError(t, err)
	ownedTaskID := automationGraphResourceID(t, first, "trigger", "task")
	scheduleID := automationGraphResourceID(t, first, "trigger", "schedule")

	otherTask := &models.Task{ProjectID: project.ID, Title: "Unrelated same-project task", Prompt: "must remain unrelated", Category: models.CategoryActive, Priority: 2, Status: models.StatusPending}
	require.NoError(t, taskRepo.Create(ctx, otherTask))
	_, err = db.ExecContext(ctx, `UPDATE schedules SET task_id = ? WHERE id = ?`, otherTask.ID, scheduleID)
	require.NoError(t, err)

	_, _, err = repo.SaveCurrentGraph(ctx, AutomationSaveWrite{
		ProjectID: project.ID, AutomationID: automationID, GraphID: NewID(), ExpectedCurrentGraphID: first.Version.ID, Candidate: candidate,
		Tasks: []AutomationSaveTask{{NodeKey: "trigger", ExistingTaskID: ownedTaskID, Title: "Trigger", Prompt: "Run the retained trigger.", Category: models.CategoryActive, Priority: 2}},
		Schedules: []AutomationSaveSchedule{{NodeKey: "trigger", ExistingScheduleID: scheduleID, TaskNodeKey: "trigger", RunAt: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC), RepeatType: models.RepeatDaily,
			RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}},
	})
	require.ErrorContains(t, err, "must target the task bound to that same node")

	current, err := repo.GetDefinition(ctx, project.ID, automationID)
	require.NoError(t, err)
	require.Equal(t, first.Version.ID, current.Version.ID, "a repointed schedule must not replace the current graph")
	var linkedTaskID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT task_id FROM schedules WHERE id = ?`, scheduleID).Scan(&linkedTaskID))
	require.Equal(t, otherTask.ID, linkedTaskID, "a rejected save must not retarget the schedule")
}

func TestSaveCurrentGraphSkipsStaleGitHubIssueTaskBackfillResources(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationGraphTestProject(t, db, "Stale GitHub task backfill")
	repo := NewAutomationRepo(db)
	ctx := context.Background()
	automationID := NewID()

	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Stale GitHub Backfill",
		AutomationType: "github_sdlc",
		AdapterKey:     "github_sdlc",
		Nodes: []models.AutomationDraftNode{
			{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "implementation", Config: map[string]any{"prompt": "Implement assigned issues."}},
		},
	}
	first, _, err := repo.SaveCurrentGraph(ctx, AutomationSaveWrite{
		ProjectID: project.ID, AutomationID: automationID, GraphID: NewID(), Candidate: candidate,
	})
	require.NoError(t, err)

	var implementationNodeID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM automation_nodes WHERE project_id = ? AND automation_id = ? AND version_id = ? AND node_key = 'implementation'`,
		project.ID, automationID, first.Version.ID).Scan(&implementationNodeID))

	workItemID := NewID()
	require.NoError(t, execAutomationGraphTestSQL(ctx, db, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, work_item_key, kind, title, status)
		VALUES (?, ?, ?, ?, 'github-issue:example/repo:42', 'github_issue', 'Issue 42', 'active')`,
		workItemID, project.ID, automationID, first.Version.ID))
	activityID := NewID()
	require.NoError(t, execAutomationGraphTestSQL(ctx, db, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'create_task', 'completed')`,
		activityID, project.ID, automationID, first.Version.ID, implementationNodeID, workItemID, "work-item:"+workItemID+":implementation-task"))
	require.NoError(t, execAutomationGraphTestSQL(ctx, db, `INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
		VALUES (?, 'task', 'deleted-task-id', 'child'), (?, 'github_issue', 'github:issue:example/repo:42', 'subject')`, activityID, activityID))

	updated := candidate
	updated.Name = "Updated Stale GitHub Backfill"
	_, _, err = repo.SaveCurrentGraph(ctx, AutomationSaveWrite{
		ProjectID: project.ID, AutomationID: automationID, GraphID: NewID(), ExpectedCurrentGraphID: first.Version.ID, Candidate: updated,
	})
	require.NoError(t, err)
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automation_github_issue_task_provenance WHERE project_id = ? AND automation_id = ?`, project.ID, automationID))
}

func TestAutomationGraphWriterRejectsUnknownEdgeNodesForSaveAndRegistration(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationGraphTestProject(t, db, "Unknown graph edge")
	repo := NewAutomationRepo(db)
	ctx := context.Background()

	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Broken Save Graph",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes:          []models.AutomationDraftNode{{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{}}},
		Edges:          []models.AutomationDraftEdge{{Key: "missing_source", From: "missing", To: "task"}},
	}
	_, _, err := repo.SaveCurrentGraph(ctx, AutomationSaveWrite{ProjectID: project.ID, AutomationID: NewID(), GraphID: NewID(), Candidate: candidate})
	require.ErrorContains(t, err, `edge "missing_source" references an unknown node`)
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automations WHERE project_id = ? AND name = 'Broken Save Graph'`, project.ID))

	_, _, err = repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: project.ID, StableKey: "broken/registered", Name: "Broken Registered Graph",
		AutomationType: "custom", AdapterKey: "custom", CreatedVia: "test",
		Nodes: []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "schedule"}},
		Edges: []models.AutomationEdgeSpec{{Key: "missing_target", SourceNodeKey: "trigger", TargetNodeKey: "missing"}},
	})
	require.ErrorContains(t, err, `edge "missing_target" references an unknown node`)
	require.Equal(t, 0, automationGraphCountRows(t, db, `SELECT COUNT(*) FROM automations WHERE project_id = ? AND stable_key = 'broken/registered'`, project.ID))
}

func automationGraphResourceID(t *testing.T, definition *models.AutomationDefinition, nodeKey, resourceType string) string {
	t.Helper()
	for _, resource := range definition.Resources {
		if resource.NodeKey == nodeKey && resource.ResourceType == resourceType {
			return resource.ResourceID
		}
	}
	require.FailNow(t, "Automation resource not found", nodeKey+"/"+resourceType)
	return ""
}

func automationGraphTestProject(t *testing.T, db *sql.DB, name string) models.Project {
	t.Helper()
	project := models.Project{Name: name}
	require.NoError(t, NewProjectRepo(db).Create(context.Background(), &project))
	return project
}

func automationGraphCountRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(context.Background(), query, args...).Scan(&count))
	return count
}

func execAutomationGraphTestSQL(ctx context.Context, db *sql.DB, query string, args ...any) error {
	_, err := db.ExecContext(ctx, query, args...)
	return err
}
