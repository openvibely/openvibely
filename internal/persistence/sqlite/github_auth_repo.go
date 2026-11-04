package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// GitHubAuthRepo handles database operations for GitHub authorized users and project inbox assignees.
type GitHubAuthRepo struct {
	db *sql.DB
}

// NewGitHubAuthRepo creates a new GitHubAuthRepo.
func NewGitHubAuthRepo(db *sql.DB) *GitHubAuthRepo {
	return &GitHubAuthRepo{db: db}
}

// ListAuthorizedActors returns all system-level GitHub users on the authorization allowlist.
func (r *GitHubAuthRepo) ListAuthorizedActors(ctx context.Context) ([]models.GitHubAuthorizedActor, error) {
	return r.listAuthorizedActors(ctx,
		`SELECT id, github_user_id, github_login, display_name, permission, added_at, added_by
		 FROM github_authorized_actors
		 ORDER BY added_at ASC`)
}

// ListAuthorizedInboxAssignees returns authorized GitHub users that scheduled tasks may scan for assigned issues.
func (r *GitHubAuthRepo) ListAuthorizedInboxAssignees(ctx context.Context) ([]models.GitHubAuthorizedActor, error) {
	return r.listAuthorizedActors(ctx,
		`SELECT id, github_user_id, github_login, display_name, permission, added_at, added_by
		 FROM github_authorized_actors
		 ORDER BY lower(github_login) ASC`)
}

func (r *GitHubAuthRepo) listAuthorizedActors(ctx context.Context, query string) ([]models.GitHubAuthorizedActor, error) {
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list github authorized actors: %w", err)
	}
	defer rows.Close()

	var actors []models.GitHubAuthorizedActor
	for rows.Next() {
		actor, err := scanGitHubAuthorizedActor(rows)
		if err != nil {
			return nil, err
		}
		actors = append(actors, actor)
	}
	return actors, rows.Err()
}

// IsActorAuthorized checks whether a GitHub actor is explicitly authorized. Empty authorized lists deny by default.
func (r *GitHubAuthRepo) IsActorAuthorized(ctx context.Context, githubLogin string) (bool, error) {
	login := persistence.NormalizeGitHubLogin(githubLogin)
	if login == "" {
		return false, nil
	}
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM github_authorized_actors WHERE lower(github_login) = ?`, login).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check github actor authorization: %w", err)
	}
	return count > 0, nil
}

// UpsertAuthorizedActor creates or updates a system-level GitHub authorized actor.
func (r *GitHubAuthRepo) UpsertAuthorizedActor(ctx context.Context, actor *models.GitHubAuthorizedActor) error {
	if actor == nil {
		return fmt.Errorf("github authorized actor is required")
	}
	actor.GitHubLogin = persistence.NormalizeGitHubLogin(actor.GitHubLogin)
	if actor.GitHubLogin == "" {
		return fmt.Errorf("github login is required")
	}
	actor.Permission = strings.TrimSpace(actor.Permission)
	if actor.Permission == "" {
		actor.Permission = "triage"
	}
	actor.DisplayName = strings.TrimSpace(actor.DisplayName)
	actor.AddedBy = strings.TrimSpace(actor.AddedBy)
	if actor.AddedBy == "" {
		actor.AddedBy = "web"
	}

	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		principal, identified := models.PrincipalFromContext(ctx)
		if identified {
			if _, err := orgActor(ctx, tx, principal.UserID); err != nil {
				return err
			}
			actor.AddedBy = principal.UserID
		}
		var githubUserID any
		if actor.GitHubUserID != nil {
			githubUserID = *actor.GitHubUserID
		}
		var existingID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM github_authorized_actors WHERE lower(github_login)=?`, actor.GitHubLogin).Scan(&existingID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == sql.ErrNoRows {
			err = tx.QueryRowContext(ctx, `INSERT INTO github_authorized_actors(github_user_id,github_login,display_name,permission,added_by) VALUES(?,?,?,?,?) RETURNING id,github_user_id,added_at`, githubUserID, actor.GitHubLogin, actor.DisplayName, actor.Permission, actor.AddedBy).Scan(&actor.ID, &githubUserID, &actor.AddedAt)
		} else {
			err = tx.QueryRowContext(ctx, `UPDATE github_authorized_actors SET github_user_id=COALESCE(?,github_user_id),display_name=CASE WHEN ?!='' THEN ? ELSE display_name END,permission=?,added_by=? WHERE id=? RETURNING id,github_user_id,added_at`, githubUserID, actor.DisplayName, actor.DisplayName, actor.Permission, actor.AddedBy, existingID).Scan(&actor.ID, &githubUserID, &actor.AddedAt)
		}
		if err != nil {
			return fmt.Errorf("upsert github authorized actor: %w", err)
		}
		actor.GitHubUserID = nullableInt64Ptr(githubUserID)
		if identified {
			return projectAudit(ctx, tx, "default", principal.UserID, "github.reviewer_saved", actor.ID, `{}`)
		}
		return nil
	})
}

// DeleteAuthorizedActor removes installation-wide reviewer trust atomically.
func (r *GitHubAuthRepo) DeleteAuthorizedActor(ctx context.Context, id string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		principal, identified := models.PrincipalFromContext(ctx)
		if identified {
			if _, err := orgActor(ctx, tx, principal.UserID); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM github_authorized_actors WHERE id=?`, strings.TrimSpace(id))
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("github authorized actor not found")
		}
		if identified {
			return projectAudit(ctx, tx, "default", principal.UserID, "github.reviewer_removed", strings.TrimSpace(id), `{}`)
		}
		return nil
	})
}

// UpsertProjectInbox configures the authorized GitHub inbox assignee whose assigned issues a project's scheduled tasks inspect.
func (r *GitHubAuthRepo) UpsertProjectInbox(ctx context.Context, inbox *models.GitHubProjectInbox) error {
	if inbox == nil {
		return fmt.Errorf("github project inbox is required")
	}
	inbox.ProjectID = strings.TrimSpace(inbox.ProjectID)
	inbox.GitHubLogin = persistence.NormalizeGitHubLogin(inbox.GitHubLogin)
	if inbox.ProjectID == "" {
		return fmt.Errorf("project id is required")
	}
	if inbox.GitHubLogin == "" {
		return fmt.Errorf("github login is required")
	}
	var githubUserID any
	if inbox.GitHubUserID != nil {
		githubUserID = *inbox.GitHubUserID
	}
	var agentID any
	if inbox.AgentID != nil && strings.TrimSpace(*inbox.AgentID) != "" {
		trimmed := strings.TrimSpace(*inbox.AgentID)
		agentID = trimmed
		inbox.AgentID = &trimmed
	}

	var enabled int
	if inbox.Enabled {
		enabled = 1
	}
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actor, identified := models.PrincipalFromContext(ctx)
		if identified {
			role, err := projectRole(ctx, tx, actor.UserID, inbox.ProjectID)
			if err != nil {
				return err
			}
			if role != "maintainer" && role != "owner" {
				return persistence.ErrProjectDenied
			}
		}
		err := tx.QueryRowContext(ctx,
			`INSERT INTO github_project_inboxes (project_id, github_user_id, github_login, agent_id, enabled)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
			github_user_id = excluded.github_user_id,
			github_login = excluded.github_login,
			agent_id = excluded.agent_id,
			enabled = excluded.enabled,
			updated_at = datetime('now')
		 RETURNING github_user_id, created_at, updated_at`,
			inbox.ProjectID, githubUserID, inbox.GitHubLogin, agentID, enabled).
			Scan(&githubUserID, &inbox.CreatedAt, &inbox.UpdatedAt)
		if err != nil {
			return err
		}
		if identified {
			return projectAudit(ctx, tx, inbox.ProjectID, actor.UserID, "github.inbox_updated", inbox.ProjectID, `{}`)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("upsert github project inbox: %w", err)
	}

	inbox.GitHubUserID = nullableInt64Ptr(githubUserID)
	return nil
}

// GetProjectInbox returns a project's GitHub inbox configuration, including disabled rows.
func (r *GitHubAuthRepo) GetProjectInbox(ctx context.Context, projectID string) (*models.GitHubProjectInbox, error) {
	return r.getProjectInbox(ctx,
		`SELECT project_id, github_user_id, github_login, agent_id, enabled, created_at, updated_at
		 FROM github_project_inboxes WHERE project_id = ?`, strings.TrimSpace(projectID))
}

// GetEnabledProjectInbox returns a project's enabled GitHub inbox configuration.
func (r *GitHubAuthRepo) GetEnabledProjectInbox(ctx context.Context, projectID string) (*models.GitHubProjectInbox, error) {
	return r.getProjectInbox(ctx,
		`SELECT project_id, github_user_id, github_login, agent_id, enabled, created_at, updated_at
		 FROM github_project_inboxes WHERE project_id = ? AND enabled = 1`, strings.TrimSpace(projectID))
}

func (r *GitHubAuthRepo) getProjectInbox(ctx context.Context, query, projectID string) (*models.GitHubProjectInbox, error) {
	var inbox models.GitHubProjectInbox
	var githubUserID any
	var agentID sql.NullString
	var enabled int
	err := r.db.QueryRowContext(ctx, query, projectID).Scan(
		&inbox.ProjectID, &githubUserID, &inbox.GitHubLogin, &agentID, &enabled, &inbox.CreatedAt, &inbox.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get github project inbox: %w", err)
	}
	inbox.GitHubUserID = nullableInt64Ptr(githubUserID)
	if agentID.Valid {
		inbox.AgentID = &agentID.String
	}
	inbox.Enabled = enabled != 0
	return &inbox, nil
}

func scanGitHubAuthorizedActor(scanner interface{ Scan(dest ...any) error }) (models.GitHubAuthorizedActor, error) {
	var actor models.GitHubAuthorizedActor
	var githubUserID any
	if err := scanner.Scan(&actor.ID, &githubUserID, &actor.GitHubLogin, &actor.DisplayName, &actor.Permission, &actor.AddedAt, &actor.AddedBy); err != nil {
		return actor, fmt.Errorf("scan github authorized actor: %w", err)
	}
	actor.GitHubUserID = nullableInt64Ptr(githubUserID)
	return actor, nil
}

func nullableInt64Ptr(value any) *int64 {
	switch v := value.(type) {
	case nil:
		return nil
	case int64:
		return &v
	case int:
		converted := int64(v)
		return &converted
	case []byte:
		if len(v) == 0 {
			return nil
		}
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
	}
	return nil
}
