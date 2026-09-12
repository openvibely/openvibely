package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type ProjectRepoRoot struct {
	ID       string
	RepoPath string
}

type ProjectRepoValidationProject struct {
	ID       string
	Name     string
	RepoPath string
	RepoURL  string
}

type ProjectRepo struct {
	db *sql.DB
}

const projectWorkerCapacityProjectsQuery = `SELECT id, name, max_workers
	FROM projects ORDER BY is_default DESC, name ASC`

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

const projectAPIProjectsQuery = `SELECT id, name, repo_path, created_at
	FROM projects ORDER BY is_default DESC, name ASC`

// ListAPIProjects returns the compact project projection required by
// GET /api/projects. Keep this separate from List because management, detail,
// repository configuration, and runtime callers need the complete project row.
func (r *ProjectRepo) ListAPIProjects(ctx context.Context) ([]models.ProjectAPIItem, error) {
	rows, err := r.db.QueryContext(ctx, projectAPIProjectsQuery)
	if err != nil {
		return nil, fmt.Errorf("listing API projects: %w", err)
	}
	defer rows.Close()

	var projects []models.ProjectAPIItem
	for rows.Next() {
		var p models.ProjectAPIItem
		if err := rows.Scan(&p.ID, &p.Name, &p.RepoPath, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning API project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ListWorkerCapacityProjects returns the project identity, display name, and
// configured worker limit used exclusively by Workers capacity rows. Ordering
// remains default-first and then name-ascending, matching List.
func (r *ProjectRepo) ListWorkerCapacityProjects(ctx context.Context) ([]models.ProjectWorkerCapacity, error) {
	rows, err := r.db.QueryContext(ctx, projectWorkerCapacityProjectsQuery)
	if err != nil {
		return nil, fmt.Errorf("listing project worker capacities: %w", err)
	}
	defer rows.Close()

	var projects []models.ProjectWorkerCapacity
	for rows.Next() {
		var p models.ProjectWorkerCapacity
		if err := rows.Scan(&p.ID, &p.Name, &p.MaxWorkers); err != nil {
			return nil, fmt.Errorf("scanning project worker capacity: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

const projectRepoRootsQuery = `SELECT id, repo_path FROM projects`
const projectRepoValidationProjectsQuery = `SELECT id, name, repo_path, repo_url FROM projects`

// ListRepoValidationProjects returns the compact unordered project projection
// needed by startup repository-path validation. Keep this separate from List:
// validation does not need display ordering or project settings and metadata.
func (r *ProjectRepo) ListRepoValidationProjects(ctx context.Context) ([]ProjectRepoValidationProject, error) {
	rows, err := r.db.QueryContext(ctx, projectRepoValidationProjectsQuery)
	if err != nil {
		return nil, fmt.Errorf("listing project repository validation rows: %w", err)
	}
	defer rows.Close()

	projects := make([]ProjectRepoValidationProject, 0, 64)
	for rows.Next() {
		var p ProjectRepoValidationProject
		if err := rows.Scan(&p.ID, &p.Name, &p.RepoPath, &p.RepoURL); err != nil {
			return nil, fmt.Errorf("scanning project repository validation row: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ForEachRepoRoot scans the compact unordered repository-root projection and
// invokes visit once per project without materializing a full result slice.
func (r *ProjectRepo) ForEachRepoRoot(ctx context.Context, visit func(ProjectRepoRoot)) error {
	rows, err := r.db.QueryContext(ctx, projectRepoRootsQuery)
	if err != nil {
		return fmt.Errorf("listing project repository roots: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p ProjectRepoRoot
		if err := rows.Scan(&p.ID, &p.RepoPath); err != nil {
			return fmt.Errorf("scanning project repository root: %w", err)
		}
		visit(p)
	}
	return rows.Err()
}

// ListRepoRoots returns only project identity and repository roots for
// work-directory attribution. The result is intentionally unordered.
func (r *ProjectRepo) ListRepoRoots(ctx context.Context) ([]ProjectRepoRoot, error) {
	projects := make([]ProjectRepoRoot, 0, 64)
	if err := r.ForEachRepoRoot(ctx, func(project ProjectRepoRoot) {
		projects = append(projects, project)
	}); err != nil {
		return nil, err
	}
	return projects, nil
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

const projectIdentityByIDQuery = `SELECT id, name FROM projects WHERE id = ?`

// GetIdentityByID returns only the project identity required by channel
// current-project responses. Full project callers should continue using
// GetByID so their metadata and settings remain available.
func (r *ProjectRepo) GetIdentityByID(ctx context.Context, id string) (*models.ProjectIdentity, error) {
	var project models.ProjectIdentity
	err := r.db.QueryRowContext(ctx, projectIdentityByIDQuery, id).Scan(&project.ID, &project.Name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting project identity: %w", err)
	}
	return &project, nil
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
	if err := models.ValidateProjectWorkerLimit(p.MaxWorkers, 0); err != nil {
		return err
	}
	return queryRowBoundSQLite(ctx, r.db,
		`INSERT INTO projects (id, name, description, repo_path, repo_url, default_agent_config_id, max_workers)
			 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?)
			 RETURNING id, repo_path, repo_url, created_at, updated_at`,
		p.Name, p.Description, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers).
		Scan(&p.ID, &p.RepoPath, &p.RepoURL, &p.CreatedAt, &p.UpdatedAt)
}

func (r *ProjectRepo) Update(ctx context.Context, p *models.Project) error {
	if err := models.ValidateProjectWorkerLimit(p.MaxWorkers, 0); err != nil {
		return err
	}
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
	_, _, err := r.DeleteWithCleanupManifest(ctx, id, nil)
	return err
}

// DeleteWithCleanupManifest removes a project and all project-owned relational
// state in one transaction while preserving the filesystem cleanup information
// that database cascades would otherwise discard.
func (r *ProjectRepo) DeleteWithCleanupManifest(ctx context.Context, id string, beforeDelete func(TaskDeletionManifest) error) (manifest TaskDeletionManifest, deleted bool, err error) {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return manifest, false, fmt.Errorf("beginning project deletion: %w", err)
	}
	defer cleanup()

	var isDefault bool
	if err = tx.QueryRowContext(ctx, `SELECT is_default FROM projects WHERE id = ?`, id).Scan(&isDefault); err != nil {
		if err == sql.ErrNoRows {
			return manifest, false, fmt.Errorf("project not found or is the default project")
		}
		return manifest, false, fmt.Errorf("checking project for deletion: %w", err)
	}
	if isDefault {
		return manifest, false, fmt.Errorf("project not found or is the default project")
	}

	readStrings := func(query string, destination *[]string, args ...any) error {
		rows, queryErr := tx.QueryContext(ctx, query, args...)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			if scanErr := rows.Scan(&value); scanErr != nil {
				return scanErr
			}
			*destination = append(*destination, value)
		}
		return rows.Err()
	}
	if err = readStrings(`SELECT id FROM tasks WHERE project_id = ? ORDER BY created_at, id`, &manifest.TaskIDs, id); err != nil {
		return manifest, false, fmt.Errorf("listing project tasks for deletion: %w", err)
	}
	worktreeRows, queryErr := tx.QueryContext(ctx, `
		SELECT t.id, t.worktree_path, t.worktree_branch,
		       t.worktree_branch <> '' AND NOT EXISTS (
		           SELECT 1 FROM tasks other
		           WHERE other.project_id <> t.project_id
		             AND other.worktree_branch = t.worktree_branch
		       )
		FROM tasks t
		WHERE t.project_id = ? AND t.worktree_path <> ''
		ORDER BY t.created_at, t.id`, id)
	if queryErr != nil {
		return manifest, false, fmt.Errorf("listing project task worktrees for deletion: %w", queryErr)
	}
	for worktreeRows.Next() {
		var worktree TaskWorktreeCleanup
		if scanErr := worktreeRows.Scan(&worktree.TaskID, &worktree.WorktreePath, &worktree.WorktreeBranch, &worktree.DeleteBranch); scanErr != nil {
			_ = worktreeRows.Close()
			return manifest, false, fmt.Errorf("scanning project task worktree for deletion: %w", scanErr)
		}
		manifest.TaskWorktrees = append(manifest.TaskWorktrees, worktree)
	}
	if rowsErr := worktreeRows.Err(); rowsErr != nil {
		_ = worktreeRows.Close()
		return manifest, false, fmt.Errorf("listing project task worktrees for deletion: %w", rowsErr)
	}
	if closeErr := worktreeRows.Close(); closeErr != nil {
		return manifest, false, fmt.Errorf("closing project task worktrees for deletion: %w", closeErr)
	}
	if err = readStrings(`
		SELECT DISTINCT ta.file_path
		FROM task_attachments ta
		JOIN tasks t ON t.id = ta.task_id
		WHERE t.project_id = ?
		  AND NOT EXISTS (
			SELECT 1 FROM task_attachments other
			JOIN tasks other_task ON other_task.id = other.task_id
			WHERE other.file_path = ta.file_path AND other_task.project_id <> t.project_id
		  )`, &manifest.TaskAttachmentPaths, id); err != nil {
		return manifest, false, fmt.Errorf("listing project task attachments for deletion: %w", err)
	}
	if err = readStrings(`
		SELECT DISTINCT ca.file_path
		FROM chat_attachments ca
		JOIN executions e ON e.id = ca.execution_id
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ?
		  AND NOT EXISTS (
			SELECT 1 FROM chat_attachments other
			JOIN executions other_execution ON other_execution.id = other.execution_id
			JOIN tasks other_task ON other_task.id = other_execution.task_id
			WHERE other.file_path = ca.file_path AND other_task.project_id <> t.project_id
		  )`, &manifest.ExecutionAttachmentPaths, id); err != nil {
		return manifest, false, fmt.Errorf("listing project execution attachments for deletion: %w", err)
	}
	if err = readStrings(`
		SELECT DISTINCT attachment_session_id
		FROM thread_inputs owned
		WHERE owned.project_id = ?
		  AND owned.attachment_session_id IS NOT NULL AND owned.attachment_session_id <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM thread_inputs other
			WHERE other.attachment_session_id = owned.attachment_session_id
			  AND other.project_id <> owned.project_id
		  )`, &manifest.PendingUploadSessionIDs, id); err != nil {
		return manifest, false, fmt.Errorf("listing project pending upload sessions for deletion: %w", err)
	}
	for _, sessionID := range manifest.PendingUploadSessionIDs {
		if !isTaskDeletionUploadSessionID(sessionID) {
			return manifest, false, fmt.Errorf("invalid pending attachment session for project deletion: %q", sessionID)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO retired_attachment_sessions(session_id) VALUES (?)`, sessionID); err != nil {
			return manifest, false, fmt.Errorf("retiring pending attachment session for project deletion: %w", err)
		}
	}
	if beforeDelete != nil {
		if err = beforeDelete(manifest); err != nil {
			return manifest, false, err
		}
	}

	// Mutation history stores project ownership as a historical scalar rather than
	// a foreign key, so it must participate explicitly in project deletion.
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_config_mutations WHERE project_id = ?`, id); err != nil {
		return manifest, false, fmt.Errorf("deleting project agent mutation history: %w", err)
	}

	// Project-scoped Agents use SET NULL so task deletion can preserve global
	// profiles. A project deletion owns these profiles and must remove them.
	if _, err = tx.ExecContext(ctx, `DELETE FROM agents WHERE project_id = ?`, id); err != nil {
		return manifest, false, fmt.Errorf("deleting project agents: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND is_default = 0`, id)
	if err != nil {
		return manifest, false, fmt.Errorf("deleting project: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return manifest, false, fmt.Errorf("checking rows affected: %w", err)
	}
	if rows == 0 {
		return manifest, false, fmt.Errorf("project not found or is the default project")
	}
	if err = tx.Commit(); err != nil {
		return manifest, false, fmt.Errorf("committing project deletion: %w", err)
	}
	return manifest, true, nil
}
