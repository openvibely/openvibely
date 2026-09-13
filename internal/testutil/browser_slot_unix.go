//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package testutil

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// BrowserSlot serializes resource-heavy browser fixtures across concurrently
// running Go test package processes. It can also isolate latency-sensitive
// tests from browser startup and rendering work.
type BrowserSlot struct {
	file *os.File
}

// AcquireBrowserSlot waits for exclusive access to the shared browser-test slot.
// Advisory file locking makes the slot self-releasing if a test process exits.
func AcquireBrowserSlot() (*BrowserSlot, error) {
	path := filepath.Join(os.TempDir(), "openvibely-browser-tests.lock")
	return acquireBrowserSlot(path)
}

func acquireBrowserSlot(path string) (*BrowserSlot, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &BrowserSlot{file: file}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Release relinquishes the shared slot.
func (slot *BrowserSlot) Release() {
	if slot == nil || slot.file == nil {
		return
	}
	_ = syscall.Flock(int(slot.file.Fd()), syscall.LOCK_UN)
	_ = slot.file.Close()
	slot.file = nil
}
