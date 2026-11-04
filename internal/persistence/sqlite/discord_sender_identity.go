package sqlite

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// BindApplicationUser records an administrator's explicit attestation, never a
// display-name match or an inferred connection-owner identity.
func (r *DiscordAuthRepo) BindApplicationUser(ctx context.Context, projectID, allowlistID, userID string) error {
	actor, ok := models.PrincipalFromContext(ctx)
	if !ok || actor.UserID == "" || userID == "" {
		return persistence.ErrProjectDenied
	}
	return withImmediateTx(ctx, r.allowlist.db, func(tx sqlExecutor) error {
		if _, err := orgActor(ctx, tx, actor.UserID); err != nil {
			return err
		}
		var actual, externalID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id,discord_user_id FROM discord_authorized_users WHERE id=?`, allowlistID).Scan(&actual, &externalID); err != nil || actual != projectID {
			return persistence.ErrProjectDenied
		}
		numeric, err := strconv.ParseUint(externalID, 10, 64)
		if err != nil || numeric == 0 || strconv.FormatUint(numeric, 10) != externalID {
			return persistence.ErrProjectDenied
		}
		if err := requireDiscordSenderRole(ctx, tx, projectID, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO discord_sender_identities(allowlist_id,application_user_id,verified_by) VALUES(?,?,?) ON CONFLICT(allowlist_id) DO UPDATE SET application_user_id=excluded.application_user_id,verified_by=excluded.verified_by`, allowlistID, userID, actor.UserID); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]string{"external_user_id": externalID, "application_user_id": userID})
		return projectAudit(ctx, tx, projectID, actor.UserID, "discord.sender_bound", allowlistID, string(details))
	})
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
		if err := tx.QueryRowContext(ctx, `SELECT s.application_user_id FROM discord_sender_identities s JOIN discord_authorized_users a ON a.id=s.allowlist_id WHERE a.project_id=? AND a.discord_user_id=? AND s.verified_by<>''`, projectID, externalID).Scan(&userID); err != nil {
			return persistence.ErrProjectDenied
		}
		return requireDiscordSenderRole(ctx, tx, projectID, userID)
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}
