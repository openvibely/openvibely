package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_TaskPanelCachedNavigation(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var hold atomic.Bool
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseResponse := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/events/ws" {
			serveBrowserLiveFixtureWithComputer(w, r, nil, true)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/thread")
		if id != "one" && id != "two" && id != "three" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		task := &models.Task{ID: id, ProjectID: "project", Title: id, Status: models.StatusCompleted, Category: models.CategoryCompleted}
		thread := components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30)
		w.Header().Set("Content-Type", "text/html")
		if strings.HasSuffix(r.URL.Path, "/thread") {
			_ = thread.Render(r.Context(), w)
			return
		}
		if id == "one" && hold.Load() {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if r.Header.Get("HX-Request") == "true" {
			_ = TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil, thread).Render(r.Context(), w)
		} else {
			_ = TaskDetailPage(nil, task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(r.Context(), w)
		}
	}))
	defer server.Close()
	defer releaseResponse()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/two?project_id=project", "panel-cache", func(b *composerFocusCDP) {
		b.waitFor("initial thread", `String(!!document.getElementById('task-thread-view'))`, "true")
		b.evaluate(`localStorage.setItem('task-inspector-open-one','true');localStorage.setItem('task-inspector-tab-one','computer');window.cachedPanelVisits=0;document.addEventListener('htmx:afterSwap',e=>{if(e.detail._ovCachedPreview)window.cachedPanelVisits++;});'seeded'`)
		nav := func(id string) {
			b.evaluateAwait(fmt.Sprintf(`window.openVibelyNavigate('/tasks/%s?project_id=project').then(()=>'done')`, id))
			b.waitFor("thread ready", `document.getElementById('task-thread-view')?.dataset.taskId`, id)
		}
		// Hold permission delivery independently of page delivery. Inspect the
		// panel before authorization, not just its eventual restored state.
		b.evaluate(`window.holdPanelPermission=true;window.panelPermissions=[];const originalComputer=window.openVibelyLive.computer;window.openVibelyLive.computer=function(project,task,params,options){if(!task){const listener=options.listeners.computer_status;window.deliverPanelPermission=listener;window.deliverPanelPermissionError=options.listeners.error;options.listeners.computer_status=function(event){if(window.holdPanelPermission)window.panelPermissions.push(()=>listener(event));else listener(event);};}return originalComputer.call(this,project,task,params,options);};'installed'`)
		nav("one")
		b.waitFor("initial Computer restored", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "computer")
		b.waitFor("fresh panel selected while permission pending", `String(document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab==='computer' && document.getElementById('tab-computer').inert && document.getElementById('task-detail-content').hasAttribute('data-computer-permission-pending'))`, "true")
		b.evaluate(`window.holdPanelPermission=false;window.panelPermissions.splice(0).forEach(deliver=>deliver());'released'`)
		b.waitFor("computer authorized", `String(!document.getElementById('tab-computer').inert)`, "true")
		nav("two")
		b.waitFor("other task closed", `String(document.getElementById('task-details-panel').hidden)`, "true")
		b.waitFor("one cached", `String(window.openVibelyThreadCache.pages.has('/tasks/one?project_id=project'))`, "true")
		b.evaluate(`window.holdPanelPermission=true;'held'`)
		hold.Store(true)
		b.evaluate(`window.pendingPanelVisit=window.openVibelyNavigate('/tasks/one?project_id=project').catch(()=>{});'started'`)
		b.waitFor("cached preview", `document.getElementById('task-detail-content')?.dataset.taskId`, "one")
		b.waitFor("Computer restored from cached page", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "computer")
		b.waitFor("saved Computer retained", `localStorage.getItem('task-inspector-tab-one')`, "computer")
		b.waitFor("cache actually used", `String(window.cachedPanelVisits>0)`, "true")
		b.waitFor("cached selection stays Computer before authorization", `String(document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab==='computer' && document.getElementById('tab-computer').inert)`, "true")
		b.evaluate(`window.panelBounds=document.getElementById('task-details-panel').getBoundingClientRect().toJSON();window.holdPanelPermission=false;window.panelPermissions.splice(0).forEach(deliver=>deliver());'released'`)
		b.waitFor("cached computer authorized", `String(!document.getElementById('tab-computer').inert)`, "true")
		b.waitFor("permission did not move panel", `String(JSON.stringify(document.getElementById('task-details-panel').getBoundingClientRect().toJSON())===JSON.stringify(window.panelBounds))`, "true")
		hold.Store(false)
		releaseResponse()
		b.evaluateAwait(`window.pendingPanelVisit.then(()=>'done')`)
		b.waitFor("Computer after server refresh", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "computer")
		b.click("#task-details-opener")
		nav("two")
		b.waitFor("second task still closed", `String(document.getElementById('task-details-panel').hidden)`, "true")
		b.click("#task-details-opener")
		b.click(`[data-tab="attachments"]`)
		nav("one")
		b.waitFor("cached open markup respects latest closed preference", `String(document.getElementById('task-details-panel').hidden)`, "true")
		b.click("#task-details-opener")
		b.waitFor("reopening cached task retains Computer", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "computer")
		b.evaluate(`window.deliverPanelPermissionError({data:'access blocked'});'revoked'`)
		b.waitFor("permission error returns to Details", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "details")
		b.waitFor("permission error blocks Computer", `String(document.getElementById('tab-computer').inert && document.querySelector('[data-tab="computer"]').hidden)`, "true")
		b.evaluate(`window.deliverPanelPermission({data:JSON.stringify({project_id:'project',task_id:'',enabled:true})});window.taskWorkspace.openPanel('computer');'allowed'`)
		b.evaluate(`window.deliverPanelPermission({data:JSON.stringify({project_id:'project',task_id:'',enabled:false})});'denied'`)
		b.waitFor("confirmed denial falls back to Details", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "details")
		b.waitFor("denied computer stays blocked", `String(document.getElementById('tab-computer').inert)`, "true")
		nav("two")
		b.waitFor("second task keeps its own panel selection", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "attachments")
		b.click(`[data-tab="computer"]`)
		nav("three")
		b.waitFor("untouched task stays closed after leaving Computer", `String(document.getElementById('task-details-panel').hidden)`, "true")
		nav("two")
		b.waitFor("return to clicked Computer", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "computer")

	})
}
