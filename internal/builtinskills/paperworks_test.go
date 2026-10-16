package builtinskills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/skilldeps"
)

func TestPaperworksSkillPackages(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"document_pdf", "document_word", "document_spreadsheets", "document_presentations"} {
		data, err := os.ReadFile(filepath.Join(root, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		dependencies, err := skilldeps.Parse(data)
		if err != nil || len(dependencies) != 2 || dependencies[0].Name != "paperworks" || dependencies[1].Name != "libreoffice" {
			t.Fatalf("%s: %v %v", name, dependencies, err)
		}
		if len(dependencies[0].Packages) != 0 {
			t.Fatal("unpublished Paperworks release advertised")
		}
		for _, old := range []string{"document_check.py", "uv run", "pip install", "equation.cjs", "document_native.py", "word_edit.py", "```python", "npm install", "DOCUMENT_JAVA"} {
			if strings.Contains(string(data), old) {
				t.Fatalf("%s retains %s", name, old)
			}
		}
		if strings.Contains(string(data), "examples/") || strings.Contains(string(data), "SKILL_DIR") {
			t.Fatalf("%s references removed examples", name)
		}
		if _, err := os.Stat(filepath.Join(root, "skills", name, "assets")); !os.IsNotExist(err) {
			t.Fatalf("unexpected example assets: %v", err)
		}
		for _, heading := range []string{"## Working files", "## Small authoring example", "## Sources", "## Current Paperworks limits"} {
			if !strings.Contains(string(data), heading) {
				t.Fatalf("%s missing %s", name, heading)
			}
		}
		if name != "document_pdf" && !strings.Contains(string(data), "## Native Google output") {
			t.Fatalf("lost native import guidance: %s", name)
		}
		spec := regexp.MustCompile("(?s)cat > example.json <<'JSON'\n(.*?)\nJSON").FindSubmatch(data)
		if len(spec) != 2 || !json.Valid(spec[1]) {
			t.Fatalf("invalid inline authoring spec: %s", name)
		}

	}
}

// Uses a locally built executable, never downloads or mutates the Paperworks repository.
func TestPaperworksLocalExecutionIntegration(t *testing.T) {
	executable := os.Getenv("OPENVIBELY_TEST_PAPERWORKS")
	office := os.Getenv("OPENVIBELY_TEST_SOFFICE")
	if executable == "" || office == "" {
		t.Skip("set OPENVIBELY_TEST_PAPERWORKS and OPENVIBELY_TEST_SOFFICE")
	}
	t.Setenv("OPENVIBELY_TOOL_PAPERWORKS", executable)
	t.Setenv("OPENVIBELY_TOOL_LIBREOFFICE", office)
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatal(err)
	}
	catalog, err := agentskills.BuildCatalog("paperworks", root, "")
	if err != nil {
		t.Fatal(err)
	}
	out, handled, failed, err := agentskills.SelectedSkillRuntimeTools(catalog).Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"document_pdf"}`))
	if err != nil || failed || !handled {
		t.Fatalf("%s %v", out, err)
	}
	var view struct {
		Environment map[string]string `json:"environment"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if view.Environment["PAPERWORKS"] != executable || view.Environment["DOCUMENT_SOFFICE"] != office {
		t.Fatalf("paths: %v", view.Environment)
	}
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(executable, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "DOCUMENT_SOFFICE="+office)
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("paperworks %v: %s %v", args, data, err)
		}
	}
	run("capabilities")
	checked := map[string]bool{}
	for _, name := range []string{"pdf", "word", "spreadsheets", "presentations"} {
		data, err := os.ReadFile(filepath.Join(root, "skills", "document_"+name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, command := range regexp.MustCompile(`"\$PAPERWORKS" ([a-z-]+)`).FindAllSubmatch(data, -1) {
			name := string(command[1])
			if name == "help" || checked[name] {
				continue
			}
			checked[name] = true
			run("help", name)
		}
	}
	for _, kind := range []struct{ skill, format string }{{"pdf", "pdf"}, {"word", "docx"}, {"spreadsheets", "xlsx"}, {"presentations", "pptx"}} {
		data, err := os.ReadFile(filepath.Join(root, "skills", "document_"+kind.skill, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		match := regexp.MustCompile("(?s)cat > example.json <<'JSON'\n(.*?)\nJSON").FindSubmatch(data)
		if len(match) != 2 {
			t.Fatal("missing inline spec")
		}
		spec := filepath.Join(work, kind.skill+".json")
		if err := os.WriteFile(spec, match[1], 0600); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(work, "example."+kind.format)
		run("create-"+kind.format, "--spec", spec, "--output", output)
		run("inspect", output)
		pages := filepath.Join(work, kind.skill+"-pages")
		run("render", output, "--output", pages)
		entries, err := os.ReadDir(pages)
		if err != nil || len(entries) == 0 {
			t.Fatalf("no rendered %s pages: %v", kind.skill, err)
		}
	}

}
