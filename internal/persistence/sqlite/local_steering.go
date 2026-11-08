package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// CommitLocalSteering atomically records consumed steering and its native
// conversation history. Provider completion is not the acceptance boundary.
func (r *ThreadInputRepo) CommitLocalSteering(ctx context.Context, commit persistence.LocalSteeringCommit) error {
	execID, ids := commit.ExecutionID, commit.InputIDs
	raw, err := json.Marshal(struct {
		ResponsesInput []any `json:"responses_input"`
	}{commit.ResponsesHistory})
	if err != nil {
		return err
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		for _, id := range ids {
			if err := requireInputProvenance(ctx, tx, id); err != nil {
				return err
			}
			var userID, projectID string
			if err := tx.QueryRowContext(ctx, `SELECT author_user_id,project_id FROM thread_inputs WHERE id=? AND run_execution_id=? AND input_mode='steering'`, id, execID).Scan(&userID, &projectID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("consume steering %s: %w", id, persistence.ErrInputNotPending)
				}
				return err
			}
			if userID != "" {
				if err := authorizeSteeringSender(ctx, tx, execID, userID, projectID); err != nil {
					return err
				}
			}
			res, err := tx.ExecContext(ctx, `UPDATE thread_inputs SET input_status = 'applied', applied_at = COALESCE(applied_at, datetime('now')), updated_at = datetime('now') WHERE id = ? AND run_execution_id = ? AND edit_hold = 0 AND input_status IN ('pending', 'applied') AND input_mode = 'steering'`, id, execID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if err := copyDiscordConsumedInput(ctx, tx, id, execID); err != nil {
				return err
			}
			if err := copyReviewerInput(ctx, tx, id, execID); err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("consume steering %s: %w", id, persistence.ErrInputNotPending)
			}
		}
		return replaceExecutionReplayMessages(ctx, tx, execID, []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}})
	})
}

// Steering may change a privileged run, so use the actual execution task's
// required role rather than the editor floor for ordinary project messages.
func authorizeSteeringSender(ctx context.Context, tx sqlExecutor, executionID, userID, inputProjectID string) error {
	var taskID, dispatchID string
	if err := tx.QueryRowContext(ctx, `SELECT task_id,COALESCE(dispatch_id,'') FROM executions WHERE id=?`, executionID).Scan(&taskID, &dispatchID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return persistence.ErrProjectDenied
		}
		return err
	}
	projectID, err := authorizeExecutionActor(ctx, tx, taskID, userID)
	if err != nil {
		return err
	}
	if projectID != inputProjectID {
		return persistence.ErrProjectDenied
	}
	if dispatchID != "" {
		// Shared tasks do not have a compiler origin. Their invocation still
		// requires maintainer access from anyone steering the execution.
		var invocationProject string
		if err := tx.QueryRowContext(ctx, `SELECT i.project_id FROM automation_dispatch_outbox d JOIN automation_invocations i ON i.id=d.invocation_id WHERE d.id=?`, dispatchID).Scan(&invocationProject); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return persistence.ErrProjectDenied
			}
			return err
		}
		if invocationProject != projectID {
			return persistence.ErrProjectDenied
		}
		role, err := projectRole(ctx, tx, userID, projectID)
		if err != nil {
			return err
		}
		if role != "maintainer" && role != "owner" {
			return persistence.ErrProjectDenied
		}
		return requireAutomationExecution(ctx, tx, executionID)
	}
	return nil
}

// CheckSteeringDelivery is read-only: denial must not consume instructions or
// alter replay. It is called again at the transport boundary after preparation.
func (r *ThreadInputRepo) CheckSteeringDelivery(ctx context.Context, executionID string, inputIDs []string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var taskID, userID string
		if err := tx.QueryRowContext(ctx, `SELECT e.task_id,COALESCE(r.user_id,'') FROM executions e LEFT JOIN execution_run_identities r ON r.execution_id=e.id WHERE e.id=?`, executionID).Scan(&taskID, &userID); err != nil {
			return err
		}
		if err := checkExecutionGoal(ctx, tx, executionID, taskID); err != nil {
			return err
		}
		if userID != "" {
			if _, err := authorizeExecutionActor(ctx, tx, taskID, userID); err != nil {
				return err
			}
		}
		for _, id := range inputIDs {
			var author, project string
			if err := tx.QueryRowContext(ctx, `SELECT author_user_id,project_id FROM thread_inputs WHERE id=? AND run_execution_id=? AND input_mode='steering' AND input_status IN ('pending','applied') AND edit_hold=0`, id, executionID).Scan(&author, &project); err != nil {
				return persistence.ErrProjectDenied
			}
			if err := requireInputProvenance(ctx, tx, id); err != nil {
				return err
			}
			if author != "" {
				if err := authorizeSteeringSender(ctx, tx, executionID, author, project); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
