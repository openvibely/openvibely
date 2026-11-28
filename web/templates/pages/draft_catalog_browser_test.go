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
	for _, scenario := range []struct{ chat, remote, dirty, remembered, pending, pasted bool }{
		{dirty: true}, {chat: true, dirty: true}, {remote: true}, {chat: true, remote: true},
		{remote: true, dirty: true}, {chat: true, remote: true, dirty: true},
		{remembered: true}, {chat: true, remembered: true}, {chat: true, dirty: true, pending: true},
		{remote: true, pasted: true}, {chat: true, remote: true, pasted: true},
		{remembered: true, pasted: true}, {chat: true, remembered: true, pasted: true},
	} {
		chat := scenario.chat
		t.Run(fmt.Sprint("chat=", chat, "/remote=", scenario.remote, "/dirty=", scenario.dirty, "/remembered=", scenario.remembered, "/pending=", scenario.pending, "/pasted=", scenario.pasted), func(t *testing.T) {
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
					if r.URL.Path == "/chat" {
						_ = SharedChat(projects, group.ID, nil, projects).Render(layout.WithSharedProjects(ctx, []layout.SharedProject{group}), w)
						return
					}
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
				if scenario.pasted {
					b.evaluate(`var dt=new DataTransfer();dt.setData('text/plain','keep this pasted draft '.repeat(300));document.querySelector('textarea[name="message"]').dispatchEvent(new ClipboardEvent('paste',{clipboardData:dt,bubbles:true,cancelable:true}));'pasted'`)
					b.waitFor("pasted card created with empty message", `String(document.querySelector('[data-pasted-text-cards]').children.length === 1 && document.querySelector('textarea[name="message"]').value === '')`, "true")
				}
				attachmentCase := chat && !scenario.remote && scenario.dirty
				if attachmentCase {
					b.evaluate(`var f=document.getElementById('shared-chat-form');var k=f.dataset.attachmentStateKey;sessionStorage.setItem(k,JSON.stringify({sessionID:'draft-upload',projectID:'local-one',attachments:[{filename:'screenshot.png',size:12,media_type:'image/png',session_id:'draft-upload'}]}));window.restoreChatPendingAttachmentState(f);'attached'`)
				}
				if scenario.pending {
					b.evaluate(`var f=document.getElementById('shared-chat-form');var realFetch=window.fetch;window.fetch=function(url,options){if(String(url).startsWith('/chat/attachments?'))return new Promise(function(){});return realFetch(url,options)};var dt=new DataTransfer();dt.items.add(new File(['new file'],'second.txt',{type:'text/plain'}));var fi=f.querySelector('input[type=file]');fi.files=dt.files;fi.dispatchEvent(new Event('change'));'attached'`)
				}
				if scenario.pending {
					b.waitFor("upload pending before regrouping", `String(document.getElementById('shared-chat-form').dataset.destinationAttachmentsPending === 'true' && document.querySelector('#shared-chat-form [name="attachment_session_id"]').value === '')`, "true")
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
				if scenario.dirty || scenario.pasted {
					expected = projects[initial].ID
				}
				b.waitFor("destination preserved or defaulted", fmt.Sprintf(`String(document.getElementById(document.querySelector('[data-project-input]').dataset.projectInput).value===%q)`, expected), "true")
				if scenario.dirty {
					b.waitFor("text preserved", `document.querySelector('textarea[name="message"]').value`, "keep this draft")
				}
				if scenario.pasted {
					b.waitFor("pasted card preserved", `String(document.querySelector('[data-pasted-text-cards]').children.length === 1)`, "true")
					b.evaluate(`document.querySelector('[data-pasted-text-cards] .ov-paste-show').click();'expanded'`)
					b.waitFor("pasted content preserved", `String(document.querySelector('textarea[name="message"]').value === 'keep this pasted draft '.repeat(300))`, "true")
				}
				if attachmentCase {
					b.waitFor("attachment draft migrated", `String(!!sessionStorage.getItem('openvibely-page-attachments-shared-chat-__repo_1234567890abcdef') && !sessionStorage.getItem('openvibely-page-attachments-shared-chat-local-one'))`, "true")
					b.call("Page.navigate", map[string]any{"url": server.URL + "/chat?project_id=" + group.ID}, nil)
					b.waitFor("attachment restored after reopening", `String(!!document.querySelector('#shared-chat-form[data-attachment-state-key="openvibely-page-attachments-shared-chat-__repo_1234567890abcdef"] [data-pending-attachment-filename="screenshot.png"]'))`, "true")
				}
				if !chat {
					b.waitFor("details updated", `String(!document.querySelector('[data-detail-property="destination_project"]').hidden && document.querySelector('[data-detail-property="destination_project"] span').textContent==='Server:')`, "true")
				}
			})
		})
	}
}
