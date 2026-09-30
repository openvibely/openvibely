package anthropicclient

import "encoding/json"

// ConversationError retains the actual failed request for bounded recovery.
// It includes completed tool results and steering already removed from the queue.
type ConversationError struct {
	Err          error
	MessagesJSON string
}

func (e *ConversationError) Error() string { return e.Err.Error() }
func (e *ConversationError) Unwrap() error { return e.Err }

func conversationError(err error, messages []agenticMessage) error {
	raw, marshalErr := json.Marshal(messages)
	if marshalErr != nil {
		return err
	}
	return &ConversationError{Err: err, MessagesJSON: string(raw)}
}
