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
func snapshotDiscordInvocation(ctx context.Context, tx sqlExecutor, id string) error {
	identity, ok := models.DiscordIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	if err := authorizeDiscordTask(ctx, tx, models.Task{ProjectID: identity.ProjectID, AutoMerge: true}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO discord_invocation_provenance(invocation_id,project_id,external_user_id,user_id) VALUES(?,?,?,?)`, id, identity.ProjectID, identity.ExternalUserID, identity.UserID)
	if err != nil {
		return err
	}
	return requireDiscordInvocation(ctx, tx, id)
}

func requireDiscordInvocation(ctx context.Context, tx sqlExecutor, id string) error {
	var identity models.DiscordIngressIdentity
	var projectID, userID string
	err := tx.QueryRowContext(ctx, `SELECT p.project_id,p.external_user_id,p.user_id,i.project_id,COALESCE(u.user_id,'') FROM discord_invocation_provenance p JOIN automation_invocations i ON i.id=p.invocation_id LEFT JOIN automation_invocation_users u ON u.invocation_id=i.id WHERE p.invocation_id=?`, id).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID, &projectID, &userID)
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
