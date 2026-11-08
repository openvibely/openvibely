package sqlite

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// reviewerHost accepts an authority, never an arbitrary URL or path. Host is part
// of identity: numeric GitHub Enterprise IDs are not globally unique.
func reviewerHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	u, err := url.Parse("https://" + host)
	if err != nil || host == "" || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return "", persistence.ErrProjectDenied
	}
	return host, nil
}

func (r *GitHubAuthRepo) BindReviewerIdentity(ctx context.Context, binding models.GitHubReviewerIdentity) error {
	actor, ok := models.PrincipalFromContext(ctx)
	if !ok || binding.ExternalUserID <= 0 || strings.TrimSpace(binding.ApplicationUserID) == "" {
		return persistence.ErrProjectDenied
	}
	host, err := reviewerHost(binding.Host)
	if err != nil {
		return err
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if _, err := orgActor(ctx, tx, actor.UserID); err != nil {
			return err
		}
		// Visibility of the selected project is required in addition to org authority.
		if role, err := projectRole(ctx, tx, actor.UserID, binding.ProjectID); err != nil {
			return err
		} else if role == "" {
			return persistence.ErrProjectDenied
		}
		role, err := projectRole(ctx, tx, binding.ApplicationUserID, binding.ProjectID)
		if err != nil {
			return err
		}
		if role != "editor" && role != "maintainer" && role != "owner" {
			return persistence.ErrProjectDenied
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM github_authorized_actors WHERE id=?)`, binding.ActorID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return persistence.ErrProjectDenied
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO github_reviewer_identities(project_id,actor_id,host,external_user_id,application_user_id,verified_by) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,actor_id) DO UPDATE SET host=excluded.host,external_user_id=excluded.external_user_id,application_user_id=excluded.application_user_id,verified_by=excluded.verified_by`, binding.ProjectID, binding.ActorID, host, binding.ExternalUserID, binding.ApplicationUserID, actor.UserID)
		if err != nil {
			return err
		}
		details, err := json.Marshal(struct {
			Host              string `json:"host"`
			ExternalUserID    int64  `json:"external_user_id"`
			ApplicationUserID string `json:"application_user_id"`
		}{host, binding.ExternalUserID, binding.ApplicationUserID})
		if err != nil {
			return err
		}
		return projectAudit(ctx, tx, binding.ProjectID, actor.UserID, "github.reviewer_bound", binding.ActorID, string(details))
	})
}

func (r *GitHubAuthRepo) ListReviewerIdentities(ctx context.Context, projectID string) ([]models.GitHubReviewerIdentity, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT project_id,actor_id,host,external_user_id,application_user_id,verified_by FROM github_reviewer_identities WHERE project_id=? ORDER BY actor_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := []models.GitHubReviewerIdentity{}
	for rows.Next() {
		var b models.GitHubReviewerIdentity
		if err := rows.Scan(&b.ProjectID, &b.ActorID, &b.Host, &b.ExternalUserID, &b.ApplicationUserID, &b.VerifiedBy); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

// ResolveReviewerIdentity uses both immutable provider identity and the current
// trust-list login. It never falls back to an installer or forwarding principal.
func (r *GitHubAuthRepo) ResolveReviewerIdentity(ctx context.Context, projectID, host string, externalID int64, login string) (string, error) {
	var userID string
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		binding, err := resolveReviewerIdentity(ctx, tx, projectID, host, externalID, login)
		if err != nil {
			return err
		}
		userID = binding.ApplicationUserID
		role, err := projectRole(ctx, tx, userID, projectID)
		if err != nil {
			return err
		}
		if role != "editor" && role != "maintainer" && role != "owner" {
			return persistence.ErrProjectDenied
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}

func resolveReviewerIdentity(ctx context.Context, tx sqlExecutor, projectID, host string, externalID int64, login string) (*models.GitHubReviewerIdentity, error) {
	host, err := reviewerHost(host)
	if err != nil || externalID <= 0 {
		return nil, persistence.ErrProjectDenied
	}
	b := &models.GitHubReviewerIdentity{ProjectID: projectID, Host: host, ExternalUserID: externalID}
	if err := tx.QueryRowContext(ctx, `SELECT b.actor_id,b.application_user_id,b.verified_by FROM github_reviewer_identities b JOIN github_authorized_actors a ON a.id=b.actor_id WHERE b.project_id=? AND b.host=? AND b.external_user_id=? AND lower(a.github_login)=? AND b.verified_by<>''`, projectID, host, externalID, persistence.NormalizeGitHubLogin(login)).Scan(&b.ActorID, &b.ApplicationUserID, &b.VerifiedBy); err != nil {
		return nil, persistence.ErrProjectDenied
	}
	return b, nil
}

// Recheck immutable snapshots against the exact live binding and current task
// permissions. Never allow a new mapping to substitute its user for a snapshot.
func requireReviewerIdentities(ctx context.Context, tx sqlExecutor, id string, execution bool) error {
	query := `SELECT p.project_id,p.actor_id,p.host,p.external_user_id,p.application_user_id,i.task_id FROM github_reviewer_input_identities p JOIN thread_inputs i ON i.id=p.input_id WHERE p.input_id=?`
	if execution {
		query = `SELECT p.project_id,p.actor_id,p.host,p.external_user_id,p.application_user_id,e.task_id FROM github_reviewer_execution_identities p JOIN executions e ON e.id=p.execution_id WHERE p.execution_id=?`
	}
	rows, err := tx.QueryContext(ctx, query, id)
	if err != nil {
		return err
	}
	type snapshot struct {
		b    models.GitHubReviewerIdentity
		task string
	}
	var snapshots []snapshot
	for rows.Next() {
		var s snapshot
		if err := rows.Scan(&s.b.ProjectID, &s.b.ActorID, &s.b.Host, &s.b.ExternalUserID, &s.b.ApplicationUserID, &s.task); err != nil {
			rows.Close()
			return err
		}
		snapshots = append(snapshots, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range snapshots {
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM github_reviewer_identities b JOIN tasks t ON t.project_id=b.project_id WHERE t.id=? AND b.project_id=? AND b.actor_id=? AND b.host=? AND b.external_user_id=? AND b.application_user_id=? AND b.verified_by<>'')`, s.task, s.b.ProjectID, s.b.ActorID, s.b.Host, s.b.ExternalUserID, s.b.ApplicationUserID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return persistence.ErrProjectDenied
		}
		if _, err := authorizeExecutionActor(ctx, tx, s.task, s.b.ApplicationUserID); err != nil {
			return err
		}
	}
	return nil
}
