package sqlite

import (
	"errors"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackTaskDeletionRechecksBeforeCleanup(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: models.BootstrapUserID})
	repo := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: "default", Title: "Delete target", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, repo.Create(ctx, task))
	called := false
	cleanup := func(persistence.TaskDeletionManifest) error { called = true; return nil }
	actions := map[string]func() error{
		"direct":   func() error { return repo.Delete(admitted, task.ID) },
		"manifest": func() error { _, _, err := repo.DeleteWithCleanupManifest(admitted, task.ID, cleanup); return err },
		"category": func() error {
			_, _, err := repo.DeleteWithCleanupManifestIfCategory(admitted, task.ID, "default", models.CategoryBacklog, cleanup)
			return err
		},
		"observed": func() error {
			_, _, err := repo.DeleteWithCleanupManifestIfObserved(admitted, task, cleanup)
			return err
		},
	}
	unchanged := func() {
		t.Helper()
		saved, err := repo.GetByID(ctx, task.ID)
		require.NoError(t, err)
		require.NotNil(t, saved)
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='slack.task_deleted'`).Scan(&count))
		require.Zero(t, count)
	}
	denied := func() {
		t.Helper()
		for name, action := range actions {
			called = false
			require.Error(t, action(), name)
			require.False(t, called, name)
			unchanged()
		}
	}
	original := admitted
	admitted = models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "foreign", UserID: models.BootstrapUserID})
	denied()
	admitted = original
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	denied()
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	_, err := db.Exec(`CREATE TRIGGER fail_delete_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	denied()
	_, err = db.Exec(`DROP TRIGGER fail_delete_audit`)
	require.NoError(t, err)
	_, deleted, err := repo.DeleteWithCleanupManifest(admitted, task.ID, func(persistence.TaskDeletionManifest) error { return errors.New("cleanup refused") })
	require.Error(t, err)
	require.False(t, deleted)
	unchanged()
	called = false
	_, deleted, err = repo.DeleteWithCleanupManifest(admitted, task.ID, cleanup)
	require.NoError(t, err)
	require.True(t, deleted)
	require.True(t, called)
	saved, err := repo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Nil(t, saved)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='slack.task_deleted' AND target_id=?`, task.ID).Scan(&count))
	require.Equal(t, 1, count)
}
