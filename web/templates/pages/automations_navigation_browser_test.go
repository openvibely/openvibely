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
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
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
		`href="/tasks/scheduled-task?project_id=project-live&amp;from=automation&amp;automation_id=automation-live&amp;automation_name=Live+only"`,
		`href="/tasks/follow-up-task?project_id=project-live&amp;from=automation&amp;automation_id=automation-live&amp;automation_name=Live+only"`,
		`data-refresh-url="/automations/automation-live?project_id=project-live"`,
		`window.openVibelyAutomationLiveRefresh = function(method, url)`,
		`X-OpenVibely-Automation-Live-Generation`,
		`htmx:beforeSwap`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected simplified Automation Live to contain %q", want)
		}
	}
	if got := strings.Count(body, `<a class="automation-graph-link"`); got != 2 {
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

func TestAutomationLiveHeaderUsesStandardSpacingAndDescriptionStyle(t *testing.T) {
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{
			ID:          "automation-live-header",
			Name:        "Header spacing",
			Description: "A standard Automation description.",
		},
	}

	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-header", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live header: %v", err)
	}
	body := out.String()
	headerMarker := strings.Index(body, `data-automation-live-header`)
	cardStart := strings.Index(body, `data-automation-readonly-canvas`)
	if headerMarker < 0 || cardStart <= headerMarker {
		t.Fatal("expected one Live header block immediately before the Automation card")
	}
	headerStart := strings.LastIndex(body[:headerMarker], `<div`)
	if headerStart < 0 {
		t.Fatal("expected Live header opening element")
	}
	header := body[headerStart:cardStart]
	for _, want := range []string{
		`class="mb-6 flex flex-wrap items-start justify-between gap-3"`,
		`data-automation-live-header-actions`,
		`data-automation-live-edit`,
		`data-automation-live-menu`,
		`class="mt-1 text-sm opacity-60"`,
		`>A standard Automation description.</p>`,
	} {
		if !strings.Contains(header, want) {
			t.Errorf("expected standard Live header to contain %q", want)
		}
	}
	for _, forbidden := range []string{`class="mb-5 min-w-0"`, `text-base-content/65`} {
		if strings.Contains(header, forbidden) {
			t.Errorf("expected standard Live header to omit legacy styling %q", forbidden)
		}
	}

	graph.Automation.Description = ""
	out.Reset()
	if err := AutomationLiveContent(graph, "project-live-header", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live without description: %v", err)
	}
	emptyHeaderEnd := strings.Index(out.String(), `data-automation-readonly-canvas`)
	if emptyHeaderEnd < 0 {
		t.Fatal("expected Automation card after empty-description header")
	}
	if strings.Contains(out.String(), `>A standard Automation description.</p>`) {
		t.Error("empty Automation description must not render a description line")
	}
}

func TestAutomationBuilderEditHeaderUsesYAMLAuthoring(t *testing.T) {
	candidate := models.AutomationDraftCandidate{
		SchemaVersion: 1, Name: "Edit YAML", Description: "A YAML-authored Automation description.",
		AutomationType: "custom", AdapterKey: "custom",
	}
	page := models.AutomationBuilderPage{AutomationID: "automation-edit-yaml", Source: "edit", Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: Edit YAML\n"}

	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-edit-yaml").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Edit YAML: %v", err)
	}
	body := out.String()
	for _, want := range []string{`data-automation-yaml-builder`, `data-automation-builder-cancel`, `data-automation-builder-save`,
		`data-automation-view-switcher`, `data-automation-view-graph`, `data-automation-view-yaml`, `data-automation-view-details`,
		`data-automation-yaml-editor`, `name="automation_yaml"`, `schema_version: 1`,
		`data-automation-yaml-editor-shell`, `data-automation-yaml-line-numbers`, `data-automation-yaml-diagnostic`,
		`data-automation-graph-panel`, `data-automation-details-panel`, `data-automation-details-form`, `data-automation-node-details`, `data-automation-edge-details`,
		`min-h-[20rem] flex-1 flex-col overflow-hidden rounded-box border border-base-300 bg-base-200/20 px-0 py-4 font-mono text-sm leading-6`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected synchronized Automation Edit view to contain %q", want)
		}
	}
	for _, forbidden := range []string{`data-automation-yaml-preview`, "Automation YAML", "YAML controls node and connection configuration"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("interactive graph must omit obsolete YAML authoring chrome %q", forbidden)
		}
	}
	if !strings.Contains(body, `name="candidate_json"`) {
		t.Error("Details view must preserve the prior card-form candidate submission")
	}
	if !strings.Contains(body, `>A YAML-authored Automation description.</p>`) {
		t.Error("Edit header must retain its description")
	}
}

func TestAutomationBuilderGraphAndYAMLViewsAreNonDivergent(t *testing.T) {
	candidate := models.AutomationDraftCandidate{
		SchemaVersion: 1, Name: "YAML graph", AutomationType: "custom", AdapterKey: "custom",
		Nodes: []models.AutomationDraftNode{{Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task", Position: &models.AutomationDraftPoint{}}},
	}
	page := models.AutomationBuilderPage{AutomationID: "automation-yaml-graph", Source: "edit", Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: YAML graph\nnodes: []\n"}

	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-yaml-graph").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation YAML graph: %v", err)
	}
	body := out.String()
	graph := strings.Index(body, `data-automation-graph-panel`)
	yaml := strings.Index(body, `data-automation-yaml-panel`)
	editor := strings.Index(body, `data-automation-yaml-editor`)
	if graph < 0 || yaml < 0 || editor < 0 || !(yaml < graph) {
		t.Fatalf("expected YAML editor and read-only graph panels, got yaml=%d graph=%d editor=%d", yaml, graph, editor)
	}
	if !strings.Contains(body, `data-node-key="review"`) {
		t.Error("graph view must render the same candidate represented by the YAML editor")
	}
	for _, want := range []string{`data-automation-add-node-open`, `data-automation-node-tool`, `data-automation-draft-canvas`, `visualCandidateYAML`, `data-automation-yaml-submission`, `data-automation-details-panel`, `data-automation-details-form`, `data-automation-view-details`} {
		if !strings.Contains(body, want) {
			t.Errorf("synchronized authoring surface must contain %q", want)
		}
	}
	for _, want := range []string{`graphButton && graphButton.addEventListener`, `yamlButton && yamlButton.addEventListener`, `detailsButton && detailsButton.addEventListener`, `detailsPanel.hidden = !detailsSelected`, `form.requestSubmit()`, `automationYAMLValue() !== visualCandidateYAML()`, `input.value = submittedYAML`} {
		if !strings.Contains(body, want) {
			t.Errorf("Graph/YAML/Details synchronization must contain %q", want)
		}
	}

	page.AutomationID = ""
	page.Source = "blank"
	out.Reset()
	if err := AutomationBuilderContent(page, "project-yaml-graph").Render(context.Background(), &out); err != nil {
		t.Fatalf("render blank YAML builder: %v", err)
	}
	if strings.Contains(out.String(), `data-delete-automation-open`) {
		t.Error("unsaved YAML builder must not expose Delete")
	}
}

func TestAutomationLiveActionsUsePrimaryButtonsAndBreadcrumbKebab(t *testing.T) {
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{
			ID:             "automation-live-actions",
			Name:           "Card actions",
			LifecycleState: models.AutomationActive,
			HealthState:    models.AutomationHealthHealthy,
		},
		Nodes: []models.AutomationLiveNode{{
			AutomationNode: models.AutomationNode{ID: "node-live-actions", NodeKey: "review", Name: "Review", NodeType: models.AutomationNodeAgentTask, Role: "task", ConfigJSON: `{"prompt":"Review the queue.","priority":2}`},
		}},
	}

	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-actions", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render active Automation Live: %v", err)
	}
	body := out.String()
	cardStart := strings.Index(body, `data-automation-readonly-canvas`)
	viewportStart := strings.Index(body, `role="region" aria-label="Live automation graph"`)
	if cardStart < 0 || viewportStart < 0 || viewportStart <= cardStart {
		t.Fatal("expected Live Automation graph card before its viewport")
	}
	cardHeader := body[cardStart:viewportStart]
	breadcrumbHeader := body[:cardStart]
	for _, want := range []string{
		`class="mb-3 flex flex-wrap items-center justify-between gap-3" data-automation-live-card-actions`,
		`data-automation-view-switcher`,
		`data-automation-view-graph`,
		`data-automation-view-details`,
		`data-automation-view-yaml`,
		`data-automation-live-badges`,
		`data-automation-live-status`,
		`data-automation-live-health`,
	} {
		if !strings.Contains(cardHeader, want) {
			t.Errorf("expected Live Automation canvas actions to contain %q", want)
		}
	}
	for _, want := range []string{`data-automation-live-details-panel`, `data-automation-live-node-details`, `data-automation-live-edge-details`, `data-automation-live-node-detail="review"`, `>Prompt</dt>`, `>Review the queue.</dd>`, `selectAutomationLiveView('details')`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected Live Automation Details view to contain %q", want)
		}
	}
	if !(strings.Index(body, `data-automation-graph-panel`) < strings.Index(body, `data-automation-live-details-panel`) && strings.Index(body, `data-automation-live-details-panel`) < strings.Index(body, `data-automation-yaml-panel`)) {
		t.Error("expected Live Automation panels in Graph, Details, YAML order")
	}
	for _, forbidden := range []string{
		`Node states`, `A node’s border and label show the highest-priority work state currently present.`,
		`data-automation-live-legend-row`, `aria-label="Graph status legend"`, `Failed`, `Waiting human`, `Recently Completed`,
	} {
		if strings.Contains(cardHeader, forbidden) {
			t.Errorf("Live Automation canvas must not retain %q", forbidden)
		}
	}
	for _, want := range []string{
		`data-automation-live-header-actions`,
		`data-automation-live-edit`,
		`data-automation-live-run-now="automation-live-actions"`,
		`data-automation-live-menu`,
		`class="dropdown dropdown-end shrink-0"`,
		`aria-label="More actions for Card actions"`,
		`data-automation-live-pause`,
		`data-automation-live-delete`,
	} {
		if !strings.Contains(breadcrumbHeader, want) {
			t.Errorf("expected Live Automation breadcrumb header to contain %q", want)
		}
	}
	if !(strings.Index(cardHeader, `data-automation-view-graph`) < strings.Index(cardHeader, `data-automation-view-details`) && strings.Index(cardHeader, `data-automation-view-details`) < strings.Index(cardHeader, `data-automation-view-yaml`) && strings.Index(breadcrumbHeader, `data-automation-live-edit`) < strings.Index(breadcrumbHeader, `data-automation-live-run-now`) && strings.Index(breadcrumbHeader, `data-automation-live-run-now`) < strings.Index(breadcrumbHeader, `data-automation-live-menu`)) {
		t.Error("expected Live breadcrumb actions in Edit, Run, then kebab order")
	}
	liveSwitcherStart := strings.Index(cardHeader, `data-automation-view-switcher`)
	if liveSwitcherStart < 0 {
		t.Fatal("expected Live Automation canvas view switcher")
	}
	liveSwitcherEndOffset := strings.Index(cardHeader[liveSwitcherStart:], `</div>`)
	if liveSwitcherEndOffset < 0 {
		t.Fatal("expected Live Automation canvas view switcher end")
	}
	liveSwitcher := cardHeader[liveSwitcherStart : liveSwitcherStart+liveSwitcherEndOffset]
	for _, want := range []string{`>Graph</button>`, `>Details</button>`, `>YAML</button>`, `btn-active`, `aria-pressed="true"`, `aria-pressed="false"`} {
		if !strings.Contains(liveSwitcher, want) {
			t.Errorf("expected Live Automation view switcher to contain %q", want)
		}
	}
	if strings.Contains(liveSwitcher, `onclick=`) || strings.Contains(liveSwitcher, `hx-`) || strings.Contains(liveSwitcher, `form=`) {
		t.Error("Live Automation view switcher must remain an inert placeholder")
	}
	menuStart := strings.Index(breadcrumbHeader, `class="dropdown dropdown-end shrink-0"`)
	menuEndOffset := strings.Index(breadcrumbHeader[menuStart:], `</ul>`)
	if menuStart < 0 || menuEndOffset < 0 {
		t.Fatal("expected Live Automation breadcrumb kebab menu")
	}
	menu := breadcrumbHeader[menuStart : menuStart+menuEndOffset]
	for _, want := range []string{"Disable", "Delete"} {
		if !strings.Contains(menu, ">"+want+"</button>") {
			t.Errorf("expected Live Automation kebab to contain %q", want)
		}
	}
	for _, forbidden := range []string{`data-automation-live-menu-edit`, `data-automation-live-menu-run-now`, `>Edit</button>`, `>Run</button>`} {
		if strings.Contains(menu, forbidden) {
			t.Errorf("Live Automation kebab must not retain %q", forbidden)
		}
	}
	if got := strings.Count(body, `data-automation-live-header-actions`); got != 1 {
		t.Errorf("expected one Live Automation breadcrumb action group, got %d", got)
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
		`>Run</button>`,
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
	pausedBody := paused.String()
	if strings.Contains(pausedBody, `/run-now`) || strings.Contains(pausedBody, `>Run</button>`) {
		t.Error("paused Automation Live must not offer Run")
	}
	for _, want := range []string{`data-automation-live-header-actions`, `data-automation-live-resume`, `>Enable</button>`} {
		if !strings.Contains(pausedBody, want) {
			t.Errorf("expected paused Automation Live kebab to contain %q", want)
		}
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

func TestAutomationLiveMatchesEditVisualScale(t *testing.T) {
	nodes := []models.AutomationLiveNode{
		{AutomationNode: models.AutomationNode{ID: "first", Name: "First", PositionX: 120, PositionY: -40}},
		{AutomationNode: models.AutomationNode{ID: "second", Name: "Second", PositionX: 520, PositionY: 160}},
	}
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{ID: "automation-live-scale", Name: "Visual scale", LifecycleState: models.AutomationActive},
		Nodes:      nodes,
		Edges: []models.AutomationLiveEdge{{AutomationEdge: models.AutomationEdge{
			SourceNodeID: "first", TargetNodeID: "second",
		}}},
	}

	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-scale", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		`data-automation-live-node="first" transform="translate(120 -40)"`,
		`x1="290" y1="12" x2="520" y2="212"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected Live graph to match Edit coordinate scale with %q", want)
		}
	}
	for _, scaled := range []string{`translate(150 -46)`, `x1="320"`, `x2="650"`} {
		if strings.Contains(body, scaled) {
			t.Errorf("Live graph must not shrink nodes through legacy expanded coordinates %q", scaled)
		}
	}
}

func TestAutomationBlankBuilderUsesEditableBreadcrumbAndFullHeightEditor(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Blank Automation", AutomationType: "custom", AdapterKey: "custom"}
	page := models.AutomationBuilderPage{Source: "blank", Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: Blank Automation\n"}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-blank-editor").Render(context.Background(), &out); err != nil {
		t.Fatalf("render blank Automation builder: %v", err)
	}
	body := out.String()
	for _, want := range []string{
		`data-automation-builder-header`,
		`data-automation-editable-breadcrumb`,
		`data-automation-name`,
		`data-automation-builder-save`,
		`class="rounded-box border border-base-300 bg-base-100 mb-0 p-4 flex flex-1 min-h-[20rem] flex-col"`,
		`class="automation-canvas-shell relative w-full overflow-hidden rounded-box border border-base-300 bg-base-200/30 flex-1 min-h-[20rem]"`,
		`data-automation-connect-status`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("blank builder must use the saved-editor layout with %q", want)
		}
	}
	for _, forbidden := range []string{
		`data-automation-builder-name`,
		`<h3 class="font-semibold">Canvas</h3>`,
		`Drag nodes to arrange them and empty space to pan.`,
		`Connect steps:`,
		`data-automation-builder-card-actions`,
		`data-automation-builder-actions`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("blank builder must not retain blank-only canvas chrome %q", forbidden)
		}
	}
}

func TestAutomationCanvasIncludesDetailsConfigurationView(t *testing.T) {
	page := models.AutomationBuilderPage{AutomationID: "automation-copy", Result: models.AutomationDraftResult{Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Typography", AutomationType: "custom", AdapterKey: "custom"}}, YAML: "schema_version: 1\nname: Typography\n"}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-yaml-copy").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation YAML builder: %v", err)
	}
	body := out.String()
	for _, want := range []string{"data-automation-connect-status", "data-automation-details-panel", "data-automation-details-form", "data-automation-node-details", "data-automation-edge-details"} {
		if !strings.Contains(body, want) {
			t.Errorf("interactive authoring page must render %q", want)
		}
	}
	for _, forbidden := range []string{"Automation YAML", "YAML controls node and connection configuration", `data-automation-yaml-preview`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("interactive authoring page must omit obsolete YAML chrome %q", forbidden)
		}
	}
}

func TestAutomationLiveSmallGraphViewBoxMatchesEdit(t *testing.T) {
	for name, liveNodes := range map[string][]models.AutomationLiveNode{
		"one node": {
			{AutomationNode: models.AutomationNode{PositionX: 0, PositionY: 0}},
		},
		"small graph": {
			{AutomationNode: models.AutomationNode{PositionX: 0, PositionY: 0}},
			{AutomationNode: models.AutomationNode{PositionX: 260, PositionY: 0}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			draftNodes := make([]models.AutomationDraftNode, 0, len(liveNodes))
			for _, node := range liveNodes {
				draftNodes = append(draftNodes, models.AutomationDraftNode{Position: &models.AutomationDraftPoint{X: node.PositionX, Y: node.PositionY}})
			}
			liveViewBox := automationLiveGraphViewBox(liveNodes)
			editViewBox := automationDraftGraphViewBox(draftNodes)
			if liveViewBox != editViewBox {
				t.Fatalf("Live and Edit must use identical graph bounds for visual-scale parity: Live=%s Edit=%s", liveViewBox, editViewBox)
			}
		})
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
		`class="rounded-box border border-base-300 bg-base-100 p-4 min-w-0 min-h-0 flex flex-1 flex-col" data-automation-readonly-canvas`,
		`class="automation-canvas-shell relative min-h-[20rem] w-full flex-1 overflow-hidden rounded-box border border-base-300 bg-base-200/20"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected Automation Live viewport-filling layout to contain %q", want)
		}
	}
	for _, forbidden := range []string{`max-h-[42rem]`, `flex-none`, `h-[calc(100dvh-26rem)]`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Automation Live canvas must not retain capped viewport sizing %q", forbidden)
		}
	}
}

func TestAutomationBuilderUsesInteractiveGraphAndYAMLEditor(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Builder YAML", AutomationType: "custom", AdapterKey: "custom"}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: Builder YAML\n"}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-builder-yaml").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation YAML builder: %v", err)
	}
	body := out.String()
	for _, want := range []string{`aria-label="Automation graph builder"`, `data-automation-graph-panel`, `data-automation-yaml-panel`, `data-automation-yaml-editor`, `data-automation-zoom-in`, `data-automation-fit`, `data-automation-reset`, `data-automation-node-tool`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected interactive YAML builder to contain %q", want)
		}
	}
}

func TestAutomationYAMLBuilderUsesConsistentLayout(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "YAML Automation", AutomationType: "custom", AdapterKey: "custom"}
	for source, id := range map[string]string{"blank": "", "template": "", "edit": "automation-edit"} {
		page := models.AutomationBuilderPage{AutomationID: id, Source: source, Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: YAML Automation\n"}
		var out bytes.Buffer
		if err := AutomationBuilderContent(page, "project-yaml-layout").Render(context.Background(), &out); err != nil {
			t.Fatalf("render %s YAML builder: %v", source, err)
		}
		body := out.String()
		for _, want := range []string{`data-automation-yaml-builder`, `data-automation-yaml-form`, `data-automation-yaml-editor`, `data-automation-draft-canvas`, `data-automation-graph-panel`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s synchronized YAML builder missing %q", source, want)
			}
		}
		if !strings.Contains(body, `data-automation-yaml-panel hidden style="display: none" class="flex min-h-[20rem] flex-1 flex-col overflow-hidden rounded-box border border-base-300 bg-base-200/20 px-0 py-4 font-mono text-sm leading-6"`) {
			t.Errorf("%s YAML panel must grow to fill the builder card while remaining hidden in Graph mode", source)
		}
		if !strings.Contains(body, `data-automation-yaml-editor-shell`) || !strings.Contains(body, `data-automation-yaml-editor-viewport`) || !strings.Contains(body, `data-automation-yaml-highlight`) || !strings.Contains(body, `data-automation-yaml-fold-controls`) || !strings.Contains(body, `data-automation-yaml-line-numbers`) {
			t.Errorf("%s YAML editor must fill its panel with a highlighted, foldable line-number gutter", source)
		}
		for _, want := range []string{`data-automation-yaml-highlight-line`, `whitespace-pre-wrap break-words`, `wrap="soft"`, `data-automation-yaml-line-number`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s YAML editor must wrap its source across the available panel width: missing %q", source, want)
			}
		}
		if !strings.Contains(body, `data-automation-yaml-panel hidden style="display: none" class="flex min-h-[20rem] flex-1 flex-col overflow-hidden`) || !strings.Contains(body, `class="relative min-h-0 min-w-0 flex-1 overflow-hidden" data-automation-yaml-editor-viewport`) || !strings.Contains(body, `class="group relative shrink-0 overflow-hidden border-r border-base-300" style="box-sizing: border-box; width: max-content; min-width: 4.5rem; flex: 0 0 auto;"`) || !strings.Contains(body, `class="m-0 h-full w-full min-w-0 select-none overflow-hidden whitespace-nowrap pb-0 pl-2 pr-9 pt-0 text-left text-xs text-base-content/45" style="box-sizing: border-box; text-align: left !important;"`) || !strings.Contains(body, `data-automation-yaml-fold-controls`) || !strings.Contains(body, `w-8`) || !strings.Contains(body, `h-6 w-6`) || !strings.Contains(body, `h-5 w-5`) || !strings.Contains(body, `w-full min-w-0 overflow-hidden whitespace-pre-wrap break-words px-3`) || !strings.Contains(body, `data-yaml-indent="' + indent + '"' + hangingIndent`) || !strings.Contains(body, `left:calc(' + column + 'ch + 0.65ch)`) {
			t.Errorf("%s YAML gutter must reserve a split-diff-style line-number lane plus a separate fold-control lane with indentation-preserving wrapping", source)
		}
		if !strings.Contains(body, `detailsButton && detailsButton.addEventListener('click', function() { previewYAMLThenSelect('details'); });`) {
			t.Errorf("%s Details selector must canonicalize pending YAML before showing card fields", source)
		}
		for _, want := range []string{`data-automation-yaml-indent-guides`, `data-automation-yaml-indent-dot`, `data-automation-yaml-indent-rail`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s YAML indentation must use visual-only guides over source spaces: missing %q", source, want)
			}
		}
		if !strings.Contains(body, `data-automation-yaml-indent-rail`) || !strings.Contains(body, `width:1px;z-index:20;background-color:oklch(var(--bc) / 0.3)`) {
			t.Errorf("%s YAML indentation rails must use a visible continuous theme-colored layer", source)
		}
		if strings.Contains(body, `marker = column % 2 === 0 ? '│' : '·'`) {
			t.Errorf("%s YAML indentation must not substitute guide characters into the source overlay flow", source)
		}
	}
}

func TestAutomationBuilderSerializesGitHubImplementationCategoryToYAML(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "GitHub SDLC", AutomationType: "github_sdlc", AdapterKey: "github_sdlc"}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: GitHub SDLC\n"}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-github-category").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation YAML builder: %v", err)
	}
	body := out.String()
	if strings.Contains(body, `name="node_implementation_category"`) {
		t.Error("GitHub implementation category remains runtime-controlled")
	}
	if !strings.Contains(body, `data-automation-yaml-editor`) {
		t.Error("GitHub YAML template must use the canonical YAML editor")
	}
}

func TestAutomationBuilderReadOnlyGraphUsesScheduleWording(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Schedule wording", AutomationType: "custom", AdapterKey: "custom", Nodes: []models.AutomationDraftNode{{Key: "daily", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Position: &models.AutomationDraftPoint{}}}}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: Schedule wording\n"}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-schedule-wording").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation builder: %v", err)
	}
	body := out.String()
	for _, want := range []string{`<span>Schedule</span>`, `data-port-kind=`, `aria-label="Input for`, `aria-label="Output from`} {
		if !strings.Contains(body, want) {
			t.Errorf("interactive graph must render %q", want)
		}
	}
}

func TestAutomationBuilderRendersDeleteControls(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Delete controls", AutomationType: "custom", AdapterKey: "custom", Nodes: []models.AutomationDraftNode{
		{Key: "first", Name: "First", Type: models.AutomationNodeAgentTask, Role: "task", Position: &models.AutomationDraftPoint{}},
		{Key: "second", Name: "Second", Type: models.AutomationNodeOutcome, Role: "completed", Position: &models.AutomationDraftPoint{X: 220}},
	}, Edges: []models.AutomationDraftEdge{{Key: "first_second", From: "first", To: "second", FromPort: "right", ToPort: "left"}}}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: No delete controls\n"}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-no-delete-controls").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation builder: %v", err)
	}
	body := out.String()
	for _, want := range []string{`automation-node-delete`, `automation-edge-delete`, `data-delete-node`, `data-delete-edge`} {
		if !strings.Contains(body, want) {
			t.Errorf("interactive builder must render delete control %q", want)
		}
	}
}

func TestAutomationGraphAndNavigationInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Browser YAML",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{
			{Key: "first", Name: "First", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "YAML-only configuration"}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
			{Key: "second", Name: "Second", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 240, Y: 0}},
			{Key: "third", Name: "Third", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 480, Y: 0}},
		},
		Edges: []models.AutomationDraftEdge{{Key: "first_second", From: "first", To: "second", FromPort: "right", ToPort: "left"}},
	}
	page := models.AutomationBuilderPage{
		Source: "blank",
		Result: models.AutomationDraftResult{Candidate: candidate},
		YAML: `# preloaded parser failure
schema_version: 1
name: "Browser YAML"
description: ""
automation_type: "custom"
adapter_key: "custom"
nodes:
  - key: "first"
    name: "First"
    type: "agent_task"
    role: "task"
    config: {"prompt":"YAML-only configuration"}
    position: {"x":0,"y":0}
  - key: "second"
    name: "Second"
    type: "agent_task"
    role: "task"
    config: {}
    position: {"x":240,"y":0}
  - key: "third"
    name: "Third"
    type: "outcome"
    role: "completed"
    config: {}
    position: {"x":480,"y":0}
edges:
  - key: "first_second"
    from: "first"
    to: "second"
    from_port: "right"
    to_port: "left"
`,
	}
	var builder bytes.Buffer
	if err := AutomationBuilderContent(page, "project-browser").Render(context.Background(), &builder); err != nil {
		t.Fatalf("render browser Automation builder: %v", err)
	}
	detailsPage := page
	detailsPage.InitialView = "details"
	var detailsBuilder bytes.Buffer
	if err := AutomationBuilderContent(detailsPage, "project-browser").Render(context.Background(), &detailsBuilder); err != nil {
		t.Fatalf("render browser Details preview builder: %v", err)
	}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function fail(message) { throw new Error(message); }
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}); }
  function isVisible(element) {
    return !element.hidden && window.getComputedStyle(element).display !== 'none' && element.getClientRects().length > 0;
  }
  function click(selector, label) { var element = document.querySelector(selector); if (!element) fail('missing ' + label); element.click(); }
  function pointEvent(type, target, pointerId) {
    var rect = target.getBoundingClientRect();
    return new PointerEvent(type, {bubbles: true, cancelable: true, button: 0, buttons: type === 'pointerup' ? 0 : 1, pointerId: pointerId, clientX: rect.left + rect.width / 2, clientY: rect.top + rect.height / 2});
  }
  function submittedYAML(editor) {
    var values = Array.from(document.querySelectorAll('[data-automation-yaml-submission]')).map(function(input) { return input.value; });
    if (!values.length || values.some(function(value) { return value !== editor.value; })) fail('canvas mutation did not synchronize the YAML submitted by its forms');
  }
  function contains(editor, text, label) { if (!editor.value.includes(text)) fail(label + ': ' + editor.value); }
  function edge(from, to) { return Array.from(document.querySelectorAll('.automation-draft-edge')).find(function(group) { return group.dataset.from === from && group.dataset.to === to; }); }
  function port(node, side) { return document.querySelector('[data-connect-port="' + node + '"][data-port-side="' + side + '"]'); }
  function connect(from, to, pointerId) {
    var source = port(from, 'right'), target = port(to, 'left');
    if (!source || !target) fail('missing ports for ' + from + ' to ' + to);
    source.dispatchEvent(pointEvent('pointerdown', source, pointerId));
    target.dispatchEvent(pointEvent('pointerup', target, pointerId));
  }
  function reconnect(group, endpoint, targetNode, pointerId) {
    var controls = document.querySelector('[data-edge-controls][data-edge-key="' + group.dataset.edgeKey + '"]');
    var handle = controls && controls.querySelector('[data-reconnect-edge][data-edge-endpoint="' + endpoint + '"]');
    var target = port(targetNode, endpoint === 'from' ? 'right' : 'left');
    if (!handle || !target) fail('missing reconnect controls');
    handle.dispatchEvent(pointEvent('pointerdown', handle, pointerId));
    target.dispatchEvent(pointEvent('pointerup', target, pointerId));
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message || 'window error')); });
  (async function() {
    var editor = document.querySelector('[data-automation-yaml-editor]');
    var graph = document.querySelector('[data-automation-graph-panel]');
    var yaml = document.querySelector('[data-automation-yaml-panel]');
    var requestedInitialView = document.querySelector('[data-automation-initial-view]');
    if (requestedInitialView && requestedInitialView.value === 'details') {
      var returnedDetails = document.querySelector('[data-automation-details-panel]');
      if (!returnedDetails || !isVisible(returnedDetails) || isVisible(graph) || isVisible(yaml)) fail('YAML preview replacement did not restore the requested Details view');
      await report('pass', '');
      return;
    }
    if (!editor || !graph || !yaml) fail('builder did not render both graph and YAML views');
    if (editor.readOnly) fail('Edit YAML editor is unexpectedly read-only');
    ['Automation YAML', 'YAML controls node and connection configuration', 'Preview YAML'].forEach(function(legacy) {
      if (yaml.textContent.includes(legacy)) fail('obsolete YAML editor chrome remains: ' + legacy);
    });
    if (document.querySelector('[data-automation-yaml-preview]')) fail('obsolete YAML preview button remains');
    var editableBreadcrumb = document.querySelector('[data-automation-editable-breadcrumb]');
    if (!editableBreadcrumb || !editableBreadcrumb.querySelector('[data-automation-name]')) fail('blank builder must edit its name in the breadcrumb');
    if (document.querySelector('[data-automation-builder-name]')) fail('blank builder must not render a second name editor below the canvas');
    var canvasRoot = document.querySelector('[data-automation-draft-canvas]');
    if (!canvasRoot) fail('missing canvas root');
    if (Array.from(canvasRoot.querySelectorAll('h3')).some(function(element) { return element.textContent.trim() === 'Canvas'; })) fail('blank-only Canvas heading remains');
    ['Drag nodes to arrange them and empty space to pan.', 'Connect steps:'].forEach(function(legacy) {
      if (Array.from(canvasRoot.querySelectorAll('*')).some(function(element) { return element.children.length === 0 && element.textContent.includes(legacy); })) fail('blank-only canvas chrome remains: ' + legacy);
    });
    if (!isVisible(graph) || isVisible(yaml)) fail('initial Graph view must show only the canvas');
    await new Promise(function(resolve) { window.setTimeout(resolve, 400); });
    var initialDiagnostic = document.querySelector('[data-automation-yaml-diagnostic]');
    if (!initialDiagnostic || initialDiagnostic.classList.contains('hidden') || !initialDiagnostic.textContent.includes('line 1')) fail('preloaded YAML was not validated during editor initialization');
    var details = document.querySelector('[data-automation-details-panel]');
    var detailsButton = document.querySelector('[data-automation-view-details]');
    if (!details || !detailsButton) fail('Details view switcher or panel is missing');
    if (!details.querySelector('[data-automation-details-form]')) fail('Details view is missing its form');
    if (!details.querySelector('[data-automation-node-detail]')) fail('Details view is missing node details');
    if (!details.querySelector('[data-automation-edge-detail]')) fail('Details view is missing transition details');
    click('[data-automation-view-yaml]', 'YAML view button');
    await new Promise(function(resolve) { window.setTimeout(resolve, 400); });
    var diagnostic = document.querySelector('[data-automation-yaml-diagnostic]');
    if (!diagnostic || !diagnostic.textContent.includes('line 2')) fail('preloaded YAML was not validated when the YAML panel became visible');
    editor.value = editor.value.replace('# preloaded parser failure\n', '');
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { window.setTimeout(resolve, 400); });
    if (!diagnostic.classList.contains('hidden')) fail('valid YAML did not clear the preloaded diagnostic');
    detailsButton.click();
    if (!isVisible(details) || isVisible(graph) || isVisible(yaml)) fail('Details switch did not make only the details editor visible');
    if (!details.querySelector('textarea[name="node_first_prompt"]') || !details.querySelector('textarea[name="node_first_goal"]')) fail('Details view omitted prior task configuration controls');
    click('[data-automation-view-yaml]', 'YAML view button after Details');
    if (!isVisible(yaml) || isVisible(graph) || isVisible(details)) fail('YAML switch did not restore the editable YAML view');
	    var gutter = document.querySelector('[data-automation-yaml-gutter]');
	    var editorShell = document.querySelector('[data-automation-yaml-editor-shell]');
	    var lineNumbers = document.querySelector('[data-automation-yaml-line-numbers]');    var highlight = document.querySelector('[data-automation-yaml-highlight]');
    var foldControls = document.querySelector('[data-automation-yaml-fold-controls]');
    var fold = document.querySelector('[data-automation-yaml-fold]');
	    if (!gutter || !editorShell || !lineNumbers || lineNumbers.querySelectorAll('[data-automation-yaml-line-number]').length < 3) fail('YAML editor did not render a line-number gutter');    if (window.getComputedStyle(lineNumbers).whiteSpace !== 'nowrap') fail('YAML gutter line numbers must not wrap into the section-control lane');
    if (editor.getAttribute('wrap') !== 'soft') fail('YAML editor must wrap long YAML values within its panel');
    if (!highlight || !highlight.querySelector('[data-automation-yaml-key]')) fail('YAML editor did not syntax-highlight YAML keys');
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var indentGuides = highlight.querySelector('[data-automation-yaml-indent-guides]');
    var indentDot = highlight.querySelector('[data-automation-yaml-indent-dot]');
    var indentRails = highlight.querySelectorAll('[data-automation-yaml-indent-rail]');
    if (!indentGuides || !indentDot || !indentRails.length) fail('YAML editor did not render visual-only dot and rail indentation guides');
    if (indentGuides.textContent.includes('│')) fail('YAML indentation rails must not be source-flow characters');
    if (window.getComputedStyle(indentDot).position !== 'absolute' || window.getComputedStyle(indentRails[0]).position !== 'absolute') fail('YAML indentation guides must be positioned over source indentation, not laid out as editable text');
    if (window.getComputedStyle(indentDot).backgroundColor === 'rgba(0, 0, 0, 0)') fail('YAML indentation dots must be non-editable visual overlays');
    var firstDotBounds = indentDot.getBoundingClientRect();
    var firstRailBounds = Array.from(indentRails).sort(function(left, right) { return left.getBoundingClientRect().left - right.getBoundingClientRect().left; })[0].getBoundingClientRect();
	    if (firstDotBounds.left <= firstRailBounds.right) fail('the first indentation dot must render visibly after the first vertical grouping rail');    if (!Array.from(indentRails).some(function(rail) { return rail.getBoundingClientRect().height > 24 && window.getComputedStyle(rail).backgroundColor !== 'rgba(0, 0, 0, 0)'; })) fail('YAML indentation rails must continuously span nested YAML rows with a visible color');
		    if (!lineNumbers.classList.contains('w-full') || !lineNumbers.classList.contains('text-left') || !lineNumbers.classList.contains('text-xs') || !lineNumbers.classList.contains('pr-9') || !foldControls.classList.contains('w-8')) fail('YAML gutter must reserve a line-number lane and a separate inset fold-control lane');
		    var gutterBounds = gutter.getBoundingClientRect(), editorShellBounds = editorShell.getBoundingClientRect(), numberBounds = lineNumbers.getBoundingClientRect(), foldControlsBounds = foldControls.getBoundingClientRect();
		    if (gutterBounds.width < 71 || Math.abs(gutterBounds.left - editorShellBounds.left) > 1 || Math.abs(numberBounds.width - gutterBounds.width) > 1 || numberBounds.left < gutterBounds.left - 1 || numberBounds.right > gutterBounds.right + 1 || foldControlsBounds.right > gutterBounds.right + 1) fail('YAML line numbers and fold controls must stay contained in the reserved gutter: shell=' + editorShellBounds.left + ',' + editorShellBounds.right + ' gutter=' + gutterBounds.left + ',' + gutterBounds.right + ' number=' + numberBounds.left + ',' + numberBounds.right + ' controls=' + foldControlsBounds.left + ',' + foldControlsBounds.right);    if (window.getComputedStyle(lineNumbers).textAlign !== 'left' || window.getComputedStyle(lineNumbers).paddingLeft !== '8px' || window.getComputedStyle(lineNumbers).paddingRight !== '36px' || window.getComputedStyle(gutter).minWidth !== '72px') fail('YAML line-number gutter must keep the split-diff left inset and reserve right padding for fold controls: align=' + window.getComputedStyle(lineNumbers).textAlign + ', left=' + window.getComputedStyle(lineNumbers).paddingLeft + ', right=' + window.getComputedStyle(lineNumbers).paddingRight + ', min-width=' + window.getComputedStyle(gutter).minWidth);    var firstLineNumber = lineNumbers.querySelector('[data-automation-yaml-line-number]');
    var firstLineNumberRange = document.createRange();
    firstLineNumberRange.selectNodeContents(firstLineNumber);
    var firstLineNumberTextBounds = firstLineNumberRange.getBoundingClientRect();
	    if (Math.abs(firstLineNumberTextBounds.left - (gutterBounds.left + 8)) > 1) fail('YAML line numbers must use the split-diff 8px left inset: text=' + firstLineNumberTextBounds.left + ', gutter=' + gutterBounds.left);    var editorPadding = window.getComputedStyle(editor).paddingLeft, highlightPadding = window.getComputedStyle(highlight).paddingLeft;
    if (editorPadding !== '12px' || highlightPadding !== '12px') fail('YAML source must use the split diff viewer\'s px-3 content inset: editor=' + editorPadding + ', highlight=' + highlightPadding);
    if (highlight.querySelector('[data-automation-yaml-key]').classList.contains('text-warning')) fail('YAML editor keys still use the warning color');
    if (!foldControls || !fold || fold.parentElement !== foldControls || foldControls.parentElement !== gutter || !fold.dataset.yamlIndent) fail('YAML editor did not render a gutter section-fold control');
	    var foldBounds = fold.getBoundingClientRect();
	    if (foldBounds.left < gutterBounds.left - 1 || foldBounds.right > gutterBounds.right + 1) fail('YAML section-fold control is not contained in the line-number gutter');
	    if (firstLineNumberTextBounds.right > foldBounds.left - 4) fail('YAML line number text must not collide with the fold-control lane: text=' + firstLineNumberTextBounds.right + ', fold=' + foldBounds.left);    if (!fold.classList.contains('opacity-0') || !fold.classList.contains('group-hover:opacity-100')) fail('expanded YAML section-fold control must appear only while hovering the gutter');
if (!fold.classList.contains('h-6') || !fold.classList.contains('w-6') || !fold.classList.contains('right-1') || !fold.classList.contains('p-0') || !fold.querySelector('svg.h-5.w-5')) fail('YAML editor section-fold control must render a larger centered chevron inset from the gutter edge');
			    if (Math.abs(foldBounds.right - (gutterBounds.right - 4)) > 1 || foldBounds.left < foldControlsBounds.left - 1 || foldBounds.right > foldControlsBounds.right + 1 || Math.abs((foldBounds.left + foldBounds.width / 2) - (gutterBounds.right - 16)) > 1) fail('YAML section-fold chevron lane must be inset from the gutter border and visually centered in the reserved fold-control lane: gutter=' + gutterBounds.left + ',' + gutterBounds.right + ' controls=' + foldControlsBounds.left + ',' + foldControlsBounds.right + ' fold=' + foldBounds.left + ',' + foldBounds.right + ' center=' + (foldBounds.left + foldBounds.width / 2) + ' expected=' + (gutterBounds.right - 16));		    if (!fold.classList.contains('bg-transparent') || !fold.classList.contains('hover:bg-transparent') || fold.className.includes('hover:bg-base') || fold.className.includes('hover:text-base-content ')) fail('YAML section-fold control must remain a transparent stable-color ghost control on hover: ' + fold.className);	    var originalYAML = editor.value;    editor.value = 'section:\n  message: "' + 'long YAML value '.repeat(40) + '"\nnext: "still visible"\n';
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var wrappedSource = highlight.querySelector('[data-automation-yaml-highlight-line][data-yaml-line="2"]');
    var wrappedNumber = lineNumbers.querySelector('[data-automation-yaml-line-number][data-yaml-line="2"]');
    var continuedSource = highlight.querySelector('[data-automation-yaml-highlight-line][data-yaml-line="3"]');
    var wrappedSourceHeight = wrappedSource && wrappedSource.getBoundingClientRect().height;
    var wrappedNumberHeight = wrappedNumber && wrappedNumber.getBoundingClientRect().height;
    if (!wrappedSource || !wrappedNumber || wrappedSourceHeight <= 24 || Math.abs(wrappedSourceHeight - wrappedNumberHeight) > 1) fail('wrapped YAML source did not retain aligned line numbers: source=' + wrappedSourceHeight + ', number=' + wrappedNumberHeight);
    if (wrappedSource.dataset.yamlIndent !== '2' || window.getComputedStyle(wrappedSource).paddingLeft === '0px' || window.getComputedStyle(wrappedSource).textIndent !== '0px') fail('wrapped YAML values must retain their parent indentation as a hanging indent');
    if (!continuedSource || window.getComputedStyle(highlight).overflow !== 'visible' || highlight.clientHeight < continuedSource.offsetTop + continuedSource.offsetHeight) fail('wrapped YAML source was clipped before its following logical line');
    if (document.caretRangeFromPoint) {
	    var wrappedTextNode = wrappedSource.childNodes[wrappedSource.childNodes.length - 1];
	    while (wrappedTextNode && wrappedTextNode.nodeType !== 3) wrappedTextNode = wrappedTextNode.previousSibling;
	    if (wrappedTextNode) {
		    var wrapProbeRange = document.createRange();
		    var wrapProbeOffset = Math.min(20, wrappedTextNode.textContent.length);
		    wrapProbeRange.setStart(wrappedTextNode, wrapProbeOffset);
		    wrapProbeRange.setEnd(wrappedTextNode, wrapProbeOffset);
		    var wrapProbeRect = wrapProbeRange.getBoundingClientRect();
		    if (wrapProbeRect.left > 0 && wrapProbeRect.top > 0) {
			    editor.setSelectionRange(0, 0);
			    editor.dispatchEvent(new MouseEvent('mouseup', {bubbles: true, cancelable: true, clientX: wrapProbeRect.left, clientY: wrapProbeRect.top + 2}));
			    var expectedClickOffset = editor.value.indexOf('  message:') + '  message:'.length + wrapProbeOffset - '  message:'.length;
			    var actualIndentColumn = editor.value.slice(0, editor.selectionStart).split('\n').pop().length;
			    if (Math.abs(actualIndentColumn - wrapProbeOffset) > 3) fail('clicking a wrapped hanging-indented YAML line must position the caret at the visually clicked character, not offset by the indent width: got column ' + actualIndentColumn + ', expected near ' + wrapProbeOffset);
		    }
	    }
    }
    editor.value = originalYAML;
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    fold = document.querySelector('[data-automation-yaml-fold]');
    if (!fold) fail('YAML editor did not restore section-fold controls after wrapping source');
    editor.focus();
    if (document.activeElement !== editor) fail('YAML editor did not accept keyboard focus');
    var caretColor = window.getComputedStyle(editor).caretColor;
    if (!caretColor || caretColor === 'auto' || caretColor === 'transparent' || caretColor === 'rgba(0, 0, 0, 0)') fail('YAML editor caret is not visible');
    var foldedYAML = 'section:\n  message: "' + 'hidden collapsed content should not widen later visible lines '.repeat(20) + '"\n' + Array.from({length: 24}, function(_, index) { return 'visible_' + index + ': "still scrollable"'; }).join('\n') + '\n';
	    editor.value = foldedYAML;
	    editor.dispatchEvent(new Event('input', {bubbles: true}));
	    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
	    editor.scrollTop = 0;
	    gutter.dispatchEvent(new WheelEvent('wheel', {bubbles: true, cancelable: true, deltaY: 48}));
	    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
	    if (editor.scrollTop <= 0 || !String(highlight.style.transform || '').includes('-' + editor.scrollTop + 'px')) fail('wheel scrolling the YAML gutter must scroll and synchronize the editable textarea');
	    editor.scrollTop = 0;
	    editor.dispatchEvent(new Event('scroll', {bubbles: true}));
	    fold = document.querySelector('[data-automation-yaml-fold]');    if (!fold) fail('YAML editor did not render section-fold controls for the scroll regression');
    fold.click();
    if (editor.hidden || !highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('YAML section fold must collapse the source view while keeping the textarea editable');
    if (editor.value.includes('hidden collapsed content should not widen later visible lines')) fail('folded textarea display must omit hidden collapsed child content so caret geometry follows visible lines');
    fold = document.querySelector('[data-automation-yaml-fold]');
    if (!fold || window.getComputedStyle(fold).opacity === '0') fail('collapsed YAML section-fold control must remain visible in the line-number gutter');
    if (editor.scrollWidth > editor.clientWidth + 1) fail('collapsed hidden YAML content must not widen the editable folded textarea surface: scrollWidth=' + editor.scrollWidth + ', clientWidth=' + editor.clientWidth);
    var foldedContent = highlight.querySelector('[data-automation-yaml-highlight-line][data-yaml-line="3"]');
    var foldedContentTop = foldedContent && foldedContent.getBoundingClientRect().top;
    gutter.dispatchEvent(new WheelEvent('wheel', {bubbles: true, cancelable: true, deltaY: 48}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (editor.scrollTop <= 0 || editor.hidden || !foldedContent || foldedContent.getBoundingClientRect().top >= foldedContentTop - 1) fail('wheel scrolling the YAML gutter in a collapsed YAML view must scroll the textarea while retaining folded state');
    var foldedScrollTop = editor.scrollTop;
    var beforeVisibleValue = editor.value;
    var typedCursor = beforeVisibleValue.indexOf('visible_0') + 'visible_0'.length;
    editor.setSelectionRange(typedCursor, typedCursor);
    editor.dispatchEvent(new Event('beforeinput', {bubbles: true, cancelable: true}));
    editor.value = beforeVisibleValue.slice(0, typedCursor) + 'Z' + beforeVisibleValue.slice(typedCursor);
    editor.setSelectionRange(typedCursor + 1, typedCursor + 1);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    if (!highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('typing a single character while YAML sections are folded must keep folds intact');
    if (!editor.value.includes('visible_0Z')) fail('typed character while folded did not apply to the visible line');
    var beforeBackspaceValue = editor.value;
    var backspaceCursor = beforeBackspaceValue.indexOf('visible_0Z') + 'visible_0Z'.length;
    editor.setSelectionRange(backspaceCursor, backspaceCursor);
    editor.dispatchEvent(new InputEvent('beforeinput', {bubbles: true, cancelable: true, inputType: 'deleteContentBackward'}));
    editor.value = beforeBackspaceValue.slice(0, backspaceCursor - 1) + beforeBackspaceValue.slice(backspaceCursor);
    editor.setSelectionRange(backspaceCursor - 1, backspaceCursor - 1);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    if (!highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('backspacing a character while YAML sections are folded must keep folds intact');
    if (!editor.value.includes('visible_0') || editor.value.includes('visible_0Z')) fail('backspace with a collapsed cursor while folded did not remove the character from the visible line');
    var multilineBefore = editor.value;
    var multilineCursor = multilineBefore.indexOf('visible_1');
    editor.setSelectionRange(multilineCursor, multilineCursor);
    editor.dispatchEvent(new Event('beforeinput', {bubbles: true, cancelable: true}));
    editor.value = multilineBefore.slice(0, multilineCursor) + 'inserted_a: "x"\ninserted_b: "y"\n' + multilineBefore.slice(multilineCursor);
    editor.setSelectionRange(multilineCursor + 'inserted_a: "x"\ninserted_b: "y"\n'.length, multilineCursor + 'inserted_a: "x"\ninserted_b: "y"\n'.length);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    if (!highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('a multi-line edit while YAML sections are folded must keep folds intact');
    if (!editor.value.includes('inserted_a') || !editor.value.includes('inserted_b')) fail('multi-line insertion while folded was not applied to the visible textarea');
    fold = document.querySelector('[data-automation-yaml-fold]');
    fold.click();
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('expanding a folded section must restore the full YAML view including any edits applied while folded');
    if (!editor.value.includes('hidden collapsed content should not widen later visible lines')) fail('expanding must restore hidden collapsed content that was preserved during folded edits');
    if (!editor.value.includes('visible_0') || editor.value.includes('visible_0Z') || !editor.value.includes('inserted_a') || !editor.value.includes('inserted_b')) fail('expanding after folded edits must retain edits applied to visible lines');
    editor.value = foldedYAML;
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    fold = document.querySelector('[data-automation-yaml-fold]');
    fold.click();
    if (editor.hidden || !highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('re-folding for boundary-edit regression must collapse the source view');
    var forwardBoundaryBefore = editor.value;
    var forwardBoundaryCursor = forwardBoundaryBefore.indexOf('section:') + 'section:'.length;
    editor.setSelectionRange(forwardBoundaryCursor, forwardBoundaryCursor);
    editor.dispatchEvent(new InputEvent('beforeinput', {bubbles: true, cancelable: true, inputType: 'deleteContentForward'}));
    editor.value = forwardBoundaryBefore.slice(0, forwardBoundaryCursor) + forwardBoundaryBefore.slice(forwardBoundaryCursor + 1);
    editor.setSelectionRange(forwardBoundaryCursor, forwardBoundaryCursor);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    if (!highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('Delete at the end of a visible line before a fold must keep the fold intact');
    if (editor.value.indexOf('section:') !== 0) fail('Delete at a fold boundary must leave the line before the fold unmodified: ' + editor.value);
    if (!highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('Delete at a fold boundary must keep the section folded: ' + editor.value);
    fold = document.querySelector('[data-automation-yaml-fold]');
    fold.click();
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('expanding after a forward fold-boundary delete must restore the full YAML view');
    if (!editor.value.includes('hidden collapsed content should not widen later visible lines')) fail('a Delete at a fold boundary must not corrupt the hidden folded content');
    if (editor.value.indexOf('\nvisible_0:') >= 0) fail('expanding after a forward fold-boundary delete must merge visible_0 onto the last hidden line, not leave it on its own line: ' + editor.value);
    if (editor.value.indexOf('visible_0: "still scrollable"') < 0) fail('expanding after a forward fold-boundary delete must retain the merged visible_0 content: ' + editor.value);
    if (editor.value.indexOf('\nvisible_1:') < 0) fail('expanding after a forward fold-boundary delete must retain the unaffected next visible line on its own line: ' + editor.value);
    editor.value = foldedYAML;
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    fold = document.querySelector('[data-automation-yaml-fold]');
    fold.click();
    if (editor.hidden || !highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('re-folding for backward boundary-edit regression must collapse the source view');
    var backwardBoundaryBefore = editor.value;
    var backwardBoundaryCursor = backwardBoundaryBefore.indexOf('visible_0');
    editor.setSelectionRange(backwardBoundaryCursor, backwardBoundaryCursor);
    editor.dispatchEvent(new InputEvent('beforeinput', {bubbles: true, cancelable: true, inputType: 'deleteContentBackward'}));
    editor.value = backwardBoundaryBefore.slice(0, backwardBoundaryCursor - 1) + backwardBoundaryBefore.slice(backwardBoundaryCursor);
    editor.setSelectionRange(backwardBoundaryCursor - 1, backwardBoundaryCursor - 1);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    if (!highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('Backspace at the start of a visible line after a fold must keep the fold intact');
    if (editor.value.indexOf('section:') !== 0) fail('Backspace at a fold boundary must leave the line before the fold unmodified: ' + editor.value);
    if (!highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('Backspace at a fold boundary must keep the section folded: ' + editor.value);
    if (editor.value.indexOf('visible_1:') < 0) fail('Backspace at a fold boundary must leave the next unaffected visible line intact: ' + editor.value);
    fold = document.querySelector('[data-automation-yaml-fold]');
    fold.click();
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (highlight.querySelector('[data-automation-yaml-fold-summary]')) fail('expanding after a backward fold-boundary backspace must restore the full YAML view');
    if (!editor.value.includes('hidden collapsed content should not widen later visible lines')) fail('a Backspace at a fold boundary must not corrupt the hidden folded content');
    if (editor.value.indexOf('\nvisible_0:') >= 0) fail('expanding after a backward fold-boundary backspace must merge visible_0 onto the last hidden line, not leave it on its own line: ' + editor.value);
    if (editor.value.indexOf('visible_0: "still scrollable"') < 0) fail('expanding after a backward fold-boundary backspace must retain the merged visible_0 content: ' + editor.value);
    if (editor.value.indexOf('\nvisible_1:') < 0) fail('expanding after a backward fold-boundary backspace must retain the unaffected next visible line on its own line: ' + editor.value);
    editor.value = originalYAML;    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    click('[data-automation-view-graph]', 'Graph view button');
    if (!isVisible(graph) || isVisible(yaml)) fail('Graph switch did not restore the canvas');

    var first = document.querySelector('[data-node-key="first"]');
    if (!first) fail('missing first node');
    var originalTransform = first.getAttribute('transform');
    first.dispatchEvent(pointEvent('pointerdown', first, 1));
    var move = pointEvent('pointermove', first, 1);
    Object.defineProperties(move, {clientX: {value: move.clientX + 40}, clientY: {value: move.clientY + 30}});
    first.dispatchEvent(move);
    first.dispatchEvent(pointEvent('pointerup', first, 1));
    if (first.getAttribute('transform') === originalTransform) fail('dragging a canvas node did not move it');
    if (editor.value.includes('position: {"x":0,"y":0}')) fail('node drag did not update YAML position: ' + editor.value);
    contains(editor, 'YAML-only configuration', 'node drag discarded YAML-only configuration');
    submittedYAML(editor);

    connect('second', 'third', 2);
    if (!edge('second', 'third')) fail('canvas connect did not render the new edge');
    contains(editor, 'from: "second"\n    to: "third"', 'canvas connect did not update YAML');
    submittedYAML(editor);

    var firstSecond = edge('first', 'second');
    if (!firstSecond) fail('missing original edge for reconnection');
    reconnect(firstSecond, 'to', 'third', 3);
    if (edge('first', 'second') || !edge('first', 'third')) fail('canvas reconnect did not replace the rendered edge');
    contains(editor, 'from: "first"\n    to: "third"', 'canvas reconnect did not update YAML');
    submittedYAML(editor);

    var firstThird = edge('first', 'third');
    var firstThirdControls = firstThird && document.querySelector('[data-edge-controls][data-edge-key="' + firstThird.dataset.edgeKey + '"]');
    var firstThirdDelete = firstThirdControls && firstThirdControls.querySelector('[data-delete-edge]');
    if (!firstThirdDelete) fail('missing delete control for reconnected edge');
    firstThirdDelete.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true, button: 0}));
    if (edge('first', 'third')) fail('canvas delete did not remove the reconnected edge');
    if (editor.value.includes('from: "first"\n    to: "third"')) fail('canvas delete did not remove the edge from YAML');
    submittedYAML(editor);

    click('[data-automation-view-yaml]', 'YAML view button after canvas edits');
    editor.value = 'schema_version: [';
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { window.setTimeout(resolve, 400); });
    var diagnostic = document.querySelector('[data-automation-yaml-diagnostic]');
    var errorLine = document.querySelector('[data-automation-yaml-error-line]');
    if (!diagnostic || diagnostic.classList.contains('hidden') || !diagnostic.textContent.includes('line 1')) fail('malformed YAML did not show an inline line-aware diagnostic');
    if (!errorLine || !errorLine.classList.contains('decoration-wavy')) fail('malformed YAML did not underline the invalid source line');
    var errorDecorationColor = window.getComputedStyle(errorLine).textDecorationColor;
    if (!errorDecorationColor || errorDecorationColor === 'transparent' || errorDecorationColor === 'rgba(0, 0, 0, 0)') fail('malformed YAML error underline is transparent');
    var errorColorReference = document.createElement('span');
    errorColorReference.style.color = 'oklch(var(--er))';
    document.body.appendChild(errorColorReference);
    if (errorDecorationColor !== window.getComputedStyle(errorColorReference).color) fail('malformed YAML error underline does not use the theme error color: ' + errorDecorationColor);
    errorColorReference.remove();
    editor.value = originalYAML + '# open Details after canonical YAML preview\n';
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    detailsButton.click();
    return;
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	browserResult := make(chan string, 1)
	var yamlParseRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/automations/builder":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Method == http.MethodPost {
				if err := r.ParseForm(); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if r.FormValue("initial_view") != "details" {
					http.Error(w, "Details preview must request the Details initial view", http.StatusBadRequest)
					return
				}
				_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><style>:root{--bc:20%% 0.02 260;--er:0.68 0.15 26}body{margin:0;padding:20px}*{box-sizing:border-box}.flex{display:flex}.flex-col{flex-direction:column}.p-4{padding:16px}.px-0{padding-left:0;padding-right:0}.py-4{padding-top:16px;padding-bottom:16px}svg[data-automation-canvas]{display:block;width:100%%;height:600px}[data-automation-yaml-gutter]{width:max-content;min-width:72px;position:relative}[data-automation-yaml-fold-controls]{position:absolute;top:0;right:0;bottom:0;width:32px}[data-automation-yaml-fold]{position:absolute;right:4px;width:24px;height:24px}[data-automation-yaml-panel]{height:260px;display:flex;flex-direction:column;overflow:hidden}[data-automation-yaml-editor-shell]{display:flex;flex:1 1 0%%;min-height:0;overflow:hidden}[data-automation-yaml-editor-viewport]{position:relative;min-height:0;flex:1 1 0%%;overflow:hidden}[data-automation-yaml-editor-viewport].overflow-y-auto{overflow-y:auto}[data-automation-yaml-editor-viewport].overflow-x-hidden{overflow-x:hidden}[data-automation-yaml-highlight]{position:absolute;left:0;right:0;top:0;box-sizing:border-box;min-height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre-wrap;overflow:visible;overflow-wrap:break-word}[data-automation-yaml-editor]{position:absolute;inset:0;box-sizing:border-box;width:100%%;height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre-wrap;overflow-wrap:break-word}[data-automation-yaml-highlight-line],[data-automation-yaml-line-number]{display:block;min-height:24px}.relative{position:relative}.absolute{position:absolute}.left-0{left:0}.right-1{right:4px}.inset-y-0{top:0;bottom:0}.whitespace-nowrap{white-space:nowrap}.text-left{text-align:left}.text-right{text-align:right}.px-2{padding-left:8px;padding-right:8px}.py-0{padding-top:0;padding-bottom:0}.pb-0{padding-bottom:0}.pl-2{padding-left:8px}.pr-0{padding-right:0}.pr-7{padding-right:28px}.pr-9{padding-right:36px}.pt-0{padding-top:0}.p-0{padding:0}.w-full{width:100%%}.h-5{height:20px}.w-5{width:20px}.text-xs{font-size:12px;line-height:16px}.font-mono{font-family:monospace}.border-collapse{border-collapse:collapse}.diff-table td{padding-top:1px;padding-bottom:1px;vertical-align:top;line-height:1.5}.diff-line-num{min-width:40px;user-select:none}[data-automation-yaml-line-numbers]{width:100%%;margin:0;font:12px/24px monospace}</style></head><body><div style="position:absolute;visibility:hidden;left:20px;right:20px"><table class="diff-table w-full text-xs font-mono border-collapse"><colgroup><col style="width:40px"/><col style="width:50%%"/><col style="width:40px"/><col style="width:50%%"/></colgroup><tbody><tr><td class="diff-line-num text-right px-2 py-0" data-split-diff-gutter-reference>1</td><td>source</td><td class="diff-line-num text-right px-2 py-0">1</td><td>source</td></tr></tbody></table></div>%s%s</body></html>`, detailsBuilder.String(), runner)
				return
			}
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><style>:root{--bc:20%% 0.02 260;--er:0.68 0.15 26}body{margin:0;padding:20px}*{box-sizing:border-box}.flex{display:flex}.flex-col{flex-direction:column}.p-4{padding:16px}.px-0{padding-left:0;padding-right:0}.py-4{padding-top:16px;padding-bottom:16px}svg[data-automation-canvas]{display:block;width:100%%;height:600px}[data-automation-yaml-gutter]{width:max-content;min-width:72px;position:relative}[data-automation-yaml-fold-controls]{position:absolute;top:0;right:0;bottom:0;width:32px}[data-automation-yaml-fold]{position:absolute;right:4px;width:24px;height:24px}[data-automation-yaml-panel]{height:260px;display:flex;flex-direction:column;overflow:hidden}[data-automation-yaml-editor-shell]{display:flex;flex:1 1 0%%;min-height:0;overflow:hidden}[data-automation-yaml-editor-viewport]{position:relative;min-height:0;flex:1 1 0%%;overflow:hidden}[data-automation-yaml-editor-viewport].overflow-y-auto{overflow-y:auto}[data-automation-yaml-editor-viewport].overflow-x-hidden{overflow-x:hidden}[data-automation-yaml-highlight]{position:absolute;left:0;right:0;top:0;box-sizing:border-box;min-height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre-wrap;overflow:visible;overflow-wrap:break-word}[data-automation-yaml-editor]{position:absolute;inset:0;box-sizing:border-box;width:100%%;height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre-wrap;overflow-wrap:break-word}[data-automation-yaml-highlight-line],[data-automation-yaml-line-number]{display:block;min-height:24px}.relative{position:relative}.absolute{position:absolute}.left-0{left:0}.right-1{right:4px}.inset-y-0{top:0;bottom:0}.whitespace-nowrap{white-space:nowrap}.text-left{text-align:left}.text-right{text-align:right}.px-2{padding-left:8px;padding-right:8px}.py-0{padding-top:0;padding-bottom:0}.pb-0{padding-bottom:0}.pl-2{padding-left:8px}.pr-0{padding-right:0}.pr-7{padding-right:28px}.pr-9{padding-right:36px}.pt-0{padding-top:0}.p-0{padding:0}.w-full{width:100%%}.h-5{height:20px}.w-5{width:20px}.text-xs{font-size:12px;line-height:16px}.font-mono{font-family:monospace}.border-collapse{border-collapse:collapse}.diff-table td{padding-top:1px;padding-bottom:1px;vertical-align:top;line-height:1.5}.diff-line-num{min-width:40px;user-select:none}[data-automation-yaml-line-numbers]{width:100%%;margin:0;font:12px/24px monospace}</style></head><body><div style="position:absolute;visibility:hidden;left:20px;right:20px"><table class="diff-table w-full text-xs font-mono border-collapse"><colgroup><col style="width:40px"/><col style="width:50%%"/><col style="width:40px"/><col style="width:50%%"/></colgroup><tbody><tr><td class="diff-line-num text-right px-2 py-0" data-split-diff-gutter-reference>1</td><td>source</td><td class="diff-line-num text-right px-2 py-0">1</td><td>source</td></tr></tbody></table></div>%s%s</body></html>`, builder.String(), runner)
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		case "/automations/yaml/parse":
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.FormValue("automation_yaml"), "preloaded parser failure") {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"valid":false,"message":"Malformed YAML: yaml: line %d: did not find expected node content"}`, yamlParseRequests.Add(1))))
				return
			}
			if strings.Contains(r.FormValue("automation_yaml"), "[") {
				_, _ = w.Write([]byte(`{"valid":false,"message":"Malformed YAML: yaml: line 1: did not find expected node content"}`))
				return
			}
			_, _ = w.Write([]byte(`{"valid":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "automation-yaml-browser.stderr")
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
		"--no-first-run",
		"--no-default-browser-check",
		"--window-size=1200,700",
		"--user-data-dir="+filepath.Join(t.TempDir(), "automation-yaml-browser-profile"),
		server.URL,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	defer stopBrowserProcess(cmd)

	select {
	case outcome := <-browserResult:
		if outcome != "pass:" {
			stderr, _ := os.ReadFile(stderrPath)
			t.Fatalf("Automation YAML browser regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
		}
	case <-time.After(20 * time.Second):
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("timed out waiting for Automation YAML browser regression\n%s", strings.TrimSpace(string(stderr)))
	}
}
