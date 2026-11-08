package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Publishers retain their own provenance even if the originating task or
// allowlist is removed. Historical versions deliberately remain unbound.
func snapshotDiscordPublisher(ctx context.Context, tx sqlExecutor, id string) error {
	identity, ok := models.DiscordIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	if err := authorizeDiscordTask(ctx, tx, models.Task{ProjectID: identity.ProjectID, AutoMerge: true}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO discord_publisher_provenance(version_id,project_id,external_user_id,user_id) VALUES(?,?,?,?)`, id, identity.ProjectID, identity.ExternalUserID, identity.UserID)
	if err != nil {
		return err
	}
	return requireDiscordPublisher(ctx, tx, id)
}

func requireDiscordPublisher(ctx context.Context, tx sqlExecutor, id string) error {
	var identity models.DiscordIngressIdentity
	var projectID, userID string
	err := tx.QueryRowContext(ctx, `SELECT p.project_id,p.external_user_id,p.user_id,i.project_id,COALESCE(u.user_id,'') FROM discord_publisher_provenance p JOIN automation_versions i ON i.id=p.version_id LEFT JOIN automation_version_publishers u ON u.version_id=i.id WHERE p.version_id=?`, id).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID, &projectID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if projectID != identity.ProjectID || userID != identity.UserID {
		return persistence.ErrProjectDenied
	}
	saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: userID}), identity)
	if err := authorizeDiscordTask(saved, tx, models.Task{ProjectID: projectID, AutoMerge: true}); err != nil {
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
