package openaiclient

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/recovery"
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
	snapshot, marshalErr := recovery.Capture(c.completionsHistory, func(m CompletionsHistoryMessage) ([]byte, error) {
		if raw := m.wireReplay(); raw != nil {
			return raw, nil
		}
		return json.Marshal(m)
	})
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
