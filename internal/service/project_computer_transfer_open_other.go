//go:build !unix || hurd

package service

import "os"

func openComputerTransferFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
