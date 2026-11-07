package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

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
		ref := pageintegration.PageRef{Kind: "test-page", PageID: "report", InstanceID: id, ProjectID: project.ID, Title: id}
		return pageintegration.Conversation{Page: ref, Store: stores.PageConversations.ForPage(ref), Contexts: contexts,
			Resolve:  func(context.Context) (pageintegration.Integration, error) { return plainPage{ref}, nil },
			Begin:    func(ctx context.Context) (context.Context, func(), error) { return ctx, func() {}, nil },
			Lifetime: func() context.Context { return lifetime },
		}
	}
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
		exec, err := h.startPageChatTurn(c, binding, "summarize")
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
