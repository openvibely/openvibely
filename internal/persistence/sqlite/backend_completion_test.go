package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestNewStoresCompletionFinalizesAutomation(t *testing.T) {
	for _, viaTask := range []bool{false, true} {
		name := "execution_store"
		if viaTask {
			name = "task_store"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"work": "task"})
			stores := NewStores(db, nil)
			task := models.Task{ProjectID: fixture.ProjectID, Title: "completion", Prompt: "completion", Status: models.StatusRunning, Category: models.CategoryActive}
			require.NoError(t, stores.Tasks.Create(ctx, &task))
			execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "completion"}
			require.NoError(t, stores.Executions.Create(ctx, &execution))
			binding := models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["work"]}
			_, _, err := stores.Automations.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
				Context: models.AutomationContext{ProjectID: fixture.ProjectID}, Binding: binding, WorkItemKey: "completion-work", ActivityKey: "completion-activity", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
				Resources: []models.AutomationActivityResource{{ResourceType: "execution", ResourceID: execution.ID, Relation: "run"}}, EventKey: "completion-start", ToNodeID: binding.NodeID, Transition: models.AutomationTransitionEntered,
			})
			require.NoError(t, err)
			if viaTask {
				_, err = stores.Tasks.CompleteExecutionSuccess(ctx, execution.ID, "done", 1, 1)
			} else {
				_, err = stores.Executions.(*ExecutionRepo).CompleteSuccessIfNoPendingSteering(ctx, execution.ID, "done", 1, 1)
			}
			require.NoError(t, err)
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_transitions WHERE event_key=?`, "execution:"+execution.ID+":terminal:completed").Scan(&count))
			require.Equal(t, 1, count, "successful execution must finalize its automation projection")
		})
	}
}
