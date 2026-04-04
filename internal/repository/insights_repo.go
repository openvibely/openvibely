package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type InsightsRepo struct {
	db *sql.DB
}

func NewInsightsRepo(db *sql.DB) *InsightsRepo {
	return &InsightsRepo{db: db}
}

// --- Insights ---

func (r *InsightsRepo) CreateInsight(ctx context.Context, i *models.Insight) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO insights (project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		i.ProjectID, i.Type, i.Severity, i.Status, i.Title, i.Description, i.Evidence, i.Suggestion, i.Impact, i.TaskID, i.Confidence,
	).Scan(&i.ID, &i.CreatedAt, &i.UpdatedAt)
}

func (r *InsightsRepo) GetInsight(ctx context.Context, id string) (*models.Insight, error) {
	var i models.Insight
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence, created_at, updated_at, resolved_at
		FROM insights WHERE id = ?`, id,
	).Scan(&i.ID, &i.ProjectID, &i.Type, &i.Severity, &i.Status, &i.Title, &i.Description, &i.Evidence, &i.Suggestion, &i.Impact, &i.TaskID, &i.Confidence, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *InsightsRepo) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Insight, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence, created_at, updated_at, resolved_at
		FROM insights WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInsights(rows)
}

func (r *InsightsRepo) ListByStatus(ctx context.Context, projectID string, status models.InsightStatus, limit int) ([]models.Insight, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence, created_at, updated_at, resolved_at
		FROM insights WHERE project_id = ? AND status = ?
		ORDER BY
			CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 WHEN 'info' THEN 4 END,
			confidence DESC
		LIMIT ?`, projectID, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInsights(rows)
}

func (r *InsightsRepo) ListByType(ctx context.Context, projectID string, insightType models.InsightType, limit int) ([]models.Insight, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence, created_at, updated_at, resolved_at
		FROM insights WHERE project_id = ? AND type = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, insightType, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInsights(rows)
}

func (r *InsightsRepo) UpdateStatus(ctx context.Context, id string, status models.InsightStatus) error {
	query := `UPDATE insights SET status = ?, updated_at = datetime('now') WHERE id = ?`
	if status == models.InsightStatusResolved {
		query = `UPDATE insights SET status = ?, updated_at = datetime('now'), resolved_at = datetime('now') WHERE id = ?`
	}
	_, err := r.db.ExecContext(ctx, query, status, id)
	return err
}

func (r *InsightsRepo) LinkTask(ctx context.Context, insightID, taskID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE insights SET task_id = ?, status = 'accepted', updated_at = datetime('now') WHERE id = ?`, taskID, insightID)
	return err
}

func (r *InsightsRepo) DeleteInsight(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM insights WHERE id = ?`, id)
	return err
}

func (r *InsightsRepo) CountByStatus(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM insights WHERE project_id = ? GROUP BY status`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (r *InsightsRepo) CountByType(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT type, COUNT(*) FROM insights WHERE project_id = ? GROUP BY type`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var t string
		var count int
		if err := rows.Scan(&t, &count); err != nil {
			return nil, err
		}
		counts[t] = count
	}
	return counts, rows.Err()
}

func (r *InsightsRepo) AvgConfidence(ctx context.Context, projectID string) (float64, error) {
	var avg sql.NullFloat64
	err := r.db.QueryRowContext(ctx, `SELECT AVG(confidence) FROM insights WHERE project_id = ?`, projectID).Scan(&avg)
	if err != nil {
		return 0, err
	}
	if !avg.Valid {
		return 0, nil
	}
	return avg.Float64, nil
}

// ExistingInsight checks for duplicate insights by title and type
func (r *InsightsRepo) ExistingInsight(ctx context.Context, projectID string, title string, insightType models.InsightType) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM insights
		WHERE project_id = ? AND title = ? AND type = ? AND status NOT IN ('rejected', 'resolved')`,
		projectID, title, insightType,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// --- Insight Reports ---

func (r *InsightsRepo) CreateReport(ctx context.Context, rpt *models.InsightReport) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO insight_reports (project_id, report_date, summary, insight_ids, stats, analysis_log)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		rpt.ProjectID, rpt.ReportDate, rpt.Summary, rpt.InsightIDs, rpt.Stats, rpt.AnalysisLog,
	).Scan(&rpt.ID, &rpt.CreatedAt)
}

func (r *InsightsRepo) GetLatestReport(ctx context.Context, projectID string) (*models.InsightReport, error) {
	var rpt models.InsightReport
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, report_date, summary, insight_ids, stats, analysis_log, created_at
		FROM insight_reports WHERE project_id = ?
		ORDER BY created_at DESC LIMIT 1`, projectID,
	).Scan(&rpt.ID, &rpt.ProjectID, &rpt.ReportDate, &rpt.Summary, &rpt.InsightIDs, &rpt.Stats, &rpt.AnalysisLog, &rpt.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rpt, nil
}

func (r *InsightsRepo) ListReports(ctx context.Context, projectID string, limit int) ([]models.InsightReport, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, report_date, summary, insight_ids, stats, analysis_log, created_at
		FROM insight_reports WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reports []models.InsightReport
	for rows.Next() {
		var rpt models.InsightReport
		if err := rows.Scan(&rpt.ID, &rpt.ProjectID, &rpt.ReportDate, &rpt.Summary, &rpt.InsightIDs, &rpt.Stats, &rpt.AnalysisLog, &rpt.CreatedAt); err != nil {
			return nil, err
		}
		reports = append(reports, rpt)
	}
	return reports, rows.Err()
}

// --- Knowledge Entries ---

func (r *InsightsRepo) CreateKnowledge(ctx context.Context, k *models.KnowledgeEntry) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO knowledge_entries (project_id, topic, content, source, source_ref, tags)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		k.ProjectID, k.Topic, k.Content, k.Source, k.SourceRef, k.Tags,
	).Scan(&k.ID, &k.CreatedAt, &k.UpdatedAt)
}

func (r *InsightsRepo) GetKnowledge(ctx context.Context, id string) (*models.KnowledgeEntry, error) {
	var k models.KnowledgeEntry
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, topic, content, source, source_ref, tags, created_at, updated_at
		FROM knowledge_entries WHERE id = ?`, id,
	).Scan(&k.ID, &k.ProjectID, &k.Topic, &k.Content, &k.Source, &k.SourceRef, &k.Tags, &k.CreatedAt, &k.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func (r *InsightsRepo) ListKnowledge(ctx context.Context, projectID string, limit int) ([]models.KnowledgeEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, topic, content, source, source_ref, tags, created_at, updated_at
		FROM knowledge_entries WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []models.KnowledgeEntry
	for rows.Next() {
		var k models.KnowledgeEntry
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Topic, &k.Content, &k.Source, &k.SourceRef, &k.Tags, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, k)
	}
	return entries, rows.Err()
}

func (r *InsightsRepo) SearchKnowledge(ctx context.Context, projectID, query string, limit int) ([]models.KnowledgeEntry, error) {
	pattern := "%" + query + "%"
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, topic, content, source, source_ref, tags, created_at, updated_at
		FROM knowledge_entries WHERE project_id = ? AND (topic LIKE ? OR content LIKE ? OR tags LIKE ?)
		ORDER BY created_at DESC LIMIT ?`, projectID, pattern, pattern, pattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []models.KnowledgeEntry
	for rows.Next() {
		var k models.KnowledgeEntry
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Topic, &k.Content, &k.Source, &k.SourceRef, &k.Tags, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, k)
	}
	return entries, rows.Err()
}

func (r *InsightsRepo) DeleteKnowledge(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM knowledge_entries WHERE id = ?`, id)
	return err
}

// --- Aggregate queries for analysis ---

func (r *InsightsRepo) GetFailedTaskPatterns(ctx context.Context, projectID string, minFailures int) ([]struct {
	Title      string
	FailCount  int
	LastError  string
}, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH latest_errors AS (
			SELECT e.task_id, e.error_message,
				ROW_NUMBER() OVER (PARTITION BY e.task_id ORDER BY e.started_at DESC, e.rowid DESC) as rn
			FROM executions e
			WHERE e.status = 'failed'
		)
		SELECT t.title, COUNT(*) as fail_count, COALESCE(le.error_message, '') as last_error
		FROM tasks t
		JOIN executions e ON e.task_id = t.id
		LEFT JOIN latest_errors le ON le.task_id = t.id AND le.rn = 1
		WHERE t.project_id = ? AND e.status = 'failed'
		GROUP BY t.id
		HAVING fail_count >= ?
		ORDER BY fail_count DESC`, projectID, minFailures,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []struct {
		Title     string
		FailCount int
		LastError string
	}
	for rows.Next() {
		var r struct {
			Title     string
			FailCount int
			LastError string
		}
		if err := rows.Scan(&r.Title, &r.FailCount, &r.LastError); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (r *InsightsRepo) GetCompletedBugFixes(ctx context.Context, projectID string, limit int) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, prompt, category, status, priority, tag, COALESCE(agent_id, ''), display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, created_via, telegram_chat_id, created_at, updated_at
		FROM tasks
		WHERE project_id = ? AND tag = 'bug' AND status = 'completed'
		ORDER BY updated_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		var agentID string
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Category, &t.Status, &t.Priority, &t.Tag, &agentID, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if agentID != "" {
			t.AgentID = &agentID
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (r *InsightsRepo) GetSlowExecutions(ctx context.Context, projectID string, minDurationSecs int, limit int) ([]struct {
	TaskTitle string
	Duration  int
	AgentName string
}, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH exec_durations AS (
			SELECT e.task_id, e.agent_config_id,
				CAST((julianday(e.completed_at) - julianday(e.started_at)) * 86400 AS INTEGER) as duration_secs
			FROM executions e
			JOIN tasks t ON t.id = e.task_id
			WHERE t.project_id = ? AND e.status = 'completed' AND e.completed_at IS NOT NULL
		)
		SELECT t.title, ed.duration_secs, COALESCE(a.name, 'unknown') as agent_name
		FROM exec_durations ed
		JOIN tasks t ON t.id = ed.task_id
		LEFT JOIN agent_configs a ON a.id = ed.agent_config_id
		WHERE ed.duration_secs > ?
		ORDER BY ed.duration_secs DESC LIMIT ?`, projectID, minDurationSecs, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []struct {
		TaskTitle string
		Duration  int
		AgentName string
	}
	for rows.Next() {
		var r struct {
			TaskTitle string
			Duration  int
			AgentName string
		}
		if err := rows.Scan(&r.TaskTitle, &r.Duration, &r.AgentName); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// --- Health Checks ---

func (r *InsightsRepo) CreateHealthCheck(ctx context.Context, hc *models.HealthCheck) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO health_checks (project_id, grade, strengths, improvements, assessment, how_to_improve, tasks_total, tasks_completed, tasks_failed, tasks_pending, backlog_size, avg_completion_pct)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		hc.ProjectID, hc.Grade, hc.Strengths, hc.Improvements, hc.Assessment, hc.HowToImprove,
		hc.TasksTotal, hc.TasksCompleted, hc.TasksFailed, hc.TasksPending, hc.BacklogSize, hc.AvgCompletionPct,
	).Scan(&hc.ID, &hc.CreatedAt)
}

func (r *InsightsRepo) GetLatestHealthCheck(ctx context.Context, projectID string) (*models.HealthCheck, error) {
	var hc models.HealthCheck
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, grade, strengths, improvements, assessment, how_to_improve,
			tasks_total, tasks_completed, tasks_failed, tasks_pending, backlog_size, avg_completion_pct, created_at
		FROM health_checks WHERE project_id = ?
		ORDER BY created_at DESC LIMIT 1`, projectID,
	).Scan(&hc.ID, &hc.ProjectID, &hc.Grade, &hc.Strengths, &hc.Improvements, &hc.Assessment, &hc.HowToImprove,
		&hc.TasksTotal, &hc.TasksCompleted, &hc.TasksFailed, &hc.TasksPending, &hc.BacklogSize, &hc.AvgCompletionPct, &hc.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &hc, nil
}

func (r *InsightsRepo) ListHealthChecks(ctx context.Context, projectID string, limit int) ([]models.HealthCheck, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, grade, strengths, improvements, assessment, how_to_improve,
			tasks_total, tasks_completed, tasks_failed, tasks_pending, backlog_size, avg_completion_pct, created_at
		FROM health_checks WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var checks []models.HealthCheck
	for rows.Next() {
		var hc models.HealthCheck
		if err := rows.Scan(&hc.ID, &hc.ProjectID, &hc.Grade, &hc.Strengths, &hc.Improvements, &hc.Assessment, &hc.HowToImprove,
			&hc.TasksTotal, &hc.TasksCompleted, &hc.TasksFailed, &hc.TasksPending, &hc.BacklogSize, &hc.AvgCompletionPct, &hc.CreatedAt); err != nil {
			return nil, err
		}
		checks = append(checks, hc)
	}
	return checks, rows.Err()
}

// GetProjectTaskStats returns task counts by status for a project
func (r *InsightsRepo) GetProjectTaskStats(ctx context.Context, projectID string) (total, completed, failed, pending, backlog int, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) as completed,
			SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed,
			SUM(CASE WHEN status = 'pending' AND category = 'active' THEN 1 ELSE 0 END) as pending,
			SUM(CASE WHEN category = 'backlog' THEN 1 ELSE 0 END) as backlog
		FROM tasks WHERE project_id = ?`, projectID,
	).Scan(&total, &completed, &failed, &pending, &backlog)
	return
}

// GetTaskPriorityDistribution returns count by priority for a project
func (r *InsightsRepo) GetTaskPriorityDistribution(ctx context.Context, projectID string) (map[int]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT priority, COUNT(*) FROM tasks WHERE project_id = ? GROUP BY priority`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dist := make(map[int]int)
	for rows.Next() {
		var p, c int
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		dist[p] = c
	}
	return dist, rows.Err()
}

// GetRecentCompletionRate returns the completion rate from recent tasks
func (r *InsightsRepo) GetRecentCompletionRate(ctx context.Context, projectID string, days int) (float64, error) {
	var rate sql.NullFloat64
	err := r.db.QueryRowContext(ctx, `
		SELECT CASE WHEN COUNT(*) > 0
			THEN CAST(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS REAL) / COUNT(*) * 100
			ELSE 0 END
		FROM tasks
		WHERE project_id = ? AND category IN ('active', 'completed')
			AND created_at >= datetime('now', ?)`,
		projectID, fmt.Sprintf("-%d days", days),
	).Scan(&rate)
	if err != nil {
		return 0, err
	}
	if !rate.Valid {
		return 0, nil
	}
	return rate.Float64, nil
}

// GetTaskActivityTrend returns how many tasks were created per day over the last N days
func (r *InsightsRepo) GetTaskActivityTrend(ctx context.Context, projectID string, days int) ([]struct {
	Date  string
	Count int
}, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT date(created_at) as d, COUNT(*) as c
		FROM tasks WHERE project_id = ? AND created_at >= datetime('now', ?)
		GROUP BY d ORDER BY d`, projectID, fmt.Sprintf("-%d days", days),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []struct {
		Date  string
		Count int
	}
	for rows.Next() {
		var r struct {
			Date  string
			Count int
		}
		if err := rows.Scan(&r.Date, &r.Count); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GetTagDistribution returns count by tag for a project
func (r *InsightsRepo) GetTagDistribution(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(tag, 'none'), COUNT(*) FROM tasks WHERE project_id = ? GROUP BY tag`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dist := make(map[string]int)
	for rows.Next() {
		var t string
		var c int
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		dist[t] = c
	}
	return dist, rows.Err()
}

// --- Idea Grades ---

func (r *InsightsRepo) CreateIdeaGrade(ctx context.Context, ig *models.IdeaGrade) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO idea_grades (project_id, grade, summary, strengths, improvements, how_to_next_grade, next_grade, tasks_evaluated, clarity_score, ambition_score, follow_through, diversity_score, strategy_score)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		ig.ProjectID, ig.Grade, ig.Summary, ig.Strengths, ig.Improvements, ig.HowToNextGrade, ig.NextGrade,
		ig.TasksEvaluated, ig.ClarityScore, ig.AmbitionScore, ig.FollowThrough, ig.DiversityScore, ig.StrategyScore,
	).Scan(&ig.ID, &ig.CreatedAt)
}

func (r *InsightsRepo) GetLatestIdeaGrade(ctx context.Context, projectID string) (*models.IdeaGrade, error) {
	var ig models.IdeaGrade
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, grade, summary, strengths, improvements, how_to_next_grade, next_grade,
			tasks_evaluated, clarity_score, ambition_score, follow_through, diversity_score, strategy_score, created_at
		FROM idea_grades WHERE project_id = ?
		ORDER BY created_at DESC LIMIT 1`, projectID,
	).Scan(&ig.ID, &ig.ProjectID, &ig.Grade, &ig.Summary, &ig.Strengths, &ig.Improvements, &ig.HowToNextGrade, &ig.NextGrade,
		&ig.TasksEvaluated, &ig.ClarityScore, &ig.AmbitionScore, &ig.FollowThrough, &ig.DiversityScore, &ig.StrategyScore, &ig.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ig, nil
}

func (r *InsightsRepo) ListIdeaGrades(ctx context.Context, projectID string, limit int) ([]models.IdeaGrade, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, grade, summary, strengths, improvements, how_to_next_grade, next_grade,
			tasks_evaluated, clarity_score, ambition_score, follow_through, diversity_score, strategy_score, created_at
		FROM idea_grades WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grades []models.IdeaGrade
	for rows.Next() {
		var ig models.IdeaGrade
		if err := rows.Scan(&ig.ID, &ig.ProjectID, &ig.Grade, &ig.Summary, &ig.Strengths, &ig.Improvements, &ig.HowToNextGrade, &ig.NextGrade,
			&ig.TasksEvaluated, &ig.ClarityScore, &ig.AmbitionScore, &ig.FollowThrough, &ig.DiversityScore, &ig.StrategyScore, &ig.CreatedAt); err != nil {
			return nil, err
		}
		grades = append(grades, ig)
	}
	return grades, rows.Err()
}

// helper to scan insight rows
func scanInsights(rows *sql.Rows) ([]models.Insight, error) {
	var items []models.Insight
	for rows.Next() {
		var i models.Insight
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.Type, &i.Severity, &i.Status, &i.Title, &i.Description, &i.Evidence, &i.Suggestion, &i.Impact, &i.TaskID, &i.Confidence, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
