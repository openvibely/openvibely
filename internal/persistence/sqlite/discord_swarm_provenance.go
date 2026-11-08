package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// A child inherits database-owned parent evidence, never the completion caller's
// identity. Its own snapshot survives removal of the parent's evidence.
func discordSwarmChildContext(ctx context.Context, tx sqlExecutor, child models.Task) (context.Context, error) {
	if child.ParentTaskID == nil || child.SwarmRole == "" || child.SwarmRole == models.SwarmRoleParent {
		return ctx, nil
	}
	var identity models.DiscordIngressIdentity
	var parentProject, runUser string
	err := tx.QueryRowContext(ctx, `SELECT d.project_id,d.external_user_id,d.user_id,t.project_id,COALESCE(r.user_id,'') FROM discord_task_provenance d JOIN tasks t ON t.id=d.task_id LEFT JOIN task_run_identities r ON r.task_id=t.id WHERE d.task_id=?`, *child.ParentTaskID).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID, &parentProject, &runUser)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	if parentProject != child.ProjectID || identity.ProjectID != child.ProjectID || identity.UserID != runUser {
		return ctx, persistence.ErrProjectDenied
	}
	if actor, ok := models.PrincipalFromContext(ctx); ok && actor.UserID != runUser {
		return ctx, persistence.ErrProjectDenied
	}
	if captured, ok := models.DiscordIngressIdentityFromContext(ctx); ok && captured != identity {
		return ctx, persistence.ErrProjectDenied
	}
	saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: runUser}), identity)
	if err := authorizeDiscordExecutionTask(saved, tx, *child.ParentTaskID); err != nil {
		return ctx, err
	}
	return saved, nil
}
