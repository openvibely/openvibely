package attachmentsession

import (
	"testing"
	"time"
)

func TestLockSerializesSameSessionAndCleansUp(t *testing.T) {
	release := Lock("session-1")
	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		releaseSecond := Lock("session-1")
		close(acquired)
		releaseSecond()
		close(done)
	}()

	select {
	case <-acquired:
		t.Fatal("second lock acquired before first release")
	case <-time.After(25 * time.Millisecond):
	}

	release()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second lock did not acquire after first release")
	}

	sessionLocks.Lock()
	defer sessionLocks.Unlock()
	if _, ok := sessionLocks.byID["session-1"]; ok {
		t.Fatalf("session lock entry was not cleaned up: %#v", sessionLocks.byID["session-1"])
	}
}

func TestLockAllowsDifferentSessionsConcurrently(t *testing.T) {
	release := Lock("session-a")
	defer release()

	acquired := make(chan func(), 1)
	go func() {
		acquired <- Lock("session-b")
	}()

	select {
	case releaseOther := <-acquired:
		releaseOther()
	case <-time.After(time.Second):
		t.Fatal("different session lock should not block")
	}
}
