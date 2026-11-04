package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChannelCredentialWriteRechecksActorAndAuditsAtomically(t *testing.T) {
	for _, key := range []string{"slack_bot_token", "github_app_installation_id", "telegram_bot_token", "discord_bot_token", "x_access_token_secret", "x_run_as_user_id", "email_password", "email_smtp_host", "telegram_send_responses"} {
		t.Run(key, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			r := NewSettingsRepo(db)
			ctx := t.Context()
			require.NoError(t, r.Set(ctx, key, "old-secret"))
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('connector','connector'); INSERT INTO org_memberships VALUES('default','connector','admin')`)
			require.NoError(t, err)
			actor := models.WithPrincipal(ctx, models.Principal{UserID: "connector", Role: "owner"})
			// Callback has already passed its initial check. Demote during exchange.
			_, err = db.Exec(`UPDATE org_memberships SET role='member' WHERE user_id='connector'`)
			require.NoError(t, err)
			values := map[string]string{key: "new-secret", "unrelated-result": "must-rollback"}
			require.ErrorIs(t, r.SetMany(actor, values), persistence.ErrOrgDenied)
			got, err := r.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "old-secret", got)
			got, err = r.Get(ctx, "unrelated-result")
			require.NoError(t, err)
			require.Empty(t, got)
			require.ErrorIs(t, r.Set(actor, key, "single-write"), persistence.ErrOrgDenied)
			_, err = db.Exec(`UPDATE org_memberships SET role='admin' WHERE user_id='connector'; CREATE TRIGGER reject_credential_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, r.SetMany(actor, values), "audit unavailable")
			got, err = r.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "old-secret", got)
			_, err = db.Exec(`DROP TRIGGER reject_credential_audit`)
			require.NoError(t, err)
			require.NoError(t, r.SetMany(actor, values))
			var details, who string
			require.NoError(t, db.QueryRow(`SELECT actor_user_id,details FROM project_audit_events WHERE action='channel.credentials_changed'`).Scan(&who, &details))
			require.Equal(t, "connector", who)
			require.Contains(t, details, key)
			require.NotContains(t, details, "new-secret")
			require.NotContains(t, details, "old-secret")
			require.NotContains(t, details, "must-rollback")
			_, err = db.Exec(`UPDATE users SET disabled=1 WHERE id='connector'`)
			require.NoError(t, err)
			require.ErrorIs(t, r.Set(actor, key, "disabled-write"), persistence.ErrOrgDenied)
			got, err = r.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "new-secret", got)
		})
	}
}

func TestConditionalAndBulkCredentialsCannotBypassAuthorization(t *testing.T) {
	for _, kind := range []string{"cas", "cas-many", "bulk"} {
		t.Run(kind, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			r := NewSettingsRepo(db)
			const key = "slack_bot_token"
			require.NoError(t, r.Set(ctx, key, "old-secret"))
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('manager','manager'); INSERT INTO org_memberships VALUES('default','manager','member')`)
			require.NoError(t, err)
			actor := models.WithPrincipal(ctx, models.Principal{UserID: "manager", Role: "owner"})
			write := func(expected string) (bool, error) {
				switch kind {
				case "cas":
					return r.CompareAndSet(actor, key, expected, "new-secret", nil)
				case "cas-many":
					return r.CompareAndSetMany(actor, map[string]string{key: expected}, map[string]string{key: "new-secret"}, nil)
				default:
					err := r.RemoveChannelsBulk(actor, "default", map[string]string{key: "new-secret"}, nil, false)
					return err == nil, err
				}
			}
			for _, expected := range []string{"old-secret", "wrong-secret"} {
				changed, err := write(expected)
				require.ErrorIs(t, err, persistence.ErrOrgDenied)
				require.False(t, changed)
			}
			got, err := r.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "old-secret", got)
			_, err = db.Exec(`UPDATE org_memberships SET role='admin' WHERE user_id='manager'`)
			require.NoError(t, err)
			if kind != "bulk" {
				changed, err := write("wrong-secret")
				require.NoError(t, err)
				require.False(t, changed)
				var n int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE actor_user_id='manager'`).Scan(&n))
				require.Zero(t, n)
			}
			_, err = db.Exec(`CREATE TRIGGER fail_bulk_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			changed, err := write("old-secret")
			require.ErrorContains(t, err, "audit unavailable")
			require.False(t, changed)
			got, err = r.Get(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "old-secret", got)
			_, err = db.Exec(`DROP TRIGGER fail_bulk_audit`)
			require.NoError(t, err)
			changed, err = write("old-secret")
			require.NoError(t, err)
			require.True(t, changed)
			var details string
			require.NoError(t, db.QueryRow(`SELECT details FROM project_audit_events WHERE actor_user_id='manager'`).Scan(&details))
			require.Contains(t, details, key)
			require.NotContains(t, details, "new-secret")
			require.NotContains(t, details, "old-secret")
		})
	}
}

func TestBulkChannelRemovalRechecksWebhookMaintainer(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	settings := NewSettingsRepo(db)
	webhooks := NewWebhookRepo(db)
	endpoint := &models.WebhookEndpoint{ProjectID: "default", Name: "preserved", Enabled: true}
	require.NoError(t, webhooks.Create(ctx, endpoint))
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('maintainer','maintainer'); INSERT INTO org_memberships VALUES('default','maintainer','member')`)
	require.NoError(t, err)
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "maintainer", "maintainer"))
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "maintainer"})
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "maintainer", ""))
	require.ErrorIs(t, settings.RemoveChannelsBulk(actor, "default", nil, []string{endpoint.ID}, false), persistence.ErrProjectDenied)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "maintainer", "maintainer"))
	// Project management does not authorize changing global credentials in a mixed request.
	require.ErrorIs(t, settings.RemoveChannelsBulk(actor, "default", map[string]string{"slack_bot_token": ""}, []string{endpoint.ID}, false), persistence.ErrOrgDenied)
	got, err := webhooks.GetByID(ctx, endpoint.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	_, err = db.Exec(`CREATE TRIGGER fail_removal_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, settings.RemoveChannelsBulk(actor, "default", nil, []string{endpoint.ID}, false), "audit unavailable")
	got, err = webhooks.GetByID(ctx, endpoint.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	_, err = db.Exec(`DROP TRIGGER fail_removal_audit`)
	require.NoError(t, err)
	require.NoError(t, settings.RemoveChannelsBulk(actor, "default", nil, []string{endpoint.ID}, false))
	got, err = webhooks.GetByID(ctx, endpoint.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestChannelOperationalCursorDoesNotGrantCredentialAccess(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	r := NewSettingsRepo(db)
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('poller','poller'); INSERT INTO org_memberships VALUES('default','poller','member')`)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "poller"})
	// Cursor updates are operational bookkeeping; mixing in configuration must
	// still reject the whole batch before either value can change.
	changed, err := r.CompareAndSetMany(actor, map[string]string{"x_mentions_since_id": ""}, map[string]string{"x_mentions_since_id": "1"}, nil)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = r.CompareAndSetMany(actor, map[string]string{"x_mentions_since_id": "1"}, map[string]string{"x_mentions_since_id": "2", "x_access_token": "injected"}, nil)
	require.ErrorIs(t, err, persistence.ErrOrgDenied)
	require.False(t, changed)
	got, err := r.Get(ctx, "x_mentions_since_id")
	require.NoError(t, err)
	require.Equal(t, "1", got)
	got, err = r.Get(ctx, "x_access_token")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDiscordRemovalRollsBackCredentialsAndAuthorization(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	settings := NewSettingsRepo(db)
	users := NewDiscordAuthRepo(db)
	require.NoError(t, settings.Set(ctx, "discord_bot_token", "preserved"))
	require.NoError(t, users.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123", AddedBy: "test"}))
	actor := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	for _, table := range []string{"discord_authorized_users", "project_audit_events"} {
		operation := "DELETE"
		if table == "project_audit_events" {
			operation = "INSERT"
		}
		_, err := db.Exec("CREATE TRIGGER fail_discord_removal BEFORE " + operation + " ON " + table + " BEGIN SELECT RAISE(ABORT,'removal unavailable'); END")
		require.NoError(t, err)
		require.ErrorContains(t, settings.RemoveDiscordChannel(actor, "default", map[string]string{"discord_bot_token": ""}), "removal unavailable")
		token, err := settings.Get(ctx, "discord_bot_token")
		require.NoError(t, err)
		require.Equal(t, "preserved", token)
		allowed, err := users.ListByProject(ctx, "default")
		require.NoError(t, err)
		require.Len(t, allowed, 1)
		_, err = db.Exec("DROP TRIGGER fail_discord_removal")
		require.NoError(t, err)
	}
	require.NoError(t, settings.RemoveDiscordChannel(actor, "default", map[string]string{"discord_bot_token": ""}))
	allowed, err := users.ListByProject(ctx, "default")
	require.NoError(t, err)
	require.Empty(t, allowed)
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM project_audit_events WHERE action='channels.removed'").Scan(&count))
	require.Equal(t, 1, count)
}
