package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("219_transcript_store.go", upTranscriptStore219, downTranscriptStore219)
}

const conversationItemsSchema217 = `
-- One journal for provider items, display items and their incremental changes.
-- Sequence IDs are durable reconnect cursors, not message positions.
CREATE TABLE conversation_items (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id TEXT REFERENCES executions(id) ON DELETE CASCADE,
    checkpoint_scope_type TEXT,
    checkpoint_scope_id TEXT,
    generation INTEGER NOT NULL CHECK(generation >= 0),
    lane TEXT NOT NULL CHECK(lane IN ('provider', 'display', 'metadata')),
    item_id TEXT NOT NULL,
    position INTEGER NOT NULL CHECK(position >= 0),
    operation TEXT NOT NULL CHECK(operation IN ('put', 'append', 'remove', 'reset')),
    payload TEXT NOT NULL CHECK(json_valid(payload)),
    previous_revision INTEGER NOT NULL DEFAULT 0 CHECK(previous_revision >= 0),
    mutation_id TEXT NOT NULL,
    snapshot_sequence INTEGER NOT NULL DEFAULT 0,
    is_current INTEGER NOT NULL DEFAULT 1 CHECK(is_current IN (0, 1)),
    -- Cumulative rendered UTF-8 length of a display revision (prefix, text so
    -- far, suffix). Writers compute it in appendConversationChanges; reconnects
    -- locate byte cursors from it without rebuilding the transcript.
    display_bytes INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY(checkpoint_scope_type, checkpoint_scope_id)
        REFERENCES chat_compaction_checkpoints(scope_type, scope_id) ON DELETE CASCADE,
    CHECK((execution_id IS NOT NULL AND checkpoint_scope_type IS NULL AND checkpoint_scope_id IS NULL)
       OR (execution_id IS NULL AND checkpoint_scope_type IS NOT NULL AND checkpoint_scope_id IS NOT NULL)),
    CHECK(operation != 'reset' OR (item_id = '' AND lane = 'metadata')),
    CHECK(operation = 'reset' OR item_id != '')
);
CREATE INDEX conversation_execution_changes ON conversation_items(execution_id, lane, sequence);
CREATE INDEX conversation_checkpoint_changes ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, lane, sequence);
CREATE INDEX conversation_execution_item ON conversation_items(execution_id, generation, lane, item_id, sequence);
CREATE INDEX conversation_checkpoint_item ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, generation, lane, item_id, sequence);
CREATE UNIQUE INDEX conversation_execution_mutation ON conversation_items(execution_id, mutation_id) WHERE execution_id IS NOT NULL;
CREATE UNIQUE INDEX conversation_checkpoint_mutation ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, mutation_id) WHERE execution_id IS NULL;

-- Current heads support cold-page reads without grouping the entire journal.
CREATE UNIQUE INDEX conversation_execution_head ON conversation_items(execution_id, generation, lane, item_id) WHERE execution_id IS NOT NULL AND is_current = 1;
CREATE UNIQUE INDEX conversation_checkpoint_head ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, generation, lane, item_id) WHERE execution_id IS NULL AND is_current = 1;
CREATE INDEX conversation_execution_page ON conversation_items(execution_id, generation, lane, position, item_id) WHERE is_current = 1;
CREATE INDEX conversation_checkpoint_page ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, generation, lane, position, item_id) WHERE is_current = 1;

-- Covering index for totals and cursor location: no payload pages are read.
CREATE INDEX conversation_execution_display ON conversation_items(execution_id, generation, position, item_id, operation, snapshot_sequence, display_bytes)
    WHERE lane = 'display' AND is_current = 1;
-- Seeks the first revision of an append chain that ends after a cursor.
CREATE INDEX conversation_execution_display_offset ON conversation_items(execution_id, generation, item_id, display_bytes)
    WHERE lane = 'display';
`

// Must match displayBytesSQL in conversation_repo.go for put revisions.
const displayBytesBackfill217 = `UPDATE conversation_items SET display_bytes = length(CAST(
    COALESCE(json_extract(payload, '$.prefix'), '') ||
    CASE WHEN json_extract(payload, '$.type') = 'tool_result' AND COALESCE(json_extract(payload, '$.encoded'), 0) = 0
        THEN replace(replace(COALESCE(json_extract(payload, '$.text'), ''), '&', '&amp;'), '[', '&#91;')
        ELSE COALESCE(json_extract(payload, '$.text'), '') END ||
    COALESCE(json_extract(payload, '$.suffix'), '') AS BLOB))
WHERE lane = 'display' AND operation = 'put'`

// Goose runs the whole conversion in one transaction: malformed source data
// rolls everything back and leaves the original tables and columns in place.
func upConversationItems217(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, conversationItemsSchema217); err != nil {
		return err
	}
	if err := migrateReplayItems217(ctx, tx); err != nil {
		return err
	}
	if err := migrateCheckpointItems217(ctx, tx); err != nil {
		return err
	}
	if err := migrateDisplayItems217(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, displayBytesBackfill217)
	return err
}

func downConversationItems217(ctx context.Context, tx *sql.Tx) error {
	if err := restoreDisplayOutput217(ctx, tx); err != nil {
		return err
	}
	if err := restoreCheckpoints217(ctx, tx); err != nil {
		return err
	}
	if err := restoreReplayMessages217(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DROP TABLE conversation_items`)
	return err
}

func insertMigratedItem217(ctx context.Context, tx *sql.Tx, executionID, scopeType, scopeID any, r models.ConversationChange) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO conversation_items(execution_id,checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,mutation_id) VALUES(?,?,?,0,?,?,?,'put',?,?)`, executionID, scopeType, scopeID, r.Lane, r.ItemID, r.Position, string(r.Payload), "migration217:"+r.ItemID)
	if err != nil {
		return err
	}
	if r.Lane != models.ConversationDisplay {
		return nil
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversation_items SET snapshot_sequence=sequence WHERE sequence=?`, sequence)
	return err
}

// Process one frame at a time, including multi-frame steering executions.
func migrateReplayItems217(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT execution_id,sequence,user_content,assistant_content,reasoning_content,transcript_json FROM execution_replay_messages ORDER BY execution_id,sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var frame int
		var m models.ExecutionReplayMessage
		if err := rows.Scan(&id, &frame, &m.UserContent, &m.AssistantContent, &m.ReasoningContent, &m.TranscriptJSON); err != nil {
			return err
		}
		records, err := transcript.ReplayRecords([]models.ExecutionReplayMessage{m})
		if err != nil {
			return fmt.Errorf("migrate execution %s frame %d: %w", id, frame, err)
		}
		// Decode the generated representation before deleting its source. This also
		// checks required headers, item counts and provider envelope reconstruction.
		if _, err := transcript.RestoreReplayRecords(records); err != nil {
			return err
		}
		for _, record := range records {
			if record.Lane == models.ConversationMetadata {
				record.ItemID = transcript.ReplayHeaderID(frame)
				record.Position = int64(frame)
			} else {
				record.ItemID = transcript.ReplayItemID(frame, int(record.Position))
				record.Position = int64(frame)<<32 | record.Position
			}
			if err := insertMigratedItem217(ctx, tx, id, nil, nil, record); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DROP TABLE execution_replay_messages`)
	return err
}

func migrateCheckpointItems217(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE chat_compaction_checkpoints ADD COLUMN native_state_exists INTEGER NOT NULL DEFAULT 0 CHECK(native_state_exists IN (0,1))`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT scope_type,scope_id,history_json,provider_state_json FROM chat_compaction_checkpoints`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var scope, id, historyJSON, native string
		if err := rows.Scan(&scope, &id, &historyJSON, &native); err != nil {
			return err
		}
		var old []transcript.CheckpointExecution
		if err := json.Unmarshal([]byte(historyJSON), &old); err != nil {
			return fmt.Errorf("migrate checkpoint %s/%s: %w", scope, id, err)
		}
		history := make([]models.Execution, len(old))
		for i, e := range old {
			history[i] = e.Execution
			history[i].ReplayMessages = e.Replay
			history[i].RetainedUserMessage = e.RetainedUserMessage
			history[i].ReasoningContent = e.Reasoning
		}
		records, err := transcript.CheckpointRecords(history, native)
		if err != nil {
			return fmt.Errorf("migrate checkpoint %s/%s: %w", scope, id, err)
		}
		if _, _, err := transcript.RestoreCheckpointRecords(records); err != nil {
			return err
		}
		for _, r := range records {
			if err := insertMigratedItem217(ctx, tx, nil, scope, id, r); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chat_compaction_checkpoints SET native_state_exists=? WHERE scope_type=? AND scope_id=?`, strings.TrimSpace(native) != "", scope, id); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []string{"history_json", "provider_state_json"} {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE chat_compaction_checkpoints DROP COLUMN `+column); err != nil {
			return err
		}
	}
	return nil
}

func migrateDisplayItems217(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(output,'') || COALESCE((SELECT group_concat(output,'') FROM (SELECT output FROM execution_output_chunks WHERE execution_id=executions.id ORDER BY id)),'') FROM executions WHERE COALESCE(output,'')!='' OR EXISTS(SELECT 1 FROM execution_output_chunks WHERE execution_id=executions.id)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, output string
		if err := rows.Scan(&id, &output); err != nil {
			return err
		}
		records, err := transcript.DisplayRecords(transcript.ParseDisplay(output))
		if err != nil {
			return fmt.Errorf("migrate display %s: %w", id, err)
		}
		for _, r := range records {
			if err := insertMigratedItem217(ctx, tx, id, nil, nil, r); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE execution_output_chunks`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE executions DROP COLUMN output`)
	return err
}

func restoreDisplayOutput217(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE executions ADD COLUMN output TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE execution_output_chunks(id INTEGER PRIMARY KEY AUTOINCREMENT,execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,output TEXT NOT NULL); CREATE INDEX idx_execution_output_chunks_execution ON execution_output_chunks(execution_id,id)`); err != nil {
		return err
	}
	// Finalized puts restore directly; in-progress append chains are
	// reconstructed at their latest snapshot before restoring the output field.
	rows, err := tx.QueryContext(ctx, `SELECT head.execution_id,base.payload,COALESCE((SELECT group_concat(text,'') FROM (SELECT json_extract(delta.payload,'$.text') AS text FROM conversation_items delta WHERE delta.execution_id=head.execution_id AND delta.generation=0 AND delta.lane='display' AND delta.item_id=head.item_id AND delta.sequence>=head.snapshot_sequence AND delta.sequence<=head.sequence ORDER BY delta.sequence)),'') FROM conversation_items head JOIN conversation_items base ON base.sequence=head.snapshot_sequence WHERE head.execution_id IS NOT NULL AND head.generation=0 AND head.lane='display' AND head.is_current=1 AND head.operation!='remove' ORDER BY head.execution_id,head.position,head.item_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var current string
	var items []transcript.DisplayItem
	flush := func() error {
		if current == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE executions SET output=? WHERE id=?`, transcript.DisplayText(items), current)
		items = nil
		return err
	}
	for rows.Next() {
		var id, raw, text string
		if err := rows.Scan(&id, &raw, &text); err != nil {
			return err
		}
		if current != id {
			if err := flush(); err != nil {
				return err
			}
			current = id
		}
		var item transcript.DisplayItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return err
		}
		item.Text = text
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return flush()
}

func restoreCheckpoints217(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{`ALTER TABLE chat_compaction_checkpoints ADD COLUMN history_json TEXT NOT NULL DEFAULT '[]'`, `ALTER TABLE chat_compaction_checkpoints ADD COLUMN provider_state_json TEXT NOT NULL DEFAULT ''`} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT checkpoint_scope_type,checkpoint_scope_id,lane,item_id,position,payload FROM conversation_items WHERE checkpoint_scope_type IS NOT NULL AND generation=0 AND is_current=1 AND operation='put' AND item_id LIKE 'checkpoint:%' ORDER BY checkpoint_scope_type,checkpoint_scope_id,position,item_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var scope, id string
	var records []models.ConversationChange
	flush := func() error {
		if scope == "" {
			return nil
		}
		history, native, err := transcript.RestoreCheckpointRecords(records)
		if err != nil {
			return err
		}
		old := make([]transcript.CheckpointExecution, len(history))
		for i, e := range history {
			old[i] = transcript.CheckpointExecution{Execution: e, Replay: e.ReplayMessages, RetainedUserMessage: e.RetainedUserMessage, Reasoning: e.ReasoningContent}
		}
		raw, err := json.Marshal(old)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_compaction_checkpoints SET history_json=?,provider_state_json=? WHERE scope_type=? AND scope_id=?`, string(raw), native, scope, id)
		records = nil
		return err
	}
	for rows.Next() {
		var nextScope, nextID, raw string
		var r models.ConversationChange
		if err := rows.Scan(&nextScope, &nextID, &r.Lane, &r.ItemID, &r.Position, &raw); err != nil {
			return err
		}
		if scope != nextScope || id != nextID {
			if err := flush(); err != nil {
				return err
			}
			scope = nextScope
			id = nextID
		}
		r.Operation = models.ConversationPut
		r.Payload = json.RawMessage(raw)
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE chat_compaction_checkpoints DROP COLUMN native_state_exists`)
	return err
}

func restoreReplayMessages217(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE execution_replay_messages(execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,sequence INTEGER NOT NULL,user_content TEXT NOT NULL DEFAULT '',assistant_content TEXT NOT NULL DEFAULT '',reasoning_content TEXT NOT NULL DEFAULT '',transcript_json TEXT NOT NULL DEFAULT '',PRIMARY KEY(execution_id,sequence))`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT execution_id,lane,item_id,position,payload FROM conversation_items WHERE execution_id IS NOT NULL AND generation=0 AND is_current=1 AND operation='put' AND lane IN ('provider','metadata') AND item_id LIKE 'replay:%' ORDER BY execution_id,position,item_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var id string
	var records []models.ConversationChange
	flush := func() error {
		if id == "" {
			return nil
		}
		messages, err := transcript.RestoreReplayRecords(records)
		if err != nil {
			return err
		}
		for i, m := range messages {
			if _, err := tx.ExecContext(ctx, `INSERT INTO execution_replay_messages(execution_id,sequence,user_content,assistant_content,reasoning_content,transcript_json) VALUES(?,?,?,?,?,?)`, id, i, m.UserContent, m.AssistantContent, m.ReasoningContent, m.TranscriptJSON); err != nil {
				return err
			}
		}
		records = nil
		return nil
	}
	for rows.Next() {
		var next, raw string
		var c models.ConversationChange
		if err := rows.Scan(&next, &c.Lane, &c.ItemID, &c.Position, &raw); err != nil {
			return err
		}
		if next != id {
			if err := flush(); err != nil {
				return err
			}
			id = next
		}
		c.Operation = models.ConversationPut
		c.Payload = json.RawMessage(raw)
		records = append(records, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return flush()
}
