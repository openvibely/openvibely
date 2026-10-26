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
		b.waitFor("workspace ready", `String(document.readyState === 'complete' && !!window.htmx && !!window.syncAppearanceSettings)`, "true")
		b.evaluate(`document.getElementById('task-list-toggle').click();document.querySelector('[data-category=backlog]').scrollTop=120;document.querySelector('[data-status=running]').scrollTop=180;document.querySelector('[data-status=pending]').scrollTop=240;document.getElementById('kanban-board').scrollTop=80;document.getElementById('open-settings').click();'opened'`)
		b.waitFor("themes only", `String(document.documentElement.classList.contains('appearance-settings') && !document.documentElement.classList.contains('task-list-collapsed') && !document.querySelector('[data-settings-section="themes"]').hidden && document.querySelector('[data-settings-section="layout"]').hidden)`, "true")
		b.evaluate(`document.querySelector('[data-settings-link="layout"]').click();'layout'`)
		b.waitFor("layout previews", `String(!document.querySelector('[data-settings-section="layout"]').hidden && document.querySelector('[data-settings-section="themes"]').hidden && !!document.querySelector('[data-settings-section="layout"] .display-preview'))`, "true")
		b.evaluate(`document.querySelector('[data-settings-link="chat"]').click();'chat'`)
		b.waitFor("chat previews", `String(!document.querySelector('[data-settings-section="chat"]').hidden && !!document.querySelector('[data-settings-section="chat"] .display-preview'))`, "true")
		for _, route := range []string{"models", "channels", "personality", "skills", "agents"} {
			b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/` + route + `"] ').click();'configuration'`)
			b.waitFor("settings navigation survives "+route, `String(!!document.getElementById('`+route+`-container') && document.documentElement.classList.contains('appearance-settings') && !document.documentElement.classList.contains('task-list-collapsed'))`, "true")
			b.waitFor("settings rail selected", `String(document.querySelector('.sidebar-aside [data-nav-base="/themes"]').getAttribute('aria-current')==='page')`, "true")
		}
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-nav-base="/settings/workers"]').click();'limits'`)
		b.waitFor("worker limits editable", `String(document.documentElement.classList.contains('worker-configuration') && document.getElementById('limit-input-global').getBoundingClientRect().width>0)`, "true")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-link="layout"]').click();'appearance'`)
		b.waitFor("layout reachable from configuration", `String(!!document.getElementById('themes-container') && !document.querySelector('[data-settings-section="layout"]').hidden)`, "true")
		b.evaluate(`document.querySelector('#appearance-settings-nav [data-settings-back]').click();'workspace'`)
		b.waitFor("workspace restored", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings') && document.documentElement.classList.contains('task-list-collapsed'))`, "true")
		b.waitFor("independent board scroll restored", `JSON.stringify([document.getElementById('kanban-board').scrollTop,document.querySelector('[data-category=backlog]').scrollTop,document.querySelector('[data-status=running]').scrollTop,document.querySelector('[data-status=pending]').scrollTop])`, "[80,120,180,240]")
		b.evaluate(`document.getElementById('open-settings').click();'settings again'`)
		b.waitFor("settings reopened", `String(document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.evaluate(`document.querySelector('[data-settings-link="chat"]').click();history.back();'back'`)
		b.waitFor("browser back exits settings", `String(!!document.getElementById('settings-workspace') && !document.documentElement.classList.contains('appearance-settings'))`, "true")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.evaluate(`document.getElementById('open-settings').click();'mobile settings'`)
		b.waitFor("mobile navigation visible", `String(!!document.querySelector('.settings-mobile-nav') && document.querySelector('.settings-mobile-nav').getBoundingClientRect().height > 0)`, "true")
		b.evaluate(`document.querySelector('.settings-mobile-nav [data-settings-link="layout"]').click();'mobile layout'`)
		b.waitFor("mobile layout preview visible", `String(document.querySelector('[data-settings-section="layout"] .display-preview').getBoundingClientRect().height > 0)`, "true")
		b.evaluate(`htmx.ajax('GET','/workers',{target:'#main-content',swap:'innerHTML'});'monitor'`)
		b.waitFor("worker monitor read only", `String(!!document.getElementById('worker-settings-content') && !document.documentElement.classList.contains('appearance-settings') && document.getElementById('limit-input-global').getBoundingClientRect().width===0)`, "true")

	})
}
