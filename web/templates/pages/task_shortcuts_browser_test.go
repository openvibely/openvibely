package pages

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_TaskShortcuts(t *testing.T) {
	for _, delay := range []time.Duration{0, 100 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) { testTaskShortcuts(t, delay) })
	}
}

func testTaskShortcuts(t *testing.T, threadDelay time.Duration) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "shortcuts", Name: "Shortcuts"}
	var lists atomic.Int32
	var holdCachedC atomic.Bool
	releaseCachedC := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		var component templ.Component
		switch {
		case r.URL.Path == "/tasks/sidebar":
			component = layout.TaskListPanelContent(project.ID, []layout.TaskListGroup{
				{Label: "Active", Tasks: []models.Task{{ID: "c", ProjectID: project.ID, Title: "C"}, {ID: "a", ProjectID: project.ID, Title: "A"}}},
				{Label: "Completed", Tasks: []models.Task{{ID: "b", ProjectID: project.ID, Title: "B"}}},
			})
		case r.URL.Path == "/breadcrumb-selectors/tasks":
			lists.Add(1)
			items := []models.BreadcrumbSelectorItem{}
			for _, id := range []string{"a", "b", "c"} {
				items = append(items, models.BreadcrumbSelectorItem{ID: id, Name: id, URL: "/tasks/" + id + "?project_id=shortcuts"})
			}
			component = components.BreadcrumbSelectorResults("Task", r.URL.Query().Get("current_id"), items, false, false)
		case strings.HasSuffix(r.URL.Path, "/changes/summary"):
			fmt.Fprint(w, `{"files":0,"insertions":0,"deletions":0,"review_comments":0}`)
			return
		case strings.HasSuffix(r.URL.Path, "/thread"):
			time.Sleep(threadDelay)
			task := &models.Task{ID: strings.Split(r.URL.Path, "/")[2], ProjectID: project.ID, Title: "Task", Status: models.StatusCompleted, Category: models.CategoryCompleted}
			component = components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30)
		case r.URL.Path == "/tasks/a" || r.URL.Path == "/tasks/b" || r.URL.Path == "/tasks/c":
			if r.URL.Path == "/tasks/c" && holdCachedC.Load() {
				select {
				case <-releaseCachedC:
				case <-r.Context().Done():
					return
				}
			}
			task := &models.Task{ID: strings.TrimPrefix(r.URL.Path, "/tasks/"), ProjectID: project.ID, Title: "Task", Status: models.StatusCompleted, Category: models.CategoryCompleted}
			if r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
				component = TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil, components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30))
			} else if r.Header.Get("HX-Request") == "true" {
				component = TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil)
			} else {
				component = TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chat", nil)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Superseded navigations are aborted by the browser mid-response.
		if err := component.Render(r.Context(), w); err != nil && r.Context().Err() == nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
			t.Error(err)
		}
	}))
	defer server.Close()
	defer close(releaseCachedC)
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/a?project_id=shortcuts", "task-shortcuts", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1600, "height": 1000, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("composer", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.waitFor("breadcrumb shortcut labels", `String((function(){var button=document.querySelector('[data-breadcrumb-selector-button]'),title=button.title||button.dataset.ovTooltip||'';return title.includes('Last visited (') && title.includes('Previous (Left arrow)') && title.includes('Next (Right arrow)') && button.getAttribute('aria-keyshortcuts').includes('ArrowLeft')})())`, "true")
		b.evaluate(`['a','b','c'].forEach(function(id){localStorage.setItem('openvibely-task-thread-message-history-'+id,JSON.stringify(['Older message','Newest message']))});'ready'`)
		command := `(/Mac|iPhone|iPad/.test(navigator.platform)?{metaKey:true}:{ctrlKey:true})`
		key := func(code, modifiers string) {
			flags := 0
			fmt.Sscan(b.evaluate(`String((function(m){return (m.altKey?1:0)|(m.ctrlKey?2:0)|(m.metaKey?4:0)|(m.shiftKey?8:0)})(`+modifiers+`))`), &flags)
			keyName := strings.TrimPrefix(code, "Key")
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": keyName, "code": code, "modifiers": flags}, nil)
			b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": keyName, "code": code, "modifiers": 0}, nil)
		}
		key("KeyK", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("project shortcut opens project search", `String(document.getElementById('project-selector-dialog').open && document.activeElement.id==='project-selector-search')`, "true")
		key("KeyK", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("project shortcut closes without return ring", `String(!document.getElementById('project-selector-dialog').open && document.activeElement.id==='project-selector-trigger' && getComputedStyle(document.activeElement).outlineStyle==='none')`, "true")
		var projectX, projectY float64
		fmt.Sscan(b.evaluate(`(function(){var r=document.getElementById('project-selector-trigger').getBoundingClientRect();return (r.left+r.width/2)+' '+(r.top+r.height/2)})()`), &projectX, &projectY)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": projectX, "y": projectY}, nil)
		b.waitFor("project hover works after shortcut close without click", `String(document.activeElement.id==='project-selector-trigger' && getComputedStyle(document.activeElement).outlineStyle==='none' && getComputedStyle(document.activeElement).backgroundColor!=='rgba(0, 0, 0, 0)')`, "true")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 10, "y": 10}, nil)
		b.waitFor("project hover clears on pointer exit", `getComputedStyle(document.getElementById('project-selector-trigger')).backgroundColor`, "rgba(0, 0, 0, 0)")
		b.evaluate(`document.getElementById('task-message-input').value='Draft'; document.getElementById('task-message-input').dispatchEvent(new Event('input',{bubbles:true})); document.getElementById('task-message-input').focus(); window.sidebarBefore=document.documentElement.className; 'ready'`)
		key("KeyB", command)
		b.waitFor("task list toggles from composer", `String(document.documentElement.className!==window.sidebarBefore && document.activeElement.id==='task-message-input')`, "true")
		b.evaluate(`window.sidebarAfter=document.documentElement.className;'ready'`)
		key("KeyB", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("details opens without sidebar or focus change", `String(!document.getElementById('task-details-panel').hidden && document.documentElement.className===window.sidebarAfter && document.activeElement.id==='task-message-input')`, "true")
		key("KeyB", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("details closes retaining draft", `String(document.getElementById('task-details-panel').hidden && document.getElementById('task-message-input').value==='Draft' && document.activeElement.id==='task-message-input')`, "true")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("ArrowUp", `{}`)
		b.waitFor("plain up recalls history", `document.getElementById('task-message-input').value`, "Newest message")
		key("ArrowDown", `{}`)
		b.waitFor("plain down restores draft", `document.getElementById('task-message-input').value`, "Draft")
		key("KeyK", command)
		b.waitFor("breadcrumb focuses search", `String(document.activeElement.hasAttribute('data-breadcrumb-selector-search'))`, "true")
		key("KeyK", command)
		b.waitFor("command K closes breadcrumb", `String(!document.querySelector('dialog[open]'))`, "true")
		b.waitFor("command K restores focus without a ring", `String(document.activeElement.hasAttribute('data-breadcrumb-selector-button') && document.activeElement.hasAttribute('data-selector-return-focus') && getComputedStyle(document.activeElement).outlineStyle==='none')`, "true")
		var hoverX, hoverY float64
		fmt.Sscan(b.evaluate(`(function(){var r=document.activeElement.getBoundingClientRect();return (r.left+r.width/2)+' '+(r.top+r.height/2)})()`), &hoverX, &hoverY)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": hoverX, "y": hoverY}, nil)
		b.waitFor("hover after shortcut close keeps ring suppressed", `String(document.activeElement.hasAttribute('data-selector-return-focus') && getComputedStyle(document.activeElement).outlineStyle==='none' && getComputedStyle(document.activeElement).backgroundColor!=='rgba(0, 0, 0, 0)')`, "true")
		key("KeyK", command)
		b.waitFor("command K reopens breadcrumb", `String(document.activeElement.hasAttribute('data-breadcrumb-selector-search'))`, "true")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
		b.waitFor("breadcrumb closes", `String(!document.querySelector('dialog[open]'))`, "true")
		b.waitFor("close restores breadcrumb button focus", `String(document.activeElement.hasAttribute('data-breadcrumb-selector-button'))`, "true")
		// Plain arrows navigate breadcrumbs; modified arrows remain project shortcuts.
		for _, id := range []string{"b", "c", "b"} {
			b.evaluate(`document.querySelector('[data-breadcrumb-selector-button]').focus();'ready'`)
			direction := "ArrowRight"
			if id == "b" && b.evaluate(`location.pathname`) == "/tasks/c" {
				direction = "ArrowLeft"
			}
			key(direction, `{}`)
			b.waitFor("task visit committed", `location.pathname`, "/tasks/"+id)
			b.waitFor("breadcrumb visit settled", `String(!document.querySelector('template[hx-history-elt]') && !!document.getElementById('task-message-input'))`, "true")
		}
		b.evaluate(`document.getElementById('task-message-input').focus();'ready'`)
		for _, code := range []string{"ArrowLeft", "ArrowRight"} {
			b.waitFor("plain arrows leave composer editing unhandled", `String((function(){var e=new KeyboardEvent('keydown',{key:'`+code+`',code:'`+code+`',bubbles:true,cancelable:true});document.getElementById('task-message-input').dispatchEvent(e);return !e.defaultPrevented && location.pathname==='/tasks/b'})())`, "true")
		}
		key("KeyL", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("last task", `document.getElementById('task-detail-content').dataset.taskId`, "c")
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("KeyL", `Object.assign({shiftKey:true},`+command+`)`)
		// A cached preview shows task b before the server response replaces it; the menu must open on the committed page.
		b.waitFor("last task toggles back", `document.getElementById('task-detail-content').dataset.taskId+':'+!document.querySelector('template[hx-history-elt]')`, "b:true")
		b.waitFor("last task composer focus", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		key("KeyK", command)
		b.waitFor("task menu options", `String(document.querySelectorAll('[data-breadcrumb-selector-option]').length)`, "3")
		key("ArrowDown", `{}`)
		key("Enter", `{}`)
		b.waitFor("menu opens next task", `document.getElementById('task-detail-content').dataset.taskId`, "c")
		b.waitFor("menu selection composer focus", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.typeText("Ready to type")
		b.waitFor("typing immediately after menu navigation", `document.getElementById('task-message-input').value`, "Ready to type")
		b.waitFor("task committed before sidebar shortcut", `String(!document.querySelector('template[hx-history-elt]') && document.activeElement.id === 'task-message-input')`, "true")
		b.waitFor("sidebar loaded", `String(document.querySelectorAll('[data-task-list-row]').length)`, "3")
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("sidebar order differs from breadcrumb", `location.pathname`, "/tasks/a")
		b.waitFor("sidebar navigation focuses composer", `document.activeElement.id`, "task-message-input")
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("sidebar crosses dropzone", `location.pathname`, "/tasks/b")
		b.waitFor("composer at last sidebar task", `document.activeElement.id`, "task-message-input")
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("sidebar does not wrap", `location.pathname`, "/tasks/b")
		key("ArrowUp", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("sidebar previous", `location.pathname`, "/tasks/a")
		b.waitFor("composer after sidebar previous", `document.activeElement.id`, "task-message-input")
		b.evaluate(`document.querySelector('[data-task-list-group="Completed"]').open=false;'closed'`)
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("sidebar skips collapsed groups", `location.pathname`, "/tasks/a")

		// The builder's dirty-state contract must protect sidebar navigation too.
		b.evaluate(`document.querySelector('[data-task-list-group="Completed"]').open=true;window.savedConfirm=window.confirm;window.confirmCalls=0;window.confirm=()=>{window.confirmCalls++;return false};window.dirtyBuilder=document.createElement('div');dirtyBuilder.setAttribute('data-automation-yaml-builder','');dirtyBuilder.hasUnsavedAutomationChanges=()=>true;document.body.appendChild(dirtyBuilder);history.replaceState({},'', '/automations/example/builder?project_id=shortcuts');document.activeElement.blur();'ready'`)
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("dirty builder prompts before sidebar navigation", `String(window.confirmCalls)`, "1")
		b.waitFor("cancel keeps builder URL", `location.pathname`, "/automations/example/builder")
		b.waitFor("cancel retains edits", `String(dirtyBuilder.isConnected && dirtyBuilder.hasUnsavedAutomationChanges())`, "true")
		b.evaluate(`window.confirm=()=>{window.confirmCalls++;return true};'ready'`)
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("discard opens sidebar task", `location.pathname`, "/tasks/c")
		b.waitFor("discard confirmed once", `String(window.confirmCalls)`, "2")
		b.waitFor("composer after discard", `document.activeElement.id`, "task-message-input")
		b.evaluate(`dirtyBuilder.hasUnsavedAutomationChanges=()=>false;'ready'`)
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("clean builder allows navigation", `location.pathname`, "/tasks/a")
		b.waitFor("clean builder does not prompt", `String(window.confirmCalls)`, "2")
		b.evaluate(`dirtyBuilder.remove();window.confirm=window.savedConfirm;'ready'`)

		// Hold the authoritative response so C is visible while the URL still says A.
		holdCachedC.Store(true)
		b.evaluate(`window.cachedNavigation=window.openVibelyNavigate('/tasks/c?from=tasks').catch(()=>{});'started'`)
		b.waitFor("cached C displayed before URL commits", `String(document.getElementById('task-detail-content').dataset.taskId==='c' && location.pathname==='/tasks/a' && new URL(window.openVibelyDisplayedURL()).pathname==='/tasks/c')`, "true")
		b.waitFor("cached composer focused", `document.activeElement.id`, "task-message-input")
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("sidebar uses displayed C and opens A rather than B", `document.getElementById('task-detail-content').dataset.taskId`, "a")
		b.waitFor("sidebar navigation commits A", `String(location.pathname==='/tasks/a' && !document.querySelector('template[hx-history-elt]'))`, "true")

	})
}
