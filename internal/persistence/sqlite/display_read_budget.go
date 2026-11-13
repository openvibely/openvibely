package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ReadProjectDisplayTail authorizes before reading payloads and keeps ownership
// and the output cursor in the same snapshot. It avoids loading the latest window
// merely to fetch the preceding one.
func (r *ExecutionRepo) ReadProjectDisplayTail(ctx context.Context, projectID, id string, before int64, budget int) (models.DisplayTail, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.DisplayTail{}, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=? AND t.project_id=? AND `+workspaceChatPredicate(ctx, "t.project_id", "t.chat_owner_user_id")+`)`, id, projectID).Scan(&exists); err != nil {
		return models.DisplayTail{}, err
	}
	if !exists {
		return models.DisplayTail{}, persistence.ErrNotFound
	}
	result, err := readDisplayTail(ctx, tx, id, before, budget)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func readDisplayTail(ctx context.Context, tx sqlExecutor, id string, before int64, budget int) (models.DisplayTail, error) {
	result := models.DisplayTail{}
	if budget <= 0 {
		budget = 1
	}
	// A plan is a separate projection: surrounding history need not be loaded
	// just to preserve approval content. Raw unfinished streams still require
	// their parsing context until safe stream boundaries are available.
	var complete sql.NullString
	queryHead := `SELECT CASE WHEN display_digest IS NOT NULL THEN hex(display_digest) ELSE 'seq-'||COALESCE((SELECT MAX(sequence) FROM conversation_items WHERE execution_id=executions.id AND lane='display'),0) END,
      CASE WHEN (?=0 AND COALESCE((SELECT end_bytes FROM execution_display_groups WHERE execution_id=executions.id ORDER BY first_position DESC,first_item DESC LIMIT 1),0)+COALESCE((SELECT end_offset FROM execution_stream_text WHERE execution_id=executions.id ORDER BY end_offset DESC LIMIT 1),0)<=?) THEN ` + displayOutputSQL + ` END,
      COALESCE((SELECT end_bytes FROM execution_display_groups WHERE execution_id=executions.id ORDER BY first_position DESC,first_item DESC LIMIT 1),0)+COALESCE((SELECT end_offset FROM execution_stream_text WHERE execution_id=executions.id ORDER BY end_offset DESC LIMIT 1),0),
      CASE WHEN ?=0 THEN COALESCE((SELECT content FROM execution_display_plans WHERE execution_id=executions.id),'') ELSE '' END
      FROM executions WHERE id=?`
	if err := tx.QueryRowContext(ctx, queryHead, before, budget, before, id).Scan(&result.Revision, &complete, &result.EndOffset, &result.Plan); err != nil {
		return result, err
	}
	if complete.Valid {
		result.Text = complete.String
		return result, nil
	}
	if before <= 0 {
		handled, err := readStreamDisplayTail(ctx, tx, id, before, budget, &result)
		if err != nil || handled {
			return result, err
		}
		if before == -1 {
			before = 0
		}
	}

	// The write path records safe tool-group boundaries. Seek those boundaries
	// backwards; do not materialize metadata for the rest of the transcript.
	var beforePos int64
	var beforeItem string
	if before != 0 {
		if err := tx.QueryRowContext(ctx, `SELECT position,item_id FROM conversation_items WHERE execution_id=? AND generation=0 AND lane='display' AND is_current=1 AND operation!='remove' AND sequence=?`, id, before).Scan(&beforePos, &beforeItem); err != nil {
			return result, err
		}
	}
	q := `SELECT first_position,first_item,byte_count FROM execution_display_groups WHERE execution_id=?`
	args := []any{id}
	if before != 0 {
		q += ` AND (first_position,first_item)<(?,?)`
		args = append(args, beforePos, beforeItem)
	}
	q += ` ORDER BY first_position DESC,first_item DESC`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return result, err
	}
	var firstPos int64
	var firstItem string
	used, count, earlier := 0, 0, false
	for rows.Next() {
		var pos int64
		var item string
		var size int
		if err = rows.Scan(&pos, &item, &size); err != nil {
			break
		}
		if count > 0 && used+size > budget {
			earlier = true
			break
		}
		firstPos, firstItem = pos, item
		used += size
		count++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if count == 0 {
		return result, nil
	}
	q = `SELECT head.sequence,` + displayItemTextSQL + ` FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence)
 WHERE head.execution_id=? AND head.generation=0 AND head.lane='display' AND head.is_current=1 AND head.operation!='remove' AND (head.position,head.item_id)>=(?,?)`
	args = []any{id, firstPos, firstItem}
	if before != 0 {
		q += ` AND (head.position,head.item_id)<(?,?)`
		args = append(args, beforePos, beforeItem)
	}
	q += ` ORDER BY head.position,head.item_id`
	rows, err = tx.QueryContext(ctx, q, args...)
	if err != nil {
		return result, err
	}
	var text strings.Builder
	text.Grow(used)
	first := true
	for rows.Next() {
		var seq int64
		var part string
		if err = rows.Scan(&seq, &part); err != nil {
			break
		}
		if first && earlier {
			result.Before = seq
		}
		first = false
		text.WriteString(part)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	result.Text = text.String()
	return result, err
}

// ListDisplayExecutions is exclusively a UI read. Model recovery and exports
// continue to use complete-history APIs. Metadata is selected without output so
// the budget is applied before large payloads enter Go or SQLite result buffers.
func (r *ExecutionRepo) ListDisplayExecutions(ctx context.Context, projectID, taskID, before string, limit, budget int) ([]models.Execution, bool, error) {
	if limit <= 0 {
		return []models.Execution{}, false, nil
	}
	var query string
	var args []any
	if taskID == "" {
		query, args = chatHistoryPageSQL(projectID, before, limit+1)
		query = scopeWorkspaceExecutions(ctx, query)
		query = strings.Replace(query, executionSelectColumnsAliasLight, strings.Replace(executionSelectColumnsAliasLight, executionOutputSelectAlias, "''", 1), 1)
	} else {
		query, args = taskExecutionPageSQL(taskID, before, limit+1)
		i := strings.LastIndex(query, " ORDER BY")
		query = query[:i] + " AND EXISTS (SELECT 1 FROM tasks ct WHERE ct.id=executions.task_id AND " + workspaceChatPredicate(ctx, "ct.project_id", "ct.chat_owner_user_id") + ")" + query[i:]
		query = strings.Replace(query, executionSelectColumnsLight, strings.Replace(executionSelectColumnsLight, executionOutputSelect, "''", 1), 1)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	execs, err := scanExecutionsNewestFirstAsChronological(rows)
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	hasEarlier := len(execs) > limit
	if hasEarlier {
		execs = execs[len(execs)-limit:]
	}
	start := len(execs)
	for i := len(execs) - 1; i >= 0; i-- {
		if start < len(execs) && budget <= 0 {
			hasEarlier = true
			break
		}
		tail, err := readDisplayTail(ctx, tx, execs[i].ID, 0, budget)
		if err != nil {
			return nil, false, fmt.Errorf("read display tail: %w", err)
		}
		execs[i].Output = tail.Text
		execs[i].DisplayPlan = tail.Plan
		if tail.Before != 0 {
			execs[i].DisplayTail = &tail
		}
		budget -= len(tail.Text) + len(execs[i].PromptSent)
		start = i
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return execs[start:], hasEarlier, nil
}

func (r *ExecutionRepo) GetDisplayExecutionTail(ctx context.Context, id string) (*models.Execution, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	columns := strings.Replace(executionSelectColumnsLight, executionOutputSelect, "''", 1)
	execution, err := scanExecutionRow(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM executions WHERE id=? AND EXISTS (SELECT 1 FROM tasks ct WHERE ct.id=executions.task_id AND `+workspaceChatPredicate(ctx, "ct.project_id", "ct.chat_owner_user_id")+`)`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tail, err := readDisplayTail(ctx, tx, id, 0, 1024*1024)
	if err != nil {
		return nil, err
	}
	execution.Output = tail.Text
	execution.DisplayPlan = tail.Plan
	if tail.Before != 0 {
		execution.DisplayTail = &tail
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &execution, nil
}
