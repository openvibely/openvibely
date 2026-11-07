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
	// Small responses and live streams need no per-row materialization in Go.
	// SQLite checks indexed byte metadata first, and only assembles full output
	// when it fits (or a live/cancelled raw stream requires it). Plan-bearing
	// responses remain complete: existing approval controls inspect their raw text.
	// Older/edited rows without plan metadata fall back to inspecting public text.
	var complete sql.NullString
	queryHead := `SELECT CASE WHEN display_digest IS NOT NULL THEN hex(display_digest) ELSE 'seq-'||COALESCE((SELECT MAX(sequence) FROM conversation_items WHERE execution_id=executions.id AND lane='display'),0) END,
      CASE WHEN status IN ('running','queued') OR EXISTS(SELECT 1 FROM execution_stream_text WHERE execution_id=executions.id) OR (?=0 AND (COALESCE(display_has_plan,
        EXISTS(SELECT 1 FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence)
          WHERE head.execution_id=executions.id AND head.generation=0 AND head.lane='display' AND head.is_current=1 AND head.operation!='remove' AND instr(` + displayItemTextSQL + `,'<proposed_plan>')>0))=1
        OR COALESCE((SELECT SUM(display_bytes) FROM conversation_items WHERE execution_id=executions.id AND generation=0 AND lane='display' AND is_current=1 AND operation!='remove'),0)
        + COALESCE((SELECT end_offset FROM execution_stream_text WHERE execution_id=executions.id ORDER BY end_offset DESC LIMIT 1),0) <= ?)) THEN ` + displayOutputSQL + ` END
      FROM executions WHERE id=?`
	if err := tx.QueryRowContext(ctx, queryHead, before, budget, id).Scan(&result.Revision, &complete); err != nil {
		return result, err
	}
	if complete.Valid {
		result.Text = complete.String
		return result, nil
	}
	const filter = `execution_id=? AND generation=0 AND lane='display' AND is_current=1 AND operation!='remove'`
	query := `SELECT head.sequence,head.display_bytes,COALESCE(json_extract(base.payload,'$.type'),''),COALESCE(json_extract(base.payload,'$.name'),'')
 FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence)
 WHERE head.execution_id=? AND head.generation=0 AND head.lane='display' AND head.is_current=1 AND head.operation!='remove'`
	args := []any{id}
	if before != 0 {
		var pos int64
		var item string
		if err := tx.QueryRowContext(ctx, `SELECT position,item_id FROM conversation_items WHERE `+filter+` AND sequence=?`, id, before).Scan(&pos, &item); err != nil {
			return result, err
		}
		query += ` AND (head.position<? OR (head.position=? AND head.item_id<?))`
		args = append(args, pos, pos, item)
	}
	query += ` ORDER BY head.position,head.item_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	// Only split where all preceding tool calls are complete. A result must
	// retain its call, including the earlier calls in a parallel FIFO queue.
	// Unmatched/aliased markers conservatively keep a larger group intact.
	type group struct{ first, bytes int }
	var groups []group
	var sequences []int64
	pending := make(map[string]int)
	open := 0
	for rows.Next() {
		var seq int64
		var size int
		var kind, name string
		if err = rows.Scan(&seq, &size, &kind, &name); err != nil {
			break
		}
		if open == 0 {
			groups = append(groups, group{first: len(sequences)})
		}
		sequences = append(sequences, seq)
		groups[len(groups)-1].bytes += size
		switch kind {
		case "using":
			pending[name]++
			open++
		case "tool_result":
			if pending[name] > 0 {
				pending[name]--
				open--
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(sequences) == 0 {
		return result, nil
	}
	used, first := 0, len(sequences)
	for i := len(groups) - 1; i >= 0; i-- {
		if first < len(sequences) && used+groups[i].bytes > budget {
			break
		}
		used += groups[i].bytes
		first = groups[i].first
	}
	if first > 0 {
		result.Before = sequences[first]
	}
	sequences = sequences[first:]
	// Fetch payloads only for the selected complete groups. SQL batches are an
	// implementation detail, not another visible item-count limit.
	var text strings.Builder
	text.Grow(used)
	for start := 0; start < len(sequences); start += 256 {
		batch := sequences[start:min(start+256, len(sequences))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args = make([]any, len(batch))
		for i, seq := range batch {
			args[i] = seq
		}
		rows, err = tx.QueryContext(ctx, `SELECT `+displayItemTextSQL+` FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence) WHERE head.sequence IN (`+placeholders+`) ORDER BY head.position,head.item_id`, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var part string
			if err = rows.Scan(&part); err != nil {
				break
			}
			text.WriteString(part)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return result, err
		}
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
	if tail.Before != 0 {
		execution.DisplayTail = &tail
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &execution, nil
}
