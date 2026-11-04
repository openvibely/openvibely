package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGitHubInboxAuthorizationAndAudit(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewGitHubAuthRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('manager','manager'); INSERT INTO org_memberships VALUES('default','manager','member')`)
	require.NoError(t, err)
	manager := models.WithPrincipal(ctx, models.Principal{UserID: "manager", Role: "owner"})
	inbox := &models.GitHubProjectInbox{ProjectID: "default", GitHubLogin: "reviewer", Enabled: true}
	require.NoError(t, repo.UpsertProjectInbox(owner, inbox))
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, NewProjectAccessRepo(db).SetMember(ctx, models.BootstrapUserID, "default", "manager", role))
		inbox.GitHubLogin = role
		err := repo.UpsertProjectInbox(manager, inbox)
		if role == "maintainer" {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, persistence.ErrProjectDenied)
		}
	}
	require.ErrorIs(t, repo.UpsertProjectInbox(manager, &models.GitHubProjectInbox{ProjectID: "foreign", GitHubLogin: "reviewer", Enabled: true}), persistence.ErrProjectDenied)
	_, err = db.Exec(`CREATE TRIGGER reject_inbox_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='github.inbox_updated' BEGIN SELECT RAISE(ABORT,'inbox audit unavailable'); END`)
	require.NoError(t, err)
	inbox.Enabled = false
	inbox.GitHubLogin = "changed"
	require.ErrorContains(t, repo.UpsertProjectInbox(manager, inbox), "inbox audit unavailable")
	saved, err := repo.GetProjectInbox(ctx, "default")
	require.NoError(t, err)
	require.True(t, saved.Enabled)
	require.Equal(t, "maintainer", saved.GitHubLogin)
	_, err = db.Exec(`DROP TRIGGER reject_inbox_audit; DELETE FROM org_memberships WHERE user_id='manager'`)
	require.NoError(t, err)
	require.ErrorIs(t, repo.UpsertProjectInbox(manager, inbox), persistence.ErrProjectDenied)
	saved, err = repo.GetProjectInbox(ctx, "default")
	require.NoError(t, err)
	require.True(t, saved.Enabled)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='github.inbox_updated'`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, repo.UpsertProjectInbox(ctx, inbox), "unconfigured embedded repository retains legacy contract")
}
