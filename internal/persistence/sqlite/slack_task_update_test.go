package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackTaskUpdateRechecksBindingAndAudits(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	admitted := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: "sender"}), models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: "sender"})
	repo := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: "default", Title: "Original", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, repo.Create(admitted, task))
	unchanged := func() {
		t.Helper()
		var title string
		var merge bool
		require.NoError(t, db.QueryRow(`SELECT title,auto_merge FROM tasks WHERE id=?`, task.ID).Scan(&title, &merge))
		require.Equal(t, "Original", title)
		require.False(t, merge)
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='slack.task_updated'`).Scan(&count))
		require.Zero(t, count)
	}
	task.Title = "Changed"
	task.AutoMerge = true
	require.Error(t, repo.Update(admitted, task))
	unchanged()
	task.AutoMerge = false
	task.ProjectID = "foreign"
	require.Error(t, repo.Update(admitted, task))
	unchanged()
	task.ProjectID = "default"
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	require.Error(t, repo.Update(admitted, task))
	unchanged()
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	_, err = db.Exec(`UPDATE project_organizations SET org_role='viewer' WHERE project_id='default'`)
	require.NoError(t, err)
	require.Error(t, repo.Update(admitted, task))
	unchanged()
	_, err = db.Exec(`UPDATE project_organizations SET org_role='editor' WHERE project_id='default'; CREATE TRIGGER fail_update_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, repo.Update(admitted, task))
	unchanged()
	_, err = db.Exec(`DROP TRIGGER fail_update_audit`)
	require.NoError(t, err)
	require.NoError(t, repo.Update(admitted, task))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='slack.task_updated' AND actor_user_id='sender'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	task.Title = "After deletion"
	require.Error(t, repo.Update(admitted, task))
	var title string
	require.NoError(t, db.QueryRow(`SELECT title FROM tasks WHERE id=?`, task.ID).Scan(&title))
	require.Equal(t, "Changed", title)
}
