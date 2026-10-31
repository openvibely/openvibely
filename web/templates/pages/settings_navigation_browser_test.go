package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_SettingsNavigation(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		var content templ.Component
		switch r.URL.Path {
		case "/themes":
			content = ThemesContent(nil)
		case "/models", "/agents", "/skills", "/channels", "/personality":
			content = templ.Raw(`<div id="` + r.URL.Path[1:] + `-container">Configuration</div>`)
		case "/workers", "/settings/workers":
			content = WorkerSettingsContent(5, 1, 1, 0, nil, nil)
		case "/tasks":
			content = templ.Raw(`<div id="settings-workspace"><a id="open-settings" href="/themes" hx-get="/themes" hx-target="#main-content" hx-push-url="true">Settings</a><div id="kanban-board" style="display:block;height:100px;overflow:auto"><div style="height:300px">
<div class="category-drop-zone" data-category="backlog" style="height:60px;overflow:auto"><div style="height:600px">Backlog</div></div>
<div class="task-drop-zone category-drop-zone" data-category="active" data-status="running" style="height:60px;overflow:auto"><div style="height:600px">Running</div></div>
<div class="task-drop-zone category-drop-zone" data-category="active" data-status="pending" style="height:60px;overflow:auto"><div style="height:600px">Queued</div></div>
</div></div></div>`)
		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			if err := content.Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if err := layout.Base("Settings", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(templ.WithChildren(r.Context(), content), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks", "settings-navigation", func(b *composerFocusCDP) {
		// A swapped fragment can be visible before HTMX processes its links. Wait
		// for settlement before clicking again, otherwise a native navigation can
		// discard the workspace return state.
		waitFor := func(label, expression, want string) {
			b.waitFor(label, `(document.readyState === 'complete' && document.documentElement && document.getElementById('main-content') && !document.querySelector('#main-content.htmx-settling, #main-content.htmx-swapping')) ? (`+expression+`) : 'loading'`, want)
		}
		waitFor("workspace ready", `String(document.readyState === 'complete' && !!window.htmx && !!window.syncAppearanceSettings)`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();document.querySelector('[data-category=backlog]').scrollTop=120;document.querySelector('[data-status=running]').scrollTop=180;document.querySelector('[data-status=pending]').scrollTop=240;document.getElementById('kanban-board').scrollTop=80;document.getElementById('open-settings').click();'opened'`)
		waitFor("themes only", `String(document.documentElement.classList.contains('appearance-settings') && !document.documentElement.classList.contains('task-list-collapsed') && !document.querySelector('[data-settings-section="themes"]').hidden && document.querySelector('[data-settings-section="layout"]').hidden)`, "true")
		waitFor("title bar controls visible", `String(['#task-list-toggle', '#task-list-header .task-list-brand', '#task-list-search-toggle', '#task-list-new'].every(selector => { const el=document.querySelector(selector); return el && el.getBoundingClientRect().width>0 && getComputedStyle(el).visibility==='visible'; }))`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();'collapse settings'`)
		waitFor("settings can collapse temporarily", `String(document.documentElement.classList.contains('task-list-collapsed') && localStorage.getItem('openvibely.taskListCollapsed')==='true')`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();'expand settings'`)
		b.evaluate(`document.querySelector('[data-settings-link="layout"]').click();'layout'`)
		waitFor("layout previews", `String(!document.querySelector('[data-settings-section="layout"]').hidden && document.querySelector('[data-settings-section="themes"]').hidden && !!document.querySelector('[data-settings-section="layout"] .display-preview'))`, "true")
		b.evaluate(`document.querySelector('[data-settings-link="chat"]').click();'chat'`)
		waitFor("chat previews", `String(!document.querySelector('[data-settings-section="chat"]').hidden && !!document.querySelector('[data-settings-section="chat"] .display-preview'))`, "true")
		for _, route := range []string{"models", "channels", "personality", "skills", "agents"} {
			b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/` + route + `"] ').click();'configuration'`)
			waitFor("settings navigation survives "+route, `String(!!document.getElementById('`+route+`-container') && document.documentElement.classList.contains('appearance-settings') && !document.documentElement.classList.contains('task-list-collapsed'))`, "true")
			waitFor("settings rail selected", `String(document.querySelector('.sidebar-aside [data-nav-base="/themes"]').getAttribute('aria-current')==='page')`, "true")
		}
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/workers"]').click();'limits'`)
		waitFor("worker limits editable", `String(document.documentElement.classList.contains('appearance-settings') && !!document.getElementById('limit-input-global') && document.getElementById('limit-input-global').getBoundingClientRect().width>0)`, "true")
		waitFor("workers only in settings with utilization visible", `String(!document.querySelector('.sidebar-aside [data-nav-base="/workers"]') && Array.from(document.querySelectorAll('#worker-settings-content th')).some(el=>el.textContent==='Running' && el.getBoundingClientRect().width>0))`, "true")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-link="layout"]').click();'appearance'`)
		waitFor("layout reachable from configuration", `String(!!document.querySelector('[data-settings-section="layout"]') && !document.querySelector('[data-settings-section="layout"]').hidden)`, "true")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'workspace'`)
		waitFor("workspace restored", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings') && document.documentElement.classList.contains('task-list-collapsed'))`, "true")
		waitFor("independent board scroll restored", `JSON.stringify([document.getElementById('kanban-board').scrollTop,document.querySelector('[data-category=backlog]').scrollTop,document.querySelector('[data-status=running]').scrollTop,document.querySelector('[data-status=pending]').scrollTop])`, "[80,120,180,240]")
		b.evaluate(`document.getElementById('open-settings').click();'settings again'`)
		waitFor("settings reopened", `String(document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.evaluate(`document.querySelector('[data-settings-link="chat"]').click();history.back();'back'`)
		waitFor("browser back exits settings", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.evaluate(`document.getElementById('open-settings').click();'settings for search'`)
		waitFor("settings before search", `String(document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.evaluate(`document.getElementById('task-list-search-input').value='existing query';document.getElementById('task-list-search-toggle').click();'search'`)
		waitFor("search exits settings and focuses", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings') && document.activeElement===document.getElementById('task-list-search-input') && document.activeElement.value==='existing query')`, "true")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.evaluate(`document.getElementById('open-settings').click();'mobile settings'`)
		waitFor("mobile navigation visible", `String(!!document.querySelector('.settings-mobile-nav') && document.querySelector('.settings-mobile-nav').getBoundingClientRect().height > 0)`, "true")
		b.evaluate(`document.querySelector('.settings-mobile-nav [data-settings-link="layout"]').click();'mobile layout'`)
		waitFor("mobile layout preview visible", `String(document.querySelector('[data-settings-section="layout"] .display-preview').getBoundingClientRect().height > 0)`, "true")
		b.evaluate(`htmx.ajax('GET','/workers',{target:'#main-content',swap:'innerHTML'});'monitor'`)
		waitFor("workers remain in settings", `String(!!document.getElementById('worker-settings-content') && document.documentElement.classList.contains('appearance-settings') && !!document.getElementById('limit-input-global') && document.getElementById('limit-input-global').getBoundingClientRect().width>0)`, "true")

	})
}
