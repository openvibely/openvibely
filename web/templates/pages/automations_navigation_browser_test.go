package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestAutomationGraphThemeAndHistoryNavigationInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}
	var base bytes.Buffer
	if err := layout.Base("Automation navigation fixture", nil, "project-browser").Render(context.Background(), &base); err != nil {
		t.Fatalf("render production navigation lifecycle: %v", err)
	}
	baseHTML := base.String()
	navStart := strings.Index(baseHTML, "window.openVibelyNavigate = function")
	navEnd := strings.Index(baseHTML[navStart:], "// Scroll position restoration for drop zones")
	if navStart < 0 || navEnd < 0 {
		t.Fatal("could not isolate production HTMX navigation lifecycle")
	}
	navigationScript := baseHTML[navStart : navStart+navEnd]

	projectID := "project-browser"
	cards := []models.AutomationCard{
		{Automation: models.Automation{ID: "automation-a", Name: "Automation A", Description: "First", LifecycleState: models.AutomationActive}, Version: models.AutomationVersion{Version: 1, AdapterKey: "native_sdlc"}},
		{Automation: models.Automation{ID: "automation-b", Name: "Automation B", Description: "Second", LifecycleState: models.AutomationActive}, Version: models.AutomationVersion{Version: 1, AdapterKey: "native_sdlc"}},
	}
	renderPortfolio := func() string {
		var out bytes.Buffer
		if err := AutomationsContent(cards, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation portfolio: %v", err)
		}
		return out.String()
	}
	renderLive := func(id, name string) string {
		node := models.AutomationNode{ID: id + "-node", Name: "A very long automation node name that must wrap safely", NodeType: models.AutomationNodeAgentTask, PositionX: 20, PositionY: -90}
		graph := models.AutomationLiveGraph{
			Automation:   models.Automation{ID: id, Name: name, Description: "Theme and navigation fixture", LifecycleState: models.AutomationActive},
			Version:      models.AutomationVersion{Version: 1, AdapterKey: "native_sdlc"},
			Nodes:        []models.AutomationLiveNode{{AutomationNode: node, DisplayState: "idle"}},
			RecentCutoff: time.Unix(1, 0),
		}
		var out bytes.Buffer
		if err := AutomationLiveContent(graph, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation live graph: %v", err)
		}
		return out.String()
	}

	renderHistory := func(id, name string) string {
		dashboard := models.AutomationHistoryDashboard{
			Automation: models.Automation{ID: id, Name: name, HealthState: models.AutomationHealthUnknown},
			Health:     models.AutomationHealth{State: models.AutomationHealthUnknown, Reason: "No terminal invocation yet", EvaluatedAt: time.Unix(1, 0)},
		}
		var out bytes.Buffer
		if err := AutomationHistoryContent(dashboard, projectID, "").Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation history: %v", err)
		}
		return out.String()
	}
	renderDefinition := func(id, name string) string {
		node := models.AutomationNode{ID: id + "-node", Name: "Readable node", NodeType: models.AutomationNodeAgentTask, PositionX: 20, PositionY: 20}
		definition := models.AutomationDefinition{
			Automation: models.Automation{ID: id, Name: name, Description: "Theme and navigation fixture", LifecycleState: models.AutomationActive},
			Version:    models.AutomationVersion{Version: 1, AdapterKey: "native_sdlc"},
			Nodes:      []models.AutomationNode{node},
		}
		var out bytes.Buffer
		if err := AutomationDefinitionContent(definition, nil, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation definition: %v", err)
		}
		return out.String()
	}

	renderBlankBuilder := func(withNode bool) string {
		candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Blank Automation", AutomationType: "vision_driver", AdapterKey: "vision_driver"}
		if withNode {
			candidate.Nodes = []models.AutomationDraftNode{{Key: "first_step", Name: "First step", Type: models.AutomationNodeAgentTask, Role: "custom_agent_task", Config: map[string]any{"prompt": "Describe the work this node should perform.", "category": "backlog", "priority": 2}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}}}
		}
		page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate, Definition: &models.AutomationDefinition{Automation: models.Automation{ID: "automation-blank", Name: "Blank Automation"}, Version: models.AutomationVersion{ID: "version-blank", AdapterKey: "vision_driver"}}}}
		var out bytes.Buffer
		if err := AutomationBuilderContent(page, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render blank Automation builder: %v", err)
		}
		return out.String()
	}

	renderBuilder := func() string {
		trigger := models.AutomationDraftNode{Key: "vision_trigger", Name: "Vision Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"target_node_key": "vision_driver", "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}}
		driver := models.AutomationDraftNode{Key: "vision_driver", Name: "Vision Driver", Type: models.AutomationNodeAgentTask, Role: "vision_driver", Config: map[string]any{"prompt": "Review vision", "category": "scheduled", "priority": 2}, Position: &models.AutomationDraftPoint{X: 240, Y: 0}}
		result := models.AutomationDraftNode{Key: "result", Name: "Result", Type: models.AutomationNodeOutcome, Role: "custom_outcome", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 480, Y: 160}}
		edge := models.AutomationDraftEdge{Key: "trigger_to_driver", From: "vision_trigger", To: "vision_driver"}
		candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Visual Draft", AutomationType: "vision_driver", AdapterKey: "vision_driver", Nodes: []models.AutomationDraftNode{trigger, driver, result}}
		page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate, Definition: &models.AutomationDefinition{Automation: models.Automation{ID: "automation-draft", Name: "Visual Draft"}, Version: models.AutomationVersion{ID: "version-draft", AdapterKey: "vision_driver"}}}, NodePalette: []models.AutomationDraftNode{trigger, driver}, EdgePalette: []models.AutomationDraftEdge{edge}}
		var out bytes.Buffer
		if err := AutomationBuilderContent(page, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation builder: %v", err)
		}
		return out.String()
	}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > 5000) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function click(selector, label) {
    var element = document.querySelector(selector);
    if (!element) fail('missing ' + label);
    if (typeof element.click === 'function') element.click();
    else element.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true}));
  }
  function liveID() { var root = document.getElementById('automation-live'); return root && root.dataset.automationId; }
  function historyReady() { return !!document.getElementById('automation-history'); }
  function definitionReady() { return !!document.getElementById('automation-definition'); }
  function portfolioReady() { return !!document.getElementById('automations-container'); }
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}); }
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message || 'window error')); });
  (async function() {
    await report('progress', 'runner-started');
    await waitFor(function() { return window.htmx && document.body; }, 'HTMX initialization');
    htmx.process(document.body);
    await wait(50);
    await window.openVibelyNavigate('/automations/automation-a?project_id=project-browser');
    await report('progress', 'automation-a-clicked');
    await waitFor(function() { return liveID() === 'automation-a'; }, 'Automation A');
    await report('progress', 'automation-a-loaded');
	    var node = document.querySelector('.automation-graph-node');
	    var label = document.querySelector('.automation-node-content strong');
	    var nodeFill = getComputedStyle(node).fill;
	    var labelColor = getComputedStyle(label).color;
	    if (nodeFill === 'rgb(0, 0, 0)' || nodeFill === 'rgba(0, 0, 0, 0)') fail('node fill fell back to black: ' + nodeFill);
	    if (labelColor === 'rgb(0, 0, 0)') fail('label color fell back to black: ' + labelColor);
	    var canvasRect = document.querySelector('[data-automation-canvas]').getBoundingClientRect();
	    var nodeRect = node.getBoundingClientRect();
	    if (nodeRect.top < canvasRect.top - 1 || nodeRect.bottom > canvasRect.bottom + 1) fail('negative-position node is clipped by graph viewport');
	    var labelRect = label.getBoundingClientRect();
	    if (labelRect.left < nodeRect.left - 1 || labelRect.right > nodeRect.right + 1 || labelRect.top < nodeRect.top - 1 || labelRect.bottom > nodeRect.bottom + 1) fail('node label escapes its node bounds');
	    if (getComputedStyle(label).overflow !== 'hidden') fail('long node label is not visibly bounded');
	    if (!document.body.textContent.includes('No active work')) fail('zero counters did not collapse to a readable summary');
    click('[data-automation-view="history"]', 'History tab from Live');
    await waitFor(historyReady, 'History from Live');
    await report('progress', 'history-loaded');
    click('[data-automation-view="live"]', 'Live tab from History');
    await waitFor(function() { return liveID() === 'automation-a'; }, 'Live from History');
    click('[data-automation-view="definition"]', 'Definition tab from Live');
    await waitFor(definitionReady, 'Definition from Live');
    await report('progress', 'definition-loaded');
    click('[data-automation-view="live"]', 'Live tab from Definition');
    await waitFor(function() { return liveID() === 'automation-a'; }, 'Live from Definition');
    click('[data-automation-view="history"]', 'History tab before browser Back');
    await waitFor(historyReady, 'History before browser Back');
    history.back();
    await waitFor(function() { return liveID() === 'automation-a'; }, 'Live after browser Back from History');
    await report('progress', 'history-browser-back-restored');
    click('[data-automation-view="history"]', 'History tab after browser Back');
    await waitFor(historyReady, 'History after browser Back');
    click('#automation-history > a[href^="/automations?"]', 'in-page Automations back link from History');
    await waitFor(portfolioReady, 'portfolio after History back link');
    await report('progress', 'history-in-page-back-restored');
    click('a[href^="/automations/automation-b?"]', 'Automation B card after History return');
    await waitFor(function() { return liveID() === 'automation-b'; }, 'Automation B after History return');
    await report('progress', 'automation-b-clicked');

    click('#automation-live > a[href^="/automations?"]', 'in-page Automations back link');
    await report('progress', 'in-page-back-clicked');
    await waitFor(portfolioReady, 'portfolio after in-page back link');
    await report('progress', 'in-page-back-restored');
    await wait(100);
    click('a[href^="/automations/automation-a?"]', 'Automation A card after in-page back');
    await report('progress', 'automation-a-reclicked');
	    await waitFor(function() { return liveID() === 'automation-a'; }, 'Automation A after in-page back');

	    await window.openVibelyNavigate('/automations/automation-blank/drafts/version-blank?project_id=project-browser');
	    await waitFor(function() { return !!document.querySelector('[data-automation-add-first-node]'); }, 'empty Blank Automation canvas');
	    await report('progress', 'blank-canvas-loaded');
	    click('[data-automation-add-first-node]', 'Add first node action');
	    var nodeDialog = document.querySelector('[data-automation-node-dialog]');
	    if (!nodeDialog || !nodeDialog.open) fail('Add first node did not open the node dialog');
	    await report('progress', 'add-node-dialog-opened');
	    nodeDialog.querySelector('[name="node_name"]').value = 'First step';
	    nodeDialog.querySelector('[name="node_type"]').value = 'agent_task';
	    var nodeForm = nodeDialog.querySelector('form[hx-post]');
	    if (!nodeForm || !nodeForm.checkValidity()) fail('Add node form is not submittable: ' + (nodeDialog.querySelector(':invalid') && nodeDialog.querySelector(':invalid').validationMessage));
	    htmx.process(nodeForm);
	    nodeForm.requestSubmit(nodeDialog.querySelector('[data-automation-create-node]'));
	    await report('progress', 'add-node-submitted');
	    await waitFor(function() { return !!document.querySelector('[data-node-key="first_step"]'); }, 'new node on blank canvas');

	    await window.openVibelyNavigate('/automations/automation-draft/drafts/version-draft?project_id=project-browser');
	    await waitFor(function() { return !!document.querySelector('[data-automation-draft-canvas]'); }, 'visual builder');
	    var draftForm = document.querySelector('[data-automation-draft-form]');
	    var connectionSubmissions = 0;
	    draftForm.addEventListener('submit', function(event) {
	      connectionSubmissions++;
	      event.preventDefault();
	      event.stopImmediatePropagation();
	    });
		    function port(node, side) { return document.querySelector('[data-connect-port="' + node + '"][data-port-side="' + side + '"]'); }
		    function dragConnection(from, fromSide, to, toSide, pointerId) {
		      var sourceHandle = port(from, fromSide);
		      var targetHandle = port(to, toSide);
		      if (!sourceHandle || !targetHandle) fail('missing two-sided drag connector for ' + from + ' to ' + to);
		      var sourceRect = sourceHandle.getBoundingClientRect();
		      var targetRect = targetHandle.getBoundingClientRect();
		      sourceHandle.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:sourceRect.left+sourceRect.width/2, clientY:sourceRect.top+sourceRect.height/2}));
		      targetHandle.dispatchEvent(new PointerEvent('pointermove', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:targetRect.left+targetRect.width/2, clientY:targetRect.top+targetRect.height/2}));
		      targetHandle.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:targetRect.left+targetRect.width/2, clientY:targetRect.top+targetRect.height/2}));
		    }
		    dragConnection('vision_trigger', 'left', 'vision_driver', 'right', 11);
		    dragConnection('vision_driver', 'right', 'result', 'left', 12);
		    dragConnection('result', 'left', 'vision_trigger', 'right', 13);	    var candidateInput = document.querySelector('[data-automation-draft-form] [data-candidate-json]');
	    var connectedCandidate = JSON.parse(candidateInput.value);
	    if (connectionSubmissions !== 0) fail('drag connections submitted and replaced the builder fragment');
	    if (connectedCandidate.edges.length !== 3) fail('three consecutive drag connections were not retained');
	    var pairs = connectedCandidate.edges.map(function(edge) { return edge.from + '>' + edge.to; });
		    ['vision_trigger>vision_driver', 'vision_driver>result', 'result>vision_trigger'].forEach(function(pair) { if (!pairs.includes(pair)) fail('missing cyclic connection ' + pair); });
		    var twoSidedEdge = connectedCandidate.edges.find(function(edge) { return edge.from === 'vision_trigger' && edge.to === 'vision_driver'; });
		    if (!twoSidedEdge || twoSidedEdge.from_port !== 'left' || twoSidedEdge.to_port !== 'right') fail('chosen left/right connection sides were not retained');
		    click('[data-edge-key][data-from="result"][data-to="vision_trigger"] [data-edge-hit]', 'selectable connection');
		    var reconnectHandle = document.querySelector('[data-edge-key][data-from="result"][data-to="vision_trigger"] [data-reconnect-edge][data-edge-endpoint="to"]');
		    var reconnectTarget = port('vision_driver', 'left');
		    if (!reconnectHandle || !reconnectTarget) fail('selected connection did not expose a draggable endpoint');
		    var reconnectRect = reconnectHandle.getBoundingClientRect();
		    var reconnectTargetRect = reconnectTarget.getBoundingClientRect();
		    reconnectHandle.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, button:0, pointerId:14, clientX:reconnectRect.left+reconnectRect.width/2, clientY:reconnectRect.top+reconnectRect.height/2}));
		    reconnectTarget.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, cancelable:true, button:0, pointerId:14, clientX:reconnectTargetRect.left+reconnectTargetRect.width/2, clientY:reconnectTargetRect.top+reconnectTargetRect.height/2}));
		    connectedCandidate = JSON.parse(candidateInput.value);
		    if (!connectedCandidate.edges.some(function(edge) { return edge.from === 'result' && edge.to === 'vision_driver'; })) fail('dragging an existing endpoint did not reconnect the edge');
			    click('[data-edge-key][data-from="vision_trigger"][data-to="vision_driver"] [data-edge-hit]', 'connection to disconnect');
			    var disconnect = document.querySelector('[data-automation-disconnect-edge]');
			    if (!disconnect || disconnect.disabled) fail('selected connection did not enable the visible disconnect action');
			    disconnect.click();
			    connectedCandidate = JSON.parse(candidateInput.value);
			    if (connectedCandidate.edges.some(function(edge) { return edge.from === 'vision_trigger' && edge.to === 'vision_driver'; })) fail('visible disconnect action did not update the design');
			    var draftNode = document.querySelector('[data-node-key="vision_trigger"]');
		    var beforeX = JSON.parse(candidateInput.value).nodes[0].position.x;
		    draftNode.dispatchEvent(new KeyboardEvent('keydown', {key:'ArrowRight', bubbles:true}));
	    var afterX = JSON.parse(candidateInput.value).nodes[0].position.x;
	    if (afterX !== beforeX + 10) fail('keyboard node movement did not persist into candidate JSON');
	    var draftSVG = document.querySelector('[data-automation-draft-canvas] [data-automation-canvas]');
	    var beforeZoom = draftSVG.getAttribute('viewBox');
	    var ordinaryWheel = new WheelEvent('wheel', {bubbles:true, cancelable:true, deltaY:100});
	    draftSVG.dispatchEvent(ordinaryWheel);
	    if (ordinaryWheel.defaultPrevented) fail('ordinary vertical wheel scrolling was consumed by the graph');
	    if (draftSVG.getAttribute('viewBox') !== beforeZoom) fail('ordinary vertical wheel scrolling changed graph zoom');
	    var pinchWheel = new WheelEvent('wheel', {bubbles:true, cancelable:true, deltaY:-100, ctrlKey:true});
	    draftSVG.dispatchEvent(pinchWheel);
	    if (!pinchWheel.defaultPrevented) fail('pinch-style wheel zoom was not consumed by the graph');
	    if (draftSVG.getAttribute('viewBox') === beforeZoom) fail('pinch-style wheel zoom did not change viewBox');
	    var afterPinchZoom = draftSVG.getAttribute('viewBox');
	    click('[data-automation-draft-canvas] [data-automation-zoom-in]', 'builder zoom in');
	    if (draftSVG.getAttribute('viewBox') === afterPinchZoom) fail('builder zoom control did not change viewBox');
	    click('[data-automation-draft-canvas] [data-automation-reset]', 'builder reset layout');
	    if (JSON.parse(candidateInput.value).nodes[0].position.x !== beforeX) fail('builder reset did not restore canonical position');
	    click('[data-node-key="result"] [data-delete-node]', 'node delete control');
	    var afterNodeDelete = JSON.parse(candidateInput.value);
	    if (afterNodeDelete.nodes.some(function(node) { return node.key === 'result'; })) fail('node delete control did not remove the node');
	    if (afterNodeDelete.edges.some(function(edge) { return edge.from === 'result' || edge.to === 'result'; })) fail('node deletion left connected edges behind');
	    await report('pass', '');  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	style := `<style>
	:root { --bc: 0.746477 0.0216 264.436; --b2: 0.253267 0.015896 252.417; --p: 0.6569 0.196 275.75; --er: 0.7176 0.221 22.18; --wa: 0.8471 0.199 83.87; --in: 0.7206 0.191 231.6; --su: 0.648 0.15 160; }
	.hidden { display: none !important; }
	</style>`
	browserResult := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Header.Get("HX-Request") == "true" {
			select {
			case browserResult <- "progress:server-" + r.Method + "-" + r.URL.Path:
			default:
			}
		}
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/automations":
			fragment := renderPortfolio()
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(fragment))
				return
			}
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><script src="/htmx-2.0.4.min.js"></script><script>%s</script>%s%s</head><body><main id="main-content" hx-history-elt>%s</main></body></html>`, navigationScript, style, runner, fragment)
		case "/automations/automation-a":
			_, _ = w.Write([]byte(renderLive("automation-a", "Automation A")))
		case "/automations/automation-a/history":
			_, _ = w.Write([]byte(renderHistory("automation-a", "Automation A")))
		case "/automations/automation-a/definition":
			_, _ = w.Write([]byte(renderDefinition("automation-a", "Automation A")))
		case "/automations/automation-b":
			_, _ = w.Write([]byte(renderLive("automation-b", "Automation B")))
		case "/automations/automation-blank/drafts/version-blank":
			_, _ = w.Write([]byte(renderBlankBuilder(r.Method == http.MethodPost)))
		case "/automations/automation-draft/drafts/version-draft":
			_, _ = w.Write([]byte(renderBuilder()))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "automation-navigation.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "automation-navigation-profile"),
		server.URL+"/automations?project_id="+projectID,
	)
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}

	var outcome string
	lastProgress := "none"
	deadline := time.After(20 * time.Second)
	for outcome == "" {
		select {
		case result := <-browserResult:
			if strings.HasPrefix(result, "progress:") {
				lastProgress = strings.TrimPrefix(result, "progress:")
				continue
			}
			outcome = result
		case <-deadline:
			outcome = "fail:timed out waiting for browser result; last progress=" + lastProgress
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 4000 {
			stderr = stderr[len(stderr)-4000:]
		}
		t.Fatalf("Automation browser regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
