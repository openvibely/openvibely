package sqlite

import (
	"context"

	"github.com/openvibely/openvibely/internal/persistence"
)

// BindApplicationUser updates the global sender mapping under the actor's assignment permissions.
func (r *DiscordAuthRepo) BindApplicationUser(ctx context.Context, projectID, allowlistID, userID string) error {
	return r.BindSender(ctx, allowlistID, userID, "")
}

func requireDiscordSenderRole(ctx context.Context, tx sqlExecutor, projectID, userID string) error {
	role, err := projectRole(ctx, tx, userID, projectID)
	if err != nil {
		return err
	}
	if role != "editor" && role != "maintainer" && role != "owner" {
		return persistence.ErrProjectDenied
	}
	return nil
}

// ResolveApplicationUser rechecks current membership in the same snapshot as
// the binding lookup. A future handoff must recheck again inside its write.
func (r *DiscordAuthRepo) ResolveApplicationUser(ctx context.Context, projectID, externalID string) (string, error) {
	var userID string
	err := withImmediateTx(ctx, r.allowlist.db, func(tx sqlExecutor) error {
		if err := tx.QueryRowContext(ctx, `SELECT s.application_user_id FROM discord_sender_identities s JOIN discord_authorized_users a ON a.id=s.allowlist_id WHERE a.discord_user_id=? AND s.verified_by<>''`, externalID).Scan(&userID); err != nil {
			return persistence.ErrProjectDenied
		}
		return requireDiscordSenderRole(ctx, tx, projectID, userID)
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}
