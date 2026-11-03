package recovery

import "encoding/json"

// Failure is the common boundary between clients and service recovery. Format
// identifies only the provider's wire shape, not a separate recovery mechanism.
type Failure interface {
	error
	RecoveryRows() ([]json.RawMessage, error)
	RecoveryFormat() string
}

type Error struct {
	Err      error
	Format   string
	Snapshot *Snapshot
}

func (e *Error) Error() string                            { return e.Err.Error() }
func (e *Error) Unwrap() error                            { return e.Err }
func (e *Error) RecoveryRows() ([]json.RawMessage, error) { return e.Snapshot.Rows("") }
func (e *Error) RecoveryFormat() string                   { return e.Format }

func WithSnapshot[T any](err error, format string, items []T, encode func(T) ([]byte, error)) error {
	if err == nil {
		return nil
	}
	snapshot, captureErr := Capture(items, encode)
	if captureErr != nil {
		return err
	}
	return &Error{Err: err, Format: format, Snapshot: snapshot}
}
