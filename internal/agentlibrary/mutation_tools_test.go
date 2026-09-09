package agentlibrary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

type recRecorder struct {
	rows []recRow
}

type recRow struct {
	action  string
	target  string
	key     string
	payload string
	applied bool
	blocked []string
	cause   string
}

func (r *recRecorder) Record(_ context.Context, action, target, key string, payload []byte, result *ImportResult, blocked error) error {
	row := recRow{action: action, target: target, key: key, payload: string(payload)}
	if result != nil {
		row.applied = result.Applied
		row.blocked = append([]string(nil), result.Blocked...)
	}
	if blocked != nil {
		row.cause = blocked.Error()
	}
	r.rows = append(r.rows, row)
	return nil
}

func buildTools(t *testing.T) (*Importer, *fakeApplier, *recRecorder, string) {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "project")
	app := &fakeApplier{protected: map[string]string{}}
	imp := NewImporter(SkillRoots{Project: root}, app)
	return imp, app, &recRecorder{}, root
}

// buildToolsBothScopes wires both global and project roots so tests can verify
// scope=global mutations land in the global tree.
func buildToolsBothScopes(t *testing.T) (*Importer, *fakeApplier, *recRecorder, string, string) {
	t.Helper()
	tmp := t.TempDir()
	globalRoot := filepath.Join(tmp, "global")
	projectRoot := filepath.Join(tmp, "project")
	app := &fakeApplier{protected: map[string]string{}}
	imp := NewImporter(SkillRoots{Global: globalRoot, Project: projectRoot}, app)
	return imp, app, &recRecorder{}, globalRoot, projectRoot
}

func TestMutationTools_Definitions(t *testing.T) {
	imp, _, _, _ := buildTools(t)
	tools := MutationTools(imp, nil)
	if tools == nil {
		t.Fatalf("MutationTools returned nil")
	}
	if len(tools.Definitions) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(tools.Definitions))
	}
	names := map[string]bool{}
	for _, d := range tools.Definitions {
		names[d.Name] = true
	}
	if !names["skill_manage"] || !names["skill_import"] || names["agent_manage"] {
		t.Fatalf("unexpected tool names: %v", names)
	}
	if !tools.HasDefinition("skill_manage") {
		t.Fatalf("HasDefinition skill_manage should be true")
	}
	if owns, handled := tools.Filter("skill_manage"); !owns || !handled {
		t.Fatalf("Filter must own skill_manage")
	}
	if owns, _ := tools.Filter("other_tool"); owns {
		t.Fatalf("Filter must not own other_tool")
	}
}

func TestSkillMutationTools_ExcludesAgentManage(t *testing.T) {
	imp, _, _, _ := buildTools(t)
	tools := SkillMutationTools(imp, nil)
	if tools == nil {
		t.Fatalf("SkillMutationTools returned nil")
	}
	if !tools.HasDefinition("skill_manage") {
		t.Fatalf("expected skill_manage definition")
	}
	if tools.HasDefinition("agent_manage") {
		t.Fatalf("skill-only runtime must not expose agent_manage")
	}
	if owns, handled := tools.Filter("agent_manage"); owns || handled {
		t.Fatalf("skill-only runtime must not handle agent_manage")
	}
}

func TestMutationToolDescriptionsAvoidInternalPromptLabels(t *testing.T) {
	imp, _, _, _ := buildTools(t)
	for name, tools := range map[string]*llmcontracts.RuntimeTools{
		"skill_manage":       SkillMutationTools(imp, nil),
		"agent_skill_manage": LibraryAgentSkillMutationTools(imp, nil),
	} {
		if tools == nil || len(tools.Definitions) == 0 {
			t.Fatalf("%s tools missing definitions", name)
		}
		for _, def := range tools.Definitions {
			for _, forbidden := range []string{"generated skill", "generated skills", "protected/system agents", "non-system agent"} {
				if strings.Contains(def.Description, forbidden) {
					t.Fatalf("%s description contains unnecessary internal wording %q: %s", def.Name, forbidden, def.Description)
				}
			}
		}
	}
}

func TestSkillManage_Create_RecordsAndApplies(t *testing.T) {
	imp, app, rec, _ := buildTools(t)
	tools := MutationTools(imp, rec)

	declaration := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: verify
  scope: project
---
# verify
`
	params, _ := json.Marshal(map[string]any{
		"action":      "create",
		"declaration": declaration,
	})
	out, handled, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if !handled {
		t.Fatalf("expected handled=true")
	}
	if isErr {
		t.Fatalf("unexpected isErr=true, output=%s", out)
	}
	var result ImportResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if !result.Applied {
		t.Fatalf("expected applied=true: %+v", result)
	}
	if len(rec.rows) != 1 || rec.rows[0].action != "create" || rec.rows[0].target != "skill" {
		t.Fatalf("recorder mismatch: %+v", rec.rows)
	}
	if len(app.applied) != 0 {
		t.Fatalf("standalone skill_manage must not mutate agent DB: %+v", app.applied)
	}
}

func TestSkillManage_GlobalScopeAcceptedAndWritesToGlobalRoot(t *testing.T) {
	imp, app, rec, globalRoot, projectRoot := buildToolsBothScopes(t)
	tools := MutationTools(imp, rec)

	declaration := `---
kind: openvibely.agent_skill
version: 1
skill: {key: verify, scope: global}
---
# verify
`
	params, _ := json.Marshal(map[string]any{
		"action":      "create",
		"declaration": declaration,
	})
	out, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if isErr {
		t.Fatalf("global scope must be accepted, got %s", out)
	}
	if len(app.applied) != 0 {
		t.Fatalf("standalone global skill must not mutate agent DB, got %+v", app.applied)
	}

	// SKILL.md must land under the global root, not the project root.
	wantPath := filepath.Join(globalRoot, "skills", "verify", "SKILL.md")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected SKILL.md at global root %s: %v", wantPath, err)
	}
	unexpected := filepath.Join(projectRoot, "skills", "verify", "SKILL.md")
	if _, err := os.Stat(unexpected); err == nil {
		t.Fatalf("global mutation must not write into project root: %s", unexpected)
	}
	if len(rec.rows) != 1 || !rec.rows[0].applied {
		t.Fatalf("global mutation should be recorded as applied, rows=%+v", rec.rows)
	}
}

func TestSkillManagePatchRefreshesSkillsListDescription(t *testing.T) {
	imp, _, rec, root := buildTools(t)
	tools := MutationTools(imp, rec)
	ctx := context.Background()

	createDeclaration := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: consolidate_memory
  name: Consolidate Memory
  scope: project
  description: Old memory consolidation summary.
---
# Consolidate Memory
`
	params, _ := json.Marshal(map[string]any{
		"action":      "create",
		"declaration": createDeclaration,
	})
	out, handled, isErr, err := tools.Executor(ctx, "skill_manage", params)
	if !handled || err != nil || isErr {
		t.Fatalf("create failed handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}

	patchDeclaration := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: consolidate_memory
  name: Memory Consolidation
  scope: project
  description: Updated repo-local memory consolidation guidance.
---
# Memory Consolidation
`
	params, _ = json.Marshal(map[string]any{
		"action":      "patch",
		"handle":      "consolidate_memory",
		"declaration": patchDeclaration,
	})
	out, handled, isErr, err = tools.Executor(ctx, "skill_manage", params)
	if !handled || err != nil || isErr {
		t.Fatalf("patch failed handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}

	catalog, err := agentskills.BuildCatalog("turn", "", root)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	runtimeTools := agentskills.SkillRuntimeTools(catalog, "", root, nil)
	list, handled, isErr, err := runtimeTools.Executor(ctx, "skills_list", json.RawMessage(`{"scope":"project"}`))
	if !handled || err != nil || isErr {
		t.Fatalf("skills_list failed handled=%v isErr=%v err=%v out=%s", handled, isErr, err, list)
	}
	if !strings.Contains(list, "[Memory Consolidation](consolidate_memory/SKILL.md) — Updated repo-local memory consolidation guidance.") {
		t.Fatalf("skills_list did not show patched description:\n%s", list)
	}
	if strings.Contains(list, "Old memory consolidation summary") || strings.Contains(list, "[Consolidate Memory]") {
		t.Fatalf("skills_list still shows stale description/name:\n%s", list)
	}
	if !strings.Contains(list, "standalone:consolidate_memory") {
		t.Fatalf("skills_list missing canonical view handle:\n%s", list)
	}
}

func TestSkillManage_UnknownScopeRejected(t *testing.T) {
	imp, app, rec, _ := buildTools(t)
	tools := MutationTools(imp, rec)

	declaration := `---
kind: openvibely.agent_skill
version: 1
skill: {key: verify, scope: team}
---
# verify
`
	params, _ := json.Marshal(map[string]any{
		"action":      "create",
		"declaration": declaration,
	})
	out, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if !isErr {
		t.Fatalf("unknown scope must be rejected, got %s", out)
	}
	if len(app.applied) != 0 {
		t.Fatalf("unknown scope must not call applier: %+v", app.applied)
	}
	if len(rec.rows) != 1 || rec.rows[0].applied || !strings.Contains(rec.rows[0].cause, "scope must be global or project") {
		t.Fatalf("blocked unknown scope must be recorded, rows=%+v out=%s", rec.rows, out)
	}
}

func TestSkillManage_HandleMismatch_Blocks(t *testing.T) {
	imp, _, rec, _ := buildTools(t)
	tools := MutationTools(imp, rec)

	declaration := `---
kind: openvibely.agent_skill
version: 1
skill: {key: verify, scope: project}
---
`
	params, _ := json.Marshal(map[string]any{
		"action":      "patch",
		"handle":      "other-skill",
		"declaration": declaration,
	})
	out, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if !isErr {
		t.Fatalf("handle mismatch must be reported as error; got %s", out)
	}
	if len(rec.rows) != 1 || rec.rows[0].applied {
		t.Fatalf("blocked mutation must be recorded: %+v", rec.rows)
	}
	if !strings.Contains(rec.rows[0].cause, "handle mismatch") {
		t.Fatalf("cause should mention handle mismatch, got %q", rec.rows[0].cause)
	}
}

func TestSkillManage_WriteAndRemoveSupportFile(t *testing.T) {
	imp, _, rec, projectRoot := buildTools(t)
	tools := MutationTools(imp, rec)

	// First create the skill so the directory exists.
	declaration := `---
kind: openvibely.agent_skill
version: 1
skill: {key: verify, scope: project}
---
`
	createParams, _ := json.Marshal(map[string]any{"action": "create", "declaration": declaration})
	if _, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", createParams); isErr {
		t.Fatalf("create failed")
	}

	wfParams, _ := json.Marshal(map[string]any{
		"action": "write_file",
		"handle": "verify",
		"scope":  "project",
		"support": map[string]any{
			"kind":    "assets",
			"path":    "example.json",
			"content": `{"hello":"world"}`,
		},
	})
	out, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", wfParams)
	if isErr {
		t.Fatalf("write_file should succeed, got %s", out)
	}
	var res ImportResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v %s", err, out)
	}
	if !res.Applied {
		t.Fatalf("expected applied=true: %+v", res)
	}
	assetPath := filepath.Join(projectRoot, "skills", "verify", "assets", "example.json")
	if _, err := os.Stat(assetPath); err != nil {
		t.Fatalf("asset support file should exist: %v", err)
	}

	scriptParams, _ := json.Marshal(map[string]any{
		"action": "write_file",
		"handle": "verify",
		"scope":  "project",
		"support": map[string]any{
			"kind":    "scripts",
			"path":    "check.sh",
			"content": "#!/bin/sh\necho ok\n",
		},
	})
	out, _, isErr, _ = tools.Executor(context.Background(), "skill_manage", scriptParams)
	if isErr {
		t.Fatalf("script write should succeed, got %s", out)
	}
	info, err := os.Stat(filepath.Join(projectRoot, "skills", "verify", "scripts", "check.sh"))
	if err != nil {
		t.Fatalf("script support file should exist: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("script support files should be executable, mode=%v", info.Mode().Perm())
	}

	rmParams, _ := json.Marshal(map[string]any{
		"action": "remove_file",
		"handle": "verify",
		"scope":  "project",
		"support": map[string]any{
			"kind": "assets",
			"path": "example.json",
		},
	})
	out, _, isErr, _ = tools.Executor(context.Background(), "skill_manage", rmParams)
	if isErr {
		t.Fatalf("remove_file should succeed, got %s", out)
	}
	if _, err := os.Stat(assetPath); !os.IsNotExist(err) {
		t.Fatalf("asset support file should be removed, stat err=%v", err)
	}
	var sawWrite, sawRemove bool
	for _, r := range rec.rows {
		if r.target == "support_file" && r.action == "write_file" {
			sawWrite = true
		}
		if r.target == "support_file" && r.action == "remove_file" {
			sawRemove = true
		}
	}
	if !sawWrite || !sawRemove {
		t.Fatalf("recorder missing support_file rows: %+v", rec.rows)
	}
}

func TestSkillManage_WriteFileGlobalScopeAccepted(t *testing.T) {
	imp, _, rec, _, _ := buildToolsBothScopes(t)
	tools := MutationTools(imp, rec)

	// Seed a global skill so its support directory can receive a file.
	declaration := `---
kind: openvibely.agent_skill
version: 1
skill: {key: verify, scope: global}
---
`
	createParams, _ := json.Marshal(map[string]any{"action": "create", "declaration": declaration})
	if _, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", createParams); isErr {
		t.Fatalf("create global skill failed")
	}

	params, _ := json.Marshal(map[string]any{
		"action": "write_file",
		"handle": "verify",
		"scope":  "global",
		"support": map[string]any{
			"kind":    "references",
			"path":    "notes.md",
			"content": "hello",
		},
	})
	out, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if isErr {
		t.Fatalf("global support write must be accepted, got %s", out)
	}
	var seenSupport bool
	for _, r := range rec.rows {
		if r.target == "support_file" && r.action == "write_file" && r.applied {
			seenSupport = true
		}
	}
	if !seenSupport {
		t.Fatalf("recorder must record applied global support write: %+v", rec.rows)
	}
}

func TestSkillManage_WriteFileUnknownScopeRejected(t *testing.T) {
	imp, _, rec, _ := buildTools(t)
	tools := MutationTools(imp, rec)

	params, _ := json.Marshal(map[string]any{
		"action": "write_file",
		"handle": "verify",
		"scope":  "team",
		"support": map[string]any{
			"kind":    "references",
			"path":    "notes.md",
			"content": "hello",
		},
	})
	out, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if !isErr {
		t.Fatalf("unknown scope must be rejected, got %s", out)
	}
	if len(rec.rows) != 1 || rec.rows[0].target != "support_file" || rec.rows[0].applied || !strings.Contains(rec.rows[0].cause, "scope must be global or project") {
		t.Fatalf("blocked unknown scope must be recorded, rows=%+v out=%s", rec.rows, out)
	}
}

func TestSkillManage_RejectsAgentRootDeclaration(t *testing.T) {
	imp, app, rec, _ := buildTools(t)
	tools := MutationTools(imp, rec)
	declaration := `---
kind: openvibely.agent_skill
version: 1
agent:
  key: backend
---
# Backend Skills
`
	params, _ := json.Marshal(map[string]any{"action": "create", "declaration": declaration})
	out, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if !isErr {
		t.Fatalf("skill_manage must reject agent root declarations, got %s", out)
	}
	if len(app.applied) != 0 {
		t.Fatalf("rejected root declaration must not apply: %+v", app.applied)
	}
	if len(rec.rows) != 1 || rec.rows[0].target != "skill" || !strings.Contains(rec.rows[0].cause, "managed in the agent dialog") {
		t.Fatalf("expected recorded root-declaration rejection, rows=%+v out=%s", rec.rows, out)
	}
}

func TestMutationTools_FilterFalseForOtherTools(t *testing.T) {
	imp, _, _, _ := buildTools(t)
	tools := MutationTools(imp, nil)
	owns, handled := tools.Filter("skill_view")
	if owns || handled {
		t.Fatalf("Filter must not own skill_view")
	}
}

// Sanity check: nil importer returns nil tools.
func TestMutationTools_NilImporter(t *testing.T) {
	if MutationTools(nil, nil) != nil {
		t.Fatalf("nil importer should produce nil tools")
	}
}

// Sanity: an applier error inside the underlying importer is surfaced as an
// error result that the recorder still captures.
type erroringRecorder struct{}

func (e *erroringRecorder) Record(context.Context, string, string, string, []byte, *ImportResult, error) error {
	return errors.New("recorder boom")
}

func TestMutationTools_RecorderErrorsAreSwallowed(t *testing.T) {
	imp, _, _, _ := buildTools(t)
	tools := MutationTools(imp, &erroringRecorder{})
	declaration := `---
kind: openvibely.agent_skill
version: 1
skill: {key: verify, scope: project}
---`
	params, _ := json.Marshal(map[string]any{"action": "create", "declaration": declaration})
	_, _, isErr, _ := tools.Executor(context.Background(), "skill_manage", params)
	if isErr {
		t.Fatalf("recorder error must not propagate to tool result")
	}
}

func TestAgentSkillMutationTools_CreateScopedToAssignedAgent(t *testing.T) {
	imp, _, rec, projectRoot := buildTools(t)
	tools := AgentSkillMutationTools(imp, rec, "reviewer", "project")
	if tools == nil || !tools.HasDefinition("agent_skill_manage") {
		t.Fatalf("expected agent_skill_manage definition")
	}
	declaration := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: review_migrations
  description: Review migration safety.
---
# Review migrations
`
	params, _ := json.Marshal(map[string]any{"action": "create", "declaration": declaration})
	out, handled, isErr, err := tools.Executor(context.Background(), "agent_skill_manage", params)
	if err != nil || !handled || isErr {
		t.Fatalf("agent_skill_manage create failed output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	wantSkill := filepath.Join(projectRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md")
	if _, err := os.Stat(wantSkill); err != nil {
		t.Fatalf("expected agent-owned SKILL.md at %s: %v", wantSkill, err)
	}
	index, err := os.ReadFile(filepath.Join(projectRoot, "agents", "reviewer", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read agent index: %v", err)
	}
	if !strings.Contains(string(index), "## reviewer/review_migrations") || !strings.Contains(string(index), "skills/review_migrations/SKILL.md") {
		t.Fatalf("agent index missing skill entry:\n%s", index)
	}
	if len(rec.rows) != 1 || rec.rows[0].target != "agent_skill" || rec.rows[0].key != "reviewer/review_migrations" || !rec.rows[0].applied {
		t.Fatalf("unexpected recorder rows: %+v", rec.rows)
	}
}

func TestAgentSkillMutationTools_RejectsOtherAgentKey(t *testing.T) {
	imp, _, rec, _ := buildTools(t)
	tools := AgentSkillMutationTools(imp, rec, "reviewer", "project")
	declaration := `---
kind: openvibely.agent_skill
version: 1
agent:
  key: other_agent
skill:
  key: review_migrations
---
# Review migrations
`
	params, _ := json.Marshal(map[string]any{"action": "patch", "declaration": declaration})
	out, _, isErr, _ := tools.Executor(context.Background(), "agent_skill_manage", params)
	if !isErr || !strings.Contains(out, "scoped to agent") {
		t.Fatalf("expected scoped-agent rejection, got isErr=%v out=%s", isErr, out)
	}
	if len(rec.rows) != 1 || rec.rows[0].applied {
		t.Fatalf("blocked mutation should be recorded: %+v", rec.rows)
	}
}

func TestAgentSkillMutationTools_WriteScriptAndRemoveSupportFile(t *testing.T) {
	imp, _, _, projectRoot := buildTools(t)
	tools := AgentSkillMutationTools(imp, nil, "reviewer", "project")
	declaration := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: review_migrations
---
# Review migrations
`
	createParams, _ := json.Marshal(map[string]any{"action": "create", "declaration": declaration})
	out, handled, isErr, err := tools.Executor(context.Background(), "agent_skill_manage", createParams)
	if err != nil || !handled || isErr {
		t.Fatalf("agent_skill_manage create failed output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}

	scriptParams, _ := json.Marshal(map[string]any{
		"action": "write_file",
		"handle": "review_migrations",
		"support": map[string]any{
			"kind":    "scripts",
			"path":    "check.sh",
			"content": "#!/bin/sh\necho ok\n",
		},
	})
	out, _, isErr, err = tools.Executor(context.Background(), "agent_skill_manage", scriptParams)
	if err != nil || isErr {
		t.Fatalf("script write should succeed, got output=%s err=%v", out, err)
	}
	var writeRes ImportResult
	if err := json.Unmarshal([]byte(out), &writeRes); err != nil {
		t.Fatalf("unmarshal write result: %v %s", err, out)
	}
	if !writeRes.Applied || len(writeRes.Created) != 1 || writeRes.Created[0] != "reviewer/review_migrations/scripts/check.sh" {
		t.Fatalf("bad script write result: %+v", writeRes)
	}
	scriptPath := filepath.Join(projectRoot, "agents", "reviewer", "skills", "review_migrations", "scripts", "check.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("script support file should exist: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("agent-owned script support files should be executable, mode=%v", info.Mode().Perm())
	}

	removeParams, _ := json.Marshal(map[string]any{
		"action": "remove_file",
		"handle": "review_migrations",
		"support": map[string]any{
			"kind": "scripts",
			"path": "check.sh",
		},
	})
	out, _, isErr, err = tools.Executor(context.Background(), "agent_skill_manage", removeParams)
	if err != nil || isErr {
		t.Fatalf("remove_file should succeed, got output=%s err=%v", out, err)
	}
	var removeRes ImportResult
	if err := json.Unmarshal([]byte(out), &removeRes); err != nil {
		t.Fatalf("unmarshal remove result: %v %s", err, out)
	}
	if !removeRes.Applied || len(removeRes.Archived) != 1 || removeRes.Archived[0] != "reviewer/review_migrations/scripts/check.sh" {
		t.Fatalf("bad remove result: %+v", removeRes)
	}
	if len(removeRes.ChangedPaths) != 1 || removeRes.ChangedPaths[0] != scriptPath {
		t.Fatalf("remove result should include changed script path %q, got %+v", scriptPath, removeRes)
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("script support file should be removed, stat err=%v", err)
	}
}

func TestAgentSkillMutationTools_WriteSupportFileRejectsAgentPathHandle(t *testing.T) {
	imp, _, _, _ := buildTools(t)
	tools := AgentSkillMutationTools(imp, nil, "reviewer", "project")
	params, _ := json.Marshal(map[string]any{
		"action":  "write_file",
		"handle":  "other/review_migrations",
		"support": map[string]any{"kind": "references", "path": "notes.md", "content": "notes"},
	})
	out, _, isErr, _ := tools.Executor(context.Background(), "agent_skill_manage", params)
	if !isErr || !strings.Contains(out, "pass only the skill key") {
		t.Fatalf("expected agent path handle rejection, got isErr=%v out=%s", isErr, out)
	}
}

func TestLibraryAgentSkillMutationTools_CanTargetNonProtectedAgent(t *testing.T) {
	imp, _, rec, projectRoot := buildTools(t)
	tools := LibraryAgentSkillMutationTools(imp, rec)
	if tools == nil || !tools.HasDefinition("agent_skill_manage") {
		t.Fatalf("expected agent_skill_manage definition")
	}
	declaration := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: review_migrations
  description: Review migration safety.
---
# Review migrations
`
	params, _ := json.Marshal(map[string]any{"action": "create", "agent": "reviewer", "scope": "project", "declaration": declaration})
	out, handled, isErr, err := tools.Executor(context.Background(), "agent_skill_manage", params)
	if err != nil || !handled || isErr {
		t.Fatalf("library agent_skill_manage create failed output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	wantSkill := filepath.Join(projectRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md")
	if _, err := os.Stat(wantSkill); err != nil {
		t.Fatalf("expected agent-owned SKILL.md at %s: %v", wantSkill, err)
	}
	if len(rec.rows) != 1 || rec.rows[0].target != "agent_skill" || rec.rows[0].key != "reviewer/review_migrations" || !rec.rows[0].applied {
		t.Fatalf("unexpected recorder rows: %+v", rec.rows)
	}
}

func TestLibraryAgentSkillMutationTools_BlocksProtectedSystemAgents(t *testing.T) {
	for _, tc := range []struct {
		agent string
		skill string
		title string
	}{
		{agent: "skill_curator", skill: "maintain_skill_library", title: "Maintain"},
		{agent: "memory_curator", skill: "consolidate_memory", title: "Consolidate"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			imp, app, rec, projectRoot := buildTools(t)
			app.protected["skill:"+tc.agent+"/"+tc.skill] = "agent " + tc.agent + " is protected"
			tools := LibraryAgentSkillMutationTools(imp, rec)
			declaration := fmt.Sprintf("---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: %s\n---\n# %s\n", tc.skill, tc.title)
			params, _ := json.Marshal(map[string]any{"action": "patch", "agent": tc.agent, "scope": "project", "declaration": declaration})
			out, _, isErr, _ := tools.Executor(context.Background(), "agent_skill_manage", params)
			if isErr || !strings.Contains(out, "protected") {
				t.Fatalf("expected protected system agent block result, got isErr=%v out=%s", isErr, out)
			}
			if _, err := os.Stat(filepath.Join(projectRoot, "agents", tc.agent, "skills", tc.skill, "SKILL.md")); !os.IsNotExist(err) {
				t.Fatalf("protected system agent skill should not be written, stat err=%v", err)
			}
			if len(rec.rows) != 1 || rec.rows[0].applied || len(rec.rows[0].blocked) == 0 {
				t.Fatalf("blocked mutation should be recorded: %+v", rec.rows)
			}
		})
	}
}

func TestSkillImportTool_ImportsInlineRawSkillAndIndexesCatalog(t *testing.T) {
	imp, _, rec, projectRoot := buildTools(t)
	tools := SkillMutationTools(imp, rec)
	if tools == nil || !tools.HasDefinition("skill_import") {
		t.Fatalf("expected skill_import definition")
	}
	params, _ := json.Marshal(map[string]any{
		"content":      "# Inline Skill\n\nUse inline import.\n",
		"package_name": "inline_skill",
		"scope":        "project",
		"files": []map[string]any{{
			"path":    "references/guide.md",
			"content": "guide",
		}},
	})
	out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
	if err != nil || !handled || isErr {
		t.Fatalf("skill_import failed output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	if !strings.Contains(out, `"created"`) || !strings.Contains(out, "inline_skill") {
		t.Fatalf("expected confirmation JSON with imported handle, got %s", out)
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, "skills", "inline_skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read imported skill: %v", err)
	}
	for _, want := range []string{"kind: openvibely.agent_skill", "key: inline_skill", "enabled: true", "# Inline Skill"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("imported SKILL.md missing %q:\n%s", want, data)
		}
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "skills", "inline_skill", "references", "guide.md")); err != nil {
		t.Fatalf("expected support file: %v", err)
	}
	catalog, err := agentskills.BuildCatalog("test", projectRoot, "")
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	if _, ok := catalog.Lookup("inline_skill"); !ok {
		t.Fatalf("imported skill not in catalog")
	}
	if len(rec.rows) != 1 || rec.rows[0].action != "import" || rec.rows[0].target != "skill" || rec.rows[0].key != "inline_skill" || !rec.rows[0].applied {
		t.Fatalf("unexpected recorder rows: %+v", rec.rows)
	}
}

func TestSkillImportTool_RejectsInvalidInlineSupportPathsWithoutChanges(t *testing.T) {
	content := "# Partial Import\n\nThis content must not be persisted.\n"

	for _, invalidPath := range []string{"../outside.md", "/outside.md", "unsupported/guide.md", "references/foo..bar.md", "references/foo\x00bar.md"} {
		t.Run(invalidPath, func(t *testing.T) {
			imp, _, rec, projectRoot := buildTools(t)
			tools := SkillMutationTools(imp, rec)
			params, _ := json.Marshal(map[string]any{
				"content":      content,
				"package_name": "partial_import",
				"scope":        "project",
				"files": []map[string]any{
					{"path": "references/valid.md", "content": "valid"},
					{"path": invalidPath, "content": "invalid"},
				},
			})

			out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
			if err != nil || !handled || !isErr {
				t.Fatalf("skill_import should reject invalid support path: output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
			}
			if _, err := os.Stat(filepath.Join(projectRoot, "skills", "partial_import")); !os.IsNotExist(err) {
				t.Fatalf("rejected import created skill directory: %v", err)
			}
			if _, err := os.Stat(filepath.Join(projectRoot, "skills", "SKILLS.md")); !os.IsNotExist(err) {
				t.Fatalf("rejected import created skill index: %v", err)
			}
			if len(rec.rows) != 1 || rec.rows[0].applied {
				t.Fatalf("rejected mutation should be recorded as unapplied: %+v", rec.rows)
			}
		})
	}

	for _, invalidPath := range []string{"references/foo..bar.md", "references/foo\x00bar.md"} {
		t.Run("existing skill/"+strings.ReplaceAll(invalidPath, "\x00", "\\x00"), func(t *testing.T) {
			imp, _, rec, projectRoot := buildTools(t)
			tools := SkillMutationTools(imp, rec)
			if _, err := imp.ImportSkillPackage(context.Background(), "# Existing Import\n", "partial_import", "project", []SkillPackageFile{{Path: "references/original.md", Content: []byte("original")}}); err != nil {
				t.Fatalf("seed existing skill: %v", err)
			}

			skillPath := filepath.Join(projectRoot, "skills", "partial_import", "SKILL.md")
			indexPath := filepath.Join(projectRoot, "skills", "SKILLS.md")
			supportPath := filepath.Join(projectRoot, "skills", "partial_import", "references", "original.md")
			beforeSkill, err := os.ReadFile(skillPath)
			if err != nil {
				t.Fatalf("read seeded skill: %v", err)
			}
			beforeIndex, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatalf("read seeded index: %v", err)
			}
			beforeSupport, err := os.ReadFile(supportPath)
			if err != nil {
				t.Fatalf("read seeded support file: %v", err)
			}

			params, _ := json.Marshal(map[string]any{
				"content":      content,
				"package_name": "partial_import",
				"scope":        "project",
				"files": []map[string]any{
					{"path": "references/new.md", "content": "new"},
					{"path": invalidPath, "content": "invalid"},
				},
			})
			out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
			if err != nil || !handled || !isErr {
				t.Fatalf("skill_import should reject invalid support path: output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
			}
			for _, file := range []struct {
				path string
				want []byte
			}{
				{skillPath, beforeSkill},
				{indexPath, beforeIndex},
				{supportPath, beforeSupport},
			} {
				got, err := os.ReadFile(file.path)
				if err != nil || string(got) != string(file.want) {
					t.Fatalf("rejected import changed %s: got=%q err=%v want=%q", file.path, got, err, file.want)
				}
			}
			if _, err := os.Stat(filepath.Join(projectRoot, "skills", "partial_import", "references", "new.md")); !os.IsNotExist(err) {
				t.Fatalf("rejected import created earlier valid support file: %v", err)
			}
			if len(rec.rows) != 1 || rec.rows[0].applied {
				t.Fatalf("rejected mutation should be recorded as unapplied: %+v", rec.rows)
			}
		})
	}
}

func TestSkillImportTool_RollsBackInlineSupportWriteFailure(t *testing.T) {
	content := "# Write Failure\n\nThis content must not be persisted.\n"

	for _, tc := range []struct {
		name string
		seed bool
	}{
		{name: "new package", seed: false},
		{name: "existing skill", seed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imp, _, rec, projectRoot := buildTools(t)
			tools := SkillMutationTools(imp, rec)
			if tc.seed {
				if _, err := imp.ImportSkillPackage(context.Background(), "# Existing Write Failure\n", "write_failure", "project", []SkillPackageFile{{Path: "references/original.md", Content: []byte("original")}}); err != nil {
					t.Fatalf("seed existing skill: %v", err)
				}
			}

			skillDir := filepath.Join(projectRoot, "skills", "write_failure")
			conflictPath := filepath.Join(skillDir, "references", "conflict.md")
			if err := os.MkdirAll(conflictPath, 0o755); err != nil {
				t.Fatalf("create support path conflict: %v", err)
			}
			conflictFile := filepath.Join(conflictPath, "keep.md")
			if err := os.WriteFile(conflictFile, []byte("keep"), 0o644); err != nil {
				t.Fatalf("seed conflict directory: %v", err)
			}

			skillPath := filepath.Join(skillDir, "SKILL.md")
			indexPath := filepath.Join(projectRoot, "skills", "SKILLS.md")
			beforeSkill, skillErr := os.ReadFile(skillPath)
			beforeIndex, indexErr := os.ReadFile(indexPath)
			beforeOriginal, originalErr := os.ReadFile(filepath.Join(skillDir, "references", "original.md"))
			beforeConflict, err := os.ReadFile(conflictFile)
			if err != nil {
				t.Fatalf("read conflict sentinel: %v", err)
			}

			params, _ := json.Marshal(map[string]any{
				"content":      content,
				"package_name": "write_failure",
				"scope":        "project",
				"files": []map[string]any{
					{"path": "references/new.md", "content": "new"},
					{"path": "references/conflict.md", "content": "invalid"},
				},
			})
			out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
			if err != nil || !handled || !isErr {
				t.Fatalf("skill_import should reject support write failure: output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
			}

			for _, file := range []struct {
				path string
				data []byte
				err  error
			}{
				{skillPath, beforeSkill, skillErr},
				{indexPath, beforeIndex, indexErr},
				{filepath.Join(skillDir, "references", "original.md"), beforeOriginal, originalErr},
				{conflictFile, beforeConflict, nil},
			} {
				got, err := os.ReadFile(file.path)
				if errors.Is(file.err, os.ErrNotExist) {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("rejected import created %s: got=%q err=%v", file.path, got, err)
					}
					continue
				}
				if err != nil || string(got) != string(file.data) {
					t.Fatalf("rejected import changed %s: got=%q err=%v want=%q", file.path, got, err, file.data)
				}
			}
			if info, err := os.Stat(conflictPath); err != nil || !info.IsDir() {
				t.Fatalf("rejected import changed support path conflict: info=%v err=%v", info, err)
			}
			if _, err := os.Stat(filepath.Join(skillDir, "references", "new.md")); !os.IsNotExist(err) {
				t.Fatalf("rejected import created earlier valid support file: %v", err)
			}
			if len(rec.rows) != 1 || rec.rows[0].applied {
				t.Fatalf("rejected mutation should be recorded as unapplied: %+v", rec.rows)
			}
		})
	}
}

func TestSkillImportTool_ContainsHardLinkedInlineTargetsOnRollback(t *testing.T) {
	const (
		key             = "hard_link_import"
		originalContent = "# Existing Hard Link\n\nOriginal package content.\n"
		updatedContent  = "# Updated Hard Link\n\nUpdated package content.\n"
	)

	for _, target := range []struct {
		name       string
		targetPath func(skillPath, indexPath, supportPath string) string
		files      []map[string]any
	}{
		{
			name: "skill file",
			targetPath: func(skillPath, _, _ string) string {
				return skillPath
			},
			files: []map[string]any{{"path": "references/new.md", "content": "new"}},
		},
		{
			name: "catalog index",
			targetPath: func(_, indexPath, _ string) string {
				return indexPath
			},
			files: []map[string]any{{"path": "references/new.md", "content": "new"}},
		},
		{
			name: "support file",
			targetPath: func(_, _, supportPath string) string {
				return supportPath
			},
			files: []map[string]any{{"path": "references/original.md", "content": "updated"}},
		},
	} {
		t.Run(target.name, func(t *testing.T) {
			imp, _, rec, projectRoot := buildTools(t)
			tools := SkillMutationTools(imp, rec)
			if _, err := imp.ImportSkillPackage(context.Background(), originalContent, key, "project", []SkillPackageFile{{Path: "references/original.md", Content: []byte("original")}}); err != nil {
				t.Fatalf("seed existing skill: %v", err)
			}

			skillDir := filepath.Join(projectRoot, "skills", key)
			skillPath := filepath.Join(skillDir, "SKILL.md")
			indexPath := filepath.Join(projectRoot, "skills", "SKILLS.md")
			supportPath := filepath.Join(skillDir, "references", "original.md")
			localTarget := target.targetPath(skillPath, indexPath, supportPath)
			externalTarget := filepath.Join(t.TempDir(), strings.ReplaceAll(target.name, " ", "_")+".md")
			localData, err := os.ReadFile(localTarget)
			if err != nil {
				t.Fatalf("read local hard-link target: %v", err)
			}
			if err := os.WriteFile(externalTarget, localData, 0o644); err != nil {
				t.Fatalf("seed external hard-link target: %v", err)
			}
			if err := os.Remove(localTarget); err != nil {
				t.Fatalf("remove local hard-link target: %v", err)
			}
			if err := os.Link(externalTarget, localTarget); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}

			conflictPath := filepath.Join(skillDir, "references", "conflict.md")
			if err := os.Mkdir(conflictPath, 0o755); err != nil {
				t.Fatalf("create support conflict: %v", err)
			}
			conflictFile := filepath.Join(conflictPath, "keep.md")
			if err := os.WriteFile(conflictFile, []byte("keep"), 0o644); err != nil {
				t.Fatalf("seed support conflict: %v", err)
			}

			beforeSkill, err := os.ReadFile(skillPath)
			if err != nil {
				t.Fatalf("read skill before import: %v", err)
			}
			beforeIndex, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatalf("read index before import: %v", err)
			}
			beforeSupport, err := os.ReadFile(supportPath)
			if err != nil {
				t.Fatalf("read support before import: %v", err)
			}
			beforeExternal, err := os.ReadFile(externalTarget)
			if err != nil {
				t.Fatalf("read external hard-link target: %v", err)
			}
			beforeConflict, err := os.ReadFile(conflictFile)
			if err != nil {
				t.Fatalf("read support conflict: %v", err)
			}

			files := append([]map[string]any(nil), target.files...)
			files = append(files, map[string]any{"path": "references/conflict.md", "content": "invalid"})
			params, _ := json.Marshal(map[string]any{
				"content":      updatedContent,
				"package_name": key,
				"scope":        "project",
				"files":        files,
			})
			out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
			if err != nil || !handled || !isErr {
				t.Fatalf("skill_import should reject support write failure: output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
			}

			for _, file := range []struct {
				path string
				want []byte
			}{
				{skillPath, beforeSkill},
				{indexPath, beforeIndex},
				{supportPath, beforeSupport},
				{conflictFile, beforeConflict},
				{externalTarget, beforeExternal},
			} {
				got, err := os.ReadFile(file.path)
				if err != nil || string(got) != string(file.want) {
					t.Fatalf("rejected import changed %s: got=%q err=%v want=%q", file.path, got, err, file.want)
				}
			}
			if _, err := os.Stat(filepath.Join(skillDir, "references", "new.md")); !os.IsNotExist(err) {
				t.Fatalf("rejected import created earlier valid support file: %v", err)
			}
			if len(rec.rows) != 1 || rec.rows[0].applied {
				t.Fatalf("rejected mutation should be recorded as unapplied: %+v", rec.rows)
			}
		})
	}
}

func TestSkillImportTool_RejectsSymlinkedInlineSupportPathWithoutChanges(t *testing.T) {
	content := "# Symlinked Support Path\n\nThis content must not be persisted.\n"

	for _, tc := range []struct {
		name string
		seed bool
	}{
		{name: "unindexed package", seed: false},
		{name: "existing skill", seed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imp, _, rec, projectRoot := buildTools(t)
			tools := SkillMutationTools(imp, rec)
			if tc.seed {
				if _, err := imp.ImportSkillPackage(context.Background(), "# Existing Symlink\n", "symlinked_support", "project", []SkillPackageFile{{Path: "references/original.md", Content: []byte("original")}}); err != nil {
					t.Fatalf("seed existing skill: %v", err)
				}
			}

			skillDir := filepath.Join(projectRoot, "skills", "symlinked_support")
			if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
				t.Fatalf("create support directory: %v", err)
			}
			externalDir := t.TempDir()
			linkPath := filepath.Join(skillDir, "references", "leak")
			if err := os.Symlink(externalDir, linkPath); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}

			skillPath := filepath.Join(skillDir, "SKILL.md")
			indexPath := filepath.Join(projectRoot, "skills", "SKILLS.md")
			originalPath := filepath.Join(skillDir, "references", "original.md")
			beforeSkill, skillErr := os.ReadFile(skillPath)
			beforeIndex, indexErr := os.ReadFile(indexPath)
			beforeOriginal, originalErr := os.ReadFile(originalPath)
			beforeLink, err := os.Readlink(linkPath)
			if err != nil {
				t.Fatalf("read support link: %v", err)
			}

			params, _ := json.Marshal(map[string]any{
				"content":      content,
				"package_name": "symlinked_support",
				"scope":        "project",
				"files": []map[string]any{
					{"path": "references/new.md", "content": "new"},
					{"path": "references/leak/external.md", "content": "external"},
				},
			})
			out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
			if err != nil || !handled || !isErr {
				t.Fatalf("skill_import should reject symlinked support path: output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
			}

			for _, file := range []struct {
				path string
				data []byte
				err  error
			}{
				{skillPath, beforeSkill, skillErr},
				{indexPath, beforeIndex, indexErr},
				{originalPath, beforeOriginal, originalErr},
			} {
				got, err := os.ReadFile(file.path)
				if errors.Is(file.err, os.ErrNotExist) {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("rejected import created %s: got=%q err=%v", file.path, got, err)
					}
					continue
				}
				if err != nil || string(got) != string(file.data) {
					t.Fatalf("rejected import changed %s: got=%q err=%v want=%q", file.path, got, err, file.data)
				}
			}
			if target, err := os.Readlink(linkPath); err != nil || target != beforeLink {
				t.Fatalf("rejected import changed support link: target=%q err=%v want=%q", target, err, beforeLink)
			}
			if _, err := os.Stat(filepath.Join(skillDir, "references", "new.md")); !os.IsNotExist(err) {
				t.Fatalf("rejected import created earlier valid support file: %v", err)
			}
			if _, err := os.Stat(filepath.Join(externalDir, "external.md")); !os.IsNotExist(err) {
				t.Fatalf("rejected import wrote through support link: %v", err)
			}
			if len(rec.rows) != 1 || rec.rows[0].applied {
				t.Fatalf("rejected mutation should be recorded as unapplied: %+v", rec.rows)
			}
		})
	}
}

func TestSkillImportTool_RejectsSymlinkedInlineSkillFileWithoutChanges(t *testing.T) {
	imp, _, rec, projectRoot := buildTools(t)
	tools := SkillMutationTools(imp, rec)

	skillDir := filepath.Join(projectRoot, "skills", "symlinked_main")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatalf("create skill package: %v", err)
	}
	externalDir := t.TempDir()
	externalSkill := filepath.Join(externalDir, "SKILL.md")
	if err := os.WriteFile(externalSkill, []byte("# External skill\n"), 0o644); err != nil {
		t.Fatalf("seed external skill: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.Symlink(externalSkill, skillPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	indexPath := filepath.Join(projectRoot, "skills", "SKILLS.md")
	if err := os.WriteFile(indexPath, []byte("# Existing catalog\n"), 0o644); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	originalPath := filepath.Join(skillDir, "references", "original.md")
	if err := os.WriteFile(originalPath, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed original support: %v", err)
	}
	conflictPath := filepath.Join(skillDir, "references", "conflict.md")
	if err := os.Mkdir(conflictPath, 0o755); err != nil {
		t.Fatalf("create support conflict: %v", err)
	}
	conflictFile := filepath.Join(conflictPath, "keep.md")
	if err := os.WriteFile(conflictFile, []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed support conflict: %v", err)
	}

	beforeExternalSkill, err := os.ReadFile(externalSkill)
	if err != nil {
		t.Fatalf("read external skill: %v", err)
	}
	beforeIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	beforeOriginal, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read original support: %v", err)
	}
	beforeConflict, err := os.ReadFile(conflictFile)
	if err != nil {
		t.Fatalf("read support conflict: %v", err)
	}
	beforeLink, err := os.Readlink(skillPath)
	if err != nil {
		t.Fatalf("read skill link: %v", err)
	}

	params, _ := json.Marshal(map[string]any{
		"content":      "# Unsafe main skill link\n",
		"package_name": "symlinked_main",
		"scope":        "project",
		"files": []map[string]any{
			{"path": "references/new.md", "content": "new"},
			{"path": "references/conflict.md", "content": "invalid"},
		},
	})
	out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
	if err != nil || !handled || !isErr {
		t.Fatalf("skill_import should reject symlinked main skill: output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}

	for _, file := range []struct {
		path string
		want []byte
	}{
		{externalSkill, beforeExternalSkill},
		{indexPath, beforeIndex},
		{originalPath, beforeOriginal},
		{conflictFile, beforeConflict},
	} {
		got, err := os.ReadFile(file.path)
		if err != nil || string(got) != string(file.want) {
			t.Fatalf("rejected import changed %s: got=%q err=%v want=%q", file.path, got, err, file.want)
		}
	}
	if target, err := os.Readlink(skillPath); err != nil || target != beforeLink {
		t.Fatalf("rejected import changed skill link: target=%q err=%v want=%q", target, err, beforeLink)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "references", "new.md")); !os.IsNotExist(err) {
		t.Fatalf("rejected import created earlier valid support file: %v", err)
	}
	if len(rec.rows) != 1 || rec.rows[0].applied {
		t.Fatalf("rejected mutation should be recorded as unapplied: %+v", rec.rows)
	}
}

func TestSkillImportTool_RejectsConfiguredSkillsDirectorySymlinkWithoutChanges(t *testing.T) {
	imp, _, rec, projectRoot := buildTools(t)
	tools := SkillMutationTools(imp, rec)
	externalSkills := t.TempDir()
	externalIndex := filepath.Join(externalSkills, "SKILLS.md")
	if err := os.WriteFile(externalIndex, []byte("# External catalog\n"), 0o644); err != nil {
		t.Fatalf("seed external index: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(externalSkills, "library_escape", "references"), 0o755); err != nil {
		t.Fatalf("seed external package: %v", err)
	}
	externalSkill := filepath.Join(externalSkills, "library_escape", "SKILL.md")
	if err := os.WriteFile(externalSkill, []byte("# External skill\n"), 0o644); err != nil {
		t.Fatalf("seed external skill: %v", err)
	}
	externalSupport := filepath.Join(externalSkills, "library_escape", "references", "original.md")
	if err := os.WriteFile(externalSupport, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed external support: %v", err)
	}
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	if err := os.Symlink(externalSkills, filepath.Join(projectRoot, "skills")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	beforeIndex, err := os.ReadFile(externalIndex)
	if err != nil {
		t.Fatalf("read external index: %v", err)
	}
	beforeSkill, err := os.ReadFile(externalSkill)
	if err != nil {
		t.Fatalf("read external skill: %v", err)
	}
	beforeSupport, err := os.ReadFile(externalSupport)
	if err != nil {
		t.Fatalf("read external support: %v", err)
	}
	params, _ := json.Marshal(map[string]any{
		"content":      "# Library Escape\n",
		"package_name": "library_escape",
		"scope":        "project",
		"files": []map[string]any{{
			"path":    "references/new.md",
			"content": "new",
		}},
	})
	out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
	if err != nil || !handled || !isErr {
		t.Fatalf("skill_import should reject configured skills symlink: output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	for _, file := range []struct {
		path string
		want []byte
	}{
		{externalIndex, beforeIndex},
		{externalSkill, beforeSkill},
		{externalSupport, beforeSupport},
	} {
		got, err := os.ReadFile(file.path)
		if err != nil || string(got) != string(file.want) {
			t.Fatalf("rejected import changed external file %s: got=%q err=%v want=%q", file.path, got, err, file.want)
		}
	}
	if _, err := os.Stat(filepath.Join(externalSkills, "library_escape", "references", "new.md")); !os.IsNotExist(err) {
		t.Fatalf("rejected import wrote external support file: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(projectRoot, "skills")); err != nil || target != externalSkills {
		t.Fatalf("rejected import changed configured skills link: target=%q err=%v want=%q", target, err, externalSkills)
	}
	if len(rec.rows) != 1 || rec.rows[0].applied {
		t.Fatalf("rejected mutation should be recorded as unapplied: %+v", rec.rows)
	}
}

func TestSkillImportTool_ImportsDirectorySource(t *testing.T) {
	imp, _, _, projectRoot := buildTools(t)
	source := filepath.Join(t.TempDir(), "dir_skill")
	if err := os.MkdirAll(filepath.Join(source, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: Directory Skill\ndescription: From directory.\n---\n# Directory Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "templates", "prompt.md"), []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := SkillMutationTools(imp, nil)
	params, _ := json.Marshal(map[string]any{"source_path": source, "scope": "project"})
	out, handled, isErr, err := tools.Executor(context.Background(), "skill_import", params)
	if err != nil || !handled || isErr {
		t.Fatalf("skill_import path failed output=%s handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, "skills", "dir_skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read imported skill: %v", err)
	}
	if !strings.Contains(string(data), "name: Directory Skill") || !strings.Contains(string(data), "enabled: true") {
		t.Fatalf("directory skill not normalized:\n%s", data)
	}
	if got, err := os.ReadFile(filepath.Join(projectRoot, "skills", "dir_skill", "templates", "prompt.md")); err != nil || string(got) != "prompt" {
		t.Fatalf("template not imported got=%q err=%v", got, err)
	}
}
