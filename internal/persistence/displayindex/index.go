// Package displayindex maintains the public transcript's safe pagination boundaries.
package displayindex

import (
	"context"
	"database/sql"
	"strings"
)

type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const Schema = `CREATE TABLE execution_display_groups (
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 first_position INTEGER NOT NULL,
 first_item TEXT NOT NULL,
 byte_count INTEGER NOT NULL,
 end_bytes INTEGER NOT NULL,
 PRIMARY KEY(execution_id,first_position,first_item)
);
CREATE TABLE execution_display_plans (
 execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
 content TEXT NOT NULL,
 last_position INTEGER NOT NULL
);`

// Rebuild runs in the owning display-write transaction, never on the read path.
// A boundary is safe only when every preceding tool call has its result.
func Rebuild(ctx context.Context, tx Executor, id string) error {
	return rebuildFrom(ctx, tx, id, -1, "")
}

// Refresh starts at the affected safe group. Appending ordinary new blocks
// revisits only the last group, not all preceding metadata.
func Refresh(ctx context.Context, tx Executor, id string, pos int64, item string) error {
	rows, err := tx.QueryContext(ctx, `SELECT first_position,first_item FROM execution_display_groups WHERE execution_id=? AND (first_position,first_item)<=(?,?) ORDER BY first_position DESC,first_item DESC LIMIT 1`, id, pos, item)
	if err != nil {
		return err
	}
	if rows.Next() {
		err = rows.Scan(&pos, &item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	return rebuildFrom(ctx, tx, id, pos, item)
}

func rebuildFrom(ctx context.Context, tx Executor, id string, pos int64, item string) error {
	var cumulative int64
	prev, err := tx.QueryContext(ctx, `SELECT end_bytes FROM execution_display_groups WHERE execution_id=? AND (first_position,first_item)<(?,?) ORDER BY first_position DESC,first_item DESC LIMIT 1`, id, pos, item)
	if err != nil {
		return err
	}
	if prev.Next() {
		err = prev.Scan(&cumulative)
	}
	if err == nil {
		err = prev.Err()
	}
	prev.Close()
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT head.position,head.item_id,head.display_bytes,COALESCE(json_extract(base.payload,'$.type'),''),COALESCE(json_extract(base.payload,'$.name'),''),instr((`+ItemTextSQL+`),'<proposed_plan>')>0,substr((`+ItemTextSQL+`),-14)
 FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence)
 WHERE head.execution_id=? AND head.generation=0 AND head.lane='display' AND head.is_current=1 AND head.operation!='remove' AND (head.position,head.item_id)>=(?,?) ORDER BY head.position,head.item_id`, id, pos, item)
	if err != nil {
		return err
	}
	type group struct {
		pos   int64
		item  string
		bytes int64
	}
	var groups []group
	hasPlan := false
	pending := map[string]int{}
	open := 0
	for rows.Next() {
		var pos, size int64
		var item, kind, name string
		var tail string
		var marker bool
		if err = rows.Scan(&pos, &item, &size, &kind, &name, &marker, &tail); err != nil {
			break
		}
		hasPlan = hasPlan || marker || mayStartPlan(tail)
		if open == 0 {
			groups = append(groups, group{pos: pos, item: item})
		}
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
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM execution_display_groups WHERE execution_id=? AND (first_position,first_item)>=(?,?)`, id, pos, item); err != nil {
		return err
	}
	for start := 0; start < len(groups); start += 128 {
		batch := groups[start:min(start+128, len(groups))]
		args := make([]any, 0, len(batch)*5)
		for _, g := range batch {
			cumulative += g.bytes
			args = append(args, id, g.pos, g.item, g.bytes, cumulative)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO execution_display_groups(execution_id,first_position,first_item,byte_count,end_bytes) VALUES `+strings.TrimSuffix(strings.Repeat("(?,?,?,?,?),", len(batch)), ","), args...)
		if err != nil {
			return err
		}
	}
	if !hasPlan {
		rows, err := tx.QueryContext(ctx, `SELECT 1 FROM execution_display_plans WHERE execution_id=? AND last_position>=?`, id, pos)
		if err != nil {
			return err
		}
		hasPlan = rows.Next()
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if hasPlan {
		return RefreshPlan(ctx, tx, id)
	}
	return nil
}

// Only a real opening tag or an unfinished opening tag at the end can
// introduce a plan. Use rendered text so JSON escapes and append deltas
// behave exactly like the projection reader.
func mayStartPlan(text string) bool {
	const tag = "<proposed_plan>"
	if strings.Contains(text, tag) {
		return true
	}
	i := strings.LastIndexByte(text, '<')
	return i >= 0 && strings.HasPrefix(tag, text[i:])
}
