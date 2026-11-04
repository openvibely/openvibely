package sqlite

import (
	"context"

	"github.com/openvibely/openvibely/internal/persistence"
)

// Continuations use activity resources rather than executions.dispatch_id. Check
// their durable automation bindings too, including when recovering without context.
func requireAutomationContinuationBinding(ctx context.Context, tx sqlExecutor, projectID, automationID, versionID, invocationID string) error {
	var actualProject, actualAutomation string
	if err := tx.QueryRowContext(ctx, `SELECT project_id,automation_id FROM automation_versions WHERE id=?`, versionID).Scan(&actualProject, &actualAutomation); err != nil {
		return err
	}
	if actualProject != projectID || actualAutomation != automationID {
		return persistence.ErrProjectDenied
	}
	if err := requireAutomationPublisher(ctx, tx, projectID, versionID); err != nil {
		return err
	}
	if invocationID == "" {
		return nil
	}
	var actualVersion, userID string
	if err := tx.QueryRowContext(ctx, `SELECT i.project_id,i.automation_id,i.version_id,COALESCE(u.user_id,'') FROM automation_invocations i LEFT JOIN automation_invocation_users u ON u.invocation_id=i.id WHERE i.id=?`, invocationID).Scan(&actualProject, &actualAutomation, &actualVersion, &userID); err != nil {
		return err
	}
	if actualProject != projectID || actualAutomation != automationID || actualVersion != versionID {
		return persistence.ErrProjectDenied
	}
	if err := requireSlackInvocation(ctx, tx, invocationID); err != nil {
		return err
	}
	if err := requireDiscordInvocation(ctx, tx, invocationID); err != nil {
		return err
	}
	if userID != "" {
		role, err := projectRole(ctx, tx, userID, projectID)
		if err != nil {
			return err
		}
		if role != "owner" && role != "maintainer" {
			return persistence.ErrProjectDenied
		}
	}
	return nil
}

func requireAutomationContinuationExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.project_id,a.automation_id,a.version_id,COALESCE(a.invocation_id,''),t.project_id,COALESCE(ri.user_id,'') FROM automation_activities a JOIN automation_activity_resources r ON r.activity_id=a.id JOIN executions e ON e.id=r.resource_id JOIN tasks t ON t.id=e.task_id LEFT JOIN execution_run_identities ri ON ri.execution_id=e.id WHERE r.resource_type='execution' AND r.resource_id=?`, executionID)
	if err != nil {
		return err
	}
	type binding struct{ project, automation, version, invocation, taskProject, executor string }
	var bindings []binding
	for rows.Next() {
		var b binding
		if err := rows.Scan(&b.project, &b.automation, &b.version, &b.invocation, &b.taskProject, &b.executor); err != nil {
			rows.Close()
			return err
		}
		bindings = append(bindings, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if b.project != b.taskProject {
			return persistence.ErrProjectDenied
		}
		if err := requireAutomationContinuationAuthor(ctx, tx, b.project, b.executor); err != nil {
			return err
		}
		if err := requireAutomationContinuationBinding(ctx, tx, b.project, b.automation, b.version, b.invocation); err != nil {
			return err
		}
	}
	return nil
}

// Legacy un-attributed inputs retain compatibility; recorded authors must retain
// the same manage permission required for automation execution.
func requireAutomationContinuationAuthor(ctx context.Context, tx sqlExecutor, projectID, userID string) error {
	if userID == "" {
		return nil
	}
	role, err := projectRole(ctx, tx, userID, projectID)
	if err != nil {
		return err
	}
	if role != "owner" && role != "maintainer" {
		return persistence.ErrProjectDenied
	}
	return nil
}
