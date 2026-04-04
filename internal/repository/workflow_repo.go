package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type WorkflowRepo struct {
	db *sql.DB
}

func NewWorkflowRepo(db *sql.DB) *WorkflowRepo {
	return &WorkflowRepo{db: db}
}

// --- Workflow Templates ---

func (r *WorkflowRepo) ListTemplates(ctx context.Context) ([]models.WorkflowTemplate, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, category, definition, is_built_in, created_at, updated_at
		FROM workflow_templates ORDER BY is_built_in DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []models.WorkflowTemplate
	for rows.Next() {
		var t models.WorkflowTemplate
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Category, &t.Definition, &t.IsBuiltIn, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

func (r *WorkflowRepo) GetTemplate(ctx context.Context, id string) (*models.WorkflowTemplate, error) {
	var t models.WorkflowTemplate
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, category, definition, is_built_in, created_at, updated_at
		FROM workflow_templates WHERE id = ?`, id).Scan(
		&t.ID, &t.Name, &t.Description, &t.Category, &t.Definition, &t.IsBuiltIn, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *WorkflowRepo) CreateTemplate(ctx context.Context, t *models.WorkflowTemplate) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO workflow_templates (name, description, category, definition, is_built_in)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		t.Name, t.Description, t.Category, t.Definition, t.IsBuiltIn,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (r *WorkflowRepo) DeleteTemplate(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM workflow_templates WHERE id = ? AND is_built_in = 0`, id)
	return err
}

// --- Workflows ---

func (r *WorkflowRepo) CreateWorkflow(ctx context.Context, w *models.Workflow) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO workflows (project_id, name, description, strategy, template_id, config)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		w.ProjectID, w.Name, w.Description, w.Strategy, w.TemplateID, w.Config,
	).Scan(&w.ID, &w.CreatedAt, &w.UpdatedAt)
}

func (r *WorkflowRepo) GetWorkflow(ctx context.Context, id string) (*models.Workflow, error) {
	var w models.Workflow
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, name, description, strategy, template_id, config, created_at, updated_at
		FROM workflows WHERE id = ?`, id).Scan(
		&w.ID, &w.ProjectID, &w.Name, &w.Description, &w.Strategy, &w.TemplateID, &w.Config, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (r *WorkflowRepo) ListWorkflows(ctx context.Context, projectID string) ([]models.Workflow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, name, description, strategy, template_id, config, created_at, updated_at
		FROM workflows WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workflows []models.Workflow
	for rows.Next() {
		var w models.Workflow
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Description, &w.Strategy, &w.TemplateID, &w.Config, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		workflows = append(workflows, w)
	}
	return workflows, rows.Err()
}

func (r *WorkflowRepo) UpdateWorkflow(ctx context.Context, w *models.Workflow) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE workflows SET name = ?, description = ?, strategy = ?, config = ?, updated_at = datetime('now')
		WHERE id = ?`,
		w.Name, w.Description, w.Strategy, w.Config, w.ID)
	return err
}

func (r *WorkflowRepo) DeleteWorkflow(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM workflows WHERE id = ?`, id)
	return err
}

// --- Workflow Steps ---

func (r *WorkflowRepo) CreateStep(ctx context.Context, s *models.WorkflowStep) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO workflow_steps (workflow_id, name, step_type, step_order, agent_id, prompt, depends_on, config)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		s.WorkflowID, s.Name, s.StepType, s.StepOrder, s.AgentID, s.Prompt, s.DependsOn, s.Config,
	).Scan(&s.ID, &s.CreatedAt)
}

func (r *WorkflowRepo) ListSteps(ctx context.Context, workflowID string) ([]models.WorkflowStep, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workflow_id, name, step_type, step_order, agent_id, prompt, depends_on, config, created_at
		FROM workflow_steps WHERE workflow_id = ? ORDER BY step_order ASC, created_at ASC`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []models.WorkflowStep
	for rows.Next() {
		var s models.WorkflowStep
		if err := rows.Scan(&s.ID, &s.WorkflowID, &s.Name, &s.StepType, &s.StepOrder, &s.AgentID, &s.Prompt, &s.DependsOn, &s.Config, &s.CreatedAt); err != nil {
			return nil, err
		}
		steps = append(steps, s)
	}
	return steps, rows.Err()
}

func (r *WorkflowRepo) GetStep(ctx context.Context, id string) (*models.WorkflowStep, error) {
	var s models.WorkflowStep
	err := r.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, name, step_type, step_order, agent_id, prompt, depends_on, config, created_at
		FROM workflow_steps WHERE id = ?`, id).Scan(
		&s.ID, &s.WorkflowID, &s.Name, &s.StepType, &s.StepOrder, &s.AgentID, &s.Prompt, &s.DependsOn, &s.Config, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *WorkflowRepo) DeleteStep(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM workflow_steps WHERE id = ?`, id)
	return err
}

// --- Workflow Executions ---

func (r *WorkflowRepo) CreateWorkflowExecution(ctx context.Context, we *models.WorkflowExecution) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO workflow_executions (workflow_id, task_id, status, current_step_id, total_cost_cents, context, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id, started_at`,
		we.WorkflowID, we.TaskID, we.Status, we.CurrentStepID, we.TotalCostCents, we.Context, we.ErrorMessage,
	).Scan(&we.ID, &we.StartedAt)
}

func (r *WorkflowRepo) GetWorkflowExecution(ctx context.Context, id string) (*models.WorkflowExecution, error) {
	var we models.WorkflowExecution
	err := r.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, task_id, status, current_step_id, total_cost_cents, context, error_message, started_at, completed_at
		FROM workflow_executions WHERE id = ?`, id).Scan(
		&we.ID, &we.WorkflowID, &we.TaskID, &we.Status, &we.CurrentStepID, &we.TotalCostCents, &we.Context, &we.ErrorMessage, &we.StartedAt, &we.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &we, nil
}

func (r *WorkflowRepo) ListWorkflowExecutions(ctx context.Context, workflowID string) ([]models.WorkflowExecution, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workflow_id, task_id, status, current_step_id, total_cost_cents, context, error_message, started_at, completed_at
		FROM workflow_executions WHERE workflow_id = ? ORDER BY started_at DESC`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []models.WorkflowExecution
	for rows.Next() {
		var we models.WorkflowExecution
		if err := rows.Scan(&we.ID, &we.WorkflowID, &we.TaskID, &we.Status, &we.CurrentStepID, &we.TotalCostCents, &we.Context, &we.ErrorMessage, &we.StartedAt, &we.CompletedAt); err != nil {
			return nil, err
		}
		executions = append(executions, we)
	}
	return executions, rows.Err()
}

func (r *WorkflowRepo) ListWorkflowExecutionsByTask(ctx context.Context, taskID string) ([]models.WorkflowExecution, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workflow_id, task_id, status, current_step_id, total_cost_cents, context, error_message, started_at, completed_at
		FROM workflow_executions WHERE task_id = ? ORDER BY started_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []models.WorkflowExecution
	for rows.Next() {
		var we models.WorkflowExecution
		if err := rows.Scan(&we.ID, &we.WorkflowID, &we.TaskID, &we.Status, &we.CurrentStepID, &we.TotalCostCents, &we.Context, &we.ErrorMessage, &we.StartedAt, &we.CompletedAt); err != nil {
			return nil, err
		}
		executions = append(executions, we)
	}
	return executions, rows.Err()
}

func (r *WorkflowRepo) UpdateWorkflowExecution(ctx context.Context, we *models.WorkflowExecution) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE workflow_executions SET status = ?, current_step_id = ?, total_cost_cents = ?, context = ?, error_message = ?, completed_at = ?
		WHERE id = ?`,
		we.Status, we.CurrentStepID, we.TotalCostCents, we.Context, we.ErrorMessage, we.CompletedAt, we.ID)
	return err
}

// ClaimWorkflowExecution atomically sets a workflow execution to running if it's pending
func (r *WorkflowRepo) ClaimWorkflowExecution(ctx context.Context, id string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE workflow_executions SET status = 'running', started_at = datetime('now')
		WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// --- Step Executions ---

func (r *WorkflowRepo) CreateStepExecution(ctx context.Context, se *models.StepExecution) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO step_executions (workflow_execution_id, step_id, agent_config_id, status, iteration, input, output, score, cost_cents, duration_ms, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, started_at`,
		se.WorkflowExecutionID, se.StepID, se.AgentConfigID, se.Status, se.Iteration, se.Input, se.Output, se.Score, se.CostCents, se.DurationMs, se.ErrorMessage,
	).Scan(&se.ID, &se.StartedAt)
}

func (r *WorkflowRepo) GetStepExecution(ctx context.Context, id string) (*models.StepExecution, error) {
	var se models.StepExecution
	err := r.db.QueryRowContext(ctx, `
		SELECT id, workflow_execution_id, step_id, agent_config_id, status, iteration, input, output, score, cost_cents, duration_ms, error_message, started_at, completed_at
		FROM step_executions WHERE id = ?`, id).Scan(
		&se.ID, &se.WorkflowExecutionID, &se.StepID, &se.AgentConfigID, &se.Status, &se.Iteration, &se.Input, &se.Output, &se.Score, &se.CostCents, &se.DurationMs, &se.ErrorMessage, &se.StartedAt, &se.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &se, nil
}

func (r *WorkflowRepo) ListStepExecutions(ctx context.Context, workflowExecutionID string) ([]models.StepExecution, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workflow_execution_id, step_id, agent_config_id, status, iteration, input, output, score, cost_cents, duration_ms, error_message, started_at, completed_at
		FROM step_executions WHERE workflow_execution_id = ? ORDER BY started_at ASC`, workflowExecutionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []models.StepExecution
	for rows.Next() {
		var se models.StepExecution
		if err := rows.Scan(&se.ID, &se.WorkflowExecutionID, &se.StepID, &se.AgentConfigID, &se.Status, &se.Iteration, &se.Input, &se.Output, &se.Score, &se.CostCents, &se.DurationMs, &se.ErrorMessage, &se.StartedAt, &se.CompletedAt); err != nil {
			return nil, err
		}
		executions = append(executions, se)
	}
	return executions, rows.Err()
}

func (r *WorkflowRepo) UpdateStepExecution(ctx context.Context, se *models.StepExecution) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE step_executions SET status = ?, output = ?, score = ?, cost_cents = ?, duration_ms = ?, error_message = ?, completed_at = ?
		WHERE id = ?`,
		se.Status, se.Output, se.Score, se.CostCents, se.DurationMs, se.ErrorMessage, se.CompletedAt, se.ID)
	return err
}

// GetLatestStepExecution gets the most recent step execution for a step in a workflow execution
func (r *WorkflowRepo) GetLatestStepExecution(ctx context.Context, workflowExecID, stepID string) (*models.StepExecution, error) {
	var se models.StepExecution
	err := r.db.QueryRowContext(ctx, `
		SELECT id, workflow_execution_id, step_id, agent_config_id, status, iteration, input, output, score, cost_cents, duration_ms, error_message, started_at, completed_at
		FROM step_executions WHERE workflow_execution_id = ? AND step_id = ?
		ORDER BY iteration DESC LIMIT 1`, workflowExecID, stepID).Scan(
		&se.ID, &se.WorkflowExecutionID, &se.StepID, &se.AgentConfigID, &se.Status, &se.Iteration, &se.Input, &se.Output, &se.Score, &se.CostCents, &se.DurationMs, &se.ErrorMessage, &se.StartedAt, &se.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &se, nil
}

// --- Vote Records ---

func (r *WorkflowRepo) CreateVoteRecord(ctx context.Context, v *models.VoteRecord) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO vote_records (step_execution_id, agent_config_id, choice, reasoning, confidence)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		v.StepExecutionID, v.AgentConfigID, v.Choice, v.Reasoning, v.Confidence,
	).Scan(&v.ID, &v.CreatedAt)
}

func (r *WorkflowRepo) ListVoteRecords(ctx context.Context, stepExecutionID string) ([]models.VoteRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, step_execution_id, agent_config_id, choice, reasoning, confidence, created_at
		FROM vote_records WHERE step_execution_id = ? ORDER BY created_at ASC`, stepExecutionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var votes []models.VoteRecord
	for rows.Next() {
		var v models.VoteRecord
		if err := rows.Scan(&v.ID, &v.StepExecutionID, &v.AgentConfigID, &v.Choice, &v.Reasoning, &v.Confidence, &v.CreatedAt); err != nil {
			return nil, err
		}
		votes = append(votes, v)
	}
	return votes, rows.Err()
}

// --- Agent Performance Metrics ---

func (r *WorkflowRepo) GetAgentMetrics(ctx context.Context, agentConfigID string) ([]models.AgentPerformanceMetric, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, agent_config_id, task_type, success_count, failure_count, avg_duration_ms, avg_cost_cents, avg_quality_score, last_updated
		FROM agent_performance_metrics WHERE agent_config_id = ? ORDER BY task_type ASC`, agentConfigID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []models.AgentPerformanceMetric
	for rows.Next() {
		var m models.AgentPerformanceMetric
		if err := rows.Scan(&m.ID, &m.AgentConfigID, &m.TaskType, &m.SuccessCount, &m.FailureCount, &m.AvgDurationMs, &m.AvgCostCents, &m.AvgQualityScore, &m.LastUpdated); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}

func (r *WorkflowRepo) GetAllMetrics(ctx context.Context) ([]models.AgentPerformanceMetric, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, agent_config_id, task_type, success_count, failure_count, avg_duration_ms, avg_cost_cents, avg_quality_score, last_updated
		FROM agent_performance_metrics ORDER BY agent_config_id, task_type ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []models.AgentPerformanceMetric
	for rows.Next() {
		var m models.AgentPerformanceMetric
		if err := rows.Scan(&m.ID, &m.AgentConfigID, &m.TaskType, &m.SuccessCount, &m.FailureCount, &m.AvgDurationMs, &m.AvgCostCents, &m.AvgQualityScore, &m.LastUpdated); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	return metrics, rows.Err()
}

// UpsertMetric updates or inserts a performance metric using incremental averaging
func (r *WorkflowRepo) UpsertMetric(ctx context.Context, agentConfigID, taskType string, success bool, durationMs int64, costCents int, qualityScore float64) error {
	successInc := 0
	failureInc := 0
	if success {
		successInc = 1
	} else {
		failureInc = 1
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_performance_metrics (agent_config_id, task_type, success_count, failure_count, avg_duration_ms, avg_cost_cents, avg_quality_score, last_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(agent_config_id, task_type) DO UPDATE SET
			success_count = success_count + ?,
			failure_count = failure_count + ?,
			avg_duration_ms = (avg_duration_ms * (success_count + failure_count) + ?) / (success_count + failure_count + 1),
			avg_cost_cents = (avg_cost_cents * (success_count + failure_count) + ?) / (success_count + failure_count + 1),
			avg_quality_score = (avg_quality_score * (success_count + failure_count) + ?) / (success_count + failure_count + 1),
			last_updated = datetime('now')`,
		agentConfigID, taskType, successInc, failureInc, durationMs, costCents, qualityScore,
		successInc, failureInc, durationMs, costCents, qualityScore)
	return err
}

// GetBestAgentForTaskType returns the agent with the best performance for a given task type
func (r *WorkflowRepo) GetBestAgentForTaskType(ctx context.Context, taskType string) (*models.AgentPerformanceMetric, error) {
	var m models.AgentPerformanceMetric
	err := r.db.QueryRowContext(ctx, `
		SELECT id, agent_config_id, task_type, success_count, failure_count, avg_duration_ms, avg_cost_cents, avg_quality_score, last_updated
		FROM agent_performance_metrics
		WHERE task_type = ? AND (success_count + failure_count) >= 3
		ORDER BY avg_quality_score DESC, (CAST(success_count AS REAL) / NULLIF(success_count + failure_count, 0)) DESC
		LIMIT 1`, taskType).Scan(
		&m.ID, &m.AgentConfigID, &m.TaskType, &m.SuccessCount, &m.FailureCount, &m.AvgDurationMs, &m.AvgCostCents, &m.AvgQualityScore, &m.LastUpdated)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetCheapestAgentForTaskType returns the cheapest agent that meets a minimum quality threshold
func (r *WorkflowRepo) GetCheapestAgentForTaskType(ctx context.Context, taskType string, minQuality float64) (*models.AgentPerformanceMetric, error) {
	var m models.AgentPerformanceMetric
	err := r.db.QueryRowContext(ctx, `
		SELECT id, agent_config_id, task_type, success_count, failure_count, avg_duration_ms, avg_cost_cents, avg_quality_score, last_updated
		FROM agent_performance_metrics
		WHERE task_type = ? AND avg_quality_score >= ? AND (success_count + failure_count) >= 3
		ORDER BY avg_cost_cents ASC
		LIMIT 1`, taskType, minQuality).Scan(
		&m.ID, &m.AgentConfigID, &m.TaskType, &m.SuccessCount, &m.FailureCount, &m.AvgDurationMs, &m.AvgCostCents, &m.AvgQualityScore, &m.LastUpdated)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// CountRunningWorkflowExecutions returns the number of currently running workflow executions
func (r *WorkflowRepo) CountRunningWorkflowExecutions(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workflow_executions we
		JOIN workflows w ON we.workflow_id = w.id
		WHERE w.project_id = ? AND we.status = 'running'`, projectID).Scan(&count)
	return count, err
}

// GetWorkflowExecutionsByStatus lists workflow executions by status
func (r *WorkflowRepo) GetWorkflowExecutionsByStatus(ctx context.Context, status string) ([]models.WorkflowExecution, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, workflow_id, task_id, status, current_step_id, total_cost_cents, context, error_message, started_at, completed_at
		FROM workflow_executions WHERE status = ? ORDER BY started_at ASC`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []models.WorkflowExecution
	for rows.Next() {
		var we models.WorkflowExecution
		if err := rows.Scan(&we.ID, &we.WorkflowID, &we.TaskID, &we.Status, &we.CurrentStepID, &we.TotalCostCents, &we.Context, &we.ErrorMessage, &we.StartedAt, &we.CompletedAt); err != nil {
			return nil, err
		}
		executions = append(executions, we)
	}
	return executions, rows.Err()
}

// CompleteWorkflowExecution marks a workflow execution as completed
func (r *WorkflowRepo) CompleteWorkflowExecution(ctx context.Context, id string, status models.WorkflowStatus, errorMsg string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE workflow_executions SET status = ?, error_message = ?, completed_at = ?
		WHERE id = ?`, status, errorMsg, now, id)
	return err
}
