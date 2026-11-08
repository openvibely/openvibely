package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGlobalChannelSenderAssignmentPermissions(t *testing.T) {
	for _, channel := range []string{"telegram", "slack", "discord", "email"} {
		t.Run(channel, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('alice','alice'),('bob','bob'); INSERT INTO org_memberships VALUES('default','alice','member'),('default','bob','member')`)
			require.NoError(t, err)
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			alice := models.WithPrincipal(ctx, models.Principal{UserID: "alice", Role: "owner"}) // forged role must be ignored
			bob := models.WithPrincipal(ctx, models.Principal{UserID: "bob"})
			var create func(context.Context, string, string) (string, error)
			var bind func(context.Context, string, string, string) error
			var remove func(context.Context, string) error
			table := channel + "_authorized_users"
			switch channel {
			case "telegram":
				r := NewTelegramAuthRepo(db)
				bind = r.BindSender
				remove = r.Delete
				create = func(c context.Context, user, project string) (string, error) {
					v := &models.TelegramAuthorizedUser{ProjectID: project, TelegramUserID: 123, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: user}}
					e := r.Create(c, v)
					return v.ID, e
				}
			case "slack":
				r := NewSlackAuthRepo(db)
				bind = r.BindSender
				remove = r.Delete
				create = func(c context.Context, user, project string) (string, error) {
					v := &models.SlackAuthorizedUser{ProjectID: project, SlackUserID: "U123", ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: user, TeamID: "T123"}}
					e := r.Create(c, v)
					return v.ID, e
				}
			case "discord":
				r := NewDiscordAuthRepo(db)
				bind = r.BindSender
				remove = r.Delete
				create = func(c context.Context, user, project string) (string, error) {
					v := &models.DiscordAuthorizedUser{ProjectID: project, DiscordUserID: "123", ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: user}}
					e := r.Create(c, v)
					return v.ID, e
				}
			case "email":
				table = "email_authorized_senders"
				r := NewEmailAuthRepo(db)
				bind = r.BindSender
				remove = r.Delete
				create = func(c context.Context, user, project string) (string, error) {
					v := &models.EmailAuthorizedSender{ProjectID: project, EmailAddress: "Alice@Example.com", ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: user}}
					e := r.Create(c, v)
					return v.ID, e
				}
			}
			_, err = create(alice, "bob", "default")
			require.ErrorIs(t, err, persistence.ErrProjectDenied)
			id, err := create(alice, "alice", "default")
			require.NoError(t, err)
			other, err := create(alice, "alice", "another-project")
			require.NoError(t, err)
			require.Equal(t, id, other)
			_, err = create(bob, "bob", "")
			require.ErrorIs(t, err, persistence.ErrProjectDenied)
			require.ErrorIs(t, bind(alice, id, "bob", "T123"), persistence.ErrProjectDenied)
			require.ErrorIs(t, remove(bob, id), persistence.ErrProjectDenied)
			require.NoError(t, bind(owner, id, "bob", "T123"))
			require.ErrorIs(t, remove(alice, id), persistence.ErrProjectDenied)
			_, err = db.Exec(`UPDATE users SET disabled=1 WHERE id='bob'`)
			require.NoError(t, err)
			require.ErrorIs(t, bind(owner, id, "bob", "T123"), persistence.ErrProjectDenied)
			require.ErrorIs(t, remove(bob, id), persistence.ErrProjectDenied)
			require.NoError(t, remove(owner, id))
			var n int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
			require.Zero(t, n)
		})
	}
}

func TestTelegramGlobalMappingRequiresNumericIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	r := NewTelegramAuthRepo(db)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	u := &models.TelegramAuthorizedUser{TelegramUsername: "alice", ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: models.BootstrapUserID}}
	require.ErrorIs(t, r.Create(ctx, u), persistence.ErrProjectDenied)
	u.ApplicationUserID = ""
	require.NoError(t, r.Create(ctx, u))
	require.ErrorIs(t, r.BindSender(ctx, u.ID, models.BootstrapUserID, ""), persistence.ErrProjectDenied)
	_, err := r.ResolveApplicationUser(ctx, 123)
	require.Error(t, err)
}
