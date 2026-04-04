package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type TemplateRepo struct {
	db *sql.DB
}

func NewTemplateRepo(db *sql.DB) *TemplateRepo {
	return &TemplateRepo{db: db}
}

// Create creates a new template
func (r *TemplateRepo) Create(ctx context.Context, tmpl *models.TaskTemplate) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO task_templates (
			project_id, name, description, default_prompt, suggested_agent_id,
			category, priority, tag, tags_json, category_filter,
			is_built_in, is_favorite, created_by
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		tmpl.ProjectID, tmpl.Name, tmpl.Description, tmpl.DefaultPrompt, tmpl.SuggestedAgentID,
		tmpl.Category, tmpl.Priority, tmpl.Tag, tmpl.TagsJSON, tmpl.CategoryFilter,
		boolToInt(tmpl.IsBuiltIn), boolToInt(tmpl.IsFavorite), tmpl.CreatedBy,
	).Scan(&tmpl.ID, &tmpl.CreatedAt, &tmpl.UpdatedAt)
}

// GetByID retrieves a template by ID
func (r *TemplateRepo) GetByID(ctx context.Context, id string) (*models.TaskTemplate, error) {
	var t models.TaskTemplate
	var projectID, agentID sql.NullString
	var isBuiltIn, isFavorite int

	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, name, description, default_prompt, suggested_agent_id,
			category, priority, tag, tags_json, category_filter,
			is_built_in, is_favorite, usage_count, created_by, created_at, updated_at
		FROM task_templates WHERE id = ?`, id,
	).Scan(
		&t.ID, &projectID, &t.Name, &t.Description, &t.DefaultPrompt, &agentID,
		&t.Category, &t.Priority, &t.Tag, &t.TagsJSON, &t.CategoryFilter,
		&isBuiltIn, &isFavorite, &t.UsageCount, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting template: %w", err)
	}

	if projectID.Valid {
		t.ProjectID = &projectID.String
	}
	if agentID.Valid {
		t.SuggestedAgentID = &agentID.String
	}
	t.IsBuiltIn = isBuiltIn == 1
	t.IsFavorite = isFavorite == 1

	return &t, nil
}

// Update updates a template (built-in templates cannot be updated)
func (r *TemplateRepo) Update(ctx context.Context, tmpl *models.TaskTemplate) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE task_templates
		SET name = ?, description = ?, default_prompt = ?, suggested_agent_id = ?,
			category = ?, priority = ?, tag = ?, tags_json = ?, category_filter = ?,
			is_favorite = ?, updated_at = datetime('now')
		WHERE id = ? AND is_built_in = 0`,
		tmpl.Name, tmpl.Description, tmpl.DefaultPrompt, tmpl.SuggestedAgentID,
		tmpl.Category, tmpl.Priority, tmpl.Tag, tmpl.TagsJSON, tmpl.CategoryFilter,
		boolToInt(tmpl.IsFavorite), tmpl.ID,
	)
	return err
}

// Delete deletes a template (built-in templates cannot be deleted)
func (r *TemplateRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM task_templates WHERE id = ? AND is_built_in = 0`, id)
	return err
}

// ListByProject returns templates for a project (includes global built-in templates)
func (r *TemplateRepo) ListByProject(ctx context.Context, projectID string) ([]models.TaskTemplate, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, name, description, default_prompt, suggested_agent_id,
			category, priority, tag, tags_json, category_filter,
			is_built_in, is_favorite, usage_count, created_by, created_at, updated_at
		FROM task_templates
		WHERE project_id = ? OR project_id IS NULL
		ORDER BY is_favorite DESC, usage_count DESC, name ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	defer rows.Close()

	return r.scanTemplates(rows)
}

// ListByCategory returns templates filtered by category
func (r *TemplateRepo) ListByCategory(ctx context.Context, projectID string, category models.TaskTemplateCategory) ([]models.TaskTemplate, error) {
	var rows *sql.Rows
	var err error

	if category == models.TaskTemplateCategoryAll {
		return r.ListByProject(ctx, projectID)
	}

	rows, err = r.db.QueryContext(ctx, `
		SELECT id, project_id, name, description, default_prompt, suggested_agent_id,
			category, priority, tag, tags_json, category_filter,
			is_built_in, is_favorite, usage_count, created_by, created_at, updated_at
		FROM task_templates
		WHERE (project_id = ? OR project_id IS NULL) AND category_filter = ?
		ORDER BY is_favorite DESC, usage_count DESC, name ASC`,
		projectID, category,
	)
	if err != nil {
		return nil, fmt.Errorf("listing templates by category: %w", err)
	}
	defer rows.Close()

	return r.scanTemplates(rows)
}

// Search searches templates by name or description
func (r *TemplateRepo) Search(ctx context.Context, projectID, query string) ([]models.TaskTemplate, error) {
	searchPattern := "%" + query + "%"
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, name, description, default_prompt, suggested_agent_id,
			category, priority, tag, tags_json, category_filter,
			is_built_in, is_favorite, usage_count, created_by, created_at, updated_at
		FROM task_templates
		WHERE (project_id = ? OR project_id IS NULL)
			AND (name LIKE ? OR description LIKE ? OR default_prompt LIKE ?)
		ORDER BY is_favorite DESC, usage_count DESC, name ASC`,
		projectID, searchPattern, searchPattern, searchPattern,
	)
	if err != nil {
		return nil, fmt.Errorf("searching templates: %w", err)
	}
	defer rows.Close()

	return r.scanTemplates(rows)
}

// ListFavorites returns favorited templates
func (r *TemplateRepo) ListFavorites(ctx context.Context, projectID string) ([]models.TaskTemplate, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, name, description, default_prompt, suggested_agent_id,
			category, priority, tag, tags_json, category_filter,
			is_built_in, is_favorite, usage_count, created_by, created_at, updated_at
		FROM task_templates
		WHERE (project_id = ? OR project_id IS NULL) AND is_favorite = 1
		ORDER BY usage_count DESC, name ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing favorites: %w", err)
	}
	defer rows.Close()

	return r.scanTemplates(rows)
}

// ListRecentlyUsed returns most recently used templates
func (r *TemplateRepo) ListRecentlyUsed(ctx context.Context, projectID string, limit int) ([]models.TaskTemplate, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, name, description, default_prompt, suggested_agent_id,
			category, priority, tag, tags_json, category_filter,
			is_built_in, is_favorite, usage_count, created_by, created_at, updated_at
		FROM task_templates
		WHERE (project_id = ? OR project_id IS NULL) AND usage_count > 0
		ORDER BY updated_at DESC
		LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing recently used: %w", err)
	}
	defer rows.Close()

	return r.scanTemplates(rows)
}

// ToggleFavorite toggles the favorite status
func (r *TemplateRepo) ToggleFavorite(ctx context.Context, id string, isFavorite bool) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE task_templates
		SET is_favorite = ?, updated_at = datetime('now')
		WHERE id = ?`,
		boolToInt(isFavorite), id,
	)
	return err
}

// IncrementUsage increments the usage counter
func (r *TemplateRepo) IncrementUsage(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE task_templates
		SET usage_count = usage_count + 1, updated_at = datetime('now')
		WHERE id = ?`, id,
	)
	return err
}

// scanTemplates is a helper to scan template rows
func (r *TemplateRepo) scanTemplates(rows *sql.Rows) ([]models.TaskTemplate, error) {
	var templates []models.TaskTemplate
	for rows.Next() {
		var t models.TaskTemplate
		var projectID, agentID sql.NullString
		var isBuiltIn, isFavorite int

		if err := rows.Scan(
			&t.ID, &projectID, &t.Name, &t.Description, &t.DefaultPrompt, &agentID,
			&t.Category, &t.Priority, &t.Tag, &t.TagsJSON, &t.CategoryFilter,
			&isBuiltIn, &isFavorite, &t.UsageCount, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning template: %w", err)
		}

		if projectID.Valid {
			t.ProjectID = &projectID.String
		}
		if agentID.Valid {
			t.SuggestedAgentID = &agentID.String
		}
		t.IsBuiltIn = isBuiltIn == 1
		t.IsFavorite = isFavorite == 1

		templates = append(templates, t)
	}
	if templates == nil {
		templates = []models.TaskTemplate{}
	}
	return templates, rows.Err()
}
