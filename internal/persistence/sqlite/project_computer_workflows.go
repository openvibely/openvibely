package sqlite

import (
	"context"
	"database/sql"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func (r *ProjectComputerRepo) SaveWorkflow(ctx context.Context, w *models.ComputerWorkflow) error {
	if w.ID != "" {
		return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
			res, err := tx.ExecContext(ctx, `UPDATE computer_workflows SET name=?,instructions=? WHERE id=? AND project_id=?`, w.Name, w.Instructions, w.ID, w.ProjectID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err == nil && n == 0 {
				return sql.ErrNoRows
			}
			return err
		})
	}
	w.ID = persistence.NewID()
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO computer_workflows(id,project_id,name,instructions) VALUES(?,?,?,?)`, w.ID, w.ProjectID, w.Name, w.Instructions)
		return err
	})
}
func (r *ProjectComputerRepo) Workflows(ctx context.Context, id string) ([]models.ComputerWorkflow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,project_id,name,instructions FROM computer_workflows WHERE project_id=? ORDER BY name,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ComputerWorkflow{}
	for rows.Next() {
		var w models.ComputerWorkflow
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Instructions); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (r *ProjectComputerRepo) Workflow(ctx context.Context, id, key string) (*models.ComputerWorkflow, error) {
	w := &models.ComputerWorkflow{}
	err := r.db.QueryRowContext(ctx, `SELECT id,project_id,name,instructions FROM computer_workflows WHERE project_id=? AND id=?`, id, key).Scan(&w.ID, &w.ProjectID, &w.Name, &w.Instructions)
	return w, err
}
func (r *ProjectComputerRepo) DeleteWorkflow(ctx context.Context, id, key string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM computer_workflows WHERE project_id=? AND id=?`, id, key)
		return err
	})
}
