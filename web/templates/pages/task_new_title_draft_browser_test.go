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
		case "/tasks/new":
			project := projectByID(r.URL.Query().Get("project_id"))
			if r.Header.Get("HX-Request") == "true" {
				fmt.Fprint(w, render(NewTaskContent(project, nil, nil)))
				return
			}
			fmt.Fprint(w, render(NewTask(projects, project, nil, nil)))
		case "/chat":
			fmt.Fprint(w, `<div id="project-b-chat"><a id="open-project-b-new-task" href="/tasks/new?project_id=title-project-b" hx-get="/tasks/new?project_id=title-project-b" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">New task</a></div>`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/tasks/new?project_id=title-project-a", "new-task-title-history", func(b *composerFocusCDP) {
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
			return String(window.openVibelyNewTaskTitleHistoryRestoreInstalled===true);
		})()`)
		b.waitFor("persistent title history listener installed", `String(window.openVibelyNewTaskTitleHistoryRestoreInstalled===true)`, "true")

		b.click(`[data-project-tab="title-project-b"]`)
		b.waitFor("switch to Project B", `location.pathname+':'+new URLSearchParams(location.search).get('project_id')+':'+String(!!document.getElementById('project-b-chat'))`, "/chat:title-project-b:true")
		b.click("#open-project-b-new-task")
		b.waitFor("Project B new task form", `(function(){var root=document.querySelector('#task-detail-content');return String(!!(root && root.dataset.projectId==='title-project-b' && root.querySelector('input[name="title"]')))})()`, "true")
		b.click(`input[name="title"]`)
		b.typeText("Project B draft")
		b.waitFor("Project B title saved", `sessionStorage.getItem('openvibely.new-task-title.title-project-b')`, "Project B draft")

		b.navigateHistory(-2)
		b.waitFor("Project A history restore event", `String(window._newTaskHistoryRestores.some(function(item){return item.cacheHit && item.path.indexOf('/tasks/new?project_id=title-project-a')===0}))`, "true")
		b.waitFor("cached Project A title restored", `(function(){var root=document.getElementById('task-detail-content'),input=root&&root.querySelector('input[name="title"]');return location.pathname+':'+(root&&root.dataset.projectId)+':'+(input&&input.value)+':'+sessionStorage.getItem('openvibely.new-task-title.title-project-a')})()`, "/tasks/new:title-project-a:Project A draft:Project A draft")

		b.navigateHistory(2)
		b.waitFor("cached Project B title restored independently", `(function(){var root=document.getElementById('task-detail-content'),input=root&&root.querySelector('input[name="title"]');return location.pathname+':'+(root&&root.dataset.projectId)+':'+(input&&input.value)})()`, "/tasks/new:title-project-b:Project B draft")
		b.waitFor("Project A draft remains isolated", `sessionStorage.getItem('openvibely.new-task-title.title-project-a')`, "Project A draft")

		b.evaluate(`(function(){var form=document.getElementById('task-thread-form');form.dispatchEvent(new CustomEvent('htmx:afterRequest',{bubbles:true,detail:{elt:form,successful:false,xhr:{status:500}}}));return 'failed request dispatched';})()`)
		b.waitFor("failed create keeps its title draft", `sessionStorage.getItem('openvibely.new-task-title.title-project-b')`, "Project B draft")
		b.evaluate(`(function(){var form=document.getElementById('task-thread-form');form.dispatchEvent(new CustomEvent('htmx:afterRequest',{bubbles:true,detail:{elt:form,successful:true,xhr:{status:200}}}));return 'successful request dispatched';})()`)
		b.waitFor("successful create clears only Project B title", `String(sessionStorage.getItem('openvibely.new-task-title.title-project-b')===null && sessionStorage.getItem('openvibely.new-task-title.title-project-a')==='Project A draft')`, "true")
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('htmx:beforeHistorySave',{bubbles:true,detail:{}})); 'history save dispatched'`)
		b.waitFor("successful title is not saved again", `String(sessionStorage.getItem('openvibely.new-task-title.title-project-b')===null)`, "true")
	})
}
