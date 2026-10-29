package agentskills

import (
	"path/filepath"
	"testing"
)

func TestSkillEnabledPreferencesAreScopedAndCaseInsensitive(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	path := filepath.Join(global, SkillsDir, "document_word", SkillFile)
	if got := SkillEnabledPreference(path); got != nil {
		t.Fatal("missing preference should use declaration default")
	}
	if err := SetSkillEnabledPreference(global, "DOCUMENT_WORD", false); err != nil {
		t.Fatal(err)
	}
	if got := SkillEnabledPreference(path); got == nil || *got {
		t.Fatal("case alias should share disabled preference")
	}
	if got := SkillEnabledPreference(filepath.Join(project, SkillsDir, "document_word", SkillFile)); got != nil {
		t.Fatal("global preference leaked into project override")
	}
	if err := SetSkillEnabledPreference(global, "Document_Word", true); err != nil {
		t.Fatal(err)
	}
	if got := SkillEnabledPreference(path); got == nil || !*got {
		t.Fatal("enabling should replace disabled preference")
	}
	if err := SetSkillEnabledPreference(global, "../document_word", false); err == nil {
		t.Fatal("invalid key accepted")
	}
}
