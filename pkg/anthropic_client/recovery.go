package anthropicclient

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/recovery"
)

// ConversationError retains the actual failed request for bounded recovery.
// It includes completed tool results and steering already removed from the queue.
type ConversationError struct {
	Err          error
	MessagesJSON string // Legacy externally supplied snapshots.
	snapshot     *recovery.Snapshot
}

func (e *ConversationError) Error() string { return e.Err.Error() }
func (e *ConversationError) Unwrap() error { return e.Err }

func conversationError(err error, messages []agenticMessage) error {
	snapshot, marshalErr := recovery.Capture(messages, func(m agenticMessage) ([]byte, error) { return json.Marshal(m) })
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
