package service

import (
	"errors"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestChromeProfileReadErrors(t *testing.T) {
	for _, platform := range []string{"darwin", "windows", "linux"} {
		t.Run(platform, func(t *testing.T) {
			for _, cause := range []error{syscall.EACCES, syscall.EPERM} {
				err := chromeProfileReadError(platform, &os.PathError{Op: "open", Path: "profiles", Err: cause})
				if platform == "darwin" {
					require.Contains(t, err.Error(), "Full Disk Access")
				} else {
					require.NotContains(t, err.Error(), "macOS")
					require.Contains(t, err.Error(), "permission")
				}
			}
			require.Contains(t, chromeProfileReadError(platform, os.ErrNotExist).Error(), "No local Chrome profiles")
			cause := errors.New("I/O failure")
			err := chromeProfileReadError(platform, cause)
			require.ErrorIs(t, err, cause)
			require.NotContains(t, err.Error(), "Full Disk Access")
		})
	}
}
func TestChromeProfilesFromIsolatedDirectory(t *testing.T) {
	root := t.TempDir()
	_, err := chromeProfilesAt(root)
	require.ErrorContains(t, err, "No local Chrome profiles")
	for _, name := range []string{"Default", "Profile 2", "Crashpad", "Profile nope"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "Profile 3"), nil, 0600))
	profiles, err := chromeProfilesAt(root)
	require.NoError(t, err)
	require.Equal(t, []string{"Default", "Profile 2"}, profiles)
}
