package sqlite

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/displayindex"
)

// Final output is a complete authoritative message set. The owning transaction
// replaces rows once; it needs no per-message mutation/revision lookup. A zero
// snapshot pointer denotes the row itself, avoiding a second payload rewrite.
func replaceFinalizedDisplay(ctx context.Context, tx sqlExecutor, id string, records []models.ConversationChange) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_items WHERE execution_id=? AND generation=0 AND lane='display'`, id); err != nil {
		return err
	}
	for start := 0; start < len(records); {
		end, bytes := start, 0
		for end < len(records) && end-start < 64 {
			n := len(records[end].Payload)
			if end > start && bytes+n > 256*1024 {
				break
			}
			bytes += n
			end++
		}
		var q strings.Builder
		q.WriteString(`WITH input(execution_id,item_id,position,payload,mutation_id) AS (VALUES `)
		args := make([]any, 0, (end-start)*5)
		for i := start; i < end; i++ {
			if i > start {
				q.WriteByte(',')
			}
			q.WriteString(`(?,?,?,CAST(? AS TEXT),?)`)
			row := records[i]
			args = append(args, id, row.ItemID, row.Position, []byte(row.Payload), persistence.NewID())
		}
		// ParseDisplay preserves encoded tool text verbatim in prefix/text/suffix.
		q.WriteString(`) INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,display_bytes)
 SELECT execution_id,0,'display',item_id,position,'put',payload,mutation_id,
 length(CAST(COALESCE(json_extract(payload,'$.prefix'),'') || COALESCE(json_extract(payload,'$.text'),'') || COALESCE(json_extract(payload,'$.suffix'),'') AS BLOB)) FROM input`)
		if _, err := tx.ExecContext(ctx, q.String(), args...); err != nil {
			return err
		}
		start = end
	}
	return displayindex.Rebuild(ctx, tx, id)
}
