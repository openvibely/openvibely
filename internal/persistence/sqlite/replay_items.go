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
	return r.readReplayMessages(ctx, ids, false)
}

// ReplayRowsByExecutionIDs avoids assembling full provider JSON during recovery.
func (r *ExecutionRepo) ReplayRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return r.readReplayMessages(ctx, ids, true)
}

func (r *ExecutionRepo) readReplayMessages(ctx context.Context, ids []string, direct bool) (map[string][]models.ExecutionReplayMessage, error) {
	result := make(map[string][]models.ExecutionReplayMessage)
	placeholders, args := uniqueExecutionIDArgs(ids)
	if len(args) == 0 {
		return result, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	predicate := `execution_id IN (` + strings.Join(placeholders, ",") + `) AND generation=0 AND is_current=1 AND operation='put' AND lane IN ('provider','metadata') AND item_id LIKE 'replay:%'`
	sizes := make(map[string]int)
	var rows *sql.Rows
	if !direct {
		rows, err = tx.QueryContext(ctx, `SELECT execution_id,substr(item_id,1,20),sum(length(CAST(payload AS BLOB))+1)+128 FROM conversation_items WHERE `+predicate+` GROUP BY execution_id,substr(item_id,1,20)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, prefix string
			var size int
			if err := rows.Scan(&id, &prefix, &size); err != nil {
				rows.Close()
				return nil, err
			}
			sizes[id+prefix] = size
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT execution_id,item_id,lane,payload FROM conversation_items WHERE `+predicate+` ORDER BY execution_id,item_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var builder *transcript.ReplayBuilder
	var structured *models.ExecutionReplayMessage
	var current, prefix string
	var count, expected int
	flush := func() error {
		if builder == nil && structured == nil {
			return nil
		}
		if count != expected {
			return fmt.Errorf("replay frame has %d items, expected %d", count, expected)
		}
		if structured != nil {
			result[current] = append(result[current], *structured)
			return nil
		}
		message, err := builder.Finish()
		if err != nil {
			return err
		}
		result[current] = append(result[current], message)
		return nil
	}
	for rows.Next() {
		var id, itemID, raw string
		var lane models.ConversationLane
		if err := rows.Scan(&id, &itemID, &lane, &raw); err != nil {
			return nil, err
		}
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
			if err := json.Unmarshal([]byte(raw), &frame); err != nil {
				return nil, err
			}
			if frame.Count < 0 {
				return nil, fmt.Errorf("negative replay item count")
			}
			current, prefix, count, expected = id, itemID[:20], 0, frame.Count
			if direct {
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
			} else {
				builder, err = transcript.NewReplayBuilder(frame.ReplayHeader, sizes[id+prefix])
			}
			if err != nil {
				return nil, err
			}
		} else {
			if (builder == nil && structured == nil) || id != current || itemID != fmt.Sprintf("%sitem:%012d", prefix, count) {
				return nil, fmt.Errorf("unexpected replay item %q", itemID)
			}
			if structured != nil {
				structured.Rows.Items = append(structured.Rows.Items, json.RawMessage(raw))
			} else if err := builder.AppendValidatedJSON(raw); err != nil {
				return nil, err
			}
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
