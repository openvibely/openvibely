package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackGoalMutationsRecheckBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member'); INSERT INTO project_members VALUES('default','sender','editor')`)
	require.NoError(t, err)
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	admitted := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: "sender"}), models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: "sender"})
	tasks := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: "default", Title: "Goal task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, tasks.Create(admitted, task))
	goals := NewTaskGoalRepo(db)
	require.NoError(t, goals.CreateOrReplace(admitted, &models.TaskGoal{TaskID: task.ID, GoalID: "original", Objective: "Original"}))
	original, err := goals.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	actions := map[string]func() error{
		"replace": func() error {
			return goals.CreateOrReplace(admitted, &models.TaskGoal{TaskID: task.ID, GoalID: "replacement", Objective: "Replacement"})
		},
		"pause": func() error {
			_, err := goals.UpdateStatus(admitted, task.ID, "original", models.TaskGoalStatus("paused"), "pause", false)
			return err
		},
		"resume": func() error {
			_, err := goals.UpdateStatus(admitted, task.ID, "original", "active", "resume", true)
			return err
		},
		"clear":   func() error { return goals.Clear(admitted, task.ID, "clear") },
		"delete":  func() error { return goals.Delete(admitted, task.ID) },
		"achieve": func() error { _, err := goals.MarkAchieved(admitted, task.ID, "original", "done"); return err },
	}
	checkDenied := func() {
		t.Helper()
		for name, action := range actions {
			require.Error(t, action(), name)
			saved, err := goals.GetByTaskID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, original, saved, name)
		}
	}
	originalContext := admitted
	admitted = models.WithSlackIngressIdentity(admitted, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "foreign", UserID: "sender"})
	checkDenied()
	admitted = originalContext
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	checkDenied()
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	_, err = db.Exec(`UPDATE project_members SET role='viewer' WHERE project_id='default' AND user_id='sender'`)
	require.NoError(t, err)
	checkDenied()
	_, err = db.Exec(`UPDATE project_members SET role='editor' WHERE project_id='default' AND user_id='sender'; UPDATE tasks SET auto_merge=1 WHERE id=?`, task.ID)
	require.NoError(t, err)
	checkDenied()
	_, err = db.Exec(`UPDATE tasks SET auto_merge=0 WHERE id=?`, task.ID)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER fail_goal_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	checkDenied()
	_, err = db.Exec(`DROP TRIGGER fail_goal_audit`)
	require.NoError(t, err)
	stale, err := goals.UpdateStatus(admitted, task.ID, "stale", "paused", "ignored", false)
	require.NoError(t, err)
	require.Nil(t, stale)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action LIKE 'slack.goal_%'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, actions["pause"]())
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='slack.goal_updated' AND actor_user_id='sender'`).Scan(&count))
	require.Equal(t, 1, count)
	original, err = goals.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	checkDenied()
}
