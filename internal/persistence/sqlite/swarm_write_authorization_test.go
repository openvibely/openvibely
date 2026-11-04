package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSwarmStateWriteRechecksChildAfterPreflight(t *testing.T) {
	for _, operation := range []string{"category", "status", "fields"} {
		t.Run(operation, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			auth := NewDiscordAuthRepo(db)
			entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
			bind := func() {
				require.NoError(t, auth.Create(owner, entry))
				require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
			}
			bind()
			admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
			tasks := NewTaskRepo(db, nil)
			parent := models.Task{ProjectID: "default", Title: "Parent", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2, SwarmRole: models.SwarmRoleParent}
			require.NoError(t, tasks.Create(owner, &parent))
			child := models.Task{ProjectID: "default", Title: "Child", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2, SwarmRole: models.SwarmRoleReviewer, ParentTaskID: &parent.ID}
			require.NoError(t, tasks.Create(admitted, &child))
			guarded := persistence.WithSwarmWriteIdentities(owner, []persistence.SwarmWriteIdentity{{TaskID: parent.ID, ProjectID: "default", UserID: models.BootstrapUserID}, {TaskID: child.ID, ProjectID: "default", UserID: models.BootstrapUserID}})
			write := func(c context.Context) error {
				switch operation {
				case "category":
					return tasks.UpdateCategory(c, parent.ID, models.CategoryActive)
				case "status":
					return tasks.UpdateStatus(c, parent.ID, models.StatusRunning)
				default:
					return tasks.UpdateSwarmFields(c, parent.ID, parent.SwarmRole, "needs_review", `{"generation":2}`, 0)
				}
			}
			before, err := tasks.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
			require.Error(t, write(guarded), "revoked child must block even parent write")
			unchanged := func() {
				saved, err := tasks.GetByID(ctx, parent.ID)
				require.NoError(t, err)
				require.Equal(t, before.Category, saved.Category)
				require.Equal(t, before.Status, saved.Status)
				require.Equal(t, before.SwarmConfig, saved.SwarmConfig)
				require.Equal(t, before.SwarmStatus, saved.SwarmStatus)
			}
			unchanged()
			bind()
			_, err = db.Exec(`CREATE TRIGGER fail_swarm_audit BEFORE INSERT ON project_audit_events WHEN NEW.action LIKE 'swarm.%' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, write(guarded), "audit unavailable")
			unchanged()
			_, err = db.Exec(`DROP TRIGGER fail_swarm_audit`)
			require.NoError(t, err)
			require.NoError(t, write(guarded))
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE target_id=? AND action LIKE 'swarm.%'`, parent.ID).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func TestSwarmWriteLegacyOwnerAndScope(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	tasks := NewTaskRepo(db, nil)
	task := models.Task{ProjectID: "default", Title: "Legacy parent", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2, SwarmRole: models.SwarmRoleParent}
	require.NoError(t, tasks.Create(ctx, &task))
	identity := persistence.SwarmWriteIdentity{TaskID: task.ID, ProjectID: task.ProjectID, UserID: models.BootstrapUserID}
	guard := func() context.Context {
		return persistence.WithSwarmWriteIdentities(ctx, []persistence.SwarmWriteIdentity{identity})
	}
	require.ErrorIs(t, tasks.UpdateStatus(guard(), task.ID, models.StatusRunning), persistence.ErrProjectDenied)
	identity.AllowLegacyOwner = true
	identity.ProjectID = "foreign"
	require.ErrorIs(t, tasks.UpdateStatus(guard(), task.ID, models.StatusRunning), persistence.ErrProjectDenied)
	identity.ProjectID = task.ProjectID
	require.NoError(t, tasks.UpdateStatus(guard(), task.ID, models.StatusRunning))
	require.ErrorIs(t, tasks.UpdateSwarmFields(guard(), "other", models.SwarmRoleParent, "pending", "{}", 0), persistence.ErrProjectDenied)
}
