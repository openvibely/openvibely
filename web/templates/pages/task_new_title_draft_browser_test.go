package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_NewTaskTitleDraftRestoredFromProjectHistory(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{
		{ID: "title-project-a", Name: "Project A"},
		{ID: "title-project-b", Name: "Project B"},
	}
	projectByID := func(id string) *models.Project {
		for i := range projects {
			if projects[i].ID == id {
				return &projects[i]
			}
		}
		return &projects[0]
	}
	render := func(component templ.Component) string {
		t.Helper()
		var out bytes.Buffer
		ctx := layout.WithUIPreferences(context.Background(), layout.UIPreferences{PinnedProjectIDs: []string{"title-project-a", "title-project-b"}})
		if err := component.Render(ctx, &out); err != nil {
			t.Fatalf("render title draft fixture: %v", err)
		}
		return out.String()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/ui/preferences", "/alerts/unread-count":
			w.WriteHeader(http.StatusNoContent)
		case "/events/live":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": connected\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case "/tasks":
			project := projectByID(r.URL.Query().Get("project_id"))
			if r.Header.Get("HX-Request") == "true" {
				fmt.Fprint(w, render(TasksContent(project, nil, nil, nil, "", "")))
				return
			}
			fmt.Fprint(w, render(Tasks(projects, project, nil, nil, nil, "", "")))
		case "/tasks/new":
			project := projectByID(r.URL.Query().Get("project_id"))
			if r.Header.Get("HX-Request") == "true" {
				fmt.Fprint(w, render(NewTaskContent(project, nil, nil)))
				return
			}
			fmt.Fprint(w, render(NewTask(projects, project, nil, nil)))
		case "/chat":
			fmt.Fprint(w, `<div id="project-b-chat">Project B chat</div>`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/tasks?project_id=title-project-a", "new-task-title-history", func(b *composerFocusCDP) {
		navigateHistory := func(delta int, want string) {
			expression := fmt.Sprintf(`new Promise(function(resolve){var done=false;function finish(){if(done)return;done=true;resolve(location.pathname+':'+new URLSearchParams(location.search).get('project_id'));}window.addEventListener('popstate',finish,{once:true});setTimeout(function(){if(done)return;done=true;window.removeEventListener('popstate',finish);resolve('timeout:'+location.pathname+':'+new URLSearchParams(location.search).get('project_id'));},3000);history.go(%d);})`, delta)
			if got := b.evaluateAwait(expression); got != want {
				t.Fatalf("history navigation %d = %q, want %q", delta, got, want)
			}
		}
		b.waitFor("Project A task board", `String(!!document.querySelector('a[hx-get^="/tasks/new"]'))`, "true")
		b.click(`a[hx-get^="/tasks/new"]`)
		b.waitFor("Project A new task form", `(function(){var root=document.querySelector('#task-detail-content');return String(!!(root && root.dataset.projectId==='title-project-a' && root.querySelector('input[name="title"]')))})()`, "true")
		b.click(`input[name="title"]`)
		b.typeText("Project A draft")
		b.waitFor("Project A title saved", `sessionStorage.getItem('openvibely.new-task-title.title-project-a')`, "Project A draft")

		// Keep the cached page script from reinitializing so the historyRestore
		// lifecycle handler is responsible for hydrating the restored title.
		b.evaluate(`(function(){
			window._newTaskHistoryRestores=[];
			document.body.addEventListener('htmx:historyRestore',function(event){window._newTaskHistoryRestores.push({path:event.detail.path,cacheHit:!!event.detail.item});});
			var script=Array.from(document.querySelectorAll('#task-detail-content script')).find(function(node){return node.textContent.indexOf('openvibely.new-task-title.')!==-1;});
			if(!script) throw new Error('new task draft script not found');
			script.remove();
			return String(window.openVibelyNewTaskTitleDraftLifecycleInstalled===true);
		})()`)
		b.waitFor("persistent title history listener installed", `String(window.openVibelyNewTaskTitleDraftLifecycleInstalled===true)`, "true")

		b.click(`[data-project-tab="title-project-b"]`)
		b.waitFor("switch to Project B", `location.pathname+':'+new URLSearchParams(location.search).get('project_id')+':'+String(!!document.getElementById('project-b-chat'))`, "/chat:title-project-b:true")
		b.evaluateAwait(`window.openVibelyNavigate('/tasks/new?project_id=title-project-b').then(function(){return 'navigated';})`)
		b.waitFor("Project B new task form", `(function(){var root=document.querySelector('#task-detail-content');return String(!!(root && root.dataset.projectId==='title-project-b' && root.querySelector('input[name="title"]')))})()`, "true")
		b.click(`input[name="title"]`)
		b.typeText("Project B draft")
		b.waitFor("Project B title saved", `sessionStorage.getItem('openvibely.new-task-title.title-project-b')`, "Project B draft")
		b.waitFor("Project A cached in HTMX history", `(function(){var items=JSON.parse(localStorage.getItem('htmx-history-cache')||'[]');return String(items.some(function(item){return item.url==='/tasks/new?project_id=title-project-a';}))})()`, "true")
		navigateHistory(-2, "/tasks/new:title-project-a")
		b.waitFor("Project A history restore event", `String((window._newTaskHistoryRestores||[]).some(function(item){return item.cacheHit && item.path.indexOf('/tasks/new?project_id=title-project-a')===0}))`, "true")
		b.waitFor("cached Project A title restored", `(function(){var root=document.getElementById('task-detail-content'),input=root&&root.querySelector('input[name="title"]');return location.pathname+':'+(root&&root.dataset.projectId)+':'+(input&&input.value)+':'+sessionStorage.getItem('openvibely.new-task-title.title-project-a')})()`, "/tasks/new:title-project-a:Project A draft:Project A draft")

		// Cached fragments do not retain their original DOM listeners. A title edit
		// after restore must update storage so another history visit cannot revert it.
		b.evaluate(`(function(){var input=document.querySelector('#task-detail-content input[name="title"]');input.value='Project A revised';input.dispatchEvent(new Event('input',{bubbles:true}));return 'edited';})()`)
		b.waitFor("restored Project A title saved", `sessionStorage.getItem('openvibely.new-task-title.title-project-a')`, "Project A revised")
		navigateHistory(2, "/tasks/new:title-project-b")
		navigateHistory(-2, "/tasks/new:title-project-a")
		b.waitFor("revisit cached Project A with latest title", `(function(){var root=document.getElementById('task-detail-content'),input=root&&root.querySelector('input[name="title"]');return location.pathname+':'+(root&&root.dataset.projectId)+':'+(input&&input.value)})()`, "/tasks/new:title-project-a:Project A revised")

		navigateHistory(2, "/tasks/new:title-project-b")
		b.waitFor("cached Project B title restored independently", `(function(){var root=document.getElementById('task-detail-content'),input=root&&root.querySelector('input[name="title"]');return location.pathname+':'+(root&&root.dataset.projectId)+':'+(input&&input.value)})()`, "/tasks/new:title-project-b:Project B draft")
		b.waitFor("Project A draft remains isolated", `sessionStorage.getItem('openvibely.new-task-title.title-project-a')`, "Project A revised")

		b.evaluate(`(function(){var form=document.getElementById('task-thread-form');form.dispatchEvent(new CustomEvent('htmx:afterRequest',{bubbles:true,detail:{elt:form,successful:false,xhr:{status:500}}}));return 'failed request dispatched';})()`)
		b.waitFor("failed create keeps its title draft", `sessionStorage.getItem('openvibely.new-task-title.title-project-b')`, "Project B draft")
		b.evaluate(`(function(){var form=document.getElementById('task-thread-form');form.dispatchEvent(new CustomEvent('htmx:afterRequest',{bubbles:true,detail:{elt:form,successful:true,xhr:{status:200}}}));return 'successful request dispatched';})()`)
		b.waitFor("successful create clears only Project B title", `String(sessionStorage.getItem('openvibely.new-task-title.title-project-b')===null && sessionStorage.getItem('openvibely.new-task-title.title-project-a')==='Project A revised')`, "true")
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('htmx:beforeHistorySave',{bubbles:true,detail:{}})); 'history save dispatched'`)
		b.waitFor("successful title is not saved again", `String(sessionStorage.getItem('openvibely.new-task-title.title-project-b')===null)`, "true")
	})
}
