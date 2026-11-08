package sqlite

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/internal/persistence"
)

// requireInputProvenance rechecks saved ingress evidence in the transaction
// consuming an instruction. External reviewer trust is independent of the
// application user who forwarded it, including for legacy anonymous inputs.
func requireInputProvenance(ctx context.Context, tx sqlExecutor, inputID string) error {
	if _, _, err := telegramInputContext(ctx, tx, inputID); err != nil {
		return err
	}
	if _, _, err := discordInputContext(ctx, tx, inputID); err != nil {
		return err
	}
	if models.RequiredReviewerIdentity(ctx) {
		var missing bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM github_reviewer_input_provenance p WHERE p.input_id=? AND NOT EXISTS(SELECT 1 FROM github_reviewer_input_identities i JOIN github_authorized_actors a ON a.id=i.actor_id WHERE i.input_id=p.input_id AND lower(a.github_login)=p.reviewer_login))`, inputID).Scan(&missing)
		if err != nil {
			return err
		}
		if missing {
			return persistence.ErrProjectDenied
		}
	}

	if err := requireReviewerIdentities(ctx, tx, inputID, false); err != nil {
		return err
	}
	if err := requireSlackInput(ctx, tx, inputID); err != nil {
		return err
	}
	var revoked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM github_reviewer_input_provenance f
 WHERE f.input_id=? AND NOT EXISTS (
 SELECT 1 FROM github_authorized_actors a WHERE lower(a.github_login)=f.reviewer_login
 ))`, inputID).Scan(&revoked); err != nil {
		return err
	}
	if revoked {
		return persistence.ErrProjectDenied
	}
	return nil
}

// copyReviewerInput records all reviewers consumed by the execution independently
// of queue and PR retention. Call only within the consuming transaction.
func copyReviewerInput(ctx context.Context, tx sqlExecutor, inputID, executionID string) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO github_reviewer_execution_identities(execution_id,project_id,actor_id,host,external_user_id,application_user_id) SELECT ?,project_id,actor_id,host,external_user_id,application_user_id FROM github_reviewer_input_identities WHERE input_id=?`, executionID, inputID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO github_reviewer_execution_provenance(execution_id,reviewer_login)
 SELECT ?,reviewer_login FROM github_reviewer_input_provenance WHERE input_id=?`, executionID, inputID)
	return err
}

func requireReviewerExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	if models.RequiredReviewerIdentity(ctx) {
		var missing bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM github_reviewer_execution_provenance p WHERE p.execution_id=? AND NOT EXISTS(SELECT 1 FROM github_reviewer_execution_identities i JOIN github_authorized_actors a ON a.id=i.actor_id WHERE i.execution_id=p.execution_id AND lower(a.github_login)=p.reviewer_login))`, executionID).Scan(&missing)
		if err != nil {
			return err
		}
		if missing {
			return persistence.ErrProjectDenied
		}
	}

	if err := requireReviewerIdentities(ctx, tx, executionID, true); err != nil {
		return err
	}
	var revoked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM github_reviewer_execution_provenance p WHERE p.execution_id=? AND NOT EXISTS (
 SELECT 1 FROM github_authorized_actors a WHERE lower(a.github_login)=p.reviewer_login
 ))`, executionID).Scan(&revoked); err != nil {
		return err
	}
	if revoked {
		return persistence.ErrProjectDenied
	}
	return nil
}
