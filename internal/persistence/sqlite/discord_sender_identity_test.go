package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDiscordSenderIdentityAttestationAndRevocation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	repo := NewDiscordAuthRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	forged := models.WithPrincipal(ctx, models.Principal{UserID: "sender", Role: "owner"})
	row := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789012345678"}
	require.NoError(t, repo.Create(owner, row))
	_, err = repo.ResolveApplicationUser(ctx, "default", row.DiscordUserID)
	require.Error(t, err, "existing allowlist must not imply application identity")
	require.Error(t, repo.BindApplicationUser(ctx, "default", row.ID, "sender"))
	require.Error(t, repo.BindApplicationUser(forged, "default", row.ID, "sender"))
	require.Error(t, repo.BindApplicationUser(owner, "foreign", row.ID, "sender"))
	require.NoError(t, repo.BindApplicationUser(owner, "default", row.ID, "sender"))
	user, err := NewDiscordAuthRepo(db).ResolveApplicationUser(ctx, "default", row.DiscordUserID)
	require.NoError(t, err)
	require.Equal(t, "sender", user)
	_, err = repo.ResolveApplicationUser(ctx, "foreign", row.DiscordUserID)
	require.Error(t, err)
	_, err = repo.ResolveApplicationUser(ctx, "default", "999")
	require.Error(t, err)
	var actor, details string
	require.NoError(t, db.QueryRow(`SELECT actor_user_id,details FROM project_audit_events WHERE action='discord.sender_bound'`).Scan(&actor, &details))
	require.Equal(t, models.BootstrapUserID, actor)
	require.Contains(t, details, row.DiscordUserID)
	require.Contains(t, details, `"application_user_id":"sender"`)
	_, err = db.Exec(`CREATE TRIGGER fail_discord_binding_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, repo.BindApplicationUser(owner, "default", row.ID, models.BootstrapUserID))
	user, err = repo.ResolveApplicationUser(ctx, "default", row.DiscordUserID)
	require.NoError(t, err)
	require.Equal(t, "sender", user)
	_, err = db.Exec(`DROP TRIGGER fail_discord_binding_audit`)
	require.NoError(t, err)
	access := NewProjectAccessRepo(db)
	policy, err := access.Get(ctx, "default")
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, access.Update(ctx, models.BootstrapUserID, *policy))
	_, err = repo.ResolveApplicationUser(ctx, "default", row.DiscordUserID)
	require.Error(t, err)
	require.Error(t, repo.BindApplicationUser(owner, "default", row.ID, "sender"))
	require.NoError(t, NewProjectAccessRepo(db).SetMember(ctx, models.BootstrapUserID, "default", "sender", "editor"))
	_, err = db.Exec(`UPDATE users SET disabled=1 WHERE id='sender'`)
	require.NoError(t, err)
	_, err = repo.ResolveApplicationUser(ctx, "default", row.DiscordUserID)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE users SET disabled=0 WHERE id='sender'; DELETE FROM org_memberships WHERE user_id='sender'`)
	require.NoError(t, err)
	_, err = repo.ResolveApplicationUser(ctx, "default", row.DiscordUserID)
	require.Error(t, err)
	require.NoError(t, repo.DeleteForProject(owner, "default", row.ID))
	_, err = repo.ResolveApplicationUser(ctx, "default", row.DiscordUserID)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_sender_identities`).Scan(&count))
	require.Zero(t, count)
}

func TestDiscordSenderBindingRejectsMutableExternalIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	repo := NewDiscordAuthRepo(db)
	for _, external := range []string{"display-name", "0", "00123", "18446744073709551616"} {
		row := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: external}
		require.NoError(t, repo.Create(owner, row))
		require.Error(t, repo.BindApplicationUser(owner, "default", row.ID, models.BootstrapUserID))
	}
}
