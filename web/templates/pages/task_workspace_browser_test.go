package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_TaskMetadataBadgesAtMinimumPanelWidth(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	agentID := "long-agent"
	task := &models.Task{ID: "badge-task", Status: models.StatusRunning, Category: models.CategoryActive, Priority: 2, AgentDefinitionID: &agentID}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><link rel="stylesheet" href="%s"><link rel="stylesheet" href="%s"></head><body>`, static.URL("app.css"), static.URL("app-utilities.css"))
		// The polled fragment must retain its layout without panel-shell styles.
		fmt.Fprint(w, `<main style="width:340px;padding:12px">`)
		if err := TaskDetailMetrics(task, models.TaskExecutionMetrics{LatestDurationMs: 229000}, nil, "An exceptionally long agent label with several words").Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `</main></body></html>`)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-metadata-badges", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1500, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("metrics ready", `String(Boolean(document.getElementById('task-detail-metrics')) && document.styleSheets.length > 0)`, "true")
		b.waitFor("production styles loaded", `String(document.readyState==='complete' && getComputedStyle(document.getElementById('task-detail-metrics')).display==='grid')`, "true")
		for _, width := range []int{340, 420, 720} {
			b.evaluate(fmt.Sprintf(`document.querySelector('main').style.width='%dpx'; 'sized'`, width))
			b.waitFor("badge text inside borders", `(function(){var grid=document.getElementById('task-detail-metrics'),bounds=grid.getBoundingClientRect();return String(Array.from(grid.querySelectorAll('.badge')).every(function(badge){var box=badge.getBoundingClientRect(),range=document.createRange();range.selectNodeContents(badge);return box.left>=bounds.left && box.right<=bounds.right+1 && Array.from(range.getClientRects()).every(function(text){return text.top>=box.top-1 && text.bottom<=box.bottom+1 && text.right<=box.right+1;});}));})()`, "true")
		}
	})
}

func TestBrowserFunctional_NewTaskWorkspace(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "draft-project", Name: "Draft"}
	var unexpected atomic.Int32
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var view templ.Component
		switch r.URL.Path {
		case "/ui/preferences":
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks":
			if r.Method == http.MethodPost {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.FormValue("message") != "Implement this feature" || r.FormValue("title") != "" {
					t.Errorf("unexpected first message: %v", r.Form)
				}
				if sends.Load() == 0 && (r.FormValue("add_schedule") != "on" || r.FormValue("run_at") != "2035-01-02T09:30" || r.FormValue("repeat_type") != "weekly") {
					t.Errorf("missing draft schedule settings: %v", r.Form)
				}
				createdID := fmt.Sprintf("created-%d", sends.Add(1))
				w.Header().Set("X-Created-Task-ID", createdID)
				w.Header().Set("HX-Retarget", "#main-content")
				w.Header().Set("HX-Reswap", "innerHTML")
				w.Header().Set("HX-Push-Url", "/tasks/"+createdID+"?project_id="+project.ID)
				task := &models.Task{ID: createdID, ProjectID: project.ID, Title: "Created task", Status: models.StatusCompleted}
				fmt.Fprint(w, `<div id="created-thread">`)
				if err := components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(r.Context(), w); err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, `</div>`)

				return
			}
			if r.Header.Get("HX-Request") == "true" {
				view = TasksContent(&project, nil, nil, nil, "", "")
			} else {
				view = Tasks([]models.Project{project}, &project, nil, nil, nil, "", "")
			}
		case "/breadcrumb-selectors/tasks":
			_, _ = w.Write([]byte(`<div role="listbox"><a role="option" href="/tasks/existing">Existing task</a></div>`))
			return
		case "/tasks/new":
			if r.Header.Get("HX-Request") == "true" {
				view = NewTaskContent(&project, nil, nil)
			} else {
				view = NewTask([]models.Project{project}, &project, nil, nil)
			}
		default:
			if strings.HasPrefix(r.URL.Path, "/tasks/") {
				unexpected.Add(1)
			}
			http.NotFound(w, r)
			return
		}
		if err := view.Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks?project_id="+project.ID, "new-task-workspace", func(b *composerFocusCDP) {
		b.waitFor("board ready", `String(Boolean(document.querySelector('a[hx-get^="/tasks/new"]')))`, "true")
		b.evaluate(`window.originalSidebar=document.getElementById('sidebar'); 'saved'`)
		b.click(`a[hx-get^="/tasks/new"]`)
		b.waitFor("new workspace navigation", `location.pathname+':'+String(Boolean(document.querySelector('#task-detail-content textarea[name="message"]')))`, "/tasks/new:true")
		b.waitFor("no creation dialog", `String(document.querySelector('dialog[open]')===null)`, "true")
		b.waitFor("initial composer settled", `String(!document.querySelector('.htmx-settling, .htmx-swapping, .htmx-request'))`, "true")
		b.click(`input[name="title"]`)
		b.typeText("New task draft")
		b.click(`textarea[name="message"]`)
		b.typeText("Implement this feature")
		b.click("#task-details-opener")
		b.waitFor("draft inspector", `document.getElementById('task-details-opener').getAttribute('aria-expanded')`, "true")
		b.click(`[data-tab="schedules"]`)
		b.click(`input[name="add_schedule"]`)
		b.evaluate(`document.querySelector('input[name="run_at"]').value='2035-01-02T09:30'; document.querySelector('select[name="repeat_type"]').value='weekly'; 'configured'`)
		b.click(`[data-tab="attachments"]`)
		b.waitFor("draft attachments", `String(document.getElementById('new-task-files').getClientRects().length>0)`, "true")
		b.click("#task-details-opener")
		b.waitFor("draft retained", `document.querySelector('textarea[name="message"]').value`, "Implement this feature")
		b.click("#task-resource-selector-button")
		b.waitFor("task breadcrumb results", `String(document.querySelector('#task-resource-selector-dialog').textContent.includes('Existing task'))`, "true")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
		b.evaluate(`document.querySelector('input[name="title"]').value=''`)
		b.click("#task-message-input")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
		b.waitFor("first message navigation", `location.pathname`, "/tasks/created-1")
		b.waitFor("app shell retained after first send", `String(Boolean(window.originalSidebar && window.originalSidebar.isConnected && document.getElementById('sidebar')===window.originalSidebar && window.originalSidebar.getClientRects().length && document.querySelector('#main-content #created-thread')))`, "true")
		for attempt := 1; attempt <= 2; attempt++ {
			b.waitFor("accepted composer cleared", `document.getElementById('task-message-input').value`, "")
			b.click("#task-message-input")
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowUp", "code": "ArrowUp", "windowsVirtualKeyCode": 38}, nil)
			b.waitFor("initial message recalled", `document.getElementById('task-message-input').value`, "Implement this feature")
			if attempt == 2 {
				break
			}
			b.click(`#sidebar [data-nav-base="/tasks"]`)
			b.waitFor("board return", `location.pathname`, "/tasks")
			b.waitFor("board settled", `String(!document.querySelector('.htmx-settling, .htmx-swapping, .htmx-request'))`, "true")
			b.click(`a[hx-get^="/tasks/new"]`)
			b.waitFor("second new workspace", `location.pathname`, "/tasks/new")
			b.waitFor("new composer settled", `String(!document.querySelector('.htmx-settling, .htmx-swapping, .htmx-request') && !!document.getElementById('task-message-input'))`, "true")
			b.click("#task-message-input")
			b.typeText("Implement this feature")
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
			b.waitFor("second message navigation", `location.pathname`, "/tasks/created-2")
		}
	})
	if sends.Load() != 2 {
		t.Fatalf("expected two first-message sends, got %d", sends.Load())
	}
	if unexpected.Load() != 0 {
		t.Fatalf("unsaved task made %d persisted-task requests", unexpected.Load())
	}
}

func TestBrowserFunctional_TaskWorkspacePanelAndDiff(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "workspace-project", Name: "Workspace"}
	task := &models.Task{ID: "workspace-task", ProjectID: project.ID, Title: "Workspace", Status: models.StatusCompleted, Category: models.CategoryCompleted}
	render := func(c templ.Component) string {
		t.Helper()
		var out bytes.Buffer
		if err := c.Render(context.Background(), &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	var summaryRequests, diffRequests, threadRequests atomic.Int32
	var files atomic.Int32
	fileEvents := make(chan string, 30)
	executions := []models.Execution{}
	for i := 0; i < 20; i++ {
		executions = append(executions, models.Execution{ID: fmt.Sprintf("execution-%d", i), TaskID: task.ID, Status: models.ExecCompleted, PromptSent: strings.Repeat("Reading history ", 20), StartedAt: time.Unix(int64(i+1), 0)})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/ui/preferences":
			w.WriteHeader(http.StatusNoContent)
		case "/events/live":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": connected\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case event := <-fileEvents:
					fmt.Fprintf(w, "event: file_modified\ndata: %s\n\n", event)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/tasks/workspace-task":
			tab := r.URL.Query().Get("tab")
			if tab == "" {
				tab = "chat"
			}
			if r.Header.Get("HX-Request") == "true" {
				fmt.Fprint(w, render(TaskDetailContent(task, nil, nil, nil, nil, nil, nil, tab, nil)))
			} else {
				fmt.Fprint(w, render(TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, tab, nil)))
			}
		case "/tasks/workspace-task/thread":
			threadRequests.Add(1)
			fmt.Fprint(w, render(components.TaskThreadView(task, executions, nil, nil, nil, nil, false, 30)))
		case "/tasks/workspace-task/changes/summary":
			summaryRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"files":%d,"insertions":51,"deletions":12,"status":"completed","duration":"12m 41s","review_comments":0}`, files.Load())
		case "/tasks/workspace-task/changes":
			diffRequests.Add(1)
			fmt.Fprint(w, `<div id="diff-viewer">Authoritative full diff</div>`)
		case "/auth/me":
			fmt.Fprint(w, `{"authenticated":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/workspace-task?from=alerts", "task-workspace", func(b *composerFocusCDP) {
		b.waitFor("thread mounted", `String(Boolean(document.getElementById('task-message-input')))`, "true")
		b.waitFor("no empty activity row", `String(document.getElementById('task-change-activity').hidden)`, "true")
		b.waitFor("panel closed", `String(document.getElementById('task-details-panel').hidden)`, "true")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1500, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.click("#task-details-opener")
		b.waitFor("panel opened", `document.getElementById('task-details-opener').getAttribute('aria-expanded')`, "true")
		b.waitFor("inspector below project titlebar and flush with viewport", `(function(){var r=document.getElementById('task-details-panel').getBoundingClientRect();return [Math.round(r.top),Math.round(innerWidth-r.right),Math.round(innerHeight-r.bottom)].join(':')})()`, "46:0:0")
		b.waitFor("single toggle and no panel heading", `String(document.querySelector('#task-details-panel h2')===null && document.querySelector('#task-details-panel [aria-label="Collapse details panel"]')===null && document.querySelector('#task-details-opener line').getAttribute('x1')==='15')`, "true")
		b.waitFor("symmetric thread gutters", `(function(){var chat=document.getElementById('tab-chat').getBoundingClientRect(),main=document.getElementById('main-content').getBoundingClientRect(),panel=document.getElementById('task-details-panel').getBoundingClientRect();return String(Math.abs((panel.left-chat.right)-(chat.left-main.left))<1)})()`, "true")
		b.waitFor("reference tab vertical spacing", `(function(){var row=getComputedStyle(document.querySelector('#task-details-panel [role="tablist"]')),tab=getComputedStyle(document.getElementById('inspector-tab-details'));return [row.marginTop,row.marginBottom,tab.paddingTop,tab.paddingBottom].join(':')})()`, "8px:16px:10px:10px")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1100, "height": 760, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("desktop windowed panel below titlebar and docked", `(function(){var p=document.getElementById('task-details-panel'),r=p.getBoundingClientRect(),chat=document.getElementById('tab-chat').getBoundingClientRect();return String(r.top===46 && r.bottom===innerHeight && p.dataset.overlay==='false' && chat.right<r.left)})()`, "true")
		b.evaluate(`document.getElementById('desktop-project-titlebar').style.height='54px'; 'resized'`)
		b.waitFor("desktop titlebar height tracked", `String(document.getElementById('task-details-panel').getBoundingClientRect().top)`, "54")
		b.evaluate(`document.getElementById('desktop-project-titlebar').style.height=''; 'restored'`)
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1500, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("titlebar offset restored", `String(document.getElementById('task-details-panel').getBoundingClientRect().top)`, "46")
		b.click("#task-panel-divider")
		key := func(key string) {
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": key}, nil)
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key}, nil)
		}
		key("ArrowLeft")
		b.waitFor("keyboard resize persisted", `localStorage.getItem('task-inspector-width')`, "440")
		var x, y float64
		if _, err := fmt.Sscanf(b.evaluate(`(function(){var r=document.getElementById('task-panel-divider').getBoundingClientRect();return (r.left+r.width/2)+' '+(r.top+20)})()`), "%f %f", &x, &y); err != nil {
			t.Fatal(err)
		}
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x, "y": y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
		b.waitFor("pointer resize has no outline", `getComputedStyle(document.getElementById('task-panel-divider')).outlineStyle`, "none")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x - 60, "y": y, "button": "left", "buttons": 1}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x - 60, "y": y, "button": "left", "buttons": 0, "clickCount": 1}, nil)
		b.waitFor("pointer resize persisted", `localStorage.getItem('task-inspector-width')`, "500")
		for _, kind := range []string{"mousePressed", "mouseReleased"} {
			b.call("Input.dispatchMouseEvent", map[string]any{"type": kind, "x": x - 60, "y": y, "button": "left", "clickCount": 2}, nil)
		}
		b.waitFor("double click reset", `localStorage.getItem('task-inspector-width')`, "420")
		b.click(`[data-tab="schedules"]`)
		key("ArrowRight")
		b.waitFor("keyboard tab navigation", `document.activeElement.getAttribute('data-tab')+':'+document.activeElement.getAttribute('aria-selected')`, "chaining:true")
		b.click("#task-details-opener")
		b.click("#task-details-opener")
		b.waitFor("tab remembered", `document.querySelector('#task-details-panel [aria-selected="true"]').dataset.tab`, "chaining")
		b.click("#task-details-opener")
		b.click("#task-message-input")
		b.typeText("Keep this draft")
		b.wheel("#task-thread-messages", -650)
		b.evaluate(`window.savedThread=document.getElementById('task-thread-view');window.savedComposer=document.getElementById('task-message-input');window.savedScroll=document.getElementById('task-thread-messages').scrollTop;window.savedSources=window._threadEventSources; 'saved'`)
		files.Store(3)
		for i := 0; i < 20; i++ {
			fileEvents <- `{"type":"file_modified","task_id":"workspace-task","project_id":"workspace-project"}`
		}
		b.waitFor("live summary", `document.getElementById('task-change-activity').hidden+':'+document.querySelector('[data-change-summary]').textContent.includes('3 files changed')`, "false:true")
		b.evaluate(`window.savedScroll=document.getElementById('task-thread-messages').scrollTop; 'saved'`)
		b.click("[data-review-changes]")
		b.waitFor("one click full diff", `String(document.getElementById('diff-viewer')&&document.getElementById('diff-viewer').textContent)`, "Authoritative full diff")
		b.waitFor("context retained", `new URLSearchParams(location.search).get('from')+':'+new URLSearchParams(location.search).get('tab')`, "alerts:changes")
		b.waitFor("thread retained", `(window.savedThread===document.getElementById('task-thread-view'))+':'+(window.savedSources===window._threadEventSources)`, "true:true")
		b.click("#task-details-opener")
		b.waitFor("diff inspector docked without covering changes", `(function(){var p=document.getElementById('task-details-panel'),diff=document.getElementById('tab-changes');return String(p.dataset.overlay==='false' && !diff.inert && diff.getBoundingClientRect().right<p.getBoundingClientRect().left)})()`, "true")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1100, "height": 760, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("windowed desktop diff inspector docked below titlebar", `(function(){var p=document.getElementById('task-details-panel'),r=p.getBoundingClientRect(),diff=document.getElementById('tab-changes');return String(r.top===46 && p.dataset.overlay==='false' && !diff.inert && diff.getBoundingClientRect().right<r.left)})()`, "true")
		b.click("#task-details-opener")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1500, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.click("#task-workspace-back")
		b.waitFor("draft and exact scroll restored", `(window.savedComposer===document.getElementById('task-message-input'))+':'+document.getElementById('task-message-input').value+':'+(document.getElementById('task-thread-messages').scrollTop===window.savedScroll)`, "true:Keep this draft:true")
		b.navigateHistory(-1)
		b.waitFor("Back restores diff in place", `document.getElementById('tab-changes').classList.contains('hidden')+':'+(window.savedThread===document.getElementById('task-thread-view'))`, "false:true")
		b.navigateHistory(1)
		b.waitFor("Forward restores thread in place", `document.getElementById('tab-chat').classList.contains('hidden')+':'+(window.savedThread===document.getElementById('task-thread-view'))`, "false:true")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": true}, nil)
		b.click("#task-details-opener")
		b.waitFor("mobile full screen sheet", `(function(){var p=document.getElementById('task-details-panel'),r=p.getBoundingClientRect();return p.dataset.overlay+':'+Math.round(r.width)+':'+Math.round(r.left)})()`, "true:390:0")
		b.click("#task-details-opener")
		b.waitFor("same mobile toggle closes and retains right-panel icon", `document.getElementById('task-details-panel').hidden+':'+document.querySelector('#task-details-opener line').getAttribute('x1')`, "true:15")
		b.click("#task-details-opener")
		key("Escape")
		b.waitFor("Escape restores opener focus", `document.getElementById('task-details-panel').hidden+':'+document.activeElement.id`, "true:task-details-opener")
	})
	if threadRequests.Load() != 1 {
		t.Errorf("thread was replaced: %d requests", threadRequests.Load())
	}
	if summaryRequests.Load() > 4 {
		t.Errorf("file invalidations not debounced: %d", summaryRequests.Load())
	}
	if diffRequests.Load() == 0 {
		t.Fatal("full diff never loaded")
	}
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/workspace-task?tab=changes", "task-workspace-deep-links", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 600, "height": 760, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("direct diff lazy load", `String(document.getElementById('diff-viewer')&&document.getElementById('diff-viewer').textContent)`, "Authoritative full diff")
		b.waitFor("background thread mounted", `String(Boolean(document.getElementById('task-message-input')))`, "true")
		for _, tab := range []string{"chat", "thread", "history", "details", "schedules", "chaining", "attachments", "lifecycle"} {
			b.call("Page.navigate", map[string]any{"url": server.URL + "/tasks/workspace-task?from=automation&automation_id=origin&automation_name=Source&tab=" + tab}, nil)
			b.waitFor("deep link "+tab, `String(document.getElementById('task-detail-content')&&document.getElementById('task-detail-content').dataset.initialTab)`, tab)
			wantPanel := "false"
			if tab == "chat" || tab == "thread" || tab == "history" {
				wantPanel = "true"
			}
			b.waitFor("surface "+tab, `document.getElementById('task-details-panel').hidden+':'+document.getElementById('tab-chat').classList.contains('hidden')`, wantPanel+":false")
			if wantPanel == "false" {
				b.waitFor("selected inspector "+tab, `String(document.querySelector('#task-details-panel [aria-selected="true"]')&&document.querySelector('#task-details-panel [aria-selected="true"]').dataset.tab)`, tab)
				if tab == "details" {
					b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
					b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape"}, nil)
					b.waitFor("direct overlay Escape", `document.getElementById('task-details-panel').hidden+':'+document.activeElement.id`, "true:task-details-opener")
				}
			}
		}
	})
}
