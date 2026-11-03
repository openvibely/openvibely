package openaiclient

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/recovery"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
)

// ConversationError carries the complete failed Completions conversation to
// context recovery, including tool work performed during the current invocation.
type ConversationError struct {
	Err          error
	MessagesJSON string // Legacy externally supplied snapshots.
	snapshot     *recovery.Snapshot
}

func (e *ConversationError) Error() string { return e.Err.Error() }
func (e *ConversationError) Unwrap() error { return e.Err }

func (c *Client) ConversationError(err error) error {
	if err == nil || len(c.lastCompletionsTranscript) == 0 {
		return err
	}
	snapshot, marshalErr := recovery.Capture(c.completionsHistory, marshalCompletionsSnapshot)
	if marshalErr != nil {
		return err
	}
	return &ConversationError{Err: err, snapshot: snapshot}
}

// RecoveryRows exposes immutable rows without a whole-history JSON round trip.
func (e *ConversationError) RecoveryRows() ([]json.RawMessage, error) {
	return e.snapshot.Rows(e.MessagesJSON)
}
func (e *ConversationError) JSON() string { return e.snapshot.JSON(e.MessagesJSON) }

func marshalCompletionsSnapshot(m CompletionsHistoryMessage) ([]byte, error) {
	if raw := m.wireReplay(); raw != nil {
		return raw, nil
	}
	if m.rawContent != nil && m.Content == "" && len(m.ImageContent) == 0 {
		fields := map[string]any{"role": m.Role, "content": m.contentValue()}
		if m.ReasoningContent != "" {
			fields["reasoning_content"] = m.ReasoningContent
		}
		if len(m.ToolCalls) > 0 {
			fields["tool_calls"] = m.ToolCalls
		}
		if m.ToolCallID != "" {
			fields["tool_call_id"] = m.ToolCallID
		}
		return requestjson.MarshalValue(fields)
	}
	return json.Marshal(m)
}

// SnapshotRows preserves immutable prepared content without decoding tool results.
func (h PreparedCompletionsHistory) SnapshotRows() ([]json.RawMessage, error) {
	snapshot, err := recovery.Capture(h.messages, marshalCompletionsSnapshot)
	if err != nil {
		return nil, err
	}
	return snapshot.Rows("")
}

func (e *ConversationError) RecoveryFormat() string { return "" }
