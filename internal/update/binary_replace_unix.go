//go:build !windows

package update

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
