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

func TestAutomationPortfolioUsesSearchableSingleColumnCards(t *testing.T) {
	cards := []models.AutomationCard{
		{
			Automation: models.Automation{
				ID:             "automation-native",
				Name:           "Native Delivery",
				Description:    "Deliver approved suggestions",
				LifecycleState: models.AutomationActive,
				HealthState:    models.AutomationHealthHealthy,
			},
			Version: models.AutomationVersion{Version: 3, AdapterKey: "native_sdlc"},
		},
		{
			Automation: models.Automation{
				ID:             "automation-paused",
				Name:           "Paused Delivery",
				LifecycleState: models.AutomationPaused,
				HealthState:    models.AutomationHealthHealthy,
			},
			Version: models.AutomationVersion{Version: 1, AdapterKey: "custom"},
		},
	}

	var out bytes.Buffer
	if err := AutomationsContent(cards, "project-search").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation portfolio: %v", err)
	}
	body := out.String()

	for _, want := range []string{
		`id="automations-container"`,
		`data-search-container`,
		`data-card-search="automations"`,
		`placeholder="Search automations..."`,
		`data-search-no-results`,
		`class="grid grid-cols-1 gap-4`,
		`class="card bg-base-100 shadow-sm border border-base-300 cursor-pointer hover:border-primary/40 hover:shadow-md transition-all w-full min-w-0 max-w-full"`,
		`class="card-body relative"`,
		`class="absolute top-4 right-4"`,
		`data-automation-card-action`,
		`onclick="event.stopPropagation()"`,
		`class="dropdown dropdown-end"`,
		`onclick="handleDropdownToggle(event)"`,
		`data-automation-card-edit="automation-native"`,
		`data-automation-edit-url="/automations/automation-native/builder?project_id=project-search"`,
		`type="button" class="w-full" data-automation-card-edit="automation-native"`,
		`onclick="event.stopPropagation(); openAutomationCardEdit(this)"`,
		`data-automation-card-delete="automation-native"`,
		`class="text-error"`,
		`id="delete-automation-card-modal"`,
		`id="delete-automation-card-name"`,
		`id="delete-automation-card-form"`,
		`data-automation-delete-url="/automations/automation-native/delete?project_id=project-search"`,
		`data-automation-card-pause="automation-native"`,
		`hx-post="/automations/automation-native/pause?project_id=project-search"`,
		`data-automation-lifecycle-form="pause-automation-card-form-automation-native"`,
		`id="pause-automation-card-form-automation-native" class="hidden" method="post" action="/automations/automation-native/pause?project_id=project-search"`,
		`data-automation-card-resume="automation-paused"`,
		`hx-post="/automations/automation-paused/resume?project_id=project-search"`,
		`data-automation-lifecycle-form="resume-automation-card-form-automation-paused"`,
		`id="resume-automation-card-form-automation-paused" class="hidden" method="post" action="/automations/automation-paused/resume?project_id=project-search"`,
		`class="pr-12 min-w-0 max-w-full"`,
		`class="font-bold"`,
		`class="text-sm opacity-60 mt-1 line-clamp-2"`,
		`data-search-card`,
		`data-search-text="Native Delivery Deliver approved suggestions native_sdlc active healthy"`,
		`onclick="event.stopPropagation(); openAutomationCardDelete(this)"`,
		`onsubmit="event.preventDefault(); event.stopImmediatePropagation(); window.openVibelySubmitNavigate(this); return false;"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected compact searchable Automation portfolio to contain %q", want)
		}
	}
	for _, forbidden := range []string{
		"md:grid-cols-2",
		"xl:grid-cols-3",
		`class="truncate text-lg font-semibold"`,
		`class="card-body min-w-0 p-5"`,
		`data-automation-card-edit="automation-native" type="submit"`,
		`data-automation-card-edit="automation-native">Edit</button></form>`,
		`role="link"`,
		`focus:outline-none focus-visible:ring-2 focus-visible:ring-primary`,
		`onkeydown=`,
		"Published autonomous processes explicitly created or registered for this project.",
		"Operational work summary",
		"Last activity",
		"Next activity",
		"linked resources",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("expected compact Automation cards to omit %q", forbidden)
		}
	}
}

func TestAutomationLiveLinksOnlyTaskBackedNodesAndOmitsAuxiliarySurfaces(t *testing.T) {
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{ID: "automation-live", Name: "Live only", LifecycleState: models.AutomationActive},
		Version:    models.AutomationVersion{ID: "saved-snapshot"},
		Nodes: []models.AutomationLiveNode{
			{AutomationNode: models.AutomationNode{ID: "schedule-node", NodeKey: "schedule", Name: "Daily review", NodeType: models.AutomationNodeTrigger}, DisplayState: "idle"},
			{AutomationNode: models.AutomationNode{ID: "task-node", NodeKey: "task", Name: "Follow up", NodeType: models.AutomationNodeAgentTask}, DisplayState: "running"},
			{AutomationNode: models.AutomationNode{ID: "action-node", NodeKey: "notify", Name: "Notify", NodeType: models.AutomationNodeAction}, DisplayState: "idle"},
			{AutomationNode: models.AutomationNode{ID: "unbound-task-node", NodeKey: "unbound", Name: "Unbound task", NodeType: models.AutomationNodeAgentTask}, DisplayState: "idle"},
		},
		Resources: []models.AutomationResourceSummary{
			{NodeID: "schedule-node", ResourceType: "schedule", ResourceID: "schedule-row"},
			{NodeID: "schedule-node", ResourceType: "task", ResourceID: "scheduled-task"},
			{NodeID: "task-node", ResourceType: "task", ResourceID: "follow-up-task"},
		},
		RecentCutoff: time.Unix(1, 0),
	}

	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live: %v", err)
	}
	body := out.String()

	for _, want := range []string{
		`data-automation-live-node="schedule-node"`,
		`data-automation-live-node="task-node"`,
		`data-automation-live-node="action-node"`,
		`data-automation-live-node="unbound-task-node"`,
		`href="/tasks/scheduled-task?project_id=project-live"`,
		`href="/tasks/follow-up-task?project_id=project-live"`,
		`data-refresh-url="/automations/automation-live?project_id=project-live"`,
		`window.openVibelyAutomationLiveRefresh = function(method, url)`,
		`X-OpenVibely-Automation-Live-Generation`,
		`htmx:beforeSwap`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected simplified Automation Live to contain %q", want)
		}
	}
	if got := strings.Count(body, `data-automation-task-link`); got != 2 {
		t.Errorf("expected exactly two task-backed node links, got %d", got)
	}
	for _, forbidden := range []string{
		`id="automation-node-resources"`,
		`Node resources`,
		`/nodes/`,
		`data-automation-view="history"`,
		`aria-label="Automation views"`,
		`xl:grid-cols-[minmax(0,1fr)_22rem]`,
		`hx-trigger="every 20s`,
		`htmx.trigger(root, 'automation-visible')`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("expected simplified Automation Live to omit %q", forbidden)
		}
	}
}

func TestAutomationLiveRunNowIsActiveOnly(t *testing.T) {
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{ID: "automation-live-run", Name: "Run controls", LifecycleState: models.AutomationActive},
	}

	var active bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-run", true).Render(context.Background(), &active); err != nil {
		t.Fatalf("render active Automation Live: %v", err)
	}
	activeBody := active.String()
	for _, want := range []string{
		`action="/automations/automation-live-run/run-now?project_id=project-live-run"`,
		`data-automation-live-run-now="automation-live-run"`,
		`>Run now</button>`,
	} {
		if !strings.Contains(activeBody, want) {
			t.Errorf("expected active Automation Live actions to contain %q", want)
		}
	}

	graph.Automation.LifecycleState = models.AutomationPaused
	var paused bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-run", true).Render(context.Background(), &paused); err != nil {
		t.Fatalf("render paused Automation Live: %v", err)
	}
	if strings.Contains(paused.String(), `/run-now`) || strings.Contains(paused.String(), `>Run now</button>`) {
		t.Error("paused Automation Live must not offer Run now")
	}
}

func TestAutomationLiveControlsOverlayGraphViewport(t *testing.T) {
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{ID: "automation-live-controls", Name: "Viewport controls", LifecycleState: models.AutomationActive},
	}

	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-controls", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live: %v", err)
	}
	body := out.String()
	viewportStart := strings.Index(body, `role="region" aria-label="Live automation graph"`)
	if viewportStart < 0 {
		t.Fatal("expected Live graph viewport")
	}
	viewportEnd := strings.Index(body[viewportStart:], `</svg></div>`)
	if viewportEnd < 0 {
		t.Fatal("expected Live graph viewport end")
	}
	viewport := body[viewportStart : viewportStart+viewportEnd]
	for _, want := range []string{
		`data-automation-live-viewport-controls`,
		`data-automation-zoom-out`,
		`data-automation-zoom-in`,
		`data-automation-fit`,
	} {
		if !strings.Contains(viewport, want) {
			t.Errorf("expected Live graph viewport to contain %q", want)
		}
		if got := strings.Count(body, " "+want); got != 1 {
			t.Errorf("expected exactly one Live graph control attribute %q, got %d", want, got)
		}
	}
	if strings.Index(viewport, `data-automation-live-viewport-controls`) > strings.Index(viewport, `<svg`) {
		t.Error("expected Live graph controls to overlay the viewport outside the SVG")
	}
}

func TestAutomationLiveCanvasFillsAvailableHeight(t *testing.T) {
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{ID: "automation-live-height", Name: "Full height", LifecycleState: models.AutomationActive},
	}

	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-height", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live: %v", err)
	}
	body := out.String()

	for _, want := range []string{
		`id="automation-live" class="flex h-full min-w-0 max-w-full flex-col overflow-y-auto"`,
		`class="rounded-box border border-base-300 bg-base-100 p-4 min-w-0 flex flex-1 flex-col" data-automation-readonly-canvas`,
		`class="automation-canvas-shell relative min-h-[34rem] w-full flex-1 overflow-hidden rounded-box border border-base-300 bg-base-200/20"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected Automation Live viewport-filling layout to contain %q", want)
		}
	}
	if strings.Contains(body, `automation-canvas-shell h-[34rem]`) {
		t.Error("Automation Live canvas must grow beyond the old fixed 34rem height")
	}
}

func TestAutomationBuilderControlsOverlayGraphViewport(t *testing.T) {
	candidate := models.AutomationDraftCandidate{
		SchemaVersion: 1, Name: "Builder controls", AutomationType: "custom", AdapterKey: "custom",
	}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-builder-controls").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation builder: %v", err)
	}
	body := out.String()
	viewportStart := strings.Index(body, `role="region" aria-label="Automation graph builder"`)
	if viewportStart < 0 {
		t.Fatal("expected Automation builder graph viewport")
	}
	viewportEnd := strings.Index(body[viewportStart:], `</section>`)
	if viewportEnd < 0 {
		t.Fatal("expected Automation builder graph viewport end")
	}
	viewport := body[viewportStart : viewportStart+viewportEnd]
	for _, want := range []string{
		`data-automation-builder-viewport-controls`,
		`data-automation-zoom-out`,
		`data-automation-zoom-in`,
		`data-automation-fit`,
		`data-automation-reset`,
	} {
		if !strings.Contains(viewport, want) {
			t.Errorf("expected Automation builder graph viewport to contain %q", want)
		}
		if got := strings.Count(body, " "+want); got != 1 {
			t.Errorf("expected exactly one Automation builder graph control attribute %q, got %d", want, got)
		}
	}
	if strings.Index(viewport, `data-automation-builder-viewport-controls`) > strings.Index(viewport, `<svg`) {
		t.Error("expected Automation builder graph controls to overlay the viewport outside the SVG")
	}
}

func TestAutomationBuilderFixesGitHubImplementationCategoryToActive(t *testing.T) {
	candidate := models.AutomationDraftCandidate{
		SchemaVersion: 1, Name: "GitHub SDLC", AutomationType: "github_sdlc", AdapterKey: "github_sdlc",
		Nodes: []models.AutomationDraftNode{
			{Key: "dev_inbox", Name: "Dev Inbox", Type: models.AutomationNodeTrigger, Role: "github_inbox", Position: &models.AutomationDraftPoint{X: 0, Y: 0}, Config: map[string]any{
				"prompt": "Poll assigned issues.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "hours", "repeat_interval": 1, "enabled": true,
			}},
			{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "implementation", Position: &models.AutomationDraftPoint{X: 220, Y: 0}, Config: map[string]any{
				"prompt": "Implement the issue.", "category": "active", "priority": 2,
			}}},
	}
	var out bytes.Buffer
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}}
	if err := AutomationBuilderContent(page, "project-github-category").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation builder: %v", err)
	}
	body := out.String()
	selectMarkup := func(name string) string {
		t.Helper()
		marker := `name="` + name + `"`
		position := strings.Index(body, marker)
		if position < 0 {
			t.Fatalf("category control %q not found", name)
		}
		start := strings.LastIndex(body[:position], "<select")
		endOffset := strings.Index(body[position:], "</select>")
		if start < 0 || endOffset < 0 {
			t.Fatalf("category control %q is malformed", name)
		}
		return body[start : position+endOffset+len("</select>")]
	}

	if strings.Contains(body, `name="node_implementation_category"`) {
		t.Fatalf("GitHub configuration-only Implementation category must not be editable")
	}
	if !strings.Contains(body, ">Active</span>") {
		t.Fatalf("GitHub configuration-only Implementation category must render as Active")
	}
	devInboxCategory := selectMarkup("node_dev_inbox_category")
	if !strings.Contains(devInboxCategory, `value="scheduled"`) {
		t.Fatalf("maintained Schedule node must retain its scheduled category: %s", devInboxCategory)
	}
}

func TestAutomationBuilderRendersDirectionalPortsAndScheduleWording(t *testing.T) {
	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Directional ports",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{
			{Key: "daily", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
			{Key: "followup", Name: "Follow-up", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 260, Y: 0}},
		},
	}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-directional-ports").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation builder: %v", err)
	}
	body := out.String()

	for _, want := range []string{
		`data-port-kind="input"`,
		`data-port-kind="output"`,
		`aria-label="Input for Daily review"`,
		`aria-label="Output from Daily review"`,
		`<span>Schedule</span>`,
		`Drag from a node's right handle to another node's left handle`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected directional Automation builder to contain %q", want)
		}
	}
	for _, forbidden := range []string{`>IN</text>`, `>OUT</text>`, `automation-port-label`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("directional handles must not render redundant visible labels %q", forbidden)
		}
	}
	if strings.Contains(body, `<span>Trigger</span>`) {
		t.Error("Schedule nodes must not use stale Trigger wording")
	}
}

func TestAutomationBuilderDeleteControlsUseCenteredSVGGeometry(t *testing.T) {
	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Centered delete controls",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{
			{Key: "first", Name: "First", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
			{Key: "second", Name: "Second", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 260, Y: 0}},
		},
		Edges: []models.AutomationDraftEdge{{Key: "first_second", From: "first", To: "second"}},
	}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-delete-controls").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation builder: %v", err)
	}
	body := out.String()

	const centeredPath = `M-4 -4 L4 4 M4 -4 L-4 4`
	if got := strings.Count(body, `d="`+centeredPath+`" class="automation-delete-x"`); got != 3 {
		t.Errorf("expected two node and one edge centered delete paths, got %d", got)
	}
	if !strings.Contains(body, `svgElement('path', {d: '`+centeredPath+`', class: 'automation-delete-x'})`) {
		t.Error("dynamic edge controls must use the same centered SVG delete path")
	}
	for _, forbidden := range []string{`dominant-baseline="central"`, `>×</text>`, `x.textContent = '×'`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("delete controls must not use font-dependent glyph geometry %q", forbidden)
		}
	}
}

func TestAutomationGraphAndNavigationInChrome(t *testing.T) {
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
		{Automation: models.Automation{ID: "automation-visual", Name: "Visual Automation", Description: "Published design", LifecycleState: models.AutomationActive}, Version: models.AutomationVersion{ID: "version-visual", Version: 1, State: models.AutomationVersionPublished, AdapterKey: "vision_driver"}},
	}
	setAutomationLifecycle := func(id string, state models.AutomationLifecycleState) {
		for i := range cards {
			if cards[i].Automation.ID == id {
				cards[i].Automation.LifecycleState = state
				return
			}
		}
		t.Fatalf("missing Automation card %q", id)
	}
	renderPortfolio := func() string {
		var out bytes.Buffer
		if err := AutomationsContent(cards, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation portfolio: %v", err)
		}
		return out.String()
	}
	renderDescribeFailure := func(description string) string {
		var out bytes.Buffer
		if err := AutomationDescribeModalContent(projectID, description, "Could not generate a supported Automation: unsupported price-result branch").Render(context.Background(), &out); err != nil {
			t.Fatalf("render Describe failure modal: %v", err)
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
		if err := AutomationLiveContent(graph, projectID, true).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation live graph: %v", err)
		}
		return out.String()
	}

	renderBlankBuilder := func(withNode bool, automationID string) string {
		candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Blank Automation", AutomationType: "custom", AdapterKey: "custom"}
		if withNode {
			candidate.Nodes = []models.AutomationDraftNode{
				{Key: "first_step", Name: "First step", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Describe the work this node should perform.", "category": "scheduled", "priority": 2}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
				{Key: "second_step", Name: "Second step", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Continue the work.", "category": "scheduled", "priority": 2}, Position: &models.AutomationDraftPoint{X: 260, Y: 0}},
				{Key: "third_step", Name: "Third step", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 520, Y: 0}},
			}
		}
		page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}, AutomationID: automationID}
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
		edge := models.AutomationDraftEdge{Key: "trigger_to_driver", From: "vision_trigger", To: "vision_driver", FromPort: "left", ToPort: "right"}
		candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Visual Draft", AutomationType: "vision_driver", AdapterKey: "vision_driver", Nodes: []models.AutomationDraftNode{trigger, driver, result}, Edges: []models.AutomationDraftEdge{edge}}
		page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}, AutomationID: "automation-visual", NodePalette: []models.AutomationDraftNode{trigger, driver}, EdgePalette: []models.AutomationDraftEdge{edge}}
		var out bytes.Buffer
		if err := AutomationBuilderContent(page, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation builder: %v", err)
		}
		return out.String()
	}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  window.handleDropdownToggle = window.handleDropdownToggle || function(event) { event.stopPropagation(); };
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
  function mouseClick(selector, label) {
    var element = document.querySelector(selector);
    if (!element) fail('missing ' + label);
    element.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, button: 0}));
    if (typeof element.focus === 'function') element.focus();
    element.dispatchEvent(new MouseEvent('mouseup', {bubbles: true, cancelable: true, button: 0}));
    element.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true, button: 0}));
  }
  function clickMenuRowRightEdge(selector, label) {
    var control = document.querySelector(selector);
    if (!control) fail('missing ' + label);
    control.scrollIntoView({block: 'center', inline: 'center'});
    var row = control.closest('li');
    var rect = row && row.getBoundingClientRect();
    if (!rect || rect.width < 40) fail(label + ' has no usable menu-row bounds');
    var x = rect.right - 8;
    var y = rect.top + rect.height / 2;
    var hit = document.elementFromPoint(x, y);
    if (!hit) fail(label + ' right edge hit nothing');
    hit.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y}));
    hit.dispatchEvent(new MouseEvent('mouseup', {bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y}));
    hit.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y}));
  }
  function liveID() { var root = document.getElementById('automation-live'); return root && root.dataset.automationId; }
  function portfolioReady() { return !!document.getElementById('automations-container'); }
  function assertAutomationBreadcrumb(rootSelector, name, finalLabel) {
    var breadcrumb = document.querySelector(rootSelector + ' [data-automation-breadcrumb]');
    if (!breadcrumb) fail(rootSelector + ' is missing Automation breadcrumb navigation');
    var links = breadcrumb.querySelectorAll('a');
    if (!links.length || links[0].textContent.trim() !== 'Automations') fail(rootSelector + ' breadcrumb does not start with Automations');
    if (!breadcrumb.textContent.includes(name)) fail(rootSelector + ' breadcrumb is missing Automation name ' + name);
    if (finalLabel && !breadcrumb.textContent.includes(finalLabel)) fail(rootSelector + ' breadcrumb is missing current page ' + finalLabel);
    if (breadcrumb.textContent.includes('←')) fail(rootSelector + ' still renders back-button navigation');
  }
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
    assertAutomationBreadcrumb('#automation-live', 'Automation A', '');
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
    if (document.querySelector('[data-automation-view]')) fail('Live still exposes redundant Automation tabs');
	    if (document.getElementById('automation-node-resources')) fail('Live still exposes the node resources sidebar');
	    window.openVibelyAutomationLiveRefresh('GET', '/automations/automation-a?project_id=project-browser&refresh_order=older');
	    window.openVibelyAutomationLiveRefresh('GET', '/automations/automation-a?project_id=project-browser&refresh_order=newer');
	    await waitFor(function() { return document.getElementById('automation-live') && document.getElementById('automation-live').textContent.includes('Newest Live response'); }, 'newest concurrent Live response');
	    await fetch('/release-older-live-response', {method: 'POST'});
	    await wait(350);
	    if (!document.getElementById('automation-live').textContent.includes('Newest Live response')) fail('an older concurrent Live response overwrote the newest state');
	    await report('progress', 'live-refresh-order-guarded');
	    click('#automation-live [data-automation-breadcrumb] a[href^="/automations?"]', 'Automations breadcrumb from Live');
    await waitFor(portfolioReady, 'portfolio after Live breadcrumb');
    await report('progress', 'live-in-page-back-restored');
	    var automationBSelector = '[data-automation-url^="/automations/automation-b?"]';
	    click(automationBSelector + ' [data-automation-card-action] label', 'Automation B kebab menu');
	    if (!portfolioReady()) fail('opening the Automation card menu navigated away from the portfolio');
	    click(automationBSelector + ' [data-automation-card-delete]', 'Automation B Delete menu action');
	    var cardDeleteModal = document.getElementById('delete-automation-card-modal');
	    var cardDeleteForm = document.getElementById('delete-automation-card-form');
	    if (!cardDeleteModal || !cardDeleteModal.open) fail('Automation card Delete did not open its confirmation dialog');
	    if (document.getElementById('delete-automation-card-name').textContent !== 'Automation B') fail('Automation card Delete confirmation has the wrong name');
	    if (!cardDeleteForm || cardDeleteForm.getAttribute('action') !== '/automations/automation-b/delete?project_id=project-browser') fail('Automation card Delete confirmation has the wrong project-scoped action');
	    click('#delete-automation-card-modal button[aria-label="Close delete automation confirmation"]', 'Automation card delete modal close button');
	    if (cardDeleteModal.open) fail('Automation card delete modal close button did not close the dialog');
	    click(automationBSelector, 'Automation B card after Live return');
	    await waitFor(function() { return liveID() === 'automation-b'; }, 'Automation B after Live return');
	    await report('progress', 'automation-b-clicked');

    click('#automation-live [data-automation-breadcrumb] a[href^="/automations?"]', 'Automations breadcrumb');
    await report('progress', 'in-page-back-clicked');
    await waitFor(portfolioReady, 'portfolio after in-page back link');
    await report('progress', 'in-page-back-restored');
	    await wait(100);
		    var automationASelector = '[data-automation-url^="/automations/automation-a?"]';
		click(automationASelector + ' [data-automation-card-action] label', 'Automation A kebab menu before Pause');
		clickMenuRowRightEdge(automationASelector + ' [data-automation-card-pause]', 'Automation A Pause menu row');
		await waitFor(function() { return !!document.querySelector(automationASelector + ' [data-automation-card-resume]'); }, 'Automation A Resume after right-edge Pause click');
		await wait(50);
		click(automationASelector + ' [data-automation-card-action] label', 'Automation A kebab menu before Resume');
		clickMenuRowRightEdge(automationASelector + ' [data-automation-card-resume]', 'Automation A Resume menu row');
		await waitFor(function() { return !!document.querySelector(automationASelector + ' [data-automation-card-pause]'); }, 'Automation A Pause after right-edge Resume click');
		    await report('progress', 'automation-lifecycle-full-row-clicked');
		    click(automationASelector + ' [data-automation-card-action] label', 'Automation A kebab menu');
		    var originalHTMXAjax = window.htmx.ajax;
		    var cardEditRequests = 0;
		    window.htmx.ajax = function(method, url, options) {
		      if (String(method).toUpperCase() === 'POST' && String(url).indexOf('/automations/automation-a/builder') === 0) cardEditRequests++;
		      return originalHTMXAjax.call(this, method, url, options);
		    };
		    mouseClick(automationASelector + ' [data-automation-card-edit]', 'Automation A Edit menu action');
		    await waitFor(function() { return !!document.getElementById('automation-builder'); }, 'Automation A builder from first card Edit click');
		    window.htmx.ajax = originalHTMXAjax;
		    if (cardEditRequests !== 1) fail('first Automation card Edit click issued ' + cardEditRequests + ' edit requests');
	    await window.openVibelyNavigate('/automations?project_id=project-browser');
	    await waitFor(portfolioReady, 'portfolio after Automation card Edit');
	    click(automationASelector, 'Automation A card after in-page back');
	    await report('progress', 'automation-a-reclicked');
    await waitFor(function() { return liveID() === 'automation-a'; }, 'Automation A after in-page back');
    await report('progress', 'automation-a-ready-before-edit');

    click('#automation-live button[onclick*="delete-automation-modal"][onclick*="showModal"]', 'Live Automation Delete');
    var liveDeleteModal = document.querySelector('#automation-live #delete-automation-modal');
    if (!liveDeleteModal || !liveDeleteModal.open) fail('Live Automation Delete did not open its confirmation dialog');
    click('#automation-live #delete-automation-modal button[aria-label="Close delete automation confirmation"]', 'Live Automation delete modal close button');
    if (liveDeleteModal.open) fail('Live Automation delete modal close button did not close the dialog');

    click('#automation-live form[hx-post*="/builder"] button[type="submit"]', 'Edit automation');
    await waitFor(function() { return !!document.getElementById('automation-builder'); }, 'builder after Edit automation');
    if (document.getElementById('automation-live')) fail('live Automation root remained mounted behind the editor');
    click('#automation-builder [data-delete-automation-open]', 'Edit Automation Delete');
    var editDeleteModal = document.querySelector('#automation-builder #delete-automation-modal');
    if (!editDeleteModal || !editDeleteModal.open) fail('Edit Automation Delete did not open its confirmation dialog');
    click('#automation-builder #delete-automation-modal button[aria-label="Close delete automation confirmation"]', 'Edit Automation delete modal close button');
    if (editDeleteModal.open) fail('Edit Automation delete modal close button did not close the dialog');
	    var editedCanvas = document.querySelector('#automation-builder [data-automation-draft-canvas]');
	    if (!editedCanvas) fail('Edit automation did not render the custom canvas');
	    click('#automation-builder [data-automation-add-node-open]', 'Add node after Edit automation');    var editedNodeDialog = document.querySelector('#automation-builder [data-automation-node-dialog]');
    if (!editedNodeDialog || !editedNodeDialog.open) fail('Add node is inoperable after the Edit automation HTMX transition');
    editedNodeDialog.close();
    await report('progress', 'edit-automation-builder-operable');

	    await window.openVibelyNavigate('/automations?project_id=project-browser');
	    await waitFor(portfolioReady, 'portfolio before new Automation menu');
	    click('[data-automation-new-menu] button[data-automation-new-template]', 'Template creation menu option');
	    var templateModal = document.getElementById('automation-template-modal');
	    if (!templateModal || !templateModal.open) fail('Template option did not open its modal');
	    var templateSelect = templateModal.querySelector('select[name="template_key"]');
	    var templateDescription = templateModal.querySelector('[data-template-description-card]');
	    var nativeDescription = templateDescription && templateDescription.textContent;
	    var templateLabels = Array.from(templateSelect.options).map(function(option) { return option.textContent; });
	    templateSelect.value = 'github_sdlc';
	    templateSelect.dispatchEvent(new Event('change', {bubbles:true}));
	    if (!templateDescription || templateDescription.textContent === nativeDescription || !templateDescription.textContent.includes('GitHub')) fail('Template selection did not update its description card');
	    if (Array.from(templateSelect.options).some(function(option, index) { return option.textContent !== templateLabels[index]; })) fail('Template selection replaced an option label with its description');
	    click('#automation-template-modal button[aria-label="Close template selection"]', 'Template modal close button');
	    if (templateModal.open) fail('Template modal close button did not close the dialog');
	    click('[data-automation-new-menu] button[data-automation-new-describe]', 'Describe creation menu option');
	    var describeModal = document.getElementById('automation-describe-modal');
	    if (!describeModal || !describeModal.open) fail('Describe option did not open its modal');
	    var stockDescription = 'Monitor a stock for price increases or decreases so I can buy or sell depending on the result';
	    var describeForm = describeModal.querySelector('form[hx-post]');
	    if (!describeForm) fail('Describe form is missing');
	    describeForm.querySelector('textarea[name="description"]').value = stockDescription;
	    htmx.process(describeForm);
	    describeForm.requestSubmit(describeForm.querySelector('button[type="submit"]'));
	    await waitFor(function() { return !!document.querySelector('#automation-describe-modal [role="alert"]'); }, 'visible Describe failure in modal');
	    var describeError = document.querySelector('#automation-describe-modal [role="alert"]');
	    if (!describeError.textContent.includes('Could not generate a supported Automation')) fail('Describe failure did not explain that generation failed');
	    var retainedDescription = document.querySelector('#automation-describe-modal textarea[name="description"]');
	    if (!retainedDescription || retainedDescription.value !== stockDescription) fail('Describe failure discarded the submitted description');
	    await report('progress', 'describe-failure-visible');
	    click('#automation-describe-modal button[aria-label="Close Automation description"]', 'Describe modal close button');
	    if (describeModal.open) fail('Describe modal close button did not close the dialog');
	    click('[data-automation-new-menu] button[data-automation-new-custom]', 'Custom creation menu option');
	    await waitFor(function() { return !!document.querySelector('[data-automation-add-first-node]'); }, 'empty Custom Automation canvas');
	    await report('progress', 'blank-canvas-loaded');
	    click('[data-automation-add-first-node]', 'Add first node action');
	    var nodeDialog = document.querySelector('[data-automation-node-dialog]');
	    if (!nodeDialog || !nodeDialog.open) fail('Add first node did not open the node dialog');
	    var purposes = Array.from(nodeDialog.querySelectorAll('[name="node_kind"] option')).map(function(option) { return option.value; });
	    ['schedule', 'task', 'create_notification', 'human_approval', 'create_github_issue', 'human_assignment', 'github_inbox', 'open_pull_request', 'human_review', 'outcome'].forEach(function(purpose) {
	      if (!purposes.includes(purpose)) fail('Blank node palette is missing ' + purpose);
	    });
	    await report('progress', 'add-node-dialog-opened');
	    nodeDialog.querySelector('[name="node_name"]').value = 'First step';
	    nodeDialog.querySelector('[name="node_kind"]').value = 'task';
	    var nodeForm = nodeDialog.querySelector('form[hx-post]');
	    if (!nodeForm || !nodeForm.checkValidity()) fail('Add node form is not submittable: ' + (nodeDialog.querySelector(':invalid') && nodeDialog.querySelector(':invalid').validationMessage));
	    htmx.process(nodeForm);
	    nodeForm.requestSubmit(nodeDialog.querySelector('[data-automation-create-node]'));
	    await report('progress', 'add-node-submitted');
	    await waitFor(function() { return !!document.querySelector('[data-node-key="first_step"]'); }, 'new node on blank canvas');
	    var blankCandidateInput = document.querySelector('[data-automation-draft-form] [data-candidate-json]');
	    dragCapturedConnection('first_step', 'right', 'second_step', 'left', 10);
	    var blankCandidate = JSON.parse(blankCandidateInput.value);
	    if (!(blankCandidate.edges || []).some(function(edge) { return edge.from === 'first_step' && edge.to === 'second_step'; })) fail('captured pointer release over another Blank node did not create a connection');
	    var blankNode = document.querySelector('[data-node-key="first_step"]');
	    var foregroundEdge = document.querySelector('[data-edge-foreground][data-edge-key]');
	    if (!foregroundEdge) fail('Blank connection has no visible foreground edge');
	    if (!(blankNode.compareDocumentPosition(foregroundEdge) & Node.DOCUMENT_POSITION_FOLLOWING)) fail('Blank connection line is rendered behind graph nodes');
	    if (getComputedStyle(foregroundEdge).pointerEvents !== 'none') fail('foreground connection line blocks node connectors');
	    function colorAlpha(value) {
	      var slash = value.match(/\/\s*([0-9.]+)(%)?\s*\)/);
	      if (slash) return Number(slash[1]) / (slash[2] ? 100 : 1);
	      var rgba = value.match(/^rgba\([^,]+,[^,]+,[^,]+,\s*([0-9.]+)\)$/);
	      return rgba ? Number(rgba[1]) : 1;
	    }
	    if (colorAlpha(getComputedStyle(foregroundEdge).stroke) < 1) fail('foreground connection line is translucent and looks behind graph nodes');
	    var foregroundArrow = document.querySelector('#automation-draft-arrow .automation-graph-arrow');
		    if (!foregroundArrow || colorAlpha(getComputedStyle(foregroundArrow).fill) < 1) fail('foreground connection arrow is translucent and looks behind connector circles');
		    click('[data-edge-key][data-from="first_step"][data-to="second_step"] [data-edge-hit]', 'Blank connection for delete control layering');
		    var blankDeleteControl = document.querySelector('[data-edge-controls][data-edge-key] [data-delete-edge]');
		    if (!blankDeleteControl) fail('selected Blank connection has no midpoint delete control overlay');
		    if (!(foregroundEdge.compareDocumentPosition(blankDeleteControl) & Node.DOCUMENT_POSITION_FOLLOWING)) fail('connection delete control is painted behind the foreground line');
		    var blankDeleteRect = blankDeleteControl.getBoundingClientRect();
		    var blankDeleteHit = document.elementFromPoint(blankDeleteRect.left + blankDeleteRect.width / 2, blankDeleteRect.top + blankDeleteRect.height / 2);
		    if (!blankDeleteHit || !blankDeleteHit.closest('[data-delete-edge]')) fail('connection delete control is not the topmost interactive element at its center');

		    click('#automation-builder [data-automation-breadcrumb] a[href^="/automations?"]', 'Automations breadcrumb from blank builder');
		    await waitFor(portfolioReady, 'portfolio before published Automation selection');
	    click('[data-automation-url^="/automations/automation-visual?"]', 'published visual Automation card');
	    await waitFor(function() { return liveID() === 'automation-visual'; }, 'published visual Automation');
	    click('#automation-live form[hx-post*="/builder"] button[type="submit"]', 'Edit published visual Automation');
		    await waitFor(function() { return !!document.querySelector('[data-automation-draft-canvas]'); }, 'visual builder opened from published Automation');
		    var malformedCandidateInput = document.querySelector('#automation-builder [data-automation-draft-form] [data-candidate-json]');
		    var malformedCandidate = JSON.parse(malformedCandidateInput.value);
		    var malformedEdge = (malformedCandidate.edges || []).find(function(edge) { return edge.key === 'trigger_to_driver'; });
		    if (!malformedEdge || malformedEdge.from_port !== 'left' || malformedEdge.to_port !== 'right') fail('builder silently canonicalized explicit malformed connector geometry before Save');
		    click('#automation-builder [data-edge-key="trigger_to_driver"] [data-edge-hit]', 'malformed connection preserved for strict Save');
		    click('#automation-builder [data-edge-controls][data-edge-key="trigger_to_driver"] [data-delete-edge]', 'remove malformed fixture connection');
		    if ((JSON.parse(malformedCandidateInput.value).edges || []).length !== 0) fail('malformed fixture connection was not removed before interaction coverage');
		    var automationName = document.querySelector('[name="automation_name"]');	    if (!automationName) fail('published Automation editor did not expose an Automation name field');
	    automationName.value = 'Browser Named Automation';
	    automationName.dispatchEvent(new Event('input', {bubbles:true}));
	    var namedCandidate = JSON.parse(document.querySelector('[data-automation-draft-form] [data-candidate-json]').value);
	    if (namedCandidate.name !== 'Browser Named Automation') fail('Automation name was not synchronized into the saved design');
		    var draftForm = document.querySelector('[data-automation-draft-form]');
		    if (!draftForm.querySelector('[name="save_changes"][value="true"]')) fail('visible Save form does not apply the draft immediately');
		    if (Array.from(document.querySelectorAll('button')).some(function(button) { return /review and apply|apply changes/i.test(button.textContent); })) fail('builder still exposes a second review/apply action');
		    var connectionSubmissions = 0;
		    var saveSubmission = null;
		    draftForm.addEventListener('submit', function(event) {
		      connectionSubmissions++;
		      saveSubmission = new FormData(draftForm);
		      event.preventDefault();
		      event.stopImmediatePropagation();
		    });	    function port(node, side) { return document.querySelector('[data-connect-port="' + node + '"][data-port-side="' + side + '"]'); }
	    function dragCapturedConnection(from, fromSide, to, toSide, pointerId) {
	      var sourceHandle = port(from, fromSide);
	      var targetHandle = port(to, toSide);
	      if (!sourceHandle || !targetHandle) fail('missing Blank drag connector for ' + from + ' to ' + to);
	      sourceHandle.scrollIntoView({block:'center', inline:'center'});
	      var sourceRect = sourceHandle.getBoundingClientRect();
	      var targetRect = targetHandle.getBoundingClientRect();
	      sourceHandle.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:sourceRect.left+sourceRect.width/2, clientY:sourceRect.top+sourceRect.height/2}));
	      sourceHandle.dispatchEvent(new PointerEvent('pointermove', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:targetRect.left+targetRect.width/2, clientY:targetRect.top+targetRect.height/2}));
	      var releaseHit = document.elementFromPoint(targetRect.left+targetRect.width/2, targetRect.top+targetRect.height/2);
	      var releasePort = releaseHit && releaseHit.closest('[data-connect-port]');
	      if (!releasePort || releasePort.dataset.connectPort !== to) fail('Blank release coordinates hit ' + (releaseHit ? releaseHit.tagName + '.' + releaseHit.getAttribute('class') : 'nothing') + ' instead of the destination connector');
	      sourceHandle.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:targetRect.left+targetRect.width/2, clientY:targetRect.top+targetRect.height/2}));
	    }
	    function dragConnection(from, fromSide, to, toSide, pointerId) {
	      var sourceHandle = port(from, fromSide);
	      var targetHandle = port(to, toSide);
	      if (!sourceHandle || !targetHandle) fail('missing two-sided drag connector for ' + from + ' to ' + to);
	      var sourceRect = sourceHandle.getBoundingClientRect();
	      var targetRect = targetHandle.getBoundingClientRect();
	      sourceHandle.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:sourceRect.left+sourceRect.width/2, clientY:sourceRect.top+sourceRect.height/2}));
	      targetHandle.dispatchEvent(new PointerEvent('pointermove', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:targetRect.left+targetRect.width/2, clientY:targetRect.top+targetRect.height/2}));
		      var preview = document.querySelector('[data-automation-edge-preview]');
		      if (!preview) fail('connector preview is missing while dragging');
		      if (preview.getAttribute('x1') === preview.getAttribute('x2') && preview.getAttribute('y1') === preview.getAttribute('y2')) fail('connector preview did not follow the pointer');
		      var previewStyle = getComputedStyle(preview);
		      if (preview.hasAttribute('hidden') || previewStyle.display === 'none' || preview.getClientRects().length === 0) fail('connector preview remains hidden while dragging');
		      if (previewStyle.stroke === 'none' || previewStyle.stroke === 'transparent' || previewStyle.opacity === '0') fail('connector preview has no visible stroke');
		      if (parseFloat(previewStyle.strokeWidth) < 3) fail('connector preview is too thin to see while dragging');
		      if (previewStyle.pointerEvents !== 'none') fail('connector preview can block the destination port');
		      targetHandle.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, cancelable:true, button:0, pointerId:pointerId, clientX:targetRect.left+targetRect.width/2, clientY:targetRect.top+targetRect.height/2}));	    }
		    dragConnection('vision_trigger', 'right', 'vision_driver', 'right', 11);
		    var candidateInput = document.querySelector('[data-automation-draft-form] [data-candidate-json]');
		    if ((JSON.parse(candidateInput.value).edges || []).length !== 0) fail('output-to-output drag created an Automation connection');
		    dragConnection('vision_trigger', 'right', 'vision_driver', 'left', 12);
		    dragConnection('vision_driver', 'right', 'result', 'left', 13);
		    dragConnection('result', 'right', 'vision_trigger', 'left', 14);
		    var connectedCandidate = JSON.parse(candidateInput.value);
	    if (connectionSubmissions !== 0) fail('drag connections submitted and replaced the builder fragment');
	    if (connectedCandidate.edges.length !== 3) fail('three consecutive drag connections were not retained');
	    var pairs = connectedCandidate.edges.map(function(edge) { return edge.from + '>' + edge.to; });
		    ['vision_trigger>vision_driver', 'vision_driver>result', 'result>vision_trigger'].forEach(function(pair) { if (!pairs.includes(pair)) fail('missing cyclic connection ' + pair); });
		    var directionalEdge = connectedCandidate.edges.find(function(edge) { return edge.from === 'vision_trigger' && edge.to === 'vision_driver'; });
		    if (!directionalEdge || directionalEdge.from_port !== 'right' || directionalEdge.to_port !== 'left') fail('output-to-input connection ports were not retained');
		    if (connectedCandidate.edges.some(function(edge) { return edge.from_port !== 'right' || edge.to_port !== 'left'; })) fail('a connection retained non-directional ports');
			    click('[data-edge-key][data-from="result"][data-to="vision_trigger"] [data-edge-hit]', 'selectable connection');
			    var selectedEdge = document.querySelector('[data-edge-key][data-from="result"][data-to="vision_trigger"]');
			    if (!selectedEdge) fail('selected connection group is missing');
			    selectedEdge.focus();
			    if (getComputedStyle(selectedEdge).outlineStyle !== 'none') fail('selected connection shows an unnecessary bounding outline');
			    var reconnectHandle = document.querySelector('[data-edge-controls][data-edge-key="' + CSS.escape(selectedEdge.dataset.edgeKey) + '"] [data-reconnect-edge][data-edge-endpoint="to"]');
			    var reconnectTarget = port('vision_driver', 'left');
			    if (!reconnectHandle || !reconnectTarget) fail('selected connection did not expose a draggable endpoint');		    var reconnectRect = reconnectHandle.getBoundingClientRect();
		    var reconnectTargetRect = reconnectTarget.getBoundingClientRect();
		    reconnectHandle.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, button:0, pointerId:15, clientX:reconnectRect.left+reconnectRect.width/2, clientY:reconnectRect.top+reconnectRect.height/2}));
		    reconnectTarget.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, cancelable:true, button:0, pointerId:15, clientX:reconnectTargetRect.left+reconnectTargetRect.width/2, clientY:reconnectTargetRect.top+reconnectTargetRect.height/2}));
		    connectedCandidate = JSON.parse(candidateInput.value);
		    if (!connectedCandidate.edges.some(function(edge) { return edge.from === 'result' && edge.to === 'vision_driver'; })) fail('dragging an existing endpoint did not reconnect the edge');
			    if (document.querySelector('[data-automation-disconnect-edge]')) fail('canvas still renders a dedicated Delete connection toolbar button');
			    click('[data-edge-key][data-from="vision_trigger"][data-to="vision_driver"] [data-edge-hit]', 'connection to delete with midpoint control');
			    var selectedDelete = document.querySelector('[data-edge-controls][data-edge-key="trigger_to_driver"] [data-delete-edge]');
			    if (!selectedDelete) fail('selected connection did not expose its midpoint delete control');
			    selectedDelete.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true}));
			    connectedCandidate = JSON.parse(candidateInput.value);
			    if (connectedCandidate.edges.some(function(edge) { return edge.from === 'vision_trigger' && edge.to === 'vision_driver'; })) fail('midpoint delete control did not update the design');
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
		    click('#automation-builder button[form="automation-design-form"]', 'top Save changes action');
		    if (connectionSubmissions !== 1 || !saveSubmission) fail('visible Save changes action did not submit the design form exactly once');
		    if (saveSubmission.get('save_changes') !== 'true') fail('visible Save changes action omitted immediate-apply intent');
		    var savedCandidate = JSON.parse(String(saveSubmission.get('candidate_json') || '{}'));
		    if (savedCandidate.name !== 'Browser Named Automation' || savedCandidate.nodes.some(function(node) { return node.key === 'result'; })) fail('visible Save changes action did not submit the latest graph state');
		    await report('pass', '');  })().catch(function(error) { report('fail', String(error && error.stack || error)); });});
</script>`
	style := `<style>
		:root { --bc: 0.746477 0.0216 264.436; --b2: 0.253267 0.015896 252.417; --p: 0.6569 0.196 275.75; --er: 0.7176 0.221 22.18; --wa: 0.8471 0.199 83.87; --in: 0.7206 0.191 231.6; --su: 0.648 0.15 160; }
		.hidden { display: none !important; }
		.menu { box-sizing: border-box; display: flex; width: 12rem; flex-direction: column; padding: .5rem; }
		.menu li { position: relative; display: grid; grid-template-columns: minmax(0, 1fr); }
		.menu li > * { grid-column-start: 1; grid-row-start: 1; display: grid; grid-auto-flow: column; grid-auto-columns: minmax(auto, max-content) auto max-content; align-items: center; gap: .5rem; padding: .5rem 1rem; }
		.w-full { width: 100%; }
		.dropdown-content { position: relative; }
		</style>`
	browserResult := make(chan string, 16)
	olderLiveStarted := make(chan struct{})
	releaseOlderLive := make(chan struct{})
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
			switch r.URL.Query().Get("refresh_order") {
			case "older":
				close(olderLiveStarted)
				select {
				case <-releaseOlderLive:
				case <-time.After(5 * time.Second):
					http.Error(w, "newer Live request never overlapped the older request", http.StatusGatewayTimeout)
					return
				}
				_, _ = w.Write([]byte(renderLive("automation-a", "Older Live response")))
			case "newer":
				select {
				case <-olderLiveStarted:
					_, _ = w.Write([]byte(renderLive("automation-a", "Newest Live response")))
				case <-time.After(time.Second):
					http.Error(w, "newer Live request arrived before the older request started", http.StatusConflict)
				}
			default:
				_, _ = w.Write([]byte(renderLive("automation-a", "Automation A")))
			}
		case "/release-older-live-response":
			close(releaseOlderLive)
			w.WriteHeader(http.StatusNoContent)
		case "/automations/automation-a/builder":
			_, _ = w.Write([]byte(renderBlankBuilder(true, "automation-a")))
		case "/automations/automation-a/pause":
			setAutomationLifecycle("automation-a", models.AutomationPaused)
			_, _ = w.Write([]byte(renderPortfolio()))
		case "/automations/automation-a/resume":
			setAutomationLifecycle("automation-a", models.AutomationActive)
			_, _ = w.Write([]byte(renderPortfolio()))
		case "/automations/automation-b":
			_, _ = w.Write([]byte(renderLive("automation-b", "Automation B")))
		case "/automations/builder":
			_ = r.ParseForm()
			if r.FormValue("source") == "describe" {
				w.Header().Set("HX-Retarget", "#automation-describe-modal-content")
				w.Header().Set("HX-Reswap", "outerHTML")
				_, _ = w.Write([]byte(renderDescribeFailure(r.FormValue("description"))))
				return
			}
			_, _ = w.Write([]byte(renderBlankBuilder(r.FormValue("builder_action") == "create_node", "")))
		case "/automations/automation-visual":
			_, _ = w.Write([]byte(renderLive("automation-visual", "Visual Automation")))
		case "/automations/automation-visual/builder":
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
	if err := startBrowserProcess(cmd); err != nil {
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
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 4000 {
			stderr = stderr[len(stderr)-4000:]
		}
		t.Fatalf("Automation browser regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
