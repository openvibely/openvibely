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
			if err := NewTask([]models.Project{project}, &all, agents, nil).Render(r.Context(), w); err != nil {
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
		b.evaluate(`(function(){const input=document.getElementById('task-message-input'),data=new DataTransfer();input.value='Instructions';data.setData('text/plain','pasted context '.repeat(500));input.dispatchEvent(new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true}));document.getElementById('task-thread-form').requestSubmit();return 'submitted';})()`)
		b.waitFor("project picker", `String(document.querySelector('#task-property-picker')?.matches(':popover-open'))`, "true")
		if sends.Load() != 0 {
			t.Fatal("sent before selecting project")
		}
		b.waitFor("draft retained", `String(document.getElementById('task-thread-form')._captureChatSubmission().message==='Instructions\n\n'+'pasted context '.repeat(500))`, "true")
		b.evaluate(`(function(){const destination=document.getElementById('new-task-project');destination.value='paste-project';destination.dispatchEvent(new Event('change',{bubbles:true}));return 'selected';})()`)
		b.waitFor("retried submission accepted", `String(!!document.getElementById('accepted-paste'))`, "true")
		if sends.Load() != 1 {
			t.Fatalf("expected one send, got %d", sends.Load())
		}
	})
}
