package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ReadOutputAfter locates a byte cursor from per-item display_bytes (covering
// index, no payloads) and renders only the items overlapping the requested tail.
// The read transaction keeps status, lengths and items in the same snapshot.
func (r *ExecutionRepo) ReadOutputAfter(ctx context.Context, id string, offset int) (*persistence.ExecutionReplay, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := &persistence.ExecutionReplay{}
	var finalizedBytes, pendingBytes int
	err = tx.QueryRowContext(ctx, `SELECT status, COALESCE(error_message,''),
 COALESCE((SELECT sum(display_bytes) FROM conversation_items WHERE execution_id=executions.id AND generation=0 AND lane='display' AND is_current=1 AND operation!='remove'),0), COALESCE((SELECT end_offset FROM execution_stream_text WHERE execution_id=executions.id ORDER BY end_offset DESC LIMIT 1),0)
 FROM executions WHERE id=?`, id).Scan(&result.Status, &result.ErrorMessage, &finalizedBytes, &pendingBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result.Total = finalizedBytes + pendingBytes
	if offset < 0 {
		offset = 0
	}
	if offset > result.Total {
		if result.Status == models.ExecRunning {
			result.Start = offset
			return result, tx.Commit()
		}
		offset = 0
	}
	result.Start = offset
	if offset == result.Total {
		return result, tx.Commit()
	}
	// Up to three preceding bytes let a cursor inside a code point move back to
	// its boundary without reading the prefix.
	readStart := max(0, offset-3)
	data, err := readDisplayBytesFrom(ctx, tx, id, readStart, finalizedBytes)
	if err != nil {
		return nil, err
	}
	start := offset - readStart
	for start > 0 && start < len(data) && !utf8.RuneStart(data[start]) {
		start--
	}
	result.Start = readStart + start
	result.Output = string(data[start:])
	return result, tx.Commit()
}

const displayHeadsCTE = `WITH heads AS (
 SELECT sequence, COALESCE(NULLIF(snapshot_sequence,0),sequence) AS snapshot_sequence, operation, display_bytes, position, item_id, sum(display_bytes) OVER (ORDER BY position, item_id) - display_bytes AS start
 FROM conversation_items WHERE execution_id=? AND generation=0 AND lane='display' AND is_current=1 AND operation!='remove')`

// readDisplayBytesFrom renders output bytes from readStart. Only the item
// containing readStart is cut; within a suffix-free append chain it seeks to the
// first revision ending after the cursor, so a long streaming item is not rebuilt.
func readDisplayBytesFrom(ctx context.Context, tx *sql.Tx, id string, readStart, finalizedBytes int) ([]byte, error) {
	if readStart >= finalizedBytes {
		return readStreamBytesFrom(ctx, tx, id, readStart-finalizedBytes, nil)
	}
	var data []byte
	var head struct {
		sequence, snapshot, start int64
		operation                 models.ConversationOperation
	}
	err := tx.QueryRowContext(ctx, displayHeadsCTE+` SELECT sequence, snapshot_sequence, operation, start FROM heads WHERE start<? AND start+display_bytes>? ORDER BY position, item_id LIMIT 1`, id, readStart, readStart).Scan(&head.sequence, &head.snapshot, &head.operation, &head.start)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		local := int64(readStart) - head.start
		var escape, suffix bool
		if err := tx.QueryRowContext(ctx, `SELECT json_extract(payload,'$.type')='tool_result' AND COALESCE(json_extract(payload,'$.encoded'),0)=0, COALESCE(json_extract(payload,'$.suffix'),'')!='' FROM conversation_items WHERE sequence=?`, head.snapshot).Scan(&escape, &suffix); err != nil {
			return nil, err
		}
		var rows *sql.Rows
		if head.operation == models.ConversationAppend && !suffix {
			text := `COALESCE(json_extract(payload,'$.text'),'')`
			if escape {
				text = `replace(replace(` + text + `,'&','&amp;'),'[','&#91;')`
			}
			rows, err = tx.QueryContext(ctx, `SELECT substr(CAST(piece AS BLOB), max(0, ?-(display_bytes-length(CAST(piece AS BLOB))))+1) FROM (
 SELECT display_bytes, sequence, CASE WHEN sequence=? THEN COALESCE(json_extract(payload,'$.prefix'),'') ELSE '' END || `+text+` AS piece
 FROM conversation_items WHERE execution_id=? AND generation=0 AND lane='display' AND item_id=(SELECT item_id FROM conversation_items WHERE sequence=?) AND display_bytes>? AND sequence>=? AND sequence<=?
 ORDER BY display_bytes, sequence)`, local, head.snapshot, id, head.sequence, local, head.snapshot, head.sequence)
		} else {
			rows, err = tx.QueryContext(ctx, `SELECT substr(CAST(`+displayItemTextSQL+` AS BLOB), ?+1) FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence) WHERE head.sequence=?`, local, head.sequence)
		}
		if err != nil {
			return nil, err
		}
		if data, err = appendBlobRows(data, rows); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, displayHeadsCTE+`
 SELECT CAST(`+displayItemTextSQL+` AS BLOB)
 FROM heads JOIN conversation_items head ON head.sequence=heads.sequence JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence)
 WHERE heads.start>=? AND heads.display_bytes>0 ORDER BY heads.position, heads.item_id`, id, readStart)
	if err != nil {
		return nil, err
	}
	data, err = appendBlobRows(data, rows)
	if err != nil {
		return nil, err
	}
	return readStreamBytesFrom(ctx, tx, id, 0, data)
}

// Pending text has indexed offsets after the finalized prefix. The totals read
// in the same transaction let reconnects skip all finalized-item queries when
// their cursor is already in this raw tail.
func readStreamBytesFrom(ctx context.Context, tx *sql.Tx, id string, local int, data []byte) ([]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT substr(CAST(text AS BLOB),max(0,?-(end_offset-length(CAST(text AS BLOB))))+1)
 FROM execution_stream_text WHERE execution_id=? AND end_offset>? ORDER BY end_offset`, local, id, local)
	if err != nil {
		return nil, err
	}
	return appendBlobRows(data, rows)
}

func appendBlobRows(data []byte, rows *sql.Rows) ([]byte, error) {
	defer rows.Close()
	for rows.Next() {
		var part []byte
		if err := rows.Scan(&part); err != nil {
			return nil, err
		}
		data = append(data, part...)
	}
	return data, rows.Err()
}

// GetExecutionTaskID authorizes subscriptions without loading transcript blobs.
func (r *ExecutionRepo) GetExecutionTaskID(ctx context.Context, id string) (string, error) {
	var taskID string
	err := r.db.QueryRowContext(ctx, "SELECT task_id FROM executions WHERE id=?", id).Scan(&taskID)
	return taskID, err
}
