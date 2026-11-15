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
		if id != "one" && id != "two" {
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
		nav("one")
		b.waitFor("initial Computer restored", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "computer")
		nav("two")
		b.waitFor("other task closed", `String(document.getElementById('task-details-panel').hidden)`, "true")
		b.waitFor("one cached", `String(window.openVibelyThreadCache.pages.has('/tasks/one?project_id=project'))`, "true")
		hold.Store(true)
		b.evaluate(`window.pendingPanelVisit=window.openVibelyNavigate('/tasks/one?project_id=project').catch(()=>{});'started'`)
		b.waitFor("cached preview", `document.getElementById('task-detail-content')?.dataset.taskId`, "one")
		b.waitFor("Computer restored from cached page", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "computer")
		b.waitFor("saved Computer retained", `localStorage.getItem('task-inspector-tab-one')`, "computer")
		b.waitFor("cache actually used", `String(window.cachedPanelVisits>0)`, "true")
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
		nav("two")
		b.waitFor("second task keeps its own panel selection", `document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab`, "attachments")
	})
}
