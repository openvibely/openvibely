package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_AutomationNodeSidePanel(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "panel-project", Name: "Panel"}
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Panel automation", AutomationType: "custom", AdapterKey: "custom",
		Nodes: []models.AutomationDraftNode{
			{Key: "first", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review the code", "priority": 2, "category": "backlog"}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
			{Key: "second", Name: "Implement", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the change", "priority": 2, "category": "backlog"}, Position: &models.AutomationDraftPoint{X: 260, Y: 0}},
		}, Edges: []models.AutomationDraftEdge{{Key: "handoff", From: "first", To: "second"}},
	}
	saved := make(chan models.AutomationDraftCandidate, 1)
	var rejectNextSave atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/automations/yaml/parse" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"valid":true}`)
			return
		}
		if r.URL.Path == "/tasks/sidebar" || r.URL.Path == "/ui/preferences" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/preview" {
			config, _ := json.Marshal(candidate.Nodes[0].Config)
			graph := models.AutomationLiveGraph{Automation: models.Automation{ID: "panel-live", Name: candidate.Name}, Nodes: []models.AutomationLiveNode{{AutomationNode: models.AutomationNode{ID: "live-first", NodeKey: "first", Name: "Review", NodeType: models.AutomationNodeAgentTask, ConfigJSON: string(config)}, DisplayState: "idle"}}}
			if r.Header.Get("HX-Request") == "true" {
				if err := AutomationLiveContent(graph, project.ID, false).Render(r.Context(), w); err != nil {
					t.Error(err)
				}
			} else if err := AutomationLive([]models.Project{project}, project.ID, graph, false).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/edit" && r.URL.Path != "/automations/builder" {
			http.NotFound(w, r)
			return
		}
		current := candidate
		page := models.AutomationBuilderPage{Source: "blank"}
		if r.Method == http.MethodPost {
			var err error
			current, err = service.DecodeAutomationDraftYAML([]byte(r.FormValue("automation_yaml")))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if r.FormValue("save_changes") == "true" {
				if rejectNextSave.Swap(false) {
					page.SelectedNodeKey = r.FormValue("selected_node")
					page.Error = "Validation rejected this save"
				} else {
					saved <- current
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			if r.FormValue("builder_action") == "create_node" {
				current.Nodes = append(current.Nodes, models.AutomationDraftNode{Key: "new_task", Name: r.FormValue("node_name"), Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "New prompt", "category": "backlog", "priority": 2}, Position: &models.AutomationDraftPoint{X: 520, Y: 0}})
				page.SelectedNodeKey = "new_task"
				page.FocusNode = true
			}
		}
		page.Result.Candidate = current
		page.YAML, _ = service.EncodeAutomationDraftYAML(current)
		if r.Header.Get("HX-Request") == "true" {
			if err := AutomationBuilderContent(page, project.ID).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		} else {
			if err := AutomationBuilder([]models.Project{project}, project.ID, page).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/edit?project_id="+project.ID, "automation-node-panel", func(b *composerFocusCDP) {
		b.waitFor("builder ready", `String(typeof document.getElementById('automation-builder')?.hasUnsavedAutomationChanges==='function')`, "true")
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1500, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("initially closed", `String(document.getElementById('automation-node-panel').hidden)`, "true")
		b.evaluate(`document.querySelector('[data-node-key="first"]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));'opened'`)
		b.waitFor("first node docked", `String(!document.getElementById('automation-node-panel').hidden && document.getElementById('automation-node-panel').dataset.overlay==='false' && !document.querySelector('[data-automation-node-detail="first"]').hidden && document.querySelector('[data-automation-node-detail="second"]').hidden)`, "true")
		b.waitFor("graph alongside panel", `String(document.querySelector('[data-automation-graph-panel]').getBoundingClientRect().right <= document.getElementById('automation-node-panel').getBoundingClientRect().left)`, "true")
		b.waitFor("selection alone is not dirty", `String(document.getElementById('automation-builder').hasUnsavedAutomationChanges())`, "false")
		b.evaluate(`var prompt=document.querySelector('[name="node_first_prompt"]');prompt.value='Preserve this edited prompt';prompt.dispatchEvent(new Event('input',{bubbles:true}));'edited'`)
		b.click(`[data-node-key="second"] rect`)
		b.waitFor("switch selected node", `String(!document.querySelector('[data-automation-node-detail="second"]').hidden && document.querySelector('[data-automation-node-detail="first"]').hidden)`, "true")
		b.evaluate(`window.failedRequestDone=false;htmx.ajax('GET','/missing',{target:'#automation-builder'}).catch(function(){}).finally(function(){window.failedRequestDone=true;});'requested'`)
		b.waitFor("failed request completed", `String(window.failedRequestDone)`, "true")
		b.click("#automation-node-divider")
		b.evaluate(`document.getElementById('automation-node-divider').dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowLeft',bubbles:true,cancelable:true}));'resized'`)
		b.waitFor("shared keyboard resize", `localStorage.getItem('automation-inspector-width')`, "440")
		b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));'closed'`)
		b.waitFor("focus returned", `document.activeElement.dataset.nodeKey`, "second")
		b.click(`[data-node-key="second"] rect`)
		rejectNextSave.Store(true)
		b.evaluate(`window.previousBuilder=document.getElementById('automation-builder');'remembered'`)
		b.click("[data-automation-builder-save]")
		b.waitFor("validation rerender restores selected panel", `String(document.getElementById('automation-builder') !== window.previousBuilder && document.getElementById('automation-builder').dataset.selectedNode==='second' && !document.getElementById('automation-node-panel').hidden)`, "true")
		b.waitFor("validation rerender preserves edits", `document.querySelector('[name="node_first_prompt"]').value`, "Preserve this edited prompt")
		b.click("[data-automation-node-close]")
		b.click("[data-automation-add-node-open]")
		b.evaluate(`document.querySelector('[name="node_kind"]').value='task';document.querySelector('[name="node_name"]').value='Validate work';'named'`)
		b.click("[data-automation-create-node]")
		b.waitFor("new node panel auto opens", `String(!document.getElementById('automation-node-panel').hidden && document.getElementById('automation-builder').dataset.selectedNode==='new_task')`, "true")
		b.waitFor("new node auto opens and focuses name", `String(!document.getElementById('automation-node-panel').hidden && document.activeElement.name==='node_new_task_name')`, "true")
		b.waitFor("edits survive add node", `document.querySelector('[name="node_first_prompt"]').value`, "Preserve this edited prompt")
		b.click("[data-automation-node-close]")
		b.click("[data-automation-builder-save]")
		select {
		case result := <-saved:
			if len(result.Nodes) != 3 || result.Nodes[0].Config["prompt"] != "Preserve this edited prompt" {
				t.Fatalf("save lost edits: %#v", result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Save request did not reach server")
		}
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 520, "height": 800, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.evaluate(`document.querySelector('[data-node-key="second"]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));'opened'`)
		b.waitFor("mobile overlay", `String(document.getElementById('automation-node-panel').dataset.overlay==='true' && document.querySelector('[data-automation-graph-panel]').inert)`, "true")
		b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));'closed'`)
		b.waitFor("Escape restores graph focus", `String(document.getElementById('automation-node-panel').hidden && !document.querySelector('[data-automation-graph-panel]').inert && document.activeElement.dataset.nodeKey==='second')`, "true")
		b.call("Page.navigate", map[string]any{"url": server.URL + "/preview?project_id=" + project.ID}, nil)
		b.waitFor("preview ready", `String(!!document.querySelector('[data-automation-live-node]'))`, "true")
		b.evaluate(`document.querySelector('[data-automation-live-node]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));'opened'`)
		b.waitFor("preview is read-only", `String(!document.getElementById('automation-node-panel').hidden && !document.getElementById('automation-node-panel').querySelector('input,textarea,select') && document.getElementById('automation-node-panel').textContent.includes('Review the code'))`, "true")
		b.evaluate(`window.rejectedPreview=document.getElementById('automation-live');window.rejectedRefreshDone=false;htmx.ajax('GET','/preview',{target:'#automation-live',swap:'outerHTML'}).finally(function(){window.rejectedRefreshDone=true;});'requested'`)
		b.waitFor("stale refresh rejected", `String(window.rejectedRefreshDone && document.getElementById('automation-live')===window.rejectedPreview)`, "true")
		b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));'closed'`)
		b.waitFor("rejected refresh leaves Escape working", `String(document.getElementById('automation-node-panel').hidden && !document.querySelector('[data-automation-graph-panel]').inert)`, "true")
		b.evaluate(`document.querySelector('[data-automation-live-node]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));'opened'`)
		b.evaluate(`window.previousPreview = document.getElementById('automation-live'); window.openVibelyAutomationLiveRefresh('GET', '/preview?project_id=panel-project'); 'refreshing'`)
		b.waitFor("preview refresh preserves selected node", `String(document.getElementById('automation-live') !== window.previousPreview && !document.getElementById('automation-node-panel').hidden && document.querySelector('[data-automation-live-node-detail="first"]').hidden === false)`, "true")

	})
}
