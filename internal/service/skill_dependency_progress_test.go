package service

import (
	"context"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"testing"
)

type dependencyExecutionStore struct {
	persistence.ExecutionStore
	calls int
}

func (s *dependencyExecutionStore) GetByID(context.Context, string) (*models.Execution, error) {
	s.calls++
	return &models.Execution{TaskID: "task"}, nil
}
func TestDependencyProgressScopesEventsAndSkipsLifecycle(t *testing.T) {
	broadcaster := events.NewBroadcaster()
	sub, err := broadcaster.SubscribeProject("project")
	if err != nil {
		t.Fatal(err)
	}
	defer broadcaster.Unsubscribe(sub)
	store := &dependencyExecutionStore{}
	svc := &LLMService{broadcaster: broadcaster, execRepo: store}
	req := llmcontracts.AgentRequest{Ctx: context.Background(), ExecID: "execution", ProjectID: "project"}
	ctx := svc.withDependencyProgress(req)
	for _, stage := range []string{"downloading", "ready"} {
		skilldeps.ProgressFromContext(ctx)(skilldeps.Progress{Name: "paperworks", Stage: stage})
		select {
		case event := <-sub:
			if event.Type != events.SkillDependencyProgress || event.TaskID != "task" || event.ExecID != "execution" || event.ProjectID != "project" || event.Phase != stage {
				t.Fatalf("%+v", event)
			}
		default:
			t.Fatal("missing progress event")
		}
	}
	if store.calls != 1 {
		t.Fatalf("lookups: %d", store.calls)
	}
	req.LifecycleHookCall = true
	if skilldeps.ProgressFromContext(svc.withDependencyProgress(req)) != nil {
		t.Fatal("lifecycle progress enabled")
	}
}
