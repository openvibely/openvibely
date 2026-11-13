package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// Negative cursors address raw stream byte boundaries; positive cursors address
// finalized item sequences. -1 means the start of the raw suffix. Both are
// private display cursors, unrelated to the live transport's absolute offset.
func readStreamDisplayTail(ctx context.Context, tx sqlExecutor, id string, before int64, budget int, result *models.DisplayTail) (bool, error) {
	var end, indexed, base int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT end_offset FROM execution_stream_text WHERE execution_id=? ORDER BY end_offset DESC LIMIT 1),0),COALESCE((SELECT indexed_end FROM execution_stream_page_state WHERE execution_id=?),0),COALESCE((SELECT end_bytes FROM execution_display_groups WHERE execution_id=? ORDER BY first_position DESC,first_item DESC LIMIT 1),0)`, id, id, id).Scan(&end, &indexed, &base); err != nil {
		return false, err
	}
	if end == 0 {
		if before < 0 {
			return false, sql.ErrNoRows
		}
		return false, nil
	}
	if before == -1 {
		return false, nil
	}
	if before < 0 {
		limit := -(before + 1)
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_stream_groups WHERE execution_id=? AND start_offset=?) OR EXISTS(SELECT 1 FROM execution_stream_page_state WHERE execution_id=? AND indexed_end=?)`, id, limit, id, limit).Scan(&valid); err != nil {
			return false, err
		}
		if !valid || limit > end {
			return false, sql.ErrNoRows
		}
		end = limit
	}
	start := end
	used := int64(0)
	if end > indexed {
		start = indexed
		used = end - indexed
	}
	rows, err := tx.QueryContext(ctx, `SELECT start_offset,end_offset-start_offset FROM execution_stream_groups WHERE execution_id=? AND start_offset<? ORDER BY start_offset DESC`, id, min(indexed, end))
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var pos, size int64
		if err = rows.Scan(&pos, &size); err != nil {
			break
		}
		if start < end && used+size > int64(budget) {
			break
		}
		start = pos
		used += size
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	if start > 0 || base > 0 {
		result.Before = -(start + 1)
	}
	rows, err = tx.QueryContext(ctx, `SELECT CAST(substr(CAST(text AS BLOB),max(?,end_offset-length(CAST(text AS BLOB)))-(end_offset-length(CAST(text AS BLOB)))+1,min(?,end_offset)-max(?,end_offset-length(CAST(text AS BLOB)))) AS TEXT)
 FROM execution_stream_text WHERE execution_id=? AND end_offset>? AND end_offset<= (SELECT end_offset FROM execution_stream_text WHERE execution_id=? AND end_offset>=? ORDER BY end_offset LIMIT 1) ORDER BY end_offset`, start, end, start, id, start, id, end)
	if err != nil {
		return false, err
	}
	var text strings.Builder
	text.Grow(int(end - start))
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
	result.Text = text.String()
	return true, err
}
