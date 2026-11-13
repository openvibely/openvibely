package gitidentity

import (
	"context"
	"errors"
	"os/exec"
)

// Runtime points to the shared wrapper prepared during application startup.
// It contains no credentials and owns no files requiring shutdown cleanup.
type Runtime struct{ path string }
type runtimeKey struct{}

func WithRuntime(ctx context.Context, runtime *Runtime) context.Context {
	return context.WithValue(ctx, runtimeKey{}, runtime)
}

func Executable(ctx context.Context) (string, error) {
	if !Configured(ctx) {
		return exec.LookPath("git")
	}
	runtime, _ := ctx.Value(runtimeKey{}).(*Runtime)
	if runtime == nil || runtime.path == "" {
		return "", errors.New("Git wrapper runtime is not initialized")
	}
	return runtime.path, nil
}
