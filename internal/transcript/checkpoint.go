package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// CheckpointExecution is also the one-time migration DTO for history_json.
// Replay is split into individual records before this execution is persisted.
type CheckpointExecution struct {
	models.Execution
	Replay              []models.ExecutionReplayMessage `json:"replay_messages,omitempty"`
	RetainedUserMessage json.RawMessage                 `json:"retained_user_message,omitempty"`
	Reasoning           string                          `json:"reasoning_content,omitempty"`
}

type checkpointHeader struct {
	HistoryCount int  `json:"history_count"`
	Native       bool `json:"native"`
}

func CheckpointRecords(history []models.Execution, native string) ([]models.ConversationChange, error) {
	header := checkpointHeader{HistoryCount: len(history), Native: strings.TrimSpace(native) != ""}
	raw, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	result := []models.ConversationChange{{ItemID: "checkpoint:header", Lane: models.ConversationMetadata, Operation: models.ConversationPut, Payload: raw}}
	for i, e := range history {
		prefix := fmt.Sprintf("checkpoint:history:%012d:", i)
		replay, err := ReplayRecords(e.ReplayMessages)
		if err != nil {
			return nil, fmt.Errorf("checkpoint history %d: %w", i, err)
		}
		for _, r := range replay {
			r.ItemID = prefix + r.ItemID
			result = append(result, r)
		}
		entry := CheckpointExecution{Execution: e, RetainedUserMessage: e.RetainedUserMessage, Reasoning: e.ReasoningContent}
		raw, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		result = append(result, models.ConversationChange{ItemID: prefix + "execution", Lane: models.ConversationMetadata, Position: int64(i), Operation: models.ConversationPut, Payload: raw})
	}
	if header.Native {
		nativeRecords, err := ReplayRecords([]models.ExecutionReplayMessage{{TranscriptJSON: native}})
		if err != nil {
			return nil, fmt.Errorf("native checkpoint: %w", err)
		}
		for _, r := range nativeRecords {
			r.ItemID = "checkpoint:native:" + r.ItemID
			result = append(result, r)
		}
	}
	return result, nil
}

func RestoreCheckpointRecords(records []models.ConversationChange) ([]models.Execution, string, error) {
	var header checkpointHeader
	found := false
	entries := make(map[string]json.RawMessage)
	groups := make(map[string][]models.ConversationChange)
	for _, r := range records {
		if !strings.HasPrefix(r.ItemID, "checkpoint:") || r.Operation == models.ConversationRemove {
			continue
		}
		if r.Operation != models.ConversationPut {
			return nil, "", fmt.Errorf("invalid checkpoint operation")
		}
		if r.ItemID == "checkpoint:header" {
			if err := json.Unmarshal(r.Payload, &header); err != nil {
				return nil, "", err
			}
			found = true
			continue
		}
		if strings.HasSuffix(r.ItemID, ":execution") {
			entries[r.ItemID] = r.Payload
			continue
		}
		if i := strings.Index(r.ItemID, ReplayRecordPrefix); i >= 0 {
			prefix := r.ItemID[:i]
			r.ItemID = r.ItemID[i:]
			groups[prefix] = append(groups[prefix], r)
		} else {
			return nil, "", fmt.Errorf("unknown checkpoint record %q", r.ItemID)
		}
	}
	if !found {
		if len(entries) != 0 || len(groups) != 0 {
			return nil, "", fmt.Errorf("missing checkpoint header")
		}
		return []models.Execution{}, "", nil // session-only checkpoint
	}
	if header.HistoryCount < 0 || header.HistoryCount != len(entries) {
		return nil, "", fmt.Errorf("checkpoint history count mismatch")
	}
	history := make([]models.Execution, 0, header.HistoryCount)
	for i := 0; i < header.HistoryCount; i++ {
		prefix := fmt.Sprintf("checkpoint:history:%012d:", i)
		raw, ok := entries[prefix+"execution"]
		if !ok {
			return nil, "", fmt.Errorf("missing checkpoint execution %d", i)
		}
		var entry CheckpointExecution
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, "", err
		}
		replay, err := RestoreReplayRecords(groups[prefix])
		if err != nil {
			return nil, "", err
		}
		delete(groups, prefix)
		entry.Execution.ReplayMessages = replay
		entry.Execution.RetainedUserMessage = entry.RetainedUserMessage
		entry.Execution.ReasoningContent = entry.Reasoning
		history = append(history, entry.Execution)
	}
	var native string
	if header.Native {
		replay, err := RestoreReplayRecords(groups["checkpoint:native:"])
		if err != nil {
			return nil, "", err
		}
		if len(replay) != 1 {
			return nil, "", fmt.Errorf("missing native checkpoint frame")
		}
		native = replay[0].FullTranscriptJSON()
		delete(groups, "checkpoint:native:")
	}
	if len(groups) != 0 {
		return nil, "", fmt.Errorf("checkpoint contains unowned replay records")
	}
	return history, native, nil
}
