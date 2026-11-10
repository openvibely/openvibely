package gitidentity

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

// Command is for application-owned Git operations. Error initialization is
// carried by exec.Cmd.Err so callers cannot accidentally run without identity.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	path, pathErr := Executable(ctx)
	cmd := exec.CommandContext(ctx, path, args...)
	if runtime.GOOS == "windows" && Configured(ctx) {
		cmd = exec.CommandContext(ctx, "cmd.exe", append([]string{"/d", "/c", path}, args...)...)
	}
	if pathErr != nil {
		cmd.Err = pathErr
	}
	env, err := CommandEnvironment(ctx, os.Environ(), args)
	if err == nil && len(args) > 0 && (args[0] == "commit" || args[0] == "merge" || args[0] == "rebase") {
		err = RequireCommit(ctx)
	}
	if err != nil {
		cmd.Err = err
	}
	cmd.Env = env
	return cmd
}
