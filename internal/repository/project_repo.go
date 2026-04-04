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

func (r *ProjectRepo) Create(ctx context.Context, p *models.Project) error {
	return r.db.QueryRowContext(ctx,
		`INSERT INTO projects (id, name, description, repo_path, repo_url, default_agent_config_id, max_workers)
		 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?)
		 RETURNING id, repo_path, repo_url, created_at, updated_at`,
		p.Name, p.Description, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers).
		Scan(&p.ID, &p.RepoPath, &p.RepoURL, &p.CreatedAt, &p.UpdatedAt)
}

func (r *ProjectRepo) Update(ctx context.Context, p *models.Project) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE projects SET name = ?, description = ?, repo_path = ?, repo_url = ?, default_agent_config_id = ?, max_workers = ?, updated_at = datetime('now')
		 WHERE id = ?`,
		p.Name, p.Description, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers, p.ID)
	if err != nil {
		return fmt.Errorf("updating project: %w", err)
	}
	return nil
}

func (r *ProjectRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete workflows first (references projects without ON DELETE CASCADE)
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflows WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("deleting project workflows: %w", err)
	}

	// Delete the project (all other FKs use ON DELETE CASCADE)
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
