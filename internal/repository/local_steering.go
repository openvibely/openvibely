package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// CommitLocalSteering atomically records consumed steering and its native
// conversation history. Provider completion is not the acceptance boundary.
func (r *ThreadInputRepo) CommitLocalSteering(ctx context.Context, execID string, ids []string, history []any) error {
	raw, err := json.Marshal(struct {
		ResponsesInput []any `json:"responses_input"`
	}{history})
	if err != nil {
		return err
	}
	return withImmediateTx(ctx, r.db, func(tx SQLExecutor) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, `UPDATE thread_inputs SET input_status = 'applied', applied_at = COALESCE(applied_at, datetime('now')), updated_at = datetime('now') WHERE id = ? AND run_execution_id = ? AND edit_hold = 0 AND input_status IN ('pending', 'applied') AND input_mode = 'steering'`, id, execID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("consume steering %s: %w", id, ErrInputNotPending)
			}
		}
		return replaceExecutionReplayMessages(ctx, tx, execID, []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}})
	})
}
