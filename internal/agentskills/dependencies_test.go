package agentskills

import (
	"context"
	"encoding/json"
	"fmt"
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
    version: 0.0.0-dev
    environment_variable: TEST_TOOL
    health_check: [version]
  - name: unavailable
    version: 0.0.0-local
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
			ctx = skilldeps.WithApproval(ctx, "test", func(context.Context, skilldeps.InstallationRequest) error { return nil })
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
		resolvedExe := exe
		if len(view.Dependencies) != 2 || view.Environment["TEST_TOOL"] != resolvedExe || view.Dependencies[1].Status != "unsupported" {
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

func TestSkillViewMissingDependenciesWithholdsInstructionsUntilAnswer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, SkillsDir, "missing")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`---
kind: openvibely.agent_skill
version: 1
skill:
  key: missing
  enabled: true
dependencies:
  - name: uninstalled-skill-view-fixture
    version: 0.0.0-test-missing
    health_check: [--version]
    packages:
      %s/%s:
        url: https://example.invalid/fixture
        sha256: "%s"
        format: raw
        executable: fixture
---
PRIVATE_SKILL_INSTRUCTIONS
`, runtime.GOOS, runtime.GOARCH, strings.Repeat("0", 64))
	t.Setenv(skilldeps.OverrideVariable("uninstalled-skill-view-fixture"), "")
	writeSkill(t, root, "missing", body)
	if err := os.WriteFile(filepath.Join(dir, "scripts", "setup.sh"), []byte("PRIVATE_SUPPORT_INSTRUCTIONS"), 0644); err != nil {
		t.Fatal(err)
	}
	catalog, err := BuildCatalog("test", root, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"handle":"missing"}`, `{"handle":"missing","file_path":"scripts/setup.sh"}`} {
		ctx := skilldeps.WithApproval(context.Background(), "test", func(ctx context.Context, _ skilldeps.InstallationRequest) error {
			if string(skilldeps.SkillViewArguments(ctx)) != input {
				t.Fatalf("lost original tool arguments: %s", skilldeps.SkillViewArguments(ctx))
			}
			return skilldeps.ErrApprovalRequired
		})
		out, handled, failed, err := SelectedSkillRuntimeTools(catalog).Executor(ctx, "skill_view", json.RawMessage(input))
		if err != nil || !handled || !failed || !strings.Contains(out, "installation") || strings.Contains(out, "PRIVATE_") {
			t.Fatalf("missing-dependency loader did not wait: %s %v", out, err)
		}
	}
	out, _, failed, err := InspectSelectedSkillRuntimeTools(catalog).Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"missing"}`))
	if err != nil || failed || !strings.Contains(out, "PRIVATE_SKILL_INSTRUCTIONS") {
		t.Fatalf("inspection unexpectedly prepared dependencies: %s %v", out, err)
	}
	for _, action := range []string{"disabled", "removed"} {
		for _, input := range []string{`{"handle":"missing"}`, `{"handle":"missing","file_path":"scripts/setup.sh"}`} {
			t.Run(action+"/"+input, func(t *testing.T) {
				mainFile := filepath.Join(dir, SkillFile)
				if err := os.WriteFile(mainFile, []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
				asked := false
				ctx := skilldeps.WithApproval(context.Background(), "test", func(context.Context, skilldeps.InstallationRequest) error {
					asked = true
					if action == "disabled" {
						if err := os.WriteFile(mainFile, []byte(strings.Replace(body, "enabled: true", "enabled: false", 1)), 0644); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Remove(mainFile); err != nil {
						t.Fatal(err)
					}
					return nil // Human clicks Yes after access changes.
				})
				ctx = skilldeps.WithProgress(ctx, func(p skilldeps.Progress) {
					t.Errorf("dependency setup continued after skill became %s: %+v", action, p)
				})
				out, handled, failed, err := SelectedSkillRuntimeTools(catalog).Executor(ctx, "skill_view", json.RawMessage(input))
				const want = "skill_view could not read this skill because access is no longer available. Continue without this skill."
				if !asked || err != nil || !handled || !failed || out != want {
					t.Fatalf("revoked skill returned %q handled=%t failed=%t asked=%t err=%v", out, handled, failed, asked, err)
				}
			})
		}
	}
}
