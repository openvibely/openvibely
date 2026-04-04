package repository

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
)

type PatternRepo struct {
	db *sql.DB
}

func NewPatternRepo(db *sql.DB) *PatternRepo {
	return &PatternRepo{db: db}
}

// --- Prompt Patterns ---

func (r *PatternRepo) Create(ctx context.Context, p *models.PromptPattern) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO prompt_patterns (project_id, title, description, template_text, variables, category, is_builtin, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		p.ProjectID, p.Title, p.Description, p.TemplateText, p.Variables, p.Category, p.IsBuiltin, p.Tags,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

func (r *PatternRepo) GetByID(ctx context.Context, id string) (*models.PromptPattern, error) {
	var p models.PromptPattern
	var isBuiltin int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, title, description, template_text, variables, category, is_builtin, usage_count, last_used_at, tags, created_at, updated_at
		FROM prompt_patterns WHERE id = ?`, id,
	).Scan(&p.ID, &p.ProjectID, &p.Title, &p.Description, &p.TemplateText, &p.Variables, &p.Category, &isBuiltin, &p.UsageCount, &p.LastUsedAt, &p.Tags, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.IsBuiltin = isBuiltin == 1
	return &p, nil
}

func (r *PatternRepo) ListByProject(ctx context.Context, projectID string) ([]models.PromptPattern, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, description, template_text, variables, category, is_builtin, usage_count, last_used_at, tags, created_at, updated_at
		FROM prompt_patterns WHERE project_id = ?
		ORDER BY category, title`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPatterns(rows)
}

func (r *PatternRepo) ListByCategory(ctx context.Context, projectID string, category models.PatternCategory) ([]models.PromptPattern, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, description, template_text, variables, category, is_builtin, usage_count, last_used_at, tags, created_at, updated_at
		FROM prompt_patterns WHERE project_id = ? AND category = ?
		ORDER BY usage_count DESC, title`, projectID, category,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPatterns(rows)
}

func (r *PatternRepo) ListMostPopular(ctx context.Context, projectID string, limit int) ([]models.PromptPattern, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, description, template_text, variables, category, is_builtin, usage_count, last_used_at, tags, created_at, updated_at
		FROM prompt_patterns WHERE project_id = ? AND usage_count > 0
		ORDER BY usage_count DESC, last_used_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPatterns(rows)
}

func (r *PatternRepo) ListRecentlyUsed(ctx context.Context, projectID string, limit int) ([]models.PromptPattern, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, description, template_text, variables, category, is_builtin, usage_count, last_used_at, tags, created_at, updated_at
		FROM prompt_patterns WHERE project_id = ? AND last_used_at IS NOT NULL
		ORDER BY last_used_at DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPatterns(rows)
}

func (r *PatternRepo) Search(ctx context.Context, projectID, query string, limit int) ([]models.PromptPattern, error) {
	searchPattern := "%" + query + "%"
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, description, template_text, variables, category, is_builtin, usage_count, last_used_at, tags, created_at, updated_at
		FROM prompt_patterns
		WHERE project_id = ? AND (title LIKE ? OR description LIKE ? OR tags LIKE ?)
		ORDER BY usage_count DESC, title LIMIT ?`,
		projectID, searchPattern, searchPattern, searchPattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPatterns(rows)
}

func (r *PatternRepo) Update(ctx context.Context, p *models.PromptPattern) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE prompt_patterns
		SET title = ?, description = ?, template_text = ?, variables = ?, category = ?, tags = ?, updated_at = datetime('now')
		WHERE id = ?`,
		p.Title, p.Description, p.TemplateText, p.Variables, p.Category, p.Tags, p.ID,
	)
	return err
}

func (r *PatternRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM prompt_patterns WHERE id = ? AND is_builtin = 0`, id)
	return err
}

func (r *PatternRepo) IncrementUsage(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE prompt_patterns
		SET usage_count = usage_count + 1, last_used_at = datetime('now'), updated_at = datetime('now')
		WHERE id = ?`, id,
	)
	return err
}

// --- Usage History ---

func (r *PatternRepo) CreateUsageHistory(ctx context.Context, h *models.PatternUsageHistory) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO pattern_usage_history (pattern_id, task_id, variables_applied, result_status)
		VALUES (?, ?, ?, ?)
		RETURNING id, created_at`,
		h.PatternID, h.TaskID, h.VariablesApplied, h.ResultStatus,
	).Scan(&h.ID, &h.CreatedAt)
}

func (r *PatternRepo) UpdateUsageStatus(ctx context.Context, taskID, status string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE pattern_usage_history SET result_status = ? WHERE task_id = ?`, status, taskID,
	)
	return err
}

func (r *PatternRepo) GetUsageHistory(ctx context.Context, patternID string, limit int) ([]models.PatternUsageHistory, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, pattern_id, task_id, variables_applied, result_status, created_at
		FROM pattern_usage_history WHERE pattern_id = ?
		ORDER BY created_at DESC LIMIT ?`, patternID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageHistory(rows)
}

// --- Stats ---

func (r *PatternRepo) CountByCategory(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT category, COUNT(*) FROM prompt_patterns
		WHERE project_id = ? GROUP BY category`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err != nil {
			return nil, err
		}
		counts[cat] = count
	}
	return counts, rows.Err()
}

func (r *PatternRepo) GetStats(ctx context.Context, projectID string) (*models.PatternStats, error) {
	var stats models.PatternStats

	// Total patterns
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN is_builtin = 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN is_builtin = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(usage_count), 0)
		FROM prompt_patterns WHERE project_id = ?`, projectID,
	).Scan(&stats.TotalPatterns, &stats.CustomPatterns, &stats.BuiltinPatterns, &stats.TotalUsages)
	if err != nil {
		return nil, err
	}

	if stats.TotalPatterns > 0 {
		stats.AvgUsagePerPattern = float64(stats.TotalUsages) / float64(stats.TotalPatterns)
	}

	// Categories used
	categoryCounts, err := r.CountByCategory(ctx, projectID)
	if err != nil {
		return nil, err
	}
	stats.CategoriesUsed = len(categoryCounts)

	// Most used category
	maxCount := 0
	for cat, count := range categoryCounts {
		if count > maxCount {
			maxCount = count
			stats.MostUsedCategory = models.PatternCategory(cat)
		}
	}

	return &stats, nil
}

func (r *PatternRepo) ExistsByTitle(ctx context.Context, projectID, title string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM prompt_patterns WHERE project_id = ? AND title = ?)`,
		projectID, title,
	).Scan(&exists)
	return exists, err
}

// --- Helper functions ---

func scanPatterns(rows *sql.Rows) ([]models.PromptPattern, error) {
	var patterns []models.PromptPattern
	for rows.Next() {
		var p models.PromptPattern
		var isBuiltin int
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Title, &p.Description, &p.TemplateText, &p.Variables, &p.Category, &isBuiltin, &p.UsageCount, &p.LastUsedAt, &p.Tags, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.IsBuiltin = isBuiltin == 1
		patterns = append(patterns, p)
	}
	return patterns, rows.Err()
}

func scanUsageHistory(rows *sql.Rows) ([]models.PatternUsageHistory, error) {
	var history []models.PatternUsageHistory
	for rows.Next() {
		var h models.PatternUsageHistory
		if err := rows.Scan(&h.ID, &h.PatternID, &h.TaskID, &h.VariablesApplied, &h.ResultStatus, &h.CreatedAt); err != nil {
			return nil, err
		}
		history = append(history, h)
	}
	return history, rows.Err()
}
