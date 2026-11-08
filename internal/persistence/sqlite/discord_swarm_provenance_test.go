package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDiscordSwarmChildInheritsDurableIdentity(t *testing.T) {
	for _, role := range []models.SwarmRole{models.SwarmRolePlanner, models.SwarmRoleWorker, models.SwarmRoleReviewer, models.SwarmRoleMerger} {
		t.Run(string(role), func(t *testing.T) {
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
			parent := models.Task{ProjectID: "default", Title: "Parent", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, SwarmRole: models.SwarmRoleParent}
			require.NoError(t, tasks.Create(admitted, &parent))
			child := models.Task{ProjectID: "default", Title: "Child", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, ParentTaskID: &parent.ID, SwarmRole: role}
			require.ErrorIs(t, tasks.Create(models.WithPrincipal(ctx, models.Principal{UserID: "substitute", Role: "owner"}), &child), persistence.ErrProjectDenied)
			foreign := child
			foreign.ProjectID = "foreign"
			require.ErrorIs(t, tasks.Create(ctx, &foreign), persistence.ErrProjectDenied)
			mismatch := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: "999999", UserID: models.BootstrapUserID})
			require.ErrorIs(t, tasks.Create(mismatch, &child), persistence.ErrProjectDenied)
			require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
			require.Error(t, tasks.Create(ctx, &child))
			bind()
			_, err := db.Exec(`CREATE TRIGGER fail_inherited_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='discord.task_created' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, tasks.Create(ctx, &child), "audit unavailable")
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM tasks WHERE parent_task_id=?`, parent.ID).Scan(&count))
			require.Zero(t, count)
			_, err = db.Exec(`DROP TRIGGER fail_inherited_audit`)
			require.NoError(t, err)
			goal := &models.TaskGoal{Objective: "Finish child"}
			require.NoError(t, tasks.CreateWithGoal(ctx, &child, goal))
			require.Equal(t, models.BootstrapUserID, goal.RunAsUserID)
			identities := NewRunIdentityRepo(db)
			user, err := identities.TaskUser(ctx, child.ID)
			require.NoError(t, err)
			require.Equal(t, models.BootstrapUserID, user)
			// Child evidence is independent of the original parent snapshot.
			_, err = db.Exec(`DELETE FROM discord_task_provenance WHERE task_id=?`, parent.ID)
			require.NoError(t, err)
			agent := createThreadInputLLMConfig(t, owner, db)
			run := models.Execution{TaskID: child.ID, AgentConfigID: agent.ID, Status: models.ExecQueued}
			require.NoError(t, NewExecutionRepo(db).Create(owner, &run))
			require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
			_, err = identities.TaskUser(ctx, child.ID)
			require.Error(t, err)
			require.Error(t, identities.CheckExecutionGoal(owner, run.ID, child.ID))
			require.Error(t, NewExecutionRepo(db).MarkRunning(owner, run.ID))
		})
	}
}
