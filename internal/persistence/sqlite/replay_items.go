package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/transcript"
)

// replaceExecutionReplayMessages also serves steering and compaction callers
// inside their existing transaction. It changes individual heads, never deletes
// a complete conversation to replace a serialized snapshot.
func replaceExecutionReplayMessages(ctx context.Context, tx sqlExecutor, id string, messages []models.ExecutionReplayMessage) error {
	desired, err := transcript.ReplayRecords(messages)
	if err != nil {
		return err
	}
	return syncReplayRecords(ctx, tx, models.ConversationOwner{ExecutionID: id}, desired)
}

func syncReplayRecords(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, desired []models.ConversationChange) error {
	return syncConversationRecords(ctx, tx, owner, "replay:", desired, true)
}

func syncConversationRecords(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, prefix string, desired []models.ConversationChange, replayGuard bool) error {
	return syncConversationRecordsWithReferences(ctx, tx, owner, prefix, desired, replayGuard, nil)
}

func syncConversationRecordsWithReferences(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, prefix string, desired []models.ConversationChange, replayGuard bool, references map[string]int64) error {
	where, args, err := conversationOwnerPredicate(owner)
	if err != nil {
		return err
	}
	// Reference identity is sufficient for immutable source revisions. Read
	// placeholders here, not the resolved payloads, so unchanged checkpoints do
	// not load or compare all retained provider text again.
	previousReferences := make(map[string]int64)
	if owner.ExecutionID == "" {
		rows, err := tx.QueryContext(ctx, `SELECT item.item_id,ref.source_sequence FROM conversation_checkpoint_references ref JOIN conversation_items item ON item.sequence=ref.checkpoint_sequence WHERE item.checkpoint_scope_type=? AND item.checkpoint_scope_id=? AND item.generation=0 AND item.is_current=1`, owner.ScopeType, owner.ScopeID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var source int64
			if err := rows.Scan(&id, &source); err != nil {
				rows.Close()
				return err
			}
			previousReferences[id] = source
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+conversationColumns+" FROM conversation_items WHERE "+where+" AND generation=0 AND is_current=1 AND item_id LIKE ?", append(append([]any(nil), args...), prefix+"%")...)
	if err != nil {
		return err
	}
	previous := make(map[string]models.ConversationChange)
	for rows.Next() {
		c, err := scanConversationChange(rows)
		if err != nil {
			rows.Close()
			return err
		}
		previous[c.ItemID] = c
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var changes []models.ConversationChange
	for _, c := range desired {
		old, exists := previous[c.ItemID]
		delete(previous, c.ItemID)
		source := references[c.ItemID]
		if source != 0 {
			if exists && old.Operation == c.Operation && previousReferences[c.ItemID] == source {
				continue
			}
			c.Payload = json.RawMessage(`{}`)
		} else {
			var compact bytes.Buffer
			if err := json.Compact(&compact, c.Payload); err != nil {
				return err
			}
			c.Payload = append(json.RawMessage(nil), compact.Bytes()...)
			if exists && previousReferences[c.ItemID] == 0 && old.Operation == c.Operation && bytes.Equal(old.Payload, c.Payload) {
				continue
			}
		}
		c.PreviousRevision = old.Sequence
		c.MutationID = persistence.NewID()
		changes = append(changes, c)
	}
	for _, old := range previous {
		if old.Operation == models.ConversationRemove {
			continue
		}
		old.PreviousRevision = old.Sequence
		old.Sequence = 0
		old.Operation = models.ConversationRemove
		old.Payload = json.RawMessage(`{}`)
		old.MutationID = persistence.NewID()
		changes = append(changes, old)
	}
	if len(changes) == 0 {
		return nil
	}
	if !replayGuard {
		saved, err := appendConversationChanges(ctx, tx, owner, changes)
		if err != nil {
			return err
		}
		if err := registerCheckpointRanges(ctx, tx, saved); err != nil {
			return err
		}
		for _, c := range saved {
			if source := references[c.ItemID]; source != 0 && c.Operation == models.ConversationPut {
				if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_checkpoint_references(checkpoint_sequence,source_sequence) VALUES(?,?)`, c.Sequence, source); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var guard int64
	guardArgs := append(append([]any(nil), args...), transcript.ReplayGuardID)
	err = tx.QueryRowContext(ctx, "SELECT sequence FROM conversation_items WHERE "+where+" AND generation=0 AND lane='metadata' AND item_id=? AND is_current=1", guardArgs...).Scan(&guard)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	changes = append(changes, models.ConversationChange{ItemID: transcript.ReplayGuardID, Lane: models.ConversationMetadata, Operation: models.ConversationPut, Payload: json.RawMessage(`{}`), PreviousRevision: guard, MutationID: persistence.NewID()})
	_, err = appendConversationChanges(ctx, tx, owner, changes)
	return err
}

func (r *ExecutionRepo) ReplayMessagesByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return r.readReplayMessages(ctx, ids, false, false)
}

// ReplayRowsByExecutionIDs avoids assembling full provider JSON during recovery.
func (r *ExecutionRepo) ReplayRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return r.readReplayMessages(ctx, ids, true, false)
}

// ReplayContinuationRowsByExecutionIDs skips payloads covered by the newest full
// snapshot or linked exchange, using headers in the same read transaction as
// the selected rows. Delta bases are resolved at their immutable saved revision.
// IDs must be chronological. Older execution metadata remains with the caller.
func (r *ExecutionRepo) ReplayContinuationRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return r.readReplayMessages(ctx, ids, true, true)
}

func (r *ExecutionRepo) readReplayMessages(ctx context.Context, ids []string, direct, continuation bool) (map[string][]models.ExecutionReplayMessage, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := readReplayMessagesTx(ctx, tx, ids, continuation, map[string]bool{}, 0)
	if err != nil {
		return nil, err
	}
	if !direct {
		for id, messages := range result {
			for i := range messages {
				messages[i].TranscriptJSON = messages[i].FullTranscriptJSON()
				messages[i].Rows = nil
			}
			result[id] = messages
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func readReplayMessagesTx(ctx context.Context, tx *sql.Tx, ids []string, continuation bool, visiting map[string]bool, revision int64) (map[string][]models.ExecutionReplayMessage, error) {
	var err error
	result := make(map[string][]models.ExecutionReplayMessage)
	placeholders, args := uniqueExecutionIDArgs(ids)
	if len(args) == 0 {
		return result, nil
	}

	predicate := `execution_id IN (` + strings.Join(placeholders, ",") + `) AND generation=0 AND is_current=1 AND operation='put' AND lane IN ('provider','metadata') AND item_id LIKE 'replay:%'`
	if revision > 0 {
		// A base is pinned to a committed revision, not its mutable current heads.
		predicate = strings.Replace(predicate, "is_current=1", `sequence=(SELECT max(prior.sequence) FROM conversation_items prior WHERE prior.execution_id=conversation_items.execution_id AND prior.generation=0 AND prior.item_id=conversation_items.item_id AND prior.sequence<=?)`, 1)
		args = append(append([]any(nil), args...), revision)
	}
	if continuation {
		headerRows, err := tx.QueryContext(ctx, `SELECT execution_id,item_id,payload FROM conversation_items WHERE `+predicate+` AND lane='metadata' AND item_id LIKE '%:header'`, args...)
		if err != nil {
			return nil, err
		}
		positions := make(map[string]int, len(ids))
		for i, id := range ids {
			positions[id] = i
		}
		boundary := -1
		firstFrame := ""
		for headerRows.Next() {
			var id, item string
			var raw []byte
			if err := headerRows.Scan(&id, &item, &raw); err != nil {
				headerRows.Close()
				return nil, err
			}
			var frame transcript.ReplayFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				headerRows.Close()
				return nil, err
			}
			if (models.ReplayReplacesHistory(frame.Format, frame.ItemsKey, frame.Count) || frame.ItemsKey == "responses_delta") && (positions[id] > boundary || (positions[id] == boundary && item > firstFrame)) {
				boundary = positions[id]
				firstFrame = item
			}
		}
		err = headerRows.Err()
		headerRows.Close()
		if err != nil {
			return nil, err
		}
		if boundary >= 0 {
			for _, id := range ids[:boundary] {
				result[id] = []models.ExecutionReplayMessage{}
			}
			selected := ids[boundary:]
			placeholders, args = uniqueExecutionIDArgs(selected)
			predicate = `execution_id IN (` + strings.Join(placeholders, ",") + `) AND generation=0 AND is_current=1 AND operation='put' AND lane IN ('provider','metadata') AND item_id LIKE 'replay:%' AND (execution_id!=? OR item_id>=?)`
			args = append(args, selected[0], firstFrame)
			if revision > 0 {
				predicate = strings.Replace(predicate, "is_current=1", `sequence=(SELECT max(prior.sequence) FROM conversation_items prior WHERE prior.execution_id=conversation_items.execution_id AND prior.generation=0 AND prior.item_id=conversation_items.item_id AND prior.sequence<=?)`, 1)
				// The revision placeholder precedes the frame selector.
				args = append(append(append([]any(nil), args[:len(args)-2]...), revision), args[len(args)-2:]...)
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT execution_id,item_id,lane,payload FROM conversation_items WHERE `+predicate+` ORDER BY execution_id,item_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var structured *models.ExecutionReplayMessage
	var current, prefix string
	var count, expected int
	flush := func() error {
		if structured == nil {
			return nil
		}
		if count != expected {
			return fmt.Errorf("replay frame has %d items, expected %d", count, expected)
		}
		result[current] = append(result[current], *structured)
		return nil
	}
	for rows.Next() {
		var id, itemID, text string
		var raw []byte
		var lane models.ConversationLane
		if err := rows.Scan(&id, &itemID, &lane, &text); err != nil {
			return nil, err
		}
		// SQLite returns an owned Go string. ReplayRows are immutable; retain its
		// storage without allocating a second copy of each provider item.
		raw = unsafe.Slice(unsafe.StringData(text), len(text))
		if lane == models.ConversationMetadata {
			if len(itemID) != 26 || !strings.HasSuffix(itemID, ":header") {
				return nil, fmt.Errorf("invalid replay header identity %q", itemID)
			}
			if _, err := strconv.ParseUint(itemID[7:19], 10, 64); err != nil {
				return nil, fmt.Errorf("invalid replay frame identity %q", itemID)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			var frame transcript.ReplayFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				return nil, err
			}
			if frame.Count < 0 {
				return nil, fmt.Errorf("negative replay item count")
			}
			current, prefix, count, expected = id, itemID[:20], 0, frame.Count
			{
				h := frame.ReplayHeader
				switch h.Format {
				case "array":
				case "envelope":
					if h.ItemsKey == "" {
						return nil, fmt.Errorf("replay envelope missing item key")
					}
				case "none":
					if frame.Count != 0 {
						return nil, fmt.Errorf("text-only replay has items")
					}
				case "opaque":
					if frame.Count != 1 {
						return nil, fmt.Errorf("opaque replay requires one item")
					}
				default:
					return nil, fmt.Errorf("unknown replay format %q", h.Format)
				}
				structured = &models.ExecutionReplayMessage{UserContent: h.UserContent, AssistantContent: h.AssistantContent, ReasoningContent: h.ReasoningContent, Rows: &models.ReplayRows{Format: h.Format, ItemsKey: h.ItemsKey, NullItems: h.NullItems, Envelope: h.Envelope, Items: make([]json.RawMessage, 0, min(frame.Count, 1024))}}
			}
		} else {
			if (structured == nil) || id != current || itemID != fmt.Sprintf("%sitem:%012d", prefix, count) {
				return nil, fmt.Errorf("unexpected replay item %q", itemID)
			}
			structured.Rows.Items = append(structured.Rows.Items, raw)
			count++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := flush(); err != nil {
		return nil, err
	}
	for id, messages := range result {
		for i, m := range messages {
			if m.Rows == nil || m.Rows.ItemsKey != "responses_delta" {
				continue
			}
			if visiting[id] || len(visiting) >= models.MaxReplayDeltaDepth {
				return nil, fmt.Errorf("cyclic or excessive replay dependency at %s", id)
			}
			var bases []string
			if err := json.Unmarshal(m.Rows.Envelope["responses_base_execution_ids"], &bases); err != nil || len(bases) != 1 {
				return nil, fmt.Errorf("invalid replay base for %s", id)
			}
			visiting[id] = true
			var baseRevision int64
			if err := json.Unmarshal(m.Rows.Envelope["responses_base_revision"], &baseRevision); err != nil || baseRevision <= 0 {
				return nil, fmt.Errorf("invalid replay base revision for %s", id)
			}
			var sameTask bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM executions a JOIN executions b ON a.task_id=b.task_id WHERE a.id=? AND b.id=?)`, id, bases[0]).Scan(&sameTask); err != nil {
				return nil, err
			}
			if !sameTask {
				return nil, fmt.Errorf("missing or cross-task replay base %s for %s", bases[0], id)
			}
			previous, err := readReplayMessagesTx(ctx, tx, bases, true, visiting, baseRevision)
			delete(visiting, id)
			if err != nil {
				return nil, err
			}
			base := previous[bases[0]]
			if len(base) != 1 || !base[0].HasResponsesInput() {
				return nil, fmt.Errorf("missing or invalid replay base %s for %s", bases[0], id)
			}
			items, _, err := base[0].ResponsesReplay()
			if err != nil {
				return nil, err
			}
			combined := make([]json.RawMessage, 0, len(items)+len(m.Rows.Items))
			combined = append(combined, items...)
			combined = append(combined, m.Rows.Items...)
			messages[i].Rows = &models.ReplayRows{Format: "envelope", ItemsKey: "responses_input", Items: combined, DeltaDepth: base[0].Rows.DeltaDepth + 1}
		}
		var currentRevision int64
		if revision > 0 {
			currentRevision = revision
		} else if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(sequence),0) FROM conversation_items WHERE execution_id=? AND generation=0 AND lane IN ('provider','metadata') AND item_id LIKE 'replay:%'`, id).Scan(&currentRevision); err != nil {
			return nil, err
		}
		for j := range messages {
			if messages[j].Rows != nil {
				messages[j].Rows.Revision = currentRevision
			}
		}
		result[id] = messages
	}
	return result, nil
}
