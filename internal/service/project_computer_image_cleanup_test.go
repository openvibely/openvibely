package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputerImageCleanupScope(t *testing.T) {
	unused := "openvibely-computer:aaaaaaaaaaaaaaaa"
	used := "openvibely-computer:bbbbbbbbbbbbbbbb"
	override := "openvibely-computer:cccccccccccccccc"
	failed := "openvibely-computer:dddddddddddddddd"
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", override)
	var removed []string
	p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch {
		case args[0] == "image" && args[1] == "ls":
			return []byte(strings.Join([]string{computerImageTag(), unused, used, override, failed, "other:latest", "openvibely-computer:custom", "<none>:<none>"}, "\n")), nil
		case args[0] == "container":
			require.Contains(t, args, "--all")
			if strings.Contains(strings.Join(args, " "), used) {
				return []byte("stopped-container"), nil
			}
			if strings.Contains(strings.Join(args, " "), failed) {
				return nil, errors.New("inspect failed")
			}
			return nil, nil
		case args[0] == "image" && args[1] == "rm":
			require.Len(t, args, 3) // No --force or broad prune.
			removed = append(removed, args[2])
			return nil, nil
		default:
			t.Fatalf("unexpected operation: %v", args)
			return nil, nil
		}
	}}
	require.ErrorContains(t, p.cleanupImages(t.Context()), "inspect failed")
	require.Equal(t, []string{unused}, removed)
}
