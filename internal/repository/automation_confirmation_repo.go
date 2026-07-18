package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func (r *AutomationRepo) CreateAutomationConfirmationReceipt(ctx context.Context, receipt *models.AutomationChatConfirmationReceipt) error {
	if receipt == nil {
		return errors.New("automation confirmation receipt is required")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO automation_chat_confirmation_receipts
		(token_id, project_id, automation_id, version_id, plan_revision, principal_id, thread_id, plan_message_id, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, receipt.TokenID, receipt.ProjectID, receipt.AutomationID,
		receipt.VersionID, receipt.PlanRevision, receipt.PrincipalID, receipt.ThreadID, receipt.PlanMessageID, receipt.ExpiresAt)
	return err
}

func (r *AutomationRepo) GetAutomationConfirmationReceipt(ctx context.Context, tokenID string) (*models.AutomationChatConfirmationReceipt, error) {
	return getAutomationConfirmationReceipt(ctx, r.db, tokenID)
}

func getAutomationConfirmationReceipt(ctx context.Context, q queryer, tokenID string) (*models.AutomationChatConfirmationReceipt, error) {
	var receipt models.AutomationChatConfirmationReceipt
	var consumedAttempt, confirmingInput, method sql.NullString
	err := q.QueryRowContext(ctx, `SELECT token_id, project_id, automation_id, version_id, plan_revision,
		principal_id, thread_id, plan_message_id, expires_at, consumed_attempt_id, confirming_user_input_id,
		confirmation_method, created_at, consumed_at FROM automation_chat_confirmation_receipts WHERE token_id = ?`, tokenID).
		Scan(&receipt.TokenID, &receipt.ProjectID, &receipt.AutomationID, &receipt.VersionID, &receipt.PlanRevision,
			&receipt.PrincipalID, &receipt.ThreadID, &receipt.PlanMessageID, &receipt.ExpiresAt, &consumedAttempt,
			&confirmingInput, &method, &receipt.CreatedAt, &receipt.ConsumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	receipt.ConsumedAttemptID = consumedAttempt.String
	receipt.ConfirmingUserInputID = confirmingInput.String
	receipt.ConfirmationMethod = method.String
	return &receipt, nil
}

func (r *AutomationRepo) GetPendingAutomationConfirmation(ctx context.Context, projectID, principalID, threadID string, now time.Time) (*models.AutomationChatConfirmationReceipt, string, error) {
	var tokenID, automationName string
	err := r.db.QueryRowContext(ctx, `SELECT r.token_id, a.name
		FROM automation_chat_confirmation_receipts r
		JOIN automations a ON a.id = r.automation_id AND a.project_id = r.project_id
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
	InputID      string
	TokenID      string
	ProjectID    string
	AutomationID string
	VersionID    string
	PrincipalID  string
	ThreadID     string
	Method       string
}

func (r *AutomationRepo) MarkAutomationConfirmationInput(ctx context.Context, marker AutomationConfirmationInputMarker) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO automation_chat_confirmation_inputs
		(input_id, token_id, project_id, automation_id, version_id, principal_id, thread_id, confirmation_method)
		SELECT ?, r.token_id, r.project_id, r.automation_id, r.version_id, r.principal_id, r.thread_id, ?
		FROM automation_chat_confirmation_receipts r
		JOIN executions e ON e.id = ? AND e.task_id = r.thread_id
		WHERE r.token_id = ? AND r.project_id = ? AND r.automation_id = ? AND r.version_id = ?
		  AND r.principal_id = ? AND r.thread_id = ? AND r.consumed_at IS NULL
		ON CONFLICT(input_id) DO NOTHING`, marker.InputID, marker.Method, marker.InputID, marker.TokenID,
		marker.ProjectID, marker.AutomationID, marker.VersionID, marker.PrincipalID, marker.ThreadID)
	if err != nil {
		return err
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_chat_confirmation_inputs
		WHERE input_id = ? AND token_id = ? AND project_id = ? AND automation_id = ? AND version_id = ?
		  AND principal_id = ? AND thread_id = ? AND confirmation_method = ?`, marker.InputID, marker.TokenID,
		marker.ProjectID, marker.AutomationID, marker.VersionID, marker.PrincipalID, marker.ThreadID, marker.Method).Scan(&count); err != nil {
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
		WHERE input_id = ? AND token_id = ? AND project_id = ? AND automation_id = ? AND version_id = ?
		  AND principal_id = ? AND thread_id = ? AND confirmation_method = ?`, marker.InputID, marker.TokenID,
		marker.ProjectID, marker.AutomationID, marker.VersionID, marker.PrincipalID, marker.ThreadID, marker.Method).Scan(&count)
	return count == 1, err
}

type AutomationConfirmationConsume struct {
	TokenID               string
	ProjectID             string
	AutomationID          string
	VersionID             string
	PlanRevision          string
	PrincipalID           string
	ThreadID              string
	ConfirmingUserInputID string
	Method                string
	Now                   time.Time
	Effects               []models.AutomationPublicationEffect
}

func (r *AutomationRepo) ConsumeAutomationConfirmationAndReserve(ctx context.Context, input AutomationConfirmationConsume) (*AutomationPublicationSnapshot, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	receipt, err := getAutomationConfirmationReceipt(ctx, conn, input.TokenID)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, errors.New("automation confirmation receipt not found")
	}
	if receipt.ProjectID != input.ProjectID || receipt.AutomationID != input.AutomationID || receipt.VersionID != input.VersionID || receipt.PlanRevision != input.PlanRevision || receipt.PrincipalID != input.PrincipalID || receipt.ThreadID != input.ThreadID {
		return nil, errors.New("automation confirmation receipt scope does not match")
	}
	if !input.Now.Before(receipt.ExpiresAt) {
		return nil, errors.New("automation confirmation receipt expired")
	}
	if receipt.ConsumedAt != nil {
		if receipt.ConfirmingUserInputID != input.ConfirmingUserInputID || receipt.ConfirmationMethod != input.Method || receipt.ConsumedAttemptID == "" {
			return nil, errors.New("automation confirmation receipt was already consumed")
		}
		snapshot, err := loadPublicationSnapshot(ctx, conn, receipt.ConsumedAttemptID)
		if err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, err
		}
		committed = true
		return snapshot, nil
	}
	var state models.AutomationVersionState
	if err := conn.QueryRowContext(ctx, `SELECT state FROM automation_versions WHERE project_id = ? AND automation_id = ? AND id = ?`, input.ProjectID, input.AutomationID, input.VersionID).Scan(&state); err != nil {
		return nil, err
	}
	if state != models.AutomationVersionDraft {
		return nil, errors.New("automation version is not a draft")
	}
	var attemptID string
	err = conn.QueryRowContext(ctx, `SELECT id FROM automation_publication_attempts WHERE project_id = ? AND automation_id = ? AND version_id = ? AND plan_revision = ?`, input.ProjectID, input.AutomationID, input.VersionID, input.PlanRevision).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := conn.QueryRowContext(ctx, `INSERT INTO automation_publication_attempts
			(project_id, automation_id, version_id, plan_revision, status) VALUES (?, ?, ?, ?, 'publishing') RETURNING id`,
			input.ProjectID, input.AutomationID, input.VersionID, input.PlanRevision).Scan(&attemptID); err != nil {
			return nil, err
		}
		for order, effect := range input.Effects {
			status := "pending"
			if effect.Operation == "reuse" || effect.Operation == "unchanged" || effect.Operation == "disable" {
				status = "completed"
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO automation_publication_steps
					(attempt_id, step_key, operation, target_key, display_order, status, resource_type, resource_id)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, attemptID, effect.StepKey, effect.Operation, effect.TargetKey,
				order, status, effect.ResourceType, effect.ResourceID); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	result, err := conn.ExecContext(ctx, `UPDATE automation_chat_confirmation_receipts SET consumed_attempt_id = ?,
		confirming_user_input_id = ?, confirmation_method = ?, consumed_at = ? WHERE token_id = ? AND consumed_at IS NULL`,
		attemptID, input.ConfirmingUserInputID, input.Method, input.Now, input.TokenID)
	if err != nil {
		return nil, fmt.Errorf("consuming automation confirmation receipt: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, errors.New("automation confirmation receipt was already consumed")
	}
	snapshot, err := loadPublicationSnapshot(ctx, conn, attemptID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	return snapshot, nil
}
