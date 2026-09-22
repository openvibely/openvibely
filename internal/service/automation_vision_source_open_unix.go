//go:build unix && !hurd

package service

import (
	"os"

	"golang.org/x/sys/unix"
)

func openRootVisionSource(root *os.Root) (*os.File, error) {
	return root.OpenFile(rootVisionSourceName, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
