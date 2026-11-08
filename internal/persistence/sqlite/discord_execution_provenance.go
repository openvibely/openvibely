package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func discordExecutionAdmissionContext(ctx context.Context, tx sqlExecutor, taskID, userID string) (context.Context, error) {
	identity, captured := models.DiscordIngressIdentityFromContext(ctx)
	if !captured {
		err := tx.QueryRowContext(ctx, `SELECT project_id,external_user_id,user_id FROM discord_task_provenance WHERE task_id=?`, taskID).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return ctx, nil
		}
		if err != nil {
			return ctx, err
		}
	}
	if identity.UserID != userID {
		return ctx, persistence.ErrProjectDenied
	}
	saved := models.WithDiscordIngressIdentity(ctx, identity)
	if err := authorizeDiscordExecutionTask(saved, tx, taskID); err != nil {
		return ctx, err
	}
	return saved, nil
}
func authorizeDiscordExecutionTask(ctx context.Context, tx sqlExecutor, taskID string) error {
	task := models.Task{ID: taskID}
	if err := tx.QueryRowContext(ctx, `SELECT project_id,created_via,auto_merge,auto_merge_on_goal_achieved FROM tasks WHERE id=?`, taskID).Scan(&task.ProjectID, &task.CreatedVia, &task.AutoMerge, &task.AutoMergeOnGoalAchieved); err != nil {
		return err
	}
	return authorizeDiscordTask(ctx, tx, task)
}
func snapshotDiscordExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	identity, ok := models.DiscordIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO discord_execution_provenance(execution_id,project_id,external_user_id,user_id) VALUES(?,?,?,?)`, executionID, identity.ProjectID, identity.ExternalUserID, identity.UserID)
	return err
}
func requireDiscordExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	if err := requireDiscordConsumedExecution(ctx, tx, executionID); err != nil {
		return err
	}
	var identity models.DiscordIngressIdentity
	var taskID, executor string
	err := tx.QueryRowContext(ctx, `SELECT d.project_id,d.external_user_id,d.user_id,e.task_id,COALESCE(r.user_id,'') FROM discord_execution_provenance d JOIN executions e ON e.id=d.execution_id LEFT JOIN execution_run_identities r ON r.execution_id=e.id WHERE d.execution_id=?`, executionID).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID, &taskID, &executor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if executor != identity.UserID {
		return persistence.ErrProjectDenied
	}
	saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
	return authorizeDiscordExecutionTask(saved, tx, taskID)
}
