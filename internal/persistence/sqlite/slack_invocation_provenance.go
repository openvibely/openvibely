package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Invocations retain their own provenance even if the originating schedule or
// allowlist is removed. Historical invocations deliberately remain unbound.
func snapshotSlackInvocation(ctx context.Context, tx sqlExecutor, id string) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	if err := authorizeSlackHandoff(ctx, tx, identity.EventKey); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slack_invocation_provenance(invocation_id,project_id,team_id,external_user_id,user_id) VALUES(?,?,?,?,?)`, id, identity.ProjectID, identity.TeamID, identity.ExternalUserID, identity.UserID)
	if err != nil {
		return err
	}
	return requireSlackInvocation(ctx, tx, id)
}

func requireSlackInvocation(ctx context.Context, tx sqlExecutor, id string) error {
	var identity models.SlackIngressIdentity
	var projectID, userID string
	err := tx.QueryRowContext(ctx, `SELECT p.project_id,p.team_id,p.external_user_id,p.user_id,i.project_id,COALESCE(u.user_id,'') FROM slack_invocation_provenance p JOIN automation_invocations i ON i.id=p.invocation_id LEFT JOIN automation_invocation_users u ON u.invocation_id=i.id WHERE p.invocation_id=?`, id).Scan(&identity.ProjectID, &identity.TeamID, &identity.ExternalUserID, &identity.UserID, &projectID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if projectID != identity.ProjectID || userID != identity.UserID {
		return persistence.ErrProjectDenied
	}
	saved := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: userID}), identity)
	if err := authorizeSlackHandoff(saved, tx, ""); err != nil {
		return err
	}
	role, err := projectRole(ctx, tx, userID, projectID)
	if err != nil {
		return err
	}
	if role != "owner" && role != "maintainer" {
		return persistence.ErrProjectDenied
	}
	return nil
}
