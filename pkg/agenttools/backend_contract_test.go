package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/pkg/agenttools/tooltest"
	"github.com/openvibely/openvibely/pkg/gitidentity"
	"os/exec"
	"runtime"
	"testing"
)

func TestLocalFileContract(t *testing.T) {
	dir := t.TempDir()
	tooltest.Files(t, func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return Execute(ctx, dir, name, input)
	})
}
func TestLocalProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	m := NewProcessManager(nil)
	defer m.Close()
	tooltest.Processes(t, func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return m.Execute(ctx, dir, name, input)
	})
}

func TestSharedProcessFactoriesKeepCredentialBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	sentinel := errors.New("credentials unavailable")
	ctx := gitidentity.WithResolver(t.Context(), func(context.Context) (gitidentity.Identity, error) { return gitidentity.Identity{}, sentinel })
	local := NewProcessManager(nil)
	defer local.Close()
	if _, err := local.Execute(ctx, t.TempDir(), "shell", json.RawMessage(`{"command":"true"}`)); !errors.Is(err, sentinel) {
		t.Fatalf("local credentials were bypassed: %v", err)
	}
	isolated := NewProcessManager(func(ctx context.Context, command, dir string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", command), nil
	})
	defer isolated.Close()
	if _, err := isolated.Execute(ctx, t.TempDir(), "shell", json.RawMessage(`{"command":"true"}`)); err != nil {
		t.Fatalf("host credentials leaked into isolated factory: %v", err)
	}
}
