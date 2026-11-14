package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"time"
)

func (r *ProjectComputerRepo) ChromeImportProgress(ctx context.Context, id, grant string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.profile,p.imported FROM computer_chrome_profile_imports p JOIN computer_chrome_grants g ON g.id=p.grant_id WHERE g.project_id=? AND p.grant_id=? AND p.task_id=?`, id, grant, r.taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var profile string
		var count int
		if err := rows.Scan(&profile, &count); err != nil {
			return nil, err
		}
		out[profile] = count
	}
	return out, rows.Err()
}
func (r *ProjectComputerRepo) RecordChromeProfile(ctx context.Context, id, grant, profile string, count int) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO computer_chrome_profile_imports(grant_id,task_id,profile,imported) SELECT id,?,?,? FROM computer_chrome_grants WHERE id=? AND project_id=? AND revoked=0 AND (scope='project' OR task_id=?) ON CONFLICT(grant_id,task_id,profile) DO UPDATE SET imported=excluded.imported`, r.taskID, profile, count, grant, id, r.taskID)
		return err
	})
}

// Find unfinished work directly, independent of the paginated permissions list.
func (r *ProjectComputerRepo) PendingChromeImport(ctx context.Context, id string) (*models.ComputerChromeGrant, error) {
	var g models.ComputerChromeGrant
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT g.id,g.project_id,g.task_id,g.scope,g.selections,g.expires_at,g.revoked,g.one_time
 FROM computer_chrome_grants g
 WHERE g.project_id=? AND g.task_id=? AND g.revoked=0 AND (g.expires_at=0 OR g.expires_at>?)
 AND EXISTS (SELECT 1 FROM computer_chrome_profile_imports p WHERE p.grant_id=g.id AND p.task_id=?)
 AND EXISTS (SELECT 1 FROM json_each(g.selections) pick WHERE NOT EXISTS (
 SELECT 1 FROM computer_chrome_profile_imports p WHERE p.grant_id=g.id AND p.task_id=?
 AND p.profile=json_extract(pick.value,'$.profile') AND p.imported>0))
 ORDER BY g.expires_at DESC,g.id DESC LIMIT 1`, id, r.taskID, time.Now().Unix(), r.taskID, r.taskID).Scan(&g.ID, &g.ProjectID, &g.TaskID, &g.Scope, &raw, &g.ExpiresAt, &g.Revoked, &g.OneTime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &g.Selections); err != nil {
		return nil, err
	}
	return &g, nil
}
