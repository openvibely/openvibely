package builtinskills_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/builtinskills"
)

func TestSkillCuratorLearningDisabledByDefault(t *testing.T) {
	root := t.TempDir()
	if err := builtinskills.SyncTo(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "agents", "skill_curator", "SKILLS.md"))
	if err != nil {
		t.Fatal(err)
	}
	decl, _, err := agentlibrary.ParseDeclaration(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for slot, want := range map[string]bool{"after_complete": false} {
		hook, ok := decl.LifecycleHooks[slot]
		if !ok || hook.Enabled == nil || *hook.Enabled != want {
			t.Fatalf("%s default enabled should be %v: %+v", slot, want, hook)
		}
	}
}
