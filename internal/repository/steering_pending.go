package repository

import "context"

// HasPendingSteering checks availability without reserving or consuming input.
func (r *ThreadInputRepo) HasPendingSteering(ctx context.Context, execID string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM thread_inputs
		WHERE run_execution_id = ? AND turn_id = ?
		  AND input_mode = 'steering' AND input_status = 'pending' AND edit_hold = 0
		  AND COALESCE(expected_turn_id, '') != ''
		  AND NOT EXISTS (
			SELECT 1 FROM thread_input_provider_steering ps
			WHERE ps.thread_input_id = thread_inputs.id
		  )
	)`, execID, execID).Scan(&exists)
	return exists, err
}
