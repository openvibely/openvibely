package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// EmailTaskContextRepo persists Email thread metadata for Email-origin task notifications.
type EmailTaskContextRepo struct {
	db *sql.DB
}

func NewEmailTaskContextRepo(db *sql.DB) *EmailTaskContextRepo {
	return &EmailTaskContextRepo{db: db}
}

var emailTaskContextLifecycle = taskContextLifecycle[models.EmailTaskContext]{
	table:           "email_task_context",
	errLabel:        "email task context",
	metadataColumns: []string{"email_from", "email_message_id", "email_references", "email_subject", "email_session_key"},
	selectColumns:   "task_id, email_from, email_message_id, email_references, email_subject, COALESCE(email_session_key, ''), created_at, updated_at",
	values: func(etc models.EmailTaskContext) (string, []any) {
		return etc.TaskID, []any{etc.EmailFrom, etc.EmailMessageID, etc.EmailReferences, etc.EmailSubject, etc.EmailSessionKey}
	},
	scan: func(row taskContextScanner) (models.EmailTaskContext, error) {
		var etc models.EmailTaskContext
		err := row.Scan(
			&etc.TaskID,
			&etc.EmailFrom,
			&etc.EmailMessageID,
			&etc.EmailReferences,
			&etc.EmailSubject,
			&etc.EmailSessionKey,
			&etc.CreatedAt,
			&etc.UpdatedAt,
		)
		return etc, err
	},
}

func (r *EmailTaskContextRepo) Upsert(ctx context.Context, etc *models.EmailTaskContext) error {
	return r.UpsertWithExecutor(ctx, r.db, etc)
}

// UpsertWithExecutor persists Email task context using the caller's transaction.
func (r *EmailTaskContextRepo) UpsertWithExecutor(ctx context.Context, exec SQLExecutor, etc *models.EmailTaskContext) error {
	if etc == nil {
		return fmt.Errorf("email task context is nil")
	}
	return emailTaskContextLifecycle.Upsert(ctx, exec, *etc)
}

func (r *EmailTaskContextRepo) GetByTaskID(ctx context.Context, taskID string) (*models.EmailTaskContext, error) {
	return emailTaskContextLifecycle.GetByTaskID(ctx, r.db, taskID)
}

func (r *EmailTaskContextRepo) DeleteByTaskID(ctx context.Context, taskID string) error {
	return deleteByTaskID(ctx, r.db, "email_task_context", taskID, "email task context")
}
