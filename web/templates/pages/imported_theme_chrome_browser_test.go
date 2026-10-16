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
	fixture := `<div data-card-selection-gutter><input id="theme-selection" type="checkbox" class="checkbox checkbox-sm"/></div><button class="diff-add-comment-btn">+</button><div class="review-inline-comment-bar"></div><div id="theme-notice" class="bg-base-200/60 text-base-content/70">Notice</div><div class="stats"><span class="stat-title">Label</span><span class="stat-value">12</span></div><div data-alert-markdown><p><code id="theme-alert-code">example</code></p></div><div data-search-empty-state class="rounded-box bg-base-200/60 p-8 text-center"><h3>No saved automations</h3><p class="mt-2 text-sm text-base-content/65">Create an automation.</p></div><div id="fixture-search">` + search.String() + `</div><div id="schedule-content">` + calendar.String() + `<div data-calendar-day="2026-10-16" class="schedule-day-selected">Fri</div><div data-calendar-hour="9" class="bg-base-200 schedule-time-label schedule-day-selected">9 AM</div></div>
<ul id="fixture-menu" class="ov-menu ov-menu-actions dropdown-content menu"><li><button type="button">Action</button></li></ul>
<div id="fixture-picker">` + picker.String() + `</div><span id="fixture-deletions" style="color: var(--ov-diff-del-fg)">-4</span><span id="fixture-primary-text" class="text-primary">P</span><span id="fixture-running" class="task-state-running">Running</span><span id="fixture-primary-badge" class="badge badge-primary">New</span><div id="fixture-column" class="kanban-column bg-base-300"></div><div id="fixture-hover-card" data-search-card class="card bg-base-100 border border-base-300 hover:border-primary/40 cursor-pointer" style="position:fixed;left:300px;top:150px;width:120px;height:60px;z-index:99999">Card</div><input id="fixture-toggle-on" type="checkbox" class="toggle toggle-primary toggle-sm" checked/><input id="fixture-toggle-off" type="checkbox" class="toggle toggle-secondary toggle-sm"/><div id="probe"></div>`
	var question bytes.Buffer
	if err := ChatInputRequestCard(&events.ChatInputRequestEvent{ID: "theme-question", Questions: []events.ChatInputRequestQuestion{{ID: "q", Question: "Choose", Options: []events.ChatInputRequestOption{{Label: "First", Description: "Description"}}}}}).Render(context.Background(), &question); err != nil {
		t.Fatal(err)
	}
	fixture += question.String()
	var review bytes.Buffer
	if err := components.DiffViewerWithReview("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n", "theme-review", nil).Render(context.Background(), &review); err != nil {
		t.Fatal(err)
	}
	fixture += `<div hidden>` + review.String() + `</div>`

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
		browser.evaluate(`window.openVibelyThemes.apply('vscode-kimbie-dark');
			window.themeColor = function(value, property) { var probe = document.getElementById('probe'); probe.style[property || 'backgroundColor'] = value; return getComputedStyle(probe)[property || 'backgroundColor']; };
			window.computed = function(selector, property) { return getComputedStyle(document.querySelector(selector))[property]; };
			window.rgbOf = function(color) { var c = document.createElement('canvas'); c.width = c.height = 1; var x = c.getContext('2d'); x.fillStyle = '#000'; x.fillStyle = color; x.fillRect(0, 0, 1, 1); return Array.prototype.slice.call(x.getImageData(0, 0, 1, 1).data, 0, 3); };
			window.near = function(selector, property, value) { var got = window.rgbOf(window.computed(selector, property)); var want = window.rgbOf(window.themeColor(value, property)); return got.every(function(v, i) { return Math.abs(v - want[i]) <= 2; }) ? 'ok' : got + ' != ' + want; };
			window.matches = function(selector, property, value) { var got = window.computed(selector, property); var want = window.themeColor(value, property); return got === want ? 'ok' : got + ' != ' + want; };
			'ok'`)
		browser.waitFor("theme applied", `document.documentElement.getAttribute('data-color-theme')`, "vscode-kimbie-dark")

		checks := []struct{ label, selector, property, value string }{
			{"title bar uses sidebar color", "#desktop-project-titlebar", "backgroundColor", "var(--ov-sidebar-bg)"},
			{"selected project tab matches content", ".desktop-project-tab[data-selected=\"true\"]", "backgroundColor", "var(--ov-content-bg)"},
			{"selected project tab text", ".desktop-project-tab [aria-selected=\"true\"]", "color", "var(--ov-text)"},
			{"unselected project tab text", ".desktop-project-tab:not([data-selected=\"true\"]) [role=\"tab\"]", "color", "var(--ov-text-muted)"},
			{"task column matches title bar", "#fixture-column", "backgroundColor", "var(--ov-sidebar-bg)"},
			{"popup menu surface", "#fixture-menu", "backgroundColor", "var(--ov-surface-raised)"},
			{"popup menu border", "#fixture-menu", "borderTopColor", "var(--ov-subtle-border)"},
			{"popup menu text", "#fixture-menu", "color", "var(--ov-text)"},
			{"project tab menu surface", "#project-tab-menu", "backgroundColor", "var(--ov-surface-raised)"},
			{"selected schedule day", "#schedule-content [data-calendar-day]", "backgroundColor", "var(--ov-accent-tint)"},
			{"selected schedule hour", "#schedule-content [data-calendar-hour]", "backgroundColor", "var(--ov-accent-tint)"},
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
		browser.waitFor("task search box surface", `window.matches('.kanban-search', 'backgroundColor', 'var(--ov-input-bg)')`, "ok")
		browser.waitFor("task search box border", `window.matches('.kanban-search', 'borderTopColor', 'var(--ov-input-border)')`, "ok")
		browser.waitFor("task search input text", `window.matches('.kanban-search-input', 'color', 'var(--ov-text)')`, "ok")
		browser.waitFor("task search clear icon", `window.matches('.kanban-search-clear', 'color', 'var(--ov-text-muted)')`, "ok")

		browser.evaluate(`window.openScheduleDateTime(document.querySelector('[data-run-at-picker]'));'ok'`)
		browser.waitFor("selected date uses theme button", `window.matches('.schedule-datetime-days [aria-pressed="true"]', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")
		browser.waitFor("selected date text uses theme button text", `window.matches('.schedule-datetime-days [aria-pressed="true"]', 'color', 'var(--ov-button-fg)')`, "ok")
		browser.waitFor("selected time uses theme button", `window.matches('[data-time-column="hour"] [data-time-selected="true"]', 'backgroundColor', 'var(--ov-button-bg)')`, "ok")

		for _, palette := range []struct{ id, sidebar, accent string }{{"catppuccin-mocha", "rgb(30, 30, 46)", "#cba6f7"}, {"github-light", "rgb(255, 255, 255)", "#0969da"}} {
			browser.evaluate(`window.openVibelyThemes.apply('` + palette.id + `');'ok'`)
			browser.waitFor(palette.id+" enables custom theme layer", `document.documentElement.getAttribute('data-ov-theme-layer')`, "custom")
			browser.waitFor(palette.id+" title bar", `window.computed('#desktop-project-titlebar', 'backgroundColor')`, palette.sidebar)
			browser.waitFor(palette.id+" task column matches title bar", `window.matches('#fixture-column', 'backgroundColor', 'var(--ov-sidebar-bg)')`, "ok")
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

		for _, themeID := range []string{"rose-pine-dawn", "github-light", "tokyo-night", "vscode-light-modern"} {
			browser.evaluate(`window.openVibelyThemes.apply('` + themeID + `'); document.querySelector('#inspector-tab-details').setAttribute('aria-selected', 'true'); 'ok'`)
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

			browser.waitFor(themeID+" task panel surface", `window.matches('#task-details-panel', 'backgroundColor', 'var(--ov-surface)')`, "ok")
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
			browser.waitFor(themeID+" thin selected tab", `getComputedStyle(document.querySelector('.desktop-project-tab [aria-selected="true"]'), '::before').borderTopWidth`, "1px")
			browser.waitFor(themeID+" thin tab baseline", `String(window.computed('#desktop-project-titlebar', 'boxShadow').includes('0px -1px'))`, "true")
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
		browser.waitFor("native selected tab retains thick contour", `getComputedStyle(document.querySelector('.desktop-project-tab [aria-selected="true"]'), '::before').borderTopWidth`, "2px")
		browser.waitFor("native theme restores native layer", `document.documentElement.getAttribute('data-ov-theme-layer')`, "native")
		browser.waitFor("native theme keeps indigo running state", `window.near('#fixture-running', 'color', '#7480ff')`, "ok")
		browser.waitFor("native theme keeps indigo primary text", `window.near('#fixture-primary-text', 'color', '#7480ff')`, "ok")
		browser.waitFor("native theme keeps default selection tint", `window.computed('#schedule-content [data-calendar-day]', 'backgroundColor')`, "rgba(116, 128, 255, 0.16)")
		browser.waitFor("native dark task column matches title bar", `String(window.computed('#fixture-column', 'backgroundColor') === window.computed('#desktop-project-titlebar', 'backgroundColor'))`, "true")
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-light');'ok'`)
		browser.waitFor("native light task column matches title bar", `String(window.computed('#fixture-column', 'backgroundColor') === window.computed('#desktop-project-titlebar', 'backgroundColor'))`, "true")
		browser.evaluate(`window.openVibelyThemes.apply('openvibely-dark');'ok'`)
		browser.waitFor("native theme keeps default menu surface", `window.computed('#fixture-menu', 'backgroundColor')`, "rgb(29, 35, 42)")
		browser.waitFor("native theme keeps default deletion count", `window.computed('#fixture-deletions', 'color')`, "rgb(189, 112, 118)")
		browser.waitFor("native theme keeps default search surface", `String(window.computed('.kanban-search', 'backgroundColor') === window.themeColor('oklch(var(--b1))'))`, "true")
	})
}
