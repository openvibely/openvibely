package sqlite

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func authorizeTelegramTask(ctx context.Context, tx sqlExecutor, task models.Task) error {
	identity, ok := models.TelegramIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	p, ok := models.PrincipalFromContext(ctx)
	if !ok || p.UserID != identity.UserID {
		return persistence.ErrProjectDenied
	}
	var mapped string
	if err := tx.QueryRowContext(ctx, `SELECT application_user_id FROM telegram_authorized_users WHERE telegram_user_id=? AND mapping_review=0`, identity.ExternalUserID).Scan(&mapped); err != nil || mapped != identity.UserID {
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
func saveTelegramTaskProvenance(ctx context.Context, tx sqlExecutor, taskID string) error {
	identity, ok := models.TelegramIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO telegram_task_provenance(task_id,external_user_id,user_id) VALUES(?,?,?)`, taskID, identity.ExternalUserID, identity.UserID)
	return err
}
func requireTelegramTaskProvenance(ctx context.Context, tx sqlExecutor, taskID string) error {
	var identity models.TelegramIngressIdentity
	var task models.Task
	err := tx.QueryRowContext(ctx, `SELECT p.external_user_id,p.user_id,t.project_id,t.created_via,t.auto_merge,t.auto_merge_on_goal_achieved FROM telegram_task_provenance p JOIN tasks t ON t.id=p.task_id WHERE p.task_id=?`, taskID).Scan(&identity.ExternalUserID, &identity.UserID, &task.ProjectID, &task.CreatedVia, &task.AutoMerge, &task.AutoMergeOnGoalAchieved)
	if err != nil {
		return persistence.ErrProjectDenied
	}
	return authorizeTelegramTask(models.WithTelegramIngressIdentity(ctx, identity), tx, task)
}

// Validate any saved Telegram origin even when a later action changes created_via.
func validateTelegramTaskSnapshot(ctx context.Context, tx sqlExecutor, taskID, userID string) error {
	var saved string
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM telegram_task_provenance WHERE task_id=?`, taskID).Scan(&saved)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if saved != userID {
		return persistence.ErrProjectDenied
	}
	return requireTelegramTaskProvenance(models.WithPrincipal(ctx, models.Principal{UserID: userID}), tx, taskID)
}
