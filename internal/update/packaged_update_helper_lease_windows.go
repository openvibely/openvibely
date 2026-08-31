//go:build windows

package update

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type packagedUpdateHelperLease struct {
	file *os.File
}

func tryAcquirePackagedUpdateHelperLease(path string) (*packagedUpdateHelperLease, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &packagedUpdateHelperLease{file: file}, true, nil
}

func (l *packagedUpdateHelperLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	var overlapped windows.Overlapped
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &overlapped)
	return errors.Join(err, l.file.Close())
}
