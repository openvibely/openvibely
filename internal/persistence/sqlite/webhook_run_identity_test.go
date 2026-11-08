package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWebhookIdentityAuditAtomic(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	repo := NewWebhookRepo(db)
	endpoint := &models.WebhookEndpoint{ProjectID: "default", Name: "Audited endpoint", Enabled: true, DefaultPriority: 2}
	_, err := db.Exec(`CREATE TRIGGER reject_webhook_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='webhook.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, repo.Create(ctx, endpoint))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM webhook_endpoints`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER reject_webhook_audit`)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, endpoint))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='webhook.run_as' AND target_id=? AND actor_user_id=?`, endpoint.ID, models.BootstrapUserID).Scan(&count))
	require.Equal(t, 1, count)
}
