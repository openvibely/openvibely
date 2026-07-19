package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

type AutomationPublicationSnapshot struct {
	Attempt models.AutomationPublicationAttempt
	Steps   []models.AutomationPublicationStep
}

type AutomationPublicationTaskUpdate struct {
	StepKey  string
	TaskID   string
	Title    string
	Prompt   string
	Category models.TaskCategory
	Priority int
}

var ErrAutomationPublicationInProgress = errors.New("automation publication is already in progress")
var ErrAutomationDispatchInFlight = errors.New("automation has in-flight dispatch work")

func (r *AutomationRepo) AcquirePublicationAttempt(ctx context.Context, attemptID, owner string, now time.Time, lease time.Duration) error {
	if attemptID == "" || owner == "" || lease <= 0 {
		return errors.New("automation publication lease context is required")
	}
	expires := now.UTC().Add(lease)
	result, err := r.db.ExecContext(ctx, `UPDATE automation_publication_attempts
		SET claim_owner = ?, claim_expires_at = ?, status = 'publishing', error_message = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status <> 'completed'
		  AND (claim_owner = '' OR claim_expires_at <= ? OR claim_owner = ?)`, owner, expires, attemptID,
		automationCursorSQLTime(now.UTC()), owner)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrAutomationPublicationInProgress
	}
	return nil
}

func (r *AutomationRepo) ReleasePublicationAttempt(ctx context.Context, attemptID, owner string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE automation_publication_attempts
		SET claim_owner = '', claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND claim_owner = ?`, attemptID, owner)
	return err
}

func (r *AutomationRepo) ReservePublicationAttempt(ctx context.Context, projectID, automationID, versionID, planRevision string, effects []models.AutomationPublicationEffect) (*AutomationPublicationSnapshot, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var state models.AutomationVersionState
	if err := conn.QueryRowContext(ctx, `SELECT state FROM automation_versions WHERE project_id = ? AND automation_id = ? AND id = ?`, projectID, automationID, versionID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("automation draft not found")
		}
		return nil, err
	}
	var attemptID string
	err = conn.QueryRowContext(ctx, `SELECT id FROM automation_publication_attempts WHERE project_id = ? AND automation_id = ? AND version_id = ? AND plan_revision = ?`, projectID, automationID, versionID, planRevision).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		if state != models.AutomationVersionDraft {
			return nil, errors.New("automation version is not publishable")
		}
		if err := conn.QueryRowContext(ctx, `INSERT INTO automation_publication_attempts (project_id, automation_id, version_id, plan_revision, status)
			VALUES (?, ?, ?, ?, 'publishing') RETURNING id`, projectID, automationID, versionID, planRevision).Scan(&attemptID); err != nil {
			return nil, err
		}
		for order, effect := range effects {
			status := "pending"
			if effect.Operation == "reuse" || effect.Operation == "unchanged" || effect.Operation == "disable" {
				status = "completed"
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO automation_publication_steps
					(attempt_id, step_key, operation, target_key, display_order, status, resource_type, resource_id)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, attemptID, effect.StepKey, effect.Operation, effect.TargetKey, order, status, effect.ResourceType, effect.ResourceID); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	snapshot, err := loadPublicationSnapshot(ctx, conn, attemptID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	return snapshot, nil
}

func (r *AutomationRepo) GetPublicationAttempt(ctx context.Context, projectID, automationID, versionID, planRevision string) (*AutomationPublicationSnapshot, error) {
	var attemptID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM automation_publication_attempts WHERE project_id = ? AND automation_id = ? AND version_id = ? AND plan_revision = ?`, projectID, automationID, versionID, planRevision).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadPublicationSnapshot(ctx, r.db, attemptID)
}

func loadPublicationSnapshot(ctx context.Context, q queryer, attemptID string) (*AutomationPublicationSnapshot, error) {
	var out AutomationPublicationSnapshot
	err := q.QueryRowContext(ctx, `SELECT id, project_id, automation_id, version_id, plan_revision, status,
		error_message, claim_owner, claim_expires_at, created_at, updated_at, completed_at FROM automation_publication_attempts WHERE id = ?`, attemptID).
		Scan(&out.Attempt.ID, &out.Attempt.ProjectID, &out.Attempt.AutomationID, &out.Attempt.VersionID,
			&out.Attempt.PlanRevision, &out.Attempt.Status, &out.Attempt.ErrorMessage, &out.Attempt.ClaimOwner,
			&out.Attempt.ClaimExpiresAt, &out.Attempt.CreatedAt, &out.Attempt.UpdatedAt, &out.Attempt.CompletedAt)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id, attempt_id, step_key, operation, target_key, display_order, status,
		resource_type, resource_id, error_message, updated_at FROM automation_publication_steps
		WHERE attempt_id = ? ORDER BY display_order, id`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var step models.AutomationPublicationStep
		if err := rows.Scan(&step.ID, &step.AttemptID, &step.StepKey, &step.Operation, &step.TargetKey,
			&step.DisplayOrder, &step.Status, &step.ResourceType, &step.ResourceID, &step.ErrorMessage, &step.UpdatedAt); err != nil {
			return nil, err
		}
		out.Steps = append(out.Steps, step)
	}
	return &out, rows.Err()
}

func (r *AutomationRepo) MarkPublicationStep(ctx context.Context, attemptID, stepKey, status, resourceID, errorMessage string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE automation_publication_steps
		SET status = ?, resource_id = CASE WHEN ? = '' THEN resource_id ELSE ? END,
			error_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE attempt_id = ? AND step_key = ?`, status, resourceID, resourceID, errorMessage, attemptID, stepKey)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("automation publication step not found")
	}
	return nil
}

func (r *AutomationRepo) MarkPublicationAttemptFailed(ctx context.Context, attemptID string, failure error) error {
	message := "publication failed"
	if failure != nil {
		message = failure.Error()
	}
	_, err := r.db.ExecContext(ctx, `UPDATE automation_publication_attempts SET status = 'failed', error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status <> 'completed'`, message, attemptID)
	return err
}

func (r *AutomationRepo) FindCompilerTask(ctx context.Context, projectID, automationID, nodeKey string) (*models.Task, error) {
	createdVia := AutomationCompilerTaskCreatedVia(automationID, nodeKey)
	var taskID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM tasks WHERE project_id = ? AND created_via = ? ORDER BY created_at, id LIMIT 1`, projectID, createdVia).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return NewTaskRepo(r.db, nil).GetByID(ctx, taskID)
}

func AutomationCompilerTaskCreatedVia(automationID, nodeKey string) string {
	return "automation:" + automationID + ":" + nodeKey
}

func (r *AutomationRepo) PublishDraftVersion(ctx context.Context, attemptID string, taskUpdates []AutomationPublicationTaskUpdate) (*models.AutomationDefinition, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	snapshot, err := loadPublicationSnapshot(ctx, conn, attemptID)
	if err != nil {
		return nil, err
	}
	if snapshot.Attempt.Status == "completed" {
		var automation models.Automation
		if err := scanAutomation(conn.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
			lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id,
			created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`, snapshot.Attempt.ProjectID, snapshot.Attempt.AutomationID), &automation); err != nil {
			return nil, err
		}
		definition, err := r.loadDefinition(ctx, conn, automation, snapshot.Attempt.VersionID)
		if err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, err
		}
		committed = true
		return definition, nil
	}
	for _, step := range snapshot.Steps {
		stagedTaskUpdate := step.Operation == "update" && step.ResourceType == "task" && step.Status == "running" && step.ResourceID != ""
		if step.Status != "completed" && !stagedTaskUpdate {
			return nil, fmt.Errorf("publication step %q is not complete", step.StepKey)
		}
	}
	var automation models.Automation
	if err := scanAutomation(conn.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`, snapshot.Attempt.ProjectID, snapshot.Attempt.AutomationID), &automation); err != nil {
		return nil, err
	}
	var state models.AutomationVersionState
	if err := conn.QueryRowContext(ctx, `SELECT state FROM automation_versions WHERE project_id = ? AND automation_id = ? AND id = ?`, snapshot.Attempt.ProjectID, snapshot.Attempt.AutomationID, snapshot.Attempt.VersionID).Scan(&state); err != nil {
		return nil, err
	}
	if state != models.AutomationVersionDraft {
		return nil, errors.New("automation version is not a draft")
	}
	nodeIDs := map[string]string{}
	rows, err := conn.QueryContext(ctx, `SELECT node_key, id FROM automation_nodes WHERE project_id = ? AND automation_id = ? AND version_id = ?`, snapshot.Attempt.ProjectID, snapshot.Attempt.AutomationID, snapshot.Attempt.VersionID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			rows.Close()
			return nil, err
		}
		nodeIDs[key] = id
	}
	rows.Close()
	scheduleEnabled := map[string]bool{}
	var candidateJSON string
	if err := conn.QueryRowContext(ctx, `SELECT candidate_json FROM automation_draft_metadata
		WHERE project_id = ? AND automation_id = ? AND version_id = ?`, snapshot.Attempt.ProjectID,
		snapshot.Attempt.AutomationID, snapshot.Attempt.VersionID).Scan(&candidateJSON); err != nil {
		return nil, err
	}
	var candidate models.AutomationDraftCandidate
	if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
		return nil, err
	}
	for _, node := range candidate.Nodes {
		if enabled, ok := node.Config["enabled"].(bool); ok {
			scheduleEnabled[node.Key] = enabled
		}
	}
	stepsByKey := make(map[string]models.AutomationPublicationStep, len(snapshot.Steps))
	for _, step := range snapshot.Steps {
		stepsByKey[step.StepKey] = step
	}
	appliedTaskUpdates := make(map[string]bool, len(taskUpdates))
	for _, update := range taskUpdates {
		step, ok := stepsByKey[update.StepKey]
		if !ok || step.Operation != "update" || step.ResourceType != "task" || step.Status != "running" || step.ResourceID == "" || step.ResourceID != update.TaskID {
			return nil, fmt.Errorf("publication task update %q does not match a staged update step", update.StepKey)
		}
		result, updateErr := conn.ExecContext(ctx, `UPDATE tasks SET title = ?, prompt = ?, category = ?, priority = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND project_id = ?`, update.Title, update.Prompt, update.Category, update.Priority, update.TaskID, snapshot.Attempt.ProjectID)
		if updateErr != nil {
			if strings.Contains(updateErr.Error(), "UNIQUE constraint failed: tasks.project_id, tasks.title") {
				return nil, ErrDuplicateTask
			}
			return nil, fmt.Errorf("applying publication task update: %w", updateErr)
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if affected != 1 {
			return nil, errors.New("publication task update target is unavailable")
		}
		if _, updateErr := conn.ExecContext(ctx, `UPDATE automation_publication_steps
			SET status = 'completed', error_message = '', updated_at = CURRENT_TIMESTAMP
			WHERE attempt_id = ? AND step_key = ? AND status = 'running'`, attemptID, update.StepKey); updateErr != nil {
			return nil, updateErr
		}
		appliedTaskUpdates[update.StepKey] = true
	}
	for _, step := range snapshot.Steps {
		if step.Operation == "update" && step.ResourceType == "task" && step.Status == "running" && !appliedTaskUpdates[step.StepKey] {
			return nil, fmt.Errorf("staged publication task update %q is missing", step.StepKey)
		}
	}
	newSchedules := map[string]bool{}
	for _, step := range snapshot.Steps {
		if step.ResourceID == "" || (step.ResourceType != "task" && step.ResourceType != "schedule") || step.Operation == "disable" {
			continue
		}
		nodeKey := strings.TrimPrefix(step.TargetKey, step.ResourceType+":")
		if strings.Contains(nodeKey, ":") {
			continue
		}
		nodeID := nodeIDs[nodeKey]
		if nodeID == "" {
			return nil, fmt.Errorf("publication step %q references unknown node", step.StepKey)
		}
		if err := validateAutomationResource(ctx, conn, snapshot.Attempt.ProjectID, step.ResourceType, step.ResourceID); err != nil {
			return nil, err
		}
		relation := "owned"
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(version_id, node_id, resource_type, resource_id, relation) DO NOTHING`,
			snapshot.Attempt.ProjectID, snapshot.Attempt.AutomationID, snapshot.Attempt.VersionID, nodeID, step.ResourceType, step.ResourceID, relation); err != nil {
			return nil, err
		}
		if step.ResourceType == "schedule" {
			newSchedules[step.ResourceID] = true
			var owner string
			ownerErr := conn.QueryRowContext(ctx, `SELECT automation_id FROM automation_trigger_owners WHERE schedule_id = ?`, step.ResourceID).Scan(&owner)
			if ownerErr != nil && !errors.Is(ownerErr, sql.ErrNoRows) {
				return nil, ownerErr
			}
			if ownerErr == nil && owner != snapshot.Attempt.AutomationID {
				return nil, fmt.Errorf("%w: %s", ErrAutomationTriggerOwned, step.ResourceID)
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO automation_trigger_owners
				(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
				VALUES (?, ?, ?, ?, ?, 'active') ON CONFLICT(schedule_id) DO UPDATE SET version_id = excluded.version_id,
				node_id = excluded.node_id, ownership_state = 'active', updated_at = CURRENT_TIMESTAMP`, step.ResourceID,
				snapshot.Attempt.ProjectID, snapshot.Attempt.AutomationID, snapshot.Attempt.VersionID, nodeID); err != nil {
				return nil, err
			}
			if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				scheduleEnabled[nodeKey], step.ResourceID); err != nil {
				return nil, err
			}
		}
	}
	if len(newSchedules) == 0 {
		return nil, errors.New("published automation requires at least one trigger schedule")
	}
	oldRows, err := conn.QueryContext(ctx, `SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ? AND version_id <> ?`, snapshot.Attempt.AutomationID, snapshot.Attempt.VersionID)
	if err != nil {
		return nil, err
	}
	var oldSchedules []string
	for oldRows.Next() {
		var id string
		if err := oldRows.Scan(&id); err != nil {
			oldRows.Close()
			return nil, err
		}
		if !newSchedules[id] {
			oldSchedules = append(oldSchedules, id)
		}
	}
	oldRows.Close()
	for _, id := range oldSchedules {
		if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id); err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM automation_trigger_owners WHERE schedule_id = ? AND automation_id = ?`, id, snapshot.Attempt.AutomationID); err != nil {
			return nil, err
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_versions SET state = 'superseded' WHERE automation_id = ? AND state = 'published'`, snapshot.Attempt.AutomationID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_versions SET state = 'published', published_at = CURRENT_TIMESTAMP WHERE id = ? AND state = 'draft'`, snapshot.Attempt.VersionID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = 'active', published_version_id = ?, archived_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`, snapshot.Attempt.VersionID, snapshot.Attempt.AutomationID, snapshot.Attempt.ProjectID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_publication_attempts SET status = 'completed', error_message = '', completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, attemptID); err != nil {
		return nil, err
	}
	if err := scanAutomation(conn.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`, snapshot.Attempt.ProjectID, snapshot.Attempt.AutomationID), &automation); err != nil {
		return nil, err
	}
	definition, err := r.loadDefinition(ctx, conn, automation, snapshot.Attempt.VersionID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	r.PublishInvalidation(events.AutomationDefinitionUpdated, snapshot.Attempt.ProjectID, models.AutomationBinding{AutomationID: snapshot.Attempt.AutomationID, VersionID: snapshot.Attempt.VersionID})
	return definition, nil
}

func (r *AutomationRepo) SetAutomationLifecycle(ctx context.Context, projectID, automationID string, state models.AutomationLifecycleState) error {
	if state != models.AutomationPaused && state != models.AutomationActive && state != models.AutomationArchived {
		return errors.New("unsupported automation lifecycle state")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var current models.AutomationLifecycleState
	var published sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT lifecycle_state, published_version_id FROM automations WHERE project_id = ? AND id = ?`, projectID, automationID).Scan(&current, &published); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("automation not found")
		}
		return err
	}
	if !published.Valid {
		return errors.New("draft automation cannot change active lifecycle state")
	}
	if current == models.AutomationArchived && state != models.AutomationArchived {
		return errors.New("archived automation cannot be resumed")
	}
	if state == models.AutomationArchived {
		if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = 0, updated_at = CURRENT_TIMESTAMP
			WHERE id IN (SELECT schedule_id FROM automation_trigger_owners WHERE project_id = ? AND automation_id = ?)`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM automation_trigger_owners WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = ?, archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, state, projectID, automationID); err != nil {
			return err
		}
	} else if state == models.AutomationPaused {
		if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = 0, updated_at = CURRENT_TIMESTAMP
			WHERE id IN (SELECT schedule_id FROM automation_trigger_owners WHERE project_id = ? AND automation_id = ?)`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automation_trigger_owners SET ownership_state = 'paused', updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = ?, archived_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, state, projectID, automationID); err != nil {
			return err
		}
	} else {
		var candidateJSON string
		if err := conn.QueryRowContext(ctx, `SELECT candidate_json FROM automation_draft_metadata
			WHERE project_id = ? AND automation_id = ? AND version_id = ?`, projectID, automationID, published.String).Scan(&candidateJSON); err != nil {
			return err
		}
		var candidate models.AutomationDraftCandidate
		if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
			return err
		}
		enabledByNode := make(map[string]bool, len(candidate.Nodes))
		for _, node := range candidate.Nodes {
			if enabled, ok := node.Config["enabled"].(bool); ok {
				enabledByNode[node.Key] = enabled
			}
		}
		rows, err := conn.QueryContext(ctx, `SELECT o.schedule_id, n.node_key
			FROM automation_trigger_owners o
			JOIN automation_nodes n ON n.id = o.node_id AND n.version_id = o.version_id
				AND n.automation_id = o.automation_id AND n.project_id = o.project_id
			WHERE o.project_id = ? AND o.automation_id = ? AND o.version_id = ?`, projectID, automationID, published.String)
		if err != nil {
			return err
		}
		var owned []struct {
			scheduleID string
			nodeKey    string
		}
		for rows.Next() {
			var item struct {
				scheduleID string
				nodeKey    string
			}
			if err := rows.Scan(&item.scheduleID, &item.nodeKey); err != nil {
				rows.Close()
				return err
			}
			owned = append(owned, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range owned {
			if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, enabledByNode[item.nodeKey], item.scheduleID); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automation_trigger_owners SET ownership_state = 'active', updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = ?, archived_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, state, projectID, automationID); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	r.PublishInvalidation(events.AutomationDefinitionUpdated, projectID, models.AutomationBinding{AutomationID: automationID, VersionID: published.String})
	return nil
}

// DeleteAutomation permanently removes one project-scoped Automation definition and
// its Automation-owned metadata. Existing domain resources remain authoritative;
// trigger schedules owned by the Automation are disabled before ownership cascades.
func (r *AutomationRepo) DeleteAutomation(ctx context.Context, projectID, automationID string) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var versionID sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT published_version_id FROM automations WHERE project_id = ? AND id = ?`, projectID, automationID).Scan(&versionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("automation not found")
		}
		return err
	}
	var inFlight int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM automation_invocations i
		LEFT JOIN automation_dispatch_outbox d ON d.invocation_id = i.id
		WHERE i.project_id = ? AND i.automation_id = ?
		  AND (i.status IN ('claimed','dispatched','running')
		    OR d.status IN ('pending','processing','submitted')
		    OR EXISTS (SELECT 1 FROM executions e WHERE e.dispatch_id = d.id AND e.status = 'running'))`, projectID, automationID).Scan(&inFlight); err != nil {
		return err
	}
	if inFlight > 0 {
		return ErrAutomationDispatchInFlight
	}
	if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = 0, updated_at = CURRENT_TIMESTAMP
		WHERE id IN (SELECT schedule_id FROM automation_trigger_owners WHERE project_id = ? AND automation_id = ?)`, projectID, automationID); err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `DELETE FROM automations WHERE project_id = ? AND id = ?`, projectID, automationID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return errors.New("automation not found")
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	r.PublishInvalidation(events.AutomationDefinitionUpdated, projectID, models.AutomationBinding{AutomationID: automationID, VersionID: versionID.String})
	return nil
}
