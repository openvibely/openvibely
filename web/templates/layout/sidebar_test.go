package layout

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestSidebar_ThemeToggleInFooter(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()

	if !strings.Contains(html, `id="sidebar" class="sidebar-aside relative z-[210] lg:z-auto`) {
		t.Fatal("sidebar panel must be layered above the drawer overlay so footer controls are clickable on mobile")
	}
	if !strings.Contains(html, "theme-toggle-pill") {
		t.Fatal("sidebar footer must contain theme toggle pill")
	}
	if !strings.Contains(html, "sidebar-hide-collapsed") {
		t.Fatal("theme toggle pill must have sidebar-hide-collapsed class to hide when collapsed")
	}
	if !strings.Contains(html, "theme-toggle-collapsed-btn") {
		t.Fatal("sidebar footer must contain collapsed theme toggle button")
	}
	if !strings.Contains(html, "sidebar-theme-toggle-container") {
		t.Fatal("sidebar footer must contain theme toggle container")
	}
	if !strings.Contains(html, "justify-end") {
		t.Fatal("sidebar theme-toggle container must right-align footer controls")
	}
	if !strings.Contains(html, "theme-toggle-sun") || !strings.Contains(html, "theme-toggle-moon") {
		t.Fatal("theme toggle must include sun and moon icons")
	}
	if !strings.Contains(html, "theme-collapsed-sun") || !strings.Contains(html, "theme-collapsed-moon") {
		t.Fatal("collapsed theme toggle must include sun and moon icons")
	}
}

func TestSidebar_NavigationHeadingHiddenAndLinksPreserved(t *testing.T) {
	projects := []models.Project{{
		ID:   "project-1",
		Name: "Default",
	}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "project-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()

	if strings.Contains(html, ">Navigation</span>") {
		t.Fatal("sidebar should not render a visible Navigation heading")
	}
	if strings.Contains(html, "menu-title pointer-events-none") {
		t.Fatal("sidebar should not include the menu-title heading row")
	}
	if !strings.Contains(html, `class="menu menu-sm gap-1" aria-label="Main navigation"`) {
		t.Fatal("sidebar nav list must keep menu spacing classes and include an aria-label")
	}
	if strings.Contains(html, `id="insights-menu"`) || strings.Contains(html, "<details") {
		t.Fatal("insights navigation must render as top-level links, not a collapsible details menu")
	}

	requiredLinks := []string{
		`data-nav-base="/chat"`,
		`data-nav-base="/tasks"`,
		`data-nav-base="/schedule"`,
		`data-nav-base="/upcoming"`,
		`data-nav-base="/history"`,
		`data-nav-base="/analytics"`,
		`data-nav-base="/alerts"`,
		`data-nav-base="/workers"`,
		`data-nav-base="/models"`,
	}
	for _, marker := range requiredLinks {
		if !strings.Contains(html, marker) {
			t.Fatalf("sidebar link marker missing: %s", marker)
		}
	}

	hiddenLinks := []string{
		`data-nav-base="/dashboard-mockup"`,
		`data-nav-base="/architect"`,
		`data-nav-base="/autonomous"`,
		`data-nav-base="/suggestions"`,
	}
	for _, marker := range hiddenLinks {
		if strings.Contains(html, marker) {
			t.Fatalf("sidebar link marker should be hidden: %s", marker)
		}
	}
}

func TestSidebar_DispatchesMixtureProgressToChatAndTaskListeners(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `'mixture_progress': handleLiveEvent`) {
		t.Fatal("shared live SSE listener map must subscribe to mixture_progress")
	}
	if !strings.Contains(html, "eventType === 'chat_thread_input_cancelled' || eventType === 'mixture_progress'") {
		t.Fatal("shared live SSE dispatch must route mixture_progress through chat live events")
	}
	if !strings.Contains(html, "window._tabVisibility.dispatchSSEEvent('sse-task-event', data)") || !strings.Contains(html, "if (eventType === 'mixture_progress')") {
		t.Fatal("shared live SSE dispatch must also route mixture_progress to task listeners")
	}
}

func TestSidebar_NavigationAbortsPollingAndSuppressesStaleMorphs(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()

	// Sidebar must abort in-flight polling on navigation to prevent morph from blocking clicks
	requiredSnippets := []string{
		// Flag for stale morph suppression
		"window._sidebarNavigating = true",
		"if (window.cancelChatContentRenders) window.cancelChatContentRenders()",
		// Abort polling requests within main-content
		`querySelectorAll('[hx-trigger*="every"]')`,
		`htmx.trigger(el, 'htmx:abort')`,
		// Disable future polling
		`el.removeAttribute('hx-trigger')`,
		// Clean up thread streaming
		"window._taskThreadStreamingActive = false",
		// Close thread EventSources
		"window._threadEventSources",
		// Stale morph suppression via beforeSwap
		"event.detail.shouldSwap = false",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(html, snippet) {
			t.Fatalf("sidebar navigation abort script missing snippet: %s", snippet)
		}
	}

	// beforeSwap handler must allow main-content swap (navigation) but suppress inner-element swaps
	if !strings.Contains(html, `target.id === 'main-content'`) {
		t.Fatal("sidebar beforeSwap must check for main-content target to allow navigation swap")
	}
	if !strings.Contains(html, `target.closest('#main-content')`) {
		t.Fatal("sidebar beforeSwap must check target.closest to suppress stale inner swaps")
	}
	// Mobile drawer must close after HTMX has committed to a real nav request.
	if !strings.Contains(html, `document.getElementById('sidebar-toggle')`) {
		t.Fatal("sidebar navigation script must target the mobile drawer checkbox")
	}
	if !strings.Contains(html, `sidebarToggle.checked = false`) {
		t.Fatal("sidebar navigation script must uncheck the mobile drawer after nav selection")
	}

	pointerdownStart := strings.Index(html, `addEventListener('pointerdown'`)
	beforeRequestStart := strings.Index(html, `addEventListener('htmx:beforeRequest'`)
	beforeSendStart := strings.Index(html, `addEventListener('htmx:beforeSend'`)
	if pointerdownStart == -1 || beforeRequestStart == -1 || beforeSendStart == -1 || beforeRequestStart <= pointerdownStart || beforeSendStart <= beforeRequestStart {
		t.Fatal("sidebar navigation script must keep pointerdown before htmx:beforeRequest and close the drawer at htmx:beforeSend")
	}
	pointerdownBlock := html[pointerdownStart:beforeRequestStart]
	if strings.Contains(pointerdownBlock, `closeMobileDrawer()`) || strings.Contains(pointerdownBlock, `sidebarToggle.checked = false`) {
		t.Fatal("sidebar must not close the mobile drawer on pointerdown before the click/HTMX request can fire")
	}
	beforeRequestBlock := html[beforeRequestStart:beforeSendStart]
	if strings.Contains(beforeRequestBlock, `closeMobileDrawer();`) && strings.Contains(beforeRequestBlock, `window.location.pathname !== navBase`) {
		t.Fatal("sidebar must not close the mobile drawer in the real-navigation htmx:beforeRequest path before HTMX sends the request")
	}
	beforeSendBlock := html[beforeSendStart:]
	if !strings.Contains(beforeSendBlock, `closeMobileDrawer()`) {
		t.Fatal("sidebar must close the mobile drawer in htmx:beforeSend after HTMX accepts the nav request")
	}
}

func TestSidebar_MousedownEarlyNavigationSignal(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()

	// capture-phase pointerdown handler must exist to set _sidebarNavigating before
	// click processing and before bubble handlers under heavy morph work.
	requiredSnippets := []string{
		// pointerdown listener on body for early signal
		"addEventListener('pointerdown'",
		// capture phase enabled
		"}, true);",
		// Must find nav links via data-nav-base
		"event.target.closest('[data-nav-base]')",
		// Must set flag early
		"window._sidebarNavigating = true",
		// Must have safety timeout to clear flag
		"window._sidebarNavTimeout",
		"setTimeout(function()",
		// Must clear timeout when navigation completes in beforeSwap
		"clearTimeout(window._sidebarNavTimeout)",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(html, snippet) {
			t.Fatalf("sidebar mousedown early-signal script missing snippet: %s", snippet)
		}
	}

	// mousedown handler must skip same-page navigation (consistent with beforeRequest)
	if !strings.Contains(html, "window.location.pathname === navBase") {
		t.Fatal("mousedown handler must skip same-page navigation check")
	}
}

func TestSidebar_CollapseToggleAccessibilityAndA11ySync(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()

	requiredButtonAttrs := []string{
		`id="sidebar-collapse-btn"`,
		`type="button"`,
		`class="sidebar-toggle-btn btn btn-ghost btn-sm btn-square"`,
		`aria-controls="sidebar"`,
		`aria-label="Collapse sidebar (Ctrl+B)"`,
		`aria-expanded="true"`,
		`title="Collapse sidebar (Ctrl+B)"`,
	}
	for _, attr := range requiredButtonAttrs {
		if !strings.Contains(html, attr) {
			t.Fatalf("sidebar toggle button missing attr: %s", attr)
		}
	}

	if strings.Contains(html, `d="M11 19l-7-7 7-7m8 14l-7-7 7-7"`) {
		t.Fatal("sidebar toggle should not use the old double-chevron icon")
	}

	requiredScriptSnippets := []string{
		"function updateSidebarToggleA11y(isCollapsed)",
		"btn.setAttribute('aria-expanded', isCollapsed ? 'false' : 'true');",
		"btn.setAttribute('data-tip', isCollapsed ? 'Expand sidebar' : 'Collapse sidebar');",
		"updateSidebarToggleA11y(collapsed);",
	}
	for _, snippet := range requiredScriptSnippets {
		if !strings.Contains(html, snippet) {
			t.Fatalf("sidebar toggle script missing snippet: %s", snippet)
		}
	}
}

func TestSidebar_UserAreaAndThemeToggleCoexist(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, `id="sidebar-auth-user"`) {
		t.Fatal("sidebar must include auth user area")
	}
	if !strings.Contains(html, `id="sidebar-user-menu-trigger"`) {
		t.Fatal("sidebar auth area must include a user-menu trigger")
	}
	if !strings.Contains(html, `id="sidebar-user-menu"`) {
		t.Fatal("sidebar auth area must include a user-menu dropdown")
	}
	if !strings.Contains(html, `aria-haspopup="menu"`) {
		t.Fatal("sidebar user trigger must declare menu popup semantics")
	}
	if !strings.Contains(html, `class="text-sm"`) || !strings.Contains(html, `>Logout</button>`) {
		t.Fatal("sidebar user menu must include logout as a menu item")
	}
	if strings.Contains(html, `class="btn btn-ghost btn-xs">Logout</button>`) {
		t.Fatal("sidebar should not render standalone always-visible logout button")
	}
	if !strings.Contains(html, `action="/logout"`) {
		t.Fatal("sidebar user menus must preserve logout form action")
	}
}

func TestSidebar_FooterAlignmentAndAccessibleHitTargets(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}
	html := buf.String()

	required := []string{
		`sidebar-theme-toggle-container border-t border-base-300 p-3 flex items-center justify-end gap-2`,
		`id="sidebar-user-menu-trigger"`,
		`class="sidebar-user-trigger btn btn-ghost w-full justify-start items-center gap-2 normal-case"`,
		`class="text-sm" role="menuitem">Logout</button>`,
		`aria-label="Open user menu"`,
		`.sidebar-theme-toggle-container {`,
		`min-height: 3.25rem;`,
		`align-items: center;`,
		`.sidebar-user-trigger {`,
		`min-height: 24px !important;`,
	}
	for _, snippet := range required {
		if !strings.Contains(html, snippet) {
			t.Fatalf("sidebar footer alignment/accessibility marker missing: %s", snippet)
		}
	}
}

func TestSidebar_ForwardsChatTurnSteeredEvents(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, "eventType === 'chat_new_message' || eventType === 'chat_response_done' || eventType === 'chat_turn_steered'") {
		t.Fatal("sidebar dispatcher must forward chat_turn_steered events to chat pages")
	}
	if !strings.Contains(html, "|| eventType === 'chat_thread_input_cancelled'") {
		t.Fatal("sidebar dispatcher must forward chat input cancellation events to chat pages")
	}
	if !strings.Contains(html, "|| eventType === 'task_thread_input_cancelled'") {
		t.Fatal("sidebar dispatcher must forward task thread input cancellation events to task pages")
	}
	if !strings.Contains(html, "'chat_turn_steered': handleLiveEvent") {
		t.Fatal("shared live SSE must subscribe to chat_turn_steered events")
	}
	if !strings.Contains(html, "'chat_thread_input_cancelled': handleLiveEvent") || !strings.Contains(html, "'task_thread_input_cancelled': handleLiveEvent") {
		t.Fatal("shared live SSE must subscribe to pending input cancellation events")
	}
}
