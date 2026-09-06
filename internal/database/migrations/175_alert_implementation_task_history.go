package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("175_alert_implementation_task_history.go", upAlertImplementationTaskHistory175, downAlertImplementationTaskHistory175)
}

func upAlertImplementationTaskHistory175(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE alerts ADD COLUMN implementation_task_was_linked INTEGER NOT NULL DEFAULT 0
		CHECK(implementation_task_was_linked IN (0, 1))`); err != nil {
		return fmt.Errorf("adding alert implementation task history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alerts AS alert
		SET implementation_task_was_linked = 1
		WHERE implementation_task_id IS NOT NULL
			OR processing_state = 'implementation_task_linked'
			OR EXISTS (
				SELECT 1
				FROM automation_activities activity
				JOIN automation_activity_resources alert_resource
					ON alert_resource.activity_id = activity.id
					AND alert_resource.resource_type = 'alert'
					AND alert_resource.resource_id = alert.id
				JOIN automation_activity_resources task_resource
					ON task_resource.activity_id = activity.id
					AND task_resource.resource_type = 'task'
				WHERE activity.project_id = alert.project_id
					AND activity.activity_type = 'create_implementation_task'
			)
			OR (
				alert.decision_state = 'approved'
				AND alert.processing_state = 'completed'
				AND alert.claimant != ''
				AND EXISTS (
					SELECT 1
					FROM automation_artifact_mailbox_owners owner
					JOIN automation_definition_resources inbox_resource
						ON inbox_resource.project_id = owner.project_id
						AND inbox_resource.automation_id = owner.automation_id
						AND inbox_resource.resource_type = 'task'
						AND inbox_resource.resource_id = alert.claimant
					JOIN automation_nodes inbox
						ON inbox.id = inbox_resource.node_id
						AND inbox.project_id = inbox_resource.project_id
						AND inbox.automation_id = inbox_resource.automation_id
						AND inbox.version_id = inbox_resource.version_id
						AND inbox.role = 'native_inbox'
					JOIN automation_edges edge
						ON edge.project_id = inbox.project_id
						AND edge.automation_id = inbox.automation_id
						AND edge.version_id = inbox.version_id
						AND edge.source_node_id = inbox.id
					JOIN automation_nodes implementation
						ON implementation.id = edge.target_node_id
						AND implementation.project_id = edge.project_id
						AND implementation.automation_id = edge.automation_id
						AND implementation.version_id = edge.version_id
						AND implementation.role = 'implementation'
					WHERE owner.project_id = alert.project_id
						AND owner.artifact_type = 'alert'
						AND owner.artifact_id = alert.id
				)
			)`); err != nil {
		return fmt.Errorf("backfilling alert implementation task history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_alerts_project_implementation_task_was_linked
		ON alerts(project_id, implementation_task_was_linked, created_at DESC, id DESC)`); err != nil {
		return fmt.Errorf("indexing alert implementation task history: %w", err)
	}
	return nil
}

func downAlertImplementationTaskHistory175(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_alerts_project_implementation_task_was_linked`); err != nil {
		return fmt.Errorf("dropping alert implementation task history index: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE alerts DROP COLUMN implementation_task_was_linked`); err != nil {
		return fmt.Errorf("dropping alert implementation task history: %w", err)
	}
	return nil
}
