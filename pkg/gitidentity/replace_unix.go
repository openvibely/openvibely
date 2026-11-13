//go:build !windows

package gitidentity

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
