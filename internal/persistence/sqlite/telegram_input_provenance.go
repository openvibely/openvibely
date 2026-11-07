package sqlite

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func authorizeTelegramInputTarget(ctx context.Context, tx sqlExecutor, projectID, taskID string) error {
	task := models.Task{ProjectID: projectID}
	if taskID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT project_id,created_via,auto_merge,auto_merge_on_goal_achieved FROM tasks WHERE id=?`, taskID).Scan(&task.ProjectID, &task.CreatedVia, &task.AutoMerge, &task.AutoMergeOnGoalAchieved); err != nil {
			return err
		}
		if task.ProjectID != projectID {
			return persistence.ErrProjectDenied
		}
	}
	return authorizeTelegramTask(ctx, tx, task)
}

func snapshotTelegramInput(ctx context.Context, tx sqlExecutor, input *models.ThreadInput) error {
	identity, ok := models.TelegramIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	if identity.UserID != input.AuthorUserID || identity.ExternalUserID <= 0 {
		return persistence.ErrProjectDenied
	}
	if err := authorizeTelegramInputTarget(ctx, tx, input.ProjectID, input.TaskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE thread_inputs SET telegram_user_id=? WHERE id=?`, identity.ExternalUserID, input.ID); err != nil {
		return err
	}
	input.TelegramUserID = identity.ExternalUserID
	return nil
}

func telegramInputContext(ctx context.Context, tx sqlExecutor, inputID string) (context.Context, bool, error) {
	var identity models.TelegramIngressIdentity
	var project, task string
	err := tx.QueryRowContext(ctx, `SELECT telegram_user_id,author_user_id,project_id,COALESCE(task_id,'') FROM thread_inputs WHERE id=? AND telegram_user_id<>0`, inputID).Scan(&identity.ExternalUserID, &identity.UserID, &project, &task)
	if err == sql.ErrNoRows {
		return ctx, false, nil
	}
	if err != nil {
		return ctx, false, err
	}
	saved := models.WithTelegramIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
	if err := authorizeTelegramInputTarget(saved, tx, project, task); err != nil {
		return ctx, false, err
	}
	return saved, true, nil
}

func promoteTelegramInput(ctx context.Context, tx sqlExecutor, input models.ThreadInput, executionID string) error {
	saved, found, err := telegramInputContext(ctx, tx, input.ID)
	if err != nil || !found {
		return err
	}
	var taskID, userID string
	if err := tx.QueryRowContext(ctx, `SELECT e.task_id,r.user_id FROM executions e JOIN execution_run_identities r ON r.execution_id=e.id WHERE e.id=?`, executionID).Scan(&taskID, &userID); err != nil {
		return err
	}
	identity, _ := models.TelegramIngressIdentityFromContext(saved)
	if userID != identity.UserID || (input.TaskID != "" && taskID != input.TaskID) {
		return persistence.ErrProjectDenied
	}
	if input.TaskID == "" {
		if err := authorizeTelegramInputTarget(saved, tx, input.ProjectID, taskID); err != nil {
			return err
		}
		return saveTelegramTaskProvenance(saved, tx, taskID)
	}
	return nil
}
