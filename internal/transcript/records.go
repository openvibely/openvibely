package transcript

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

const ReplayRecordPrefix = "replay:"

type ReplayFrame struct {
	ReplayHeader
	Count int `json:"item_count"`
}

func ReplayHeaderID(frame int) string { return fmt.Sprintf("replay:%012d:header", frame) }
func ReplayItemID(frame, index int) string {
	return fmt.Sprintf("replay:%012d:item:%012d", frame, index)
}

// ReplayRecords produces individually addressable records. During migration
// the source is JSON; live recorders supply structured items without marshaling
// the full envelope first.
func ReplayRecords(messages []models.ExecutionReplayMessage) ([]models.ConversationChange, error) {
	var records []models.ConversationChange
	for i, message := range messages {
		header, items, err := SplitReplay(message)
		if err != nil {
			return nil, fmt.Errorf("replay frame %d: %w", i, err)
		}
		frame := ReplayFrame{ReplayHeader: header, Count: len(items)}
		raw, err := json.Marshal(frame)
		if err != nil {
			return nil, err
		}
		records = append(records, models.ConversationChange{Lane: models.ConversationMetadata, ItemID: ReplayHeaderID(i), Position: int64(i), Operation: models.ConversationPut, Payload: raw})
		for j, item := range items {
			records = append(records, models.ConversationChange{Lane: models.ConversationProvider, ItemID: ReplayItemID(i, j), Position: int64(i)<<32 | int64(j), Operation: models.ConversationPut, Payload: item})
		}
	}
	return records, nil
}

func RestoreReplayRecords(records []models.ConversationChange) ([]models.ExecutionReplayMessage, error) {
	return restoreReplayRecords(records, false)
}
func RestoreReplayRows(records []models.ConversationChange) ([]models.ExecutionReplayMessage, error) {
	return restoreReplayRecords(records, true)
}
func restoreReplayRecords(records []models.ConversationChange, retainRows bool) ([]models.ExecutionReplayMessage, error) {
	headers := make(map[int]ReplayFrame)
	items := make(map[string]json.RawMessage)
	for _, record := range records {
		if !strings.HasPrefix(record.ItemID, ReplayRecordPrefix) || record.Operation == models.ConversationRemove {
			continue
		}
		if record.Operation != models.ConversationPut {
			return nil, fmt.Errorf("invalid replay operation %q", record.Operation)
		}
		if record.Lane == models.ConversationMetadata {
			parts := strings.Split(record.ItemID, ":")
			if len(parts) != 3 || parts[2] != "header" {
				return nil, fmt.Errorf("invalid replay header identity %q", record.ItemID)
			}
			index, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, err
			}
			var header ReplayFrame
			if err := json.Unmarshal(record.Payload, &header); err != nil {
				return nil, err
			}
			if header.Count < 0 {
				return nil, fmt.Errorf("negative replay item count")
			}
			headers[index] = header
		} else if record.Lane == models.ConversationProvider {
			items[record.ItemID] = record.Payload
		}
	}
	indices := make([]int, 0, len(headers))
	for i := range headers {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	var messages []models.ExecutionReplayMessage
	for _, i := range indices {
		header := headers[i]
		if header.Count > len(items) {
			return nil, fmt.Errorf("replay frame %d has missing items", i)
		}
		frame := make([]json.RawMessage, 0, header.Count)
		for j := 0; j < header.Count; j++ {
			id := ReplayItemID(i, j)
			raw, ok := items[id]
			if !ok {
				return nil, fmt.Errorf("missing replay item %s", id)
			}
			frame = append(frame, raw)
			delete(items, id)
		}
		message, err := JoinReplayRows(header.ReplayHeader, frame)
		if err == nil && !retainRows {
			message.TranscriptJSON = message.FullTranscriptJSON()
			message.Rows = nil
		}
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if len(items) != 0 {
		return nil, fmt.Errorf("replay items without a header")
	}
	return messages, nil
}
