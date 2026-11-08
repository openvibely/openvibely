package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Versions retain their own provenance even if the originating task or
// allowlist is removed. Historical versions deliberately remain unbound.
func snapshotSlackPublisher(ctx context.Context, tx sqlExecutor, id string) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	if err := authorizeSlackHandoff(ctx, tx, identity.EventKey); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slack_publisher_provenance(version_id,project_id,team_id,external_user_id,user_id) VALUES(?,?,?,?,?)`, id, identity.ProjectID, identity.TeamID, identity.ExternalUserID, identity.UserID)
	if err != nil {
		return err
	}
	return requireSlackPublisher(ctx, tx, id)
}

func requireSlackPublisher(ctx context.Context, tx sqlExecutor, id string) error {
	var identity models.SlackIngressIdentity
	var projectID, userID string
	err := tx.QueryRowContext(ctx, `SELECT p.project_id,p.team_id,p.external_user_id,p.user_id,i.project_id,COALESCE(u.user_id,'') FROM slack_publisher_provenance p JOIN automation_versions i ON i.id=p.version_id LEFT JOIN automation_version_publishers u ON u.version_id=i.id WHERE p.version_id=?`, id).Scan(&identity.ProjectID, &identity.TeamID, &identity.ExternalUserID, &identity.UserID, &projectID, &userID)
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
