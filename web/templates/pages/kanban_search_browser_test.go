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

		b.evaluate(`(window.__iconColor = getComputedStyle(`+toggle+`).color, 'ok')`)
		b.click(`[data-kanban-search-toggle]`)
		check("click opens and focuses input", search+`.dataset.open === 'true' && document.activeElement === `+input)
		b.waitFor("open animation", `String(`+width(search)+` > 100)`, "true")
		check("open box stays inside header", search+`.getBoundingClientRect().left >= document.querySelector('[data-page-header] h2').getBoundingClientRect().right`)
		check("Add Task stays on screen", search+`.nextElementSibling.getBoundingClientRect().right <= window.innerWidth`)

		b.typeText("oauth")
		b.waitFor("debounced filter", visible, "alpha,gamma")
		check("status announces matches", `document.querySelector('[data-kanban-search-status]').textContent === '2 tasks match'`)
		check("backlog count", count("backlog")+` === '1'`)
		check("Active count follows search", count("active")+` === '0'`)
		check("empty Queued section hidden", `!(`+queued+`)`)
		check("Active shows no results", noResults("active"))
		check("Active drop hint hidden while no results", `document.querySelector('[data-kanban-category="active"] [data-kanban-drop-hint]') === null || document.querySelector('[data-kanban-category="active"] [data-kanban-drop-hint]').hidden`)
		check("Backlog has matches", `!(`+noResults("backlog")+`)`)

		b.click(`[data-page-header] h2`)
		check("click elsewhere keeps box open with text", search+`.dataset.open === 'true' && `+input+`.value === 'oauth' && `+visible+` === 'alpha,gamma'`)
		check("icon keeps its colour while a search is applied", `getComputedStyle(`+toggle+`).color === window.__iconColor`)
		b.click(`[data-kanban-search-toggle]`)
		check("icon click keeps box open with text and focuses input", search+`.dataset.open === 'true' && `+toggle+`.getAttribute('aria-expanded') === 'true' && document.activeElement === `+input)
		// iOS Safari keeps focus on the input when blank space is tapped; simulate a press that does not move focus.
		b.evaluate(`(document.querySelector('[data-page-header] h2').dispatchEvent(new PointerEvent('pointerdown', {bubbles:true})), 'ok')`)
		check("outside press keeps box open with text", search+`.dataset.open === 'true'`)

		b.evaluate(`(`+input+`.value = '', `+input+`.dispatchEvent(new Event('input', {bubbles:true})), 'ok')`)
		b.waitFor("cleared filter", visible, "alpha,beta,delta,epsilon,gamma")
		b.click(`[data-page-header] h2`)
		check("real click elsewhere collapses empty box", search+`.dataset.open === 'false'`)
		b.waitFor("close animation", `String(`+width(search)+` <= 33)`, "true")
		b.click(`[data-kanban-search-toggle]`)
		check("reopens", search+`.dataset.open === 'true' && document.activeElement === `+input)
		b.click(`[data-kanban-search-toggle]`)
		check("icon click collapses empty box", search+`.dataset.open === 'false' && `+toggle+`.getAttribute('aria-expanded') === 'false' && document.activeElement === `+toggle)
		b.waitFor("icon collapse animation", `String(`+width(search)+` <= 33)`, "true")
		b.click(`[data-kanban-search-toggle]`)
		b.evaluate(`(document.querySelector('[data-page-header] h2').dispatchEvent(new PointerEvent('pointerdown', {bubbles:true})), 'ok')`)
		check("outside press collapses empty box without focus change", search+`.dataset.open === 'false' && document.activeElement !== `+input)

		b.click(`[data-kanban-search-toggle]`)
		b.typeText("oauth")
		b.waitFor("re-filter", visible, "alpha,gamma")
		check("no-results sits inside In Progress", `!!document.querySelector('[data-drop-type="status"][data-status="running"] > [data-kanban-no-results]')`)
		check("native clear button hidden", `Array.from(document.styleSheets).some(sheet => { try { return Array.from(sheet.cssRules).some(rule => (rule.selectorText || '').includes('kanban-search-input::-webkit-search-cancel-button')); } catch (_) { return false; } })`)

		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		check("Escape clears and collapses", search+`.dataset.open === 'false' && `+input+`.value === '' && `+visible+` === 'alpha,beta,delta,epsilon,gamma'`)
		check("Escape returns focus to icon", `document.activeElement === `+toggle)
		check("counts restored", count("active")+` === '2' && `+count("backlog")+` === '2'`)
		check("Queued restored", queued)
		check("no-results hidden", `!(`+noResults("active")+`)`)
		check("status cleared", `document.querySelector('[data-kanban-search-status]').textContent === ''`)

		b.evaluate(`(document.querySelector('[data-kanban-filter="priority"][data-value="1"][data-column="backlog"]').click(), 'ok')`)
		check("filter-only hides every backlog card", count("backlog")+` === '0'`)
		check("filter-only shows no results", noResults("backlog"))
		b.evaluate(`(document.querySelector('[data-kanban-category="backlog"] [data-kanban-clear]').click(), 'ok')`)
		check("clearing filters hides no results", `!(`+noResults("backlog")+`) && `+count("backlog")+` === '2'`)

		b.click(`[data-kanban-search-toggle]`)
		b.typeText("epsilon")
		b.waitFor("queued-only match", visible, "epsilon")
		check("Queued shown for its match", queued)
		check("In Progress explains its empty area", noResults("active"))
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		check("Escape clears queued-only search", `!(`+noResults("active")+`)`)

		b.click(`[data-kanban-search-toggle]`)
		b.typeText("zzz")
		b.waitFor("debounced no results", `String(`+noResults("backlog")+` && `+noResults("completed")+` && `+noResults("active")+`)`, "true")
		check("status announces no results", `document.querySelector('[data-kanban-search-status]').textContent === 'No matching tasks'`)

		b.evaluate(`(function(){var a=document.createElement('a');a.id='nav-other';a.textContent='Other';a.setAttribute('hx-get','/other');a.setAttribute('hx-target','#main-content');a.setAttribute('hx-select','#main-content');a.setAttribute('hx-swap','outerHTML');a.setAttribute('hx-push-url','true');document.querySelector('[data-page-header]').appendChild(a);htmx.process(a);a.click();return 'ok';})()`)
		b.waitFor("navigated away", `location.pathname`, "/other")
		b.evaluate(`(history.back(), 'ok')`)
		b.waitFor("restored from history", `String(!!document.querySelector('#kanban-board'))`, "true")
		b.waitFor("history restore resyncs filter", visible, "alpha,beta,delta,epsilon,gamma")
		check("restored search is collapsed and inactive", search+`.dataset.open === 'false' && `+search+`.dataset.active === 'false'`)
		check("restored no-results hidden", `!(`+noResults("backlog")+`)`)

		b.evaluate(`(document.getElementById('task-delta').remove(), window.kanbanRefresh(), 'ok')`)
		b.click(`[data-kanban-search-toggle]`)
		b.typeText("beta")
		b.waitFor("nothing running, queued hidden", visible, "beta")
		check("In Progress shows no results when nothing is running", noResults("active"))
		check("In Progress drop hint hidden", `Array.from(document.querySelectorAll('[data-kanban-category="active"] [data-kanban-drop-hint]')).every(h => h.hidden)`)
	})
}
