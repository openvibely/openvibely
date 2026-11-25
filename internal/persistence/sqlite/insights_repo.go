package sqlite

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

type InsightsRepo struct {
	db *sql.DB
}

func NewInsightsRepo(db *sql.DB) *InsightsRepo {
	return &InsightsRepo{db: db}
}

// --- Insights ---

func (r *InsightsRepo) CreateInsight(ctx context.Context, i *models.Insight) error {
	return r.authorizedInsightWrite(ctx, i.ProjectID, "insight.created", func(tx sqlExecutor) (string, error) {
		err := tx.QueryRowContext(ctx, `
		INSERT INTO insights (project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
			i.ProjectID, i.Type, i.Severity, i.Status, i.Title, i.Description, i.Evidence, i.Suggestion, i.Impact, i.TaskID, i.Confidence,
		).Scan(&i.ID, &i.CreatedAt, &i.UpdatedAt)
		return i.ID, err
	})
}

func (r *InsightsRepo) GetInsight(ctx context.Context, projectID, id string) (*models.Insight, error) {
	var i models.Insight
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence, created_at, updated_at, resolved_at
		FROM insights WHERE `+analyticsProjectPredicate(ctx, "project_id", projectID)+` AND id = ?`, projectID, id,
	).Scan(&i.ID, &i.ProjectID, &i.Type, &i.Severity, &i.Status, &i.Title, &i.Description, &i.Evidence, &i.Suggestion, &i.Impact, &i.TaskID, &i.Confidence, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *InsightsRepo) ListByType(ctx context.Context, projectID string, insightType models.InsightType, limit int) ([]models.Insight, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, type, severity, status, title, description, evidence, suggestion, impact, task_id, confidence, created_at, updated_at, resolved_at
		FROM insights WHERE `+analyticsProjectPredicate(ctx, "project_id", projectID)+` AND type = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, insightType, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInsights(rows)
}

func (r *InsightsRepo) UpdateStatus(ctx context.Context, projectID, id string, status models.InsightStatus) error {
	projectID, err := r.insightRowProject(ctx, "insights", projectID, id)
	if err != nil {
		return err
	}
	return r.authorizedInsightWrite(ctx, projectID, "insight.status_changed", func(tx sqlExecutor) (string, error) {
		query := `UPDATE insights SET status = ?, updated_at = datetime('now') WHERE project_id = ? AND id = ?`
		if status == models.InsightStatusResolved {
			query = `UPDATE insights SET status = ?, updated_at = datetime('now'), resolved_at = datetime('now') WHERE project_id = ? AND id = ?`
		}
		result, err := tx.ExecContext(ctx, query, status, projectID, id)
		if err != nil {
			return "", err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if rows == 0 {
			return "", persistence.ErrInsightNotFound
		}
		return id, nil
	})
}

func (r *InsightsRepo) DeleteInsight(ctx context.Context, projectID, id string) error {
	projectID, err := r.insightRowProject(ctx, "insights", projectID, id)
	if err != nil {
		return err
	}
	return r.authorizedInsightWrite(ctx, projectID, "insight.deleted", func(tx sqlExecutor) (string, error) {
		result, err := tx.ExecContext(ctx, `DELETE FROM insights WHERE project_id = ? AND id = ?`, projectID, id)
		if err != nil {
			return "", err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if rows == 0 {
			return "", persistence.ErrInsightNotFound
		}
		return id, nil
	})
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
	return r.authorizedInsightWrite(ctx, rpt.ProjectID, "insight.report_created", func(tx sqlExecutor) (string, error) {
		err := tx.QueryRowContext(ctx, `
		INSERT INTO insight_reports (project_id, report_date, summary, insight_ids, stats, analysis_log)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
			rpt.ProjectID, rpt.ReportDate, rpt.Summary, rpt.InsightIDs, rpt.Stats, rpt.AnalysisLog,
		).Scan(&rpt.ID, &rpt.CreatedAt)
		return rpt.ID, err
	})
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

// --- Aggregate queries for analysis ---

func (r *InsightsRepo) GetFailedTaskPatterns(ctx context.Context, projectID string, minFailures int) ([]persistence.InsightFailedTaskPattern, error) {
	rows, err := queryFailedTaskPatterns(ctx, r.db, projectID, minFailures, -1)
	if err != nil {
		return nil, err
	}

	results := make([]persistence.InsightFailedTaskPattern, 0, len(rows))
	for _, row := range rows {
		results = append(results, persistence.InsightFailedTaskPattern{
			Title:     row.TaskTitle,
			FailCount: row.FailureCount,
			LastError: row.LastError,
		})
	}
	return results, nil
}

func (r *InsightsRepo) GetCompletedBugFixes(ctx context.Context, projectID string, limit int) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, prompt, category, status, priority, tag, COALESCE(agent_id, ''), display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, auto_merge_on_goal_achieved, merge_target_branch, merge_status, created_via, telegram_chat_id, created_at, updated_at
		FROM tasks
		WHERE `+insightsTaskPredicate(ctx, "", projectID)+` AND tag = 'bug' AND status = 'completed'
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
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Category, &t.Status, &t.Priority, &t.Tag, &agentID, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.AutoMergeOnGoalAchieved, &t.MergeTargetBranch, &t.MergeStatus, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
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
			WHERE `+insightsTaskPredicate(ctx, "t.", projectID)+` AND e.status = 'completed' AND e.completed_at IS NOT NULL
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

// Analysis inputs may span projects; saved reports remain owned by their scope.
func insightsTaskPredicate(ctx context.Context, alias, projectID string) string {
	predicate := analyticsProjectPredicate(ctx, alias+"project_id", projectID)
	if models.IsAggregateProject(projectID) {
		predicate += " AND " + alias + "category NOT IN ('chat','page_chat')"
	}
	return predicate
}

// Recheck at persistence after potentially long model work. Audit and result are
// committed together, with no generated content copied into the audit stream.
func (r *InsightsRepo) authorizedInsightWrite(ctx context.Context, projectID, action string, write func(sqlExecutor) (string, error)) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actor, identified := models.PrincipalFromContext(ctx)
		if identified {
			role, err := projectRole(ctx, tx, actor.UserID, projectID)
			if err != nil {
				return err
			}
			if role != "editor" && role != "maintainer" && role != "owner" {
				return persistence.ErrProjectDenied
			}
		}
		id, err := write(tx)
		if err != nil {
			return err
		}
		if identified {
			return projectAudit(ctx, tx, projectID, actor.UserID, action, id, `{}`)
		}
		return nil
	})
}

// All-project scope names no owner; writes are authorized against the row's real project.
func (r *InsightsRepo) insightRowProject(ctx context.Context, table, projectID, id string) (string, error) {
	if !models.IsAggregateProject(projectID) {
		return projectID, nil
	}
	var owner string
	err := r.db.QueryRowContext(ctx, `SELECT project_id FROM `+table+` WHERE id = ?`, id).Scan(&owner)
	if err == sql.ErrNoRows {
		return "", persistence.ErrInsightNotFound
	}
	if err == nil && !models.InScope(ctx, owner) {
		return "", persistence.ErrInsightNotFound
	}
	return owner, err
}
