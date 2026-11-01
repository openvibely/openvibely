package sqlite

import (
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
)

// Reorder by visible index, not by the numeric display_order. A project's
// positions can be sparse after another project is interleaved in All projects.
func (r *TaskRepo) reorderTaskInScope(ctx context.Context, taskID string, position int, allProjects bool, anchorID string, before bool) error {
	if position < 0 {
		return fmt.Errorf("invalid position")
	}
	return withImmediateTx(ctx, r.db, func(exec sqlExecutor) error {
		var category, owner string
		if err := exec.QueryRowContext(ctx, `SELECT category, project_id FROM tasks WHERE id=?`, taskID).Scan(&category, &owner); err != nil {
			return err
		}
		if allProjects && category == "chat" {
			return fmt.Errorf("cannot reorder chat history")
		}
		query := `SELECT id, display_order FROM tasks WHERE category=?`
		args := []any{category}
		if !allProjects {
			query += ` AND project_id=?`
			args = append(args, owner)
		}
		query += ` ORDER BY display_order, created_at, id`
		rows, err := exec.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		var ids []string
		var slots []int
		oldOrders := map[string]int{}
		oldPosition := -1
		for rows.Next() {
			var id string
			var order int
			if err := rows.Scan(&id, &order); err != nil {
				rows.Close()
				return err
			}
			if id == taskID {
				oldPosition = len(slots)
			} else {
				ids = append(ids, id)
			}
			slots = append(slots, order)
			oldOrders[id] = order
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if anchorID != "" {
			found := false
			for i, id := range ids {
				if id == anchorID {
					position = i
					if !before {
						position++
					}
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("reorder target does not belong to this task scope")
			}
		}
		if position > len(ids) {
			position = len(ids)
		}
		if oldPosition == position && !allProjects {
			return nil
		}
		// Preserve sparse slots unless the moved range touches a tie. Tied
		// slots cannot express the requested order, so repair that column.
		normalize := allProjects
		start, end := oldPosition, position
		if start > end {
			start, end = end, start
		}
		for i := start; i <= end; i++ {
			if (i > 0 && slots[i] == slots[i-1]) || (i+1 < len(slots) && slots[i] == slots[i+1]) {
				normalize = true
				break
			}
		}
		ids = append(ids, "")
		copy(ids[position+1:], ids[position:])
		ids[position] = taskID
		for i, id := range ids {
			order := slots[i]
			if normalize {
				order = i
			}
			slots[i] = order
			if oldOrders[id] == order {
				continue
			}
			if _, err := exec.ExecContext(ctx, `UPDATE tasks SET display_order=?, updated_at=datetime('now') WHERE id=?`, order, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *TaskRepo) ReorderTaskRelative(ctx context.Context, taskID, anchorID string, before bool, projectScope string) error {
	if anchorID == "" {
		return fmt.Errorf("reorder target is required")
	}
	return r.reorderTaskInScope(ctx, taskID, 0, projectScope == models.AllProjectsID, anchorID, before)
}
