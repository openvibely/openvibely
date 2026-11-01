package sqlite

import (
	"context"
	"fmt"
)

// Reindex the visible category in one transaction. Records retain their owners.
func (r *TaskRepo) reorderAllProjectsTask(ctx context.Context, taskID string, position int) error {
	if position < 0 {
		return fmt.Errorf("invalid position")
	}
	return withImmediateTx(ctx, r.db, func(exec sqlExecutor) error {
		var category string
		if err := exec.QueryRowContext(ctx, `SELECT category FROM tasks WHERE id=? AND category!='chat'`, taskID).Scan(&category); err != nil {
			return err
		}
		rows, err := exec.QueryContext(ctx, `SELECT id FROM tasks WHERE category=? ORDER BY display_order,created_at,id`, category)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if id != taskID {
				ids = append(ids, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if position > len(ids) {
			position = len(ids)
		}
		ids = append(ids, "")
		copy(ids[position+1:], ids[position:])
		ids[position] = taskID
		for i, id := range ids {
			if _, err := exec.ExecContext(ctx, `UPDATE tasks SET display_order=?,updated_at=datetime('now') WHERE id=?`, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}
