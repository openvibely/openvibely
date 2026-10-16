package service

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestFileReferencesReachExistingSkillCurator(t *testing.T) {
	for _, format := range []string{"pdf", "docx", "xlsx", "pptx"} {
		for _, chat := range []bool{false, true} {
			name := "report." + format
			root := t.TempDir()
			writeLifecycleStandaloneSkill(t, root, format, "Render and inspect with available image tools.")
			ctx := WithAttachedFiles(context.Background(), []models.Attachment{{FileName: name, FilePath: "/files/" + name, MediaType: "application/test"}})
			store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
			called := false
			runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, _ models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
				called = true
				if !strings.Contains(in.TaskPrompt, name) || !strings.Contains(in.TaskPrompt, "/files/"+name) || !strings.Contains(in.TaskPrompt, "summarize") {
					t.Errorf("curator missing user request or file references: %q", in.TaskPrompt)
				}
				return routePayload([]string{format}, 0.9), nil
			}), nil)
			worker := NewWorkerService(nil, 0, nil)
			worker.SetLifecycleRunner(runner)
			worker.SetLifecycleSkillRoot(root)
			task := models.Task{ID: "task", ProjectID: "project", Prompt: "summarize this"}
			var turn LifecycleTurn
			if chat {
				turn = worker.PrepareRecallOnlyLifecycleTurn(ctx, task)
			} else {
				turn = worker.PrepareLifecycleTurn(ctx, task)
			}
			tools := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
			if tools == nil || !tools.HasDefinition("skill_view") {
				t.Fatal("selected document skill was not exposed")
			}
			raw, _ := json.Marshal(map[string]string{"handle": format})
			body, handled, failed, err := tools.Executor(turn.Ctx, "skill_view", raw)
			if !handled || failed || err != nil || !strings.Contains(body, "Render and inspect") {
				t.Fatalf("skill unavailable: %s, %v", body, err)
			}

			if !called {
				t.Fatalf("%s chat=%v: skill curator was skipped", format, chat)
			}
		}
	}
}
