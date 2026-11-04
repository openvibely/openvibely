package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func authorizeDiscordTask(ctx context.Context, tx sqlExecutor, task models.Task) error {
	identity, ok := models.DiscordIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	actor, ok := models.PrincipalFromContext(ctx)
	if !ok || actor.UserID != identity.UserID || identity.ProjectID != task.ProjectID {
		return persistence.ErrProjectDenied
	}
	var mapped string
	if err := tx.QueryRowContext(ctx, `SELECT s.application_user_id FROM discord_sender_identities s JOIN discord_authorized_users a ON a.id=s.allowlist_id WHERE a.project_id=? AND a.discord_user_id=? AND s.verified_by<>''`, identity.ProjectID, identity.ExternalUserID).Scan(&mapped); err != nil || mapped != identity.UserID {
		return persistence.ErrProjectDenied
	}
	role, err := projectRole(ctx, tx, mapped, task.ProjectID)
	if err != nil {
		return err
	}
	manage := task.AutoMerge || task.AutoMergeOnGoalAchieved || task.CreatedVia == models.TaskOriginWebhook || persistence.IsAutomationTaskCreatedVia(task.CreatedVia)
	if role != "owner" && role != "maintainer" && (role != "editor" || manage) {
		return persistence.ErrProjectDenied
	}
	return nil
}
func saveDiscordTaskProvenance(ctx context.Context, tx sqlExecutor, taskID string) error {
	identity, ok := models.DiscordIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO discord_task_provenance(task_id,project_id,external_user_id,user_id) VALUES(?,?,?,?)`, taskID, identity.ProjectID, identity.ExternalUserID, identity.UserID)
	return err
}
func requireDiscordTaskProvenance(ctx context.Context, tx sqlExecutor, taskID, userID string) error {
	var identity models.DiscordIngressIdentity
	err := tx.QueryRowContext(ctx, `SELECT project_id,external_user_id,user_id FROM discord_task_provenance WHERE task_id=?`, taskID).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if userID != identity.UserID {
		return persistence.ErrProjectDenied
	}
	task := models.Task{ID: taskID}
	if err := tx.QueryRowContext(ctx, `SELECT project_id,created_via,auto_merge,auto_merge_on_goal_achieved FROM tasks WHERE id=?`, taskID).Scan(&task.ProjectID, &task.CreatedVia, &task.AutoMerge, &task.AutoMergeOnGoalAchieved); err != nil {
		return err
	}
	saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: userID}), identity)
	return authorizeDiscordTask(saved, tx, task)
}
