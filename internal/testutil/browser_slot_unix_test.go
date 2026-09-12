//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package testutil

import (
	"testing"
	"time"
)

func TestBrowserSlotSerializesConcurrentHolders(t *testing.T) {
	first, err := AcquireBrowserSlot()
	if err != nil {
		t.Fatalf("acquire first browser slot: %v", err)
	}

	acquired := make(chan *BrowserSlot, 1)
	errs := make(chan error, 1)
	go func() {
		second, acquireErr := AcquireBrowserSlot()
		if acquireErr != nil {
			errs <- acquireErr
			return
		}
		acquired <- second
	}()

	select {
	case second := <-acquired:
		second.Release()
		first.Release()
		t.Fatal("second holder acquired the browser slot before the first released it")
	case err := <-errs:
		first.Release()
		t.Fatalf("acquire second browser slot: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	first.Release()
	select {
	case second := <-acquired:
		second.Release()
	case err := <-errs:
		t.Fatalf("acquire second browser slot: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("second holder did not acquire the released browser slot")
	}
}
