//go:build !windows

package update

import (
	"errors"
	"os"
	"syscall"
)

type packagedUpdateHelperLease struct {
	file *os.File
}

func tryAcquirePackagedUpdateHelperLease(path string) (*packagedUpdateHelperLease, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
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
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return errors.Join(err, l.file.Close())
}
