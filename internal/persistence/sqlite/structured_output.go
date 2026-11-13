package sqlite

import (
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
)

// StartStructuredOutput returns the next display position without loading the
// transcript. Existing raw output must be finalized before switching formats.
func (r *ExecutionRepo) StartStructuredOutput(ctx context.Context, id string) (int64, error) {
	var position int64
	var raw bool
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status,
 COALESCE((SELECT max(position)+1 FROM conversation_items WHERE execution_id=executions.id AND generation=0 AND lane='display' AND is_current=1),0),
 EXISTS(SELECT 1 FROM execution_stream_text WHERE execution_id=executions.id)
 FROM executions WHERE id=?`, id).Scan(&status, &position, &raw)
	if err != nil {
		return 0, err
	}
	if raw || status != "running" {
		return 0, fmt.Errorf("execution %s cannot start structured output", id)
	}
	return position, nil
}

// SealStructuredOutput records the digest of the writer's durable bytes. Later
// completion calls supplying the same text can skip legacy parsing/replacement.
func (r *ExecutionRepo) SealStructuredOutput(ctx context.Context, id string, digest []byte, total int) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		result, err := tx.ExecContext(ctx, `UPDATE executions SET display_digest=?,display_has_plan=EXISTS(SELECT 1 FROM execution_display_plans WHERE execution_id=executions.id AND content!='') WHERE id=? AND status='running'
 AND NOT EXISTS(SELECT 1 FROM execution_stream_text WHERE execution_id=executions.id)
 AND COALESCE((SELECT sum(display_bytes) FROM conversation_items WHERE execution_id=executions.id AND generation=0 AND lane='display' AND is_current=1 AND operation!='remove'),0)=?`, digest, id, total)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("structured output not sealed for execution %s: byte count or execution state changed", id)
		}
		return nil
	})
}

// AppendStructuredOutput shares the mutation engine, but live output may only
// be appended while the owning execution is running.
func (r *ExecutionRepo) AppendStructuredOutput(ctx context.Context, id string, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	var result []models.ConversationChange
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM executions WHERE id=?", id).Scan(&status); err != nil {
			return err
		}
		if status != "running" {
			return fmt.Errorf("execution %s no longer running", id)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE executions SET display_digest=NULL WHERE id=?", id); err != nil {
			return err
		}
		var err error
		result, err = appendConversationChanges(ctx, tx, models.ConversationOwner{ExecutionID: id}, changes)
		return err
	})
	return result, err
}
