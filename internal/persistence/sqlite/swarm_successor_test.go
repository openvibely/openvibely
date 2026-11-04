package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPrepareSwarmSuccessorAtomicAuditAndScope(t *testing.T) {
	for _, role := range []models.SwarmRole{models.SwarmRoleReviewer, models.SwarmRoleMerger, models.SwarmRoleLegacyIntegrator} {
		t.Run(string(role), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			tasks := NewTaskRepo(db, nil)
			parent := models.Task{ProjectID: "default", Title: "Parent", Priority: 2, Category: models.CategoryActive, Status: models.StatusRunning, SwarmRole: models.SwarmRoleParent}
			require.NoError(t, tasks.Create(owner, &parent))
			source := models.Task{ProjectID: "default", Title: "Source", Priority: 2, Category: models.CategoryCompleted, Status: models.StatusCompleted, SwarmRole: models.SwarmRoleWorker, ParentTaskID: &parent.ID}
			require.NoError(t, tasks.Create(owner, &source))
			target := models.Task{ProjectID: "default", Title: "Target", Priority: 2, Category: models.CategoryCompleted, Status: models.StatusCompleted, SwarmRole: role, ParentTaskID: &parent.ID}
			require.NoError(t, tasks.Create(owner, &target))
			before, err := tasks.GetByID(ctx, target.ID)
			require.NoError(t, err)
			guarded := persistence.WithSwarmWriteIdentities(owner, []persistence.SwarmWriteIdentity{
				{TaskID: parent.ID, ProjectID: "default", UserID: models.BootstrapUserID},
				{TaskID: source.ID, ProjectID: "default", UserID: models.BootstrapUserID},
				{TaskID: target.ID, ProjectID: "default", UserID: models.BootstrapUserID},
			})
			target.SwarmConfig = `{"rerun_generation":2}`
			require.Error(t, tasks.PrepareSwarmSuccessor(guarded, "foreign", source.ID, &target))
			_, err = db.Exec(`CREATE TRIGGER fail_successor_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='swarm.successor_prepared' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, tasks.PrepareSwarmSuccessor(guarded, parent.ID, source.ID, &target), "audit unavailable")
			after, err := tasks.GetByID(ctx, target.ID)
			require.NoError(t, err)
			require.Equal(t, before, after, "all successor fields must roll back with audit")
			_, err = db.Exec(`DROP TRIGGER fail_successor_audit`)
			require.NoError(t, err)
			require.NoError(t, tasks.PrepareSwarmSuccessor(guarded, parent.ID, source.ID, &target))
			after, err = tasks.GetByID(ctx, target.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryActive, after.Category)
			require.Equal(t, models.StatusPending, after.Status)
			require.Equal(t, "ready", after.SwarmStatus)
			require.JSONEq(t, target.SwarmConfig, after.SwarmConfig)
			require.Error(t, tasks.PrepareSwarmSuccessor(guarded, parent.ID, source.ID, &target), "stale callback must not prepare twice")
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE target_id=? AND action='swarm.successor_prepared'`, target.ID).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}
