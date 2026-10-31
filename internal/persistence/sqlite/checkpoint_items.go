package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/transcript"
)

func (r *ExecutionRepo) UpsertChatCompactionCheckpoint(ctx context.Context, c models.ChatCompactionCheckpoint) error {
	if r == nil || r.db == nil {
		return nil
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error { return upsertChatCompactionCheckpoint(ctx, tx, c) })
}

func upsertChatCompactionCheckpoint(ctx context.Context, tx sqlExecutor, c models.ChatCompactionCheckpoint) error {
	// Serialized writes preserve the digest/storage contract for row-backed reads.
	if c.ProviderStateJSON == "" && c.ProviderStateRows != nil {
		c.ProviderStateJSON = c.NativeState().FullTranscriptJSON()
	}
	// Hash the boundary DTO without splitting/copying every provider item. A
	// summary-only update can retain exactly the already committed history.
	digest, err := checkpointHistoryDigest(c)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO chat_compaction_checkpoints(scope_type,scope_id,model_config_id,compatibility_key,source_execution_id,summary,strategy,native_state_exists,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,datetime('now'),datetime('now'))
 ON CONFLICT(scope_type,scope_id) DO UPDATE SET
 provider_session_state_json=CASE WHEN chat_compaction_checkpoints.compatibility_key=excluded.compatibility_key AND (chat_compaction_checkpoints.model_config_id='' OR excluded.model_config_id='' OR chat_compaction_checkpoints.model_config_id=excluded.model_config_id) THEN chat_compaction_checkpoints.provider_session_state_json ELSE '' END,
 model_config_id=excluded.model_config_id,compatibility_key=excluded.compatibility_key,source_execution_id=excluded.source_execution_id,summary=excluded.summary,strategy=excluded.strategy,native_state_exists=excluded.native_state_exists,updated_at=datetime('now')`, c.ScopeType, c.ScopeID, c.ModelConfigID, c.CompatibilityKey, c.SourceExecutionID, c.Summary, c.Strategy, strings.TrimSpace(c.ProviderStateJSON) != "")
	if err != nil {
		return fmt.Errorf("upsert compaction boundary: %w", err)
	}
	var same bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(history_digest=?,0) FROM chat_compaction_checkpoints WHERE scope_type=? AND scope_id=?`, digest, c.ScopeType, c.ScopeID).Scan(&same); err != nil {
		return err
	}
	if same {
		return nil
	}
	records, err := transcript.CheckpointRecords(c.History, c.ProviderStateJSON)
	if err != nil {
		return err
	}
	references, err := checkpointReferences(ctx, tx, c, records)
	if err != nil {
		return err
	}
	records, err = compactCheckpointRanges(records, references)
	if err != nil {
		return err
	}
	if err := syncConversationRecordsWithReferences(ctx, tx, models.ConversationOwner{ScopeType: c.ScopeType, ScopeID: c.ScopeID}, "checkpoint:", records, false, references); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE chat_compaction_checkpoints SET history_digest=? WHERE scope_type=? AND scope_id=?`, digest, c.ScopeType, c.ScopeID)
	return err
}

func checkpointHistoryDigest(c models.ChatCompactionCheckpoint) ([]byte, error) {
	h := sha256.New()
	encoder := json.NewEncoder(h)
	if err := encoder.Encode(len(c.History)); err != nil {
		return nil, err
	}
	for _, execution := range c.History {
		entry := transcript.CheckpointExecution{Execution: execution, Replay: execution.ReplayMessages, RetainedUserMessage: execution.RetainedUserMessage, Reasoning: execution.ReasoningContent}
		if err := encoder.Encode(entry); err != nil {
			return nil, err
		}
	}
	if err := encoder.Encode(c.ProviderStateJSON); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func (r *ExecutionRepo) GetChatCompactionCheckpoint(ctx context.Context, scopeType, scopeID string) (*models.ChatCompactionCheckpoint, error) {
	if r == nil || r.db == nil || strings.TrimSpace(scopeType) == "" || strings.TrimSpace(scopeID) == "" {
		return nil, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var c models.ChatCompactionCheckpoint
	err = tx.QueryRowContext(ctx, `SELECT scope_type,scope_id,model_config_id,compatibility_key,source_execution_id,summary,strategy,provider_session_state_json,created_at,updated_at FROM chat_compaction_checkpoints WHERE scope_type=? AND scope_id=?`, scopeType, scopeID).Scan(&c.ScopeType, &c.ScopeID, &c.ModelConfigID, &c.CompatibilityKey, &c.SourceExecutionID, &c.Summary, &c.Strategy, &c.ProviderSessionStateJSON, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+conversationColumns+` FROM conversation_resolved_items WHERE checkpoint_scope_type=? AND checkpoint_scope_id=? AND generation=0 AND is_current=1 AND operation='put' AND item_id LIKE 'checkpoint:%' ORDER BY position,item_id`, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	var records []models.ConversationChange
	for rows.Next() {
		r, err := scanConversationChange(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	records, err = expandCheckpointRanges(ctx, tx, records)
	if err != nil {
		return nil, err
	}
	if persistence.CheckpointRows(ctx) {
		var native models.ExecutionReplayMessage
		c.History, native, err = transcript.RestoreCheckpointRows(records)
		c.ProviderStateJSON, c.ProviderStateRows = native.TranscriptJSON, native.Rows
	} else {
		c.History, c.ProviderStateJSON, err = transcript.RestoreCheckpointRecords(records)
	}
	if err != nil {
		return nil, fmt.Errorf("restore compaction checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Session settings have a separate lifetime. Only an incompatible native
// checkpoint is cleared; a local summary survives session-setting updates.
func (r *ExecutionRepo) UpsertChatProviderSessionState(ctx context.Context, c models.ChatCompactionCheckpoint) error {
	if r == nil || r.db == nil {
		return nil
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var native bool
		var key, model string
		err := tx.QueryRowContext(ctx, `SELECT native_state_exists,compatibility_key,model_config_id FROM chat_compaction_checkpoints WHERE scope_type=? AND scope_id=?`, c.ScopeType, c.ScopeID).Scan(&native, &key, &model)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		clear := native && (key != c.CompatibilityKey || (model != "" && c.ModelConfigID != "" && model != c.ModelConfigID))
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_compaction_checkpoints(scope_type,scope_id,model_config_id,compatibility_key,provider_session_state_json,created_at,updated_at) VALUES(?,?,?,?,?,datetime('now'),datetime('now')) ON CONFLICT(scope_type,scope_id) DO UPDATE SET model_config_id=excluded.model_config_id,compatibility_key=excluded.compatibility_key,provider_session_state_json=excluded.provider_session_state_json,updated_at=datetime('now')`, c.ScopeType, c.ScopeID, c.ModelConfigID, c.CompatibilityKey, c.ProviderSessionStateJSON)
		if err != nil {
			return err
		}
		if clear {
			if _, err := tx.ExecContext(ctx, `UPDATE chat_compaction_checkpoints SET source_execution_id='',summary='',strategy='',native_state_exists=0 WHERE scope_type=? AND scope_id=?`, c.ScopeType, c.ScopeID); err != nil {
				return err
			}
			records, err := transcript.CheckpointRecords(nil, "")
			if err != nil {
				return err
			}
			return syncConversationRecords(ctx, tx, models.ConversationOwner{ScopeType: c.ScopeType, ScopeID: c.ScopeID}, "checkpoint:", records, false)
		}
		return nil
	})
}

// Match exact native payloads, including retained subranges and reordered items.
// Synthetic summaries or transformed provider items remain owned by the checkpoint.
func checkpointReferences(ctx context.Context, tx sqlExecutor, checkpoint models.ChatCompactionCheckpoint, records []models.ConversationChange) (map[string]int64, error) {
	ids := make([]string, 0, len(checkpoint.History)+1)
	for _, execution := range checkpoint.History {
		if execution.ID != "" {
			ids = append(ids, execution.ID)
		}
	}
	if checkpoint.SourceExecutionID != "" {
		ids = append(ids, checkpoint.SourceExecutionID)
	}
	placeholders, args := uniqueExecutionIDArgs(ids)
	if len(args) == 0 {
		return nil, nil
	}
	// Compact once and collect only hashes of the retained records. Large
	// executions are never loaded just to locate a small retained suffix.
	payloads := make(map[string][]string)
	hashes := make([]any, 0, len(records))
	for _, record := range records {
		if record.Lane != models.ConversationProvider {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, record.Payload); err != nil {
			return nil, err
		}
		payload := compact.String()
		if _, exists := payloads[payload]; !exists {
			digest := sha256.Sum256(compact.Bytes())
			hashes = append(hashes, digest[:])
		}
		payloads[payload] = append(payloads[payload], record.ItemID)
	}
	refs := make(map[string]int64)
	for start := 0; start < len(hashes); start += 128 {
		batch := hashes[start:min(start+128, len(hashes))]
		hashMarks := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		queryArgs := append(append([]any(nil), args...), batch...)
		rows, err := tx.QueryContext(ctx, `SELECT sequence,payload FROM conversation_items WHERE execution_id IN (`+strings.Join(placeholders, ",")+`) AND generation=0 AND lane='provider' AND is_current=1 AND operation='put' AND payload_hash IN (`+hashMarks+`) AND item_id LIKE 'replay:%'`, queryArgs...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sequence int64
			var payload string
			if err := rows.Scan(&sequence, &payload); err != nil {
				rows.Close()
				return nil, err
			}
			// Hashes narrow the search; exact equality decides whether sharing is safe.
			for _, itemID := range payloads[payload] {
				refs[itemID] = sequence
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return refs, nil
}
