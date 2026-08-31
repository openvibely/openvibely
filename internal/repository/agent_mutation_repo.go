package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// AgentMutationRepo persists historical `agent_config_mutations` audit rows for
// model-driven skill_manage proposals. Every applied or blocked proposal must
// produce one row so debugging can explain why no persisted-state change
// occurred. The table/repo names are historical; agents are user-managed now.
type AgentMutationRepo struct {
	db *sql.DB
}

func NewAgentMutationRepo(db *sql.DB) *AgentMutationRepo {
	return &AgentMutationRepo{db: db}
}

const mutationCols = `id, lifecycle_execution_id, task_id, task_run_id, project_id,
                      actor_agent_id, target_type, target_key, action,
                      proposed_payload_json, validation_status, validation_errors_json,
                      changed_paths_json, imported_config_changes_json, evidence_refs_json,
                      idempotency_key, created_at`

func scanMutation(row interface{ Scan(...any) error }) (*models.AgentConfigMutation, error) {
	var m models.AgentConfigMutation
	var execID, taskID, actor sql.NullString
	var target, status string
	if err := row.Scan(&m.ID, &execID, &taskID, &m.TaskRunID, &m.ProjectID,
		&actor, &target, &m.TargetKey, &m.Action,
		&m.ProposedPayloadJSON, &status, &m.ValidationErrorsJSON,
		&m.ChangedPathsJSON, &m.ImportedChangesJSON, &m.EvidenceRefsJSON,
		&m.IdempotencyKey, &m.CreatedAt); err != nil {
		return nil, err
	}
	if execID.Valid {
		m.LifecycleExecutionID = execID.String
	}
	if taskID.Valid {
		m.TaskID = taskID.String
	}
	if actor.Valid {
		m.ActorAgentID = actor.String
	}
	m.TargetType = models.MutationTargetType(target)
	m.ValidationStatus = models.MutationValidationStatus(status)
	return &m, nil
}

// Create inserts one mutation audit row. Empty foreign keys are stored as NULL
// so deleting a parent task or execution does not cascade-clear the audit.
func (r *AgentMutationRepo) Create(ctx context.Context, m *models.AgentConfigMutation) error {
	if m.ValidationStatus == "" {
		m.ValidationStatus = models.MutationStatusApplied
	}
	payload := m.ProposedPayloadJSON
	if payload == "" {
		payload = "{}"
	}
	errs := m.ValidationErrorsJSON
	if errs == "" {
		errs = "[]"
	}
	paths := m.ChangedPathsJSON
	if paths == "" {
		paths = "[]"
	}
	imported := m.ImportedChangesJSON
	if imported == "" {
		imported = "[]"
	}
	evidence := m.EvidenceRefsJSON
	if evidence == "" {
		evidence = "[]"
	}
	err := queryRowBoundSQLite(ctx, r.db, `
        INSERT INTO agent_config_mutations
            (id, lifecycle_execution_id, task_id, task_run_id, project_id,
             actor_agent_id, target_type, target_key, action,
             proposed_payload_json, validation_status, validation_errors_json,
             changed_paths_json, imported_config_changes_json, evidence_refs_json,
             idempotency_key)
        VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        RETURNING id, created_at`,
		nullIfEmpty(m.LifecycleExecutionID), nullIfEmpty(m.TaskID),
		m.TaskRunID, m.ProjectID,
		nullIfEmpty(m.ActorAgentID),
		string(m.TargetType), m.TargetKey, m.Action,
		payload, string(m.ValidationStatus), errs,
		paths, imported, evidence,
		m.IdempotencyKey,
	).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return fmt.Errorf("creating agent config mutation: %w", err)
	}
	return nil
}

// FindAppliedByIdempotencyKey returns an applied mutation row matching the
// supplied key, or sql.ErrNoRows if none exists. Empty keys always miss so
// unkeyed proposals are never deduplicated.
func (r *AgentMutationRepo) FindAppliedByIdempotencyKey(ctx context.Context, key string) (*models.AgentConfigMutation, error) {
	if key == "" {
		return nil, sql.ErrNoRows
	}
	row := r.db.QueryRowContext(ctx, `
        SELECT `+mutationCols+`
        FROM agent_config_mutations
        WHERE idempotency_key = ? AND validation_status = 'applied'`, key)
	return scanMutation(row)
}

// ListForExecution returns every mutation row produced by one lifecycle
// execution, ordered by creation time so the UI can render them as activity.
func (r *AgentMutationRepo) ListForExecution(ctx context.Context, executionID string) ([]models.AgentConfigMutation, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT `+mutationCols+`
        FROM agent_config_mutations
        WHERE lifecycle_execution_id = ?
        ORDER BY created_at ASC`, executionID)
	if err != nil {
		return nil, fmt.Errorf("listing mutations for execution %s: %w", executionID, err)
	}
	defer rows.Close()
	var out []models.AgentConfigMutation
	for rows.Next() {
		m, err := scanMutation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ListForTask returns mutations attached to a task across all lifecycle
// executions, ordered by creation time.
func (r *AgentMutationRepo) ListForTask(ctx context.Context, taskID string) ([]models.AgentConfigMutation, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT `+mutationCols+`
        FROM agent_config_mutations
        WHERE task_id = ?
        ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing mutations for task %s: %w", taskID, err)
	}
	defer rows.Close()
	var out []models.AgentConfigMutation
	for rows.Next() {
		m, err := scanMutation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
