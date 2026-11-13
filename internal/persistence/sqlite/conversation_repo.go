package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/displayindex"
)

var ErrConversationConflict = persistence.ErrConversationConflict

var _ persistence.ConversationStore = (*ExecutionRepo)(nil)

// conversationOwnerPredicate uses only static SQL identifiers. Ownership is
// checked by foreign keys as well as by the HTTP/task caller's authorization.
func conversationOwnerPredicate(owner models.ConversationOwner) (string, []any, error) {
	if owner.ExecutionID != "" && owner.ScopeType == "" && owner.ScopeID == "" {
		return "execution_id = ?", []any{owner.ExecutionID}, nil
	}
	if owner.ExecutionID == "" && owner.ScopeType != "" && owner.ScopeID != "" {
		return "checkpoint_scope_type = ? AND checkpoint_scope_id = ?", []any{owner.ScopeType, owner.ScopeID}, nil
	}
	return "", nil, errors.New("conversation requires exactly one execution or checkpoint owner")
}

// Checkpoint readers resolve borrowed immutable execution payloads. Execution
// reads stay on the base table and do not pay for the reference joins.
func conversationReadTable(owner models.ConversationOwner) string {
	if owner.ExecutionID != "" {
		return "conversation_items"
	}
	return "conversation_resolved_items"
}

// displayBytesSQL is the rendered UTF-8 length of a display revision, matching
// displayItemTextSQL: a put measures prefix+text+suffix; an append adds its
// text (escaped like its snapshot) to the previous revision's length. It is
// evaluated in the INSERT ... SELECT of appendConversationChanges (?5 lane,
// ?8 operation, ?9 payload, ?10 previous revision, ?12 snapshot).
const displayBytesSQL = `CASE WHEN ?5!='display' THEN 0 WHEN ?8='put' THEN length(CAST(
 COALESCE(json_extract(?9,'$.prefix'),'') ||
 CASE WHEN json_extract(?9,'$.type')='tool_result' AND COALESCE(json_extract(?9,'$.encoded'),0)=0
 THEN replace(replace(COALESCE(json_extract(?9,'$.text'),''),'&','&amp;'),'[','&#91;') ELSE COALESCE(json_extract(?9,'$.text'),'') END ||
 COALESCE(json_extract(?9,'$.suffix'),'') AS BLOB))
 WHEN ?8='append' THEN COALESCE((SELECT display_bytes FROM conversation_items WHERE sequence=?10),0) + length(CAST(
 CASE WHEN (SELECT json_extract(payload,'$.type')='tool_result' AND COALESCE(json_extract(payload,'$.encoded'),0)=0 FROM conversation_items WHERE sequence=?12)
 THEN replace(replace(COALESCE(json_extract(?9,'$.text'),''),'&','&amp;'),'[','&#91;') ELSE COALESCE(json_extract(?9,'$.text'),'') END AS BLOB))
 ELSE 0 END`

const conversationColumns = "sequence, generation, lane, item_id, position, operation, payload, previous_revision, mutation_id"

func scanConversationChange(row interface{ Scan(...any) error }) (models.ConversationChange, error) {
	var c models.ConversationChange
	var raw string
	err := row.Scan(&c.Sequence, &c.Generation, &c.Lane, &c.ItemID, &c.Position, &c.Operation, &raw, &c.PreviousRevision, &c.MutationID)
	c.Payload = json.RawMessage(raw)
	return c, err
}

// AppendConversationChanges commits a batch atomically. Callers accepting queued
// inputs or replacing compaction checkpoints use appendConversationChanges in
// their existing immediate transaction, preserving the same acceptance boundary.
func (r *ExecutionRepo) AppendConversationChanges(ctx context.Context, owner models.ConversationOwner, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	var result []models.ConversationChange
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var err error
		result, err = appendConversationChanges(ctx, tx, owner, changes)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func appendConversationChanges(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	return appendConversationChangesEncoded(ctx, tx, owner, changes, false)
}

func appendConversationChangesEncoded(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, changes []models.ConversationChange, canonical bool) ([]models.ConversationChange, error) {
	where, args, err := conversationOwnerPredicate(owner)
	if err != nil {
		return nil, err
	}
	if result, handled, err := insertInitialReplay(ctx, tx, owner, changes, canonical); handled {
		return result, err
	}
	result := make([]models.ConversationChange, 0, len(changes))
	for _, change := range changes {
		if strings.TrimSpace(change.MutationID) == "" || (!canonical && !json.Valid(change.Payload)) || change.Generation < 0 || change.Position < 0 || change.PreviousRevision < 0 {
			return nil, errors.New("invalid conversation mutation")
		}
		if change.Lane != models.ConversationProvider && change.Lane != models.ConversationDisplay && change.Lane != models.ConversationMetadata {
			return nil, errors.New("invalid conversation lane")
		}
		if change.Operation != models.ConversationPut && change.Operation != models.ConversationAppend && change.Operation != models.ConversationRemove && change.Operation != models.ConversationReset {
			return nil, errors.New("invalid conversation operation")
		}
		if change.Operation == models.ConversationReset {
			if change.Lane != models.ConversationMetadata || change.ItemID != "" {
				return nil, errors.New("reset must be a metadata boundary")
			}
		} else if change.ItemID == "" {
			return nil, errors.New("conversation item identity required")
		}
		if !canonical {
			var compact bytes.Buffer
			if err := json.Compact(&compact, change.Payload); err != nil {
				return nil, err
			}
			change.Payload = compact.Bytes()
		}
		existing, e := scanConversationChange(tx.QueryRowContext(ctx, "SELECT "+conversationColumns+" FROM "+conversationReadTable(owner)+" WHERE "+where+" AND mutation_id = ?", append(append([]any(nil), args...), change.MutationID)...))
		if e == nil {
			if existing.Generation != change.Generation || existing.Lane != change.Lane || existing.ItemID != change.ItemID || existing.Position != change.Position || existing.Operation != change.Operation || existing.PreviousRevision != change.PreviousRevision || !bytes.Equal(existing.Payload, change.Payload) {
				return nil, fmt.Errorf("%w: mutation identity reused with different content", ErrConversationConflict)
			}
			result = append(result, existing)
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		var revision int64
		var position int64
		var operation models.ConversationOperation
		var snapshot int64
		itemArgs := append(append([]any(nil), args...), change.Generation, change.Lane, change.ItemID)
		e = tx.QueryRowContext(ctx, "SELECT sequence, position, operation, snapshot_sequence FROM conversation_items WHERE "+where+" AND generation = ? AND lane = ? AND item_id = ? AND is_current = 1", itemArgs...).Scan(&revision, &position, &operation, &snapshot)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		if revision != 0 && snapshot == 0 {
			snapshot = revision
		}
		if revision != change.PreviousRevision {
			return nil, fmt.Errorf("%w: item %q expected %d, found %d", ErrConversationConflict, change.ItemID, change.PreviousRevision, revision)
		}
		if revision != 0 && position != change.Position {
			return nil, fmt.Errorf("%w: item position changed", ErrConversationConflict)
		}
		if change.Operation == models.ConversationAppend && (revision == 0 || operation == models.ConversationRemove) {
			return nil, errors.New("cannot append without a live item")
		}
		if change.Operation == models.ConversationRemove && (revision == 0 || operation == models.ConversationRemove) {
			return nil, errors.New("cannot remove absent item")
		}
		if change.Operation == models.ConversationAppend {
			if change.Lane != models.ConversationDisplay {
				return nil, errors.New("append requires a display item")
			}
			if _, err := conversationTextDelta(change.Payload); err != nil {
				return nil, err
			}
			var textType sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT json_type(payload, '$.text') FROM conversation_items WHERE sequence = ?`, snapshot).Scan(&textType); err != nil {
				return nil, err
			}
			if textType.String != "text" {
				return nil, errors.New("cannot append to an item without string text")
			}
		} else {
			snapshot = 0 // A put/remove/reset starts a new reconstruction boundary.
		}
		if revision != 0 && change.Operation == models.ConversationAppend {
			if _, err := tx.ExecContext(ctx, `UPDATE conversation_items SET is_current = 0 WHERE sequence = ?`, revision); err != nil {
				return nil, err
			}
		} else if revision != 0 {
			// A put/remove/reset is a complete snapshot, so earlier revisions are
			// unreachable for reconstruction. Cursor readers past them get this row.
			// A retry of a deleted mutation then reports a revision conflict.
			if _, err := tx.ExecContext(ctx, "DELETE FROM conversation_items WHERE "+where+" AND generation = ? AND lane = ? AND item_id = ? AND sequence <= ?", append(itemArgs, revision)...); err != nil {
				return nil, err
			}
		}
		var executionID, scopeType, scopeID any
		if owner.ExecutionID != "" {
			executionID = owner.ExecutionID
		} else {
			scopeType = owner.ScopeType
			scopeID = owner.ScopeID
		}
		var payloadHash any
		if owner.ExecutionID != "" && change.Lane == models.ConversationProvider && change.Operation == models.ConversationPut {
			digest := sha256.Sum256(change.Payload)
			payloadHash = digest[:]
		}
		inserted, e := tx.ExecContext(ctx, `INSERT INTO conversation_items(execution_id,checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,previous_revision,mutation_id,snapshot_sequence,display_bytes,payload_hash) SELECT ?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,`+displayBytesSQL+`,?13`, executionID, scopeType, scopeID, change.Generation, change.Lane, change.ItemID, change.Position, change.Operation, string(change.Payload), change.PreviousRevision, change.MutationID, snapshot, payloadHash)
		if e != nil {
			return nil, e
		}
		change.Sequence, e = inserted.LastInsertId()
		if e != nil {
			return nil, e
		}
		if snapshot == 0 && change.Lane == models.ConversationDisplay {
			if _, err := tx.ExecContext(ctx, `UPDATE conversation_items SET snapshot_sequence = sequence WHERE sequence = ?`, change.Sequence); err != nil {
				return nil, err
			}
		}
		if owner.ExecutionID != "" && change.Lane == models.ConversationMetadata && change.ItemID == "replay:000000000000:header" && change.Operation == models.ConversationPut {
			var header struct {
				Reasoning     string `json:"reasoning_content"`
				Authoritative bool   `json:"reasoning_authoritative"`
			}
			if err := json.Unmarshal(change.Payload, &header); err != nil {
				return nil, err
			}
			if header.Authoritative {
				if _, err := tx.ExecContext(ctx, `UPDATE executions SET reasoning_content=? WHERE id=?`, header.Reasoning, owner.ExecutionID); err != nil {
					return nil, err
				}
			}
		}
		result = append(result, change)
	}
	if owner.ExecutionID != "" {
		var first *models.ConversationChange
		for i := range changes {
			c := &changes[i]
			if c.Lane == models.ConversationDisplay && c.Generation == 0 && (first == nil || c.Position < first.Position || (c.Position == first.Position && c.ItemID < first.ItemID)) {
				first = c
			}
		}
		if first != nil {
			if err := displayindex.Refresh(ctx, tx, owner.ExecutionID, first.Position, first.ItemID); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// ReadConversationChanges is lane-specific by design: display consumers cannot
// accidentally retrieve raw provider/system/compaction payloads. The sequence
// index seeks directly past the durable cursor. Global sequence gaps are valid.
func (r *ExecutionRepo) ReadConversationChanges(ctx context.Context, owner models.ConversationOwner, lane models.ConversationLane, after int64, limit int) ([]models.ConversationChange, error) {
	where, args, err := conversationOwnerPredicate(owner)
	if err != nil {
		return nil, err
	}
	if lane != models.ConversationProvider && lane != models.ConversationDisplay && lane != models.ConversationMetadata {
		return nil, errors.New("invalid conversation lane")
	}
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid conversation cursor or page size")
	}
	args = append(args, lane, after, limit)
	rows, err := r.db.QueryContext(ctx, "SELECT "+conversationColumns+" FROM "+conversationReadTable(owner)+" WHERE "+where+" AND lane = ? AND sequence > ? ORDER BY sequence LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.ConversationChange, 0, min(limit, 64))
	for rows.Next() {
		c, err := scanConversationChange(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// Appends have one deliberately narrow meaning: extend a display item's text.
// Provider objects (including mutable parallel tool results) use complete item
// replacements, preserving their native structure without interpreting it here.
func conversationTextDelta(raw json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 1 {
		return "", errors.New("display append requires only a text field")
	}
	var text string
	v, ok := fields["text"]
	if !ok || string(v) == "null" || json.Unmarshal(v, &text) != nil {
		return "", errors.New("display append text must be a string")
	}
	return text, nil
}

// ReadConversationPage reads current item heads using the position index. A
// finalized item's put is read directly; only unfinished text needs its deltas.
// afterPosition=-1 starts a page. afterID disambiguates equal positions.
func (r *ExecutionRepo) ReadConversationPage(ctx context.Context, owner models.ConversationOwner, lane models.ConversationLane, generation, afterPosition int64, afterID string, limit int) (models.ConversationPage, error) {
	var page models.ConversationPage
	where, args, err := conversationOwnerPredicate(owner)
	if err != nil {
		return page, err
	}
	if lane != models.ConversationProvider && lane != models.ConversationDisplay && lane != models.ConversationMetadata {
		return page, errors.New("invalid conversation lane")
	}
	if generation < 0 || afterPosition < -1 || limit < 1 || limit > 1000 {
		return page, errors.New("invalid conversation page")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM conversation_items WHERE "+where+" AND lane = ?", append(append([]any(nil), args...), lane)...).Scan(&page.Cursor); err != nil {
		return page, err
	}
	queryArgs := append(append([]any(nil), args...), generation, lane, afterPosition, afterID, limit)
	rows, err := tx.QueryContext(ctx, "SELECT "+conversationColumns+", snapshot_sequence FROM "+conversationReadTable(owner)+" WHERE "+where+" AND generation = ? AND lane = ? AND is_current = 1 AND (position, item_id) > (?, ?) AND operation NOT IN ('remove','reset') ORDER BY position, item_id LIMIT ?", queryArgs...)
	if err != nil {
		return page, err
	}
	type head struct {
		change   models.ConversationChange
		snapshot int64
	}
	heads := make([]head, 0, min(limit, 64))
	for rows.Next() {
		var h head
		var raw string
		c := &h.change
		if err := rows.Scan(&c.Sequence, &c.Generation, &c.Lane, &c.ItemID, &c.Position, &c.Operation, &raw, &c.PreviousRevision, &c.MutationID, &h.snapshot); err != nil {
			rows.Close()
			return page, err
		}
		c.Payload = json.RawMessage(raw)
		heads = append(heads, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	page.Items = make([]models.ConversationItem, 0, len(heads))
	for _, h := range heads {
		payload := h.change.Payload
		if h.change.Operation == models.ConversationAppend {
			payload, err = restoreConversationText(ctx, tx, owner, h.change, h.snapshot)
			if err != nil {
				return models.ConversationPage{}, err
			}
		}
		page.Items = append(page.Items, models.ConversationItem{ID: h.change.ItemID, Position: h.change.Position, Revision: h.change.Sequence, Payload: payload})
	}
	if owner.ExecutionID != "" && lane == models.ConversationDisplay && generation == 0 && len(page.Items) < limit {
		var position, revision int64
		var text string
		err := tx.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT max(position)+1 FROM conversation_items WHERE execution_id=? AND generation=0 AND lane='display' AND is_current=1),0),
 COALESCE(max(end_offset),0), COALESCE(group_concat(text,''),'') FROM
 (SELECT end_offset,text FROM execution_stream_text WHERE execution_id=? ORDER BY end_offset)`, owner.ExecutionID, owner.ExecutionID).Scan(&position, &revision, &text)
		if err != nil {
			return page, err
		}
		const pendingID = "display:pending"
		if revision > 0 && (position > afterPosition || position == afterPosition && pendingID > afterID) {
			payload, err := json.Marshal(struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Complete bool   `json:"complete"`
			}{"text", text, false})
			if err != nil {
				return page, err
			}
			page.Items = append(page.Items, models.ConversationItem{ID: pendingID, Position: position, Revision: revision, Payload: payload})
		}
	}
	if err := tx.Commit(); err != nil {
		return models.ConversationPage{}, err
	}
	return page, nil
}

func restoreConversationText(ctx context.Context, tx *sql.Tx, owner models.ConversationOwner, head models.ConversationChange, snapshot int64) (json.RawMessage, error) {
	where, args, err := conversationOwnerPredicate(owner)
	if err != nil {
		return nil, err
	}
	args = append(args, head.Generation, head.Lane, head.ItemID, snapshot, head.Sequence)
	rows, err := tx.QueryContext(ctx, "SELECT operation,payload FROM conversation_items WHERE "+where+" AND generation=? AND lane=? AND item_id=? AND sequence>=? AND sequence<=? ORDER BY sequence", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fields map[string]json.RawMessage
	var text strings.Builder
	first := true
	for rows.Next() {
		var op models.ConversationOperation
		var raw string
		if err := rows.Scan(&op, &raw); err != nil {
			return nil, err
		}
		if first {
			if op != models.ConversationPut {
				return nil, errors.New("missing display item snapshot")
			}
			if err := json.Unmarshal([]byte(raw), &fields); err != nil {
				return nil, err
			}
			var initial string
			if v, ok := fields["text"]; !ok || string(v) == "null" || json.Unmarshal(v, &initial) != nil {
				return nil, errors.New("display snapshot text must be a string")
			}
			text.WriteString(initial)
			first = false
			continue
		}
		if op != models.ConversationAppend {
			return nil, errors.New("unexpected display operation after snapshot")
		}
		delta, err := conversationTextDelta(json.RawMessage(raw))
		if err != nil {
			return nil, err
		}
		text.WriteString(delta)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if first {
		return nil, errors.New("missing display item")
	}
	fields["text"], err = json.Marshal(text.String())
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// ReadConversationHeads includes tombstones so a restarted writer can safely
// recreate an item at the same position using its last committed revision.
// This is an internal writer API, not a public display endpoint.
func (r *ExecutionRepo) ReadConversationHeads(ctx context.Context, owner models.ConversationOwner, lane models.ConversationLane, generation int64) ([]models.ConversationChange, error) {
	where, args, err := conversationOwnerPredicate(owner)
	if err != nil {
		return nil, err
	}
	if lane != "" && lane != models.ConversationProvider && lane != models.ConversationMetadata {
		return nil, errors.New("writer heads require provider or metadata lane")
	}
	if generation < 0 {
		return nil, errors.New("invalid conversation generation")
	}
	args = append(args, generation)
	laneFilter := " AND lane IN ('provider','metadata')"
	if lane != "" {
		laneFilter = " AND lane=?"
		args = append(args, lane)
	}
	rows, err := r.db.QueryContext(ctx, "SELECT "+conversationColumns+" FROM "+conversationReadTable(owner)+" WHERE "+where+" AND generation=?"+laneFilter+" AND is_current=1 ORDER BY position,item_id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.ConversationChange
	for rows.Next() {
		c, err := scanConversationChange(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) ConversationRevision(ctx context.Context, owner models.ConversationOwner, lane models.ConversationLane, itemID string, generation int64) (int64, error) {
	where, args, err := conversationOwnerPredicate(owner)
	if err != nil {
		return 0, err
	}
	args = append(args, generation, lane, itemID)
	var revision int64
	err = r.db.QueryRowContext(ctx, "SELECT sequence FROM conversation_items WHERE "+where+" AND generation=? AND lane=? AND item_id=? AND is_current=1", args...).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}
