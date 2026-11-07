package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackSenderBindingAndHandoff(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	repo := NewSlackAuthRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	sender := models.WithPrincipal(ctx, models.Principal{UserID: "sender", Role: "owner"})
	row := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, repo.Create(owner, row))
	require.Error(t, repo.BindApplicationUser(ctx, "default", row.ID, "T123", "sender"))
	require.Error(t, repo.BindApplicationUser(sender, "default", row.ID, "T123", "sender"))
	require.NoError(t, repo.BindApplicationUser(owner, "foreign", row.ID, "T123", "sender"))
	require.NoError(t, repo.BindApplicationUser(owner, "default", row.ID, "T123", "sender"))
	user, err := NewSlackAuthRepo(db).ResolveApplicationUser(ctx, "default", "T123", "U123")
	require.NoError(t, err)
	require.Equal(t, "sender", user)
	_, err = repo.ResolveApplicationUser(ctx, "default", "TOTHER", "U123")
	require.Error(t, err)
	_, err = repo.ResolveApplicationUser(ctx, "foreign", "T123", "U123")
	require.NoError(t, err) // Identity is global; handoff checks the project role.
	identity := models.SlackIngressIdentity{ProjectID: "default", TeamID: "T123", ExternalUserID: "U123", UserID: "sender", EventKey: "event"}
	ingress := models.WithSlackIngressIdentity(sender, identity)
	receipts := NewSlackInboundReceiptRepo(db)
	called := false
	persist := func(tx persistence.Tx) error {
		called = true
		executor, err := executorFromTx(tx)
		if err != nil {
			return err
		}
		_, err = executor.ExecContext(ctx, `INSERT INTO app_settings(key,value) VALUES('slack_handoff_probe','saved')`)
		return err
	}
	_, err = receipts.WithHandoff(models.WithSlackIngressIdentity(owner, identity), "event", persist)
	require.Error(t, err)
	require.False(t, called)
	_, err = db.Exec(`CREATE TRIGGER fail_slack_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = receipts.WithHandoff(ingress, "event", persist)
	require.Error(t, err)
	exists, err := receipts.Exists(ctx, "event")
	require.NoError(t, err)
	require.False(t, exists)
	var workCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key='slack_handoff_probe'`).Scan(&workCount))
	require.Zero(t, workCount)
	_, err = db.Exec(`CREATE TRIGGER fail_slack_binding_audit BEFORE INSERT ON channel_sender_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, repo.BindApplicationUser(owner, "default", row.ID, "T123", models.BootstrapUserID))
	_, err = db.Exec(`DROP TRIGGER fail_slack_binding_audit`)
	require.NoError(t, err)
	user, err = repo.ResolveApplicationUser(ctx, "default", "T123", "U123")
	require.NoError(t, err)
	require.Equal(t, "sender", user)
	_, err = db.Exec(`DROP TRIGGER fail_slack_audit`)
	require.NoError(t, err)
	reused, err := receipts.WithHandoff(ingress, "event", persist)
	require.NoError(t, err)
	require.False(t, reused)
	_, err = db.Exec(`UPDATE project_organizations SET org_role='viewer' WHERE project_id='default'`)
	require.NoError(t, err)
	called = false
	_, err = receipts.WithHandoff(ingress, "event", persist)
	require.Error(t, err)
	require.False(t, called)
	_, err = db.Exec(`UPDATE project_organizations SET org_role='editor' WHERE project_id='default'`)
	require.NoError(t, err)
	require.NoError(t, repo.DeleteForProject(owner, "default", row.ID))
	_, err = receipts.WithHandoff(ingress, "event", persist)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM slack_sender_identities`).Scan(&count))
	require.Zero(t, count)
}
