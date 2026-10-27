package agentlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/builtinskills"
)

func snapshotSkillFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestBundledSkillMutationProtection(t *testing.T) {
	for _, target := range []struct {
		name, scope, key string
		blocked          bool
	}{
		{"bundled global", "global", "document_word", true},
		{"project override", "project", "document_word", false},
		{"user global", "global", "user_skill", false},
	} {
		for _, action := range []string{"create", "patch", "archive", "write_file", "remove_file", "import"} {
			t.Run(target.name+"/"+action, func(t *testing.T) {
				global, project := t.TempDir(), t.TempDir()
				if err := builtinskills.SyncTo(global); err != nil {
					t.Fatal(err)
				}
				// No applier: protection must be enforced at the filesystem mutation boundary.
				imp := NewImporter(SkillRoots{Global: global, Project: project}, nil)
				declaration := fmt.Sprintf("---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: %s\n  scope: %s\n---\nREPLACEMENT\n", target.key, target.scope)
				if !target.blocked {
					decl, body, err := ParseDeclaration(declaration)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = imp.WriteSkill(context.Background(), decl, body); err != nil {
						t.Fatal(err)
					}
					if _, err = imp.WriteSupportFile(context.Background(), target.scope, target.key, SupportReferences, "creation.md", []byte("original")); err != nil {
						t.Fatal(err)
					}
				}
				beforeGlobal, beforeProject := snapshotSkillFiles(t, global), snapshotSkillFiles(t, project)
				recorder := &recRecorder{}
				tools := SkillMutationTools(imp, recorder)
				tool := "skill_manage"
				args := map[string]any{"action": action, "scope": target.scope, "handle": target.key, "declaration": declaration, "reason": "audit fixture"}
				switch action {
				case "write_file":
					args["support"] = map[string]string{"kind": "scripts", "path": "new.sh", "content": "new content"}
				case "remove_file":
					args["support"] = map[string]string{"kind": "references", "path": "creation.md"}
				case "import":
					tool = "skill_import"
					args = map[string]any{"scope": target.scope, "content": declaration, "files": []map[string]string{{"path": "scripts/imported.sh", "content": "new content"}}}
				}
				input, _ := json.Marshal(args)
				out, handled, failed, err := tools.Executor(context.Background(), tool, input)
				if err != nil || !handled || failed != target.blocked {
					t.Fatalf("handled=%v failed=%v err=%v output=%s", handled, failed, err, out)
				}
				if target.blocked {
					if !strings.Contains(out, "protected: bundled skill") {
						t.Fatal(out)
					}
					if !reflect.DeepEqual(beforeGlobal, snapshotSkillFiles(t, global)) || !reflect.DeepEqual(beforeProject, snapshotSkillFiles(t, project)) {
						t.Fatal("blocked operation changed skill files or indexes")
					}
					if len(recorder.rows) != 1 || recorder.rows[0].applied || !strings.Contains(recorder.rows[0].cause, "protected: bundled skill") {
						t.Fatalf("blocked mutation not audited: %+v", recorder.rows)
					}
				} else if target.scope == "project" && !reflect.DeepEqual(beforeGlobal, snapshotSkillFiles(t, global)) {
					t.Fatal("project override mutation changed bundled global files")
				}
			})
		}
	}
}

func TestBundledInventoryProtectsAllSkillsBeforeSync(t *testing.T) {
	global := t.TempDir()
	imp := NewImporter(SkillRoots{Global: global}, nil)
	err := fs.WalkDir(builtinskills.FS, "builtin", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		parts := strings.Split(strings.TrimPrefix(path, "builtin/"), "/")
		key := parts[len(parts)-2]
		decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: key, Scope: "global"}}
		if len(parts) == 3 {
			_, err = imp.WriteSkill(context.Background(), decl, "replacement")
		} else {
			_, err = imp.WriteAgentOwnedSkill(context.Background(), "global", parts[1], decl, "replacement")
		}
		if err == nil || !strings.Contains(err.Error(), "protected: bundled skill") {
			t.Errorf("unprotected bundled package %s: %v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotSkillFiles(t, global)) != 0 {
		t.Fatal("blocked writes created files before sync")
	}
}

func TestBundledSkillProtectionCoversFallbackScopeAndMultiRootArchive(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	imp := NewImporter(SkillRoots{Global: global}, nil)
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "document_word"}}
	if _, err := imp.WriteSkill(context.Background(), decl, "replacement"); err == nil {
		t.Fatal("empty scope bypassed global protection")
	}
	if _, err := imp.WriteSupportFile(context.Background(), "", "document_word", SupportScripts, "new.sh", []byte("replacement")); err == nil {
		t.Fatal("empty support scope bypassed global protection")
	}
	imp = NewImporter(SkillRoots{Global: global, Project: project}, nil)
	decl.Skill.Scope = "project"
	if _, err := imp.WriteSkill(context.Background(), decl, "project override"); err != nil {
		t.Fatal(err)
	}
	before := snapshotSkillFiles(t, project)
	if _, err := imp.ArchiveSkill(context.Background(), "document_word", "", "fixture"); err == nil {
		t.Fatal("multi-root archive bypassed protection")
	}
	if !reflect.DeepEqual(before, snapshotSkillFiles(t, project)) {
		t.Fatal("archive partially mutated project before rejecting global target")
	}
	for _, action := range []string{"write", "remove"} {
		var err error
		if action == "write" {
			_, err = imp.WriteAgentOwnedSupportFile(context.Background(), "global", "skill_curator", "maintain_skill_library", SupportScripts, "new.sh", []byte("new"))
		} else {
			_, err = imp.RemoveAgentOwnedSupportFile(context.Background(), "global", "skill_curator", "maintain_skill_library", SupportScripts, "new.sh")
		}
		if err == nil || !strings.Contains(err.Error(), "protected: bundled skill") {
			t.Fatalf("agent-owned support %s unprotected: %v", action, err)
		}
	}
}
