package gitidentity

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Preserve identity settings and include directives for the wrapper's read-only
// identity lookup. Includes must be evaluated in the target repository, not here.
// Never forward these parameters to the actual Git operation.
func environmentIdentityConfig(ctx context.Context, base []string, executable string) (string, error) {
	present := false
	for _, entry := range base {
		if strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") || strings.HasPrefix(entry, "GIT_CONFIG_PARAMETERS=") {
			present = true
		}
	}
	if !present {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, "git", "config", "--no-includes", "--null", "--get-regexp", `^((user|author|committer)\.(name|email)|include\.path|includeif\..*\.path)$`)
	// Read only the environment entries here, without resolving their includes.
	cmd.Dir = filepath.Dir(executable)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("read environment Git identity: %w", err)
	}
	var fields [][2]string
	for _, record := range strings.Split(string(out), "\x00") {
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			continue
		}
		fields = append(fields, [2]string{key, value})
	}
	return configParameters(fields), nil
}
