//go:build !windows

package update

import "testing"

func killExecutablePlatform(t *testing.T, executables ...string) bool { return false }
