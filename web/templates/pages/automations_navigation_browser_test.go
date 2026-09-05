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
	"regexp"
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
		`data-breadcrumb-selector`,
		`Switch Automation`,
		`/breadcrumb-selectors/automations?project_id=project-live&amp;current_id=automation-live&amp;view=live`,
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
	if !strings.Contains(body, `data-automation-builder-save`) {
		t.Error("Edit header must retain the primary save button")
	}
	if strings.Contains(body, `data-automation-details-save`) || strings.Contains(body, `>Save changes</button>`) {
		t.Error("Details view must not render a duplicate bottom save button")
	}
	if !strings.Contains(body, `>A YAML-authored Automation description.</p>`) {
		t.Error("Edit header must retain its description")
	}
}

func TestAutomationViewSwitcherMarkupAndStateHelperAreShared(t *testing.T) {
	sourceBytes, err := os.ReadFile("automations.templ")
	if err != nil {
		t.Fatalf("read automations.templ: %v", err)
	}
	source := string(sourceBytes)
	for _, forbidden := range []string{"templ automationCanvasViewSwitcher", "templ automationBuilderViewSwitcher"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("Automation view switcher markup must not be duplicated through %s", forbidden)
		}
	}
	for _, want := range []string{
		"templ automationViewSwitcher(ariaLabel string)",
		`@automationViewSwitcher("Automation canvas view")`,
		`@automationViewSwitcher("Automation builder view")`,
		"templ automationViewStateScript()",
		"window.setAutomationCanvasView",
		"return true;",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("expected shared Automation view switching source to contain %q", want)
		}
	}
	for _, marker := range []string{
		`data-automation-view-graph>Graph</button>`,
		`data-automation-view-details>Details</button>`,
		`data-automation-view-yaml>YAML</button>`,
	} {
		if got := strings.Count(source, marker); got != 1 {
			t.Errorf("expected shared switcher marker %q to appear once in source, got %d", marker, got)
		}
	}
	for _, update := range []string{`style.display`, `classList.toggle('btn-active'`, `setAttribute('aria-pressed'`} {
		if got := strings.Count(source, update); got != 3 {
			t.Errorf("expected three shared view-state %q updates in the helper only, got %d", update, got)
		}
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
	markupOnly := regexp.MustCompile(`(?s)<script>.*?</script>`).ReplaceAllString(body, "")
	graph := strings.Index(markupOnly, `data-automation-graph-panel`)
	yaml := strings.Index(markupOnly, `data-automation-yaml-panel`)
	editor := strings.Index(markupOnly, `data-automation-yaml-editor`)
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
	for _, want := range []string{`graphButton && graphButton.addEventListener`, `yamlButton && yamlButton.addEventListener`, `detailsButton && detailsButton.addEventListener`, `setAutomationCanvasView(viewRoot, view`, `if (view === 'yaml') validateYAMLNow()`, `form.requestSubmit()`, `automationYAMLValue() !== visualCandidateYAML()`, `input.value = submittedYAML`} {
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

func TestAutomationLiveYAMLPanelMatchesEditorButIsReadOnly(t *testing.T) {
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{ID: "automation-live-yaml", Name: "Live YAML", LifecycleState: models.AutomationActive},
		Version:    models.AutomationVersion{ID: "saved-snapshot"},
		YAML:       "schema_version: 1\nname: Live YAML\nnodes: []\n",
	}

	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-yaml", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live YAML: %v", err)
	}
	body := out.String()

	for _, want := range []string{
		`data-automation-yaml-panel`,
		`data-automation-yaml-editor-shell`,
		`data-automation-yaml-gutter`,
		`data-automation-yaml-line-numbers`,
		`data-automation-yaml-editor-viewport`,
		`data-automation-yaml-highlight`,
		`data-automation-yaml-editor`,
		`data-automation-yaml-readonly`,
		`readonly`,
		`tabindex="-1"`,
		`cursor-default`,
		`wrap="off"`,
		`whitespace-pre`,
		`schema_version: 1`,
		`aria-label="Automation definition (read-only).`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Preview YAML panel must reuse the editable editor's structure and contain %q", want)
		}
	}
	// Strip <script> contents before checking for forbidden markup: the page
	// includes a shared YAML rendering script (used by both the editable
	// builder and this read-only panel) whose JS source text legitimately
	// contains strings like "data-automation-yaml-fold-summary" as string
	// literals for the builder's fold feature. Those are not DOM elements
	// rendered on this read-only page, so they must not fail this check.
	markupOnly := regexp.MustCompile(`(?s)<script>.*?</script>`).ReplaceAllString(body, "")
	for _, forbidden := range []string{
		`name="automation_yaml"`,
		`data-automation-yaml-parse-url`,
		`data-automation-yaml-fold-controls`,
		`data-automation-yaml-fold`,
	} {
		if strings.Contains(markupOnly, forbidden) {
			t.Errorf("Preview YAML panel must not be a submittable/editable/foldable surface, but found %q", forbidden)
		}
	}
	// The YAML panel's class list includes Tailwind's "flex" utility (display: flex),
	// which has equal specificity to the browser's default `[hidden] { display: none }`
	// UA rule. Without an explicit inline `display: none` fallback matching the `hidden`
	// attribute, a later-loaded `.flex` utility rule can win the cascade and force the
	// panel to always render regardless of which view is selected.
	if !strings.Contains(body, `data-automation-yaml-panel hidden style="display: none"`) {
		t.Error("Preview YAML panel must pair the hidden attribute with an inline display:none fallback so a flex utility class cannot override it")
	}
}

// TestAutomationLiveYAMLViewSwitcherActuallyTogglesVisibility exercises the Live/Preview
// page's Graph/Details/YAML switcher in a real browser using a CSS fixture that defines
// `.flex { display: flex }`, matching production Tailwind output. This guards against a
// regression where the YAML panel's `flex` utility class visually wins the CSS cascade
// over the `hidden` attribute, leaving the YAML panel always visible (shrinking the graph
// and details panels) regardless of which switcher button is selected.
func TestAutomationLiveYAMLViewSwitcherActuallyTogglesVisibility(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	graph := models.AutomationLiveGraph{
		Automation: models.Automation{ID: "automation-live-switch", Name: "Live Switch", LifecycleState: models.AutomationActive},
		Version:    models.AutomationVersion{ID: "saved-snapshot"},
		Nodes: []models.AutomationLiveNode{{
			AutomationNode: models.AutomationNode{ID: "node-switch", NodeKey: "first", Name: "First", NodeType: models.AutomationNodeAgentTask, Role: "task"},
		}},
		YAML: "schema_version: 1\nname: Live Switch\nnodes:\n  - key: \"first\"\n    name: First\n",
	}
	var out bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-switch", true).Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation Live view-switcher fixture: %v", err)
	}
	var fresh bytes.Buffer
	if err := AutomationLiveContent(graph, "project-live-switch", true).Render(context.Background(), &fresh); err != nil {
		t.Fatalf("render Automation Live view-switcher refresh fixture: %v", err)
	}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function fail(message) { throw new Error(message); }
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}); }
  function isVisible(element) {
    return !element.hidden && window.getComputedStyle(element).display !== 'none' && element.getClientRects().length > 0;
  }
  (async function() {
    await new Promise(function(resolve) { window.setTimeout(resolve, 200); });
    var graphPanel = document.querySelector('[data-automation-graph-panel]');
    var detailsPanel = document.querySelector('[data-automation-live-details-panel]');
    var yamlPanel = document.querySelector('[data-automation-yaml-panel]');
    var yamlButton = document.querySelector('[data-automation-view-yaml]');
    var detailsButton = document.querySelector('[data-automation-view-details]');
    var graphButton = document.querySelector('[data-automation-view-graph]');
    if (!graphPanel || !detailsPanel || !yamlPanel || !yamlButton || !detailsButton || !graphButton) fail('missing Live view switcher elements');
    function expectActive(view, label) {
      [[graphButton, 'graph'], [detailsButton, 'details'], [yamlButton, 'yaml']].forEach(function(pair) {
        var selected = pair[1] === view;
        if (pair[0].classList.contains('btn-active') !== selected) fail(label + ' btn-active mismatch for ' + pair[1]);
        if (pair[0].getAttribute('aria-pressed') !== String(selected)) fail(label + ' aria-pressed mismatch for ' + pair[1]);
      });
    }
    if (!isVisible(graphPanel) || isVisible(detailsPanel) || isVisible(yamlPanel)) fail('initial Live view must show only the graph panel');
    expectActive('graph', 'initial Live view');
    yamlButton.click();
    expectActive('yaml', 'selected Live YAML view');
    if (isVisible(graphPanel) || isVisible(detailsPanel) || !isVisible(yamlPanel)) fail('selecting YAML must hide the graph and details panels and show only the YAML panel');
    var yamlTextarea = yamlPanel.querySelector('[data-automation-yaml-editor]');
    if (!yamlTextarea || !yamlTextarea.value.includes('schema_version: 1')) fail('YAML panel did not render the saved automation YAML when selected');
    if (!yamlTextarea.hasAttribute('readonly')) fail('Live/Preview YAML panel textarea must be read-only');
    var lineNumberEls = yamlPanel.querySelectorAll('[data-automation-yaml-line-number]');
    var expectedLineCount = yamlTextarea.value.split('\n').length;
    if (lineNumberEls.length !== expectedLineCount) fail('Live/Preview YAML panel must render one line-number element per source line, got ' + lineNumberEls.length + ' expected ' + expectedLineCount);
    var lineNumbersEl = yamlPanel.querySelector('[data-automation-yaml-line-numbers]');
    var firstNumberTop = lineNumberEls[0] && lineNumberEls[0].getBoundingClientRect().top;
    var lastNumberTop = lineNumberEls[lineNumberEls.length - 1] && lineNumberEls[lineNumberEls.length - 1].getBoundingClientRect().top;
    if (lineNumbersEl && lineNumberEls.length > 1 && lastNumberTop <= firstNumberTop) fail('Live/Preview YAML line numbers must stack vertically, not collapse onto one row');
    var yamlHighlight = yamlPanel.querySelector('[data-automation-yaml-highlight]');
    if (!yamlHighlight || !yamlHighlight.querySelector('[data-automation-yaml-key]')) fail('Live/Preview YAML panel did not syntax-highlight the read-only YAML');
    var indentedLine = yamlHighlight.querySelector('[data-automation-yaml-highlight-line][data-yaml-indent="4"]');
    if (!indentedLine) fail('Live/Preview YAML panel did not render an indented line for the nested "name: First" source line');
    if (window.getComputedStyle(indentedLine).paddingLeft === '0px') fail('Live/Preview YAML panel must reuse the editable editor hanging-indent rendering, but nested lines have no left padding');
    if (!indentedLine.textContent.includes('First')) fail('Live/Preview YAML indented line did not render its text content');
    detailsButton.click();
    expectActive('details', 'selected Live Details view');
    if (isVisible(graphPanel) || !isVisible(detailsPanel) || isVisible(yamlPanel)) fail('selecting Details must hide the graph and YAML panels and show only the details panel');
    graphButton.click();
    expectActive('graph', 'selected Live Graph view');
    if (!isVisible(graphPanel) || isVisible(detailsPanel) || isVisible(yamlPanel)) fail('selecting Graph must hide the details and YAML panels and show only the graph panel');
    yamlButton.click();
    expectActive('yaml', 're-selected Live YAML view');
    if (isVisible(graphPanel) || isVisible(detailsPanel) || !isVisible(yamlPanel)) fail('re-selecting YAML must show only the YAML panel');
    var freshContainer = document.getElementById('automation-live-fresh-markup');
    var liveRoot = document.getElementById('automation-live');
    var replacement = freshContainer.querySelector('#automation-live');
    liveRoot.replaceWith(replacement);
    Array.from(freshContainer.querySelectorAll('script')).forEach(function(script) {
      var clone = document.createElement('script');
      clone.textContent = script.textContent;
      document.body.appendChild(clone);
      clone.remove();
    });
    await new Promise(function(resolve) { window.setTimeout(resolve, 200); });
    graphPanel = document.querySelector('[data-automation-graph-panel]');
    detailsPanel = document.querySelector('[data-automation-live-details-panel]');
    yamlPanel = document.querySelector('[data-automation-yaml-panel]');
    if (isVisible(graphPanel) || isVisible(detailsPanel) || !isVisible(yamlPanel)) fail('a background htmx refresh swap must not reset the selected YAML view back to Graph');
    report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	browserResult := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><style>:root{--bc:20%% 0.02 260}body{margin:0;padding:20px}*{box-sizing:border-box}.flex{display:flex}.flex-col{flex-direction:column}.flex-1{flex:1 1 0%%}.whitespace-nowrap{white-space:nowrap}.whitespace-pre{white-space:pre}.block{display:block}.min-h-6{min-height:24px}svg[data-automation-canvas]{display:block;width:100%%;height:600px}</style></head><body><script>window.htmx = {ajax: function() { return Promise.resolve(); }};</script>%s%s<div id="automation-live-fresh-markup" hidden>%s</div></body></html>`, out.String(), runner, fresh.String())
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "automation-live-view-switcher.stderr")
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
		"--user-data-dir="+filepath.Join(t.TempDir(), "automation-live-view-switcher-profile"),
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
			t.Fatalf("Automation Live view-switcher regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
		}
	case <-time.After(20 * time.Second):
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("timed out waiting for Automation Live view-switcher regression\n%s", strings.TrimSpace(string(stderr)))
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

func TestAutomationEditUsesSearchableBreadcrumbAndRetainsRenameField(t *testing.T) {
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Saved Automation", AutomationType: "custom", AdapterKey: "custom"}
	page := models.AutomationBuilderPage{AutomationID: "automation-saved", Source: "blank", Result: models.AutomationDraftResult{Candidate: candidate}}
	if got := automationBuilderPageTitle(page); got != candidate.Name {
		t.Fatalf("saved Automation builder title = %q, want %q", got, candidate.Name)
	}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-saved").Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{`data-breadcrumb-selector`, `Switch Automation`, `/breadcrumb-selectors/automations?project_id=project-saved&amp;current_id=automation-saved&amp;view=edit`, `data-automation-name`, `>Edit</h2>`} {
		if !strings.Contains(body, want) {
			t.Errorf("saved Automation edit is missing %q", want)
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
		for _, want := range []string{`data-automation-yaml-highlight-line`, `whitespace-pre`, `wrap="off"`, `data-automation-yaml-line-number`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s YAML editor must render source without wrapping, relying on horizontal scroll: missing %q", source, want)
			}
		}
		if !strings.Contains(body, `data-automation-yaml-panel hidden style="display: none" class="flex min-h-[20rem] flex-1 flex-col overflow-hidden`) || !strings.Contains(body, `class="relative min-h-0 min-w-0 flex-1 overflow-hidden" data-automation-yaml-editor-viewport`) || !strings.Contains(body, `class="group relative shrink-0 overflow-hidden border-r border-base-300" style="box-sizing: border-box; width: max-content; min-width: 3.25rem; flex: 0 0 auto;"`) || !strings.Contains(body, `class="m-0 h-full w-full min-w-0 select-none overflow-hidden whitespace-nowrap pb-0 pl-2 pr-2 pt-0 text-left text-xs text-base-content/45" style="box-sizing: border-box; text-align: left !important;"`) || !strings.Contains(body, `data-automation-yaml-fold-controls hidden`) || !strings.Contains(body, `left:calc(' + column + 'ch + 0.65ch)`) {
			t.Errorf("%s YAML gutter must reserve a split-diff-style line-number lane and keep the fold-control lane hidden", source)
		}
		if !strings.Contains(body, `detailsButton && detailsButton.addEventListener('click', function() { previewYAMLThenSelect('details'); });`) {
			t.Errorf("%s Details selector must canonicalize pending YAML before showing card fields", source)
		}
		for _, want := range []string{`data-automation-yaml-indent-guides`, `data-automation-yaml-indent-dot`, `data-automation-yaml-indent-rail`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s YAML indentation must use visual-only guides over source spaces: missing %q", source, want)
			}
		}
		if !strings.Contains(body, `data-automation-yaml-indent-rail`) || !strings.Contains(body, `width:1px;z-index:20;background-color:`) || !strings.Contains(body, `function yamlRailColor(active)`) || !strings.Contains(body, `oklch(var(--bc) / 0.3)`) {
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
	if strings.Contains(body, `>Delete node<`) || strings.Contains(body, `>Delete connection<`) {
		t.Error("Details view node/edge delete controls must be icon-only trash-can buttons, not text labels")
	}
	if !strings.Contains(body, `name="remove_node" value="first" aria-label="Delete node First"`) {
		t.Error("Details view is missing an icon-only delete-node button with an accessible label")
	}
	if !strings.Contains(body, `name="remove_edge" value="first_second" aria-label="Delete connection First to Second"`) {
		t.Error("Details view is missing an icon-only delete-connection button with an accessible label")
	}
	deleteNodeButtonStart := strings.Index(body, `name="remove_node" value="first"`)
	deleteEdgeButtonStart := strings.Index(body, `name="remove_edge" value="first_second"`)
	if deleteNodeButtonStart < 0 || !strings.Contains(body[deleteNodeButtonStart:deleteNodeButtonStart+400], `<svg`) {
		t.Error("Details view delete-node button must render a trash-can SVG icon")
	}
	if deleteEdgeButtonStart < 0 || !strings.Contains(body[deleteEdgeButtonStart:deleteEdgeButtonStart+400], `<svg`) {
		t.Error("Details view delete-connection button must render a trash-can SVG icon")
	}
}

func TestAutomationBuilderDetailsHeaderSaveSubmitsBreadcrumbNameInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Original Details Name",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{{
			Key:      "first",
			Name:     "First",
			Type:     models.AutomationNodeAgentTask,
			Role:     "task",
			Config:   map[string]any{"prompt": "Details prompt", "priority": 2},
			Position: &models.AutomationDraftPoint{X: 0, Y: 0},
		}},
	}
	page := models.AutomationBuilderPage{
		Source: "blank",
		Result: models.AutomationDraftResult{Candidate: candidate},
		YAML:   "schema_version: 1\nname: Original Details Name\nautomation_type: custom\nadapter_key: custom\nnodes:\n  - key: first\n    name: First\n    type: agent_task\n    role: task\n",
	}
	var out bytes.Buffer
	if err := AutomationBuilderContent(page, "project-details-save").Render(context.Background(), &out); err != nil {
		t.Fatalf("render Automation builder Details save fixture: %v", err)
	}

	const editedName = "Edited Details Breadcrumb Name"
	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}); }
  function fail(message) { report('fail', message); }
  try {
    var name = document.querySelector('[data-automation-name]');
    var detailsButton = document.querySelector('[data-automation-view-details]');
    var detailsPanel = document.querySelector('[data-automation-details-panel]');
    var save = document.querySelector('[data-automation-builder-save]');
    if (!name || !detailsButton || !detailsPanel || !save) return fail('missing Details header-save fixture elements');
    name.value = '` + editedName + `';
    name.dispatchEvent(new Event('input', {bubbles: true}));
    detailsButton.click();
    if (detailsPanel.hidden || window.getComputedStyle(detailsPanel).display === 'none') return fail('Details panel was not selected before header save');
    function assertSingleHeaderSave(theme) {
      document.documentElement.setAttribute('data-theme', theme);
      var headerSaves = document.querySelectorAll('[data-automation-builder-save]');
      if (headerSaves.length !== 1) return fail(theme + ' theme rendered header save count=' + headerSaves.length);
      if (detailsPanel.querySelector('[data-automation-details-save]')) return fail(theme + ' theme rendered duplicate Details save marker');
      var duplicateText = Array.prototype.filter.call(detailsPanel.querySelectorAll('button'), function(button) {
        return button.textContent.trim().toLowerCase() === 'save changes';
      });
      if (duplicateText.length) return fail(theme + ' theme rendered duplicate Details Save changes button');
    }
    assertSingleHeaderSave('dark');
    assertSingleHeaderSave('light');
    save.click();
  } catch (error) {
    fail(String(error && error.stack || error));
  }
});
</script>`

	browserResult := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><style>body{margin:0;padding:20px}*{box-sizing:border-box}.flex{display:flex}.flex-col{flex-direction:column}svg[data-automation-canvas]{display:block;width:100%%;height:400px}</style></head><body>%s%s</body></html>`, out.String(), runner)
		case "/automations/builder":
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			if err := r.ParseForm(); err != nil {
				browserResult <- "fail:" + err.Error()
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if got := r.FormValue("automation_name"); got != editedName {
				browserResult <- "fail:Details header save submitted automation_name=" + got
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.FormValue("save_changes") != "true" {
				browserResult <- "fail:Details header save did not submit save_changes=true"
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.FormValue("candidate_json") == "" {
				browserResult <- "fail:Details header save did not submit candidate_json"
				w.WriteHeader(http.StatusNoContent)
				return
			}
			browserResult <- "pass:"
			w.WriteHeader(http.StatusNoContent)
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "automation-details-header-save.stderr")
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
		"--window-size=1000,700",
		"--user-data-dir="+filepath.Join(t.TempDir(), "automation-details-header-save-profile"),
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
			t.Fatalf("Automation Details header-save regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
		}
	case <-time.After(20 * time.Second):
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("timed out waiting for Automation Details header-save regression\n%s", strings.TrimSpace(string(stderr)))
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
    config:
      prompt: "YAML-only configuration"
    position:
      x: 0
      y: 0
  - key: "second"
    name: "Second"
    type: "agent_task"
    role: "task"
    config: {}
    position:
      x: 240
      y: 0
  - key: "third"
    name: "Third"
    type: "outcome"
    role: "completed"
    config: {}
    position:
      x: 480
      y: 0
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
    var graphButton = document.querySelector('[data-automation-view-graph]');
    var yamlButton = document.querySelector('[data-automation-view-yaml]');
    if (!details || !detailsButton || !graphButton || !yamlButton) fail('Details view switcher or panel is missing');
    function expectBuilderActive(view, label) {
      [[graphButton, 'graph'], [detailsButton, 'details'], [yamlButton, 'yaml']].forEach(function(pair) {
        var selected = pair[1] === view;
        if (pair[0].classList.contains('btn-active') !== selected) fail(label + ' btn-active mismatch for ' + pair[1]);
        if (pair[0].getAttribute('aria-pressed') !== String(selected)) fail(label + ' aria-pressed mismatch for ' + pair[1]);
      });
    }
    expectBuilderActive('graph', 'initial builder Graph view');
    if (!details.querySelector('[data-automation-details-form]')) fail('Details view is missing its form');
    if (!details.querySelector('[data-automation-node-detail]')) fail('Details view is missing node details');
    if (!details.querySelector('[data-automation-edge-detail]')) fail('Details view is missing transition details');
    click('[data-automation-view-yaml]', 'YAML view button');
    expectBuilderActive('yaml', 'selected builder YAML view');
    await new Promise(function(resolve) { window.setTimeout(resolve, 400); });
    var diagnostic = document.querySelector('[data-automation-yaml-diagnostic]');
    if (!diagnostic || !diagnostic.textContent.includes('line 2')) fail('preloaded YAML was not validated when the YAML panel became visible');
    editor.value = editor.value.replace('# preloaded parser failure\n', '');
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { window.setTimeout(resolve, 400); });
    if (!diagnostic.classList.contains('hidden')) fail('valid YAML did not clear the preloaded diagnostic');
    detailsButton.click();
    expectBuilderActive('details', 'selected builder Details view');
    if (!isVisible(details) || isVisible(graph) || isVisible(yaml)) fail('Details switch did not make only the details editor visible');
    if (!details.querySelector('textarea[name="node_first_prompt"]') || !details.querySelector('textarea[name="node_first_goal"]')) fail('Details view omitted prior task configuration controls');
    click('[data-automation-view-yaml]', 'YAML view button after Details');
    expectBuilderActive('yaml', 're-selected builder YAML view');
    if (!isVisible(yaml) || isVisible(graph) || isVisible(details)) fail('YAML switch did not restore the editable YAML view');
	    var gutter = document.querySelector('[data-automation-yaml-gutter]');
	    var editorShell = document.querySelector('[data-automation-yaml-editor-shell]');
	    var lineNumbers = document.querySelector('[data-automation-yaml-line-numbers]');    var highlight = document.querySelector('[data-automation-yaml-highlight]');
    var foldControls = document.querySelector('[data-automation-yaml-fold-controls]');
	    if (!gutter || !editorShell || !lineNumbers || lineNumbers.querySelectorAll('[data-automation-yaml-line-number]').length < 3) fail('YAML editor did not render a line-number gutter');    if (window.getComputedStyle(lineNumbers).whiteSpace !== 'nowrap') fail('YAML gutter line numbers must not wrap into the section-control lane');
    if (editor.getAttribute('wrap') !== 'off') fail('YAML editor must not wrap long YAML values; horizontal scroll is used instead');
    if (window.getComputedStyle(editor).whiteSpace !== 'pre') fail('YAML editor must render source with plain pre formatting (no wrap) to keep caret positioning reliable');
    if (foldControls && window.getComputedStyle(foldControls).display !== 'none') fail('YAML section-fold controls must remain hidden for now');
    if (document.querySelector('[data-automation-yaml-fold]')) fail('YAML editor must not render any section-fold buttons while folding is disabled');
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
	    var gutterBounds = gutter.getBoundingClientRect(), editorShellBounds = editorShell.getBoundingClientRect(), numberBounds = lineNumbers.getBoundingClientRect();
	    if (Math.abs(gutterBounds.left - editorShellBounds.left) > 1 || Math.abs(numberBounds.width - gutterBounds.width) > 1 || numberBounds.left < gutterBounds.left - 1 || numberBounds.right > gutterBounds.right + 1) fail('YAML line numbers must stay contained in the reserved gutter: shell=' + editorShellBounds.left + ',' + editorShellBounds.right + ' gutter=' + gutterBounds.left + ',' + gutterBounds.right + ' number=' + numberBounds.left + ',' + numberBounds.right);    var firstLineNumber = lineNumbers.querySelector('[data-automation-yaml-line-number]');
    var firstLineNumberRange = document.createRange();
    firstLineNumberRange.selectNodeContents(firstLineNumber);
    var firstLineNumberTextBounds = firstLineNumberRange.getBoundingClientRect();
	    if (Math.abs(firstLineNumberTextBounds.left - (gutterBounds.left + 8)) > 1) fail('YAML line numbers must use the split-diff 8px left inset: text=' + firstLineNumberTextBounds.left + ', gutter=' + gutterBounds.left);    var editorPadding = window.getComputedStyle(editor).paddingLeft, highlightPadding = window.getComputedStyle(highlight).paddingLeft;
    if (editorPadding !== '12px' || highlightPadding !== '12px') fail('YAML source must use the split diff viewer\'s px-3 content inset: editor=' + editorPadding + ', highlight=' + highlightPadding);
    if (highlight.querySelector('[data-automation-yaml-key]').classList.contains('text-warning')) fail('YAML editor keys still use the warning color');
    var originalYAML = editor.value;
    editor.value = 'section:\n  message: "' + 'long YAML value '.repeat(40) + '"\nnext: "still visible"\n';
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var longSourceLine = highlight.querySelector('[data-automation-yaml-highlight-line][data-yaml-line="2"]');
    var longNumberLine = lineNumbers.querySelector('[data-automation-yaml-line-number][data-yaml-line="2"]');
    if (!longSourceLine || !longNumberLine) fail('unwrapped long YAML source did not retain its own line number');
    if (editor.scrollWidth <= editor.clientWidth) fail('a long YAML value must overflow horizontally instead of wrapping');
    if (window.getComputedStyle(editor).whiteSpace !== 'pre' || window.getComputedStyle(highlight).whiteSpace !== 'pre') fail('the editable textarea and its highlight overlay must both use plain pre formatting so their unwrapped text geometry stays pixel-identical for caret placement');
    if (window.getComputedStyle(editor).paddingLeft !== window.getComputedStyle(highlight).paddingLeft) fail('the editable textarea and its highlight overlay must share identical left padding so native clicks land on the correct character');
    editor.value = originalYAML;
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    editor.focus();
    if (document.activeElement !== editor) fail('YAML editor did not accept keyboard focus');
    var caretColor = window.getComputedStyle(editor).caretColor;
    if (!caretColor || caretColor === 'auto' || caretColor === 'transparent' || caretColor === 'rgba(0, 0, 0, 0)') fail('YAML editor caret is not visible');
    var tabBeforeValue = editor.value;
    var tabCursor = tabBeforeValue.indexOf('name:');
    editor.setSelectionRange(tabCursor, tabCursor);
    editor.dispatchEvent(new KeyboardEvent('keydown', {bubbles: true, cancelable: true, key: 'Tab'}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (editor.value.slice(tabCursor, tabCursor + 2) !== '  ') fail('pressing Tab in the YAML editor must insert two spaces, got: ' + JSON.stringify(editor.value.slice(tabCursor, tabCursor + 4)));
    if (editor.selectionStart !== tabCursor + 2) fail('pressing Tab must move the caret past the inserted two spaces');
    if (document.activeElement !== editor) fail('pressing Tab must not move focus away from the YAML editor');
    var shiftTabCursor = tabCursor + 2;
    editor.setSelectionRange(shiftTabCursor, shiftTabCursor);
    editor.dispatchEvent(new KeyboardEvent('keydown', {bubbles: true, cancelable: true, key: 'Tab', shiftKey: true}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (editor.value !== tabBeforeValue) fail('pressing Shift+Tab must remove the two spaces of indentation that Tab inserted, got: ' + JSON.stringify(editor.value.slice(tabCursor, tabCursor + 4)));
    if (document.activeElement !== editor) fail('pressing Shift+Tab must not move focus away from the YAML editor');
    var lineIndentCursor = editor.value.indexOf('    name: "First"') + 4;
    editor.setSelectionRange(lineIndentCursor, lineIndentCursor);
    editor.dispatchEvent(new KeyboardEvent('keydown', {bubbles: true, cancelable: true, key: 'Tab', shiftKey: true}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (!editor.value.includes('  name: "First"') || editor.value.includes('    name: "First"')) fail('Shift+Tab must remove one level (two spaces) of indentation from the current line');
    if (editor.selectionStart !== lineIndentCursor - 2) fail('Shift+Tab must move the caret back by the removed indentation width');
    editor.value = tabBeforeValue;
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    editor.setSelectionRange(0, 0);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var nestedRail = Array.from(highlight.querySelectorAll('[data-automation-yaml-indent-rail]')).find(function(rail) { return Number(rail.dataset.yamlRailColumn) === 2 && Number(rail.dataset.yamlRailStart) === 34; });
    if (!nestedRail) fail('expected an indentation rail at column 2 for the edge item fields');
    var railStartLine = Number(nestedRail.dataset.yamlRailStart);
    var railLine = highlight.querySelector('[data-automation-yaml-highlight-line][data-yaml-line="' + railStartLine + '"]');
    if (!railLine) fail('could not locate the highlight line for the active rail regression');
    var railLineText = railLine.querySelector('[data-automation-yaml-key], [data-automation-yaml-value]') || railLine;
    var railLineRect = railLineText.getBoundingClientRect();
    var inactiveColor = window.getComputedStyle(nestedRail).backgroundColor;
    var inactiveWidth = window.getComputedStyle(nestedRail).width;
    var lineOffset = editor.value.split('\n').slice(0, railStartLine - 1).join('\n').length + (railStartLine > 1 ? 1 : 0);
    editor.setSelectionRange(lineOffset, lineOffset);
    editor.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, clientX: railLineRect.left + 1, clientY: railLineRect.top + 2}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var activeColor = window.getComputedStyle(nestedRail).backgroundColor;
    var activeWidth = window.getComputedStyle(nestedRail).width;
    if (activeColor === inactiveColor) fail('mousedown into a YAML group must highlight its vertical indentation rail on press, not release: inactive=' + inactiveColor + ' active=' + activeColor);
    if (activeColor === 'rgb(0, 0, 0)' || activeColor.indexOf('88, 28, 135') >= 0) fail('active YAML rail must not use the primary purple accent color: ' + activeColor);
    if (activeWidth !== inactiveWidth) fail('active YAML rail must only change color, not width/boldness: inactive=' + inactiveWidth + ' active=' + activeWidth);
    var headerLine = highlight.querySelector('[data-automation-yaml-highlight-line][data-yaml-line="' + (railStartLine - 1) + '"]');
    var headerLineText = headerLine && (headerLine.querySelector('[data-automation-yaml-key], [data-automation-yaml-value]') || headerLine);
    if (!headerLineText) fail('could not locate the group header line for the top-level rail regression');
    var headerLineRect = headerLineText.getBoundingClientRect();
    var headerOffset = editor.value.split('\n').slice(0, railStartLine - 2).join('\n').length + (railStartLine > 2 ? 1 : 0);
    editor.setSelectionRange(headerOffset, headerOffset);
    editor.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, clientX: headerLineRect.left + 1, clientY: headerLineRect.top + 2}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var headerActiveColor = window.getComputedStyle(nestedRail).backgroundColor;
    if (headerActiveColor === inactiveColor) fail('clicking the top-level field for a group must highlight the associated vertical rail');
    editor.setSelectionRange(lineOffset, lineOffset);
    editor.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, clientX: railLineRect.left + 1, clientY: railLineRect.top + 2}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var beforeTypingColor = window.getComputedStyle(Array.from(highlight.querySelectorAll('[data-automation-yaml-indent-rail]')).find(function(rail) { return Number(rail.dataset.yamlRailColumn) === 2 && Number(rail.dataset.yamlRailStart) === 34; })).backgroundColor;
    if (beforeTypingColor === inactiveColor) fail('rail must be active before typing regression begins');
    var railLineEndOffset = lineOffset + editor.value.split('\n')[railStartLine - 1].length;
    editor.setRangeText('x', railLineEndOffset, railLineEndOffset, 'end');
    editor.setSelectionRange(railLineEndOffset + 1, railLineEndOffset + 1);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var afterTypingRail = Array.from(highlight.querySelectorAll('[data-automation-yaml-indent-rail]')).find(function(rail) { return Number(rail.dataset.yamlRailColumn) === 2 && Number(rail.dataset.yamlRailStart) === 34; });
    if (!afterTypingRail) fail('rail disappeared after typing a character inside the group');
    var afterTypingColor = window.getComputedStyle(afterTypingRail).backgroundColor;
    if (afterTypingColor === inactiveColor) fail('typing inside a highlighted YAML group must not flash the rail back to its inactive color: got=' + afterTypingColor);
    if (afterTypingColor !== beforeTypingColor) fail('typing inside a highlighted YAML group must keep the rail continuously active without flashing: before=' + beforeTypingColor + ' after=' + afterTypingColor);
    editor.value = tabBeforeValue;
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    editor.setSelectionRange(lineOffset, lineOffset);
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    nestedRail = Array.from(highlight.querySelectorAll('[data-automation-yaml-indent-rail]')).find(function(rail) { return Number(rail.dataset.yamlRailColumn) === 2 && Number(rail.dataset.yamlRailStart) === 34; });
    if (!nestedRail) fail('expected an indentation rail at column 2 for the edge item fields after restoring typed edit');
    document.documentElement.setAttribute('data-theme', 'dark');
    editor.dispatchEvent(new KeyboardEvent('keyup', {bubbles: true, key: 'ArrowRight'}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    var darkActiveColor = window.getComputedStyle(nestedRail).backgroundColor;
    document.documentElement.setAttribute('data-theme', 'light');
    editor.dispatchEvent(new KeyboardEvent('keyup', {bubbles: true, key: 'ArrowRight'}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    var lightActiveColor = window.getComputedStyle(nestedRail).backgroundColor;
    if (darkActiveColor === lightActiveColor) fail('active YAML rail color must differ between dark and light themes: dark=' + darkActiveColor + ' light=' + lightActiveColor);
    document.documentElement.setAttribute('data-theme', 'dark');
    var outsideCursor = editor.value.indexOf('schema_version');
    editor.setSelectionRange(outsideCursor, outsideCursor);
    editor.dispatchEvent(new KeyboardEvent('keyup', {bubbles: true, key: 'ArrowLeft'}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    var restoredColor = window.getComputedStyle(nestedRail).backgroundColor;
    if (restoredColor !== inactiveColor) fail('leaving a YAML group must unhighlight its vertical indentation rail: expected=' + inactiveColor + ' got=' + restoredColor);
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    var yamlNameValueClassBeforeDrag = '';
    function yamlValueClassForLineContaining(text) {
      var line = Array.from(highlight.querySelectorAll('[data-automation-yaml-highlight-line]')).find(function(row) { return row.textContent.indexOf(text) >= 0; });
      if (!line) fail('missing YAML highlight line containing ' + text);
      var spans = Array.from(line.querySelectorAll('span')).filter(function(span) { return !span.hasAttribute('data-automation-yaml-key') && !span.hasAttribute('data-automation-yaml-indent-guides') && !span.hasAttribute('data-automation-yaml-indent-guide') && !span.hasAttribute('data-automation-yaml-indent-dot'); });
      if (!spans.length) fail('missing YAML highlighted value span for line containing ' + text + ': ' + line.innerHTML);
      return spans[spans.length - 1].className;
    }
    click('[data-automation-view-graph]', 'Graph view button');
    expectBuilderActive('graph', 'selected builder Graph view');
    if (!isVisible(graph) || isVisible(yaml)) fail('Graph switch did not restore the canvas');

    click('[data-automation-view-yaml]', 'YAML view button before Add node dialog');
    var yamlCommentMarker = '# manual edit that must survive typing in the Add node dialog';
    editor.value = editor.value + '\n' + yamlCommentMarker + '\n';
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    var yamlBeforeTypingNodeName = editor.value;
    if (yamlBeforeTypingNodeName.indexOf(yamlCommentMarker) < 0) fail('manual YAML edit was not applied before opening the Add node dialog');
    document.getElementById('automation-node-dialog').showModal();
    var nodeNameInput = document.querySelector('[data-automation-node-dialog] [name="node_name"]');
    nodeNameInput.value = 'Fourth';
    nodeNameInput.dispatchEvent(new Event('input', {bubbles: true}));
    nodeNameInput.dispatchEvent(new Event('change', {bubbles: true}));
    if (editor.value !== yamlBeforeTypingNodeName) fail('typing a name in the Add node dialog must not update or reformat the YAML editor before the dialog is saved: ' + editor.value);
    document.querySelector('[data-automation-node-dialog] [name="node_kind"]').value = 'task';
    document.querySelector('[data-automation-node-dialog] form').dispatchEvent(new Event('submit', {bubbles: true, cancelable: true}));
    var addNodeSubmittedYAML = document.querySelector('[data-automation-node-dialog] [data-automation-yaml-submission]').value;
    if (/[:\-]\s*\{"/.test(addNodeSubmittedYAML) || /[:\-]\s*\[/.test(addNodeSubmittedYAML)) fail('Add node dialog submission serialized nested YAML fields as inline JSON: ' + addNodeSubmittedYAML);
    if (!/config:\n\s+prompt:/.test(addNodeSubmittedYAML)) fail('Add node dialog submission did not render node config as block-style YAML: ' + addNodeSubmittedYAML);
    document.getElementById('automation-node-dialog').close();

    editor.value = editor.value.replace('name: "Browser YAML"', 'name: Browser YAML');
    editor.dispatchEvent(new Event('input', {bubbles: true}));
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    yamlNameValueClassBeforeDrag = yamlValueClassForLineContaining('Browser YAML');
    var first = document.querySelector('[data-node-key="first"]');
    if (!first) fail('missing first node');
    var originalTransform = first.getAttribute('transform');
    var yamlHighlightLayer = document.querySelector('[data-automation-yaml-highlight]');
    var highlightRebuildCount = 0;
    var highlightObserver = new MutationObserver(function(mutations) { highlightRebuildCount += mutations.length; });
    highlightObserver.observe(yamlHighlightLayer, {childList: true});
    first.dispatchEvent(pointEvent('pointerdown', first, 1));
    for (var dragStep = 1; dragStep <= 12; dragStep++) {
      var stepMove = pointEvent('pointermove', first, 1);
      Object.defineProperties(stepMove, {clientX: {value: stepMove.clientX + dragStep * 3}, clientY: {value: stepMove.clientY + dragStep * 2}});
      first.dispatchEvent(stepMove);
    }
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (highlightRebuildCount > 1) fail('dragging a node must throttle YAML highlight rebuilds to at most one per animation frame to avoid visible color flashing, saw ' + highlightRebuildCount + ' rebuilds for 12 pointermove events');
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    first.dispatchEvent(pointEvent('pointerup', first, 1));
    highlightObserver.disconnect();
    if (first.getAttribute('transform') === originalTransform) fail('dragging a canvas node did not move it');
    if (editor.value.includes('position: {"x":0,"y":0}')) fail('node drag did not update YAML position: ' + editor.value);
    contains(editor, 'YAML-only configuration', 'node drag discarded YAML-only configuration');
    var yamlNameValueClassAfterDrag = yamlValueClassForLineContaining('Browser YAML');
    if (yamlNameValueClassAfterDrag !== yamlNameValueClassBeforeDrag) fail('dragging a graph node must not change YAML string value color class: before=' + yamlNameValueClassBeforeDrag + ' after=' + yamlNameValueClassAfterDrag);
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
				_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><style>:root{--bc:20%% 0.02 260;--er:0.68 0.15 26}body{margin:0;padding:20px}*{box-sizing:border-box}.flex{display:flex}.flex-col{flex-direction:column}.p-4{padding:16px}.px-0{padding-left:0;padding-right:0}.py-4{padding-top:16px;padding-bottom:16px}svg[data-automation-canvas]{display:block;width:100%%;height:600px}[data-automation-yaml-gutter]{width:max-content;min-width:72px;position:relative}[data-automation-yaml-fold-controls]{position:absolute;top:0;right:0;bottom:0;width:32px}[data-automation-yaml-fold]{position:absolute;right:4px;width:24px;height:24px}[data-automation-yaml-panel]{height:260px;display:flex;flex-direction:column;overflow:hidden}[data-automation-yaml-editor-shell]{display:flex;flex:1 1 0%%;min-height:0;overflow:hidden}[data-automation-yaml-editor-viewport]{position:relative;min-height:0;flex:1 1 0%%;overflow:hidden}[data-automation-yaml-editor-viewport].overflow-y-auto{overflow-y:auto}[data-automation-yaml-editor-viewport].overflow-x-hidden{overflow-x:hidden}[data-automation-yaml-highlight]{position:absolute;left:0;top:0;box-sizing:border-box;min-height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre;overflow:visible}[data-automation-yaml-editor]{position:absolute;inset:0;box-sizing:border-box;width:100%%;height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre;overflow:auto}[data-automation-yaml-highlight-line],[data-automation-yaml-line-number]{display:block;min-height:24px}.relative{position:relative}.absolute{position:absolute}.left-0{left:0}.right-1{right:4px}.inset-y-0{top:0;bottom:0}.whitespace-nowrap{white-space:nowrap}.text-left{text-align:left}.text-right{text-align:right}.px-2{padding-left:8px;padding-right:8px}.py-0{padding-top:0;padding-bottom:0}.pb-0{padding-bottom:0}.pl-2{padding-left:8px}.pr-0{padding-right:0}.pr-7{padding-right:28px}.pr-9{padding-right:36px}.pt-0{padding-top:0}.p-0{padding:0}.w-full{width:100%%}.h-5{height:20px}.w-5{width:20px}.text-xs{font-size:12px;line-height:16px}.font-mono{font-family:monospace}.border-collapse{border-collapse:collapse}.diff-table td{padding-top:1px;padding-bottom:1px;vertical-align:top;line-height:1.5}.diff-line-num{min-width:40px;user-select:none}[data-automation-yaml-line-numbers]{width:100%%;margin:0;font:12px/24px monospace}</style></head><body><div style="position:absolute;visibility:hidden;left:20px;right:20px"><table class="diff-table w-full text-xs font-mono border-collapse"><colgroup><col style="width:40px"/><col style="width:50%%"/><col style="width:40px"/><col style="width:50%%"/></colgroup><tbody><tr><td class="diff-line-num text-right px-2 py-0" data-split-diff-gutter-reference>1</td><td>source</td><td class="diff-line-num text-right px-2 py-0">1</td><td>source</td></tr></tbody></table></div>%s%s</body></html>`, detailsBuilder.String(), runner)
				return
			}
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><style>:root{--bc:20%% 0.02 260;--er:0.68 0.15 26}body{margin:0;padding:20px}*{box-sizing:border-box}.flex{display:flex}.flex-col{flex-direction:column}.p-4{padding:16px}.px-0{padding-left:0;padding-right:0}.py-4{padding-top:16px;padding-bottom:16px}svg[data-automation-canvas]{display:block;width:100%%;height:600px}[data-automation-yaml-gutter]{width:max-content;min-width:72px;position:relative}[data-automation-yaml-fold-controls]{position:absolute;top:0;right:0;bottom:0;width:32px}[data-automation-yaml-fold]{position:absolute;right:4px;width:24px;height:24px}[data-automation-yaml-panel]{height:260px;display:flex;flex-direction:column;overflow:hidden}[data-automation-yaml-editor-shell]{display:flex;flex:1 1 0%%;min-height:0;overflow:hidden}[data-automation-yaml-editor-viewport]{position:relative;min-height:0;flex:1 1 0%%;overflow:hidden}[data-automation-yaml-editor-viewport].overflow-y-auto{overflow-y:auto}[data-automation-yaml-editor-viewport].overflow-x-hidden{overflow-x:hidden}[data-automation-yaml-highlight]{position:absolute;left:0;top:0;box-sizing:border-box;min-height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre;overflow:visible}[data-automation-yaml-editor]{position:absolute;inset:0;box-sizing:border-box;width:100%%;height:100%%;margin:0;padding-left:12px;font:16px/24px monospace;white-space:pre;overflow:auto}[data-automation-yaml-highlight-line],[data-automation-yaml-line-number]{display:block;min-height:24px}.relative{position:relative}.absolute{position:absolute}.left-0{left:0}.right-1{right:4px}.inset-y-0{top:0;bottom:0}.whitespace-nowrap{white-space:nowrap}.text-left{text-align:left}.text-right{text-align:right}.px-2{padding-left:8px;padding-right:8px}.py-0{padding-top:0;padding-bottom:0}.pb-0{padding-bottom:0}.pl-2{padding-left:8px}.pr-0{padding-right:0}.pr-7{padding-right:28px}.pr-9{padding-right:36px}.pt-0{padding-top:0}.p-0{padding:0}.w-full{width:100%%}.h-5{height:20px}.w-5{width:20px}.text-xs{font-size:12px;line-height:16px}.font-mono{font-family:monospace}.border-collapse{border-collapse:collapse}.diff-table td{padding-top:1px;padding-bottom:1px;vertical-align:top;line-height:1.5}.diff-line-num{min-width:40px;user-select:none}[data-automation-yaml-line-numbers]{width:100%%;margin:0;font:12px/24px monospace}</style></head><body><div style="position:absolute;visibility:hidden;left:20px;right:20px"><table class="diff-table w-full text-xs font-mono border-collapse"><colgroup><col style="width:40px"/><col style="width:50%%"/><col style="width:40px"/><col style="width:50%%"/></colgroup><tbody><tr><td class="diff-line-num text-right px-2 py-0" data-split-diff-gutter-reference>1</td><td>source</td><td class="diff-line-num text-right px-2 py-0">1</td><td>source</td></tr></tbody></table></div>%s%s</body></html>`, builder.String(), runner)
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
