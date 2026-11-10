package gitidentity

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Exercise Git's actual parser, including empty resets and shell-sensitive
// characters. Windows uses this representation through its PowerShell wrapper.
func TestWindowsConfigParametersPreserveHeaderResets(t *testing.T) {
	key := "http.https://github.com/acme/team's-repo.git.extraHeader"
	want := "Authorization: Basic actor-token"
	cmd := exec.Command("git", "config", "--get-all", key)
	cmd.Dir = t.TempDir()
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_CONFIG_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_PARAMETERS="+configParameters([][2]string{{key, ""}, {key, want}}))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Git parameter parsing: %v: %s", err, out)
	}
	if strings.ReplaceAll(string(out), "\r\n", "\n") != "\n"+want+"\n" {
		t.Fatalf("header reset or value lost: %q", out)
	}
}
