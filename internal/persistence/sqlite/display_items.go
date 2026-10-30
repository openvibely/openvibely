package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
)

// displayItemTextSQL renders one current display head (aliased head, with its
// snapshot aliased base). displayBytesSQL stores the UTF-8 length of
// this value in display_bytes; both must change together.
const displayItemBodySQL = `CASE WHEN head.snapshot_sequence=0 OR head.snapshot_sequence=head.sequence
 THEN COALESCE(json_extract(base.payload,'$.text'),'')
 ELSE COALESCE((SELECT group_concat(part,'') FROM (
 SELECT json_extract(delta.payload,'$.text') AS part FROM conversation_items delta
 WHERE delta.execution_id=head.execution_id AND delta.generation=head.generation AND delta.lane='display' AND delta.item_id=head.item_id AND delta.sequence>=head.snapshot_sequence AND delta.sequence<=head.sequence ORDER BY delta.sequence)),'') END`

const displayItemTextSQL = `COALESCE(json_extract(base.payload,'$.prefix'),'') ||
 CASE WHEN json_extract(base.payload,'$.type')='tool_result' AND COALESCE(json_extract(base.payload,'$.encoded'),0)=0
 THEN replace(replace((` + displayItemBodySQL + `),'&','&amp;'),'[','&#91;')
 ELSE (` + displayItemBodySQL + `) END || COALESCE(json_extract(base.payload,'$.suffix'),'')`

// Execution reads and text exports project current display items into the output
// string expected by existing callers. Live reconnects use ReadOutputAfter.
const finalizedDisplayOutputSQL = `COALESCE((SELECT group_concat(rendered, '') FROM (
 SELECT head.position, head.item_id, ` + displayItemTextSQL + ` AS rendered
 FROM conversation_items head JOIN conversation_items base ON base.sequence=COALESCE(NULLIF(head.snapshot_sequence,0),head.sequence)
 WHERE head.execution_id=executions.id AND head.generation=0 AND head.lane='display' AND head.is_current=1 AND head.operation!='remove'
 ORDER BY head.position,head.item_id)), '')`

const streamOutputSQL = `COALESCE((SELECT group_concat(text,'') FROM (
 SELECT text FROM execution_stream_text WHERE execution_id=executions.id ORDER BY end_offset)), '')`
const displayOutputSQL = finalizedDisplayOutputSQL + ` || ` + streamOutputSQL

func replaceDisplayOutput(ctx context.Context, tx sqlExecutor, id, output string) error {
	// A digest avoids rebuilding the transcript when FinalizeOutput and Complete
	// receive the same final output. Pending chunks always invalidate this check.
	// Sum256 only reads the slice and does not retain it; output remains alive
	// throughout the call. Avoid copying the complete transcript just to hash it.
	digest := sha256.Sum256(unsafe.Slice(unsafe.StringData(output), len(output)))
	var same bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(display_digest=?,0) AND NOT EXISTS (
 SELECT 1 FROM execution_stream_text WHERE execution_id=executions.id)
 FROM executions WHERE id=?`, digest[:], id).Scan(&same); err != nil {
		return err
	}
	if same {
		return nil
	}
	records, err := transcript.DisplayRecords(transcript.ParseDisplay(output))
	if err != nil {
		return err
	}
	if err := replaceFinalizedDisplay(ctx, tx, id, records); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM execution_stream_text WHERE execution_id=?`, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE executions SET display_digest=?,display_has_plan=? WHERE id=?`, digest[:], strings.Contains(output, "<proposed_plan>"), id)
	return err
}

// AppendOutput persists a coalesced raw text suffix with one guarded statement.
// It does not encode JSON, create revisions, or reconstruct earlier output.
func (r *ExecutionRepo) AppendOutput(ctx context.Context, id, output string) error {
	if !utf8.ValidString(output) {
		output = string([]rune(output))
	}
	if output == "" {
		return nil
	}
	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO execution_stream_text(execution_id,end_offset,text)
 SELECT id,COALESCE((SELECT end_offset FROM execution_stream_text WHERE execution_id=executions.id ORDER BY end_offset DESC LIMIT 1),0)+?,?
 FROM executions WHERE id=? AND status='running'`, len(output), output, id)
	return err
}

func (r *ExecutionRepo) FinalizeOutput(ctx context.Context, id, output string) error {
	return r.replaceRunningOutput(ctx, id, output, "finalizing")
}
func (r *ExecutionRepo) replaceRunningOutput(ctx context.Context, id, output, operation string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var state models.ExecutionStatus
		err := tx.QueryRowContext(ctx, `SELECT status FROM executions WHERE id=?`, id).Scan(&state)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if state != models.ExecRunning {
			return nil
		}
		if err := replaceDisplayOutput(ctx, tx, id, output); err != nil {
			return fmt.Errorf("%s display items: %w", operation, err)
		}
		return nil
	})
}

// Empty completion snapshots preserve any durable output, including cancellation
// and successful completion after steering admission has been checked.
func finalizeDisplayOutput(ctx context.Context, tx sqlExecutor, id, output string) error {
	if output != "" {
		return replaceDisplayOutput(ctx, tx, id, output)
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_stream_text WHERE execution_id=?)`, id).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT `+displayOutputSQL+` FROM executions WHERE id=?`, id).Scan(&output); err != nil {
		return err
	}
	return replaceDisplayOutput(ctx, tx, id, output)
}
