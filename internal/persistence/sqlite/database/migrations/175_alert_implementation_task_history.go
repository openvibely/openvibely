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
					AND activity.status = 'completed'
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
