package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/persistence"
)

// Historical versions lack a recorded publisher; do not invent one. New version
// identities are immutable and permission is always resolved from current grants.
func requireAutomationPublisher(ctx context.Context, tx sqlExecutor, projectID, versionID string) error {
	if err := requireSlackPublisher(ctx, tx, versionID); err != nil {
		return err
	}
	if err := requireDiscordPublisher(ctx, tx, versionID); err != nil {
		return err
	}
	var userID string
	err := tx.QueryRowContext(ctx, `SELECT p.user_id FROM automation_version_publishers p JOIN automation_versions v ON v.id=p.version_id WHERE p.version_id=? AND v.project_id=?`, versionID, projectID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	role, err := projectRole(ctx, tx, userID, projectID)
	if err != nil {
		return err
	}
	if role != "maintainer" && role != "owner" {
		return persistence.ErrProjectDenied
	}
	return nil
}

// Apply both immutable automation identities to execution startup and periodic
// recovery checks, even when the task itself is shared rather than compiler-owned.
func requireAutomationExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	if err := requireAutomationContinuationExecution(ctx, tx, executionID); err != nil {
		return err
	}
	var projectID, versionID, initiator, executor, invocationID string
	err := tx.QueryRowContext(ctx, `SELECT i.id,i.project_id,i.version_id,COALESCE(u.user_id,''),COALESCE(r.user_id,'') FROM executions e JOIN automation_dispatch_outbox d ON d.id=e.dispatch_id JOIN automation_invocations i ON i.id=d.invocation_id LEFT JOIN automation_invocation_users u ON u.invocation_id=i.id LEFT JOIN execution_run_identities r ON r.execution_id=e.id WHERE e.id=?`, executionID).Scan(&invocationID, &projectID, &versionID, &initiator, &executor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := requireSlackInvocation(ctx, tx, invocationID); err != nil {
		return err
	}
	if err := requireDiscordInvocation(ctx, tx, invocationID); err != nil {
		return err
	}
	if err := requireAutomationPublisher(ctx, tx, projectID, versionID); err != nil {
		return err
	}
	if initiator != "" {
		if executor != initiator {
			return persistence.ErrProjectDenied
		}
		role, err := projectRole(ctx, tx, initiator, projectID)
		if err != nil {
			return err
		}
		if role != "owner" && role != "maintainer" {
			return persistence.ErrProjectDenied
		}
	}
	return nil
}
