package agenttools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/executionenv"
)

func TestNativeShellToolAndLegacyAlias(t *testing.T) {
	selected, err := executionenv.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	command := "printf shell-ok"
	if selected.Name == "powershell" {
		command = "Write-Output 'shell-ok'"
	} else if selected.Name == "cmd" {
		command = "echo shell-ok"
	}
	args, _ := json.Marshal(map[string]any{"command": command})
	for _, name := range []string{"shell", "bash", "Bash"} {
		out, err := agenttools.Execute(context.Background(), t.TempDir(), name, args)
		if err != nil || !strings.HasPrefix(out, "shell-ok") || !strings.Contains(out, "exit_code: 0") {
			t.Fatalf("%s: %q %v", name, out, err)
		}
	}
	for _, def := range agenttools.Definitions() {
		if def.Name == "shell" && !strings.Contains(def.Description, "Selected shell: "+selected.DisplayName()) {
			t.Fatal("tool description differs from executor")
		}
	}
}
