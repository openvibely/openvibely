package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestBindTaskRechecksRequiredRoleTransactionally(t *testing.T) {
	for _, kind := range []string{"ordinary", "merge", "goal-merge", "webhook", "automation"} {
		t.Run(kind, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			task := createThreadInputTask(t, owner, db, "default")
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('runner','runner'); INSERT INTO org_memberships VALUES('default','runner','member'); INSERT INTO project_members VALUES('default','runner','editor')`)
			require.NoError(t, err)
			switch kind {
			case "merge":
				_, err = db.Exec(`UPDATE tasks SET auto_merge=1 WHERE id=?`, task.ID)
			case "goal-merge":
				_, err = db.Exec(`UPDATE tasks SET auto_merge_on_goal_achieved=1 WHERE id=?`, task.ID)
			case "webhook":
				_, err = db.Exec(`UPDATE tasks SET created_via=? WHERE id=?`, models.TaskOriginWebhook, task.ID)
			case "automation":
				_, err = db.Exec(`UPDATE tasks SET created_via=? WHERE id=?`, persistence.AutomationCompilerTaskCreatedVia("automation", "node"), task.ID)
			}
			require.NoError(t, err)
			identities := NewRunIdentityRepo(db)
			require.NoError(t, identities.BindTask(owner, task.ID))
			actor := models.WithPrincipal(ctx, models.Principal{UserID: "runner", Role: "owner"})
			var before int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE target_id=? AND action='task.run_as'`, task.ID).Scan(&before))
			if kind == "ordinary" {
				require.NoError(t, identities.BindTask(actor, task.ID))
				return
			}
			require.ErrorIs(t, identities.BindTask(actor, task.ID), persistence.ErrProjectDenied)
			saved, err := identities.TaskUser(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, models.BootstrapUserID, saved)
			var after int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE target_id=? AND action='task.run_as'`, task.ID).Scan(&after))
			require.Equal(t, before, after)
			access := NewProjectAccessRepo(db)
			require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "runner", "maintainer"))
			require.NoError(t, identities.BindTask(actor, task.ID))
			require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "runner", ""))
			require.ErrorIs(t, identities.BindTask(actor, task.ID), persistence.ErrProjectDenied)
		})
	}
}
