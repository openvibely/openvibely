package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func (r *AutomationRepo) CreateAutomationConfirmationReceipt(ctx context.Context, receipt *models.AutomationChatConfirmationReceipt) error {
	if receipt == nil {
		return errors.New("automation confirmation receipt is required")
	}
	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO automation_chat_confirmation_receipts
		(token_id, project_id, principal_id, thread_id, plan_message_id, automation_name, source, candidate_json, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, receipt.TokenID, receipt.ProjectID, receipt.PrincipalID,
		receipt.ThreadID, receipt.PlanMessageID, receipt.AutomationName, receipt.Source, receipt.CandidateJSON, receipt.ExpiresAt)
	return err
}

func (r *AutomationRepo) GetAutomationConfirmationReceipt(ctx context.Context, tokenID string) (*models.AutomationChatConfirmationReceipt, error) {
	return getAutomationConfirmationReceipt(ctx, r.db, tokenID)
}

func getAutomationConfirmationReceipt(ctx context.Context, q queryer, tokenID string) (*models.AutomationChatConfirmationReceipt, error) {
	var receipt models.AutomationChatConfirmationReceipt
	var confirmingInput, method sql.NullString
	err := q.QueryRowContext(ctx, `SELECT token_id, project_id, principal_id, thread_id, plan_message_id,
		automation_name, source, candidate_json, expires_at, confirming_user_input_id,
		confirmation_method, created_at, consumed_at
		FROM automation_chat_confirmation_receipts WHERE token_id = ?`, tokenID).
		Scan(&receipt.TokenID, &receipt.ProjectID, &receipt.PrincipalID, &receipt.ThreadID, &receipt.PlanMessageID,
			&receipt.AutomationName, &receipt.Source, &receipt.CandidateJSON, &receipt.ExpiresAt, &confirmingInput,
			&method, &receipt.CreatedAt, &receipt.ConsumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	receipt.ConfirmingUserInputID = confirmingInput.String
	receipt.ConfirmationMethod = method.String
	return &receipt, nil
}

func (r *AutomationRepo) GetPendingAutomationConfirmation(ctx context.Context, projectID, principalID, threadID string, now time.Time) (*models.AutomationChatConfirmationReceipt, string, error) {
	var tokenID, automationName string
	err := r.db.QueryRowContext(ctx, `SELECT r.token_id, r.automation_name
		FROM automation_chat_confirmation_receipts r
		JOIN executions e ON e.id = r.plan_message_id AND e.task_id = r.thread_id
		WHERE r.project_id = ? AND r.principal_id = ? AND r.thread_id = ?
		  AND r.consumed_at IS NULL AND r.expires_at > ? AND e.status = 'completed'
		ORDER BY r.created_at DESC, r.token_id DESC LIMIT 1`, projectID, principalID, threadID, automationCursorSQLTime(now.UTC())).Scan(&tokenID, &automationName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	receipt, err := getAutomationConfirmationReceipt(ctx, r.db, tokenID)
	return receipt, automationName, err
}

type AutomationConfirmationInputMarker struct {
	InputID     string
	TokenID     string
	ProjectID   string
	PrincipalID string
	ThreadID    string
	Method      string
}

func (r *AutomationRepo) MarkAutomationConfirmationInput(ctx context.Context, marker AutomationConfirmationInputMarker) error {
	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO automation_chat_confirmation_inputs
		(input_id, token_id, project_id, principal_id, thread_id, confirmation_method)
		SELECT ?, r.token_id, r.project_id, r.principal_id, r.thread_id, ?
		FROM automation_chat_confirmation_receipts r
		JOIN executions e ON e.id = ? AND e.task_id = r.thread_id
		WHERE r.token_id = ? AND r.project_id = ? AND r.principal_id = ? AND r.thread_id = ? AND r.consumed_at IS NULL
		ON CONFLICT(input_id) DO NOTHING`, marker.InputID, marker.Method, marker.InputID, marker.TokenID,
		marker.ProjectID, marker.PrincipalID, marker.ThreadID)
	if err != nil {
		return err
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_chat_confirmation_inputs
		WHERE input_id = ? AND token_id = ? AND project_id = ? AND principal_id = ? AND thread_id = ?
		  AND confirmation_method = ?`, marker.InputID, marker.TokenID, marker.ProjectID, marker.PrincipalID,
		marker.ThreadID, marker.Method).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("automation confirmation input scope does not match")
	}
	return nil
}

func (r *AutomationRepo) HasAutomationConfirmationInput(ctx context.Context, marker AutomationConfirmationInputMarker) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_chat_confirmation_inputs
		WHERE input_id = ? AND token_id = ? AND project_id = ? AND principal_id = ? AND thread_id = ?
		  AND confirmation_method = ?`, marker.InputID, marker.TokenID, marker.ProjectID, marker.PrincipalID,
		marker.ThreadID, marker.Method).Scan(&count)
	return count == 1, err
}
