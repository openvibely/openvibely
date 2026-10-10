package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_KanbanHeaderSearch(t *testing.T) {
	for _, width := range []int{1280, 390} {
		t.Run(fmt.Sprint(width), func(t *testing.T) { testKanbanHeaderSearch(t, width) })
	}
}

func testKanbanHeaderSearch(t *testing.T, width int) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "search-project", Name: "Search"}
	tasks := []models.Task{
		{ID: "alpha", Title: "Alpha login fix", Prompt: "repair oauth", ProjectID: project.ID, Category: models.CategoryBacklog, Status: models.StatusPending},
		{ID: "beta", Title: "Beta docs", Prompt: "write guide", ProjectID: project.ID, Category: models.CategoryBacklog, Status: models.StatusPending},
		{ID: "gamma", Title: "Gamma", Prompt: "OAuth refresh", ProjectID: project.ID, Category: models.CategoryCompleted, Status: models.StatusCompleted},
		{ID: "delta", Title: "Delta", ProjectID: project.ID, Category: models.CategoryActive, Status: models.StatusRunning},
		{ID: "epsilon", Title: "Epsilon", ProjectID: project.ID, Category: models.CategoryActive, Status: models.StatusQueued},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		var out bytes.Buffer
		switch r.URL.Path {
		case "/tasks":
			_ = Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out)
		case "/other":
			out.WriteString(`<!doctype html><html><body><main id="main-content" hx-history-elt><h2>Other</h2></main></body></html>`)
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, out.String())
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/tasks?project_id="+project.ID, fmt.Sprintf("kanban-search-%d", width), func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": width < 640}, nil)
		b.waitFor("search ready", `document.readyState === 'complete' && window.kanbanRefresh && document.querySelector('[data-kanban-search]') ? 'ready' : 'waiting'`, "ready")
		check := func(label, expression string) {
			t.Helper()
			if got := b.evaluate(`String(` + expression + `)`); got != "true" {
				t.Fatalf("%s: %s evaluated to %s", label, expression, got)
			}
		}
		const (
			search = `document.querySelector('[data-kanban-search]')`
			input  = `document.querySelector('[data-kanban-search-input]')`
			toggle = `document.querySelector('[data-kanban-search-toggle]')`
		)
		visible := `Array.from(document.querySelectorAll('#kanban-board .card[data-task-id]')).filter(c => !c.hidden).map(c => c.dataset.taskId).sort().join(',')`
		count := func(col string) string {
			return `document.querySelector('[data-kanban-category="` + col + `"] [data-kanban-count]').textContent.trim()`
		}
		noResults := func(col string) string {
			return `!document.querySelector('[data-kanban-category="` + col + `"] [data-kanban-no-results]').hidden`
		}
		width := func(expr string) string { return expr + `.getBoundingClientRect().width` }
		queued := `getComputedStyle(document.querySelector('.kanban-queue')).display !== 'none'`

		check("search sits next to Add Task", search+`.nextElementSibling.textContent.includes('Add Task')`)
		check("collapsed by default", search+`.dataset.open === 'false' && `+width(search)+` <= 33`)
		check("Active count has refresh hook", count("active")+` === '2'`)

		b.click(`[data-kanban-search-toggle]`)
		check("click opens and focuses input", search+`.dataset.open === 'true' && document.activeElement === `+input)
		b.waitFor("open animation", `String(`+width(search)+` > 100)`, "true")
		check("open box stays inside header", search+`.getBoundingClientRect().left >= document.querySelector('[data-page-header] h2').getBoundingClientRect().right`)
		check("Add Task stays on screen", search+`.nextElementSibling.getBoundingClientRect().right <= window.innerWidth`)

		b.typeText("oauth")
		check("filters by title and prompt", visible+` === 'alpha,gamma'`)
		check("backlog count", count("backlog")+` === '1'`)
		check("Active count follows search", count("active")+` === '0'`)
		check("empty Queued section hidden", `!(`+queued+`)`)
		check("Active shows no results", noResults("active"))
		check("Backlog has matches", `!(`+noResults("backlog")+`)`)

		b.click(`[data-page-header] h2`)
		check("real click elsewhere collapses", search+`.dataset.open === 'false'`)
		b.waitFor("close animation", `String(`+width(search)+` <= 33)`, "true")
		check("term keeps filtering while collapsed", visible+` === 'alpha,gamma' && `+search+`.dataset.active === 'true'`)

		b.click(`[data-kanban-search-toggle]`)
		check("reopens with term", search+`.dataset.open === 'true' && document.activeElement === `+input)
		// iOS Safari keeps focus on the input when blank space is tapped; simulate a press that does not move focus.
		b.evaluate(`(document.querySelector('[data-page-header] h2').dispatchEvent(new PointerEvent('pointerdown', {bubbles:true})), 'ok')`)
		check("outside press collapses without focus change", search+`.dataset.open === 'false' && document.activeElement !== `+input)
		check("no-results sits inside In Progress", `!!document.querySelector('[data-drop-type="status"][data-status="running"] > [data-kanban-no-results]')`)
		check("native clear button hidden", `Array.from(document.styleSheets).some(sheet => { try { return Array.from(sheet.cssRules).some(rule => (rule.selectorText || '').includes('kanban-search-input::-webkit-search-cancel-button')); } catch (_) { return false; } })`)

		b.click(`[data-kanban-search-toggle]`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		check("Escape clears and collapses", search+`.dataset.open === 'false' && `+input+`.value === '' && `+visible+` === 'alpha,beta,delta,epsilon,gamma'`)
		check("Escape returns focus to icon", `document.activeElement === `+toggle)
		check("counts restored", count("active")+` === '2' && `+count("backlog")+` === '2'`)
		check("Queued restored", queued)
		check("no-results hidden", `!(`+noResults("active")+`)`)

		b.click(`[data-kanban-search-toggle]`)
		b.typeText("zzz")
		check("every column shows no results", noResults("backlog")+` && `+noResults("completed")+` && `+noResults("active"))

		b.evaluate(`(function(){var a=document.createElement('a');a.id='nav-other';a.textContent='Other';a.setAttribute('hx-get','/other');a.setAttribute('hx-target','#main-content');a.setAttribute('hx-select','#main-content');a.setAttribute('hx-swap','outerHTML');a.setAttribute('hx-push-url','true');document.querySelector('[data-page-header]').appendChild(a);htmx.process(a);a.click();return 'ok';})()`)
		b.waitFor("navigated away", `location.pathname`, "/other")
		b.evaluate(`(history.back(), 'ok')`)
		b.waitFor("restored from history", `String(!!document.querySelector('#kanban-board'))`, "true")
		b.waitFor("history restore resyncs filter", visible, "alpha,beta,delta,epsilon,gamma")
		check("restored search is collapsed and inactive", search+`.dataset.open === 'false' && `+search+`.dataset.active === 'false'`)
		check("restored no-results hidden", `!(`+noResults("backlog")+`)`)
	})
}
