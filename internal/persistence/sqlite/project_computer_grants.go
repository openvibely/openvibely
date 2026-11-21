package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"time"
)

func (r *ProjectComputerRepo) CreateChromeGrant(ctx context.Context, id, scope string, selections []models.ChromeCookieSelection, expires int64) (*models.ComputerChromeGrant, error) {
	oneTime := expires == -1
	if oneTime {
		expires = time.Now().Add(time.Hour).Unix()
		scope = "task"
	}
	if r.taskID == "" || (scope != "task" && scope != "project") || len(selections) == 0 || len(selections) > 100 || (expires != 0 && expires <= time.Now().Unix()) || expires > time.Now().Add(30*24*time.Hour).Unix()+1 {
		return nil, errors.New("invalid Chrome grant")
	}
	data, err := json.Marshal(selections)
	if err != nil {
		return nil, err
	}
	grant := &models.ComputerChromeGrant{ID: persistence.NewID(), ProjectID: id, TaskID: r.taskID, Scope: scope, Selections: selections, ExpiresAt: expires, OneTime: oneTime}
	err = withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT s.enabled=1 AND p.enabled=1 AND (s.handoff_kind='' OR s.held=1) FROM task_computer_sessions s JOIN project_computers p ON p.project_id=s.project_id WHERE s.project_id=? AND s.task_id=?`, id, r.taskID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errors.New("Enable the task computer and finish any in-progress sign-in verification before importing")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO computer_chrome_grants(id,project_id,task_id,scope,selections,expires_at,one_time) VALUES (?,?,?,?,?,?,?)`, grant.ID, id, r.taskID, scope, string(data), expires, oneTime)
		return err
	})
	return grant, err
}
func (r *ProjectComputerRepo) ChromeGrants(ctx context.Context, id string) ([]models.ComputerChromeGrant, error) {
	return r.chromeGrants(ctx, id, 200, 0)
}

// Fetch one extra row so callers can determine whether another page exists.
func (r *ProjectComputerRepo) ChromeGrantsPage(ctx context.Context, id string, offset int) ([]models.ComputerChromeGrant, error) {
	if offset < 0 {
		return nil, errors.New("invalid grant offset")
	}
	return r.chromeGrants(ctx, id, 201, offset)
}

func (r *ProjectComputerRepo) chromeGrants(ctx context.Context, id string, limit, offset int) ([]models.ComputerChromeGrant, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,project_id,COALESCE(task_id,''),scope,selections,expires_at,revoked,one_time FROM computer_chrome_grants WHERE project_id=? AND (scope='project' OR task_id=?) ORDER BY expires_at DESC,id DESC LIMIT ? OFFSET ?`, id, r.taskID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ComputerChromeGrant{}
	for rows.Next() {
		var g models.ComputerChromeGrant
		var raw string
		if err := rows.Scan(&g.ID, &g.ProjectID, &g.TaskID, &g.Scope, &raw, &g.ExpiresAt, &g.Revoked, &g.OneTime); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &g.Selections); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (r *ProjectComputerRepo) ChromeGrant(ctx context.Context, id, grantID string) (*models.ComputerChromeGrant, error) {
	// A specific grant must remain addressable regardless of list pagination,
	// expiry or revocation. Preserve the same project/task visibility as the list.
	var grant models.ComputerChromeGrant
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT id,project_id,COALESCE(task_id,''),scope,selections,expires_at,revoked,one_time FROM computer_chrome_grants WHERE id=? AND project_id=? AND (scope='project' OR task_id=?)`, grantID, id, r.taskID).Scan(&grant.ID, &grant.ProjectID, &grant.TaskID, &grant.Scope, &raw, &grant.ExpiresAt, &grant.Revoked, &grant.OneTime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("Chrome grant not found for this task")
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &grant.Selections); err != nil {
		return nil, err
	}
	return &grant, nil
}
func (r *ProjectComputerRepo) RecordChromeInstall(ctx context.Context, id, grantID string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM computer_chrome_grants WHERE id=? AND project_id=? AND revoked=0 AND (expires_at=0 OR expires_at>?) AND (one_time=0 OR NOT EXISTS (SELECT 1 FROM computer_chrome_installations i WHERE i.grant_id=computer_chrome_grants.id)) AND (scope='project' OR task_id=?))`, grantID, id, time.Now().Unix(), r.taskID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errors.New("Chrome grant is unavailable for this task")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO computer_chrome_installations(grant_id,task_id) VALUES (?,?) ON CONFLICT DO NOTHING`, grantID, r.taskID)
		return err
	})
}
func (r *ProjectComputerRepo) RevokeChromeGrant(ctx context.Context, id, grantID string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE computer_chrome_grants SET revoked=1 WHERE project_id=? AND id=? AND (scope='project' OR task_id=?)`, id, grantID, r.taskID)
		return err
	})
}
func (r *ProjectComputerRepo) ChromeInstallations(ctx context.Context, id, grantID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT i.task_id FROM computer_chrome_installations i JOIN computer_chrome_grants g ON g.id=i.grant_id WHERE g.id=? AND g.project_id=?`, grantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var task string
		if err := rows.Scan(&task); err != nil {
			return nil, err
		}
		ids = append(ids, task)
	}
	return ids, rows.Err()
}

func (r *ProjectComputerRepo) ExpiredChromeInstallations(ctx context.Context, id string) ([]models.ComputerChromeGrant, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT g.id,g.selections FROM computer_chrome_grants g JOIN computer_chrome_installations i ON g.id=i.grant_id WHERE g.project_id=? AND i.task_id=? AND (g.revoked=1 OR (g.expires_at>0 AND g.expires_at<=?))`, id, r.taskID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ComputerChromeGrant
	for rows.Next() {
		var g models.ComputerChromeGrant
		var data string
		if err := rows.Scan(&g.ID, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &g.Selections); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (r *ProjectComputerRepo) ClearChromeInstall(ctx context.Context, id, grantID string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM computer_chrome_installations WHERE task_id=? AND grant_id IN (SELECT id FROM computer_chrome_grants WHERE id=? AND project_id=?)`, r.taskID, grantID, id)
		return err
	})
}
