package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pressly/goose/v3"
)

const (
	alertImplementationTaskUseMarker175         = "[Using tool: create_alert_implementation_task]\n"
	alertImplementationTaskResultMarker175      = "[Tool create_alert_implementation_task done]\n"
	alertImplementationTaskErrorResultMarker175 = "[Tool create_alert_implementation_task error]\n"
)

func init() {
	goose.AddNamedMigrationContext("175_alert_implementation_task_history.go", upAlertImplementationTaskHistory175, downAlertImplementationTaskHistory175)
}

type alertImplementationTaskResult175 struct {
	AlertID              string `json:"alert_id"`
	ImplementationTaskID string `json:"implementation_task_id"`
	Task                 struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
	} `json:"task"`
}

type alertImplementationTaskRecovery175 struct {
	alertID   string
	claimant  string
	projectID string
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
			)`); err != nil {
		return fmt.Errorf("backfilling alert implementation task history: %w", err)
	}

	recoveries, err := findAlertImplementationTaskRecoveries175(ctx, tx)
	if err != nil {
		return err
	}
	for _, recovery := range recoveries {
		if _, err := tx.ExecContext(ctx, `UPDATE alerts SET implementation_task_was_linked = 1
			WHERE id = ? AND project_id = ? AND claimant = ? AND implementation_task_was_linked = 0`,
			recovery.alertID, recovery.projectID, recovery.claimant); err != nil {
			return fmt.Errorf("restoring alert implementation task history: %w", err)
		}
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

func findAlertImplementationTaskRecoveries175(ctx context.Context, tx *sql.Tx) ([]alertImplementationTaskRecovery175, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id, task_project_id, output
		FROM executions
		WHERE INSTR(output, ?) > 0
			AND task_id != ''
			AND task_project_id != ''
			AND EXISTS (
				SELECT 1 FROM alerts
				WHERE alerts.claimant = executions.task_id
					AND alerts.project_id = executions.task_project_id
					AND alerts.implementation_task_was_linked = 0
			)`, alertImplementationTaskResultMarker175)
	if err != nil {
		return nil, fmt.Errorf("listing historical alert task results: %w", err)
	}
	type executionOutput struct {
		claimant, projectID, output string
	}
	var outputs []executionOutput
	for rows.Next() {
		var output executionOutput
		if err := rows.Scan(&output.claimant, &output.projectID, &output.output); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scanning historical alert task result: %w", err)
		}
		outputs = append(outputs, output)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	alertExists, err := tx.PrepareContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM alerts
		WHERE id = ? AND project_id = ? AND claimant = ? AND implementation_task_was_linked = 0
	)`)
	if err != nil {
		return nil, err
	}
	defer alertExists.Close()
	taskHistoryExists, err := tx.PrepareContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM llm_usage_events WHERE project_id = ? AND chat_thread_id = ?
	)`)
	if err != nil {
		return nil, err
	}
	defer taskHistoryExists.Close()

	seen := make(map[string]bool)
	var recoveries []alertImplementationTaskRecovery175
	for _, output := range outputs {
		for _, result := range parseAlertImplementationTaskResults175(output.output) {
			if result.Task.ProjectID != output.projectID {
				continue
			}
			var validAlert bool
			if err := alertExists.QueryRowContext(ctx, result.AlertID, output.projectID, output.claimant).Scan(&validAlert); err != nil {
				return nil, err
			}
			if !validAlert {
				continue
			}
			var validTaskHistory bool
			if err := taskHistoryExists.QueryRowContext(ctx, output.projectID, result.ImplementationTaskID).Scan(&validTaskHistory); err != nil {
				return nil, err
			}
			key := output.projectID + "\x00" + result.AlertID
			if !validTaskHistory || seen[key] {
				continue
			}
			seen[key] = true
			recoveries = append(recoveries, alertImplementationTaskRecovery175{
				alertID: result.AlertID, claimant: output.claimant, projectID: output.projectID,
			})
		}
	}
	return recoveries, nil
}

func parseAlertImplementationTaskResults175(output string) []alertImplementationTaskResult175 {
	var results []alertImplementationTaskResult175
	pendingUses := 0
	for len(output) > 0 {
		useAt := markerIndex175(output, alertImplementationTaskUseMarker175)
		doneAt := markerIndex175(output, alertImplementationTaskResultMarker175)
		errorAt := markerIndex175(output, alertImplementationTaskErrorResultMarker175)
		next := min(useAt, doneAt, errorAt)
		if next == len(output) {
			return results
		}
		switch next {
		case useAt:
			pendingUses++
			output = output[useAt+len(alertImplementationTaskUseMarker175):]
		case errorAt:
			if pendingUses > 0 {
				pendingUses--
			}
			output = output[errorAt+len(alertImplementationTaskErrorResultMarker175):]
		case doneAt:
			output = output[doneAt+len(alertImplementationTaskResultMarker175):]
			end := strings.Index(output, "\n[/Tool]")
			if end < 0 {
				return results
			}
			if pendingUses > 0 {
				pendingUses--
				var result alertImplementationTaskResult175
				if err := json.Unmarshal([]byte(strings.TrimSpace(output[:end])), &result); err == nil &&
					isHexID175(result.AlertID) && isHexID175(result.ImplementationTaskID) &&
					result.Task.ID == result.ImplementationTaskID && isHexID175(result.Task.ProjectID) {
					results = append(results, result)
				}
			}
			output = output[end+len("\n[/Tool]"):]
		}
	}
	return results
}

func markerIndex175(output, marker string) int {
	if index := strings.Index(output, marker); index >= 0 {
		return index
	}
	return len(output)
}

func isHexID175(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
