package pages

import (
	"bytes"
	"context"
	"strings"
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
		`data-automation-view-switcher`, `data-automation-view-graph`, `data-automation-view-yaml`,
		`data-automation-yaml-editor`, `data-automation-yaml-preview`, `name="automation_yaml"`, `schema_version: 1`,
		`data-automation-graph-panel`, `data-automation-draft-canvas`, `data-automation-node-tool`, `Node and connection settings`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected synchronized Automation Edit view to contain %q", want)
		}
	}
	if strings.Contains(body, `name="candidate_json"`) {
		t.Error("visual editor must submit automation_yaml rather than candidate_json")
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
	for _, want := range []string{`data-automation-add-node-open`, `data-automation-node-tool`, `data-automation-draft-canvas`, `visualCandidateYAML`, `data-automation-yaml-submission`} {
		if !strings.Contains(body, want) {
			t.Errorf("synchronized authoring surface must contain %q", want)
		}
	}
	if strings.Contains(body, `name="candidate_json"`) {
		t.Error("visual editor must not submit a legacy candidate_json document")
	}
	for _, want := range []string{`graphButton && graphButton.addEventListener`, `yamlButton && yamlButton.addEventListener`, `graphPanel.hidden = yamlSelected`, `yamlPanel.hidden = !yamlSelected`, `form.requestSubmit()`, `yamlEditor.value !== visualCandidateYAML()`} {
		if !strings.Contains(body, want) {
			t.Errorf("Graph/YAML synchronization must contain %q", want)
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
		`data-automation-view-yaml`,
		`data-automation-live-badges`,
		`data-automation-live-status`,
		`data-automation-live-health`,
	} {
		if !strings.Contains(cardHeader, want) {
			t.Errorf("expected Live Automation canvas actions to contain %q", want)
		}
	}
	for _, forbidden := range []string{
		`class="dropdown dropdown-end"`, `data-automation-live-edit`, `data-automation-live-run-now`, `data-automation-live-pause`, `data-automation-live-delete`,
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
	if !(strings.Index(cardHeader, `data-automation-view-graph`) < strings.Index(cardHeader, `data-automation-view-yaml`) && strings.Index(breadcrumbHeader, `data-automation-live-edit`) < strings.Index(breadcrumbHeader, `data-automation-live-run-now`) && strings.Index(breadcrumbHeader, `data-automation-live-run-now`) < strings.Index(breadcrumbHeader, `data-automation-live-menu`)) {
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
	for _, want := range []string{`>Graph</button>`, `>YAML</button>`, `btn-active`, `aria-pressed="true"`, `aria-pressed="false"`} {
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

func TestAutomationCanvasExplainsInteractiveYAMLSynchronization(t *testing.T) {
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Typography", AutomationType: "custom", AdapterKey: "custom"}}, YAML: "schema_version: 1\nname: Typography\n"}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-yaml-copy").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation YAML builder: %v", err)
	}
	body := out.String()
	for _, text := range []string{"Node and connection settings", "Drag nodes to arrange them", "Connect steps:", "data-automation-connect-status", "Canvas and settings edits update it"} {
		if !strings.Contains(body, text) {
			t.Errorf("interactive YAML authoring page must render %q", text)
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
	for _, want := range []string{`automation-node-delete`, `automation-edge-delete`, `remove_node`, `remove_edge`} {
		if !strings.Contains(body, want) {
			t.Errorf("interactive builder must render delete control %q", want)
		}
	}
}

func TestAutomationGraphAndNavigationInChrome(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Browser YAML", AutomationType: "custom", AdapterKey: "custom"}
	page := models.AutomationBuilderPage{Result: models.AutomationDraftResult{Candidate: candidate}, YAML: "schema_version: 1\nname: Browser YAML\n"}
	var builder bytes.Buffer
	if err := AutomationBuilderContent(page, "project-browser").Render(context.Background(), &builder); err != nil {
		t.Fatalf("render browser YAML fixture: %v", err)
	}
	body := builder.String()
	for _, want := range []string{`data-automation-view-graph`, `data-automation-view-yaml`, `data-automation-yaml-editor`, `graphPanel.hidden = yamlSelected`, `yamlPanel.hidden = !yamlSelected`, `visualCandidateYAML`, `data-automation-add-node-open`, `data-automation-draft-canvas`, `data-candidate-json`} {
		if !strings.Contains(body, want) {
			t.Errorf("synchronized YAML UI fixture missing %q", want)
		}
	}
	if strings.Contains(body, `name="candidate_json"`) {
		t.Error("synchronized YAML UI must not submit candidate_json")
	}
	graph := models.AutomationLiveGraph{Automation: models.Automation{ID: "automation-browser", Name: "Browser YAML", LifecycleState: models.AutomationActive}, YAML: "schema_version: 1\nname: Browser YAML\n"}
	var live bytes.Buffer
	if err := AutomationLiveContent(graph, "project-browser", true).Render(context.Background(), &live); err != nil {
		t.Fatalf("render live YAML fixture: %v", err)
	}
	liveBody := live.String()
	if !strings.Contains(liveBody, `data-automation-yaml-panel`) || strings.Contains(liveBody, `name="automation_yaml"`) {
		t.Error("Live YAML view must be present and read-only")
	}
}
