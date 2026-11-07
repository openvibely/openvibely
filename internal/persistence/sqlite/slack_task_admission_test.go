package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSlackTaskAdmissionRechecksSender(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member'); INSERT INTO project_members VALUES('default','sender','editor')`)
	require.NoError(t, err)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	admitted := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: "sender"}), models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: "sender"})
	repo := NewTaskRepo(db, nil)
	fresh := func() *models.Task {
		return &models.Task{ProjectID: "default", Title: "Slack action", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	}
	noWrites := func() {
		t.Helper()
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count))
		require.Zero(t, count)
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_run_identities`).Scan(&count))
		require.Zero(t, count)
	}
	privileged := fresh()
	privileged.AutoMerge = true
	require.Error(t, repo.Create(admitted, privileged))
	noWrites()
	foreign := fresh()
	foreign.ProjectID = "foreign"
	require.Error(t, repo.Create(admitted, foreign))
	noWrites()
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	require.Error(t, repo.Create(admitted, fresh()))
	noWrites()
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	_, err = db.Exec(`UPDATE project_members SET role='viewer' WHERE project_id='default' AND user_id='sender'`)
	require.NoError(t, err)
	require.Error(t, repo.Create(admitted, fresh()))
	noWrites()
	_, err = db.Exec(`UPDATE project_members SET role='editor' WHERE project_id='default' AND user_id='sender'`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER fail_task_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	goal := &models.TaskGoal{Objective: "Finish"}
	called := false
	require.Error(t, repo.CreateWithGoalAndCallback(admitted, fresh(), goal, func(tx persistence.Tx) error { called = true; return nil }))
	require.False(t, called)
	noWrites()
	_, err = db.Exec(`DROP TRIGGER fail_task_audit`)
	require.NoError(t, err)
	task := fresh()
	require.NoError(t, repo.CreateWithGoal(admitted, task, goal))
	var userID string
	require.NoError(t, db.QueryRow(`SELECT user_id FROM task_run_identities WHERE task_id=?`, task.ID).Scan(&userID))
	require.Equal(t, "sender", userID)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='slack.task_created' AND target_id=?`, task.ID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, NewProjectAccessRepo(db).SetMember(ctx, models.BootstrapUserID, "default", "sender", "maintainer"))
	privileged = fresh()
	privileged.Title = "Privileged Slack task"
	privileged.AutoMerge = true
	require.NoError(t, repo.Create(admitted, privileged))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, repo.Create(admitted, fresh()))
}
