package database

import (
	"database/sql"
	"sync"
)

// One worker per writer pool. Repository callers resolve their reader pool to
// this same writer. Requests are hints, not goroutines or queued work items.
type vacuumWorker struct {
	requests chan struct{}
	done     chan struct{}
}

var vacuumWorkers = struct {
	sync.Mutex
	workers map[*sql.DB]*vacuumWorker
}{workers: make(map[*sql.DB]*vacuumWorker)}

// RequestIncrementalVacuum never waits for cleanup. The capacity-one mailbox
// retains at most one recheck during a run or its contention backoff. Requests
// before startup/after shutdown are covered by the periodic fallback.
func RequestIncrementalVacuum(db *sql.DB) {
	vacuumWorkers.Lock()
	defer vacuumWorkers.Unlock()
	if w := vacuumWorkers.workers[db]; w != nil {
		select {
		case w.requests <- struct{}{}:
		default:
		}
	}
}
