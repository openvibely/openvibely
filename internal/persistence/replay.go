package persistence

// ReplayEncodingError distinguishes encoding failures from storage failures.
// Providers may attach their existing error context without wrapping DB errors
// as encoding failures.
type ReplayEncodingError struct{ Err error }

func (e *ReplayEncodingError) Error() string { return e.Err.Error() }
func (e *ReplayEncodingError) Unwrap() error { return e.Err }
