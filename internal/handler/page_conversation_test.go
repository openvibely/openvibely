package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
)

type plainPage struct{ ref pageintegration.PageRef }

func (p plainPage) Ref() pageintegration.PageRef            { return p.ref }
func (p plainPage) Operations() []pageintegration.Operation { return nil }
func (p plainPage) Invoke(context.Context, pageintegration.Caller, string, json.RawMessage) (json.RawMessage, error) {
	return nil, pageintegration.ErrOperationNotFound
}

// No plugin manager, manifest, iframe or plugin records are involved.
func TestPageConversationWithoutPlugin(t *testing.T) {
	h, e, configs, stores := setupTestHandlerWithStores(t)
	project := createProject(t, h, "Shared pages")
	createAgent(t, configs)
	lifetime, disable := context.WithCancel(context.Background())
	defer disable()
	contexts := pageintegration.NewContextStore()
	makeBinding := func(id string) pageintegration.Conversation {
		ref := pageintegration.PageRef{Kind: "test-page", PageID: "report", InstanceID: id, ProjectID: project.ID, Title: "Sales"}
		return pageintegration.Conversation{Page: ref, Store: stores.PageConversations.ForPage(ref), Contexts: contexts,
			Resolve:  func(context.Context) (pageintegration.Integration, error) { return plainPage{ref}, nil },
			Begin:    func(ctx context.Context) (context.Context, func(), error) { return ctx, func() {}, nil },
			Lifetime: func() context.Context { return lifetime },
		}
	}
	// Display names need not be unique; conversation identity comes from the page.
	a, b := makeBinding("a"), makeBinding("b")
	var starts []streamingResponseParams
	h.startStreamingResponseOverride = func(p streamingResponseParams) error { starts = append(starts, p); return nil }
	request := func(binding pageintegration.Conversation) *models.Execution {
		t.Helper()
		c := e.NewContext(httptest.NewRequest("POST", "/test-page/chat", nil), httptest.NewRecorder())
		_, err := contexts.Set(h.authPrincipalID(c), binding.Page, pageintegration.Context{Summary: binding.Page.InstanceID}, "test-page")
		if err != nil {
			t.Fatal(err)
		}
		exec, _, err := h.startPageChatTurn(c, binding, "summarize", pageChatInput{})
		if err != nil {
			t.Fatal(err)
		}
		return exec
	}
	first := request(a)
	second := request(b)
	if err := b.Store.SaveSnapshot(t.Context(), first.ID, `{}`); err == nil {
		t.Fatal("another page accepted the turn snapshot")
	}
	if first.TaskID == second.TaskID || streamingTransportScope(starts[0]) == streamingTransportScope(starts[1]) {
		t.Fatal("pages share a conversation")
	}
	// Generic stale recovery must recognize non-plugin page conversations too.
	if _, err := h.execRepo.RecoverStaleRunningTaskExecutions(t.Context()); err != nil {
		t.Fatal(err)
	}
	active, err := h.execRepo.GetByID(t.Context(), first.ID)
	if err != nil || active.Status != models.ExecRunning {
		t.Fatalf("generic page incorrectly recovered: %+v %v", active, err)
	}
	task, history, err := h.loadPageChatHistory(t.Context(), makeBinding("a"))
	if err != nil || task.ID != first.TaskID || len(history) != 1 {
		t.Fatalf("reopen failed: %v", err)
	}
	if err := h.cancelPageConversation(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	other, err := h.execRepo.GetByID(t.Context(), second.ID)
	if err != nil || other.Status != models.ExecRunning {
		t.Fatal("stopping A affected B")
	}
	resumed := request(makeBinding("a"))
	if resumed.TaskID != first.TaskID {
		t.Fatal("reopen changed persisted task")
	}
	disable()
	if starts[2].CancellationContext.Err() == nil {
		t.Fatal("lifetime cancellation missing")
	}
}

func TestPageConversationStoreRejectsCrossProjectTask(t *testing.T) {
	h, _, configs, stores := setupTestHandlerWithStores(t)
	a := createProject(t, h, "A")
	b := createProject(t, h, "B")
	agent := createAgent(t, configs)
	ref := pageintegration.PageRef{Kind: "test", PageID: "report", InstanceID: "same", ProjectID: a.ID}
	binding := pageintegration.Conversation{Page: ref, Store: stores.PageConversations.ForPage(ref)}
	task, err := h.pageConversationTask(t.Context(), binding, "hello", agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref.ProjectID = b.ID
	wrong := stores.PageConversations.ForPage(ref)
	if err := wrong.BindTask(t.Context(), task.ID); err == nil {
		t.Fatal("cross-project binding accepted")
	}
	id, err := wrong.TaskID(t.Context())
	if err != nil || id != "" {
		t.Fatal("cross-project lookup returned conversation")
	}
}

func TestPageConversationQueuedTurnKeepsContextAndIsolation(t *testing.T) {
	h, e, configs, stores := setupTestHandlerWithStores(t)
	project := createProject(t, h, "Queued page")
	createAgent(t, configs)
	contexts := pageintegration.NewContextStore()
	ref := pageintegration.PageRef{Kind: "test", PageID: "report", InstanceID: "queued", ProjectID: project.ID, Title: "Report"}
	binding := pageintegration.Conversation{Page: ref, Store: stores.PageConversations.ForPage(ref), Contexts: contexts,
		Resolve: func(context.Context) (pageintegration.Integration, error) { return plainPage{ref}, nil },
		Begin:   func(ctx context.Context) (context.Context, func(), error) { return ctx, func() {}, nil }, Lifetime: context.Background}
	starts := make(chan streamingResponseParams, 4)
	h.startStreamingResponseOverride = func(p streamingResponseParams) error { starts <- p; return nil }
	c := e.NewContext(httptest.NewRequest("POST", "/test", nil), httptest.NewRecorder())
	first, _, err := h.startPageChatTurn(c, binding, "first", pageChatInput{})
	if err != nil {
		t.Fatal(err)
	}
	initial := <-starts
	_, err = contexts.Set(h.authPrincipalID(c), ref, pageintegration.Context{Summary: "queued North selection"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.startPageChatTurn(c, binding, "second", pageChatInput{AllowQueue: true})
	queued, ok := err.(*pageChatQueued)
	if !ok {
		t.Fatalf("expected queued input, got %v", err)
	}
	if _, err = binding.Store.QueuedContext(t.Context(), queued.Input.ID); err != nil {
		t.Fatal(err)
	}

	otherRef := ref
	otherRef.InstanceID = "other"
	if _, err = stores.PageConversations.ForPage(otherRef).QueuedContext(t.Context(), queued.Input.ID); err == nil {
		t.Fatal("another page read queued context")
	}
	_, err = contexts.Set(h.authPrincipalID(c), ref, pageintegration.Context{Summary: "later South selection"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.execRepo.Complete(t.Context(), first.ID, models.ExecCompleted, "done", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err = h.taskRepo.UpdateStatus(t.Context(), first.TaskID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err = h.threadInputRepo.ClaimQueuedForTaskExecutionWithModel(t.Context(), queued.Input.ID, &models.Execution{TaskID: first.TaskID, AgentConfigID: initial.Agent.ID}, &initial.Agent.ID, &initial.Agent); err == nil {
		t.Fatal("task board claimed page input")
	}
	h.startNextQueuedTurnAfter(t.Context(), initial, "")
	var next streamingResponseParams
	select {
	case next = <-starts:
	case <-time.After(time.Second):
		t.Fatal("queue did not advance")
	}
	if next.TaskID != first.TaskID || next.Message != "second" || next.PrincipalID != initial.PrincipalID || !strings.Contains(next.SystemContext, "queued North selection") || strings.Contains(next.SystemContext, "later South selection") {
		t.Fatalf("lost queued context: %+v", next)
	}
	task, err := h.taskRepo.GetByID(t.Context(), first.TaskID)
	if err != nil || task.Category != models.CategoryPageChat || task.Status != models.StatusRunning {
		t.Fatalf("page escaped isolation: %+v %v", task, err)
	}
	_, _, err = h.startPageChatTurn(c, binding, "third", pageChatInput{AllowQueue: true})
	if _, ok = err.(*pageChatQueued); !ok {
		t.Fatal(err)
	}
	if err = h.cancelPageConversation(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	pending, err := h.threadInputRepo.FindOldestQueuedForTask(t.Context(), first.TaskID)
	if err != nil || pending != nil {
		t.Fatalf("Stop left queued work: %+v %v", pending, err)
	}
}
