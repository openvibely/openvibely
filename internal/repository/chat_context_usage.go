package repository

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
)

func (r *ExecutionRepo) GetChatContextUsage(ctx context.Context, scopeType, scopeID, modelKey string) (*models.ChatContextUsage, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var usage models.ChatContextUsage
	err := r.db.QueryRowContext(ctx, `SELECT source_execution_id, context_tokens, overhead_tokens FROM chat_context_usage WHERE scope_type = ? AND scope_id = ? AND model_key = ?`, scopeType, scopeID, modelKey).Scan(&usage.SourceExecutionID, &usage.ContextTokens, &usage.OverheadTokens)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &usage, err
}

func (r *ExecutionRepo) SaveChatContextUsage(ctx context.Context, scopeType, scopeID, modelKey string, usage models.ChatContextUsage) error {
	if r == nil || r.db == nil {
		return nil
	}
	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO chat_context_usage (scope_type, scope_id, model_key, source_execution_id, context_tokens, overhead_tokens) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(scope_type, scope_id, model_key) DO UPDATE SET source_execution_id = excluded.source_execution_id, context_tokens = excluded.context_tokens, overhead_tokens = excluded.overhead_tokens`, scopeType, scopeID, modelKey, usage.SourceExecutionID, usage.ContextTokens, usage.OverheadTokens)
	return err
}
