package displayindex

import (
	"context"
	"strings"
)

const ItemBodySQL = `CASE WHEN head.snapshot_sequence=0 OR head.snapshot_sequence=head.sequence
 THEN COALESCE(json_extract(base.payload,'$.text'),'')
 ELSE COALESCE((SELECT group_concat(part,'') FROM (
 SELECT json_extract(delta.payload,'$.text') AS part FROM conversation_items delta
 WHERE delta.execution_id=head.execution_id AND delta.generation=head.generation AND delta.lane='display' AND delta.item_id=head.item_id AND delta.sequence>=head.snapshot_sequence AND delta.sequence<=head.sequence ORDER BY delta.sequence)),'') END`

const ItemTextSQL = `COALESCE(json_extract(base.payload,'$.prefix'),'') ||
 CASE WHEN json_extract(base.payload,'$.type')='tool_result' AND COALESCE(json_extract(base.payload,'$.encoded'),0)=0
 THEN replace(replace((` + ItemBodySQL + `),'&','&amp;'),'[','&#91;')
 ELSE (` + ItemBodySQL + `) END || COALESCE(json_extract(base.payload,'$.suffix'),'')`

// RefreshPlan stores only the plan, not its surrounding execution output.
func RefreshPlan(ctx context.Context, tx Executor, id string) error {
	rows, err := tx.QueryContext(ctx, `SELECT head.position,`+ItemTextSQL+` FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence) WHERE head.execution_id=? AND head.generation=0 AND head.lane='display' AND head.is_current=1 AND head.operation!='remove' ORDER BY head.position,head.item_id`, id)
	if err != nil {
		return err
	}
	var carry string
	var lastPosition int64 = -1
	var result strings.Builder
	inside := false
	const start = "<proposed_plan>"
	const end = "</proposed_plan>"
	for rows.Next() {
		var part string
		var position int64
		if err = rows.Scan(&position, &part); err != nil {
			break
		}
		carry += part
		for {
			tag := start
			if inside {
				tag = end
			}
			i := strings.Index(carry, tag)
			if i < 0 {
				keep := min(len(carry), len(tag)-1)
				if inside {
					lastPosition = position
					result.WriteString(carry[:len(carry)-keep])
				}
				carry = carry[len(carry)-keep:]
				break
			}
			if inside {
				lastPosition = position
				result.WriteString(carry[:i+len(tag)])
				result.WriteByte('\n')
			} else {
				result.WriteString(start)
			}
			carry = carry[i+len(tag):]
			inside = !inside
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if inside {
		result.WriteString(carry)
	}
	pendingStart := false
	for n := 1; n < len(start) && n <= len(carry); n++ {
		pendingStart = pendingStart || strings.HasSuffix(carry, start[:n])
	}
	if inside || pendingStart {
		lastPosition = 1<<63 - 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_display_plans(execution_id,content,last_position) VALUES(?,?,?) ON CONFLICT(execution_id) DO UPDATE SET content=excluded.content,last_position=excluded.last_position`, id, result.String(), lastPosition)
	return err
}
