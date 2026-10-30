package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

const checkpointRangePrefix = "checkpoint:range:"

type checkpointRange struct {
	Prefix      string            `json:"prefix"`
	FirstIndex  int               `json:"first_index"`
	Position    int64             `json:"position"`
	FirstSource int64             `json:"first_source"`
	LastSource  int64             `json:"last_source"`
	Items       []json.RawMessage `json:"items,omitempty"`
}

func compactCheckpointRanges(records []models.ConversationChange, refs map[string]int64) ([]models.ConversationChange, error) {
	result := make([]models.ConversationChange, 0, len(records))
	for i := 0; i < len(records); {
		first := records[i]
		source := refs[first.ItemID]
		sep := strings.LastIndex(first.ItemID, ":")
		index, err := strconv.Atoi(first.ItemID[sep+1:])
		if first.Lane != models.ConversationProvider || source == 0 || err != nil {
			result = append(result, first)
			i++
			continue
		}
		prefix := first.ItemID[:sep+1]
		end := i + 1
		for end < len(records) {
			next := records[end]
			if next.Lane != models.ConversationProvider || refs[next.ItemID] != source+int64(end-i) || next.Position != first.Position+int64(end-i) || next.ItemID != fmt.Sprintf("%s%012d", prefix, index+end-i) {
				break
			}
			end++
		}
		if end-i < 4 {
			result = append(result, first)
			i++
			continue
		}
		descriptor := checkpointRange{Prefix: prefix, FirstIndex: index, Position: first.Position, FirstSource: source, LastSource: source + int64(end-i-1)}
		raw, err := json.Marshal(descriptor)
		if err != nil {
			return nil, err
		}
		result = append(result, models.ConversationChange{ItemID: checkpointRangePrefix + strings.TrimPrefix(first.ItemID, "checkpoint:"), Lane: models.ConversationMetadata, Position: first.Position, Operation: models.ConversationPut, Payload: raw})
		i = end
	}
	return result, nil
}

func registerCheckpointRanges(ctx context.Context, tx sqlExecutor, saved []models.ConversationChange) error {
	for _, record := range saved {
		if record.Operation != models.ConversationPut || !strings.HasPrefix(record.ItemID, checkpointRangePrefix) {
			continue
		}
		var r checkpointRange
		if err := json.Unmarshal(record.Payload, &r); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_checkpoint_ranges(checkpoint_sequence,first_source_sequence,last_source_sequence) VALUES(?,?,?)`, record.Sequence, r.FirstSource, r.LastSource); err != nil {
			return err
		}
	}
	return nil
}

func expandCheckpointRanges(ctx context.Context, tx sqlExecutor, records []models.ConversationChange) ([]models.ConversationChange, error) {
	result := make([]models.ConversationChange, 0, len(records))
	for _, record := range records {
		if !strings.HasPrefix(record.ItemID, checkpointRangePrefix) {
			result = append(result, record)
			continue
		}
		var r checkpointRange
		if err := json.Unmarshal(record.Payload, &r); err != nil {
			return nil, err
		}
		count := r.LastSource - r.FirstSource + 1
		if count < 4 || count > 1000000 || !strings.HasPrefix(r.Prefix, "checkpoint:") || r.FirstIndex < 0 {
			return nil, fmt.Errorf("invalid checkpoint range")
		}
		items := r.Items
		if items == nil {
			rows, err := tx.QueryContext(ctx, `SELECT payload FROM conversation_items WHERE sequence BETWEEN ? AND ? ORDER BY sequence`, r.FirstSource, r.LastSource)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var raw string
				if err = rows.Scan(&raw); err != nil {
					break
				}
				items = append(items, json.RawMessage(raw))
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		if int64(len(items)) != count {
			return nil, fmt.Errorf("checkpoint range source missing")
		}
		for i, raw := range items {
			result = append(result, models.ConversationChange{ItemID: fmt.Sprintf("%s%012d", r.Prefix, r.FirstIndex+i), Lane: models.ConversationProvider, Position: r.Position + int64(i), Operation: models.ConversationPut, Payload: raw})
		}
	}
	return result, nil
}
