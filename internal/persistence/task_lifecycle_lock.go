package persistence

import (
	"context"
	"sync"
)

// taskLifecycleLock serializes in-process user admission with task cancellation.
// Database transactions protect the durable state; this lock also covers the
// task-ID-only process callback and worker queue operations between transactions.
var taskLifecycleLocks = struct {
	sync.Mutex
	entries map[string]*taskLifecycleLockEntry
}{entries: make(map[string]*taskLifecycleLockEntry)}

type taskLifecycleLockEntry struct {
	sync.Mutex
	users int
}

// LockTaskLifecycle returns a release function for the task's admission gate.
// Entries are removed when no caller holds or waits for the lock.
func LockTaskLifecycle(taskID string) func() {
	taskLifecycleLocks.Lock()
	entry := taskLifecycleLocks.entries[taskID]
	if entry == nil {
		entry = &taskLifecycleLockEntry{}
		taskLifecycleLocks.entries[taskID] = entry
	}
	entry.users++
	taskLifecycleLocks.Unlock()

	entry.Lock()
	return func() {
		entry.Unlock()
		taskLifecycleLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(taskLifecycleLocks.entries, taskID)
		}
		taskLifecycleLocks.Unlock()
	}
}

// WithTaskActivationCheck carries the runtime admission check to repositories
// that own the lifecycle lock. The callback must not acquire that lock itself.
func WithTaskActivationCheck(ctx context.Context, check func(string) error) context.Context {
	return context.WithValue(ctx, taskActivationCheckKey{}, check)
}

type taskActivationCheckKey struct{}

// CheckTaskActivation must be called while holding the task lifecycle lock,
// before changing board state or admitting an execution.
func CheckTaskActivation(ctx context.Context, taskID string) error {
	if check, ok := ctx.Value(taskActivationCheckKey{}).(func(string) error); ok {
		return check(taskID)
	}
	return nil
}
