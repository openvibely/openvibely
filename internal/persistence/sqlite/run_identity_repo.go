package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

type RunIdentityRepo struct{ db *sql.DB }

func NewRunIdentityRepo(db *sql.DB) *RunIdentityRepo { return &RunIdentityRepo{db: db} }

var _ persistence.RunIdentityStore = (*RunIdentityRepo)(nil)

func (r *RunIdentityRepo) TaskUser(ctx context.Context, taskID string) (string, error) {
	var userID string
	err := r.db.QueryRowContext(ctx, `SELECT user_id FROM task_run_identities WHERE task_id=?`, taskID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := requireDiscordTaskProvenance(ctx, r.db, taskID, userID); err != nil {
		return "", err
	}
	if _, _, err := slackTaskContext(ctx, r.db, taskID, userID); err != nil {
		return "", err
	}
	return userID, nil
}

func (r *RunIdentityRepo) BindTask(ctx context.Context, taskID string) error {
	p, ok := models.PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return persistence.ErrProjectDenied
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if err := requireDiscordTaskProvenance(ctx, tx, taskID, p.UserID); err != nil {
			return err
		}
		if err := authorizeSlackTaskMutation(ctx, tx, taskID); err != nil {
			return err
		}
		var projectID, createdVia string
		var manageRequired bool
		if err := tx.QueryRowContext(ctx, `SELECT project_id,created_via,(auto_merge OR auto_merge_on_goal_achieved) FROM tasks WHERE id=?`, taskID).Scan(&projectID, &createdVia, &manageRequired); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return persistence.ErrProjectDenied
			}
			return err
		}
		role, err := projectRole(ctx, tx, p.UserID, projectID)
		if err != nil {
			return err
		}
		manageRequired = manageRequired || createdVia == models.TaskOriginWebhook || persistence.IsAutomationTaskCreatedVia(createdVia)
		if role != "maintainer" && role != "owner" && (role != "editor" || manageRequired) {
			return persistence.ErrProjectDenied
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_run_identities(task_id,user_id) VALUES(?,?) ON CONFLICT(task_id) DO UPDATE SET user_id=excluded.user_id,updated_at=datetime('now')`, taskID, p.UserID); err != nil {
			return err
		}
		if err := saveSlackTaskProvenance(ctx, tx, taskID); err != nil {
			return err
		}
		return projectAudit(ctx, tx, projectID, p.UserID, "task.run_as", taskID, `{}`)
	})
}

func (r *RunIdentityRepo) ScheduleUser(ctx context.Context, scheduleID string) (string, error) {
	var userID string
	err := r.db.QueryRowContext(ctx, `SELECT user_id FROM schedule_run_identities WHERE schedule_id=?`, scheduleID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := slackScheduleContext(ctx, r.db, scheduleID, userID); err != nil {
		return "", err
	}
	if _, err := discordScheduleContext(ctx, r.db, scheduleID, userID); err != nil {
		return "", err
	}
	return userID, nil
}
func (r *RunIdentityRepo) ExecutionUser(ctx context.Context, executionID string) (string, bool, error) {
	var userID string
	err := r.db.QueryRowContext(ctx, `SELECT user_id FROM execution_run_identities WHERE execution_id=?`, executionID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return userID, err == nil, err
}

func (r *RunIdentityRepo) RequireChannelTaskProvenance(ctx context.Context, taskID string) error {
	var origin, userID string
	err := r.db.QueryRowContext(ctx, `SELECT t.created_via,COALESCE(d.user_id,'') FROM tasks t LEFT JOIN discord_task_provenance d ON d.task_id=t.id WHERE t.id=?`, taskID).Scan(&origin, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return persistence.ErrProjectDenied
	}
	if err != nil {
		return err
	}
	if origin != models.TaskOriginDiscord {
		return nil
	}
	actor, ok := models.PrincipalFromContext(ctx)
	if userID == "" || !ok || actor.UserID != userID {
		return persistence.ErrProjectDenied
	}
	return requireDiscordTaskProvenance(ctx, r.db, taskID, userID)
}

func (r *RunIdentityRepo) CheckExecutionGoal(ctx context.Context, executionID, taskID string) error {
	return checkExecutionGoal(ctx, r.db, executionID, taskID)
}

func checkExecutionGoal(ctx context.Context, tx sqlExecutor, executionID, taskID string) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN e.task_id<>? THEN 0 WHEN r.execution_id IS NULL OR r.goal_continuation=0 THEN 1 ELSE EXISTS(
 SELECT 1 FROM task_goals g WHERE g.task_id=? AND g.goal_id=r.goal_id AND g.run_as_user_id=r.user_id AND g.status='active' AND r.goal_id<>''
 ) END FROM executions e LEFT JOIN execution_run_identities r ON e.id=r.execution_id WHERE e.id=?`, taskID, taskID, executionID).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return persistence.ErrProjectDenied
	}
	if err != nil {
		return err
	}
	if !valid {
		return persistence.ErrProjectDenied
	}
	if err := requireReviewerExecution(ctx, tx, executionID); err != nil {
		return err
	}
	if err := requireDiscordExecution(ctx, tx, executionID); err != nil {
		return err
	}
	if err := requireSlackExecution(ctx, tx, executionID); err != nil {
		return err
	}
	return requireAutomationExecution(ctx, tx, executionID)
}
