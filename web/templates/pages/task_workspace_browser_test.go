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

func TestBrowserFunctional_TaskDetailPropertyEditors(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "details-project", Name: "Details"}
	agents := []models.LLMConfig{{ID: "model-1", Name: "Example model"}}
	var autoMerge atomic.Bool
	var priority atomic.Int32
	priority.Store(2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		task := &models.Task{ID: "details-task", ProjectID: project.ID, Title: "Task", Prompt: "Original prompt", Status: models.StatusCompleted, Category: models.CategoryCompleted, Priority: int(priority.Load()), AutoMerge: autoMerge.Load()}
		var component templ.Component
		switch r.URL.Path {
		case "/tasks/new":
			component = NewTask([]models.Project{project}, &project, agents, nil)
		case "/tasks/details-task":
			component = TaskDetailPage([]models.Project{project}, task, nil, nil, nil, agents, nil, nil, "details", nil)
		case "/tasks/details-task/detail-status":
			component = TaskDetailMetrics(task, models.TaskExecutionMetrics{}, agents, "")
		case "/tasks/details-task/details/property":
			if r.FormValue("field") == "priority" {
				priority.Store(4)
				task.Priority = 4
				component = TaskDetailMetrics(task, models.TaskExecutionMetrics{}, agents, "")
			} else if r.FormValue("field") == "auto_merge" {
				autoMerge.Store(r.FormValue("value") == "true")
				task.AutoMerge = autoMerge.Load()
				component = TaskAutoMergePanel(task)
			} else if r.FormValue("field") == "prompt" {
				task.Prompt = r.FormValue("value")
				component = TaskPromptPanel(task)
			} else {
				http.Error(w, "invalid field", 400)
				return
			}
		case "/tasks/details-task/goal":
			component = TaskGoalPanel(task.ID, &models.TaskGoal{Objective: r.FormValue("goal"), Status: models.TaskGoalStatusActive})
		case "/tasks/details-task/thread":
			fmt.Fprint(w, `<div style="display:flex;flex-direction:column;flex:1;min-height:0"><form id="task-thread-form" style="margin-top:auto;padding:16px"><textarea name="message">Retained draft</textarea></form></div>`)
			return
		case "/tasks/details-task/changes/summary":
			fmt.Fprint(w, `{"files":0}`)
			return
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := component.Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/new?tab=details", "detail-properties", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1500, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("draft details", `String(document.getElementById('task-details-panel') && !document.getElementById('task-details-panel').hidden)`, "true")
		b.waitFor("property highlight inset", `(function(){var row=document.querySelector('[data-detail-property="priority"]'),text=row.firstElementChild;return String(Math.abs(text.getBoundingClientRect().left-row.getBoundingClientRect().left-12)<1 && getComputedStyle(row).paddingRight==='12px')})()`, "true")
		b.click(`[data-detail-property="priority"]`)
		b.click(`[data-options="priority"] [data-value="4"]`)
		b.waitFor("draft priority", `document.querySelector('[data-draft-property="priority"]').value`, "4")
		b.click(`[data-detail-property="agent_id"]`)
		b.waitFor("model picker open", `String(document.getElementById('task-property-picker').matches(':popover-open'))`, "true")
		b.click(`[data-detail-property="agent_id"]`)
		b.waitFor("same row closes model picker", `String(document.getElementById('task-property-picker').matches(':popover-open'))`, "false")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "a"}, nil)
		b.waitFor("unrelated key does not outline closed row", `getComputedStyle(document.querySelector('[data-detail-property="agent_id"]')).outlineStyle`, "none")
		b.click(`[data-detail-property="agent_id"]`)
		b.typeText("Example")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowDown"}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowUp"}, nil)
		b.waitFor("keyboard option has hover styling without outline", `String(document.activeElement.hasAttribute('data-active') && getComputedStyle(document.activeElement).outlineStyle==='none')`, "true")
		b.waitFor("filtered model keyboard focus", `document.activeElement.dataset.value`, "model-1")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter"}, nil)
		b.waitFor("keyboard selection closes picker", `String(document.getElementById('task-property-picker').matches(':popover-open'))`, "false")
		b.waitFor("composer synchronized", `document.getElementById('task-thread-form-agent-id').value`, "model-1")
		b.waitFor("no return focus outline", `getComputedStyle(document.activeElement).outlineStyle`, "none")
		b.click(`[data-detail-property="agent_id"]`)
		b.waitFor("current model highlighted on reopen", `document.querySelector('#task-property-picker [data-active]').dataset.value`, "model-1")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter"}, nil)
		b.waitFor("immediate enter retains current model", `document.getElementById('task-thread-form-agent-id').value+':'+document.getElementById('task-property-picker').matches(':popover-open')`, "model-1:false")
		b.click(`[data-detail-editor="goal"]`)
		b.typeText("A draft goal")
		b.click(`#task-detail-text-editor [type="submit"]`)
		b.waitFor("draft goal", `document.querySelector('[data-draft-property="goal"]').value`, "A draft goal")
		b.click("#task-panel-divider")
		b.waitFor("thin tab baseline and stronger selected underline", `(function(){var row=document.querySelector('#task-details-panel [role="tablist"]'),active=getComputedStyle(row.querySelector('[aria-selected="true"]')),inactive=getComputedStyle(row.querySelector('[aria-selected="false"]'));return String(getComputedStyle(row).boxShadow.includes('0px -1px') && active.borderBottomWidth==='2px' && active.borderBottomColor===active.color && inactive.borderBottomColor==='rgba(0, 0, 0, 0)')})()`, "true")
		b.waitFor("muted resize hover line", `getComputedStyle(document.getElementById('task-panel-divider'),'::after').opacity`, "0.45")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 10, "y": 10}, nil)
		b.waitFor("resize line fades after leaving", `getComputedStyle(document.getElementById('task-panel-divider'),'::after').opacity`, "0")
		b.evaluate(`document.getElementById('task-panel-divider').dispatchEvent(new PointerEvent('pointerdown',{button:0,pointerId:1})); 'drag'`)
		b.waitFor("resize line persists during drag", `getComputedStyle(document.getElementById('task-panel-divider'),'::after').opacity`, "0.45")
		b.evaluate(`document.getElementById('task-panel-divider').dispatchEvent(new PointerEvent('pointercancel')); 'cancel'`)
		b.waitFor("resize line clears on cancel", `getComputedStyle(document.getElementById('task-panel-divider'),'::after').opacity`, "0")

		b.evaluate(`var d=document.getElementById('task-panel-divider'); d.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',ctrlKey:true,bubbles:true})); d.dispatchEvent(new KeyboardEvent('keydown',{key:'Alt',altKey:true,bubbles:true})); d.blur(); window.dispatchEvent(new Event('blur')); window.dispatchEvent(new Event('focus')); d.focus(); 'returned'`)
		b.waitFor("pointer outline stays hidden on return", `getComputedStyle(document.getElementById('task-panel-divider')).outlineStyle`, "none")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowLeft"}, nil)
		b.waitFor("keyboard focus remains visible", `String(!document.getElementById('task-panel-divider').hasAttribute('data-pointer-focus'))`, "true")
		b.call("Page.navigate", map[string]any{"url": server.URL + "/tasks/details-task?tab=details"}, nil)
		b.waitFor("saved details ready", `String(Boolean(document.querySelector('#task-detail-content[data-task-id="details-task"]') && document.querySelector('#task-thread-form textarea')))`, "true")
		assertFooter := func() {
			b.waitFor("actions aligned with composer bottom and panel left", `(function(){var panel=document.getElementById('task-details-panel').getBoundingClientRect(),actions=document.getElementById('task-detail-actions'),first=actions.firstElementChild.getBoundingClientRect(),composer=document.getElementById('task-thread-form').getBoundingClientRect();return String(Math.abs(first.bottom-composer.bottom)<1 && Math.abs(first.left-panel.left-13)<1 && !document.getElementById('task-detail-view').contains(actions))})()`, "true")
		}
		assertFooter()
		b.click(`#task-auto-merge-panel input[name="auto_merge"]`)
		b.waitFor("auto merge saved and directly visible", `String(document.querySelector('#task-auto-merge-panel').tagName === 'SECTION' && document.querySelector('#task-auto-merge-panel input[name="auto_merge"]').hasAttribute("checked") && !document.querySelector('#task-auto-merge-panel input').disabled)`, "true")
		if !autoMerge.Load() {
			t.Fatal("auto merge switch was not saved")
		}
		b.click(`#task-auto-merge-panel input[name="auto_merge"]`)
		b.waitFor("auto merge disabled", `String(!document.querySelector('#task-auto-merge-panel input[name="auto_merge"]').hasAttribute("checked") && !document.querySelector('#task-auto-merge-panel input').disabled)`, "true")

		for _, width := range []int{340, 420, 720} {
			b.evaluate(fmt.Sprintf(`document.getElementById('task-detail-content').style.setProperty('--task-panel-width','%dpx'); document.querySelector('#task-detail-view [data-property-label]').textContent='long-model-name-'.repeat(30); 'sized'`, width))

			b.waitFor("Details fits horizontally", `(function(){return String(['task-details-panel','task-inspector-body','tab-details','task-detail-view'].every(function(id){var el=document.getElementById(id);return el.scrollWidth<=el.clientWidth}) && Array.from(document.querySelectorAll('#task-detail-view .task-property-row')).every(function(row){var r=row.getBoundingClientRect(),v=document.getElementById('task-detail-view').getBoundingClientRect();return r.left>=v.left && r.right<=v.right}))})()`, "true")
		}
		b.evaluate(`var content=document.getElementById('task-detail-view'),filler=document.createElement('div'); filler.style.height='2000px'; content.appendChild(filler); content.scrollTop=content.scrollHeight; 'scrolled'`)
		assertFooter()
		b.evaluate(`var content=document.getElementById('task-detail-view'); content.lastElementChild.remove(); content.scrollTop=0; 'restored'`)
		b.waitFor("legacy Edit removed", `String(!document.querySelector('[data-task-detail-edit]'))`, "true")
		b.evaluate(`window.retainedThread=document.getElementById('task-thread-form'); 'saved'`)
		b.click(`[data-detail-property="priority"]`)
		b.click(`[data-options="priority"] [data-value="4"]`)
		b.waitFor("saved priority", `document.querySelector('[data-detail-property="priority"]').dataset.value`, "4")
		b.waitFor("mounted thread preserved", `String(window.retainedThread===document.getElementById('task-thread-form'))`, "true")
		b.click(`[data-detail-editor="prompt"]`)
		b.evaluate(`document.querySelector('#task-detail-text-editor textarea').value='Updated prompt'; 'edited'`)
		b.click(`#task-detail-text-editor [type="submit"]`)
		b.waitFor("prompt saved", `document.querySelector('[data-detail-editor="prompt"]').dataset.value`, "Updated prompt")
		b.waitFor("no prompt expander", `String(!document.querySelector('#task-prompt-panel details'))`, "true")
		b.click(`[data-detail-editor="goal"]`)
		b.typeText("Saved goal")
		b.click(`#task-detail-text-editor [type="submit"]`)
		b.waitFor("saved goal", `document.querySelector('[data-detail-editor="goal"]').dataset.value`, "Saved goal")
		for _, field := range []string{"goal", "prompt"} {
			b.click(`[data-detail-editor="` + field + `"]`)
			b.evaluate(`document.querySelector('#task-detail-text-editor textarea').value='Discard this edit'; 'edited'`)
			b.click(`#task-detail-text-editor .modal-backdrop button`)
			b.waitFor("backdrop dismisses "+field, `String(document.getElementById('task-detail-text-editor').open)`, "false")
			b.click(`[data-detail-editor="` + field + `"]`)
			want := "Saved goal"
			if field == "prompt" {
				want = "Updated prompt"
			}
			b.waitFor("dismiss discarded "+field, `document.querySelector('#task-detail-text-editor textarea').value`, want)
			b.click(`#task-detail-text-editor [data-editor-cancel]`)
		}

	})
}

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
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1100, "height": 760, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("breadcrumb and panel toggle do not overlap", `(function(){var caret=document.getElementById('task-resource-selector-button').getBoundingClientRect(),toggle=document.getElementById('task-details-opener').getBoundingClientRect();return String(caret.right+8<=toggle.left)})()`, "true")

		b.click(`[data-tab="schedules"]`)
		b.click(`[aria-label="Add schedule"]`)
		b.evaluate(`document.querySelector('dialog [name="run_at"]').value='2035-01-02T09:30'; document.querySelector('dialog select[name="repeat_type"]').value='weekly'; document.querySelector('dialog select[name="repeat_type"]').dispatchEvent(new Event('change')); 'configured'`)
		b.click(`[data-schedule-save]`)
		b.waitFor("pending schedule card", `String(!document.querySelector('[data-draft-schedule-card]').hidden && !document.querySelector('[data-schedule-editor]').open)`, "true")
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
		b.waitFor("reference tab vertical spacing", `(function(){var row=getComputedStyle(document.querySelector('#task-details-panel [role="tablist"]')),tab=getComputedStyle(document.getElementById('inspector-tab-details'));return [row.marginTop,row.marginBottom,tab.paddingTop,tab.paddingBottom,tab.lineHeight,tab.fontWeight].join(':')})()`, "8px:16px:6px:6px:20px:600")
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
			b.waitFor("deep link "+tab, `String(document.readyState!=='loading'&&document.getElementById('task-detail-content').dataset.initialTab)`, tab)
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

func TestBrowserFunctional_TaskChangesStickyHeadersStayInsideWorkspace(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "sticky-project", Name: "Sticky"}
	task := &models.Task{ID: "sticky-task", ProjectID: project.ID, Title: "Sticky headers", Status: models.StatusCompleted, Category: models.CategoryCompleted}
	diff := "diff --git a/example.go b/example.go\n--- a/example.go\n+++ b/example.go\n@@ -1,100 +1,100 @@\n" + strings.Repeat(" unchanged line\n", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/tasks/sticky-task":
			_ = TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "changes", nil).Render(r.Context(), w)
		case "/tasks/sticky-task/changes":
			_ = components.DiffViewerWithReview(diff, task.ID, nil).Render(r.Context(), w)
		case "/tasks/sticky-task/changes/summary":
			fmt.Fprint(w, `{"files":1}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/sticky-task?tab=changes", "sticky-diff", func(b *composerFocusCDP) {
		b.waitFor("diff loaded", `String(!!document.querySelector('.diff-file-header'))`, "true")
		b.waitFor("scroll diff workspace", `(function(){var s=document.getElementById('tab-changes');s.scrollTop=300;return String(s.scrollTop>0)})()`, "true")
		b.waitFor("whole pinned header visible", `(function(){var s=document.getElementById('tab-changes').getBoundingClientRect(),h=document.querySelector('.diff-file-header'),r=h.getBoundingClientRect();return String(Math.abs(r.top-s.top)<1 && r.bottom<=s.bottom && h.dataset.stuck==='1')})()`, "true")
		b.waitFor("reset scrolling", `(function(){document.getElementById('tab-changes').scrollTop=0;return 'done'})()`, "done")
		b.waitFor("rounded header restored", `String(!document.querySelector('.diff-file-header').hasAttribute('data-stuck'))`, "true")
	})
}

func TestBrowserFunctional_TaskPanelOpenPreferenceAcrossProjects(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if strings.HasSuffix(r.URL.Path, "/thread") || strings.HasSuffix(r.URL.Path, "/changes/summary") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/tasks/") || strings.Contains(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		project := models.Project{ID: r.URL.Query().Get("project_id"), Name: "Project"}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/tasks/new" {
			if err := NewTask([]models.Project{project}, &project, nil, nil).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		task := &models.Task{ID: strings.TrimPrefix(r.URL.Path, "/tasks/"), ProjectID: project.ID, Title: "Task", Status: models.StatusCompleted, Category: models.CategoryCompleted}
		tab := r.URL.Query().Get("tab")
		if tab == "" {
			tab = "chat"
		}
		if r.Header.Get("HX-Request") == "true" {
			if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, tab, nil).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if err := TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, tab, nil).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/one?project_id=one", "panel-preference", func(b *composerFocusCDP) {
		b.waitFor("initially closed", `String(document.getElementById('task-details-opener')?.getAttribute('aria-expanded'))`, "false")
		b.click("#task-details-opener")
		b.waitFor("open preference saved", `localStorage.getItem('task-inspector-open')`, "true")
		navigate := func(path, expected string) {
			b.call("Page.navigate", map[string]any{"url": server.URL + path}, nil)
			b.waitFor("navigation ready", `location.pathname + location.search + ':' + (document.getElementById('task-detail-content')?.dataset.projectId || '')`, path+":"+strings.Split(strings.Split(path, "project_id=")[1], "&")[0])
			b.waitFor("restored panel", `String(document.getElementById('task-details-opener')?.getAttribute('aria-expanded'))`, expected)
		}
		for _, path := range []string{"/tasks/other?project_id=one&tab=details", "/tasks/two?project_id=two", "/tasks/one?project_id=one&tab=details"} {
			b.evaluate(`window.panelSwapSettled = false; window.openVibelyNavigate('` + path + `').then(function(){setTimeout(function(){window.panelSwapSettled=true},100)}); 'started'`)
			b.waitFor("task swap settled", `String(window.panelSwapSettled)`, "true")
			b.waitFor("Details content visible after task/project swap", `String(!document.getElementById('task-details-panel').hidden && !document.getElementById('tab-details').classList.contains('hidden') && document.querySelector('[data-detail-property=priority]').checkVisibility() && document.getElementById('task-detail-view').getBoundingClientRect().height > 100)`, "true")
		}
		navigate("/tasks/two?project_id=two", "true")
		navigate("/tasks/new?project_id=three", "true")
		b.click("#task-details-opener")
		b.waitFor("closed preference saved", `localStorage.getItem('task-inspector-open')`, "false")
		navigate("/tasks/one?project_id=one", "false")
		navigate("/tasks/two?project_id=two&tab=details", "true")
		navigate("/tasks/one?project_id=one", "false")
	})
}

func TestBrowserFunctional_TaskScheduleModal(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "schedule-project", Name: "Schedules"}
	task := &models.Task{ID: "schedule-task", ProjectID: project.ID, Title: "Schedule task", Category: models.CategoryBacklog, Status: models.StatusPending}
	var enabled, clear, removed atomic.Bool
	enabled.Store(true)
	clear.Store(true)
	var interval atomic.Int32
	interval.Store(30)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.Method != http.MethodGet && !strings.HasPrefix(r.URL.Path, "/schedules/") && r.URL.Path != "/tasks/schedule-task/schedule" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPut {
			interval.Store(45)
			clear.Store(false)
			if r.FormValue("repeat_interval") != "45" || r.FormValue("schedule_agent_definition_present") != "" {
				t.Error("invalid schedule update")
			}
		}
		if r.Method == http.MethodPost {
			if r.URL.Path == "/tasks/schedule-task/schedule" {
				removed.Store(false)
				enabled.Store(true)
			} else {
				enabled.Store(!enabled.Load())
			}
		}
		if r.Method == http.MethodDelete {
			removed.Store(true)
		}
		schedules := []models.Schedule{{ID: "saved", TaskID: task.ID, RepeatType: models.RepeatMinutes, RepeatInterval: int(interval.Load()), Enabled: enabled.Load(), ClearContextOnStart: clear.Load(), RunAt: time.Date(2035, 1, 2, 9, 30, 0, 0, time.Local)}}
		if removed.Load() {
			schedules = nil
		}
		w.Header().Set("Content-Type", "text/html")
		if r.Method != http.MethodGet {
			_ = TaskSchedulePanel(task, schedules).Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/tasks/schedule-task" {
			_ = TaskDetailPage([]models.Project{project}, task, nil, nil, schedules, nil, nil, nil, "schedules", nil).Render(r.Context(), w)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/schedule-task?tab=schedules", "schedule-modal", func(b *composerFocusCDP) {
		b.waitFor("schedule card", `String(!!document.getElementById('schedule-card-saved'))`, "true")
		b.click(`[data-schedule-open="task-schedule-saved"]`)
		b.waitFor("schedule modal open", `String(document.getElementById('task-schedule-saved').open)`, "true")
		b.evaluate(`document.querySelector('[data-schedule-editor] [name="repeat_interval"]').value='99'`)
		b.click(`[data-schedule-cancel]`)
		b.click(`[data-schedule-open="task-schedule-saved"]`)
		b.waitFor("cancel discards edits", `document.querySelector('[data-schedule-editor] [name="repeat_interval"]').value`, "30")
		b.evaluate(`document.querySelector('[data-schedule-editor] [name="repeat_interval"]').value='45'; document.querySelector('[data-schedule-editor] input[type="checkbox"]').checked=false; 'configured'`)
		b.click(`[data-schedule-save]`)
		b.waitFor("schedule saved", `String(document.getElementById('schedule-card-saved').textContent.includes('45') && document.getElementById('schedule-card-saved').textContent.includes('Continue previous context') && !document.querySelector('dialog[open]'))`, "true")
		b.evaluate(`window.scheduleLayoutReady=false; requestAnimationFrame(function(){requestAnimationFrame(function(){window.scheduleLayoutReady=true;})}); 'waiting'`)
		b.waitFor("schedule layout settled", `String(window.scheduleLayoutReady)`, "true")
		b.click(`[aria-label="Schedule enabled"]`)
		b.waitFor("schedule paused", `String(document.getElementById('schedule-card-saved').textContent.includes('Paused'))`, "true")
		b.waitFor("schedule card ready after pause", `(function(){var b=document.querySelector('[data-schedule-open="task-schedule-saved"]'),r=b.getBoundingClientRect();return String(b.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)))})()`, "true")
		b.click(`[data-schedule-open="task-schedule-saved"]`)
		b.evaluate(`window.confirm=function(message){window.scheduleDeleteConfirmed=message; return true;}; 'ready'`)
		b.waitFor("schedule delete ready", `(function(){var b=document.querySelector('[data-schedule-editor] [hx-delete]'),r=b.getBoundingClientRect();return String(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)===b)})()`, "true")
		b.click(`[data-schedule-editor] [hx-delete]`)
		b.waitFor("empty schedule restored", `JSON.stringify({add:!!document.querySelector('[aria-label="Add schedule"]'),card:!!document.getElementById('schedule-card-saved'),root:!!document.getElementById('task-schedule-panel')})`, `{"add":true,"card":false,"root":true}`)
		b.waitFor("delete confirmation requested", `window.scheduleDeleteConfirmed`, "Delete this schedule?")
		b.evaluate(`window.scheduleThread=document.getElementById('tab-chat'); 'retained'`)
		b.click(`[aria-label="Add schedule"]`)
		b.waitFor("once hides interval", `String(document.querySelector('[data-schedule-interval]').hidden)`, "true")
		b.evaluate(`var form=document.querySelector('[data-schedule-editor-form]'); form.elements.run_at.value='2020-01-02T12:47'; form.elements.repeat_type.value='weekly'; form.elements.repeat_type.dispatchEvent(new Event('change')); form.requestSubmit(); 'attempted'`)
		b.waitFor("past start stays in editor", `String(document.querySelector('[data-schedule-editor]').open && document.querySelector('[data-run-at-picker]').validationMessage.includes('future') && !document.getElementById('schedule-card-saved'))`, "true")
		b.evaluate(`var input=document.querySelector('[data-run-at-picker]'); input.value='2035-01-02T09:30'; input.dispatchEvent(new Event('input')); 'configured'`)
		b.click(`[data-schedule-save]`)
		b.waitFor("schedule added again", `String(!!document.getElementById('schedule-card-saved') && !document.querySelector('[aria-label="Add schedule"]'))`, "true")
		b.waitFor("thread stays mounted", `String(window.scheduleThread===document.getElementById('tab-chat') && window.scheduleThread.isConnected)`, "true")

	})
}
