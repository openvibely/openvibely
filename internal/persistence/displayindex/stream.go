package displayindex

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/transcript"
)

const StreamSchema = `CREATE TABLE execution_stream_groups (
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 start_offset INTEGER NOT NULL, end_offset INTEGER NOT NULL,
 PRIMARY KEY(execution_id,start_offset)
);
CREATE TABLE execution_stream_page_state (
 execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
 indexed_end INTEGER NOT NULL, next_scan INTEGER NOT NULL
);`

const streamIndexBatch = 256 * 1024

// IndexStream seals only complete groups preceding the unfinished final group.
// Offsets refer to the original bytes, not normalized or re-encoded output.
func IndexStream(ctx context.Context, tx Executor, id string, force bool) error {
	var indexed, end, next int64
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE((SELECT indexed_end FROM execution_stream_page_state WHERE execution_id=?),0),COALESCE((SELECT next_scan FROM execution_stream_page_state WHERE execution_id=?),?),COALESCE((SELECT end_offset FROM execution_stream_text WHERE execution_id=? ORDER BY end_offset DESC LIMIT 1),0)`, id, id, streamIndexBatch, id)
	if err != nil {
		return err
	}
	if rows.Next() {
		err = rows.Scan(&indexed, &next, &end)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if end-indexed < streamIndexBatch || (!force && end < next) {
		return nil
	}
	rows, err = tx.QueryContext(ctx, `SELECT CAST(substr(CAST(text AS BLOB),max(1,?- (end_offset-length(CAST(text AS BLOB)))+1)) AS TEXT) FROM execution_stream_text WHERE execution_id=? AND end_offset>? ORDER BY end_offset`, indexed, id, indexed)
	if err != nil {
		return err
	}
	var source strings.Builder
	for rows.Next() {
		var part string
		if err = rows.Scan(&part); err != nil {
			break
		}
		source.WriteString(part)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	raw := source.String()
	items := transcript.ParseDisplay(raw)
	// Legacy marker normalization may change byte lengths. Never manufacture an
	// offset for normalized text: leave that suffix as one indivisible group.
	if transcript.DisplayText(items) != raw {
		items = nil
	}
	pending := map[string]int{}
	open := 0
	var boundaries []int64
	offset := indexed
	for _, item := range items {
		if open == 0 {
			boundaries = append(boundaries, offset)
		}
		offset += int64(len(item.Prefix) + len(item.Text) + len(item.Suffix))
		switch item.Type {
		case "using":
			pending[item.Name]++
			open++
		case "tool_result":
			if pending[item.Name] > 0 {
				pending[item.Name]--
				open--
			}
		}
	}
	for i := 0; i+1 < len(boundaries); i++ {
		if _, err = tx.ExecContext(ctx, `INSERT INTO execution_stream_groups(execution_id,start_offset,end_offset) VALUES(?,?,?)`, id, boundaries[i], boundaries[i+1]); err != nil {
			return err
		}
	}
	if len(boundaries) > 1 {
		indexed = boundaries[len(boundaries)-1]
	}
	// Exponential backoff for one huge unfinished block avoids parsing its entire
	// growing body on every chunk. Closing markers force another attempt.
	next = end + max(int64(streamIndexBatch), end-indexed)
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_stream_page_state(execution_id,indexed_end,next_scan) VALUES(?,?,?) ON CONFLICT(execution_id) DO UPDATE SET indexed_end=excluded.indexed_end,next_scan=excluded.next_scan`, id, indexed, next)
	return err
}
