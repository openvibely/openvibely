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
