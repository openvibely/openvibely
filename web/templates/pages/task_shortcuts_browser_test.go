package pages

import (
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

func TestBrowserFunctional_TaskShortcuts(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "shortcuts", Name: "Shortcuts"}
	var lists atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		var component templ.Component
		switch {
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
			time.Sleep(100 * time.Millisecond) // Exercise focus across the lazy thread response.
			task := &models.Task{ID: strings.Split(r.URL.Path, "/")[2], ProjectID: project.ID, Title: "Task", Status: models.StatusCompleted, Category: models.CategoryCompleted}
			component = components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30)
		case r.URL.Path == "/tasks/a" || r.URL.Path == "/tasks/b" || r.URL.Path == "/tasks/c":
			task := &models.Task{ID: strings.TrimPrefix(r.URL.Path, "/tasks/"), ProjectID: project.ID, Title: "Task", Status: models.StatusCompleted, Category: models.CategoryCompleted}
			if r.Header.Get("HX-Request") == "true" {
				component = TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil)
			} else {
				component = TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chat", nil)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := component.Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/a?project_id=shortcuts", "task-shortcuts", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1600, "height": 1000, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("composer", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`['a','b','c'].forEach(function(id){localStorage.setItem('openvibely-task-thread-message-history-'+id,JSON.stringify(['Older message','Newest message']))});'ready'`)
		command := `(/Mac|iPhone|iPad/.test(navigator.platform)?{metaKey:true}:{ctrlKey:true})`
		key := func(code, modifiers string) {
			b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',Object.assign({key:'` + strings.TrimPrefix(code, "Key") + `',code:'` + code + `',bubbles:true,cancelable:true},` + modifiers + `)));'sent'`)
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
		b.evaluate(`document.getElementById('task-message-input').value='Draft'; document.getElementById('task-message-input').dispatchEvent(new Event('input',{bubbles:true})); document.getElementById('task-message-input').focus(); window.sidebarBefore=document.getElementById('sidebar').className; 'ready'`)
		key("KeyB", command)
		b.waitFor("sidebar toggles from composer", `String(document.getElementById('sidebar').className!==window.sidebarBefore && document.activeElement.id==='task-message-input')`, "true")
		b.evaluate(`window.sidebarAfter=document.getElementById('sidebar').className;'ready'`)
		key("KeyB", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("details opens without sidebar or focus change", `String(!document.getElementById('task-details-panel').hidden && document.getElementById('sidebar').className===window.sidebarAfter && document.activeElement.id==='task-message-input')`, "true")
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
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("command shift down from breadcrumb button switches task", `document.getElementById('task-detail-content').dataset.taskId`, "b")
		b.waitFor("composer after breadcrumb shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		key("ArrowUp", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("return after breadcrumb shortcut", `document.getElementById('task-detail-content').dataset.taskId`, "a")
		// Navigate without showing the selector, then reverse through the frozen list.
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("ArrowUp", `{}`)
		b.waitFor("history active before task switch", `document.getElementById('task-message-input').value`, "Newest message")
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("next task", `document.getElementById('task-detail-content').dataset.taskId`, "b")
		count := lists.Load()
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("ArrowUp", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("return to original task", `document.getElementById('task-detail-content').dataset.taskId`, "a")
		b.waitFor("draft restored after navigation", `String(document.getElementById('task-message-input') && document.getElementById('task-message-input').value)`, "Newest message")
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("ArrowUp", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("first task does not wrap", `location.pathname`, "/tasks/a")
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("next again", `document.getElementById('task-detail-content').dataset.taskId`, "b")
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("ArrowDown", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("second next task", `document.getElementById('task-detail-content').dataset.taskId`, "c")
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("ArrowUp", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("previous task", `document.getElementById('task-detail-content').dataset.taskId`, "b")
		if lists.Load() != count {
			t.Fatal("recent list refetched during cycling")
		}
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("KeyL", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("last task", `document.getElementById('task-detail-content').dataset.taskId`, "c")
		b.waitFor("composer ready for task shortcut", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.evaluate(`document.getElementById('task-message-input').setSelectionRange(0,0);'ready'`)
		key("KeyL", `Object.assign({shiftKey:true},`+command+`)`)
		b.waitFor("last task toggles back", `document.getElementById('task-detail-content').dataset.taskId`, "b")
		b.waitFor("last task composer focus", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		key("KeyK", command)
		b.waitFor("task menu options", `String(document.querySelectorAll('[data-breadcrumb-selector-option]').length)`, "3")
		key("ArrowDown", `{}`)
		key("Enter", `{}`)
		b.waitFor("menu opens next task", `document.getElementById('task-detail-content').dataset.taskId`, "c")
		b.waitFor("menu selection composer focus", `String(document.activeElement && document.activeElement.id)`, "task-message-input")
		b.typeText("Ready to type")
		b.waitFor("typing immediately after menu navigation", `document.getElementById('task-message-input').value`, "Ready to type")
	})
}
