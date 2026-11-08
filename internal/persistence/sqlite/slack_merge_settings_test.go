package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackMergeSettingsRecheckSender(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	admitted := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: "sender"}), models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: "sender"})
	repo := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: "default", Title: "Merge settings", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, repo.Create(ctx, task))
	enabled := true
	rejected := func() {
		t.Helper()
		require.Error(t, repo.UpdateAutoMerge(admitted, task.ID, true, "main"))
		require.Error(t, repo.UpdateAutoMergeSettings(admitted, task.ID, true, &enabled, "main"))
		var merge, goal bool
		var branch string
		require.NoError(t, db.QueryRow(`SELECT auto_merge,auto_merge_on_goal_achieved,merge_target_branch FROM tasks WHERE id=?`, task.ID).Scan(&merge, &goal, &branch))
		require.False(t, merge)
		require.False(t, goal)
		require.Empty(t, branch)
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='slack.task_merge_settings_updated'`).Scan(&count))
		require.Zero(t, count)
	}
	rejected() // An ordinary editor cannot enable either merge setting.
	require.NoError(t, NewProjectAccessRepo(db).SetMember(ctx, models.BootstrapUserID, "default", "sender", "maintainer"))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	rejected()
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	_, err = db.Exec(`CREATE TRIGGER fail_merge_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	rejected()
	_, err = db.Exec(`DROP TRIGGER fail_merge_audit`)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateAutoMergeSettings(admitted, task.ID, true, &enabled, "release"))
	require.NoError(t, repo.UpdateAutoMerge(admitted, task.ID, false, "release"))
	var merge, goal bool
	require.NoError(t, db.QueryRow(`SELECT auto_merge,auto_merge_on_goal_achieved FROM tasks WHERE id=?`, task.ID).Scan(&merge, &goal))
	require.False(t, merge)
	require.True(t, goal, "legacy setter preserves independent goal setting")
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	disabled := false
	require.Error(t, repo.UpdateAutoMergeSettings(admitted, task.ID, false, &disabled, "other"))
	var branch string
	require.NoError(t, db.QueryRow(`SELECT auto_merge_on_goal_achieved,merge_target_branch FROM tasks WHERE id=?`, task.ID).Scan(&goal, &branch))
	require.True(t, goal)
	require.Equal(t, "release", branch)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='slack.task_merge_settings_updated' AND actor_user_id='sender'`).Scan(&count))
	require.Equal(t, 2, count)
}
