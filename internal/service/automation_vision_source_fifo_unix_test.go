//go:build unix && !hurd

package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestOpenRootVisionSourceDoesNotBlockOnFIFO(t *testing.T) {
	repoPath := t.TempDir()
	visionPath := filepath.Join(repoPath, rootVisionSourceName)
	require.NoError(t, unix.Mkfifo(visionPath, 0o600))

	root, err := os.OpenRoot(repoPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	type openResult struct {
		file *os.File
		err  error
	}
	opened := make(chan openResult, 1)
	go func() {
		file, openErr := openRootVisionSource(root)
		opened <- openResult{file: file, err: openErr}
	}()

	select {
	case result := <-opened:
		require.NoError(t, result.err)
		require.NotNil(t, result.file)
		t.Cleanup(func() { require.NoError(t, result.file.Close()) })

		info, statErr := result.file.Stat()
		require.NoError(t, statErr)
		require.NotZero(t, info.Mode()&os.ModeNamedPipe)
	case <-time.After(time.Second):
		// Release a blocking reader so a failing implementation does not leak a
		// goroutine or hold the temporary directory open after this assertion.
		writer, writerErr := os.OpenFile(visionPath, os.O_WRONLY, 0)
		if writerErr == nil {
			require.NoError(t, writer.Close())
		}
		result := <-opened
		if result.file != nil {
			require.NoError(t, result.file.Close())
		}
		t.Fatal("opening the vision source FIFO blocked")
	}
}
