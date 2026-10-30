package agentskills

import (
	"os"
	"path/filepath"
	"testing"
)

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
