//go:build windows

package update

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func atomicReplace(source, destination string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	// Windows can retain executable mappings briefly after process exit; scanners
	// can also hold handles that deny replacement. Keep the source intact and retry
	// the atomic move, with a bounded deadline for permanent permission failures.
	return retryWindowsReplacement(func() error {
		return windows.MoveFileEx(sourcePath, destinationPath, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	}, 10*time.Second, 100*time.Millisecond)
}

func retryWindowsReplacement(move func() error, window, delay time.Duration) error {
	deadline := time.Now().Add(window)
	for {
		err := move()
		if err == nil || (!errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION)) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(min(delay, time.Until(deadline)))
	}
}
