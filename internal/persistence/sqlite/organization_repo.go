package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

type OrganizationRepo struct{ db *sql.DB }

var _ persistence.OrganizationStore = (*OrganizationRepo)(nil)

func NewOrganizationRepo(db *sql.DB) *OrganizationRepo { return &OrganizationRepo{db: db} }

func (r *OrganizationRepo) Bootstrap(ctx context.Context, username, hash string) error {
	if username == "" || hash == "" {
		return nil
	}
	_, err := execBoundSQLite(ctx, r.db, `UPDATE users SET username=?,display_name=?,password_hash=? WHERE id=? AND password_hash=''`, username, username, hash, models.BootstrapUserID)
	return err
}
func (r *OrganizationRepo) Organization(ctx context.Context) (*models.Organization, error) {
	o := &models.Organization{}
	err := r.db.QueryRowContext(ctx, `SELECT id,name FROM organizations WHERE id=?`, models.DefaultOrgID).Scan(&o.ID, &o.Name)
	return o, err
}

const orgUserSelect = `SELECT u.id,COALESCE(m.org_id,''),u.username,u.display_name,COALESCE(m.role,''),u.password_hash,u.disabled FROM users u LEFT JOIN org_memberships m ON m.user_id=u.id AND m.org_id='default' `

func scanOrgUser(row interface{ Scan(...any) error }) (*models.OrgUser, error) {
	u := &models.OrgUser{}
	err := row.Scan(&u.UserID, &u.OrgID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &u.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}
func (r *OrganizationRepo) UserByID(ctx context.Context, id string) (*models.OrgUser, error) {
	return scanOrgUser(r.db.QueryRowContext(ctx, orgUserSelect+`WHERE u.id=?`, id))
}
func (r *OrganizationRepo) UserByUsername(ctx context.Context, name string) (*models.OrgUser, error) {
	return scanOrgUser(r.db.QueryRowContext(ctx, orgUserSelect+`WHERE u.username=? COLLATE NOCASE`, name))
}
func (r *OrganizationRepo) Members(ctx context.Context) ([]models.OrgUser, error) {
	rows, err := r.db.QueryContext(ctx, orgUserSelect+`WHERE m.org_id='default' ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.OrgUser{}
	for rows.Next() {
		u, err := scanOrgUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}
func orgActor(ctx context.Context, tx sqlExecutor, id string) (string, error) {
	var role string
	err := tx.QueryRowContext(ctx, `SELECT m.role FROM org_memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id='default' AND u.id=? AND u.disabled=0`, id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", persistence.ErrOrgDenied
	}
	if err != nil {
		return "", err
	}
	if role != "owner" && role != "admin" {
		return "", persistence.ErrOrgDenied
	}
	return role, nil
}
func (r *OrganizationRepo) RenameOrganization(ctx context.Context, actor, name string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if _, err := orgActor(ctx, tx, actor); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE organizations SET name=? WHERE id='default'`, name)
		return err
	})
}
func (r *OrganizationRepo) ChangeMember(ctx context.Context, actor, id, role string, remove bool) error {
	if !remove && role != "owner" && role != "admin" && role != "member" {
		return persistence.ErrOrgDenied
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actorRole, err := orgActor(ctx, tx, actor)
		if err != nil {
			return err
		}
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM org_memberships WHERE org_id='default' AND user_id=?`, id).Scan(&old); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return persistence.ErrNotFound
			}
			return err
		}
		if actorRole != "owner" && (old != "member" || (!remove && role != "member")) {
			return persistence.ErrOrgDenied
		}
		if old == "owner" && (remove || role != "owner") {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_memberships m JOIN users u ON m.user_id=u.id WHERE m.org_id='default' AND m.role='owner' AND u.disabled=0`).Scan(&count); err != nil {
				return err
			}
			if count <= 1 {
				return persistence.ErrLastOwner
			}
		}
		if remove {
			_, err = tx.ExecContext(ctx, `DELETE FROM org_memberships WHERE org_id='default' AND user_id=?`, id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE org_memberships SET role=? WHERE org_id='default' AND user_id=?`, role, id)
		}
		if err != nil {
			return err
		}
		// Outstanding invitations must not survive removal/demotion of their issuer.
		if remove || (old != "member" && role == "member") {
			if _, err = tx.ExecContext(ctx, `UPDATE org_invitations SET revoked=1 WHERE created_by=? AND used=0`, id); err != nil {
				return err
			}
		}
		if remove {
			_, err = tx.ExecContext(ctx, `DELETE FROM user_sessions WHERE user_id=?`, id)
		}
		return err
	})
}
func (r *OrganizationRepo) CreateInvitation(ctx context.Context, actor, id, hash, role string, expires int64) error {
	if role != "admin" && role != "member" {
		return persistence.ErrOrgDenied
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actorRole, err := orgActor(ctx, tx, actor)
		if err != nil {
			return err
		}
		if role == "admin" && actorRole != "owner" {
			return persistence.ErrOrgDenied
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO org_invitations(id,org_id,token_hash,role,created_by,expires_at) VALUES(?,'default',?,?,?,?)`, id, hash, role, actor, expires)
		return err
	})
}
func (r *OrganizationRepo) Invitations(ctx context.Context) ([]models.OrgInvitation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,role,expires_at,created_by,used,revoked FROM org_invitations WHERE org_id='default' ORDER BY expires_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.OrgInvitation{}
	for rows.Next() {
		var i models.OrgInvitation
		if err := rows.Scan(&i.ID, &i.Role, &i.ExpiresAt, &i.CreatedBy, &i.Used, &i.Revoked); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (r *OrganizationRepo) RevokeInvitation(ctx context.Context, actor, id string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		role, err := orgActor(ctx, tx, actor)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE org_invitations SET revoked=1 WHERE id=? AND org_id='default' AND (?='owner' OR created_by=?)`, id, role, actor)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return persistence.ErrNotFound
		}
		return err
	})
}
func (r *OrganizationRepo) AcceptInvitation(ctx context.Context, hash string, now int64, newUser models.OrgUser, existingID string) (*models.OrgUser, error) {
	id := existingID
	if id == "" {
		id = newUser.UserID
	}
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var role, issuer string
		err := tx.QueryRowContext(ctx, `SELECT role,created_by FROM org_invitations WHERE token_hash=? AND org_id='default' AND used=0 AND revoked=0 AND expires_at>?`, hash, now).Scan(&role, &issuer)
		if errors.Is(err, sql.ErrNoRows) {
			return persistence.ErrInvitationInvalid
		}
		if err != nil {
			return err
		}
		issuerRole, err := orgActor(ctx, tx, issuer)
		if err != nil {
			return persistence.ErrInvitationInvalid
		}
		if role == "admin" && issuerRole != "owner" {
			return persistence.ErrInvitationInvalid
		}
		if existingID == "" {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username=? COLLATE NOCASE`, newUser.Username).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				return persistence.ErrAccountExists
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,display_name,password_hash) VALUES(?,?,?,?)`, id, newUser.Username, newUser.DisplayName, newUser.PasswordHash); err != nil {
				return err
			}
		} else {
			var enabled int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=? AND disabled=0`, id).Scan(&enabled); err != nil {
				return err
			}
			if enabled != 1 {
				return persistence.ErrOrgDenied
			}
		}
		// Joining twice must never change an existing member's role.
		if _, err := tx.ExecContext(ctx, `INSERT INTO org_memberships(org_id,user_id,role) VALUES('default',?,?) ON CONFLICT(org_id,user_id) DO NOTHING`, id, role); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE org_invitations SET used=1 WHERE token_hash=?`, hash)
		return err
	})
	if err != nil {
		return nil, err
	}
	return r.UserByID(ctx, id)
}
func (r *OrganizationRepo) CreateSession(ctx context.Context, hash, id string, expires int64) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO user_sessions(token_hash,user_id,expires_at) SELECT ?,u.id,? FROM users u JOIN org_memberships m ON m.user_id=u.id WHERE u.id=? AND u.disabled=0 AND m.org_id='default'`, hash, expires, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return persistence.ErrOrgDenied
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM user_sessions WHERE expires_at<=?`, time.Now().Unix())
		return err
	})
}
func (r *OrganizationRepo) SessionUser(ctx context.Context, hash string, now int64) (*models.OrgUser, error) {
	return scanOrgUser(r.db.QueryRowContext(ctx, orgUserSelect+`JOIN user_sessions s ON s.user_id=u.id WHERE s.token_hash=? AND s.expires_at>? AND u.disabled=0 AND m.org_id='default'`, hash, now))
}
func (r *OrganizationRepo) RevokeSession(ctx context.Context, hash string) error {
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM user_sessions WHERE token_hash=?`, hash)
	return err
}
func (r *OrganizationRepo) BindHostedOwner(ctx context.Context, subject string) (*models.OrgUser, error) {
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT subject FROM user_identities WHERE provider='hosted_sso' AND user_id=?`, models.BootstrapUserID).Scan(&existing)
		if err == nil {
			if existing != subject {
				return persistence.ErrOrgDenied
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO user_identities(provider,subject,user_id) VALUES('hosted_sso',?,?)`, subject, models.BootstrapUserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return r.UserByID(ctx, models.BootstrapUserID)
}
func (r *OrganizationRepo) Preferences(ctx context.Context, id string, keys []string) (map[string]string, error) {
	out := map[string]string{}
	if len(keys) == 0 {
		return out, nil
	}
	args := []any{id}
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT key,value FROM user_preferences WHERE user_id=? AND key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}
func (r *OrganizationRepo) SetPreferences(ctx context.Context, id string, values map[string]string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		for key, value := range values {
			if _, err := tx.ExecContext(ctx, `INSERT INTO user_preferences(user_id,key,value) VALUES(?,?,?) ON CONFLICT(user_id,key) DO UPDATE SET value=excluded.value`, id, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *OrganizationRepo) ComparePreferences(ctx context.Context, userID string, expected, values, guards map[string]string) (bool, error) {
	updated := false
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		for _, group := range []map[string]string{expected, guards} {
			for key, want := range group {
				var got string
				err := tx.QueryRowContext(ctx, `SELECT value FROM user_preferences WHERE user_id=? AND key=?`, userID, key).Scan(&got)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if got != want {
					return nil
				}
			}
		}
		for key, value := range values {
			if _, err := tx.ExecContext(ctx, `INSERT INTO user_preferences(user_id,key,value) VALUES(?,?,?) ON CONFLICT(user_id,key) DO UPDATE SET value=excluded.value`, userID, key, value); err != nil {
				return err
			}
		}
		updated = true
		return nil
	})
	return updated, err
}
