package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_TaskNamingReconcilesMissedEvents(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "naming-project", Name: "Naming"}
	task := &models.Task{ID: "naming-task", ProjectID: project.ID, Title: "Provisional", Prompt: "Request", Status: models.StatusCompleted, Category: models.CategoryCompleted, Priority: 2}
	var title atomic.Value
	title.Store("Generated before page loaded")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/naming-task":
			w.Header().Set("Content-Type", "text/html")
			if err := TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/naming-task/changes/summary":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"title": title.Load(), "files": 0})
		case "/tasks/naming-task/thread":
			fmt.Fprint(w, `<div id="thread-content">Thread</div>`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/naming-task?project_id=naming-project", "naming-reconcile", func(b *composerFocusCDP) {
		b.waitFor("initial missed event reconciled", `document.getElementById('task-breadcrumb-title') ? document.getElementById('task-breadcrumb-title').textContent : ''`, "Generated before page loaded")
		b.waitFor("document title reconciled", `document.title`, "Generated before page loaded - OpenVibely")
		title.Store("Generated while disconnected")
		b.evaluate(`window.dispatchEvent(new Event('sse-live-connected')); 'reconnect'`)
		b.waitFor("reconnect reconciles title", `document.getElementById('task-breadcrumb-title') ? document.getElementById('task-breadcrumb-title').textContent : ''`, "Generated while disconnected")
		b.evaluate(`window._originalNamingFetch=window.fetch; window.fetch=function(url,opts){if(String(url).includes('/changes/summary'))return new Promise(function(resolve){window._resolveNamingSummary=resolve;});return window._originalNamingFetch(url,opts);};window.dispatchEvent(new Event('sse-live-connected')); 'held'`)
		b.waitFor("older request in flight", `typeof window._resolveNamingSummary`, "function")
		title.Store("Newest title")
		b.evaluate(`var input=document.querySelector('input[name="title"]');input.value='Unsaved manual edit';window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_board_updated',task_id:'naming-task',task_name:'Newest title'}})); 'event'`)
		b.waitFor("event title displayed", `document.getElementById('task-breadcrumb-title') ? document.getElementById('task-breadcrumb-title').textContent : ''`, "Newest title")
		b.evaluateAwait(`window.fetch=window._originalNamingFetch;window._resolveNamingSummary({ok:true,json:function(){return Promise.resolve({files:0,title:'Old response'});}}); new Promise(function(resolve){setTimeout(function(){resolve('settled');},50);})`)
		b.waitFor("stale response ignored", `document.getElementById('task-breadcrumb-title') ? document.getElementById('task-breadcrumb-title').textContent : ''`, "Newest title")
		b.waitFor("document title follows event", `document.title`, "Newest title - OpenVibely")
		b.waitFor("title marker follows event", `document.querySelector('[data-openvibely-page-title]').getAttribute('data-openvibely-page-title')`, "Newest title - OpenVibely")
		b.waitFor("unsaved edit preserved", `document.querySelector('input[name="title"]').value`, "Unsaved manual edit")
	})
}
