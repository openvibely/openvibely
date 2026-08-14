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
