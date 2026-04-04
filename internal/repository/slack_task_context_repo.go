package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// SlackTaskContextRepo persists Slack thread metadata for Slack-origin task notifications.
type SlackTaskContextRepo struct {
	db *sql.DB
}

func NewSlackTaskContextRepo(db *sql.DB) *SlackTaskContextRepo {
	return &SlackTaskContextRepo{db: db}
}

func (r *SlackTaskContextRepo) Upsert(ctx context.Context, stc *models.SlackTaskContext) error {
	if stc == nil {
		return fmt.Errorf("slack task context is nil")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO slack_task_context (task_id, slack_team_id, slack_channel_id, slack_thread_ts, slack_user_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(task_id) DO UPDATE SET
		 slack_team_id = excluded.slack_team_id,
		 slack_channel_id = excluded.slack_channel_id,
		 slack_thread_ts = excluded.slack_thread_ts,
		 slack_user_id = excluded.slack_user_id,
		 updated_at = datetime('now')`,
		stc.TaskID, stc.SlackTeamID, stc.SlackChannelID, stc.SlackThreadTS, stc.SlackUserID)
	if err != nil {
		return fmt.Errorf("upsert slack task context: %w", err)
	}
	return nil
}

func (r *SlackTaskContextRepo) GetByTaskID(ctx context.Context, taskID string) (*models.SlackTaskContext, error) {
	var stc models.SlackTaskContext
	err := r.db.QueryRowContext(ctx,
		`SELECT task_id, slack_team_id, slack_channel_id, slack_thread_ts, slack_user_id, created_at, updated_at
		 FROM slack_task_context WHERE task_id = ?`,
		taskID,
	).Scan(
		&stc.TaskID,
		&stc.SlackTeamID,
		&stc.SlackChannelID,
		&stc.SlackThreadTS,
		&stc.SlackUserID,
		&stc.CreatedAt,
		&stc.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get slack task context: %w", err)
	}
	return &stc, nil
}

func (r *SlackTaskContextRepo) DeleteByTaskID(ctx context.Context, taskID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM slack_task_context WHERE task_id = ?`, taskID)
	if err != nil {
		return fmt.Errorf("delete slack task context: %w", err)
	}
	return nil
}
