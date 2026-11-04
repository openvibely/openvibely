package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

type ProjectAccessRepo struct{ db *sql.DB }

func NewProjectAccessRepo(db *sql.DB) *ProjectAccessRepo { return &ProjectAccessRepo{db: db} }

var _ persistence.ProjectAccessStore = (*ProjectAccessRepo)(nil)

func (r *ProjectAccessRepo) Get(ctx context.Context, id string) (*models.ProjectAccess, error) {
	p := &models.ProjectAccess{}
	err := r.db.QueryRowContext(ctx, `SELECT project_id,org_id,owner_user_id,visibility,org_role FROM project_organizations WHERE project_id=?`, id).Scan(&p.ProjectID, &p.OrgID, &p.OwnerUserID, &p.Visibility, &p.OrgRole)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// projectRole resolves the maximum of org access and explicit grants. An org
// admin or project owner has owner authority; departed/disabled users have none.
func projectRole(ctx context.Context, db sqlExecutor, userID, projectID string) (string, error) {
	var rank int
	err := db.QueryRowContext(ctx, `SELECT MAX(
 CASE WHEN m.role IN ('owner','admin') OR p.owner_user_id=u.id THEN 4 ELSE 0 END,
 CASE WHEN p.visibility='org' THEN CASE p.org_role WHEN 'editor' THEN 2 ELSE 1 END ELSE 0 END,
 CASE pm.role WHEN 'maintainer' THEN 3 WHEN 'editor' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END)
 FROM project_organizations p JOIN org_memberships m ON m.org_id=p.org_id
 JOIN users u ON u.id=m.user_id AND u.disabled=0
 LEFT JOIN project_members pm ON pm.project_id=p.project_id AND pm.user_id=u.id
 WHERE p.project_id=? AND u.id=?`, projectID, userID).Scan(&rank)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return []string{"", "viewer", "editor", "maintainer", "owner"}[rank], nil
}
func (r *ProjectAccessRepo) Role(ctx context.Context, userID, projectID string) (string, error) {
	return projectRole(ctx, r.db, userID, projectID)
}
func (r *ProjectAccessRepo) Members(ctx context.Context, id string) ([]models.ProjectMember, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT pm.user_id,u.username,pm.role FROM project_members pm JOIN users u ON u.id=pm.user_id JOIN org_memberships m ON m.user_id=u.id JOIN project_organizations p ON p.project_id=pm.project_id AND p.org_id=m.org_id WHERE pm.project_id=? AND u.disabled=0 ORDER BY u.username`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ProjectMember{}
	for rows.Next() {
		var m models.ProjectMember
		if err := rows.Scan(&m.UserID, &m.Username, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func projectAudit(ctx context.Context, db sqlExecutor, projectID, actorID, action, target, details string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO project_audit_events(project_id,actor_user_id,action,target_id,details) VALUES(?,?,?,?,?)`, projectID, actorID, action, target, details)
	return err
}
func (r *ProjectAccessRepo) Record(ctx context.Context, projectID, actorID, action, target, details string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error { return projectAudit(ctx, tx, projectID, actorID, action, target, details) })
}
func (r *ProjectAccessRepo) Update(ctx context.Context, actor string, p models.ProjectAccess) error {
	if (p.Visibility != "org" && p.Visibility != "private") || (p.OrgRole != "viewer" && p.OrgRole != "editor") {
		return persistence.ErrProjectDenied
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		role, err := projectRole(ctx, tx, actor, p.ProjectID)
		if err != nil {
			return err
		}
		if role != "owner" && role != "maintainer" {
			return persistence.ErrProjectDenied
		}
		old := models.ProjectAccess{ProjectID: p.ProjectID}
		err = tx.QueryRowContext(ctx, `SELECT owner_user_id,org_id,visibility,org_role FROM project_organizations WHERE project_id=?`, p.ProjectID).Scan(&old.OwnerUserID, &old.OrgID, &old.Visibility, &old.OrgRole)
		if err != nil {
			return err
		}
		if p.OwnerUserID != old.OwnerUserID && role != "owner" {
			return persistence.ErrProjectDenied
		}
		var enabled int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u JOIN org_memberships m ON m.user_id=u.id WHERE u.id=? AND u.disabled=0 AND m.org_id=?`, p.OwnerUserID, old.OrgID).Scan(&enabled); err != nil {
			return err
		}
		if enabled != 1 {
			return persistence.ErrProjectDenied
		}
		_, err = tx.ExecContext(ctx, `UPDATE project_organizations SET owner_user_id=?,visibility=?,org_role=? WHERE project_id=?`, p.OwnerUserID, p.Visibility, p.OrgRole, p.ProjectID)
		if err != nil {
			return err
		}
		p.OrgID = old.OrgID
		details, _ := json.Marshal(map[string]any{"before": old, "after": p})
		return projectAudit(ctx, tx, p.ProjectID, actor, "project.policy", p.ProjectID, string(details))
	})
}
func (r *ProjectAccessRepo) SetMember(ctx context.Context, actorID, projectID, userID, role string) error {
	if role != "" && role != "viewer" && role != "editor" && role != "maintainer" {
		return persistence.ErrProjectDenied
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		current, err := projectRole(ctx, tx, actorID, projectID)
		if err != nil {
			return err
		}
		if current != "owner" && current != "maintainer" {
			return persistence.ErrProjectDenied
		}
		var enabled int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u JOIN org_memberships m ON m.user_id=u.id JOIN project_organizations p ON p.org_id=m.org_id WHERE u.id=? AND u.disabled=0 AND p.project_id=?`, userID, projectID).Scan(&enabled)
		if err != nil {
			return err
		}
		if enabled != 1 {
			return persistence.ErrProjectDenied
		}
		if role == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM project_members WHERE project_id=? AND user_id=?`, projectID, userID)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?) ON CONFLICT(project_id,user_id) DO UPDATE SET role=excluded.role`, projectID, userID, role)
		}
		if err != nil {
			return err
		}
		return projectAudit(ctx, tx, projectID, actorID, "project.member", userID, role)
	})
}
func (r *ProjectAccessRepo) Audit(ctx context.Context, id string) ([]models.ProjectAuditEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,project_id,actor_user_id,action,target_id,details,created_at FROM project_audit_events WHERE project_id=? ORDER BY id DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ProjectAuditEvent{}
	for rows.Next() {
		var e models.ProjectAuditEvent
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.ActorUserID, &e.Action, &e.TargetID, &e.Details, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// requireProjectMutation runs inside the mutation transaction so a revoked
// membership cannot slip between the outer service check and a database write.
func requireProjectMutation(ctx context.Context, tx sqlExecutor, projectID string, ownerOnly bool) error {
	principal, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return nil
	} // Internal maintenance; user requests always carry a principal.
	role, err := projectRole(ctx, tx, principal.UserID, projectID)
	if err != nil {
		return err
	}
	if role == "owner" || (!ownerOnly && role == "maintainer") {
		return nil
	}
	return persistence.ErrProjectDenied
}
