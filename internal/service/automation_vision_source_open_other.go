//go:build !unix || hurd

package service

import "os"

func openRootVisionSource(root *os.Root) (*os.File, error) {
	return root.Open(rootVisionSourceName)
}
