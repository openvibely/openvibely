package agentskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderSelectedSkillsMarkdownDeduplicatesAndSkipsUnknown(t *testing.T) {
	catalog := NewCatalog("turn", []Entry{
		{Handle: "one", Skill: "one", Source: SourceProject},
		{Handle: "two", Skill: "two", Source: SourceGlobal},
	})

	got := RenderSelectedSkillsMarkdown(catalog, []string{" one ", "missing", "one", "", "two"})
	if strings.Count(got, "`one`") != 1 {
		t.Fatalf("expected one to render once, got:\n%s", got)
	}
	if !strings.Contains(got, "`two` (global)") || !strings.Contains(got, "`one` (project)") {
		t.Fatalf("expected selected skills with scopes, got:\n%s", got)
	}
	if strings.Contains(got, "missing") {
		t.Fatalf("unknown handle should be skipped, got:\n%s", got)
	}
}

func TestRenderSelectedSkillsMarkdownAgentOwnedAndEmpty(t *testing.T) {
	if got := RenderSelectedSkillsMarkdown(nil, []string{"x"}); got != "" {
		t.Fatalf("nil catalog should render empty string, got %q", got)
	}
	if got := RenderSelectedSkillsMarkdown(NewCatalog("turn", nil), []string{"x"}); got != "" {
		t.Fatalf("no selected known handles should render empty string, got %q", got)
	}

	catalog := NewCatalog("turn", []Entry{{Handle: "review", Skill: "review", Source: SourceAgent, AgentKey: "reviewer"}})
	got := RenderSelectedSkillsMarkdown(catalog, []string{"review"})
	if !strings.Contains(got, "assigned-agent skills") || !strings.Contains(got, "`review` (agent:reviewer)") {
		t.Fatalf("expected assigned-agent selected skill rendering, got:\n%s", got)
	}
}

func TestCatalogPathHelpersAndEnsureRoots(t *testing.T) {
	root := t.TempDir()
	if got := AgentsIndexPath(root); got != filepath.Join(root, "agents", "AGENTS.md") {
		t.Fatalf("AgentsIndexPath = %q", got)
	}
	if err := EnsureAgentsRoot(root); err != nil {
		t.Fatalf("EnsureAgentsRoot: %v", err)
	}
	if err := EnsureSkillsRoot(root); err != nil {
		t.Fatalf("EnsureSkillsRoot: %v", err)
	}
	for _, rel := range []string{"agents", "skills"} {
		if !dirExists(filepath.Join(root, rel)) {
			t.Fatalf("expected %s to exist", rel)
		}
	}
	if err := EnsureAgentsRoot(""); err != nil {
		t.Fatalf("empty agent root should be a no-op: %v", err)
	}
	if err := EnsureSkillsRoot(""); err != nil {
		t.Fatalf("empty skill root should be a no-op: %v", err)
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
