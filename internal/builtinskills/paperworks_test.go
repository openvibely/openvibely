package builtinskills

import (
	"context"
	"encoding/json"
	"io/fs"
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
		paperworks := dependencies[0]
		if paperworks.Version != "0.0.0-rc7" || paperworks.EnvironmentVariable != "PAPERWORKS" || strings.Join(paperworks.HealthCheck, " ") != "version" {
			t.Fatalf("%s: invalid Paperworks release configuration: %+v", name, paperworks)
		}
		platforms := []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"}
		if len(paperworks.Packages) != len(platforms) {
			t.Fatalf("%s: missing release platforms", name)
		}
		for _, platform := range platforms {
			pkg := paperworks.Packages[platform]
			executable := "paperworks"
			if strings.HasPrefix(platform, "windows/") {
				executable += ".exe"
			}
			url := "https://github.com/openvibely/openvibely-paperworks/releases/download/v" + paperworks.Version + "/paperworks-" + strings.ReplaceAll(platform, "/", "-") + ".zip"
			if pkg.URL != url || pkg.Format != "zip" || pkg.Executable != executable || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(pkg.SHA256) {
				t.Fatalf("%s: invalid %s release package: %+v", name, platform, pkg)
			}
		}
		data = documentSkillText(t, root, name)
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
		data := documentSkillText(t, root, "document_"+name)
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
		data := documentSkillText(t, root, "document_"+kind.skill)
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
		exerciseDocumentEditExample(t, executable, office, work, kind.skill, kind.format, data)
		exerciseDocumentCreationOptions(t, executable, office, work, kind.skill, data)
		exerciseGeneralDocumentEditExample(t, executable, office, work, kind.skill, data)
	}

}

func TestDocumentReferencesAvailableThroughSkillView(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatal(err)
	}
	catalog, err := agentskills.BuildCatalog("document-references", root, "")
	if err != nil {
		t.Fatal(err)
	}
	runtime := agentskills.SkillRuntimeTools(catalog, root, "", nil)
	files, err := fs.Glob(FS, "builtin/skills/document_*/references/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("bundled references: %v %v", files, err)
	}
	for _, file := range files {
		parts := strings.Split(file, "/")
		item := struct{ handle, file string }{parts[2], strings.Join(parts[3:], "/")}

		t.Run(item.handle+"/"+item.file, func(t *testing.T) {
			call := func(file string) string {
				t.Helper()
				args, err := json.Marshal(map[string]string{"handle": item.handle, "file_path": file})
				if err != nil {
					t.Fatal(err)
				}
				out, handled, failed, err := runtime.Executor(context.Background(), "skill_view", args)
				if err != nil || !handled || failed {
					t.Fatalf("skill_view: %s %v", out, err)
				}
				return out
			}
			var skill struct {
				LinkedFiles map[string][]string `json:"linked_files"`
			}
			if err := json.Unmarshal([]byte(call("")), &skill); err != nil {
				t.Fatal(err)
			}
			listed := false
			for _, name := range skill.LinkedFiles["references"] {
				listed = listed || name == strings.TrimPrefix(item.file, "references/")
			}
			if !listed {
				t.Fatalf("reference not discoverable: %v", skill.LinkedFiles)
			}
			var support struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(call(item.file)), &support); err != nil {
				t.Fatal(err)
			}
			bundled, err := FS.ReadFile("builtin/skills/" + item.handle + "/" + item.file)
			if err != nil {
				t.Fatal(err)
			}
			if len(bundled) == 0 || support.Content != string(bundled) {
				t.Fatal("reference body was not delivered")
			}
			// Existing installs must receive updated bundled references too.
			if err := os.WriteFile(filepath.Join(root, "skills", item.handle, item.file), []byte("stale instructions"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := SyncTo(root); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(call(item.file)), &support); err != nil {
				t.Fatal(err)
			}
			if support.Content != string(bundled) {
				t.Fatal("reference not refreshed on sync")
			}
		})
	}
}

// Collect the test recipes by following the same reference paths a model receives.
// Runtime loading remains selective; only the integration tests load every recipe.
func documentSkillText(t *testing.T, root, handle string) []byte {
	t.Helper()
	catalog, err := agentskills.BuildCatalog("document-recipes", root, "")
	if err != nil {
		t.Fatal(err)
	}
	runtime := agentskills.SkillRuntimeTools(catalog, root, "", nil)
	data, err := os.ReadFile(filepath.Join(root, "skills", handle, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	all := string(data)
	pending := []string{""}
	seen := map[string]bool{}
	var advertised []string
	references := regexp.MustCompile(`references/[a-z0-9-]+\.md`)
	for len(pending) > 0 {
		file := pending[0]
		pending = pending[1:]
		if seen[file] {
			continue
		}
		seen[file] = true
		args, err := json.Marshal(map[string]string{"handle": handle, "file_path": file})
		if err != nil {
			t.Fatal(err)
		}
		out, handled, failed, err := runtime.Executor(context.Background(), "skill_view", args)
		if err != nil || !handled || failed {
			t.Fatalf("%s/%s: %s %v", handle, file, out, err)
		}
		var view struct {
			Body        string              `json:"body"`
			Content     string              `json:"content"`
			LinkedFiles map[string][]string `json:"linked_files"`
		}
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		content := view.Content
		if file == "" {
			content = view.Body
			advertised = view.LinkedFiles["references"]
		} else {
			all += "\n" + content
		}
		if strings.TrimSpace(content) == "" {
			t.Fatalf("empty instructions: %s/%s", handle, file)
		}
		pending = append(pending, references.FindAllString(content, -1)...)
	}
	for _, file := range advertised {
		if !seen["references/"+file] {
			t.Fatalf("reference has no instruction route: %s/%s", handle, file)
		}
	}
	return []byte(all)
}
