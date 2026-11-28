package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	templateui "github.com/openvibely/openvibely/web/templates"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_DraftCatalogReconnect(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, scenario := range []struct{ chat, remote, dirty, remembered bool }{{false, false, true, false}, {true, false, true, false}, {false, true, false, false}, {true, true, false, false}, {false, true, true, false}, {true, true, true, false}, {false, false, false, true}, {true, false, false, true}} {
		chat := scenario.chat
		t.Run(fmt.Sprint("chat=", chat, "/remote=", scenario.remote, "/dirty=", scenario.dirty, "/remembered=", scenario.remembered), func(t *testing.T) {
			projects := []models.Project{{ID: "local-one", Name: "Foo"}, {ID: "r-1234567890abcdef-remote-one", Name: "Foo"}}
			initial := 0
			if scenario.remote {
				initial = 1
			}
			group := layout.SharedProject{ID: "__repo_1234567890abcdef", Name: "Foo", Members: []layout.SharedMember{{ID: projects[0].ID, Name: "Foo", Machine: "Local"}, {ID: projects[1].ID, Name: "Foo", Machine: "Remote"}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/wails/runtime.js" {
					fmt.Fprint(w, `export const Window={};`)
					return
				}
				if r.URL.Path == "/ui/preferences" {
					w.WriteHeader(204)
					return
				}
				ctx := layout.WithUIPreferences(layout.WithDesktopMode(context.Background(), true), layout.UIPreferences{PinnedProjectIDs: []string{"local-one"}})
				if r.URL.Path == "/ui/project-catalog" {
					_ = layout.ProjectCatalog(projects, "local-one").Render(layout.WithSharedProjects(ctx, []layout.SharedProject{group}), w)
					return
				}
				if r.URL.Path != "/tasks/new" && r.URL.Path != "/chat" {
					w.WriteHeader(204)
					return
				}
				if r.URL.Query().Get("model_options") != "" || r.URL.Query().Get("agent_options") != "" {
					w.WriteHeader(204)
					return
				}
				if r.URL.Query().Get("project_id") == group.ID {
					ctx = templateui.WithProjectNames(ctx, projects)
					_ = NewTaskContent(&models.Project{ID: group.ID}, nil, nil, projects...).Render(ctx, w)
					return
				}
				if chat {
					_ = SharedChat(projects, projects[initial].ID, nil, projects[initial:initial+1]).Render(ctx, w)
				} else {
					_ = NewTask(projects, &projects[initial], nil, nil).Render(ctx, w)
				}
			}))
			defer server.Close()
			path := "/tasks/new"
			if chat {
				path = "/chat"
			}
			runComposerFocusCDP(t, chrome, server.URL+path+"?project_id="+projects[initial].ID, "draft-catalog", func(b *composerFocusCDP) {
				b.waitFor("composer ready", `String(!!document.querySelector('[data-project-input]'))`, "true")
				b.waitFor("single-server picker hidden", `String(document.querySelector('[data-project-input]').getClientRects().length === 0)`, "true")
				if !chat {
					b.click("#task-details-opener")
					b.waitFor("Details panel open", `document.getElementById('task-details-opener').getAttribute('aria-expanded')`, "true")
					b.waitFor("single-server Details row hidden", `String(document.querySelector('[data-detail-property="destination_project"]').getClientRects().length === 0)`, "true")
				}
				if scenario.remembered {
					b.evaluate(`localStorage.setItem("ov.composer.destination.__repo_1234567890abcdef","r-1234567890abcdef-remote-one");"saved"`)
				}
				if scenario.dirty {
					b.evaluate(`document.querySelector('textarea[name="message"]').value='keep this draft';'typed'`)
				}
				b.evaluate(`window.dispatchEvent(new Event('openvibely-servers-changed'));'sent'`)
				b.waitFor("picker updated", `String(document.querySelector('[data-project-input]').dataset.destinationScope==='__repo_1234567890abcdef' && !document.querySelector('[data-project-input]').parentElement.hidden)`, "true")
				b.waitFor("shared picker visible", `String(document.querySelector('[data-project-input]').getClientRects().length > 0)`, "true")
				if !chat {
					b.waitFor("shared Details row visible", `String(document.querySelector('[data-detail-property="destination_project"]').getClientRects().length > 0)`, "true")
				}
				expected := projects[0].ID
				if scenario.remembered {
					expected = projects[1].ID
				}
				if scenario.dirty {
					expected = projects[initial].ID
				}
				b.waitFor("destination preserved or defaulted", fmt.Sprintf(`String(document.getElementById(document.querySelector('[data-project-input]').dataset.projectInput).value===%q)`, expected), "true")
				if scenario.dirty {
					b.waitFor("text preserved", `document.querySelector('textarea[name="message"]').value`, "keep this draft")
				}
				if !chat {
					b.waitFor("details updated", `String(!document.querySelector('[data-detail-property="destination_project"]').hidden && document.querySelector('[data-detail-property="destination_project"] span').textContent==='Server:')`, "true")
				}
			})
		})
	}
}
