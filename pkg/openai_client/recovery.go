package openaiclient

import "encoding/json"

// ConversationError carries the complete failed Completions conversation to
// context recovery, including tool work performed during the current invocation.
type ConversationError struct {
	Err          error
	MessagesJSON string
}

func (e *ConversationError) Error() string { return e.Err.Error() }
func (e *ConversationError) Unwrap() error { return e.Err }

func (c *Client) ConversationError(err error) error {
	if err == nil || len(c.lastCompletionsTranscript) == 0 {
		return err
	}
	raw, marshalErr := json.Marshal(c.completionsHistory)
	if marshalErr != nil {
		return err
	}
	return &ConversationError{Err: err, MessagesJSON: string(raw)}
}
