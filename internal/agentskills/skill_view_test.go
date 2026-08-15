package agentskills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSkillView_RejectsHandleNotInFrozenIndex(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	cat, _ := BuildCatalog("turn-1", root, "")

	rt := SkillRuntimeTools(cat, root, "", nil)
	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"nonexistent"}`))
	if !handled || err != nil {
		t.Fatalf("expected handled tool call, got handled=%v err=%v", handled, err)
	}
	if !isErr {
		t.Fatalf("expected isError=true for unknown handle, got out=%q", out)
	}
	if !strings.Contains(out, "not in this turn's authorized index") {
		t.Fatalf("expected rejection message, got %q", out)
	}
}

func TestSelectedSkillRuntimeToolsOmitsLibraryToolsAndViewsSelectedSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nselected body")
	cat, _ := BuildCatalog("turn-1", root, "")

	rt := SelectedSkillRuntimeTools(cat)
	if rt == nil {
		t.Fatal("SelectedSkillRuntimeTools returned nil")
	}
	if len(rt.Definitions) != 1 || rt.Definitions[0].Name != "skill_view" {
		t.Fatalf("selected runtime should expose only skill_view, got %+v", rt.Definitions)
	}
	if mine, allowed := rt.Filter("skills_list"); mine || allowed {
		t.Fatal("selected runtime should not own skills_list")
	}
	if mine, allowed := rt.Filter(" skill_view "); !mine || !allowed {
		t.Fatalf("selected runtime should own skill_view, got mine=%v allowed=%v", mine, allowed)
	}

	out, handled, isErr, err := rt.Executor(context.Background(), "skills_list", json.RawMessage(`{}`))
	if handled || isErr || err != nil || out != "" {
		t.Fatalf("skills_list should fall through for selected runtime, handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}

	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"skill"}`))
	if !handled || isErr || err != nil || !strings.Contains(out, "selected body") {
		t.Fatalf("expected selected skill view, handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestSkillView_RejectsTraversalAbsoluteAndAgentPrefixedPaths(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	cat, _ := BuildCatalog("turn-1", root, "")
	rt := SkillRuntimeTools(cat, root, "", nil)

	for _, bad := range []string{
		`{"handle":"../secret"}`,
		`{"handle":"/Users/alice/.ssh/id_rsa"}`,
		`{"handle":"agent/skill"}`,
		`{"handle":"skill/SKILL.md"}`,
		`{"handle":""}`,
	} {
		out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(bad))
		if !handled || err != nil {
			t.Fatalf("expected handled, got handled=%v err=%v (input=%s)", handled, err, bad)
		}
		if !isErr {
			t.Fatalf("expected isError=true for %s, got %q", bad, out)
		}
	}
}

func TestSkillView_ReadsResolvedSkillBodyAndLinkedFiles(t *testing.T) {
	root := t.TempDir()
	body := "---\ntitle: T\ndescription: D\n---\n\nimportant skill body"
	writeSkill(t, root, "skill", body)
	mustWriteFile(t, root, "skills/skill/references/notes.md", "reference notes")
	mustWriteFile(t, root, "skills/skill/assets/example.json", `{"ok":true}`)
	cat, _ := BuildCatalog("turn-1", root, "")
	rt := SkillRuntimeTools(cat, root, "", nil)

	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"skill"}`))
	if !handled || err != nil || isErr {
		t.Fatalf("expected ok result, got handled=%v err=%v isErr=%v out=%q", handled, err, isErr, out)
	}
	var got struct {
		Body        string              `json:"body"`
		LinkedFiles map[string][]string `json:"linked_files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("expected JSON skill view, got %q: %v", out, err)
	}
	if !strings.Contains(got.Body, "important skill body") {
		t.Fatalf("expected body content, got %+v", got)
	}
	if len(got.LinkedFiles["references"]) != 1 || got.LinkedFiles["references"][0] != "notes.md" {
		t.Fatalf("expected reference link metadata, got %+v", got.LinkedFiles)
	}
	if len(got.LinkedFiles["assets"]) != 1 || got.LinkedFiles["assets"][0] != "example.json" {
		t.Fatalf("expected asset link metadata, got %+v", got.LinkedFiles)
	}
}

func TestSkillView_QualifiedHandlesDisambiguateMergedCatalogs(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "shared", "---\ntitle: Standalone\n---\nstandalone body")
	mustWriteFile(t, root, "agents/reviewer/SKILLS.md", "# Reviewer Skills\n\n## reviewer/shared\n")
	mustWriteFile(t, root, "agents/reviewer/skills/shared/SKILL.md", "---\ntitle: Agent\n---\nagent body")
	standalone, err := BuildCatalog("turn-standalone", root, "")
	if err != nil {
		t.Fatalf("build standalone: %v", err)
	}
	agent, err := BuildAgentCatalog("turn-agent", root, "", "reviewer")
	if err != nil {
		t.Fatalf("build agent: %v", err)
	}
	merged := NewCatalog("turn-merged", append(standalone.Entries(), agent.Entries()...))
	rt := SkillRuntimeTools(merged, root, "", nil)

	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"shared"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "ambiguous") {
		t.Fatalf("expected ambiguous bare handle rejection handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}

	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"standalone:shared"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "standalone body") {
		t.Fatalf("expected standalone qualified view handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}

	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"agent:reviewer/shared"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "agent body") {
		t.Fatalf("expected agent qualified view handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestSkillView_LoadsAuthorizedSupportFile(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	mustWriteFile(t, root, "skills/skill/references/notes.md", "reference notes")
	cat, _ := BuildCatalog("turn-1", root, "")
	rt := SkillRuntimeTools(cat, root, "", nil)

	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"skill","file_path":"references/notes.md"}`))
	if !handled || err != nil || isErr {
		t.Fatalf("expected ok result, got handled=%v err=%v isErr=%v out=%q", handled, err, isErr, out)
	}
	if !strings.Contains(out, "reference notes") || !strings.Contains(out, "references/notes.md") {
		t.Fatalf("expected support file payload, got %q", out)
	}
}

func TestSkillView_ExposesSkillAndScriptDirectories(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nrun ${OPENVIBELY_SKILL_DIR}/scripts/echo.sh")
	mustWriteFile(t, root, "skills/skill/scripts/echo.sh", "#!/bin/sh\necho hi\n")
	cat, _ := BuildCatalog("turn-1", root, "")
	rt := SkillRuntimeTools(cat, root, "", nil)

	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"skill"}`))
	if !handled || err != nil || isErr {
		t.Fatalf("expected ok result, got handled=%v err=%v isErr=%v out=%q", handled, err, isErr, out)
	}
	var got struct {
		Body       string `json:"body"`
		SkillDir   string `json:"skill_dir"`
		ScriptsDir string `json:"scripts_dir"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	wantSkillDir := filepath.Join(root, "skills", "skill")
	wantScriptsDir := filepath.Join(wantSkillDir, "scripts")
	if got.SkillDir != wantSkillDir || got.ScriptsDir != wantScriptsDir {
		t.Fatalf("expected dirs %q/%q, got %q/%q", wantSkillDir, wantScriptsDir, got.SkillDir, got.ScriptsDir)
	}
	if !strings.Contains(got.Body, wantScriptsDir+string(filepath.Separator)+"echo.sh") {
		t.Fatalf("expected skill dir token substitution in body, got %q", got.Body)
	}
}

func TestSkillView_RejectsSupportFileTraversalAndUnknownDirs(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	cat, _ := BuildCatalog("turn-1", root, "")
	rt := SkillRuntimeTools(cat, root, "", nil)

	for _, bad := range []string{
		`{"handle":"skill","file_path":"../secret.md"}`,
		`{"handle":"skill","file_path":"references/../secret.md"}`,
		`{"handle":"skill","file_path":"docs/notes.md"}`,
		`{"handle":"skill","file_path":"/tmp/notes.md"}`,
	} {
		out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(bad))
		if !handled || err != nil {
			t.Fatalf("expected handled (%s), got handled=%v err=%v", bad, handled, err)
		}
		if !isErr {
			t.Fatalf("expected isError=true for %s, got %q", bad, out)
		}
	}
}

func TestSkillsList_ReturnsRawTopLevelSkillsMd(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill_x", "---\ntitle: T1\n---\nbody")
	writeSkill(t, root, "skill_y", "---\ntitle: T2\n---\nbody")
	cat, _ := BuildCatalog("turn-1", root, "")
	rt := SkillRuntimeTools(cat, root, "", nil)

	out, handled, isErr, err := rt.Executor(context.Background(), "skills_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr {
		t.Fatalf("expected ok, got handled=%v err=%v isErr=%v out=%q", handled, err, isErr, out)
	}
	if !strings.Contains(out, "## skill_x") || !strings.Contains(out, "## skill_y") {
		t.Fatalf("skills_list missing skill headers: %s", out)
	}
	if !strings.Contains(out, "=== view_handles ===") || !strings.Contains(out, "standalone:skill_x") || !strings.Contains(out, "standalone:skill_y") {
		t.Fatalf("skills_list missing qualified view handles: %s", out)
	}
}

func TestSkillsList_ExcludesDisabledSkillsFromOutput(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "enabled_skill", "---\ntitle: Enabled\n---\nbody")
	writeDisabledSkill(t, root, "disabled_skill")
	// Runtime catalog only contains enabled_skill.
	cat, err := BuildCatalog("turn-disabled", root, "")
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	rt := SkillRuntimeTools(cat, root, "", nil)

	out, handled, isErr, execErr := rt.Executor(context.Background(), "skills_list", json.RawMessage(`{}`))
	if !handled || execErr != nil || isErr {
		t.Fatalf("expected ok, got handled=%v execErr=%v isErr=%v out=%q", handled, execErr, isErr, out)
	}
	if strings.Contains(out, "disabled_skill") {
		t.Fatalf("skills_list must not include disabled_skill in output, got:\n%s", out)
	}
	if !strings.Contains(out, "## enabled_skill") {
		t.Fatalf("skills_list must include enabled_skill header, got:\n%s", out)
	}
	if !strings.Contains(out, "standalone:enabled_skill") {
		t.Fatalf("skills_list must include enabled_skill view handle, got:\n%s", out)
	}
}

type fakeInspector struct {
	list    []AgentSummary
	listErr error
	out     *AgentDetails
	err     error
}

func (f *fakeInspector) ListAgents(ctx context.Context) ([]AgentSummary, error) {
	return f.list, f.listErr
}

func (f *fakeInspector) InspectAgent(ctx context.Context, key string) (*AgentDetails, error) {
	return f.out, f.err
}

func TestAgentList_OptionalAndReturnsInspectorSummaries(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	cat, _ := BuildCatalog("turn", root, "")

	rtNo := SkillRuntimeTools(cat, root, "", nil)
	for _, def := range rtNo.Definitions {
		if def.Name == "agent_list" {
			t.Fatalf("agent_list must not be exposed without inspector")
		}
	}
	if allow, handled := rtNo.Filter("agent_list"); handled || allow {
		t.Fatalf("filter must not own agent_list when no inspector configured")
	}

	rt := SkillRuntimeTools(cat, root, "", &fakeInspector{list: []AgentSummary{{Key: "backend-engineer", Name: "Backend Engineer", Enabled: true, AttachedSkills: []string{"run-go-tests"}}}})
	out, handled, isErr, err := rt.Executor(context.Background(), "agent_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr {
		t.Fatalf("expected ok agent_list, got handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	var got []AgentSummary
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("expected JSON, got %q: %v", out, err)
	}
	if len(got) != 1 || got[0].Key != "backend-engineer" || len(got[0].AttachedSkills) != 1 {
		t.Fatalf("unexpected summaries: %+v", got)
	}

	out, handled, isErr, err = rt.Executor(context.Background(), "agent_list", json.RawMessage(`{"scope":"all"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "no parameters") {
		t.Fatalf("expected parameter rejection, got handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestAgentView_OptionalAndRequiresInspector(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	cat, _ := BuildCatalog("turn", root, "")

	rtNo := SkillRuntimeTools(cat, root, "", nil)
	for _, def := range rtNo.Definitions {
		if def.Name == "agent_view" {
			t.Fatalf("agent_view must not be exposed without inspector")
		}
	}
	if allow, handled := rtNo.Filter("agent_view"); handled || allow {
		t.Fatalf("filter must not own agent_view when no inspector configured")
	}

	insp := &fakeInspector{out: &AgentDetails{
		Key: "backend-engineer", Name: "Backend Engineer", Enabled: true,
		ToolGrants:     []string{"file_read", "file_edit"},
		Hooks:          []AgentHookView{{When: "after_complete", SkillKey: "observe", Enabled: true}},
		AttachedSkills: []string{"run-go-tests"},
	}}
	rt := SkillRuntimeTools(cat, root, "", insp)
	out, handled, isErr, err := rt.Executor(context.Background(), "agent_view", json.RawMessage(`{"key":"backend-engineer"}`))
	if !handled || err != nil || isErr {
		t.Fatalf("expected ok agent_view, got handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	var got AgentDetails
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("expected JSON, got %q: %v", out, err)
	}
	if got.Key != "backend-engineer" || len(got.Hooks) != 1 || len(got.AttachedSkills) != 1 {
		t.Fatalf("unexpected details: %+v", got)
	}
}

func TestAgentView_RejectsBadKeys(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	cat, _ := BuildCatalog("turn", root, "")
	rt := SkillRuntimeTools(cat, root, "", &fakeInspector{})

	for _, bad := range []string{`{"key":""}`, `{"key":"../etc"}`, `{"key":"a b"}`, `{}`} {
		out, handled, isErr, err := rt.Executor(context.Background(), "agent_view", json.RawMessage(bad))
		if !handled || err != nil {
			t.Fatalf("expected handled (%s), got handled=%v err=%v", bad, handled, err)
		}
		if !isErr {
			t.Fatalf("expected isError=true for %s, got %q", bad, out)
		}
	}

	out, handled, isErr, _ := rt.Executor(context.Background(), "agent_view", json.RawMessage(`{"key":"unknown"}`))
	if !handled || !isErr || !strings.Contains(out, "not found") {
		t.Fatalf("expected not-found rejection, got handled=%v isErr=%v out=%q", handled, isErr, out)
	}
}

func TestBuildCatalog_RejectsSlugsSkillViewCannotLoad(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "agent with space", "body")
	writeSkill(t, root, "_leading-underscore", "body")
	writeSkill(t, root, "good-skill", "---\n---\nbody")

	cat, err := BuildCatalog("turn", root, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, bad := range []string{"agent with space", "_leading-underscore"} {
		if _, ok := cat.Lookup(bad); ok {
			t.Fatalf("catalog should reject slug %q", bad)
		}
	}
	if _, ok := cat.Lookup("good-skill"); !ok {
		t.Fatalf("catalog should accept good-skill")
	}
}

func TestSkillView_DoesNotHandleOtherTools(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skill", "---\ntitle: T\n---\nbody")
	cat, _ := BuildCatalog("turn-1", root, "")
	rt := SkillRuntimeTools(cat, root, "", nil)

	out, handled, isErr, err := rt.Executor(context.Background(), "other_tool", json.RawMessage(`{}`))
	if handled || err != nil || isErr || out != "" {
		t.Fatalf("expected unhandled passthrough, got handled=%v isErr=%v out=%q err=%v", handled, isErr, out, err)
	}
}
