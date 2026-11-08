package sqlite

import (
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestApplySwarmPlanRejectsStaleOrForeignWritesWithoutPartialState(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	tasks := NewTaskRepo(db, nil)
	parent := models.Task{ProjectID: "default", Title: "Parent", Prompt: "p", Priority: 2, Category: models.CategoryActive, Status: models.StatusBlocked, SwarmRole: models.SwarmRoleParent, SwarmStatus: "planning"}
	require.NoError(t, tasks.Create(owner, &parent))
	planner := models.Task{ProjectID: "default", Title: "Planner", Prompt: "p", Priority: 2, Category: models.CategoryActive, Status: models.StatusCompleted, SwarmRole: models.SwarmRolePlanner, SwarmStatus: "planning", ParentTaskID: &parent.ID}
	require.NoError(t, tasks.Create(owner, &planner))
	other := models.Task{ProjectID: "default", Title: "Unrelated", Prompt: "p", Priority: 2, Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, tasks.Create(owner, &other))
	observedParent, err := tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	observedPlanner, err := tasks.GetByID(ctx, planner.ID)
	require.NoError(t, err)
	identities := persistence.WithSwarmWriteIdentities(owner, []persistence.SwarmWriteIdentity{
		{TaskID: parent.ID, ProjectID: "default", UserID: models.BootstrapUserID},
		{TaskID: planner.ID, ProjectID: "default", UserID: models.BootstrapUserID},
	})
	commit := func(writes ...persistence.SwarmTaskWrite) persistence.SwarmPlanCommit {
		return persistence.SwarmPlanCommit{
			Parent: *observedParent, Planner: *observedPlanner, Action: "swarm.plan_applied", Writes: writes,
			Title: func(seq int, base string, attempt int) string {
				if attempt == 0 {
					return base
				}
				return fmt.Sprintf("%s-%d-%d", base, seq, attempt)
			},
			Creates: []*models.Task{{ProjectID: "default", Title: "Worker", Prompt: "w", Priority: 2, Category: models.CategoryActive, Status: models.StatusPending, SwarmRole: models.SwarmRoleWorker, ParentTaskID: &parent.ID, SwarmSequence: 10}},
		}
	}
	unchanged := func() {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM tasks WHERE parent_task_id=? AND swarm_role='worker'`, parent.ID).Scan(&n))
		require.Zero(t, n)
		saved, err := tasks.GetByID(ctx, parent.ID)
		require.NoError(t, err)
		require.Equal(t, observedParent, saved)
		savedOther, err := tasks.GetByID(ctx, other.ID)
		require.NoError(t, err)
		require.Equal(t, models.CategoryBacklog, savedOther.Category)
	}

	require.ErrorIs(t, tasks.ApplySwarmPlan(identities, commit(persistence.SwarmTaskWrite{ID: other.ID, SwarmStatus: "x", Status: models.StatusRunning, Category: models.CategoryActive})), persistence.ErrProjectDenied)
	unchanged()

	stale := commit(persistence.SwarmTaskWrite{ID: parent.ID, SwarmStatus: "workers_running"})
	stale.Planner.SwarmStatus = "planned"
	require.ErrorIs(t, tasks.ApplySwarmPlan(identities, stale), persistence.ErrProjectDenied)
	unchanged()

	stranger := persistence.WithSwarmWriteIdentities(owner, []persistence.SwarmWriteIdentity{{TaskID: parent.ID, ProjectID: "default", UserID: "someone-else"}})
	require.ErrorIs(t, tasks.ApplySwarmPlan(stranger, commit(persistence.SwarmTaskWrite{ID: parent.ID, SwarmStatus: "workers_running"})), persistence.ErrProjectDenied)
	unchanged()

	// A duplicate title retries inside the transaction instead of failing the plan.
	require.NoError(t, tasks.Create(owner, &models.Task{ProjectID: "default", Title: "Worker", Prompt: "p", Priority: 2, Category: models.CategoryBacklog, Status: models.StatusPending}))
	require.NoError(t, tasks.ApplySwarmPlan(identities, commit(persistence.SwarmTaskWrite{ID: parent.ID, SwarmStatus: "workers_running"})))
	var title string
	require.NoError(t, db.QueryRow(`SELECT title FROM tasks WHERE parent_task_id=? AND swarm_role='worker'`, parent.ID).Scan(&title))
	require.Equal(t, "Worker-10-1", title)
	var audits int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='swarm.plan_applied' AND target_id=?`, parent.ID).Scan(&audits))
	require.Equal(t, 1, audits)
}
