package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/builtinskills"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestSkillCuratorMaintenanceReadsWithoutDependencyPreparation(t *testing.T) {
	root := t.TempDir()
	body := fmt.Sprintf(`---
kind: openvibely.agent_skill
version: 1
skill:
  key: document_fixture
  enabled: true
dependencies:
  - name: curator-inspection-missing-fixture
    version: 0.0.0-test
    health_check: [--version]
    packages:
      %s/%s:
        url: https://example.invalid/curator-fixture
        sha256: "%s"
        format: raw
        executable: fixture
---
DOCUMENT_INSTRUCTIONS
`, runtime.GOOS, runtime.GOARCH, strings.Repeat("0", 64))
	writeLifecycleStandaloneSkill(t, root, "document_fixture", body)
	writeLifecycleTestSkill(t, root, "skill_curator", "document_fixture", body)
	t.Setenv(skilldeps.OverrideVariable("curator-inspection-missing-fixture"), "")
	catalog, err := agentskills.BuildCatalog("task", root, "")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	svc.SetGlobalSkillRoot(root)
	for _, tc := range []struct {
		name         string
		kind         string
		category     models.TaskCategory
		followup     bool
		tools        []string
		handle       string
		wantApproval bool
	}{
		{"maintenance library", models.AgentSystemKindSkillCurator, models.CategoryScheduled, false, []string{"skill_view", "skills_list"}, "standalone:document_fixture", false},
		{"maintenance selected only", models.AgentSystemKindSkillCurator, models.CategoryScheduled, false, []string{"skill_view"}, "document_fixture", false},
		{"ordinary execution", "", models.CategoryActive, false, []string{"skill_view"}, "document_fixture", true},
		{"curator followup", models.AgentSystemKindSkillCurator, models.CategoryScheduled, true, []string{"skill_view"}, "document_fixture", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked, progressed := false, false
			ctx := withLifecycleTurnContext(context.Background(), lifecycleTurnContext{Catalog: catalog, TaskThreadTurn: tc.followup})
			ctx = skilldeps.WithApproval(ctx, "project", func(context.Context, skilldeps.InstallationRequest) error {
				asked = true
				return skilldeps.ErrApprovalRequired // Never approve or download fixture dependencies.
			})
			ctx = skilldeps.WithProgress(ctx, func(skilldeps.Progress) { progressed = true })
			agent := &models.Agent{ID: "agent", Key: "skill_curator", Enabled: true, SystemKind: tc.kind, Tools: tc.tools}
			task := models.Task{ID: "task", ProjectID: "project", Category: tc.category}
			tools := svc.agentDeclaredSkillRuntimeTools(ctx, task, agent, "")
			if tools == nil {
				t.Fatal("missing skill reader")
			}
			input, _ := json.Marshal(map[string]string{"handle": tc.handle})
			out, handled, failed, err := tools.Executor(ctx, "skill_view", input)
			if err != nil || !handled {
				t.Fatalf("skill_view: handled=%v err=%v output=%s", handled, err, out)
			}
			if asked != tc.wantApproval {
				t.Fatalf("installation prompt=%v, want %v; output=%s", asked, tc.wantApproval, out)
			}
			if tc.wantApproval {
				if !failed || strings.Contains(out, "DOCUMENT_INSTRUCTIONS") {
					t.Fatalf("execution bypassed approval: %s", out)
				}
			} else if failed || progressed || !strings.Contains(out, "DOCUMENT_INSTRUCTIONS") {
				t.Fatalf("inspection should return instructions without dependency preparation: failed=%v progress=%v output=%s", failed, progressed, out)
			}
		})
	}
}

func TestSkillCuratorOmitsBundledLibrarySkills(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	root := t.TempDir()
	if err := builtinskills.SyncTo(root); err != nil {
		t.Fatal(err)
	}
	writeLifecycleStandaloneSkill(t, root, "user_skill", "USER_INSTRUCTIONS")
	agent, err := stores.Agents.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil || agent == nil {
		t.Fatalf("load curator: %v", err)
	}
	svc := NewLLMService(nil, nil, nil, stores.Projects, nil, nil)
	svc.SetGlobalSkillRoot(root)
	svc.SetAgentRepo(stores.Agents)
	svc.SetLifecycleRepo(stores.Lifecycle)
	task := models.Task{ID: "curator-task", ProjectID: project.ID, Category: models.CategoryScheduled}
	tools := svc.agentDeclaredSkillRuntimeTools(ctx, task, agent, "")
	call := func(name string, args map[string]string, wantFailed bool) string {
		t.Helper()
		input, _ := json.Marshal(args)
		out, handled, failed, err := tools.Executor(ctx, name, input)
		if err != nil || !handled || failed != wantFailed {
			t.Fatalf("%s %s: handled=%v failed=%v err=%v output=%s", name, input, handled, failed, err, out)
		}
		return out
	}
	catalog, err := agentskills.BuildCatalog("normal", root, "")
	if err != nil {
		t.Fatal(err)
	}
	listed := call("skills_list", map[string]string{"scope": "all"}, false)
	for _, entry := range catalog.Entries() {
		if entry.Skill == "user_skill" {
			continue
		}
		if strings.Contains(listed, entry.Skill) {
			t.Errorf("bundled skill %s exposed in maintenance index", entry.Skill)
		}
		for _, handle := range []string{entry.Skill, "standalone:" + entry.Skill, "skill:" + entry.Skill} {
			call("skill_view", map[string]string{"handle": handle}, true)
			call("skill_view", map[string]string{"handle": handle, "file_path": "references/creation.md"}, true)
		}
	}
	if !strings.Contains(listed, "standalone:user_skill") {
		t.Fatal("user skill omitted from discovery")
	}
	if out := call("skill_view", map[string]string{"handle": "user_skill"}, false); !strings.Contains(out, "USER_INSTRUCTIONS") {
		t.Fatal(out)
	}
	// Curator instructions are required to perform the maintenance task itself.
	call("skill_view", map[string]string{"handle": "agent:skill_curator/maintain_skill_library"}, false)
	// Ordinary readers still see the bundled library.
	out, _, failed, err := agentskills.SkillRuntimeTools(catalog, root, "", nil).Executor(ctx, "skill_view", json.RawMessage(`{"handle":"document_word"}`))
	if err != nil || failed || !strings.Contains(out, "paperworks") {
		t.Fatalf("ordinary skill reader lost bundled skill: %s %v", out, err)
	}
	// The policy is tied to app-owned global files, not reserved skill names.
	// Creating a project override after reader construction must refresh visibility.
	writeLifecycleStandaloneSkill(t, filepath.Join(project.RepoPath, ".openvibely"), "document_word", "USER_OVERRIDE")
	out = call("skill_view", map[string]string{"handle": "standalone:document_word"}, false)
	if !strings.Contains(out, "USER_OVERRIDE") {
		t.Fatal(out)
	}
	listed = call("skills_list", map[string]string{"scope": "project"}, false)
	if !strings.Contains(listed, "standalone:document_word") {
		t.Fatal("user override omitted from project discovery")
	}
	listed = call("skills_list", map[string]string{"scope": "global"}, false)
	if strings.Contains(listed, "document_word") {
		t.Fatal("global bundled skill leaked after project override")
	}
}

func TestCuratorFiltersBundledCaseAliases(t *testing.T) {
	for _, key := range []string{"DOCUMENT_WORD", "Document_Word"} {
		t.Run(key, func(t *testing.T) {
			global, project := t.TempDir(), t.TempDir()
			// Materialize the alias so this also exercises the policy on Linux.
			writeLifecycleStandaloneSkill(t, global, key, "BUNDLED_ALIAS")
			writeLifecycleStandaloneSkill(t, global, "user_skill", "USER_SKILL")
			check := func(projectRoot string, wantVisible bool) {
				t.Helper()
				catalog, err := agentskills.BuildCatalog("audit", global, projectRoot)
				if err != nil {
					t.Fatal(err)
				}
				tools := agentskills.FilteredSkillRuntimeTools(catalog, global, projectRoot, nil, func(path string) bool {
					return curatorMayInspectSkill(global, path)
				})
				out, handled, failed, err := tools.Executor(context.Background(), "skills_list", json.RawMessage(`{"scope":"all"}`))
				if err != nil || !handled || failed || strings.Contains(out, key) != wantVisible || !strings.Contains(out, "user_skill") {
					t.Fatalf("unexpected discovery: %s (%v)", out, err)
				}
				for _, handle := range []string{key, "standalone:" + key, "skill:" + key} {
					args, _ := json.Marshal(map[string]string{"handle": handle})
					out, handled, failed, err = tools.Executor(context.Background(), "skill_view", args)
					if err != nil || !handled || failed == wantVisible {
						t.Fatalf("unexpected view: %s (%v)", out, err)
					}
					if wantVisible && !strings.Contains(out, "PROJECT_OVERRIDE") {
						t.Fatal(out)
					}
				}
			}
			check("", false)
			writeLifecycleStandaloneSkill(t, project, key, "PROJECT_OVERRIDE")
			check(project, true)
		})
	}
	root := t.TempDir()
	if !curatorMayInspectSkill(root, filepath.Join(root, "AGENTS", "SKILL_CURATOR", "SKILLS", "MAINTAIN_SKILL_LIBRARY", "SKILL.md")) {
		t.Fatal("curator must retain access to its own instructions")
	}
}
