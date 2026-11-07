package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestChannelAllowlistTransactions(t *testing.T) {
	for _, provider := range []string{"telegram", "slack", "discord", "email"} {
		t.Run(provider, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('manager','manager'); INSERT INTO org_memberships VALUES('default','manager','admin')`)
			require.NoError(t, err)
			actor := models.WithPrincipal(ctx, models.Principal{UserID: "manager", Role: "owner"})
			var create func(context.Context, string) (string, error)
			var remove func(context.Context, string, string) error
			table := provider + "_authorized_users"
			switch provider {
			case "telegram":
				r := NewTelegramAuthRepo(db)
				remove = r.DeleteForProject
				create = func(c context.Context, name string) (string, error) {
					u := &models.TelegramAuthorizedUser{ProjectID: "default", TelegramUserID: 123, DisplayName: name}
					err := r.Create(c, u)
					return u.ID, err
				}
			case "slack":
				r := NewSlackAuthRepo(db)
				remove = r.DeleteForProject
				create = func(c context.Context, name string) (string, error) {
					u := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123", DisplayName: name}
					err := r.Create(c, u)
					return u.ID, err
				}
			case "discord":
				r := NewDiscordAuthRepo(db)
				remove = r.DeleteForProject
				create = func(c context.Context, name string) (string, error) {
					u := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123", DisplayName: name}
					err := r.Create(c, u)
					return u.ID, err
				}
			case "email":
				table = "email_authorized_senders"
				r := NewEmailAuthRepo(db)
				remove = r.DeleteForProject
				create = func(c context.Context, name string) (string, error) {
					u := &models.EmailAuthorizedSender{ProjectID: "default", EmailAddress: "user@example.test", DisplayName: name}
					err := r.Create(c, u)
					return u.ID, err
				}
			}
			id, err := create(actor, "original")
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE org_memberships SET role='member' WHERE user_id='manager'`)
			require.NoError(t, err)
			_, err = create(actor, "denied")
			require.ErrorIs(t, err, persistence.ErrProjectDenied)
			require.ErrorIs(t, remove(actor, "default", id), persistence.ErrProjectDenied)
			_, err = db.Exec(`UPDATE org_memberships SET role='admin' WHERE user_id='manager'`)
			require.NoError(t, err)
			// Project selection no longer controls global sender deletion.
			_, err = db.Exec(`CREATE TRIGGER fail_allowlist_audit BEFORE INSERT ON channel_sender_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			_, err = create(actor, "rolled back")
			require.ErrorContains(t, err, "audit unavailable")
			require.ErrorContains(t, remove(actor, "default", id), "audit unavailable")
			var display string
			require.NoError(t, db.QueryRow(`SELECT display_name FROM `+table+` WHERE id=?`, id).Scan(&display))
			require.Equal(t, "original", display)
			_, err = db.Exec(`DROP TRIGGER fail_allowlist_audit`)
			require.NoError(t, err)
			require.NoError(t, remove(actor, "default", id))
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM channel_sender_audit WHERE action IN ('saved','removed')`).Scan(&count))
			require.Equal(t, 2, count)
		})
	}
}
