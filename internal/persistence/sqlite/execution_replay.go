package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ReadOutputAfter avoids rebuilding historical output with group_concat. SQLite
// measures byte lengths and returns only the suffix, excluding reasoning/diffs.
// The read transaction keeps status, lengths and chunks in the same snapshot,
// even when completion folds chunks into executions.output concurrently.
func (r *ExecutionRepo) ReadOutputAfter(ctx context.Context, id string, offset int) (*persistence.ExecutionReplay, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := &persistence.ExecutionReplay{}
	err = tx.QueryRowContext(ctx, `SELECT status, COALESCE(error_message,''),
 length(CAST(COALESCE(output,'') AS BLOB)) + COALESCE((SELECT sum(length(CAST(output AS BLOB))) FROM execution_output_chunks WHERE execution_id=executions.id),0)
 FROM executions WHERE id=?`, id).Scan(&result.Status, &result.ErrorMessage, &result.Total)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset > result.Total {
		if result.Status == models.ExecCompleted || result.Status == models.ExecFailed || result.Status == models.ExecCancelled {
			offset = 0
		} else {
			result.Start = offset
			return result, tx.Commit()
		}
	}
	result.Start = offset
	if offset == result.Total {
		return result, tx.Commit()
	}
	// Include up to three preceding bytes to preserve UTF-8 boundary behavior
	// for a reconnect cursor inside a code point, without reading the prefix.
	readStart := max(0, offset-3)
	rows, err := tx.QueryContext(ctx, `WITH lengths AS (
 SELECT 0 AS seq, length(CAST(COALESCE(output,'') AS BLOB)) AS size FROM executions WHERE id=?
 UNION ALL SELECT id,length(CAST(output AS BLOB)) FROM execution_output_chunks WHERE execution_id=?
 ), positions AS (SELECT seq,size,sum(size) OVER (ORDER BY seq)-size AS start FROM lengths)
 SELECT CASE WHEN seq=0 THEN
 (SELECT substr(CAST(COALESCE(output,'') AS BLOB), max(0,?-start)+1) FROM executions WHERE id=?)
 ELSE (SELECT substr(CAST(output AS BLOB),max(0,?-start)+1) FROM execution_output_chunks WHERE id=seq) END
 FROM positions WHERE start+size>? ORDER BY seq`, id, id, readStart, id, readStart, readStart)
	if err != nil {
		return nil, err
	}
	var data []byte
	for rows.Next() {
		var part []byte
		if err = rows.Scan(&part); err != nil {
			rows.Close()
			return nil, err
		}
		data = append(data, part...)
	}
	err = rows.Err()
	rows.Close()
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

// GetExecutionTaskID authorizes subscriptions without loading transcript blobs.
func (r *ExecutionRepo) GetExecutionTaskID(ctx context.Context, id string) (string, error) {
	var taskID string
	err := r.db.QueryRowContext(ctx, "SELECT task_id FROM executions WHERE id=?", id).Scan(&taskID)
	return taskID, err
}
