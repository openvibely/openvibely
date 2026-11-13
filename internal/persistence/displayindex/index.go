// Package displayindex maintains the public transcript's safe pagination boundaries.
package displayindex

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
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
	if err := rebuildFrom(ctx, tx, id, -1, ""); err != nil {
		return err
	}
	return RefreshPlan(ctx, tx, id)
}

// Refresh starts at the affected safe group. Appending ordinary new blocks
// revisits only the last group, not all preceding metadata.
func Refresh(ctx context.Context, tx Executor, id string, pos int64, item string, changes []models.ConversationChange) error {
	originalPos := pos
	hasPlan := false
	for _, change := range changes {
		if change.Lane == models.ConversationDisplay && change.Generation == 0 {
			hasPlan = hasPlan || payloadMayStartPlan(change.Payload)
			if !hasPlan && change.Operation == models.ConversationAppend {
				marker, err := appendBoundaryMayStartPlan(ctx, tx, id, change)
				if err != nil {
					return err
				}
				hasPlan = marker
			}
		}
	}
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
	if err := rebuildFrom(ctx, tx, id, pos, item); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT 1 FROM execution_display_plans WHERE execution_id=? AND last_position>=?`, id, originalPos)
	if err != nil {
		return err
	}
	hasPlan = rows.Next() || hasPlan
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if hasPlan {
		return RefreshPlan(ctx, tx, id)
	}
	return nil
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
	rows, err := tx.QueryContext(ctx, `SELECT head.position,head.item_id,head.display_bytes,COALESCE(json_extract(base.payload,'$.type'),''),COALESCE(json_extract(base.payload,'$.name'),'')
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
	pending := map[string]int{}
	open := 0
	for rows.Next() {
		var pos, size int64
		var item, kind, name string
		if err = rows.Scan(&pos, &item, &size, &kind, &name); err != nil {
			break
		}
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

// Inspect only the incoming mutation, never its accumulated append chain.
// A partial tag triggers a projection refresh, which records pending state
// until a following mutation completes or invalidates the tag.
func payloadMayStartPlan(payload []byte) bool {
	if !bytes.ContainsRune(payload, '<') && !bytes.Contains(payload, []byte(`\u003`)) {
		return false
	}
	var value struct {
		Prefix string `json:"prefix"`
		Text   string `json:"text"`
		Suffix string `json:"suffix"`
	}
	if json.Unmarshal(payload, &value) != nil {
		return false
	}
	return mayStartPlan(value.Prefix) || mayStartPlan(value.Text) || mayStartPlan(value.Suffix)
}

// Read at most the previous opening-tag length, walking deltas backwards.
// Prefixes precede the body; suffixes do not, because append inserts before
// the suffix. No group_concat or full-item reconstruction is needed.
func appendBoundaryMayStartPlan(ctx context.Context, tx Executor, id string, change models.ConversationChange) (bool, error) {
	const width = len("<proposed_plan>") - 1
	var delta struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(change.Payload, &delta); err != nil {
		return false, err
	}
	// A boundary can matter only when this delta begins with a continuation
	// of the opening tag. Ordinary deltas require no prior-content read.
	possible := false
	const tag = "<proposed_plan>"
	if delta.Text != "" {
		for i := 1; i < len(tag); i++ {
			n := min(len(delta.Text), len(tag)-i)
			possible = possible || delta.Text[:n] == tag[i:i+n]
		}
	}
	if !possible {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT substr(COALESCE(json_extract(payload,'$.text'),''),-14),substr(COALESCE(json_extract(payload,'$.prefix'),''),-14)
 FROM conversation_items WHERE execution_id=? AND generation=0 AND lane='display' AND item_id=? AND sequence<=? AND sequence>=(SELECT COALESCE(NULLIF(snapshot_sequence,0),sequence) FROM conversation_items WHERE sequence=?) ORDER BY sequence DESC`, id, change.ItemID, change.PreviousRevision, change.PreviousRevision)
	if err != nil {
		return false, err
	}
	var tail string
	for rows.Next() {
		var text, prefix string
		if err = rows.Scan(&text, &prefix); err != nil {
			break
		}
		tail = prefix + text + tail
		if len(tail) >= width {
			tail = tail[len(tail)-width:]
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	return strings.Contains(tail+delta.Text[:min(width, len(delta.Text))], "<proposed_plan>"), nil
}
