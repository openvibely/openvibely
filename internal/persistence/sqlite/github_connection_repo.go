package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

var _ persistence.GitHubConnectionStore = (*GitHubConnectionRepo)(nil)

type GitHubConnectionRepo struct{ db *sql.DB }

func NewGitHubConnectionRepo(db *sql.DB) *GitHubConnectionRepo { return &GitHubConnectionRepo{db: db} }

// A missing actor is never interpreted as the owner. Auth-off callers also
// supply the built-in owner's principal explicitly.
func githubConnectionActor(ctx context.Context, db sqlExecutor) (string, error) {
	p, ok := models.PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return "", persistence.ErrProjectDenied
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u JOIN org_memberships m ON m.user_id=u.id WHERE u.id=? AND u.disabled=0 AND m.org_id=?`, p.UserID, models.DefaultOrgID).Scan(&count)
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", persistence.ErrProjectDenied
	}
	return p.UserID, nil
}

func (r *GitHubConnectionRepo) GetOwn(ctx context.Context) (*models.GitHubConnection, error) {
	// Check membership and read the secret in the same snapshot.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := githubConnectionActor(ctx, tx)
	if err != nil {
		return nil, err
	}
	c := &models.GitHubConnection{}
	err = tx.QueryRowContext(ctx, `SELECT pat,login,api_endpoint,author_name,author_email,revision FROM user_github_connections WHERE user_id=?`, id).Scan(&c.PAT, &c.Login, &c.APIEndpoint, &c.AuthorName, &c.AuthorEmail, &c.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return c, tx.Commit()
}

func (r *GitHubConnectionRepo) SaveOwn(ctx context.Context, c models.GitHubConnection, expected int64) (bool, error) {
	changed := false
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		id, err := githubConnectionActor(ctx, tx)
		if err != nil {
			return err
		}
		var res sql.Result
		if expected == 0 {
			res, err = tx.ExecContext(ctx, `INSERT INTO user_github_connections(user_id,pat,login,api_endpoint,author_name,author_email) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id) DO NOTHING`, id, c.PAT, c.Login, c.APIEndpoint, c.AuthorName, c.AuthorEmail)
		} else {
			res, err = tx.ExecContext(ctx, `UPDATE user_github_connections SET pat=?,login=?,api_endpoint=?,author_name=?,author_email=?,revision=revision+1 WHERE user_id=? AND revision=?`, c.PAT, c.Login, c.APIEndpoint, c.AuthorName, c.AuthorEmail, id, expected)
		}
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		changed = n == 1
		if changed {
			return projectAudit(ctx, tx, "", id, "github.connection.updated", id, `{}`)
		}
		return nil
	})
	return changed && err == nil, err
}

func (r *GitHubConnectionRepo) DeleteOwn(ctx context.Context, expected int64) (bool, error) {
	changed := false
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		id, err := githubConnectionActor(ctx, tx)
		if err != nil {
			return err
		}
		// Keep the revision tombstone to prevent stale forms overwriting a new token.
		res, err := tx.ExecContext(ctx, `UPDATE user_github_connections SET pat='',login='',api_endpoint='',author_name='',author_email='',revision=revision+1 WHERE user_id=? AND revision=?`, id, expected)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		changed = n == 1
		if changed {
			return projectAudit(ctx, tx, "", id, "github.connection.disconnected", id, `{}`)
		}
		return nil
	})
	return changed && err == nil, err
}
