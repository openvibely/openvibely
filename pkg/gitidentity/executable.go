package gitidentity

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// realGitExecutable skips OV wrappers inherited from an agent shell. Never
// rediscover a wrapper as its own backend, or trust a caller-supplied override.
func realGitExecutable() (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate, err := exec.LookPath(filepath.Join(dir, "git"))
		if err != nil {
			continue
		}
		f, err := os.Open(candidate)
		if err != nil {
			return "", err
		}
		header, err := io.ReadAll(io.LimitReader(f, 4096))
		f.Close()
		if err != nil {
			return "", err
		}
		// Recognize both older and current POSIX and Windows installations.
		if strings.Contains(string(header), "OV_GIT_REAL_EXECUTABLE") || strings.Contains(string(header), "git-wrapper.ps1") {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("real Git executable not found on PATH (OV wrappers excluded)")
}
