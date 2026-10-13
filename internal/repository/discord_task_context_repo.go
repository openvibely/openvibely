package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// DiscordTaskContextRepo persists Discord metadata for Discord-origin task notifications.
type DiscordTaskContextRepo struct {
	db *sql.DB
}

func NewDiscordTaskContextRepo(db *sql.DB) *DiscordTaskContextRepo {
	return &DiscordTaskContextRepo{db: db}
}

var discordTaskContextLifecycle = taskContextLifecycle[models.DiscordTaskContext]{
	table:           "discord_task_context",
	errLabel:        "discord task context",
	metadataColumns: []string{"discord_channel_id", "discord_thread_id", "discord_message_id", "discord_user_id"},
	selectColumns:   "task_id, discord_channel_id, discord_thread_id, discord_message_id, discord_user_id, created_at, updated_at",
	values: func(dtc models.DiscordTaskContext) (string, []any) {
		return dtc.TaskID, []any{dtc.DiscordChannelID, dtc.DiscordThreadID, dtc.DiscordMessageID, dtc.DiscordUserID}
	},
	scan: func(row taskContextScanner) (models.DiscordTaskContext, error) {
		var dtc models.DiscordTaskContext
		err := row.Scan(
			&dtc.TaskID,
			&dtc.DiscordChannelID,
			&dtc.DiscordThreadID,
			&dtc.DiscordMessageID,
			&dtc.DiscordUserID,
			&dtc.CreatedAt,
			&dtc.UpdatedAt,
		)
		return dtc, err
	},
}

func (r *DiscordTaskContextRepo) Upsert(ctx context.Context, dtc *models.DiscordTaskContext) error {
	return withBoundSQLiteConn(ctx, r.db, func(conn *sql.Conn) error {
		return r.upsertWithExecutor(ctx, conn, dtc)
	})
}

// UpsertInTx persists task context using the caller's store transaction.
func (r *DiscordTaskContextRepo) UpsertInTx(ctx context.Context, tx persistence.Tx, dtc *models.DiscordTaskContext) error {
	exec, err := executorFromTx(tx)
	if err != nil {
		return err
	}
	return r.upsertWithExecutor(ctx, exec, dtc)
}

func (r *DiscordTaskContextRepo) upsertWithExecutor(ctx context.Context, exec sqlExecutor, dtc *models.DiscordTaskContext) error {
	if dtc == nil {
		return fmt.Errorf("discord task context is nil")
	}
	return discordTaskContextLifecycle.Upsert(ctx, exec, *dtc)
}

func (r *DiscordTaskContextRepo) GetByTaskID(ctx context.Context, taskID string) (*models.DiscordTaskContext, error) {
	return discordTaskContextLifecycle.GetByTaskID(ctx, r.db, taskID)
}

func (r *DiscordTaskContextRepo) DeleteByTaskID(ctx context.Context, taskID string) error {
	return deleteByTaskID(ctx, r.db, "discord_task_context", taskID, "discord task context")
}
