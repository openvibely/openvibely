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

var slackTaskContextLifecycle = taskContextLifecycle[models.SlackTaskContext]{
	table:           "slack_task_context",
	errLabel:        "slack task context",
	metadataColumns: []string{"slack_team_id", "slack_channel_id", "slack_thread_ts", "slack_user_id"},
	selectColumns:   "task_id, slack_team_id, slack_channel_id, slack_thread_ts, slack_user_id, created_at, updated_at",
	values: func(stc models.SlackTaskContext) (string, []any) {
		return stc.TaskID, []any{stc.SlackTeamID, stc.SlackChannelID, stc.SlackThreadTS, stc.SlackUserID}
	},
	scan: func(row taskContextScanner) (models.SlackTaskContext, error) {
		var stc models.SlackTaskContext
		err := row.Scan(
			&stc.TaskID,
			&stc.SlackTeamID,
			&stc.SlackChannelID,
			&stc.SlackThreadTS,
			&stc.SlackUserID,
			&stc.CreatedAt,
			&stc.UpdatedAt,
		)
		return stc, err
	},
}

func (r *SlackTaskContextRepo) Upsert(ctx context.Context, stc *models.SlackTaskContext) error {
	return r.UpsertWithExecutor(ctx, r.db, stc)
}

// UpsertWithExecutor persists Slack task context using the caller's transaction.
func (r *SlackTaskContextRepo) UpsertWithExecutor(ctx context.Context, exec SQLExecutor, stc *models.SlackTaskContext) error {
	if stc == nil {
		return fmt.Errorf("slack task context is nil")
	}
	return slackTaskContextLifecycle.Upsert(ctx, exec, *stc)
}

func (r *SlackTaskContextRepo) GetByTaskID(ctx context.Context, taskID string) (*models.SlackTaskContext, error) {
	return slackTaskContextLifecycle.GetByTaskID(ctx, r.db, taskID)
}

func (r *SlackTaskContextRepo) DeleteByTaskID(ctx context.Context, taskID string) error {
	return deleteByTaskID(ctx, r.db, "slack_task_context", taskID, "slack task context")
}
