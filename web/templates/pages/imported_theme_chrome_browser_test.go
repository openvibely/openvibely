package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/themes"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ImportedThemeStylesTitlebarMenusAndSchedulePickers(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{{ID: "p1", Name: "Alpha"}, {ID: "p2", Name: "Beta"}}
	var base bytes.Buffer
	if err := layout.Base("Imported theme chrome", projects, "p1").Render(layout.WithUIPreferences(layout.WithDesktopMode(context.Background(), true), layout.UIPreferences{PinnedProjectIDs: []string{"p1", "p2"}}), &base); err != nil {
		t.Fatalf("render base: %v", err)
	}
	var picker bytes.Buffer
	if err := components.ScheduleDateTime("2026-10-16T09:30").Render(context.Background(), &picker); err != nil {
		t.Fatalf("render schedule picker: %v", err)
	}
	var calendar bytes.Buffer
	if err := ScheduleCalendarControls(nil).Render(context.Background(), &calendar); err != nil {
		t.Fatalf("render schedule calendar styles: %v", err)
	}
	var search bytes.Buffer
	if err := kanbanSearch().Render(context.Background(), &search); err != nil {
		t.Fatalf("render kanban search: %v", err)
	}
	fixture := `<div id="theme-pinned-header" class="diff-file-header bg-base-200" data-stuck="1" style="position:fixed;left:210px;top:350px;width:140px;height:30px;z-index:999999">Pinned</div><div id="theme-copy-host" class="chat-markdown"><pre style="position:fixed;left:210px;top:400px;width:100px;height:40px;margin:0;z-index:999999"><code>example</code></pre></div><input id="theme-file" type="file" class="file-input file-input-bordered"/><div data-card-selection-gutter><input id="theme-selection" type="checkbox" class="checkbox checkbox-sm"/></div><button class="diff-add-comment-btn">+</button><div class="review-inline-comment-bar"></div><div id="theme-notice" class="bg-base-200/60 text-base-content/70">Notice</div><div class="stats"><span class="stat-title">Label</span><span class="stat-value">12</span></div><div data-alert-markdown><p><code id="theme-alert-code">example</code></p></div><div data-search-empty-state class="rounded-box bg-base-200/60 p-8 text-center"><h3>No saved automations</h3><p class="mt-2 text-sm text-base-content/65">Create an automation.</p></div><div id="fixture-search">` + search.String() + `</div><div id="schedule-content">` + calendar.String() + `<div data-calendar-day="2026-10-16" class="schedule-day-selected">Fri</div><div data-calendar-hour="9" class="bg-base-200 schedule-time-label schedule-day-selected">9 AM</div><div id="fixture-schedule-card" class="bg-indigo-600 bg-opacity-30 border-l-2 border-indigo-500" data-schedule-card>Daily</div></div>
<ul id="fixture-menu" class="ov-menu ov-menu-actions dropdown-content menu"><li><button type="button">Action</button></li></ul>
<div id="fixture-settings-card" class="card bg-base-100 shadow-sm border border-base-300">Card<input id="fixture-input" class="input input-bordered"/></div><div id="fixture-stats" class="stats shadow w-full bg-base-100"><div class="stat">S</div></div><ul id="fixture-menu" class="ov-menu menu dropdown-content"><li>Item</li></ul><div id="fixture-bubble" class="chat-bubble-assistant-msg rounded-xl p-4">Bubble</div><form id="fixture-composer" class="chat-input-container rounded-xl p-4"></form><div id="fixture-picker">` + picker.String() + `</div><span id="fixture-deletions" style="color: var(--ov-diff-del-fg)">-4</span><span id="fixture-primary-text" class="text-primary">P</span><span id="fixture-running" class="task-state-running">Running</span><span id="fixture-primary-badge" class="badge badge-primary">New</span><div id="fixture-column" class="kanban-column bg-base-300"><div id="fixture-column-card" class="card bg-base-100">Card</div><div id="fixture-selected-card" class="card bg-base-100 border border-base-300 task-selected">Selected</div></div><div id="fixture-hover-card" data-search-card class="card bg-base-100 border border-base-300 hover:border-primary/40 cursor-pointer" style="position:fixed;left:300px;top:150px;width:120px;height:60px;z-index:99999">Card</div><input id="fixture-toggle-on" type="checkbox" class="toggle toggle-primary toggle-sm" checked/><input id="fixture-toggle-off" type="checkbox" class="toggle toggle-secondary toggle-sm"/><div id="probe"></div>`
	var taskCard bytes.Buffer
	if err := components.TaskCard(models.Task{ID: "theme-title", Title: "Theme task", Prompt: "Task description", Status: models.StatusPending}, "p1", "active", nil, nil).Render(context.Background(), &taskCard); err != nil {
		t.Fatal(err)
	}
	fixture += `<div id="theme-task-card">` + taskCard.String() + `</div><div class="chat-markdown"><a id="theme-content-link" href="/example">Content</a></div><a id="theme-result-link" class="ov-task-result-link" href="/example">Result</a><a id="theme-explicit-link" class="link link-primary" href="/example">Link</a>`
	fixture += `<div class="card"><p id="theme-card-description" class="text-sm opacity-60 mt-1 line-clamp-2">Automation description</p></div>`
	var themePicker bytes.Buffer
	if err := ThemesContent(themes.Catalog()).Render(context.Background(), &themePicker); err != nil {
		t.Fatal(err)
	}
	fixture += themePicker.String()
	var question bytes.Buffer
	if err := ChatInputRequestCard(&events.ChatInputRequestEvent{ID: "theme-question", Questions: []events.ChatInputRequestQuestion{{ID: "q", Question: "Choose", Options: []events.ChatInputRequestOption{{Label: "First", Description: "Description"}}}}}).Render(context.Background(), &question); err != nil {
		t.Fatal(err)
	}
	fixture += question.String()
	var navigation bytes.Buffer
	if err := components.BreadcrumbSelector(models.BreadcrumbSelector{ID: "theme-breadcrumb", Kind: "Task"}).Render(context.Background(), &navigation); err != nil {
		t.Fatal(err)
	}
	if err := components.TaskChangeActivity("theme-task").Render(context.Background(), &navigation); err != nil {
		t.Fatal(err)
	}
	fixture += navigation.String() + `<a id="theme-breadcrumb-title" data-breadcrumb-title href="#" style="position:fixed;left:220px;top:360px;width:120px;height:30px;z-index:999999">Tasks</a>`

	var review bytes.Buffer
	if err := components.DiffViewerWithReview("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n", "theme-review", nil).Render(context.Background(), &review); err != nil {
		t.Fatal(err)
	}
	fixture += `<div hidden>` + review.String() + `</div>`

	var graphStyles bytes.Buffer
	if err := automationGraphThemeStyles().Render(context.Background(), &graphStyles); err != nil {
		t.Fatal(err)
	}
	fixture += graphStyles.String() + `<svg id="theme-graph"><rect class="automation-graph-node"/><path class="automation-graph-edge"/><circle class="automation-edge-endpoint"/><text class="automation-graph-label">Task</text></svg>`

	var panel bytes.Buffer
	if err := TaskDetailPanel().Render(context.Background(), &panel); err != nil {
		t.Fatal(err)
	}
	fixture += panel.String()
	html := base.String()
	end := strings.LastIndex(html, "</body>")
	page := html[:end] + fixture + html[end:]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL, "imported-theme-chrome", func(browser *composerFocusCDP) {
		browser.waitFor("theme runtime", `String(!!(window.openVibelyThemes && window.openVibelyThemes.apply))`, "true")
		browser.waitFor("page body ready", `String(document.readyState !== 'loading' && !!document.getElementById('fixture-hover-card') && !!document.getElementById('desktop-project-titlebar'))`, "true")
		// Exercise full theme surfaces with assistant bubbles and filled board columns enabled.
		browser.evaluate(`document.documentElement.setAttribute('data-assistant-bubbles','on'); document.documentElement.setAttribute('data-board-columns','filled'); 'ok'`)
		// Palette colors must preserve native geometry and fading.
		browser.evaluate(`document.querySelectorAll('.theme-toggle-pill, .theme-toggle-pill span').forEach(function(el) { el.style.transition = 'none'; });
		window.toggleGeometry = function() {
			var pill = document.querySelector('.theme-toggle-pill');
			var origin = pill.getBoundingClientRect();
			return JSON.stringify([pill, pill.querySelector('.theme-toggle-thumb'), pill.querySelector('.theme-toggle-sun'), pill.querySelector('.theme-toggle-moon')].map(function(el) {
				var r = el.getBoundingClientRect(); var style = getComputedStyle(el); return [r.x-origin.x, r.y-origin.y, r.width, r.height, style.opacity];
			}));
		}; 'ok'`)
		for _, mode := range []string{"light", "dark"} {
			browser.evaluate(`window.openVibelyThemes.apply('openvibely-` + mode + `'); window.originalToggleGeometry = window.toggleGeometry(); 'ok'`)
			for _, theme := range themes.Catalog() {
				if theme.Mode != mode || theme.ID == "openvibely-layered" { // Legacy ID migrates to the native Dark theme.
					continue
				}
				browser.evaluate(`window.openVibelyThemes.apply('` + theme.ID + `'); 'ok'`)
				if mode == "dark" {
					browser.waitFor(theme.ID+" chat surfaces have no shadow", `['#fixture-bubble','#fixture-composer'].map(function(s){return getComputedStyle(document.querySelector(s)).boxShadow;}).join()`, "none,none")
				} else {
					browser.waitFor(theme.ID+" chat surfaces share card shadow", `(function(){var c=getComputedStyle(document.querySelector('#fixture-settings-card')).boxShadow;return String(c!=='none'&&['#fixture-bubble','#fixture-composer'].every(function(s){return getComputedStyle(document.querySelector(s)).boxShadow===c;}));})()`, "true")
				}
				browser.waitFor(theme.ID+" preserves default toggle geometry and fading", `window.toggleGeometry() === window.originalToggleGeometry ? 'true' : window.toggleGeometry() + ' expected ' + window.originalToggleGeometry`, "true")
				if theme.ID != "openvibely-light" && theme.ID != "openvibely-dark" {
					browser.waitFor(theme.ID+" consumes dedicated toggle colors", `JSON.stringify(['.theme-toggle-pill','.theme-toggle-thumb','.theme-toggle-'+(document.documentElement.dataset.theme === 'dark' ? 'moon' : 'sun'),'.theme-toggle-'+(document.documentElement.dataset.theme === 'dark' ? 'sun' : 'moon')].map(function(selector, i) { var probe = document.getElementById('probe'); probe.style.color = 'var(--ov-mode-'+['track','thumb','active','inactive'][i]+')'; return getComputedStyle(document.querySelector(selector))[i < 2 ? 'backgroundColor' : 'color'] === getComputedStyle(probe).color; }))`, "[true,true,true,true]")
				}
			}
		}
		browser.evaluate(`window.openVibelyThemes.apply('vscode-kimbie-dark');
			window.themeColor = function(value, property) { var probe = document.getElementById('probe'); probe.style[property || 'backgroundColor'] = value; return getComputedStyle(probe)[property || 'backgroundColor']; };
			window.computed = function(selector, property) { return getComputedStyle(document.querySelector(selector))[property]; };
			window.rgbOf = function(color) { var c = document.createElement('canvas'); c.width = c.height = 1; var x = c.getContext('2d'); x.fillStyle = '#000'; x.fillStyle = color; x.fillRect(0, 0, 1, 1); return Array.prototype.slice.call(x.getImageData(0, 0, 1, 1).data, 0, 3); };
			window.near = function(selector, property, value) { var got = window.rgbOf(window.computed(selector, property)); var want = window.rgbOf(window.themeColor(value, property)); return got.every(function(v, i) { return Math.abs(v - want[i]) <= 2; }) ? 'ok' : got + ' != ' + want; };
			window.matches = function(selector, property, value) { var got = window.computed(selector, property); var want = window.themeColor(value, property); return got === want ? 'ok' : got + ' != ' + want; };
			'ok'`)
		browser.waitFor("theme applied", `document.documentElement.getAttribute('data-color-theme')`, "vscode-kimbie-dark")
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-dark');'ok'`)
		browser.waitFor("default dark retains native styling", `document.documentElement.getAttribute('data-ov-theme-layer')`, "native")
		browser.waitFor("rail stays transparent", `window.computed('#sidebar', 'backgroundColor')`, "rgba(0, 0, 0, 0)")
		browser.waitFor("rail has no divider", `window.computed('#sidebar', 'borderRightWidth')`, "0px")
		for _, step := range []struct{ selector, color string }{{"body", "#191e24"}, {"#fixture-settings-card", "#191e24"}, {"#fixture-stats", "#191e24"}, {"#fixture-menu", "#1d232a"}, {"#fixture-bubble", "#191e24"}, {"#fixture-composer", "#191e24"}} {
			browser.waitFor("default dark "+step.selector+" surface", `window.near('`+step.selector+`', 'backgroundColor', '`+step.color+`')`, "ok")
		}
		for _, selector := range []string{"#fixture-bubble", "#fixture-composer"} {
			browser.waitFor("default dark "+selector+" uses card outline without shadow", `(function(){var a=getComputedStyle(document.querySelector('`+selector+`')),b=getComputedStyle(document.querySelector('#fixture-settings-card'));return String(a.borderTopColor===b.borderTopColor&&a.borderTopWidth===b.borderTopWidth&&a.boxShadow==='none');})()`, "true")
		}
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-dark');'ok'`)
		browser.waitFor("transparent rail restored", `window.computed('#sidebar', 'backgroundColor')`, "rgba(0, 0, 0, 0)")
		browser.evaluate(`window.openVibelyThemes.apply('vscode-kimbie-dark');'ok'`)

		browser.evaluate(`document.querySelector('[data-kanban-search]').dataset.open = 'true';'ok'`)
		for _, themeID := range []string{"vscode-dark-2026", "vscode-light-2026", "github-light", "catppuccin-mocha"} {
			browser.evaluate(`window.openVibelyThemes.apply('` + themeID + `');'ok'`)
			for _, property := range []string{"backgroundColor", "borderTopColor", "borderTopWidth", "borderRadius", "boxShadow"} {
				browser.waitFor(themeID+" search matches card search "+property, `String(window.computed('.kanban-search', '`+property+`') === getComputedStyle(document.querySelector('[data-card-search="themes"]').parentElement)['`+property+`'])`, "true")
			}
			browser.evaluate(`document.querySelector('.kanban-search-input').focus({preventScroll:true});'ok'`)
			browser.waitFor(themeID+" focused search retains card border", `String(window.computed('.kanban-search', 'borderTopColor') === getComputedStyle(document.querySelector('[data-card-search="themes"]').parentElement).borderTopColor)`, "true")
			browser.waitFor(themeID+" search placeholder matches", `String(getComputedStyle(document.querySelector('.kanban-search-input'), '::placeholder').color === getComputedStyle(document.querySelector('[data-card-search="themes"]'), '::placeholder').color)`, "true")
			browser.evaluate(`document.querySelector('.kanban-search-input').blur();'ok'`)
			browser.waitFor(themeID+" task title inherits normal text", `String(window.computed('#theme-task-card .task-card-title-link', 'color') === window.computed('#theme-task-card .card-body', 'color'))`, "true")
			for _, selector := range []string{"#theme-task-card [data-task-prompt]", "#theme-card-description"} {
				browser.waitFor(themeID+selector+" description uses palette", `window.matches('`+selector+`', 'color', 'var(--ov-text-muted)')`, "ok")
				browser.waitFor(themeID+selector+" description is not dimmed twice", `window.computed('`+selector+`', 'opacity')`, "1")
			}
			for _, selector := range []string{"#theme-content-link", "#theme-result-link", "#theme-explicit-link"} {
				browser.waitFor(themeID+selector+" keeps link accent", `window.matches('`+selector+`', 'color', 'var(--ov-link)')`, "ok")
			}
		}
		for _, mode := range []string{"dark", "light"} {
			browser.evaluate(`window.openVibelyThemes.apply('openvibely-` + mode + `');'ok'`)
			opacity := "0.6"
			if mode == "light" {
				opacity = "1"
			}
			for _, selector := range []string{"#theme-task-card [data-task-prompt]", "#theme-card-description"} {
				browser.waitFor(mode+selector+" preserves native opacity", `window.computed('`+selector+`', 'opacity')`, opacity)
				if mode == "light" {
					browser.waitFor(mode+selector+" preserves native color", `window.matches('`+selector+`', 'color', 'var(--ov-l-text-muted)')`, "ok")
				}
			}
		}
		browser.evaluate(`window.openVibelyThemes.apply('vscode-kimbie-dark');'ok'`)
		for _, id := range []string{"openvibely-dark", "openvibely-light", "vscode-dark-2026", "vscode-light-2026", "github-light", "catppuccin-mocha"} {
			browser.evaluate(`window.selectOpenVibelyTheme('` + id + `');
                var selected = document.querySelector('[data-theme-card][aria-pressed="true"]');
                var other = document.querySelector('[data-theme-card][aria-pressed="false"]');
                selected.style.cssText = 'position:fixed;left:220px;top:150px;width:150px;height:100px;z-index:2147483647';
                other.style.cssText = 'position:fixed;left:400px;top:150px;width:150px;height:100px;z-index:2147483647';
                other.id = 'theme-hover-probe'; 'ok'`)
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 430, "y": 180}, nil)
			browser.waitFor(id+" unselected card is hovered", `String(document.querySelector('#theme-hover-probe').matches(':hover'))`, "true")
			browser.waitFor(id+" selected picker border", `window.matches('[data-theme-card][aria-pressed="true"]', 'borderTopColor', 'var(--ov-link, var(--ov-link-color))')`, "ok")
			browser.waitFor(id+" subdued hover border", `window.matches('#theme-hover-probe', 'borderTopColor', 'oklch(var(--p) / 0.4)')`, "ok")
			browser.waitFor(id+" selection differs from hover", `String(window.computed('#theme-hover-probe', 'borderTopColor') !== window.computed('[data-theme-card][aria-pressed="true"]', 'borderTopColor'))`, "true")
			browser.waitFor(id+" exactly one selected card", `String(document.querySelectorAll('[data-theme-card][aria-pressed="true"]').length)`, "1")
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 250, "y": 180}, nil)
			browser.waitFor(id+" selected border survives hover", `window.matches('[data-theme-card][aria-pressed="true"]', 'borderTopColor', 'var(--ov-link, var(--ov-link-color))')`, "ok")
			browser.evaluate(`document.querySelectorAll('[data-theme-card]').forEach(function(card) { card.removeAttribute('style'); card.removeAttribute('id'); }); 'ok'`)
		}
		browser.evaluate(`window.openVibelyThemes.apply('vscode-kimbie-dark');'ok'`)
		checks := []struct{ label, selector, property, value string }{
			{"title bar uses recessed column color", "#desktop-project-titlebar", "backgroundColor", "var(--ov-kanban-column-bg)"},
			{"selected project tab matches content", ".desktop-project-tab[data-selected=\"true\"]", "backgroundColor", "var(--ov-content-bg)"},
			{"selected project tab text", ".desktop-project-tab [aria-selected=\"true\"]", "color", "var(--ov-text)"},
			{"unselected project tab text", ".desktop-project-tab:not([data-selected=\"true\"]) [role=\"tab\"]", "color", "var(--ov-text-muted)"},
			{"task column matches title bar", "#fixture-column", "backgroundColor", "var(--ov-kanban-column-bg)"},
			{"task card matches other card pages", "#fixture-column-card", "backgroundColor", "var(--ov-surface)"},
			{"selected task card border uses text color", "#fixture-selected-card", "borderTopColor", "var(--ov-text)"},
			{"popup menu surface", "#fixture-menu", "backgroundColor", "var(--ov-surface-raised)"},
			{"popup menu border", "#fixture-menu", "borderTopColor", "var(--ov-subtle-border)"},
			{"popup menu text", "#fixture-menu", "color", "var(--ov-text)"},
			{"project tab menu surface", "#project-tab-menu", "backgroundColor", "var(--ov-surface-raised)"},
			{"selected schedule day", "#schedule-content [data-calendar-day]", "backgroundColor", "var(--ov-schedule-selection-bg)"},
			{"selected schedule hour", "#schedule-content [data-calendar-hour]", "backgroundColor", "var(--ov-schedule-selection-bg)"},
			{"schedule card tint", "#fixture-schedule-card", "backgroundColor", "var(--ov-schedule-task-bg)"},
			{"schedule card border", "#fixture-schedule-card", "borderLeftColor", "var(--ov-schedule-task-border)"},
			{"date picker surface", ".schedule-datetime-popup", "backgroundColor", "var(--ov-surface-raised)"},
			{"date picker border", ".schedule-datetime-popup", "borderTopColor", "var(--ov-subtle-border)"},
			{"date picker text", ".schedule-datetime-popup", "color", "var(--ov-text)"},
			{"diff deletion count", "#fixture-deletions", "color", "var(--ov-diff-delete-fg)"},
		}
		for _, check := range checks {
			browser.waitFor(check.label, `window.matches('`+strings.ReplaceAll(check.selector, `'`, `\'`)+`', '`+check.property+`', '`+check.value+`')`, "ok")
		}

		browser.waitFor("primary text uses theme accent", `window.near('#fixture-primary-text', 'color', 'var(--ov-link)')`, "ok")
		browser.waitFor("running task state uses theme accent", `window.near('#fixture-running', 'color', 'var(--ov-link)')`, "ok")
		browser.waitFor("primary badge uses theme accent", `window.near('#fixture-primary-badge', 'backgroundColor', 'var(--ov-link)')`, "ok")

		browser.evaluate(`document.querySelector('[data-kanban-search]').dataset.open = 'true';'ok'`)
		browser.waitFor("task search box surface", `window.matches('.kanban-search', 'backgroundColor', 'var(--ov-surface)')`, "ok")
		browser.waitFor("task search box border", `window.matches('.kanban-search', 'borderTopColor', 'var(--ov-subtle-border)')`, "ok")
		browser.waitFor("task search input text", `window.matches('.kanban-search-input', 'color', 'var(--ov-text)')`, "ok")
		browser.waitFor("task search clear icon", `window.matches('.kanban-search-clear', 'color', 'var(--ov-text-muted)')`, "ok")

		browser.evaluate(`window.openScheduleDateTime(document.querySelector('[data-run-at-picker]'));'ok'`)
		browser.waitFor("selected date uses theme button", `window.matches('.schedule-datetime-days [aria-pressed="true"]', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")
		browser.waitFor("selected date text uses theme button text", `window.matches('.schedule-datetime-days [aria-pressed="true"]', 'color', 'var(--ov-button-fg)')`, "ok")
		browser.waitFor("selected time uses theme button", `window.matches('[data-time-column="hour"] [data-time-selected="true"]', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")

		for _, palette := range []struct{ id, sidebar, accent string }{{"catppuccin-mocha", "rgb(17, 17, 27)", "#cba6f7"}, {"github-light", "rgb(240, 240, 240)", "#0969da"}} {
			browser.evaluate(`window.openVibelyThemes.apply('` + palette.id + `');'ok'`)
			browser.waitFor(palette.id+" enables custom theme layer", `document.documentElement.getAttribute('data-ov-theme-layer')`, "custom")
			browser.waitFor(palette.id+" title bar", `window.computed('#desktop-project-titlebar', 'backgroundColor')`, palette.sidebar)
			browser.waitFor(palette.id+" task column matches title bar", `window.matches('#fixture-column', 'backgroundColor', 'var(--ov-kanban-column-bg)')`, "ok")
			browser.waitFor(palette.id+" popup menu surface", `window.matches('#fixture-menu', 'backgroundColor', 'var(--ov-surface-raised)')`, "ok")
			browser.waitFor(palette.id+" checked toggle uses button color", `window.matches('#fixture-toggle-on', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")
			browser.waitFor(palette.id+" unchecked toggle uses strong border color", `window.matches('#fixture-toggle-off', 'backgroundColor', 'var(--ov-border-strong)')`, "ok")
			for _, state := range []struct{ id, color string }{{"on", "--ov-button-bg"}, {"off", "--ov-border-strong"}} {
				browser.evaluate(`var toggle = document.getElementById('fixture-toggle-` + state.id + `'); toggle.style.cssText = 'position:fixed;left:300px;top:250px;z-index:99999';'ok'`)
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 305, "y": 255}, nil)
				browser.waitFor(palette.id+state.id+" toggle hovered", `String(document.getElementById('fixture-toggle-`+state.id+`').matches(':hover'))`, "true")
				for _, property := range []string{"backgroundColor", "borderTopColor"} {
					browser.waitFor(palette.id+state.id+" stable toggle "+property, `window.matches('#fixture-toggle-`+state.id+`', '`+property+`', 'var(`+state.color+`)')`, "ok")
				}
				browser.evaluate(`document.getElementById('fixture-toggle-` + state.id + `').style.cssText = ''; 'ok'`)
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 290, "y": 240}, nil)
			}
			browser.waitFor(palette.id+" spreadsheet focus follows palette", `String(window.themeColor(getComputedStyle(document.getElementById('image-gallery')).getPropertyValue('--studio-focus')) === window.themeColor('var(--ov-focus)'))`, "true")
			browser.waitFor(palette.id+" file viewer page uses theme surface", `String(getComputedStyle(document.getElementById('image-gallery')).getPropertyValue('--studio-side').trim() === 'var(--ov-surface)' || window.themeColor(getComputedStyle(document.getElementById('image-gallery')).getPropertyValue('--studio-side')) === window.themeColor('var(--ov-surface)'))`, "true")
			browser.waitFor(palette.id+" file viewer shell stays transparent", `window.computed('#image-gallery .image-gallery-shell', 'backgroundColor')`, "rgba(0, 0, 0, 0)")
			browser.waitFor(palette.id+" running state uses palette accent", `window.near('#fixture-running', 'color', '`+palette.accent+`')`, "ok")
		}

		browser.evaluate(`window.addCodeCopyButtons(document.getElementById('theme-copy-host'));var copy=document.querySelector('#theme-copy-host .code-copy-btn');copy.id='theme-copy';copy.style.cssText='left:0;top:0;width:100px;height:30px';Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.themeCopiedText=text}}});'ok'`)
		for _, themeID := range []string{"rose-pine-dawn", "github-light", "tokyo-night", "vscode-light-modern"} {
			browser.evaluate(`window.openVibelyThemes.apply('` + themeID + `'); document.querySelector('#inspector-tab-details').setAttribute('aria-selected', 'true'); 'ok'`)
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 0, "y": 0}, nil)
			// Touch/coarse-pointer devices keep copy controls visible.
			browser.waitFor(themeID+" copy visibility without hover", `String(window.computed('#theme-copy', 'opacity') === (matchMedia('(hover: hover) and (pointer: fine)').matches ? '0' : '1'))`, "true")
			browser.waitFor(themeID+" copy normal", `window.matches('#theme-copy', 'color', 'var(--ov-text)')`, "ok")
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 240, "y": 415}, nil)
			browser.waitFor(themeID+" copy hover background", `window.matches('#theme-copy', 'backgroundColor', 'var(--ov-hover-bg)')`, "ok")
			browser.waitFor(themeID+" copy hover text", `window.matches('#theme-copy', 'color', 'var(--ov-text)')`, "ok")
			browser.waitFor(themeID+" copy visible on hover", `window.computed('#theme-copy', 'opacity')`, "1")
			browser.evaluate(`window.themeCopiedText=null;document.getElementById('theme-copy').click();'ok'`)
			browser.waitFor(themeID+" copied content and feedback", `String(window.themeCopiedText==='example' && document.querySelector('#theme-copy span').textContent==='Copied')`, "true")
			browser.evaluate(`document.getElementById('theme-copy').blur();'ok'`)
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 240, "y": 365}, nil)
			browser.waitFor(themeID+" pinned header hover", `window.matches('#theme-pinned-header', 'backgroundColor', 'var(--ov-content-bg)')`, "ok")
			browser.waitFor(themeID+" upload background", `window.matches('#theme-file', 'backgroundColor', 'var(--ov-input-bg)')`, "ok")
			browser.waitFor(themeID+" upload text", `window.matches('#theme-file', 'color', 'var(--ov-text)')`, "ok")
			browser.waitFor(themeID+" upload button", `String(getComputedStyle(document.getElementById('theme-file'), '::file-selector-button').backgroundColor === window.themeColor('var(--ov-button-bg)'))`, "true")
			browser.waitFor(themeID+" upload button text", `String(getComputedStyle(document.getElementById('theme-file'), '::file-selector-button').color === window.themeColor('var(--ov-button-fg)'))`, "true")
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 0, "y": 0}, nil)
			browser.waitFor(themeID+" breadcrumb text", `window.matches('#theme-breadcrumb-title', 'color', 'var(--ov-text-muted)')`, "ok")
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 240, "y": 375}, nil)
			browser.waitFor(themeID+" breadcrumb hover", `window.matches('#theme-breadcrumb-title', 'color', 'var(--ov-text)')`, "ok")
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 0, "y": 0}, nil)
			for _, attribute := range []string{"data-pointer-over", "aria-expanded"} {
				browser.evaluate(`document.getElementById('theme-breadcrumb-button').setAttribute('` + attribute + `', 'true'); 'ok'`)
				browser.waitFor(themeID+" selector "+attribute, `window.matches('#theme-breadcrumb-button', 'backgroundColor', 'var(--ov-hover-bg)')`, "ok")
				browser.evaluate(`document.getElementById('theme-breadcrumb-button').removeAttribute('` + attribute + `'); 'ok'`)
			}
			browser.waitFor(themeID+" review summary icon", `window.matches('.task-change-icon', 'color', 'var(--ov-text-muted)')`, "ok")
			for _, check := range []struct{ selector, property, role string }{
				{".automation-graph-node", "fill", "--ov-surface-raised"},
				{".automation-graph-node", "stroke", "--ov-automation-node-border"},
				{".automation-graph-edge", "stroke", "--ov-automation-edge"},
				{".automation-edge-endpoint", "stroke", "--ov-surface"},
				{".automation-graph-label", "fill", "--ov-text"},
			} {
				browser.waitFor(themeID+check.selector+check.property, `window.matches('#theme-graph `+check.selector+`', '`+check.property+`', 'var(`+check.role+`)')`, "ok")
			}
			browser.waitFor(themeID+" notice surface", `window.matches('#theme-notice', 'backgroundColor', 'var(--ov-surface)')`, "ok")
			browser.waitFor(themeID+" notice text", `window.matches('#theme-notice', 'color', 'var(--ov-text-muted)')`, "ok")
			browser.waitFor(themeID+" stat label", `window.matches('.stat-title', 'color', 'var(--ov-text-muted)')`, "ok")
			browser.waitFor(themeID+" stat value", `window.matches('.stat-value', 'color', 'var(--ov-text)')`, "ok")
			browser.waitFor(themeID+" alert code background", `window.matches('#theme-alert-code', 'backgroundColor', 'var(--ov-code-bg)')`, "ok")
			browser.waitFor(themeID+" alert code text", `window.matches('#theme-alert-code', 'color', 'var(--ov-code-fg)')`, "ok")
			browser.waitFor(themeID+" review button", `window.matches('.diff-add-comment-btn', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")
			browser.waitFor(themeID+" review button text", `window.matches('.diff-add-comment-btn', 'color', 'var(--ov-button-fg)')`, "ok")
			browser.evaluate(`document.querySelector('.diff-add-comment-btn').style.cssText='position:fixed;left:220px;right:auto;top:300px;transform:none;opacity:1;pointer-events:auto;z-index:999999'; 'ok'`)
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 228, "y": 308}, nil)
			browser.waitFor(themeID+" review hover", `window.matches('.diff-add-comment-btn', 'backgroundColor', 'var(--ov-button-hover-bg)')`, "ok")
			browser.waitFor(themeID+" review hover text", `window.matches('.diff-add-comment-btn', 'color', 'var(--ov-button-hover-fg)')`, "ok")
			browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 0, "y": 0}, nil)
			browser.waitFor(themeID+" comment marker", `window.matches('.review-inline-comment-bar', 'backgroundColor', 'var(--ov-warning)')`, "ok")
			browser.evaluate(`document.getElementById('theme-selection').checked=false; document.getElementById('theme-selection').indeterminate=false; 'ok'`)
			browser.waitFor(themeID+" unchecked selection", `window.matches('#theme-selection', 'backgroundColor', 'var(--ov-input-bg)')`, "ok")
			browser.evaluate(`document.getElementById('theme-selection').checked=true; 'ok'`)
			browser.waitFor(themeID+" checked selection", `window.matches('#theme-selection', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")
			browser.waitFor(themeID+" checkmark color", `String(getComputedStyle(document.getElementById('theme-selection')).backgroundImage.includes(window.themeColor('var(--ov-button-fg)')))`, "true")
			browser.evaluate(`document.getElementById('theme-selection').indeterminate=true; 'ok'`)
			browser.waitFor(themeID+" mixed selection", `window.matches('#theme-selection', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")
			browser.waitFor(themeID+" empty state surface", `window.matches('[data-search-empty-state]', 'backgroundColor', 'var(--ov-surface)')`, "ok")
			browser.waitFor(themeID+" empty state message", `window.matches('[data-search-empty-state] p', 'color', 'var(--ov-text-muted)')`, "ok")

			browser.waitFor(themeID+" task panel surface", `window.matches('#task-details-panel', 'backgroundColor', 'var(--shell-page)')`, "ok")
			browser.waitFor(themeID+" task panel text", `window.matches('#task-details-panel', 'color', 'var(--ov-text)')`, "ok")
			browser.waitFor(themeID+" selected panel tab", `window.matches('#inspector-tab-details', 'color', 'var(--ov-text)')`, "ok")
			browser.waitFor(themeID+" inactive panel tab", `window.matches('#inspector-tab-schedules', 'color', 'var(--ov-text-muted)')`, "ok")
		}

		for _, themeID := range []string{"tokyo-night", "github-light", "vscode-kimbie-dark"} {
			browser.evaluate(`window.openVibelyThemes.apply('` + themeID + `');'ok'`)
			browser.evaluate(`document.querySelector('.chat-input-recommended-shortcut').style.cssText = 'position:fixed;left:500px;top:150px;z-index:99999';'ok'`)
			browser.waitFor(themeID+" question text", `window.matches('[data-chat-input-option]', 'color', 'var(--ov-text)')`, "ok")
			browser.evaluate(`document.querySelector('[data-chat-input-option]').classList.add('chat-input-option-selected');'ok'`)
			browser.waitFor(themeID+" selected question border", `window.matches('[data-chat-input-option]', 'borderTopColor', 'var(--ov-focus)')`, "ok")
			for _, x := range []int{490, 550} {
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": 165}, nil)
				browser.waitFor(themeID+" recommended fill", `window.matches('.chat-input-recommended-shortcut', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")
				browser.waitFor(themeID+" recommended text", `window.matches('.chat-input-recommended-shortcut', 'color', 'var(--ov-button-fg)')`, "ok")
			}
			browser.waitFor(themeID+" detached tab has no connector", `getComputedStyle(document.querySelector('.desktop-project-tab [aria-selected="true"]'), '::before').display`, "none")
			browser.waitFor(themeID+" titlebar has no baseline", `window.computed('#desktop-project-titlebar', 'boxShadow')`, "none")
			for _, cardKind := range []string{"search", "task"} {
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 290, "y": 140}, nil)
				browser.evaluate(`var card = document.getElementById('fixture-hover-card'); card.classList.toggle('cursor-pointer', '` + cardKind + `' === 'search'); card.toggleAttribute('data-search-card', '` + cardKind + `' === 'search');'ok'`)
				browser.waitFor(themeID+cardKind+" idle border", `window.matches('#fixture-hover-card', 'borderTopColor', 'var(--ov-subtle-border)')`, "ok")
				browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 350, "y": 175}, nil)
				browser.waitFor(themeID+cardKind+" hover border", `window.matches('#fixture-hover-card', 'borderTopColor', 'var(--ov-link)')`, "ok")
				browser.waitFor(themeID+cardKind+" stable card fill", `window.matches('#fixture-hover-card', 'backgroundColor', 'var(--ov-surface)')`, "ok")
			}
		}
		browser.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": 290, "y": 140}, nil)
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-dark');'ok'`)
		browser.evaluate(`var sample=document.createElement('div'); sample.innerHTML='<pre><code class="hljs"><span id="native-keyword" class="hljs-keyword">const</span><span id="native-string" class="hljs-string">text</span></code></pre>'; document.body.appendChild(sample); 'ok'`)
		for _, mode := range []string{"dark", "light"} {
			browser.evaluate(`window.openVibelyThemes.apply('openvibely-` + mode + `'); 'ok'`)
			browser.waitFor(mode+" native syntax keyword matches main GitHub Dark stylesheet", `window.computed('#native-keyword', 'color')`, "rgb(255, 123, 114)")
			browser.waitFor(mode+" native syntax string matches main GitHub Dark stylesheet", `window.computed('#native-string', 'color')`, "rgb(165, 214, 255)")
			browser.waitFor(mode+" native palette leaves CSS variables to main stylesheet", `String(document.documentElement.style.getPropertyValue('--ov-diff-add-bg') === '' && document.documentElement.style.getPropertyValue('--ov-syntax-foreground') === '')`, "true")
			browser.evaluate(`window.openVibelyThemes.apply('github-light'); window.openVibelyThemes.apply('openvibely-` + mode + `'); 'ok'`)
			browser.waitFor(mode+" returning from custom restores syntax", `window.computed('#native-keyword', 'color')`, "rgb(255, 123, 114)")
		}
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-dark'); 'ok'`)
		browser.waitFor("native selected tab retains thick contour", `getComputedStyle(document.querySelector('.desktop-project-tab [aria-selected="true"]'), '::before').borderTopWidth`, "2px")
		browser.waitFor("native theme restores native layer", `document.documentElement.getAttribute('data-ov-theme-layer')`, "native")
		browser.waitFor("native theme keeps indigo running state", `window.near('#fixture-running', 'color', '#7480ff')`, "ok")
		browser.waitFor("native theme keeps indigo primary text", `window.near('#fixture-primary-text', 'color', '#7480ff')`, "ok")
		browser.waitFor("native theme keeps default selection tint", `window.computed('#schedule-content [data-calendar-day]', 'backgroundColor')`, "rgba(116, 128, 255, 0.16)")
		browser.waitFor("native theme keeps default task card", `String(document.documentElement.style.getPropertyValue('--ov-kanban-column-bg') === '' && window.matches('#fixture-column-card', 'backgroundColor', 'oklch(var(--b1))') === 'ok')`, "true")
		browser.waitFor("native theme keeps indigo schedule card", `String(window.computed('#fixture-schedule-card', 'backgroundColor').indexOf('var(') === -1 && document.documentElement.style.getPropertyValue('--ov-schedule-task-bg') === '')`, "true")
		browser.waitFor("native dark task column matches title bar", `String(window.computed('#fixture-column', 'backgroundColor') === window.computed('#desktop-project-titlebar', 'backgroundColor'))`, "true")
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-light');'ok'`)
		browser.waitFor("native light task column matches title bar", `String(window.computed('#fixture-column', 'backgroundColor') === window.computed('#desktop-project-titlebar', 'backgroundColor'))`, "true")
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-dark');'ok'`)
		browser.waitFor("native theme menu retains theme surface", `window.computed('#fixture-menu', 'backgroundColor')`, "rgb(29, 35, 42)")
		browser.waitFor("native theme keeps default deletion count", `window.computed('#fixture-deletions', 'color')`, "rgb(189, 112, 118)")
		for _, property := range []string{"backgroundColor", "borderTopColor"} {
			browser.waitFor("native dark search matches card search "+property, `String(window.computed('.kanban-search', '`+property+`') === getComputedStyle(document.querySelector('[data-card-search="themes"]').parentElement)['`+property+`'])`, "true")
		}
		browser.waitFor("native dark search uses card color", `window.near('.kanban-search', 'backgroundColor', '#191e24')`, "ok")
	})
}
