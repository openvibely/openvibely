package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	templateui "github.com/openvibely/openvibely/web/templates"
)

func TestBrowserFunctional_NewTaskPastedTextProjectSelection(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "paste-project", Name: "Paste project"}
	all := models.Project{ID: models.AllProjectsID, Name: "All projects"}
	agents := []models.LLMConfig{{ID: "draft-model", Name: "Draft model", Model: "test-model", Provider: models.ProviderTest}}
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/new":
			if err := NewTask([]models.Project{project}, &all, agents, nil).Render(templateui.WithProjectNames(r.Context(), []models.Project{project}), w); err != nil {
				t.Error(err)
			}
		case "/tasks":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.FormValue("message") != "Instructions\n\n"+strings.Repeat("pasted context ", 500) || r.FormValue("destination_project") != project.ID {
				t.Errorf("project retry sent incorrect draft or destination: message length=%d destination=%q", len(r.FormValue("message")), r.FormValue("destination_project"))
			}
			sends.Add(1)
			fmt.Fprint(w, `<div id="accepted-paste">Accepted</div>`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/new", "new-task-paste-project", func(b *composerFocusCDP) {
		b.waitFor("composer initialized", `String(document.readyState==='complete' && !!document.getElementById('task-thread-form')?._captureChatSubmission)`, "true")
		b.evaluateAwait(`(async function(){const picker=document.querySelector('.chat-model-select');if(picker?._ovModelState)await picker._ovModelState.ready;return 'ready';})()`)
		b.evaluate(`(function(){const input=document.getElementById('task-message-input'),data=new DataTransfer();input.value='Instructions';data.setData('text/plain','pasted context '.repeat(500));input.dispatchEvent(new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true}));return 'pasted';})()`)
		b.waitFor("pasted preview visible", `String((function(){const card=document.querySelector('[aria-label="Edit pasted text"]');return !!card && card.getBoundingClientRect().height>0 && !card.closest('[hidden], .hidden');})())`, "true")
		b.evaluate(`document.getElementById('task-thread-form').requestSubmit();'submitted'`)
		b.waitFor("project picker", `String(document.getElementById('task-thread-form-project-select')?.getAttribute('aria-expanded'))`, "true")
		if sends.Load() != 0 {
			t.Fatal("sent before selecting project")
		}
		b.waitFor("draft retained", `String(document.getElementById('task-thread-form')._captureChatSubmission().message==='Instructions\n\n'+'pasted context '.repeat(500))`, "true")
		b.evaluate(`(function(){document.querySelector('#chat-custom-select-panel li[data-value="paste-project"]').click();return 'selected';})()`)
		b.waitFor("retried submission accepted", `String(!!document.getElementById('accepted-paste'))`, "true")
		if sends.Load() != 1 {
			t.Fatalf("expected one send, got %d", sends.Load())
		}
	})
}
