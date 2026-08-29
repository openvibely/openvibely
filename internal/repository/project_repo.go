package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type ProjectRepo struct {
	db *sql.DB
}

func NewProjectRepo(db *sql.DB) *ProjectRepo {
	return &ProjectRepo{db: db}
}

func (r *ProjectRepo) List(ctx context.Context) ([]models.Project, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, description, repo_path, repo_url, is_default, default_agent_config_id, max_workers, created_at, updated_at
		 FROM projects ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()

	var projects []models.Project
	for rows.Next() {
		var p models.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.RepoPath, &p.RepoURL, &p.IsDefault, &p.DefaultAgentConfigID, &p.MaxWorkers, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ListSelectorOptions returns a compact projection for the shared sidebar
// project selector and current-project fallback. It selects only id, name, and
// is_default — the fields the shared app shell renders and the fallback path
// reads — and deliberately omits description, repo_path, repo_url,
// default_agent_config_id, max_workers, created_at, and updated_at so ordinary
// full-page navigation does not copy unbounded project text it never displays.
//
// Ordering matches List (default project first, then name ascending) with an
// explicit id tie-breaker so equal project names are deterministic. The
// idx_projects_selector_order covering index lets SQLite satisfy this order
// without a temp B-tree sort or table lookup.
func (r *ProjectRepo) ListSelectorOptions(ctx context.Context) ([]models.Project, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, is_default
		 FROM projects ORDER BY is_default DESC, name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing project selector options: %w", err)
	}
	defer rows.Close()

	var projects []models.Project
	for rows.Next() {
		var p models.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.IsDefault); err != nil {
			return nil, fmt.Errorf("scanning project selector option: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (r *ProjectRepo) GetByID(ctx context.Context, id string) (*models.Project, error) {
	var p models.Project
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, description, repo_path, repo_url, is_default, default_agent_config_id, max_workers, created_at, updated_at
			 FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.RepoPath, &p.RepoURL, &p.IsDefault, &p.DefaultAgentConfigID, &p.MaxWorkers, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting project: %w", err)
	}
	return &p, nil
}

// GetDefaultAgentConfigID returns only the project default model ID used by hot
// task-thread render paths. Full project detail reads should continue using GetByID.
func (r *ProjectRepo) GetDefaultAgentConfigID(ctx context.Context, id string) (*string, error) {
	var defaultAgentConfigID *string
	err := r.db.QueryRowContext(ctx,
		`SELECT default_agent_config_id FROM projects WHERE id = ?`, id).
		Scan(&defaultAgentConfigID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting project default model id: %w", err)
	}
	return defaultAgentConfigID, nil
}

func (r *ProjectRepo) Create(ctx context.Context, p *models.Project) error {
	return queryRowBoundSQLite(ctx, r.db,
		`INSERT INTO projects (id, name, description, repo_path, repo_url, default_agent_config_id, max_workers)
			 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?)
			 RETURNING id, repo_path, repo_url, created_at, updated_at`,
		p.Name, p.Description, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers).
		Scan(&p.ID, &p.RepoPath, &p.RepoURL, &p.CreatedAt, &p.UpdatedAt)
}

func (r *ProjectRepo) Update(ctx context.Context, p *models.Project) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE projects SET name = ?, description = ?, repo_path = ?, repo_url = ?, default_agent_config_id = ?, max_workers = ?, updated_at = datetime('now')
			 WHERE id = ?`,
		p.Name, p.Description, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers, p.ID)
	if err != nil {
		return fmt.Errorf("updating project: %w", err)
	}
	return nil
}

func (r *ProjectRepo) HasTasks(ctx context.Context, id string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id = ?)`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking project tasks: %w", err)
	}
	return exists != 0, nil
}

func (r *ProjectRepo) Delete(ctx context.Context, id string) error {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer cleanup()

	// Delete the project (related live tables use ON DELETE CASCADE)
	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND is_default = 0`, id)
	if err != nil {
		return fmt.Errorf("deleting project: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("project not found or is the default project")
	}

	return tx.Commit()
}
