package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

type fakeProjectResolver struct{ workDir string }

func (f fakeProjectResolver) ResolveWorkDir(context.Context, string) string { return f.workDir }

type fakeAgentCaller struct {
	message string
	agent   models.LLMConfig
	workDir string
	err     error
}

func (f *fakeAgentCaller) CallAgentDirect(_ context.Context, message string, agent models.LLMConfig, workDir string) (string, error) {
	f.message = message
	f.agent = agent
	f.workDir = workDir
	if f.err != nil {
		return "", f.err
	}
	return "agent output", nil
}

type fakeTaskCreator struct {
	created []*models.Task
	err     error
}

func (f *fakeTaskCreator) Create(_ context.Context, task *models.Task) error {
	if f.err != nil {
		return f.err
	}
	if task.ID == "" {
		task.ID = "created-task-" + string(rune('0'+len(f.created)+1))
	}
	copyTask := *task
	f.created = append(f.created, &copyTask)
	return nil
}

type fakeLineageResolver struct {
	branch string
	sha    string
	err    error
}

func (f fakeLineageResolver) ResolveParentLineage(context.Context, models.Task) (string, string, error) {
	return f.branch, f.sha, f.err
}

func TestServiceCallAgentForWorkflowUsesResolvedWorkDir(t *testing.T) {
	caller := &fakeAgentCaller{}
	svc := NewService(fakeProjectResolver{workDir: "/repo"}, nil, caller)
	agent := &models.LLMConfig{ID: "agent-1", Provider: "test"}

	out, err := svc.CallAgentForWorkflow(context.Background(), "prompt", agent, "project-1")
	if err != nil || out != "agent output" {
		t.Fatalf("CallAgentForWorkflow = %q, %v", out, err)
	}
	if caller.message != "prompt" || caller.agent.ID != "agent-1" || caller.workDir != "/repo" {
		t.Fatalf("caller captured message=%q agent=%#v workDir=%q", caller.message, caller.agent, caller.workDir)
	}
}

func TestCleanOutputForChainStripsControlMarkersOutsideCode(t *testing.T) {
	input := "[Thinking]\nprivate\n[/Thinking]\nkeep\n[Using tool: bash]\n[Tool bash done: ok]\n```text\n[Thinking]\ncode stays\n[/Thinking]\n```"
	got := CleanOutputForChain(input)
	if strings.Contains(got, "private") || strings.Contains(got, "Using tool") || strings.Contains(got, "Tool bash") {
		t.Fatalf("control markers were not stripped: %q", got)
	}
	if !strings.Contains(got, "keep") || !strings.Contains(got, "code stays") {
		t.Fatalf("meaningful output was stripped: %q", got)
	}
}

func TestTriggerTaskChainCreatesChildAndBlockedGrandchild(t *testing.T) {
	creator := &fakeTaskCreator{}
	svc := NewService(nil, creator, &fakeAgentCaller{})
	svc.SetLineageResolver(fakeLineageResolver{branch: "main", sha: "abc123"})
	parent := models.Task{
		ID:           "parent-1",
		ProjectID:    "project-1",
		Title:        "Plan feature",
		Category:     models.CategoryCompleted,
		Status:       models.StatusCompleted,
		Priority:     3,
		Tag:          "feature",
		LineageDepth: 2,
	}
	if err := parent.SetChainConfig(&models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "Implement feature",
		ChildPromptPrefix: "Build this",
		ChildCategory:     string(models.CategoryActive),
		ChildAgentID:      "agent-child",
		ChildChainConfig: &models.ChainConfiguration{
			Enabled:           true,
			ChildTitle:        "Review feature",
			ChildPromptPrefix: "Review this",
			ChildAgentID:      "agent-review",
		},
	}); err != nil {
		t.Fatalf("SetChainConfig: %v", err)
	}

	if err := svc.TriggerTaskChain(context.Background(), parent, "parent output"); err != nil {
		t.Fatalf("TriggerTaskChain: %v", err)
	}
	if len(creator.created) != 2 {
		t.Fatalf("expected child and blocked grandchild, got %#v", creator.created)
	}
	child := creator.created[0]
	if child.Title != "Implement feature" || child.Category != models.CategoryActive || child.Prompt != "Build this\n\nparent output" ||
		child.ParentTaskID == nil || *child.ParentTaskID != parent.ID || child.AgentID == nil || *child.AgentID != "agent-child" ||
		child.BaseBranch != "main" || child.BaseCommitSHA != "abc123" || child.LineageDepth != 3 {
		t.Fatalf("unexpected child task: %#v", child)
	}
	grandchild := creator.created[1]
	if grandchild.Title != "Review feature" || grandchild.Category != models.CategoryBacklog || grandchild.Status != models.StatusBlocked ||
		grandchild.ParentTaskID == nil || *grandchild.ParentTaskID != child.ID || grandchild.AgentID == nil || *grandchild.AgentID != "agent-review" {
		t.Fatalf("unexpected blocked grandchild: %#v", grandchild)
	}
}

func TestTriggerTaskChainNoopsAndSurfacesErrors(t *testing.T) {
	svc := NewService(nil, &fakeTaskCreator{}, &fakeAgentCaller{})
	parent := models.Task{ID: "parent", ProjectID: "project", Title: "Parent", ChainConfig: `{"enabled":false}`}
	if err := svc.TriggerTaskChain(context.Background(), parent, "output"); err != nil {
		t.Fatalf("disabled chain should no-op: %v", err)
	}
	parent.ChainConfig = `{"enabled":true,"trigger":"later"}`
	if err := svc.TriggerTaskChain(context.Background(), parent, "output"); err != nil {
		t.Fatalf("unknown trigger should no-op: %v", err)
	}
	parent.ChainConfig = `{`
	if err := svc.TriggerTaskChain(context.Background(), parent, "output"); err == nil {
		t.Fatal("invalid chain config should error")
	}

	wantErr := errors.New("create failed")
	creator := &fakeTaskCreator{err: wantErr}
	svc = NewService(nil, creator, &fakeAgentCaller{})
	_ = parent.SetChainConfig(&models.ChainConfiguration{Enabled: true, Trigger: "on_completion"})
	if err := svc.TriggerTaskChain(context.Background(), parent, "output"); !errors.Is(err, wantErr) {
		t.Fatalf("expected create error, got %v", err)
	}
}

func TestBuildBlockedChildUsesDefaultsAndNestedChain(t *testing.T) {
	parent := models.Task{ID: "parent", ProjectID: "project", Title: "Design", Category: models.CategoryActive, Priority: 2, Tag: "design", LineageDepth: 1}
	child := BuildBlockedChild(parent, &models.ChainConfiguration{
		ChildChainConfig: &models.ChainConfiguration{Enabled: true, Trigger: "on_completion"},
	})
	if child.Title != "Design (Implementation)" || child.Prompt != "Waiting for parent task to complete..." ||
		child.Category != models.CategoryBacklog || child.Status != models.StatusBlocked || child.LineageDepth != 2 ||
		child.ParentTaskID == nil || *child.ParentTaskID != parent.ID || child.ChainConfig == "{}" {
		t.Fatalf("unexpected blocked child: %#v", child)
	}
}
