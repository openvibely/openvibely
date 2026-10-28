package pages

import (
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_TaskChainPanel(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "chain-project", Name: "Chain"}
	task := &models.Task{ID: "chain-task", ProjectID: project.ID, Title: "Chain", Category: models.CategoryBacklog, Status: models.StatusPending}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/tasks/chain-task/attachments" {
			_ = components.TaskFilesList(nil, project.ID, nil, nil).Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/tasks/chain-task/chain" {
			config := &models.ChainConfiguration{Enabled: r.FormValue("chain_enabled") == "true", Trigger: r.FormValue("chain_trigger"), ChildCategory: r.FormValue("chain_child_category"), ChildModel: r.FormValue("chain_child_model")}
			if r.FormValue("chain_remove") == "true" {
				config = nil
			}
			if config == nil {
				_ = task.SetChainConfig(nil)
			} else {
				data, _ := json.Marshal(config)
				task.ChainConfig = string(data)
			}
			_ = TaskChainPanel(task, nil).Render(r.Context(), w)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/new" {
			_ = NewTask([]models.Project{project}, &project, nil, nil).Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/tasks/chain-task" {
			_ = TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chaining", nil).Render(r.Context(), w)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	for _, path := range []string{"/new?tab=chaining", "/tasks/chain-task?tab=chaining"} {
		t.Run(path, func(t *testing.T) {
			runComposerFocusCDP(t, chrome, server.URL+path, "chain-panel", func(b *composerFocusCDP) {
				b.waitFor("chain add visible", `String(!!document.querySelector('[data-chain-add]') && document.querySelector('[data-chain-add]').getClientRects().length>0)`, "true")
				// Visibility can precede DOMContentLoaded shell initialization. Capture
				// geometry only after fonts and layout observers have settled, as the
				// native click helper does before measuring its target coordinates.
				b.waitFor("page initialization complete", `document.readyState`, "complete")
				b.evaluateAwait(`document.fonts.ready.then(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve('settled')))))`)
				b.evaluate(`window.chainHeadingTop=document.querySelector('#task-chain-panel h3').getBoundingClientRect().top; 'saved'`)
				b.evaluate(`window.panelContentTop=document.querySelector("[data-chain-empty]").getBoundingClientRect().top; "saved"`)
				waitAligned := func(label, selector, baseline string) {
					t.Helper()
					b.waitFor(label, fmt.Sprintf(`(()=>{const el=document.querySelector(%q),expected=window[%q],actual=el?.getBoundingClientRect().top;return el&&el.getClientRects().length&&Math.abs(actual-expected)<1?'true':JSON.stringify({actual,expected,delta:actual-expected,panelTop:document.querySelector('#task-details-panel').getBoundingClientRect().top,scrollTop:document.querySelector('#task-inspector-body').scrollTop});})()`, selector, baseline), "true")
				}
				for _, tab := range []struct{ key, heading string }{{"details", "#task-detail-metrics h3"}, {"schedules", "#task-schedule-panel h3"}, {"attachments", "#tab-attachments h3"}} {
					b.click("#inspector-tab-" + tab.key)
					if tab.key == "attachments" && path != "/new?tab=chaining" {
						// Saved tasks use an upload control instead of a Files heading.
						b.waitFor("files upload control visible", `String(document.querySelector('#add-attachment-btn')?.getClientRects().length>0)`, "true")
						b.waitFor("files list below upload control", `String((()=>{const list=document.querySelector('#attachment-list'),add=document.querySelector('#add-attachment-btn');return !!list && list.getBoundingClientRect().top>=add.getBoundingClientRect().bottom;})())`, "true")
						continue
					}
					waitAligned("heading aligned with "+tab.key, tab.heading, "chainHeadingTop")
				}
				if path == "/new?tab=chaining" {
					waitAligned("files content aligned with chaining", "#draft-panel-attachments, #attachment-list", "panelContentTop")
				}
				if path != "/new?tab=chaining" {
					b.click("#inspector-tab-lifecycle")
					waitAligned("lifecycle heading aligned", "#tab-lifecycle h3", "chainHeadingTop")
					waitAligned("lifecycle content aligned with chaining", "#lifecycle-activity-scroll", "panelContentTop")
				}
				b.click("#inspector-tab-chaining")
				b.evaluate(`window.retainedThread=document.getElementById('tab-chat'); 'saved'`)
				b.click(`[data-chain-add]`)
				b.evaluate(`document.querySelector('[data-chain-form] [name="chain_child_category"]').value='backlog'; 'set'`)
				b.click(`[data-chain-save]`)
				b.waitFor("configured card", `String(!document.querySelector('[data-chain-card]').hidden && !document.getElementById('task-chain-editor').open && document.querySelector('[data-chain-category]').textContent.includes('backlog'))`, "true")
				waitAligned("configured heading keeps its position", "#task-chain-panel h3", "chainHeadingTop")
				b.waitFor("thread retained", `String(window.retainedThread===document.getElementById('tab-chat'))`, "true")
				b.click(`[data-chain-card] [data-chain-open]`)
				b.waitFor("consistent delete label", `document.querySelector("[data-chain-remove]").textContent`, "Delete follow-up")
				b.evaluate(`document.querySelector('[data-chain-form] [name="chain_child_category"]').value='active'; 'set'`)
				b.click(`#task-chain-editor .btn-ghost.ml-auto`)
				b.click(`[data-chain-card] [data-chain-open]`)
				b.waitFor("cancel discards changes", `document.querySelector('[data-chain-form] [name="chain_child_category"]').value`, "backlog")
				b.click(`#task-chain-editor .btn-ghost.ml-auto`)
				b.click(`[data-chain-toggle]`)
				b.waitFor("paused", `String(!document.querySelector('[data-chain-toggle]').checked && !document.querySelector('[data-chain-toggle]').disabled)`, "true")
				b.click(`[data-chain-card] [data-chain-open]`)
				b.click(`[data-chain-remove]`)
				b.waitFor("confirmation open", `String(document.getElementById('task-chain-delete').open)`, "true")
				b.click(`#task-chain-delete .btn-error`)
				b.waitFor("removed", `String(document.querySelector('[data-chain-card]').hidden && !document.querySelector('[data-chain-add]').hidden && !document.querySelector('dialog[open]'))`, "true")
			})
		})
	}
}
