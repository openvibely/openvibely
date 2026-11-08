package sqlite

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// BindApplicationUser updates the global sender mapping under the actor's assignment permissions.
func (r *SlackAuthRepo) BindApplicationUser(ctx context.Context, projectID, allowlistID, teamID, userID string) error {
	return r.BindSender(ctx, allowlistID, userID, teamID)
}
func resolveSlackApplicationUser(ctx context.Context, tx sqlExecutor, projectID, teamID, externalID string) (string, error) {
	var userID string
	err := tx.QueryRowContext(ctx, `SELECT s.application_user_id FROM slack_sender_identities s JOIN slack_authorized_users a ON a.id=s.allowlist_id WHERE a.slack_user_id=? AND s.team_id=? AND s.verified_by<>''`, externalID, teamID).Scan(&userID)
	if err != nil {
		return "", persistence.ErrProjectDenied
	}
	return userID, nil
}
func (r *SlackAuthRepo) ResolveApplicationUser(ctx context.Context, projectID, teamID, externalID string) (string, error) {
	return resolveSlackApplicationUser(ctx, r.allowlist.db, projectID, teamID, externalID)
}
func authorizeSlackHandoff(ctx context.Context, tx sqlExecutor, eventKey string) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	} // Embedded and auth-off compatibility.
	actor, ok := models.PrincipalFromContext(ctx)
	if !ok || actor.UserID != identity.UserID || identity.EventKey != eventKey {
		return persistence.ErrProjectDenied
	}
	mapped, err := resolveSlackApplicationUser(ctx, tx, identity.ProjectID, identity.TeamID, identity.ExternalUserID)
	if err != nil || mapped != actor.UserID {
		return persistence.ErrProjectDenied
	}
	role, err := projectRole(ctx, tx, actor.UserID, identity.ProjectID)
	if err != nil {
		return err
	}
	if role != "editor" && role != "maintainer" && role != "owner" {
		return persistence.ErrProjectDenied
	}
	return nil
}
