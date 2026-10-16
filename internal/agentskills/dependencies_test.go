package agentskills

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkillDependenciesPrepareOnlyOnExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses shell executable")
	}
	root := t.TempDir()
	exe := filepath.Join(root, "local tool")
	marker := filepath.Join(root, "ran")
	// Paths come only from this test; JSON-quoted shell paths are not used.
	script := "#!/bin/sh\nprintf checked > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\n"
	if err := os.WriteFile(exe, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENVIBELY_TOOL_TEST_TOOL", exe)
	writeSkill(t, root, "skill", `---
kind: openvibely.agent_skill
version: 1
skill:
  key: skill
  enabled: true
dependencies:
  - name: test_tool
    version: development
    environment_variable: TEST_TOOL
    health_check: [version]
  - name: unavailable
    version: local
    health_check: [--version]
---
Use declared executables.
`)
	catalog, err := BuildCatalog("test", root, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tools := range []bool{false, true} {
		var progress []skilldeps.Progress
		ctx := skilldeps.WithProgress(context.Background(), func(p skilldeps.Progress) { progress = append(progress, p) })
		rt := InspectSelectedSkillRuntimeTools(catalog)
		if tools {
			rt = SelectedSkillRuntimeTools(catalog)
		}
		out, handled, isErr, err := rt.Executor(ctx, "skill_view", json.RawMessage(`{"handle":"skill"}`))
		if err != nil || isErr || !handled {
			t.Fatalf("%s %v", out, err)
		}
		if !tools {
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("inspection ran a dependency")
			}
			continue
		}
		var view skillViewResponse
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Dependencies) != 2 || view.Environment["TEST_TOOL"] != exe || view.Dependencies[1].Status != "unsupported" {
			t.Fatalf("%s", out)
		}
		if len(progress) < 2 {
			t.Fatalf("missing progress: %v", progress)
		}
	}
	// Unknown handles must never reach dependency setup.
	out, _, failed, _ := SelectedSkillRuntimeTools(catalog).Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"unknown"}`))
	if !failed || !strings.Contains(out, "not in this turn") {
		t.Fatal(out)
	}
}
