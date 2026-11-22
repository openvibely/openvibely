//go:build windows

package update

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsReplacementRetriesTransientLocks(t *testing.T) {
	for _, lock := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION} {
		t.Run(lock.Error(), func(t *testing.T) {
			calls := 0
			err := retryWindowsReplacement(func() error {
				calls++
				if calls < 3 {
					return lock
				}
				return nil
			}, time.Second, time.Millisecond)
			if err != nil || calls != 3 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestWindowsReplacementStopsOnPermanentFailure(t *testing.T) {
	calls := 0
	err := retryWindowsReplacement(func() error { calls++; return windows.ERROR_FILE_NOT_FOUND }, time.Second, time.Millisecond)
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	err = retryWindowsReplacement(func() error { calls++; return windows.ERROR_ACCESS_DENIED }, 0, time.Millisecond)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
