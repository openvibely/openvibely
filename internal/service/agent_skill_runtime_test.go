package service

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/skilldeps"
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
