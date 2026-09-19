package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type OpenAIAsyncToolCallRepo struct {
	db *sql.DB
}

func NewOpenAIAsyncToolCallRepo(db *sql.DB) *OpenAIAsyncToolCallRepo {
	return &OpenAIAsyncToolCallRepo{db: db}
}

func scanOpenAIAsyncToolCall(scanner interface{ Scan(...interface{}) error }) (*models.OpenAIAsyncToolCall, error) {
	var call models.OpenAIAsyncToolCall
	var deliveredAt sql.NullTime
	var isError int
	err := scanner.Scan(&call.ID, &call.ProjectID, &call.TaskID, &call.ExecutionID, &call.ResponseID, &call.CallID, &call.ToolName, &call.ArgumentsJSON, &call.Status, &call.Result, &isError, &call.ErrorMessage, &call.DeliverAttempts, &call.DeadlineAt, &deliveredAt, &call.CreatedAt, &call.UpdatedAt)
	if err != nil {
		return nil, err
	}
	call.IsError = isError != 0
	if deliveredAt.Valid {
		v := deliveredAt.Time
		call.DeliveredAt = &v
	}
	return &call, nil
}

const openAIAsyncToolCallColumns = `id, project_id, task_id, execution_id, response_id, call_id, tool_name, arguments_json, status, result, is_error, error_message, deliver_attempts, deadline_at, delivered_at, created_at, updated_at`

func (r *OpenAIAsyncToolCallRepo) CreatePending(ctx context.Context, call *models.OpenAIAsyncToolCall) (*models.OpenAIAsyncToolCall, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("OpenAI async tool call repo is unavailable")
	}
	if call == nil {
		return nil, fmt.Errorf("OpenAI async tool call is nil")
	}
	if call.ID == "" {
		call.ID = NewID()
	}
	if call.ID == "" {
		return nil, fmt.Errorf("generating OpenAI async tool call id")
	}
	if call.ArgumentsJSON == "" {
		call.ArgumentsJSON = "{}"
	}
	if call.Status == "" {
		call.Status = models.OpenAIAsyncToolCallPending
	}
	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO openai_async_tool_calls (id, project_id, task_id, execution_id, response_id, call_id, tool_name, arguments_json, status, deadline_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(execution_id, call_id) DO NOTHING`, call.ID, call.ProjectID, call.TaskID, call.ExecutionID, call.ResponseID, call.CallID, call.ToolName, call.ArgumentsJSON, call.Status, call.DeadlineAt)
	if err != nil {
		return nil, fmt.Errorf("creating OpenAI async tool call: %w", err)
	}
	persisted, err := r.GetByExecutionCallID(ctx, call.ExecutionID, call.CallID)
	if err != nil {
		return nil, err
	}
	if persisted == nil {
		return nil, fmt.Errorf("OpenAI async tool call was not persisted")
	}
	return persisted, nil
}

func (r *OpenAIAsyncToolCallRepo) GetByExecutionCallID(ctx context.Context, executionID, callID string) (*models.OpenAIAsyncToolCall, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	call, err := scanOpenAIAsyncToolCall(r.db.QueryRowContext(ctx, `SELECT `+openAIAsyncToolCallColumns+` FROM openai_async_tool_calls WHERE execution_id = ? AND call_id = ?`, executionID, callID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting OpenAI async tool call: %w", err)
	}
	return call, nil
}

func (r *OpenAIAsyncToolCallRepo) ListRecoverable(ctx context.Context, now time.Time, limit int) ([]models.OpenAIAsyncToolCall, error) {
	return r.listRecoverable(ctx, "", now, limit)
}

func (r *OpenAIAsyncToolCallRepo) ListRecoverableByExecution(ctx context.Context, executionID string, now time.Time, limit int) ([]models.OpenAIAsyncToolCall, error) {
	return r.listRecoverable(ctx, executionID, now, limit)
}

func (r *OpenAIAsyncToolCallRepo) listRecoverable(ctx context.Context, executionID string, now time.Time, limit int) ([]models.OpenAIAsyncToolCall, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	query := `SELECT ` + openAIAsyncToolCallColumns + ` FROM openai_async_tool_calls WHERE status IN ('pending', 'running', 'completed') AND deadline_at > ?`
	args := []any{now}
	if executionID != "" {
		query += ` AND execution_id = ?`
		args = append(args, executionID)
	}
	query += ` ORDER BY created_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing recoverable OpenAI async tool calls: %w", err)
	}
	defer rows.Close()
	var out []models.OpenAIAsyncToolCall
	for rows.Next() {
		call, err := scanOpenAIAsyncToolCall(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning OpenAI async tool call: %w", err)
		}
		out = append(out, *call)
	}
	return out, rows.Err()
}

func (r *OpenAIAsyncToolCallRepo) ClaimForRun(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'running', updated_at = ? WHERE id = ? AND status = 'pending' AND deadline_at > ?`, now, id, now)
	if err != nil {
		return false, fmt.Errorf("claiming OpenAI async tool call: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r *OpenAIAsyncToolCallRepo) ClaimRecoverableForRun(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'running', updated_at = ? WHERE id = ? AND status IN ('pending', 'running') AND deadline_at > ?`, now, id, now)
	if err != nil {
		return false, fmt.Errorf("claiming recoverable OpenAI async tool call: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r *OpenAIAsyncToolCallRepo) MarkCompleted(ctx context.Context, id, result string, isError bool, now time.Time) error {
	isErr := 0
	if isError {
		isErr = 1
	}
	_, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'completed', result = ?, is_error = ?, error_message = '', updated_at = ? WHERE id = ? AND status IN ('pending', 'running', 'completed')`, result, isErr, now, id)
	if err != nil {
		return fmt.Errorf("completing OpenAI async tool call: %w", err)
	}
	return nil
}

func (r *OpenAIAsyncToolCallRepo) MarkFailed(ctx context.Context, id, message string, now time.Time) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'failed', is_error = 1, error_message = ?, updated_at = ? WHERE id = ? AND status IN ('pending', 'running')`, message, now, id)
	if err != nil {
		return fmt.Errorf("failing OpenAI async tool call: %w", err)
	}
	return nil
}

func (r *OpenAIAsyncToolCallRepo) MarkDelivered(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'delivered', delivered_at = ?, deliver_attempts = deliver_attempts + 1, updated_at = ? WHERE id = ? AND status = 'completed'`, now, now, id)
	if err != nil {
		return false, fmt.Errorf("delivering OpenAI async tool call: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (r *OpenAIAsyncToolCallRepo) MarkProviderRejected(ctx context.Context, id, message string, now time.Time) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'provider_rejected', error_message = ?, deliver_attempts = deliver_attempts + 1, updated_at = ? WHERE id = ? AND status = 'completed'`, message, now, id)
	if err != nil {
		return fmt.Errorf("marking OpenAI async tool call provider rejected: %w", err)
	}
	return nil
}

func (r *OpenAIAsyncToolCallRepo) MarkStale(ctx context.Context, id, message string, now time.Time) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'stale', error_message = ?, updated_at = ? WHERE id = ? AND status IN ('pending', 'running', 'completed')`, message, now, id)
	if err != nil {
		return fmt.Errorf("marking OpenAI async tool call stale: %w", err)
	}
	return nil
}

func (r *OpenAIAsyncToolCallRepo) CancelByExecution(ctx context.Context, executionID, message string, now time.Time) (int64, error) {
	res, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'cancelled', error_message = ?, updated_at = ? WHERE execution_id = ? AND status IN ('pending', 'running', 'completed')`, message, now, executionID)
	if err != nil {
		return 0, fmt.Errorf("cancelling OpenAI async tool calls: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func cancelOpenAIAsyncToolCallsForExecution(ctx context.Context, exec SQLExecutor, executionID, message string) error {
	if exec == nil || executionID == "" {
		return nil
	}
	_, err := exec.ExecContext(ctx, `UPDATE openai_async_tool_calls SET status = 'cancelled', error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE execution_id = ? AND status IN ('pending', 'running', 'completed')`, message, executionID)
	if err != nil {
		return fmt.Errorf("cancelling OpenAI async tool calls: %w", err)
	}
	return nil
}

func (r *OpenAIAsyncToolCallRepo) ExpireDue(ctx context.Context, now time.Time) (int64, error) {
	res, err := execBoundSQLite(ctx, r.db, `UPDATE openai_async_tool_calls SET status = 'expired', error_message = 'async tool call expired before delivery', updated_at = ? WHERE status IN ('pending', 'running', 'completed') AND deadline_at <= ?`, now, now)
	if err != nil {
		return 0, fmt.Errorf("expiring OpenAI async tool calls: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
