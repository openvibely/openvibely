//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package testutil

// BrowserSlot is a no-op on platforms where the hosted browser suite is not run.
type BrowserSlot struct{}

func AcquireBrowserSlot() (*BrowserSlot, error) { return &BrowserSlot{}, nil }

func (slot *BrowserSlot) Release() {}
