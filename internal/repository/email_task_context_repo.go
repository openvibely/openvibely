package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
	return withBoundSQLiteConn(ctx, r.db, func(conn *sql.Conn) error {
		return r.UpsertWithExecutor(ctx, conn, etc)
	})
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

func (r *EmailTaskContextRepo) RecordOutboundMessageRef(ctx context.Context, projectID, sender, outboundMessageID, sessionKey string) error {
	projectID = strings.TrimSpace(projectID)
	sender = NormalizeEmailAddress(sender)
	outboundMessageID = strings.TrimSpace(outboundMessageID)
	sessionKey = strings.TrimSpace(sessionKey)
	if projectID == "" || sender == "" || outboundMessageID == "" || sessionKey == "" {
		return fmt.Errorf("project, sender, outbound message id, and session key are required")
	}
	_, err := execBoundSQLite(ctx, r.db, `
		INSERT INTO email_outbound_message_refs (project_id, email_from, outbound_message_id, email_session_key)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, email_from, outbound_message_id) DO UPDATE SET
			email_session_key = excluded.email_session_key`, projectID, sender, outboundMessageID, sessionKey)
	if err != nil {
		return fmt.Errorf("recording email outbound message reference: %w", err)
	}
	return nil
}

func (r *EmailTaskContextRepo) ResolveOutboundMessageSessionKey(ctx context.Context, projectID, sender, outboundMessageID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	sender = NormalizeEmailAddress(sender)
	outboundMessageID = strings.TrimSpace(outboundMessageID)
	if projectID == "" || sender == "" || outboundMessageID == "" {
		return "", nil
	}
	var sessionKey string
	err := r.db.QueryRowContext(ctx, `
		SELECT email_session_key
		FROM email_outbound_message_refs
		WHERE project_id = ? AND email_from = ? AND outbound_message_id = ?
		LIMIT 1`, projectID, sender, outboundMessageID).Scan(&sessionKey)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolving email outbound message reference: %w", err)
	}
	return sessionKey, nil
}
