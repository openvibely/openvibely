package repository

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
)

type BacklogRepo struct {
	db *sql.DB
}

func NewBacklogRepo(db *sql.DB) *BacklogRepo {
	return &BacklogRepo{db: db}
}

// --- Suggestions ---

func (r *BacklogRepo) CreateSuggestion(ctx context.Context, s *models.BacklogSuggestion) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO backlog_suggestions (project_id, type, status, title, description, task_id, suggested_priority, suggested_subtasks, reasoning, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		s.ProjectID, s.Type, s.Status, s.Title, s.Description, s.TaskID, s.SuggestedPriority, s.SuggestedSubtasks, s.Reasoning, s.Confidence,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

func (r *BacklogRepo) GetSuggestion(ctx context.Context, id string) (*models.BacklogSuggestion, error) {
	var s models.BacklogSuggestion
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, type, status, title, description, task_id, suggested_priority, suggested_subtasks, reasoning, confidence, created_at, updated_at, applied_at
		FROM backlog_suggestions WHERE id = ?`, id,
	).Scan(&s.ID, &s.ProjectID, &s.Type, &s.Status, &s.Title, &s.Description, &s.TaskID, &s.SuggestedPriority, &s.SuggestedSubtasks, &s.Reasoning, &s.Confidence, &s.CreatedAt, &s.UpdatedAt, &s.AppliedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *BacklogRepo) ListByProject(ctx context.Context, projectID string, limit int) ([]models.BacklogSuggestion, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, type, status, title, description, task_id, suggested_priority, suggested_subtasks, reasoning, confidence, created_at, updated_at, applied_at
		FROM backlog_suggestions WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSuggestions(rows)
}

func (r *BacklogRepo) ListByStatus(ctx context.Context, projectID string, status models.BacklogSuggestionStatus, limit int) ([]models.BacklogSuggestion, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, type, status, title, description, task_id, suggested_priority, suggested_subtasks, reasoning, confidence, created_at, updated_at, applied_at
		FROM backlog_suggestions WHERE project_id = ? AND status = ?
		ORDER BY confidence DESC, created_at DESC LIMIT ?`, projectID, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSuggestions(rows)
}

func (r *BacklogRepo) ListByType(ctx context.Context, projectID string, suggestionType models.BacklogSuggestionType, limit int) ([]models.BacklogSuggestion, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, type, status, title, description, task_id, suggested_priority, suggested_subtasks, reasoning, confidence, created_at, updated_at, applied_at
		FROM backlog_suggestions WHERE project_id = ? AND type = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, suggestionType, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSuggestions(rows)
}

func (r *BacklogRepo) UpdateSuggestionStatus(ctx context.Context, id string, status models.BacklogSuggestionStatus) error {
	var query string
	if status == models.SuggestionApplied {
		query = `UPDATE backlog_suggestions SET status = ?, applied_at = datetime('now'), updated_at = datetime('now') WHERE id = ?`
	} else {
		query = `UPDATE backlog_suggestions SET status = ?, updated_at = datetime('now') WHERE id = ?`
	}
	_, err := r.db.ExecContext(ctx, query, status, id)
	return err
}

func (r *BacklogRepo) DeleteSuggestion(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM backlog_suggestions WHERE id = ?`, id)
	return err
}

func (r *BacklogRepo) CountByStatus(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM backlog_suggestions
		WHERE project_id = ? GROUP BY status`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result[status] = count
	}
	return result, rows.Err()
}

func (r *BacklogRepo) CountByType(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT type, COUNT(*) FROM backlog_suggestions
		WHERE project_id = ? AND status IN ('pending', 'approved')
		GROUP BY type`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int)
	for rows.Next() {
		var typ string
		var count int
		if err := rows.Scan(&typ, &count); err != nil {
			return nil, err
		}
		result[typ] = count
	}
	return result, rows.Err()
}

func (r *BacklogRepo) AvgConfidence(ctx context.Context, projectID string) (float64, error) {
	var avg sql.NullFloat64
	err := r.db.QueryRowContext(ctx, `
		SELECT AVG(confidence) FROM backlog_suggestions WHERE project_id = ?`, projectID,
	).Scan(&avg)
	if err != nil {
		return 0, err
	}
	if !avg.Valid {
		return 0, nil
	}
	return avg.Float64, nil
}

// ExistingSuggestion checks if a similar suggestion already exists (by task_id + type, excluding rejected/expired)
func (r *BacklogRepo) ExistingSuggestion(ctx context.Context, projectID string, taskID string, suggestionType models.BacklogSuggestionType) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM backlog_suggestions
		WHERE project_id = ? AND task_id = ? AND type = ? AND status NOT IN ('rejected', 'expired', 'applied')`,
		projectID, taskID, suggestionType,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// --- Health Snapshots ---

func (r *BacklogRepo) CreateHealthSnapshot(ctx context.Context, h *models.BacklogHealthSnapshot) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO backlog_health_snapshots (project_id, total_tasks, avg_age_days, stale_count, high_priority_count, completion_velocity, bottleneck_tags, health_score, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		h.ProjectID, h.TotalTasks, h.AvgAgeDays, h.StaleCount, h.HighPriorityCount, h.CompletionVelocity, h.BottleneckTags, h.HealthScore, h.Details,
	).Scan(&h.ID, &h.CreatedAt)
}

func (r *BacklogRepo) GetLatestHealth(ctx context.Context, projectID string) (*models.BacklogHealthSnapshot, error) {
	var h models.BacklogHealthSnapshot
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, total_tasks, avg_age_days, stale_count, high_priority_count, completion_velocity, bottleneck_tags, health_score, details, created_at
		FROM backlog_health_snapshots WHERE project_id = ?
		ORDER BY created_at DESC LIMIT 1`, projectID,
	).Scan(&h.ID, &h.ProjectID, &h.TotalTasks, &h.AvgAgeDays, &h.StaleCount, &h.HighPriorityCount, &h.CompletionVelocity, &h.BottleneckTags, &h.HealthScore, &h.Details, &h.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (r *BacklogRepo) ListHealthSnapshots(ctx context.Context, projectID string, limit int) ([]models.BacklogHealthSnapshot, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, total_tasks, avg_age_days, stale_count, high_priority_count, completion_velocity, bottleneck_tags, health_score, details, created_at
		FROM backlog_health_snapshots WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []models.BacklogHealthSnapshot
	for rows.Next() {
		var h models.BacklogHealthSnapshot
		if err := rows.Scan(&h.ID, &h.ProjectID, &h.TotalTasks, &h.AvgAgeDays, &h.StaleCount, &h.HighPriorityCount, &h.CompletionVelocity, &h.BottleneckTags, &h.HealthScore, &h.Details, &h.CreatedAt); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, h)
	}
	return snapshots, rows.Err()
}

// --- Reports ---

func (r *BacklogRepo) CreateReport(ctx context.Context, report *models.BacklogAnalysisReport) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO backlog_analysis_reports (project_id, report_date, summary, suggestion_ids, stats)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		report.ProjectID, report.ReportDate, report.Summary, report.SuggestionIDs, report.Stats,
	).Scan(&report.ID, &report.CreatedAt)
}

func (r *BacklogRepo) GetLatestReport(ctx context.Context, projectID string) (*models.BacklogAnalysisReport, error) {
	var report models.BacklogAnalysisReport
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, report_date, summary, suggestion_ids, stats, created_at
		FROM backlog_analysis_reports WHERE project_id = ?
		ORDER BY created_at DESC LIMIT 1`, projectID,
	).Scan(&report.ID, &report.ProjectID, &report.ReportDate, &report.Summary, &report.SuggestionIDs, &report.Stats, &report.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &report, nil
}

func (r *BacklogRepo) ListReports(ctx context.Context, projectID string, limit int) ([]models.BacklogAnalysisReport, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, report_date, summary, suggestion_ids, stats, created_at
		FROM backlog_analysis_reports WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reports []models.BacklogAnalysisReport
	for rows.Next() {
		var report models.BacklogAnalysisReport
		if err := rows.Scan(&report.ID, &report.ProjectID, &report.ReportDate, &report.Summary, &report.SuggestionIDs, &report.Stats, &report.CreatedAt); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

// --- Aggregate Queries for Analysis ---

// CountBacklogTasks returns the number of backlog tasks for a project
func (r *BacklogRepo) CountBacklogTasks(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks WHERE project_id = ? AND category = 'backlog'`, projectID,
	).Scan(&count)
	return count, err
}

// CountStaleTasks returns the number of backlog tasks older than 30 days
func (r *BacklogRepo) CountStaleTasks(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE project_id = ? AND category = 'backlog'
		AND created_at < datetime('now', '-30 days')`, projectID,
	).Scan(&count)
	return count, err
}

// AvgBacklogAgeDays returns the average age in days of backlog tasks
func (r *BacklogRepo) AvgBacklogAgeDays(ctx context.Context, projectID string) (float64, error) {
	var avg sql.NullFloat64
	err := r.db.QueryRowContext(ctx, `
		SELECT AVG(julianday('now') - julianday(created_at)) FROM tasks
		WHERE project_id = ? AND category = 'backlog'`, projectID,
	).Scan(&avg)
	if err != nil {
		return 0, err
	}
	if !avg.Valid {
		return 0, nil
	}
	return avg.Float64, nil
}

// CountHighPriorityBacklog returns the number of high/urgent priority backlog tasks
func (r *BacklogRepo) CountHighPriorityBacklog(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE project_id = ? AND category = 'backlog' AND priority >= 3`, projectID,
	).Scan(&count)
	return count, err
}

// CompletionVelocity returns the avg tasks completed per day over the last 7 days
func (r *BacklogRepo) CompletionVelocity(ctx context.Context, projectID string) (float64, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE project_id = ? AND category = 'completed' AND status = 'completed'
		AND updated_at >= datetime('now', '-7 days')`, projectID,
	).Scan(&count)
	if err != nil {
		return 0, err
	}
	return float64(count) / 7.0, nil
}

// GetBacklogTasksForAnalysis returns backlog tasks with relevant details for AI analysis
func (r *BacklogRepo) GetBacklogTasksForAnalysis(ctx context.Context, projectID string) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, category, priority, status, prompt, agent_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, created_via, telegram_chat_id, created_at, updated_at
		FROM tasks WHERE project_id = ? AND category = 'backlog'
		ORDER BY priority DESC, created_at ASC`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category, &t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// GetRecentCompletedTasks returns recently completed tasks for context
func (r *BacklogRepo) GetRecentCompletedTasks(ctx context.Context, projectID string, limit int) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, category, priority, status, prompt, agent_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, created_via, telegram_chat_id, created_at, updated_at
		FROM tasks WHERE project_id = ? AND category = 'completed'
		ORDER BY updated_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category, &t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// --- scan helpers ---

func scanSuggestions(rows *sql.Rows) ([]models.BacklogSuggestion, error) {
	var suggestions []models.BacklogSuggestion
	for rows.Next() {
		var s models.BacklogSuggestion
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Type, &s.Status, &s.Title, &s.Description, &s.TaskID, &s.SuggestedPriority, &s.SuggestedSubtasks, &s.Reasoning, &s.Confidence, &s.CreatedAt, &s.UpdatedAt, &s.AppliedAt); err != nil {
			return nil, err
		}
		suggestions = append(suggestions, s)
	}
	return suggestions, rows.Err()
}
