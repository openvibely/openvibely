package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type ExecutionRepo struct {
	db *sql.DB
}

func NewExecutionRepo(db *sql.DB) *ExecutionRepo {
	return &ExecutionRepo{db: db}
}

func (r *ExecutionRepo) ListByTask(ctx context.Context, taskID string) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, COALESCE(agent_config_id, ''), status, prompt_sent, output, error_message,
		 tokens_used, duration_ms, is_followup, diff_output, cli_session_id, started_at, completed_at
		 FROM executions WHERE task_id = ? ORDER BY started_at DESC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing executions: %w", err)
	}
	defer rows.Close()

	var execs []models.Execution
	for rows.Next() {
		var e models.Execution
		if err := rows.Scan(&e.ID, &e.TaskID, &e.AgentConfigID, &e.Status, &e.PromptSent,
			&e.Output, &e.ErrorMessage, &e.TokensUsed, &e.DurationMs, &e.IsFollowup, &e.DiffOutput, &e.CliSessionID, &e.StartedAt, &e.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		execs = append(execs, e)
	}
	return execs, rows.Err()
}

func (r *ExecutionRepo) ListByTaskIDs(ctx context.Context, taskIDs []string) (map[string][]models.Execution, error) {
	if len(taskIDs) == 0 {
		return map[string][]models.Execution{}, nil
	}
	placeholders := make([]byte, 0, len(taskIDs)*2-1)
	args := make([]interface{}, len(taskIDs))
	for i, id := range taskIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, COALESCE(agent_config_id, ''), status, prompt_sent, output, error_message,
		 tokens_used, duration_ms, is_followup, diff_output, cli_session_id, started_at, completed_at
		 FROM executions WHERE task_id IN (`+string(placeholders)+`) ORDER BY started_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("batch listing executions: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.Execution, len(taskIDs))
	for rows.Next() {
		var e models.Execution
		if err := rows.Scan(&e.ID, &e.TaskID, &e.AgentConfigID, &e.Status, &e.PromptSent,
			&e.Output, &e.ErrorMessage, &e.TokensUsed, &e.DurationMs, &e.IsFollowup, &e.DiffOutput, &e.CliSessionID, &e.StartedAt, &e.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		result[e.TaskID] = append(result[e.TaskID], e)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT e.id, e.task_id, COALESCE(e.agent_config_id, ''), e.status, e.prompt_sent, e.output, e.error_message,
		 e.tokens_used, e.duration_ms, e.is_followup, e.diff_output, e.cli_session_id, e.started_at, e.completed_at
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE t.project_id = ?
		 ORDER BY e.started_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing project executions: %w", err)
	}
	defer rows.Close()

	var execs []models.Execution
	for rows.Next() {
		var e models.Execution
		if err := rows.Scan(&e.ID, &e.TaskID, &e.AgentConfigID, &e.Status, &e.PromptSent,
			&e.Output, &e.ErrorMessage, &e.TokensUsed, &e.DurationMs, &e.IsFollowup, &e.DiffOutput, &e.CliSessionID, &e.StartedAt, &e.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		execs = append(execs, e)
	}
	return execs, rows.Err()
}

func (r *ExecutionRepo) GetByID(ctx context.Context, id string) (*models.Execution, error) {
	var e models.Execution
	err := r.db.QueryRowContext(ctx,
		`SELECT id, task_id, COALESCE(agent_config_id, ''), status, prompt_sent, output, error_message,
		 tokens_used, duration_ms, is_followup, diff_output, cli_session_id, started_at, completed_at
		 FROM executions WHERE id = ?`, id).
		Scan(&e.ID, &e.TaskID, &e.AgentConfigID, &e.Status, &e.PromptSent,
			&e.Output, &e.ErrorMessage, &e.TokensUsed, &e.DurationMs, &e.IsFollowup, &e.DiffOutput, &e.CliSessionID, &e.StartedAt, &e.CompletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting execution: %w", err)
	}
	return &e, nil
}

func (r *ExecutionRepo) Create(ctx context.Context, e *models.Execution) error {
	isFollowup := 0
	if e.IsFollowup {
		isFollowup = 1
	}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO executions (id, task_id, agent_config_id, status, prompt_sent, is_followup)
		 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?)
		 RETURNING id, started_at`,
		e.TaskID, e.AgentConfigID, e.Status, e.PromptSent, isFollowup).
		Scan(&e.ID, &e.StartedAt)
	if err != nil {
		return fmt.Errorf("creating execution: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) UpdateOutput(ctx context.Context, id string, output string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET output = ? WHERE id = ?`, output, id)
	if err != nil {
		return fmt.Errorf("updating execution output: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) Complete(ctx context.Context, id string, status models.ExecutionStatus, output, errMsg string, tokensUsed int, durationMs int64) error {
	// When output is empty, preserve any partial output already written by the
	// streaming writer during LLM execution. Failure completion paths frequently
	// call Complete with empty output while the streamed transcript already exists
	// in the row; preserving it keeps thread continuity after failures/retries.
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET status = ?, output = CASE WHEN ? = '' THEN output ELSE ? END, error_message = ?,
		 tokens_used = ?, duration_ms = ?, completed_at = datetime('now')
		 WHERE id = ?`,
		status, output, output, errMsg, tokensUsed, durationMs, id)
	if err != nil {
		return fmt.Errorf("completing execution: %w", err)
	}
	return nil
}

// ListChatHistory returns recent chat messages (CategoryChat tasks) for a project
func (r *ExecutionRepo) ListChatHistory(ctx context.Context, projectID string, limit int) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT e.id, e.task_id, COALESCE(e.agent_config_id, ''), e.status, e.prompt_sent, e.output, e.error_message,
		 e.tokens_used, e.duration_ms, e.is_followup, e.diff_output, e.cli_session_id, e.started_at, e.completed_at
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE t.project_id = ? AND t.category = 'chat'
		 ORDER BY e.started_at ASC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing chat history: %w", err)
	}
	defer rows.Close()

	var execs []models.Execution
	for rows.Next() {
		var e models.Execution
		if err := rows.Scan(&e.ID, &e.TaskID, &e.AgentConfigID, &e.Status, &e.PromptSent,
			&e.Output, &e.ErrorMessage, &e.TokensUsed, &e.DurationMs, &e.IsFollowup, &e.DiffOutput, &e.CliSessionID, &e.StartedAt, &e.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		execs = append(execs, e)
	}
	return execs, rows.Err()
}

// ListByTaskChronological returns all executions for a task ordered chronologically (oldest first)
func (r *ExecutionRepo) ListByTaskChronological(ctx context.Context, taskID string) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, COALESCE(agent_config_id, ''), status, prompt_sent, output, error_message,
		 tokens_used, duration_ms, is_followup, diff_output, cli_session_id, started_at, completed_at
		 FROM executions WHERE task_id = ? ORDER BY started_at ASC, rowid ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing executions chronological: %w", err)
	}
	defer rows.Close()

	var execs []models.Execution
	for rows.Next() {
		var e models.Execution
		if err := rows.Scan(&e.ID, &e.TaskID, &e.AgentConfigID, &e.Status, &e.PromptSent,
			&e.Output, &e.ErrorMessage, &e.TokensUsed, &e.DurationMs, &e.IsFollowup, &e.DiffOutput, &e.CliSessionID, &e.StartedAt, &e.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		execs = append(execs, e)
	}
	return execs, rows.Err()
}

func (r *ExecutionRepo) UpdateDiffOutput(ctx context.Context, id string, diffOutput string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET diff_output = ? WHERE id = ?`, diffOutput, id)
	if err != nil {
		return fmt.Errorf("updating execution diff output: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) UpdateCliSessionID(ctx context.Context, id string, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET cli_session_id = ? WHERE id = ?`, sessionID, id)
	if err != nil {
		return fmt.Errorf("updating execution cli_session_id: %w", err)
	}
	return nil
}

// Analytics queries

// SuccessFailureRate represents success/failure rates for a time period
type SuccessFailureRate struct {
	Period       string
	TotalCount   int
	SuccessCount int
	FailureCount int
	SuccessRate  float64
}

// GetSuccessFailureRates returns success/failure rates grouped by time period
// groupBy: "day", "week", or "month"
// dateFrom/dateTo: optional date range filters (RFC3339 format)
func (r *ExecutionRepo) GetSuccessFailureRates(ctx context.Context, projectID string, groupBy string, dateFrom, dateTo string) ([]SuccessFailureRate, error) {
	var dateFormat string
	switch groupBy {
	case "day":
		dateFormat = "%Y-%m-%d"
	case "week":
		dateFormat = "%Y-W%W"
	case "month":
		dateFormat = "%Y-%m"
	default:
		dateFormat = "%Y-%m-%d"
	}

	query := `
		SELECT
			strftime(?, e.started_at) as period,
			COUNT(*) as total_count,
			SUM(CASE WHEN e.status = 'completed' THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN e.status = 'failed' THEN 1 ELSE 0 END) as failure_count
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ? AND e.status IN ('completed', 'failed')
	`
	args := []interface{}{dateFormat, projectID}

	if dateFrom != "" {
		query += ` AND e.started_at >= ?`
		args = append(args, dateFrom)
	}
	if dateTo != "" {
		query += ` AND e.started_at <= ?`
		args = append(args, dateTo)
	}

	query += ` GROUP BY period ORDER BY period ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting success/failure rates: %w", err)
	}
	defer rows.Close()

	rates := []SuccessFailureRate{}
	for rows.Next() {
		var rate SuccessFailureRate
		if err := rows.Scan(&rate.Period, &rate.TotalCount, &rate.SuccessCount, &rate.FailureCount); err != nil {
			return nil, fmt.Errorf("scanning success/failure rate: %w", err)
		}
		if rate.TotalCount > 0 {
			rate.SuccessRate = float64(rate.SuccessCount) / float64(rate.TotalCount) * 100
		}
		rates = append(rates, rate)
	}
	return rates, rows.Err()
}

// AvgExecutionTime represents average execution time
type AvgExecutionTime struct {
	ID      string
	Name    string
	AvgMs   float64
	Count   int
	MinMs   int64
	MaxMs   int64
}

// GetAvgExecutionTimeByTask returns average execution times per task
func (r *ExecutionRepo) GetAvgExecutionTimeByTask(ctx context.Context, projectID string, limit int) ([]AvgExecutionTime, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			t.id,
			t.title,
			AVG(e.duration_ms) as avg_ms,
			COUNT(*) as count,
			MIN(e.duration_ms) as min_ms,
			MAX(e.duration_ms) as max_ms
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ? AND e.status = 'completed' AND e.duration_ms > 0
		GROUP BY t.id, t.title
		ORDER BY avg_ms DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting avg execution time by task: %w", err)
	}
	defer rows.Close()

	times := []AvgExecutionTime{}
	for rows.Next() {
		var t AvgExecutionTime
		if err := rows.Scan(&t.ID, &t.Name, &t.AvgMs, &t.Count, &t.MinMs, &t.MaxMs); err != nil {
			return nil, fmt.Errorf("scanning avg execution time: %w", err)
		}
		times = append(times, t)
	}
	return times, rows.Err()
}

// GetAvgExecutionTimeByAgent returns average execution times per agent
func (r *ExecutionRepo) GetAvgExecutionTimeByAgent(ctx context.Context, projectID string) ([]AvgExecutionTime, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			ac.id,
			ac.name,
			AVG(e.duration_ms) as avg_ms,
			COUNT(*) as count,
			MIN(e.duration_ms) as min_ms,
			MAX(e.duration_ms) as max_ms
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		JOIN agent_configs ac ON ac.id = e.agent_config_id
		WHERE t.project_id = ? AND e.status = 'completed' AND e.duration_ms > 0
		GROUP BY ac.id, ac.name
		ORDER BY count DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("getting avg execution time by agent: %w", err)
	}
	defer rows.Close()

	times := []AvgExecutionTime{}
	for rows.Next() {
		var t AvgExecutionTime
		if err := rows.Scan(&t.ID, &t.Name, &t.AvgMs, &t.Count, &t.MinMs, &t.MaxMs); err != nil {
			return nil, fmt.Errorf("scanning avg execution time: %w", err)
		}
		times = append(times, t)
	}
	return times, rows.Err()
}

// ExecutionTrend represents execution frequency data
type ExecutionTrend struct {
	Hour  int
	Count int
}

// GetExecutionTrendsByHour returns execution counts by hour of day
func (r *ExecutionRepo) GetExecutionTrendsByHour(ctx context.Context, projectID string, dateFrom, dateTo string) ([]ExecutionTrend, error) {
	query := `
		SELECT
			CAST(strftime('%H', e.started_at) as INTEGER) as hour,
			COUNT(*) as count
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ?
	`
	args := []interface{}{projectID}

	if dateFrom != "" {
		query += ` AND e.started_at >= ?`
		args = append(args, dateFrom)
	}
	if dateTo != "" {
		query += ` AND e.started_at <= ?`
		args = append(args, dateTo)
	}

	query += ` GROUP BY hour ORDER BY hour ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting execution trends by hour: %w", err)
	}
	defer rows.Close()

	trends := []ExecutionTrend{}
	for rows.Next() {
		var trend ExecutionTrend
		if err := rows.Scan(&trend.Hour, &trend.Count); err != nil {
			return nil, fmt.Errorf("scanning execution trend: %w", err)
		}
		trends = append(trends, trend)
	}
	return trends, rows.Err()
}

// AgentUsage represents agent usage statistics
type AgentUsage struct {
	AgentID      string
	AgentName    string
	ProjectID    string
	ProjectName  string
	ExecutionCount int
	SuccessCount   int
	FailureCount   int
}

// GetAgentUsageByProject returns agent usage breakdown by project
func (r *ExecutionRepo) GetAgentUsageByProject(ctx context.Context, projectID string) ([]AgentUsage, error) {
	query := `
		SELECT
			ac.id as agent_id,
			ac.name as agent_name,
			p.id as project_id,
			p.name as project_name,
			COUNT(*) as execution_count,
			SUM(CASE WHEN e.status = 'completed' THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN e.status = 'failed' THEN 1 ELSE 0 END) as failure_count
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		JOIN projects p ON p.id = t.project_id
		JOIN agent_configs ac ON ac.id = e.agent_config_id
		WHERE 1=1
	`
	args := []interface{}{}

	if projectID != "" {
		query += ` AND t.project_id = ?`
		args = append(args, projectID)
	}

	query += ` GROUP BY ac.id, ac.name, p.id, p.name ORDER BY execution_count DESC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting agent usage by project: %w", err)
	}
	defer rows.Close()

	usage := []AgentUsage{}
	for rows.Next() {
		var u AgentUsage
		if err := rows.Scan(&u.AgentID, &u.AgentName, &u.ProjectID, &u.ProjectName, &u.ExecutionCount, &u.SuccessCount, &u.FailureCount); err != nil {
			return nil, fmt.Errorf("scanning agent usage: %w", err)
		}
		usage = append(usage, u)
	}
	return usage, rows.Err()
}

// TaskFrequency represents task execution frequency
type TaskFrequency struct {
	TaskID        string
	TaskTitle     string
	ExecutionCount int
	LastExecutedAt string
}

// GetMostFrequentTasks returns the most frequently executed tasks
func (r *ExecutionRepo) GetMostFrequentTasks(ctx context.Context, projectID string, limit int) ([]TaskFrequency, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			t.id,
			t.title,
			COUNT(*) as execution_count,
			MAX(e.started_at) as last_executed_at
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ?
		GROUP BY t.id, t.title
		ORDER BY execution_count DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting most frequent tasks: %w", err)
	}
	defer rows.Close()

	frequencies := []TaskFrequency{}
	for rows.Next() {
		var freq TaskFrequency
		if err := rows.Scan(&freq.TaskID, &freq.TaskTitle, &freq.ExecutionCount, &freq.LastExecutedAt); err != nil {
			return nil, fmt.Errorf("scanning task frequency: %w", err)
		}
		frequencies = append(frequencies, freq)
	}
	return frequencies, rows.Err()
}

// FailedTaskPattern represents failed task pattern
type FailedTaskPattern struct {
	TaskID       string
	TaskTitle    string
	FailureCount int
	LastError    string
	LastFailedAt string
}

// GetFailedTaskPatterns returns tasks with failure patterns
func (r *ExecutionRepo) GetFailedTaskPatterns(ctx context.Context, projectID string, limit int) ([]FailedTaskPattern, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			t.id,
			t.title,
			COUNT(*) as failure_count,
			e.error_message as last_error,
			MAX(e.started_at) as last_failed_at
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ? AND e.status = 'failed'
		GROUP BY t.id, t.title, e.error_message
		ORDER BY failure_count DESC, last_failed_at DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting failed task patterns: %w", err)
	}
	defer rows.Close()

	patterns := []FailedTaskPattern{}
	for rows.Next() {
		var pattern FailedTaskPattern
		if err := rows.Scan(&pattern.TaskID, &pattern.TaskTitle, &pattern.FailureCount, &pattern.LastError, &pattern.LastFailedAt); err != nil {
			return nil, fmt.Errorf("scanning failed task pattern: %w", err)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, rows.Err()
}
