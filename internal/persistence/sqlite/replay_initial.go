package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// insertInitialReplay uses bounded multi-row inserts for a fresh recorder. The
// caller owns an immediate transaction, so the emptiness check and inserts are
// atomic. Existing conversations retain the normal conflict/retry checks.
func insertInitialReplay(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, changes []models.ConversationChange, canonical bool) ([]models.ConversationChange, bool, error) {
	if owner.ExecutionID == "" || owner.ScopeType != "" || owner.ScopeID != "" || len(changes) == 0 {
		return nil, false, nil
	}
	ids := make(map[string]bool, len(changes))
	mutations := make(map[string]bool, len(changes))
	for _, c := range changes {
		if c.Generation != 0 || c.PreviousRevision != 0 || c.Position < 0 || c.Operation != models.ConversationPut || (c.Lane != models.ConversationProvider && c.Lane != models.ConversationMetadata) || c.ItemID == "" || strings.TrimSpace(c.MutationID) == "" || ids[c.ItemID] || mutations[c.MutationID] {
			return nil, false, nil
		}
		ids[c.ItemID] = true
		mutations[c.MutationID] = true
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_items WHERE execution_id=?)`, owner.ExecutionID).Scan(&exists); err != nil {
		return nil, true, err
	}
	if exists {
		return nil, false, nil
	}
	result, err := insertReplayRows(ctx, tx, owner, changes, canonical)
	return result, true, err
}

func insertReplayRows(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, changes []models.ConversationChange, canonical bool) ([]models.ConversationChange, error) {
	result := append([]models.ConversationChange(nil), changes...)
	for start := 0; start < len(result); {
		// Bound native SQLite statement buffers by bytes as well as
		// row count; a small number of tool results can contain megabytes.
		end, payloadBytes := start, 0
		for end < len(result) && end-start < 64 {
			size := len(result[end].Payload)
			if end > start && payloadBytes+size > 256*1024 {
				break
			}
			payloadBytes += size
			end++
		}
		var query strings.Builder
		query.WriteString(`INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,payload_hash,previous_revision) VALUES `)
		args := make([]any, 0, (end-start)*8)
		for i := start; i < end; i++ {
			c := &result[i]
			if !canonical {
				var compact bytes.Buffer
				if err := json.Compact(&compact, c.Payload); err != nil {
					return nil, err
				}
				c.Payload = compact.Bytes()
			}
			// Canonical recorder bytes are already encoded JSON. The table's
			// CHECK(json_valid(payload)) still rejects invalid external input;
			// do not scan every large payload a second time in Go.
			var hash any
			if c.Lane == models.ConversationProvider {
				digest := sha256.Sum256(c.Payload)
				hash = digest[:]
			}
			if i > start {
				query.WriteByte(',')
			}
			query.WriteString(`(?,0,?,?,?,'put',CAST(? AS TEXT),?,?,?)`)
			args = append(args, owner.ExecutionID, c.Lane, c.ItemID, c.Position, []byte(c.Payload), c.MutationID, hash, c.PreviousRevision)
		}
		inserted, err := tx.ExecContext(ctx, query.String(), args...)
		if err != nil {
			return nil, err
		}
		last, err := inserted.LastInsertId()
		if err != nil {
			return nil, err
		}
		// The immediate transaction excludes other writers. This plain INSERT
		// has neither conflict suppression nor triggers inserting conversation rows,
		// so SQLite assigns consecutive rowids within this statement.
		for i := start; i < end; i++ {
			result[i].Sequence = last - int64(end-1-i)
		}
		start = end
	}

	// Preserve the authoritative reasoning projection maintained by the generic writer.
	for _, c := range result {
		if c.Lane == models.ConversationMetadata && c.ItemID == "replay:000000000000:header" {
			var header struct {
				Reasoning     string `json:"reasoning_content"`
				Authoritative bool   `json:"reasoning_authoritative"`
			}
			if err := json.Unmarshal(c.Payload, &header); err != nil {
				return nil, err
			}
			if header.Authoritative {
				if _, err := tx.ExecContext(ctx, `UPDATE executions SET reasoning_content=? WHERE id=?`, header.Reasoning, owner.ExecutionID); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}

// AppendEncodedReplayChanges accepts immutable canonical JSON produced by the
// Recorder's encoder. Its returned payloads may share those immutable bytes.
func (r *ExecutionRepo) AppendEncodedReplayChanges(ctx context.Context, owner models.ConversationOwner, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	var saved []models.ConversationChange
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var handled bool
		var err error
		saved, handled, err = appendReplayPutBatch(ctx, tx, owner, changes)
		if handled {
			return err
		}
		saved, err = appendConversationChangesEncoded(ctx, tx, owner, changes, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// The recorder normally puts a small changed suffix plus its header/guard.
// Validate all heads in one read and insert the batch once. Removals, legacy
// operations and idempotent retries keep the general mutation path.
func appendReplayPutBatch(ctx context.Context, tx sqlExecutor, owner models.ConversationOwner, changes []models.ConversationChange) ([]models.ConversationChange, bool, error) {
	if owner.ExecutionID == "" || owner.ScopeType != "" || owner.ScopeID != "" || len(changes) == 0 || len(changes) > 128 {
		return nil, false, nil
	}
	ids := make(map[string]int, len(changes))
	mutations := make(map[string]bool, len(changes))
	args := []any{owner.ExecutionID}
	for i, c := range changes {
		if c.Generation != 0 || c.PreviousRevision < 0 || c.Position < 0 || c.Operation != models.ConversationPut || (c.Lane != models.ConversationProvider && c.Lane != models.ConversationMetadata) || c.ItemID == "" || strings.TrimSpace(c.MutationID) == "" {
			return nil, false, nil
		}
		if _, ok := ids[c.ItemID]; ok || mutations[c.MutationID] {
			return nil, false, nil
		}
		ids[c.ItemID] = i
		mutations[c.MutationID] = true
		args = append(args, c.ItemID)
	}
	for _, c := range changes {
		args = append(args, c.MutationID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(changes)), ",")
	rows, err := tx.QueryContext(ctx, `SELECT sequence,lane,item_id,position,mutation_id,is_current FROM conversation_items WHERE execution_id=? AND ((generation=0 AND is_current=1 AND item_id IN (`+placeholders+`)) OR mutation_id IN (`+placeholders+`))`, args...)
	if err != nil {
		return nil, true, err
	}
	found := make([]bool, len(changes))
	remove := false
	fallback := false
	for rows.Next() {
		var seq, pos int64
		var lane models.ConversationLane
		var id, mutation string
		var current bool
		if err = rows.Scan(&seq, &lane, &id, &pos, &mutation, &current); err != nil {
			break
		}
		if mutations[mutation] {
			fallback = true
			break
		}
		i, ok := ids[id]
		if !ok || !current || lane != changes[i].Lane {
			fallback = true
			break
		}
		c := changes[i]
		if c.PreviousRevision != seq || c.Position != pos {
			err = fmt.Errorf("%w: replay item %q changed", ErrConversationConflict, id)
			break
		}
		found[i] = true
		remove = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, true, err
	}
	if fallback {
		return nil, false, nil
	}
	for i, c := range changes {
		if !found[i] && c.PreviousRevision != 0 {
			return nil, true, fmt.Errorf("%w: replay item %q disappeared", ErrConversationConflict, c.ItemID)
		}
	}
	if remove {
		// Deletion materializes any checkpoint references through existing triggers.
		preserve, checkErr := preserveCompletedReplay(ctx, tx, owner, changes)
		if checkErr != nil {
			return nil, true, checkErr
		}
		statement := `DELETE FROM conversation_items WHERE execution_id=? AND generation=0 AND lane IN ('provider','metadata') AND item_id IN (` + placeholders + `)`
		if preserve {
			statement = `UPDATE conversation_items SET is_current=0 WHERE execution_id=? AND generation=0 AND lane IN ('provider','metadata') AND item_id IN (` + placeholders + `)`
		}
		_, err = tx.ExecContext(ctx, statement, args[:len(changes)+1]...)
		if err != nil {
			return nil, true, err
		}
	}
	result, err := insertReplayRows(ctx, tx, owner, changes, true)
	return result, true, err
}
