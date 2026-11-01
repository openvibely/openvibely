package sqlite

import (
	"context"
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
func (r *ProjectComputerRepo) PendingChromeImport(ctx context.Context, id string) (*models.ComputerChromeGrant, error) {
	grants, err := r.ChromeGrants(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, g := range grants {
		if g.TaskID != r.taskID || g.Revoked || g.ExpiresAt > 0 && g.ExpiresAt <= time.Now().Unix() {
			continue
		}
		progress, err := r.ChromeImportProgress(ctx, id, g.ID)
		if err != nil {
			return nil, err
		}
		if len(progress) == 0 {
			continue
		}
		for _, pick := range g.Selections {
			if progress[pick.Profile] == 0 {
				return &g, nil
			}
		}
	}
	return nil, nil
}
