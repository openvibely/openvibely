package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDiscordTaskAdmissionAndRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "sender"))
	identity := models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: "sender"}
	admitted := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: "sender"}), identity)
	repo := NewTaskRepo(db, nil)
	fresh := func() *models.Task {
		return &models.Task{ProjectID: "default", Title: "Discord task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, CreatedVia: models.TaskOriginDiscord}
	}
	require.Error(t, repo.Create(models.WithDiscordIngressIdentity(owner, identity), fresh()))
	require.Error(t, repo.Create(models.WithDiscordIngressIdentity(ctx, identity), fresh()))
	foreign := fresh()
	foreign.ProjectID = "foreign"
	require.Error(t, repo.Create(admitted, foreign))
	privileged := fresh()
	privileged.AutoMerge = true
	require.Error(t, repo.Create(admitted, privileged))
	_, err = db.Exec(`CREATE TRIGGER fail_discord_task_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='discord.task_created' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
	require.NoError(t, err)
	require.Error(t, repo.Create(admitted, fresh()))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_task_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tasks WHERE title='Discord task'`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_discord_task_audit`)
	require.NoError(t, err)
	task := fresh()
	require.NoError(t, repo.Create(admitted, task))
	runs := NewRunIdentityRepo(db)
	user, err := runs.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "sender", user)
	require.Error(t, runs.BindTask(owner, task.ID), "owner cannot replace captured sender")
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	_, err = runs.TaskUser(owner, task.ID)
	require.Error(t, err, "fresh owner cannot replace saved evidence")
	next := fresh()
	next.Title = "after rebind"
	require.Error(t, repo.Create(admitted, next))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "sender"))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	_, err = runs.TaskUser(ctx, task.ID)
	require.Error(t, err)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_task_provenance WHERE task_id=?`, task.ID).Scan(&count))
	require.Equal(t, 1, count, "live binding removal retains denial evidence")
}
