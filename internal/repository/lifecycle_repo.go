package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
)

// LifecycleRepo persists agent lifecycle hooks and lifecycle execution rows.
type LifecycleRepo struct {
	db *sql.DB

	eventSeqMu sync.Mutex
}

func NewLifecycleRepo(db *sql.DB) *LifecycleRepo {
	return &LifecycleRepo{db: db}
}

const hookCols = `id, agent_id, when_slot, skill_key, prompt_override, output_contract,
                  blocking, enabled, permissions_json, run_policy_json, schedule_json,
                  payload_json, created_at, updated_at`

// prefixedHookCols returns hookCols with each column prefixed by the supplied
// table alias. Used by HooksForWhen so the JOIN against agents does not need
// a separate scan path.
func prefixedHookCols(alias string) string {
	cols := []string{
		"id", "agent_id", "when_slot", "skill_key", "prompt_override", "output_contract",
		"blocking", "enabled", "permissions_json", "run_policy_json", "schedule_json",
		"payload_json", "created_at", "updated_at",
	}
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += alias + "." + c
	}
	return out
}

func scanHook(row interface{ Scan(...any) error }) (*models.AgentLifecycleHook, error) {
	var h models.AgentLifecycleHook
	var scheduleJSON, payloadJSON sql.NullString
	var blocking, enabled int
	var when, contract string
	if err := row.Scan(&h.ID, &h.AgentID, &when, &h.SkillKey, &h.PromptOverride, &contract,
		&blocking, &enabled, &h.PermissionsJSON, &h.RunPolicyJSON, &scheduleJSON,
		&payloadJSON, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return nil, err
	}
	if payloadJSON.Valid {
		h.PayloadJSON = payloadJSON.String
	}
	h.When = models.LifecycleWhen(when)
	h.OutputContract = models.LifecycleOutputContract(contract)
	h.Blocking = blocking != 0
	h.Enabled = enabled != 0
	if scheduleJSON.Valid {
		h.ScheduleJSON = scheduleJSON.String
	}
	return &h, nil
}

// CreateHook persists a new lifecycle hook binding.
func (r *LifecycleRepo) CreateHook(ctx context.Context, h *models.AgentLifecycleHook) error {
	blocking := 0
	if h.Blocking {
		blocking = 1
	}
	enabled := 0
	if h.Enabled {
		enabled = 1
	}
	permissions := h.PermissionsJSON
	if permissions == "" {
		permissions = "{}"
	}
	runPolicy := h.RunPolicyJSON
	if runPolicy == "" {
		runPolicy = "{}"
	}
	payload := h.PayloadJSON
	if payload == "" {
		payload = "{}"
	}
	var schedule any
	if h.ScheduleJSON != "" {
		schedule = h.ScheduleJSON
	}
	err := r.db.QueryRowContext(ctx, `
        INSERT INTO agent_lifecycle_hooks
            (id, agent_id, when_slot, skill_key, prompt_override, output_contract,
             blocking, enabled, permissions_json, run_policy_json, schedule_json, payload_json)
        VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        RETURNING id, created_at, updated_at`,
		h.AgentID, string(h.When), h.SkillKey, h.PromptOverride, string(h.OutputContract),
		blocking, enabled, permissions, runPolicy, schedule, payload,
	).Scan(&h.ID, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating lifecycle hook: %w", err)
	}
	return nil
}

// UpdateHook applies edits to an existing lifecycle hook.
func (r *LifecycleRepo) UpdateHook(ctx context.Context, h *models.AgentLifecycleHook) error {
	blocking := 0
	if h.Blocking {
		blocking = 1
	}
	enabled := 0
	if h.Enabled {
		enabled = 1
	}
	permissions := h.PermissionsJSON
	if permissions == "" {
		permissions = "{}"
	}
	runPolicy := h.RunPolicyJSON
	if runPolicy == "" {
		runPolicy = "{}"
	}
	payload := h.PayloadJSON
	if payload == "" {
		payload = "{}"
	}
	var schedule any
	if h.ScheduleJSON != "" {
		schedule = h.ScheduleJSON
	}
	_, err := r.db.ExecContext(ctx, `
        UPDATE agent_lifecycle_hooks
        SET when_slot = ?, skill_key = ?, prompt_override = ?, output_contract = ?,
            blocking = ?, enabled = ?, permissions_json = ?, run_policy_json = ?,
            schedule_json = ?, payload_json = ?, updated_at = datetime('now')
        WHERE id = ?`,
		string(h.When), h.SkillKey, h.PromptOverride, string(h.OutputContract),
		blocking, enabled, permissions, runPolicy, schedule, payload, h.ID,
	)
	if err != nil {
		return fmt.Errorf("updating lifecycle hook: %w", err)
	}
	return nil
}

// DeleteHook removes a lifecycle hook configuration.
func (r *LifecycleRepo) DeleteHook(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM agent_lifecycle_hooks WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting lifecycle hook: %w", err)
	}
	return nil
}

// HooksByAgent returns all hooks configured for one agent, ordered by `when` value.
func (r *LifecycleRepo) HooksByAgent(ctx context.Context, agentID string) ([]models.AgentLifecycleHook, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT `+hookCols+`
        FROM agent_lifecycle_hooks
        WHERE agent_id = ?
        ORDER BY when_slot ASC, created_at ASC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("listing hooks: %w", err)
	}
	defer rows.Close()
	var out []models.AgentLifecycleHook
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// HooksForWhen returns enabled hooks across all agents for one `when` value.
// Hooks are returned ordered by agent_id, created_at for deterministic execution.
//
// Hooks owned by archived or disabled agents are excluded so a single
// (skill_key, when) pair is not invoked once per leftover copy of a system
// agent that was renamed or absorbed (for example, both the legacy "memory"
// and current "memory_curator" agents carrying the same recall_memory /
// update_memory hooks). Without this filter, agent_lifecycle_hooks rows
// linger after MarkArchived flips the owning agent to enabled=0, so every
// task run records duplicate before_run/after_complete executions per
// archived copy.
func (r *LifecycleRepo) HooksForWhen(ctx context.Context, when models.LifecycleWhen) ([]models.AgentLifecycleHook, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT `+prefixedHookCols("h")+`
        FROM agent_lifecycle_hooks h
        JOIN agents a ON a.id = h.agent_id
        WHERE h.when_slot = ?
          AND h.enabled = 1
          AND a.archived_at IS NULL
          AND COALESCE(a.generated_status, 'user_edited') <> 'archived'
        ORDER BY h.agent_id ASC, h.created_at ASC`, string(when))
	if err != nil {
		return nil, fmt.Errorf("listing hooks for %s: %w", when, err)
	}
	defer rows.Close()
	var out []models.AgentLifecycleHook
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

const execCols = `id, task_id, task_run_id, agent_id, when_slot, lifecycle_hook_id,
                  parent_execution_id, skill_key, output_contract, status,
                  input_json, output_json, error, attempt_count, priority,
                  next_retry_at, idempotency_key,
                  started_at, completed_at`

const lifecycleExecutionListCols = `id, agent_id, when_slot, skill_key, output_contract, status,
					 output_json, error, started_at, completed_at`

const listExecutionsForTaskSQL = `
	        SELECT ` + lifecycleExecutionListCols + `
	        FROM lifecycle_executions
	        WHERE task_id = ?
	        ORDER BY started_at DESC, id DESC`

func scanExecution(row interface{ Scan(...any) error }) (*models.LifecycleExecution, error) {
	var e models.LifecycleExecution
	var agentID, hookID, parent sql.NullString
	var taskRunID sql.NullString
	var when, contract, status string
	if err := row.Scan(&e.ID, &e.TaskID, &taskRunID, &agentID, &when, &hookID,
		&parent, &e.SkillKey, &contract, &status,
		&e.InputJSON, &e.OutputJSON, &e.Error, &e.AttemptCount,
		&e.Priority, &e.NextRetryAt,
		&e.IdempotencyKey,
		&e.StartedAt, &e.CompletedAt); err != nil {
		return nil, err
	}
	if taskRunID.Valid {
		e.TaskRunID = taskRunID.String
	}
	if agentID.Valid {
		e.AgentID = agentID.String
	}
	if hookID.Valid {
		v := hookID.String
		e.LifecycleHookID = &v
	}
	if parent.Valid {
		v := parent.String
		e.ParentExecID = &v
	}
	e.When = models.LifecycleWhen(when)
	e.OutputContract = models.LifecycleOutputContract(contract)
	e.Status = models.LifecycleExecutionStatus(status)
	return &e, nil
}

func scanExecutionList(row interface{ Scan(...any) error }) (*models.LifecycleExecution, error) {
	var e models.LifecycleExecution
	var agentID sql.NullString
	var when, contract, status string
	if err := row.Scan(&e.ID, &agentID, &when, &e.SkillKey, &contract, &status,
		&e.OutputJSON, &e.Error, &e.StartedAt, &e.CompletedAt); err != nil {
		return nil, err
	}
	if agentID.Valid {
		e.AgentID = agentID.String
	}
	e.When = models.LifecycleWhen(when)
	e.OutputContract = models.LifecycleOutputContract(contract)
	e.Status = models.LifecycleExecutionStatus(status)
	return &e, nil
}

// CreateExecution records the start of a lifecycle hook invocation.
func (r *LifecycleRepo) CreateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	var hookID, parent any
	if e.LifecycleHookID != nil {
		hookID = *e.LifecycleHookID
	}
	if e.ParentExecID != nil {
		parent = *e.ParentExecID
	}
	if e.Status == "" {
		e.Status = models.LifecycleExecPending
	}
	input := e.InputJSON
	if input == "" {
		input = "{}"
	}
	output := e.OutputJSON
	if output == "" {
		output = "{}"
	}
	err := r.db.QueryRowContext(ctx, `
        INSERT INTO lifecycle_executions
            (id, task_id, task_run_id, agent_id, when_slot, lifecycle_hook_id,
             parent_execution_id, skill_key, output_contract, status,
             input_json, output_json, error, attempt_count, priority,
             next_retry_at, idempotency_key)
        VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        RETURNING id, started_at`,
		e.TaskID, e.TaskRunID, nullIfEmpty(e.AgentID), string(e.When), hookID,
		parent, e.SkillKey, string(e.OutputContract), string(e.Status),
		input, output, e.Error, e.AttemptCount, e.Priority,
		e.NextRetryAt, e.IdempotencyKey,
	).Scan(&e.ID, &e.StartedAt)
	if err != nil {
		return fmt.Errorf("creating lifecycle execution: %w", err)
	}
	return nil
}

// FindExecutionByIdempotencyKey returns an existing execution with the supplied
// idempotency key, or sql.ErrNoRows if none exists. An empty key returns
// sql.ErrNoRows so callers do not collide on unkeyed rows.
func (r *LifecycleRepo) FindExecutionByIdempotencyKey(ctx context.Context, key string) (*models.LifecycleExecution, error) {
	if key == "" {
		return nil, sql.ErrNoRows
	}
	row := r.db.QueryRowContext(ctx, `
	        SELECT `+execCols+`
	        FROM lifecycle_executions
	        WHERE idempotency_key = ?`, key)
	return scanExecution(row)
}

type LifecycleTaskRunFreshness struct {
	Stale           bool
	TaskID          string
	SourceRunID     string
	SourceStartedAt string
	SourceRowID     int64
	LatestRunID     string
	LatestStartedAt string
	LatestRowID     int64
}

func (r *LifecycleRepo) TaskRunFreshness(ctx context.Context, taskID, taskRunID string) (LifecycleTaskRunFreshness, error) {
	detail := LifecycleTaskRunFreshness{TaskID: taskID, SourceRunID: taskRunID}
	if taskID == "" || taskRunID == "" {
		return detail, nil
	}
	stale := 0
	if err := r.db.QueryRowContext(ctx, `
		WITH run_heads AS (
			SELECT task_run_id, MIN(rowid) AS head_rowid
			FROM lifecycle_executions
			WHERE task_id = ? AND task_run_id != ''
			GROUP BY task_run_id
		), ordered AS (
			SELECT h.task_run_id, e.started_at, h.head_rowid
			FROM run_heads h
			JOIN lifecycle_executions e ON e.rowid = h.head_rowid
			ORDER BY e.started_at DESC, h.head_rowid DESC
		), source AS (
			SELECT task_run_id, started_at, head_rowid
			FROM ordered
			WHERE task_run_id = ?
		), latest AS (
			SELECT task_run_id, started_at, head_rowid
			FROM ordered
			LIMIT 1
		)
		SELECT
			COALESCE((SELECT started_at FROM source), ''),
			COALESCE((SELECT head_rowid FROM source), 0),
			COALESCE((SELECT task_run_id FROM latest), ''),
			COALESCE((SELECT started_at FROM latest), ''),
			COALESCE((SELECT head_rowid FROM latest), 0),
			CASE
				WHEN COALESCE((SELECT head_rowid FROM source), 0) = 0 THEN 1
				WHEN COALESCE((SELECT task_run_id FROM latest), '') != ? THEN 1
				ELSE 0
			END`, taskID, taskRunID, taskRunID).Scan(&detail.SourceStartedAt, &detail.SourceRowID, &detail.LatestRunID, &detail.LatestStartedAt, &detail.LatestRowID, &stale); err != nil {
		return detail, fmt.Errorf("checking lifecycle task run freshness: %w", err)
	}
	detail.Stale = stale != 0
	return detail, nil
}

func (r *LifecycleRepo) HasNewerTaskRun(ctx context.Context, taskID, taskRunID string) (bool, error) {
	detail, err := r.TaskRunFreshness(ctx, taskID, taskRunID)
	if err != nil {
		return false, err
	}
	return detail.Stale, nil
}

// UpdateExecution applies the terminal status, output, and timing.
func (r *LifecycleRepo) UpdateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	output := e.OutputJSON
	if output == "" {
		output = "{}"
	}
	_, err := r.db.ExecContext(ctx, `
        UPDATE lifecycle_executions
        SET status = ?, output_json = ?, error = ?, attempt_count = ?,
            next_retry_at = ?, completed_at = ?
        WHERE id = ?`,
		string(e.Status), output, e.Error, e.AttemptCount,
		e.NextRetryAt, e.CompletedAt, e.ID,
	)
	if err != nil {
		return fmt.Errorf("updating lifecycle execution: %w", err)
	}
	return nil
}

// ListExecutionsForTask returns compact lifecycle executions attached to a task,
// ordered newest-first with a deterministic ID tie-breaker so the UI shows the
// most recent activity without scrolling. It intentionally omits raw input and
// retry/hook metadata that are not part of the prompt-safe list response.
func (r *LifecycleRepo) ListExecutionsForTask(ctx context.Context, taskID string) ([]models.LifecycleExecution, error) {
	rows, err := r.db.QueryContext(ctx, listExecutionsForTaskSQL, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing executions for task %s: %w", taskID, err)
	}
	defer rows.Close()
	var out []models.LifecycleExecution
	for rows.Next() {
		e, err := scanExecutionList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// AppendExecutionEvent persists one trace event for a lifecycle execution.
func (r *LifecycleRepo) AppendExecutionEvent(ctx context.Context, event *models.LifecycleExecutionEvent) error {
	if event == nil {
		return fmt.Errorf("appending lifecycle execution event: nil event")
	}
	if event.LifecycleExecutionID == "" {
		return fmt.Errorf("appending lifecycle execution event: missing lifecycle execution id")
	}
	if event.EventType == "" {
		event.EventType = "event"
	}
	payload := event.PayloadJSON
	if payload == "" {
		payload = "{}"
	}

	// SQLite has no portable single-statement sequence allocator that is pleasant
	// across our test/prod driver, so serialize sequence assignment per repo.
	r.eventSeqMu.Lock()
	defer r.eventSeqMu.Unlock()

	err := r.db.QueryRowContext(ctx, `
		INSERT INTO lifecycle_execution_events
		    (lifecycle_execution_id, seq, event_type, payload_json)
		VALUES (?, COALESCE((SELECT MAX(seq) + 1 FROM lifecycle_execution_events WHERE lifecycle_execution_id = ?), 1), ?, ?)
		RETURNING id, seq, created_at`,
		event.LifecycleExecutionID, event.LifecycleExecutionID, event.EventType, payload,
	).Scan(&event.ID, &event.Seq, &event.CreatedAt)
	if err != nil {
		return fmt.Errorf("appending lifecycle execution event: %w", err)
	}
	event.PayloadJSON = payload
	return nil
}

// ListExecutionEvents returns trace events for a lifecycle execution in emitted order.
func (r *LifecycleRepo) ListExecutionEvents(ctx context.Context, lifecycleExecutionID string) ([]models.LifecycleExecutionEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, lifecycle_execution_id, seq, event_type, payload_json, created_at
		FROM lifecycle_execution_events
		WHERE lifecycle_execution_id = ?
		ORDER BY seq ASC`, lifecycleExecutionID)
	if err != nil {
		return nil, fmt.Errorf("listing lifecycle execution events: %w", err)
	}
	defer rows.Close()
	var out []models.LifecycleExecutionEvent
	for rows.Next() {
		var e models.LifecycleExecutionEvent
		if err := rows.Scan(&e.ID, &e.LifecycleExecutionID, &e.Seq, &e.EventType, &e.PayloadJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PatchExecutionOutputSkills rewrites the `skills` field inside a route_task
// execution's output_json to reflect the post-merge (always-use + LLM) list,
// preserving all other fields (confidence, reason, etc.) intact.  It is a
// no-op when execID is empty or the row cannot be read.
func (r *LifecycleRepo) PatchExecutionOutputSkills(ctx context.Context, execID string, handles []string) error {
	if execID == "" {
		return nil
	}
	var raw string
	err := r.db.QueryRowContext(ctx,
		`SELECT output_json FROM lifecycle_executions WHERE id = ?`, execID,
	).Scan(&raw)
	if err != nil {
		// Row gone or DB error — silently skip rather than surfacing to user.
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		probe = map[string]any{}
	}
	// Convert []string → []any for JSON round-trip consistency.
	skillsAny := make([]any, len(handles))
	for i, h := range handles {
		skillsAny[i] = h
	}
	probe["skills"] = skillsAny
	patched, err := json.Marshal(probe)
	if err != nil {
		return fmt.Errorf("patching execution output_json: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE lifecycle_executions SET output_json = ? WHERE id = ?`,
		string(patched), execID,
	)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
