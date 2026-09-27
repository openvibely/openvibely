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
		b.waitFor("inspector flush with viewport", `(function(){var r=document.getElementById('task-details-panel').getBoundingClientRect();return [Math.round(r.top),Math.round(innerWidth-r.right),Math.round(innerHeight-r.bottom)].join(':')})()`, "0:0:0")
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
		b.click(`[aria-label="Collapse details panel"]`)
		b.click("#task-details-opener")
		b.waitFor("tab remembered", `document.querySelector('#task-details-panel [aria-selected="true"]').dataset.tab`, "chaining")
		b.click(`[aria-label="Collapse details panel"]`)
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
		b.click("#task-workspace-back")
		b.waitFor("draft and exact scroll restored", `(window.savedComposer===document.getElementById('task-message-input'))+':'+document.getElementById('task-message-input').value+':'+(document.getElementById('task-thread-messages').scrollTop===window.savedScroll)`, "true:Keep this draft:true")
		b.navigateHistory(-1)
		b.waitFor("Back restores diff in place", `document.getElementById('tab-changes').classList.contains('hidden')+':'+(window.savedThread===document.getElementById('task-thread-view'))`, "false:true")
		b.navigateHistory(1)
		b.waitFor("Forward restores thread in place", `document.getElementById('tab-chat').classList.contains('hidden')+':'+(window.savedThread===document.getElementById('task-thread-view'))`, "false:true")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": true}, nil)
		b.click("#task-details-opener")
		b.waitFor("mobile full screen sheet", `(function(){var p=document.getElementById('task-details-panel'),r=p.getBoundingClientRect();return p.dataset.overlay+':'+Math.round(r.width)+':'+Math.round(r.left)})()`, "true:390:0")
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
