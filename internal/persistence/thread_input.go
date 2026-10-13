package persistence

import (
	"errors"
)

var (
	ErrNoActiveTurn      = errors.New("no active turn")
	ErrExpectedTurnEmpty = errors.New("expected turn id is required")
	ErrActiveTurnChanged = errors.New("active turn changed")
	ErrInputNotPending   = errors.New("input is no longer pending")
	ErrTaskModelChanged  = errors.New("task model changed before queued input claim")
)

// LocalSteeringCommit records steering inputs consumed by a running execution
// together with the provider conversation history that now includes them.
type LocalSteeringCommit struct {
	ExecutionID string
	InputIDs    []string
	// ResponsesHistory is the provider's native Responses API input items; each
	// item must be JSON-serializable and is replayed verbatim on resume.
	ResponsesHistory []any
}
