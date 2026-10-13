package persistence

import (
	"errors"
)

const (
	// LifecycleExecutionPageDefaultLimit is the number of rows fetched when the
	// lifecycle tab does not provide a page size.
	LifecycleExecutionPageDefaultLimit = 20
	// LifecycleExecutionPageMaxLimit prevents a caller from turning the compact
	// lifecycle endpoint back into an unbounded detail payload.
	LifecycleExecutionPageMaxLimit = 50
)

// ErrLifecycleExecutionCursor reports a malformed or direction/task-mismatched
// lifecycle page cursor.
var ErrLifecycleExecutionCursor = errors.New("invalid lifecycle execution cursor")

type LifecycleTaskRunFreshness struct {
	Stale           bool
	TaskID          string
	SourceRunID     string
	SourceStartedAt string
	SourceRowID     int64
	LatestRunID     string
	LatestStartedAt string
	LatestRowID     int64
}
