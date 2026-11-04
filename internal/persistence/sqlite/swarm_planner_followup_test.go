package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPreparePlannerFollowupRollsBackBothTasks(t *testing.T) {
	for _, role := range []models.SwarmRole{models.SwarmRolePlanner} {
		t.Run(string(role), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			tasks := NewTaskRepo(db, nil)
			parent := models.Task{ProjectID: "default", Title: "Parent", Priority: 2, Category: models.CategoryCompleted, Status: models.StatusCompleted, SwarmRole: models.SwarmRoleParent}
			require.NoError(t, tasks.Create(owner, &parent))
			child := models.Task{ProjectID: "default", Title: "Child", Priority: 2, Category: models.CategoryCompleted, Status: models.StatusCompleted, SwarmRole: role, ParentTaskID: &parent.ID}
			require.NoError(t, tasks.Create(owner, &child))
			p, err := tasks.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			c, err := tasks.GetByID(ctx, child.ID)
			require.NoError(t, err)
			guarded := persistence.WithSwarmWriteIdentities(owner, []persistence.SwarmWriteIdentity{{TaskID: p.ID, ProjectID: p.ProjectID, UserID: models.BootstrapUserID}, {TaskID: c.ID, ProjectID: c.ProjectID, UserID: models.BootstrapUserID}})
			next := *c
			next.Prompt = "new generation instructions"
			next.SwarmConfig = `{"rerun_generation":2}`
			apply := func() error {
				return tasks.PreparePlannerFollowup(guarded, p, c, &next, `{"generation":2,"stop_revision":1}`)
			}
			unchanged := func() {
				saved, err := tasks.GetByID(ctx, p.ID)
				require.NoError(t, err)
				require.Equal(t, p, saved)
				saved, err = tasks.GetByID(ctx, c.ID)
				require.NoError(t, err)
				require.Equal(t, c, saved)
			}
			_, err = db.Exec(`CREATE TRIGGER fail_child_rerun BEFORE UPDATE ON tasks WHEN NEW.id='` + c.ID + `' BEGIN SELECT RAISE(ABORT,'child write unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, apply(), "child write unavailable")
			unchanged()
			_, err = db.Exec(`DROP TRIGGER fail_child_rerun`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TRIGGER fail_rerun_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='swarm.planner_followup_prepared' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, apply(), "audit unavailable")
			unchanged()
			_, err = db.Exec(`DROP TRIGGER fail_rerun_audit`)
			require.NoError(t, err)
			stale := *p
			stale.SwarmConfig = `{"stop_revision":10}`
			require.Error(t, tasks.PreparePlannerFollowup(guarded, &stale, c, &next, "{}"))
			unchanged()
			foreign := *c
			foreign.ProjectID = "foreign"
			require.Error(t, tasks.PreparePlannerFollowup(guarded, p, &foreign, &next, "{}"))
			unchanged()
			require.NoError(t, apply())
			savedParent, err := tasks.GetByID(ctx, p.ID)
			require.NoError(t, err)
			savedChild, err := tasks.GetByID(ctx, c.ID)
			require.NoError(t, err)
			require.Equal(t, models.StatusRunning, savedParent.Status)
			require.Equal(t, models.CategoryActive, savedParent.Category)
			require.Equal(t, next.Prompt, savedChild.Prompt)
			require.Equal(t, models.StatusPending, savedChild.Status)
			require.Equal(t, models.CategoryActive, savedChild.Category)
			require.JSONEq(t, `{"generation":2,"stop_revision":1}`, savedParent.SwarmConfig)
			require.JSONEq(t, `{"rerun_generation":2}`, savedChild.SwarmConfig)
			require.Error(t, apply(), "duplicate stale request denied")
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE target_id=? AND action='swarm.planner_followup_prepared'`, c.ID).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}
