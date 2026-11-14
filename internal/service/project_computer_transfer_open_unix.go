//go:build unix && !hurd

package service

import (
	"golang.org/x/sys/unix"
	"os"
)

func openComputerTransferFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
