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

func TestBrowserFunctional_HeaderDropdownHeightIgnoresTextMetrics(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	cases := []struct {
		name    string
		content templ.Component
	}{
		{"chat", ChatContent(nil, nil, "project", nil, nil, nil, false, false, 20)},
		{"alerts", AlertsContent([]models.AlertSummary{{ID: "alert", Title: "Alert"}}, "project", 1)},
		{"skills", SkillsContent(nil, true)},
		{"custom-pages", PluginSettingsContent(PluginSettingsView{ProjectID: "project"})},
		{"channels", SettingsContent(defaultChannelsSettingsView("project"))},
		{"automations", AutomationsContent(nil, "project")},
		{"automation-live", AutomationLiveContent(models.AutomationLiveGraph{Automation: models.Automation{ID: "automation", Name: "Automation"}}, "project", true)},
		{"automation-builder", AutomationBuilderContent(models.AutomationBuilderPage{Source: "blank", Result: models.AutomationDraftResult{Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Automation", AutomationType: "custom", AdapterKey: "custom"}}}, "project")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path != "/fixture" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if err := layout.Base("Header menus", nil, "project").Render(templ.WithChildren(r.Context(), tc.content), w); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/fixture", "header-dropdown-height", func(b *composerFocusCDP) {
				b.waitFor("page loaded", `String(document.readyState==='complete')`, "true")
				// A larger inherited line box exposes the baseline space that varies
				// with platform fonts, even on systems where the default happens to fit.
				got := b.evaluate(`(()=>{
				 const menu=document.querySelector('#main-content [data-page-header] .dropdown');
				 if(!menu) return 'missing header dropdown';
				 const header=menu.closest('[data-page-header]'), button=menu.querySelector(':scope > button');
				 const initial=header.getBoundingClientRect().height;
				 for(const lineHeight of ['24px','40px']) {
				  menu.style.lineHeight=lineHeight;
				  const m=menu.getBoundingClientRect(), b=button.getBoundingClientRect();
				  if(m.height!==32 || b.height!==32 || m.top!==b.top || header.getBoundingClientRect().height!==initial)
				   return 'line-height='+lineHeight+', wrapper='+m.height+', button='+b.height+', header='+header.getBoundingClientRect().height;
				 }
				 menu.style.lineHeight='';
				 return 'pass';
				})()`)
				if got != "pass" {
					t.Fatalf("header dropdown layout: %s", got)
				}
			})
		})
	}
}
