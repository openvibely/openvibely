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

func (r *EmailTaskContextRepo) Upsert(ctx context.Context, etc *models.EmailTaskContext) error {
	return r.UpsertWithExecutor(ctx, r.db, etc)
}

// UpsertWithExecutor persists Email task context using the caller's transaction.
func (r *EmailTaskContextRepo) UpsertWithExecutor(ctx context.Context, exec SQLExecutor, etc *models.EmailTaskContext) error {
	if etc == nil {
		return fmt.Errorf("email task context is nil")
	}
	_, err := exec.ExecContext(ctx,
		`INSERT INTO email_task_context (task_id, email_from, email_message_id, email_references, email_subject, email_session_key, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
				 ON CONFLICT(task_id) DO UPDATE SET
				 email_from = excluded.email_from,
				 email_message_id = excluded.email_message_id,
				 email_references = excluded.email_references,
				 email_subject = excluded.email_subject,
				 email_session_key = excluded.email_session_key,
				 updated_at = datetime('now')`,
		etc.TaskID, etc.EmailFrom, etc.EmailMessageID, etc.EmailReferences, etc.EmailSubject, etc.EmailSessionKey)
	if err != nil {
		return fmt.Errorf("upsert email task context: %w", err)
	}
	return nil
}

func (r *EmailTaskContextRepo) GetByTaskID(ctx context.Context, taskID string) (*models.EmailTaskContext, error) {
	var etc models.EmailTaskContext
	err := r.db.QueryRowContext(ctx,
		`SELECT task_id, email_from, email_message_id, email_references, email_subject, COALESCE(email_session_key, ''), created_at, updated_at
		 FROM email_task_context WHERE task_id = ?`, taskID).
		Scan(&etc.TaskID, &etc.EmailFrom, &etc.EmailMessageID, &etc.EmailReferences, &etc.EmailSubject, &etc.EmailSessionKey, &etc.CreatedAt, &etc.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get email task context: %w", err)
	}
	return &etc, nil
}

func (r *EmailTaskContextRepo) DeleteByTaskID(ctx context.Context, taskID string) error {
	return deleteByTaskID(ctx, r.db, "email_task_context", taskID, "email task context")
}
