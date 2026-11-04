package sqlite

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// BindApplicationUser requires explicit administrator attestation. Never infer
// an application user from a mutable display name or the connection installer.
func (r *SlackAuthRepo) BindApplicationUser(ctx context.Context, projectID, allowlistID, teamID, userID string) error {
	actor, ok := models.PrincipalFromContext(ctx)
	if !ok || strings.TrimSpace(teamID) == "" || strings.TrimSpace(userID) == "" {
		return persistence.ErrProjectDenied
	}
	return withImmediateTx(ctx, r.allowlist.db, func(tx sqlExecutor) error {
		if _, err := orgActor(ctx, tx, actor.UserID); err != nil {
			return err
		}
		var actual string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM slack_authorized_users WHERE id=?`, allowlistID).Scan(&actual); err != nil || actual != projectID {
			return persistence.ErrProjectDenied
		}
		role, err := projectRole(ctx, tx, userID, projectID)
		if err != nil {
			return err
		}
		if role != "editor" && role != "maintainer" && role != "owner" {
			return persistence.ErrProjectDenied
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO slack_sender_identities(allowlist_id,team_id,application_user_id,verified_by) VALUES(?,?,?,?) ON CONFLICT(allowlist_id,team_id) DO UPDATE SET application_user_id=excluded.application_user_id,verified_by=excluded.verified_by`, allowlistID, teamID, userID, actor.UserID); err != nil {
			return err
		}
		return projectAudit(ctx, tx, projectID, actor.UserID, "slack.sender_bound", allowlistID, `{}`)
	})
}
func resolveSlackApplicationUser(ctx context.Context, tx sqlExecutor, projectID, teamID, externalID string) (string, error) {
	var userID string
	err := tx.QueryRowContext(ctx, `SELECT s.application_user_id FROM slack_sender_identities s JOIN slack_authorized_users a ON a.id=s.allowlist_id WHERE a.project_id=? AND a.slack_user_id=? AND s.team_id=? AND s.verified_by<>''`, projectID, externalID, teamID).Scan(&userID)
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
