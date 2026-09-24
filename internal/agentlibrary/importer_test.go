package agentlibrary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
)

type fakeApplier struct {
	applied        []*SkillDeclaration
	archivedAgents []string
	archivedSkills []string
	protected      map[string]string // "<type>:<key>" -> reason
}

func (f *fakeApplier) ApplyDeclaration(_ context.Context, d *SkillDeclaration) ([]string, error) {
	f.applied = append(f.applied, d)
	return []string{"agent:" + d.Agent.Key, "skill:" + d.Handle()}, nil
}
func (f *fakeApplier) ArchiveAgent(_ context.Context, key, _, _ string) error {
	f.archivedAgents = append(f.archivedAgents, key)
	return nil
}
func (f *fakeApplier) ArchiveSkill(_ context.Context, handle, _, _ string) error {
	f.archivedSkills = append(f.archivedSkills, handle)
	return nil
}
func (f *fakeApplier) IsProtected(_ context.Context, targetType, key string) (bool, string, error) {
	r, ok := f.protected[targetType+":"+key]
	return ok, r, nil
}

func newImporter(t *testing.T) (*Importer, *fakeApplier, string) {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "project")
	app := &fakeApplier{protected: map[string]string{}}
	imp := NewImporter(SkillRoots{Project: root}, app)
	return imp, app, root
}

func TestWriteSkill_CreatesStandaloneFileAndIndex(t *testing.T) {
	imp, app, root := newImporter(t)
	decl := &SkillDeclaration{
		Kind: "openvibely.agent_skill", Version: 1,
		Skill: SkillBlock{Key: "verify", Scope: "project", Name: "Verify", Description: "checks changes"},
	}
	res, err := imp.WriteSkill(context.Background(), decl, "# Verify\n")
	if err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	if !res.Applied || len(res.Created) != 1 || res.Created[0] != "verify" {
		t.Fatalf("bad result: %+v", res)
	}
	path := filepath.Join(root, "skills", "verify", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	if len(app.applied) != 0 {
		t.Fatalf("standalone skill writes must not mutate agent DB, got %#v", app.applied)
	}
	index, err := os.ReadFile(filepath.Join(root, "skills", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read SKILLS.md: %v", err)
	}
	if !strings.Contains(string(index), "## verify") || !strings.Contains(string(index), "[Verify](verify/SKILL.md) — checks changes") {
		t.Fatalf("standalone index missing skill link:\n%s", index)
	}

	res, err = imp.WriteSkill(context.Background(), decl, "# Verify v2\n")
	if err != nil {
		t.Fatalf("WriteSkill update: %v", err)
	}
	if len(res.Created) != 0 || len(res.Updated) != 1 || res.Updated[0] != "verify" {
		t.Fatalf("expected update, got %+v", res)
	}
}

func TestWriteAgentRootDeclaration_CreatesSkillsIndexAndCallsApplier(t *testing.T) {
	imp, app, root := newImporter(t)
	decl := &SkillDeclaration{
		Kind: "openvibely.agent_skill", Version: 1,
		Agent: AgentDeclaration{Key: "backend", Scope: "project"},
		Tools: []string{"skill_view"},
	}
	body := "# Backend Skills\n\n## backend/verify\n\n[Verify](skills/verify/SKILL.md) — checks changes.\n"
	res, err := imp.WriteAgentRootDeclaration(context.Background(), decl, body)
	if err != nil {
		t.Fatalf("WriteAgentRootDeclaration: %v", err)
	}
	if !res.Applied || len(res.Created) != 1 || res.Created[0] != "backend" {
		t.Fatalf("bad result: %+v", res)
	}
	path := filepath.Join(root, "agents", "backend", "SKILLS.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SKILLS.md: %v", err)
	}
	if !strings.Contains(string(got), "## backend/verify") || !strings.Contains(string(got), "skills/verify/SKILL.md") {
		t.Fatalf("SKILLS.md did not preserve linked index body:\n%s", got)
	}
	agentsIndex, err := os.ReadFile(filepath.Join(root, "agents", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agentsIndex), "## backend") || !strings.Contains(string(agentsIndex), "backend/SKILLS.md") {
		t.Fatalf("AGENTS.md missing backend entry:\n%s", agentsIndex)
	}
	if len(app.applied) != 1 || !app.applied[0].IsAgentRootDeclaration() {
		t.Fatalf("expected root declaration applied, got %#v", app.applied)
	}
}

func TestWriteAgentRootDeclaration_MergesExistingSkillLinks(t *testing.T) {
	imp, _, root := newImporter(t)
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Agent: AgentDeclaration{Key: "backend", Scope: "project"}}
	first := "# Backend Skills\n\n## backend/verify\n\n[Verify](skills/verify/SKILL.md) — checks changes.\n"
	if _, err := imp.WriteAgentRootDeclaration(context.Background(), decl, first); err != nil {
		t.Fatalf("first root declaration: %v", err)
	}
	second := "# Backend Skills\n\n## backend/review\n\n[Review](skills/review/SKILL.md) — reviews changes.\n"
	if _, err := imp.WriteAgentRootDeclaration(context.Background(), decl, second); err != nil {
		t.Fatalf("second root declaration: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "agents", "backend", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read SKILLS.md: %v", err)
	}
	text := string(got)
	for _, want := range []string{"## backend/verify", "skills/verify/SKILL.md", "## backend/review", "skills/review/SKILL.md"} {
		if !strings.Contains(text, want) {
			t.Fatalf("merged SKILLS.md missing %q:\n%s", want, text)
		}
	}
}

func TestWriteAgentRootDeclaration_UpdatesExistingAgentIndexEntry(t *testing.T) {
	imp, _, root := newImporter(t)
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Agent: AgentDeclaration{Key: "backend", Scope: "project", Name: "Backend", Description: "old"}}
	if _, err := imp.WriteAgentRootDeclaration(context.Background(), decl, "# Backend\n"); err != nil {
		t.Fatalf("first root declaration: %v", err)
	}
	decl.Agent.Name = "Backend Updated"
	decl.Agent.Description = "new"
	if _, err := imp.WriteAgentRootDeclaration(context.Background(), decl, "# Backend Updated\n"); err != nil {
		t.Fatalf("second root declaration: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "agents", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(got)
	if !strings.Contains(text, "[Backend Updated](backend/SKILLS.md) — new") || strings.Contains(text, "[Backend](backend/SKILLS.md) — old") {
		t.Fatalf("AGENTS.md did not refresh metadata:\n%s", text)
	}
}

func TestWriteSkill_RejectsAgentRootDeclaration(t *testing.T) {
	imp, _, _ := newImporter(t)
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Agent: AgentDeclaration{Key: "backend"}}
	if _, err := imp.WriteSkill(context.Background(), decl, "# Backend\n"); err == nil {
		t.Fatalf("expected WriteSkill to reject agent root declaration")
	}
}

func TestWriteSkill_BlockedByProtection(t *testing.T) {
	imp, app, _ := newImporter(t)
	app.protected["skill:verify"] = "bundled"
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "verify", Scope: "project"}}
	res, err := imp.WriteSkill(context.Background(), decl, "")
	if err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	if res.Applied {
		t.Fatalf("protection must block")
	}
	if len(res.Blocked) != 1 || res.Blocked[0] != "verify" {
		t.Fatalf("blocked list: %+v", res.Blocked)
	}
	if len(app.applied) != 0 {
		t.Fatalf("applier must not be called on block")
	}
}

func TestWriteSkill_RejectsBadScope(t *testing.T) {
	imp, _, _ := newImporter(t)
	imp.roots = SkillRoots{Global: ""}
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "b", Scope: "global"}}
	if _, err := imp.WriteSkill(context.Background(), decl, ""); err == nil {
		t.Fatalf("expected error for unconfigured scope")
	}
}

func TestWriteAgentOwnedSkill_ProjectScopeRequiresProjectRoot(t *testing.T) {
	globalRoot := t.TempDir()
	imp := NewImporter(SkillRoots{Global: globalRoot}, &fakeApplier{protected: map[string]string{}})
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "review", Name: "Review"}}
	if _, err := imp.WriteAgentOwnedSkill(context.Background(), "project", "reviewer", decl, "body"); err == nil {
		t.Fatalf("expected project-scoped agent skill write to require project root")
	}
	if _, err := os.Stat(filepath.Join(globalRoot, "agents", "reviewer", "skills", "review", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("project write must not fall back to global root, stat err=%v", err)
	}
}

func TestWriteAgentOwnedSkill_EmptyScopeUsesDeclarationScope(t *testing.T) {
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()
	imp := NewImporter(SkillRoots{Global: globalRoot, Project: projectRoot}, &fakeApplier{protected: map[string]string{}})
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "review", Scope: "global", Name: "Review"}}
	if _, err := imp.WriteAgentOwnedSkill(context.Background(), "", "reviewer", decl, "body"); err != nil {
		t.Fatalf("WriteAgentOwnedSkill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(globalRoot, "agents", "reviewer", "skills", "review", "SKILL.md")); err != nil {
		t.Fatalf("expected global agent skill file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "agents", "reviewer", "skills", "review", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("empty scope with global declaration must not write project file, stat err=%v", err)
	}
}

func TestWriteAgentOwnedSkill_EmptyScopeWithoutDeclarationIsRejected(t *testing.T) {
	imp := NewImporter(SkillRoots{Global: t.TempDir(), Project: t.TempDir()}, &fakeApplier{protected: map[string]string{}})
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "review", Name: "Review"}}
	if _, err := imp.WriteAgentOwnedSkill(context.Background(), "", "reviewer", decl, "body"); err == nil {
		t.Fatal("expected empty agent-owned skill scope without declaration scope to be rejected")
	}
}

func TestWriteAgentOwnedSupportFile_ProjectScopeRequiresProjectRoot(t *testing.T) {
	globalRoot := t.TempDir()
	imp := NewImporter(SkillRoots{Global: globalRoot}, &fakeApplier{protected: map[string]string{}})
	if _, err := imp.WriteAgentOwnedSupportFile(context.Background(), "project", "reviewer", "review", SupportReferences, "note.md", []byte("x")); err == nil {
		t.Fatalf("expected project-scoped agent support write to require project root")
	}
	if _, err := os.Stat(filepath.Join(globalRoot, "agents", "reviewer", "skills", "review", "references", "note.md")); !os.IsNotExist(err) {
		t.Fatalf("project support write must not fall back to global root, stat err=%v", err)
	}
}

func TestWriteAgentOwnedSupportFile_EmptyScopeIsRejected(t *testing.T) {
	imp := NewImporter(SkillRoots{Global: t.TempDir(), Project: t.TempDir()}, &fakeApplier{protected: map[string]string{}})
	if _, err := imp.WriteAgentOwnedSupportFile(context.Background(), "", "reviewer", "review", SupportReferences, "note.md", []byte("x")); err == nil {
		t.Fatal("expected empty agent-owned support scope to be rejected")
	}
}

func TestWriteSupportFile_AcceptsAllowedKinds(t *testing.T) {
	imp, _, root := newImporter(t)
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "verify", Scope: "project"}}
	if _, err := imp.WriteSkill(context.Background(), decl, ""); err != nil {
		t.Fatal(err)
	}
	res, err := imp.WriteSupportFile(context.Background(), "project", "verify", SupportAssets, "example.json", []byte("hello"))
	if err != nil {
		t.Fatalf("WriteSupportFile: %v", err)
	}
	if !res.Applied || len(res.Created) != 1 {
		t.Fatalf("bad result: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(root, "skills", "verify", "assets", "example.json"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("file mismatch: %v / %q", err, got)
	}
}

func TestWriteSupportFile_RejectsTraversal(t *testing.T) {
	imp, _, _ := newImporter(t)
	if _, err := imp.WriteSupportFile(context.Background(), "project", "verify", SupportReferences, "../outside.md", []byte("x")); err == nil {
		t.Fatalf("expected traversal rejection")
	}
}

func TestWriteSupportFile_RejectsUnknownKind(t *testing.T) {
	imp, _, _ := newImporter(t)
	if _, err := imp.WriteSupportFile(context.Background(), "project", "verify", SupportFileKind("docs"), "x.md", []byte("y")); err == nil {
		t.Fatalf("expected unknown kind rejection")
	}
}

func TestRemoveSupportFile_RemovesAllowedKind(t *testing.T) {
	imp, _, root := newImporter(t)
	decl := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "verify", Scope: "project"}}
	if _, err := imp.WriteSkill(context.Background(), decl, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := imp.WriteSupportFile(context.Background(), "project", "verify", SupportTemplates, "plan.md", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	res, err := imp.RemoveSupportFile(context.Background(), "project", "verify", SupportTemplates, "plan.md")
	if err != nil {
		t.Fatalf("RemoveSupportFile: %v", err)
	}
	if !res.Applied || len(res.Archived) != 1 {
		t.Fatalf("bad result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "verify", "templates", "plan.md")); !os.IsNotExist(err) {
		t.Fatalf("support file should be removed, stat err=%v", err)
	}
}

func TestRemoveSkillIndexEntryPreservesUnrelatedContent(t *testing.T) {
	tests := []struct {
		name          string
		index         string
		wantChanged   bool
		wantUnchanged bool
	}{
		{
			name:        "without frontmatter",
			index:       "# Standalone Skills\n\nIndex narrative stays.\n\n## verify\n\n[Verify](verify/SKILL.md)\n\n## verify_extended\n\n[Extended](verify_extended/SKILL.md)\n\n## review\n\n[Review](review/SKILL.md)\n",
			wantChanged: true,
		},
		{
			name:        "with frontmatter",
			index:       "---\nalways_use:\n  - verify\n  - review\ncustom: value\n---\n\n# Standalone Skills\n\nIndex narrative stays.\n\n## verify\n\n[Verify](verify/SKILL.md)\n\n## verify_extended\n\n[Extended](verify_extended/SKILL.md)\n\n## review\n\n[Review](review/SKILL.md)\n",
			wantChanged: true,
		},
		{
			name:          "similarly named and nonmatching headings",
			index:         "# Standalone Skills\n\nIndex narrative stays.\n\n## verify_extended\n\n[Extended](verify_extended/SKILL.md)\n\n## review\n\n[Review](review/SKILL.md)\n",
			wantUnchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skills", "SKILLS.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.index), 0o644); err != nil {
				t.Fatal(err)
			}

			changed, err := RemoveSkillIndexEntry(path, "verify")
			if err != nil {
				t.Fatalf("RemoveSkillIndexEntry: %v", err)
			}
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantUnchanged {
				if string(got) != tt.index {
					t.Fatalf("nonmatching index changed:\n%s", got)
				}
				return
			}
			text := string(got)
			if strings.Contains(text, "## verify\n") {
				t.Fatalf("exact verify section was not removed:\n%s", text)
			}
			for _, want := range []string{
				"Index narrative stays.",
				"## verify_extended",
				"## review",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("result lost %q:\n%s", want, text)
				}
			}
			if strings.Contains(tt.index, "always_use:") {
				meta := agentskills.ParseSkillsIndexMeta(text)
				if len(meta.AlwaysUse) != 1 || meta.AlwaysUse[0] != "review" {
					t.Fatalf("deleted handle should be removed from always_use, got %v; content:\n%s", meta.AlwaysUse, text)
				}
				for _, want := range []string{"always_use:", "custom: value"} {
					if !strings.Contains(text, want) {
						t.Fatalf("result lost frontmatter %q:\n%s", want, text)
					}
				}
			}
		})
	}

	missingPath := filepath.Join(t.TempDir(), "skills", "SKILLS.md")
	changed, err := RemoveSkillIndexEntry(missingPath, "verify")
	if err != nil {
		t.Fatalf("RemoveSkillIndexEntry missing index: %v", err)
	}
	if changed {
		t.Fatal("missing index should be a no-op")
	}
}

func TestArchiveSkill_BlockedWhenProtected(t *testing.T) {
	imp, app, _ := newImporter(t)
	app.protected["skill:verify"] = "bundled"
	res, err := imp.ArchiveSkill(context.Background(), "verify", "", "")
	if err != nil {
		t.Fatalf("ArchiveSkill: %v", err)
	}
	if res.Applied || len(res.Blocked) != 1 {
		t.Fatalf("must be blocked, got %+v", res)
	}
}

func TestArchiveSkill_MarksSkillFileAndRemovesStandaloneIndexLink(t *testing.T) {
	imp, app, root := newImporter(t)
	verify := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "verify", Scope: "project", Name: "Verify", Description: "checks changes"}}
	if _, err := imp.WriteSkill(context.Background(), verify, "# Verify\n"); err != nil {
		t.Fatalf("write verify: %v", err)
	}
	review := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "review", Scope: "project", Name: "Review", Description: "reviews changes"}}
	if _, err := imp.WriteSkill(context.Background(), review, "# Review\n"); err != nil {
		t.Fatalf("write review: %v", err)
	}
	supportPath := filepath.Join(root, "skills", "verify", "references", "archive-guide.md")
	if err := os.MkdirAll(filepath.Dir(supportPath), 0o755); err != nil {
		t.Fatalf("mkdir support files: %v", err)
	}
	if err := os.WriteFile(supportPath, []byte("# Archive guide\n"), 0o644); err != nil {
		t.Fatalf("write support file: %v", err)
	}
	rootIndexPath := filepath.Join(root, "skills", "SKILLS.md")
	rootIndex := "---\nalways_use:\n  - verify\n  - review\ncustom: value\n---\n\n# Standalone Skills\n\nIndex narrative stays.\n\n## verify\n\n[Verify](verify/SKILL.md)\n\n## verify_extended\n\n[Extended](verify_extended/SKILL.md)\n\n## review\n\n[Review](review/SKILL.md)\n"
	if err := os.WriteFile(rootIndexPath, []byte(rootIndex), 0o644); err != nil {
		t.Fatalf("write root index: %v", err)
	}
	res, err := imp.ArchiveSkill(context.Background(), "verify", "review", "consolidated")
	if err != nil {
		t.Fatalf("ArchiveSkill: %v", err)
	}
	if !res.Applied || len(res.Archived) != 1 || len(app.archivedSkills) != 1 {
		t.Fatalf("bad archive result/apply: %+v app=%+v", res, app.archivedSkills)
	}
	rootIndexData, err := os.ReadFile(rootIndexPath)
	if err != nil {
		t.Fatalf("read root index: %v", err)
	}
	rootIndexText := string(rootIndexData)
	if strings.Contains(rootIndexText, "## verify\n") || !strings.Contains(rootIndexText, "## verify_extended\n") || !strings.Contains(rootIndexText, "## review\n") {
		t.Fatalf("root index not consolidated:\n%s", rootIndexText)
	}
	meta := agentskills.ParseSkillsIndexMeta(rootIndexText)
	if len(meta.AlwaysUse) != 1 || meta.AlwaysUse[0] != "review" {
		t.Fatalf("archived skill should be removed from always_use, got %v; content:\n%s", meta.AlwaysUse, rootIndexText)
	}
	for _, want := range []string{"always_use:", "custom: value", "Index narrative stays."} {
		if !strings.Contains(rootIndexText, want) {
			t.Fatalf("root index lost %q:\n%s", want, rootIndexText)
		}
	}
	indexReported := false
	for _, path := range res.ChangedPaths {
		if path == rootIndexPath {
			indexReported = true
			break
		}
	}
	if !indexReported {
		t.Fatalf("archive result did not report changed root index path: %+v", res.ChangedPaths)
	}
	if got, err := os.ReadFile(supportPath); err != nil || string(got) != "# Archive guide\n" {
		t.Fatalf("archive removed or changed support file: %q err=%v", got, err)
	}
	skillFile, err := os.ReadFile(filepath.Join(root, "skills", "verify", "SKILL.md"))
	if err != nil {
		t.Fatalf("read archived skill: %v", err)
	}
	for _, want := range []string{"enabled: false", "archived: true", "absorbed_into: review", "archive_reason: consolidated"} {
		if !strings.Contains(string(skillFile), want) {
			t.Fatalf("archived skill missing %q:\n%s", want, skillFile)
		}
	}
}

func TestArchiveAgent_PassesThrough(t *testing.T) {
	imp, app, _ := newImporter(t)
	res, err := imp.ArchiveAgent(context.Background(), "backend", "platform", "merged")
	if err != nil {
		t.Fatalf("ArchiveAgent: %v", err)
	}
	if !res.Applied || len(res.Archived) != 1 {
		t.Fatalf("bad result: %+v", res)
	}
	if len(app.archivedAgents) != 1 || app.archivedAgents[0] != "backend" {
		t.Fatalf("applier not called: %v", app.archivedAgents)
	}
}

func TestNormalizeStandaloneSkillPackage_RawMarkdownGeneratesRequiredFrontmatter(t *testing.T) {
	decl, body, err := NormalizeStandaloneSkillPackage("# Raw Skill\n\nUse this skill for raw imports.\n", "raw_skill", "project")
	if err != nil {
		t.Fatalf("NormalizeStandaloneSkillPackage: %v", err)
	}
	if decl.Kind != "openvibely.agent_skill" || decl.Version != 1 || decl.Skill.Key != "raw_skill" || decl.Skill.Name != "raw_skill" || decl.Skill.Scope != "project" || decl.Skill.Enabled == nil || !*decl.Skill.Enabled {
		t.Fatalf("unexpected declaration: %+v", decl)
	}
	if !strings.Contains(body, "# Raw Skill") {
		t.Fatalf("body not preserved: %q", body)
	}
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		t.Fatalf("RenderSkillMarkdown: %v", err)
	}
	for _, want := range []string{"kind: openvibely.agent_skill", "version: 1", "key: raw_skill", "enabled: true", "# Raw Skill"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered SKILL.md missing %q:\n%s", want, rendered)
		}
	}
}

func TestNormalizeStandaloneSkillPackage_StandardFrontmatterConvertsToOpenVibelyDeclaration(t *testing.T) {
	content := `---
name: Skill Creator
description: Create and improve skills.
---
# Skill Creator
`
	decl, body, err := NormalizeStandaloneSkillPackage(content, "skill-creator", "global")
	if err != nil {
		t.Fatalf("NormalizeStandaloneSkillPackage: %v", err)
	}
	if decl.Kind != "openvibely.agent_skill" || decl.Skill.Key != "skill-creator" || decl.Skill.Name != "Skill Creator" || decl.Skill.Description != "Create and improve skills." || decl.Skill.Enabled == nil || !*decl.Skill.Enabled {
		t.Fatalf("unexpected declaration: %+v", decl)
	}
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		t.Fatalf("RenderSkillMarkdown: %v", err)
	}
	if strings.Contains(rendered, "name: Skill Creator\ndescription:") || !strings.Contains(rendered, "kind: openvibely.agent_skill") || !strings.Contains(rendered, "enabled: true") {
		t.Fatalf("standard package was not converted to OpenVibely frontmatter:\n%s", rendered)
	}
}

func TestNormalizeStandaloneSkillPackage_ValidDeclarationNormalizesWithoutClobberingFields(t *testing.T) {
	content := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: existing_skill
  name: Existing Skill
  scope: global
  description: Keep me.
routing:
  triggers:
    - existing trigger
  priority: 7
---
# Existing
`
	decl, body, err := NormalizeStandaloneSkillPackage(content, "ignored", "project")
	if err != nil {
		t.Fatalf("NormalizeStandaloneSkillPackage: %v", err)
	}
	if decl.Skill.Key != "existing_skill" || decl.Skill.Name != "Existing Skill" || decl.Skill.Description != "Keep me." || decl.Routing.Priority != 7 || len(decl.Routing.Triggers) != 1 || decl.Skill.Enabled == nil || !*decl.Skill.Enabled {
		t.Fatalf("valid fields were clobbered: %+v", decl)
	}
	if decl.Skill.Scope != "project" {
		t.Fatalf("scope should be normalized to requested import scope, got %q", decl.Skill.Scope)
	}
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		t.Fatalf("RenderSkillMarkdown: %v", err)
	}
	for _, want := range []string{"routing:", "priority: 7", "enabled: true", "# Existing"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered declaration missing %q:\n%s", want, rendered)
		}
	}
}

func TestNormalizeStandaloneSkillPackage_ValidDeclarationFillsRequiredFrontmatterFields(t *testing.T) {
	content := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: sparse_skill
---
# Sparse Skill

Use this sparse skill body as the description.
`
	decl, body, err := NormalizeStandaloneSkillPackage(content, "ignored", "project")
	if err != nil {
		t.Fatalf("NormalizeStandaloneSkillPackage: %v", err)
	}
	if decl.Skill.Name == "" || decl.Skill.Description == "" || decl.Skill.Enabled == nil || !*decl.Skill.Enabled {
		t.Fatalf("required fields were not filled: %+v", decl.Skill)
	}
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		t.Fatalf("RenderSkillMarkdown: %v", err)
	}
	for _, want := range []string{"name: Sparse Skill", "description: Use this sparse skill body as the description.", "enabled: true"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered declaration missing %q:\n%s", want, rendered)
		}
	}
}

func TestImportSkillPackage_WritesSkillSupportIndexAndCatalogLoads(t *testing.T) {
	imp, _, root := newImporter(t)
	content := `---
name: Imported Skill
description: Imported from disk.
---
# Imported Skill

Use this imported skill.
`
	res, err := imp.ImportSkillPackage(context.Background(), content, "imported_skill", "project", []SkillPackageFile{{Path: "references/guide.md", Content: []byte("# Guide\n")}})
	if err != nil {
		t.Fatalf("ImportSkillPackage: %v", err)
	}
	if !res.Applied || len(res.Created) == 0 || res.Created[0] != "imported_skill" {
		t.Fatalf("unexpected result: %+v", res)
	}
	skillPath := filepath.Join(root, "skills", "imported_skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read imported skill: %v", err)
	}
	for _, want := range []string{"kind: openvibely.agent_skill", "key: imported_skill", "enabled: true", "Use this imported skill."} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("imported SKILL.md missing %q:\n%s", want, data)
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, "skills", "imported_skill", "references", "guide.md")); err != nil || string(got) != "# Guide\n" {
		t.Fatalf("support file mismatch got=%q err=%v", got, err)
	}
	index, err := os.ReadFile(filepath.Join(root, "skills", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(index), "## imported_skill") || !strings.Contains(string(index), "imported_skill/SKILL.md") {
		t.Fatalf("index missing imported skill:\n%s", index)
	}
	catalog, err := agentskills.BuildCatalog("test", root, "")
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	if _, ok := catalog.Lookup("imported_skill"); !ok {
		t.Fatalf("imported skill not loadable from catalog")
	}
}

func TestReadSkillPackageFromPath_ReadsDirectorySupportFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path_skill")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Path Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte("guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillMD, packageName, files, err := ReadSkillPackageFromPath(dir)
	if err != nil {
		t.Fatalf("ReadSkillPackageFromPath: %v", err)
	}
	if !strings.Contains(skillMD, "# Path Skill") || packageName != "path_skill" || len(files) != 1 || files[0].Path != "references/guide.md" || string(files[0].Content) != "guide" {
		t.Fatalf("unexpected package read: packageName=%q files=%+v content=%q", packageName, files, skillMD)
	}
}

func TestSlugifySkillName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "Legacy Debug", want: "legacy_debug"},
		{input: "Review---Migrations...Now", want: "review_migrations_now"},
		{input: "  Spaced   Skill   ", want: "spaced_skill"},
		{input: "___Punctuation_Wrap___", want: "punctuation_wrap"},
		{input: "mix.dot_dash-sep", want: "mix_dot_dash_sep"},
		{input: "...", want: "skill"},
		{input: "---", want: "skill"},
		{input: "", want: "skill"},
		{input: "Skill123", want: "skill123"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := SlugifySkillName(tc.input)
			if got != tc.want {
				t.Errorf("SlugifySkillName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
