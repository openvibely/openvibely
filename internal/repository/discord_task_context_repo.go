package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// DiscordTaskContextRepo persists Discord metadata for Discord-origin task notifications.
type DiscordTaskContextRepo struct {
	db *sql.DB
}

func NewDiscordTaskContextRepo(db *sql.DB) *DiscordTaskContextRepo {
	return &DiscordTaskContextRepo{db: db}
}

func (r *DiscordTaskContextRepo) Upsert(ctx context.Context, dtc *models.DiscordTaskContext) error {
	return r.UpsertWithExecutor(ctx, r.db, dtc)
}

// UpsertWithExecutor persists Discord task context using the caller's transaction.
func (r *DiscordTaskContextRepo) UpsertWithExecutor(ctx context.Context, exec SQLExecutor, dtc *models.DiscordTaskContext) error {
	if dtc == nil {
		return fmt.Errorf("discord task context is nil")
	}
	_, err := exec.ExecContext(ctx,
		`INSERT INTO discord_task_context (task_id, discord_channel_id, discord_thread_id, discord_message_id, discord_user_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(task_id) DO UPDATE SET
		 discord_channel_id = excluded.discord_channel_id,
		 discord_thread_id = excluded.discord_thread_id,
		 discord_message_id = excluded.discord_message_id,
		 discord_user_id = excluded.discord_user_id,
		 updated_at = datetime('now')`,
		dtc.TaskID, dtc.DiscordChannelID, dtc.DiscordThreadID, dtc.DiscordMessageID, dtc.DiscordUserID)
	if err != nil {
		return fmt.Errorf("upsert discord task context: %w", err)
	}
	return nil
}

func (r *DiscordTaskContextRepo) GetByTaskID(ctx context.Context, taskID string) (*models.DiscordTaskContext, error) {
	var dtc models.DiscordTaskContext
	err := r.db.QueryRowContext(ctx,
		`SELECT task_id, discord_channel_id, discord_thread_id, discord_message_id, discord_user_id, created_at, updated_at
		 FROM discord_task_context WHERE task_id = ?`,
		taskID,
	).Scan(
		&dtc.TaskID,
		&dtc.DiscordChannelID,
		&dtc.DiscordThreadID,
		&dtc.DiscordMessageID,
		&dtc.DiscordUserID,
		&dtc.CreatedAt,
		&dtc.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get discord task context: %w", err)
	}
	return &dtc, nil
}

func (r *DiscordTaskContextRepo) DeleteByTaskID(ctx context.Context, taskID string) error {
	return deleteByTaskID(ctx, r.db, "discord_task_context", taskID, "discord task context")
}
