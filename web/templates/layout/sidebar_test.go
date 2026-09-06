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

func TestSidebar_ProjectSelectorUsesSharedSearchableSelectorImplementation(t *testing.T) {
	projects := []models.Project{{ID: "default", Name: "Default", IsDefault: true}, {ID: "other", Name: "Other"}}
	var buf bytes.Buffer
	if err := Sidebar(projects, "default").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}
	html := buf.String()

	for _, required := range []string{
		`data-searchable-selector`,
		`data-searchable-selector-trigger`,
		`data-searchable-selector-dialog`,
		`data-searchable-selector-search`,
		`data-searchable-selector-option`,
		`window.openVibelySearchableSelectorInstalled`,
		`window.openVibelySearchableSelectorController`,
		`previousController.abort.abort()`,
		`var abortController = new AbortController();`,
		`config.signal = abortController.signal`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("project selector missing shared searchable selector contract %q", required)
		}
	}
	if strings.Contains(html, `window.openVibelyProjectSelectorInstalled`) {
		t.Fatal("project selector must not install a separate searchable selector controller")
	}
}

func TestSidebar_ProjectSelectorSearchableAndIdentityOnly(t *testing.T) {
	projects := []models.Project{
		{ID: "default", Name: "Default", IsDefault: true, Description: "private description", RepoPath: "private-repo-path", RepoURL: "https://private.example/repo"},
		{ID: "payments-api", Name: "Payments API"},
		{ID: "payments-web", Name: "Payments Web"},
		{ID: "payments-web-copy", Name: "Payments Web"},
	}

	var buf bytes.Buffer
	if err := Sidebar(projects, "default").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}
	html := buf.String()

	for _, required := range []string{
		`data-project-selector`,
		`id="project-selector"`,
		`class="sr-only"`,
		`aria-hidden="true"`,
		`data-project-selector-value`,
		`id="project-selector-trigger"`,
		`class="select select-bordered select-sm w-full sidebar-project-select`,
		`aria-haspopup="dialog"`,
		`aria-expanded="false"`,
		`aria-controls="project-selector-dialog"`,
		`fixed m-0`,
		`id="project-selector-dialog"`, `role="dialog"`,
		`aria-modal="true"`,
		`id="project-selector-search"`,
		`type="search"`,
		`placeholder="Search projects"`,
		`data-searchable-selector-search-shell`,
		`class="card border border-base-300 bg-base-100 shadow-sm"`,
		`class="w-full border-0 bg-transparent px-4 py-2 text-sm focus:outline-none focus:ring-0"`,
		`aria-autocomplete="list"`,
		`class="menu w-full gap-1 p-0"`,
		`class="flex min-w-0 items-center gap-2 rounded-btn px-4 py-2 hover:bg-base-content/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"`,
		`class="w-4 shrink-0" aria-hidden="true" data-project-selector-current data-searchable-selector-current>✓</span>`,
		`role="listbox"`,
		`data-project-selector-option`,
		`data-project-id="payments-api"`,
		`data-project-name="Payments API"`,
		`aria-selected="true"`,
		`data-project-selector-current`,
		`data-project-selector-no-match`,
		`No projects match your search.`,
		`oninput="window.openVibelySearchableSelector && window.openVibelySearchableSelector.filter(this.closest('[data-searchable-selector]'))"`,
		`onsearch="window.openVibelySearchableSelector && window.openVibelySearchableSelector.filter(this.closest('[data-searchable-selector]'))"`,
		`event.target.matches('[data-searchable-selector-search]')`,
		`function position(root)`,
		`var anchorLeft = root.hasAttribute('data-searchable-selector-left-anchor') ? trigger.left`,
		`dialog.style.top = top + 'px';`,
		`String(state.search.value || '').trim().toLowerCase()`,
		`var hidden = query ? (!match || current) : false;`,
		`if (!hidden) matchCount++;`,
		`option.hidden = hidden;`,
		`option.classList.toggle('hidden', hidden);`,
		`state.search.focus(); state.search.select();`,
		`event.key === 'ArrowDown'`,
		`event.key === 'ArrowUp'`,
		`event.key === 'Escape'`,
		`window.openVibelySearchableSelectorInstalled`,
		`window.openVibelyProjectSelectorChangeInstalled`,
		`state.value.dispatchEvent(new Event('change', {bubbles: true}))`, `persistSelectedProject(newProjectId)`,
		`window.openVibelyNavigate(newUrl)`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("searchable project selector missing %q", required)
		}
	}

	markupEnd := strings.Index(html, "<script>")
	if markupEnd < 0 {
		t.Fatal("project selector shared controller script is missing")
	}
	if strings.Contains(html[:markupEnd], `data-project-selector-clear`) {
		t.Fatal("project selector must use the same native search clear affordance as the breadcrumb selector")
	}
	if strings.Contains(html, `listen(document, 'input'`) || strings.Contains(html, `listen(document, 'search'`) {
		t.Fatal("local project search must use one direct shared-component event path, not duplicate delegated filtering")
	}
	if strings.Contains(html, `min-h-11`) || strings.Contains(html, `hover:bg-base-200`) {
		t.Fatal("selector options must match Automation card kebab menu row height and highlight treatment")
	}
	if strings.Contains(html, `class="relative border-b border-base-300"`) {
		t.Fatal("selector search border must be inset in a rounded shell rather than touching the popup border")
	}
	if strings.Contains(html, `class="modal-box`) {
		t.Fatal("project selector must use the same direct shared panel structure as the breadcrumb selector")
	}
	if strings.Contains(html, `data-project-selector-caret`) || strings.Contains(html, `bg-none`) {
		t.Fatal("project selector must use the original select background arrow without a custom caret")
	}
	if strings.Contains(html, `class="badge badge-sm badge-outline shrink-0"`) {
		t.Fatal("project selector must use the breadcrumb selector check-column treatment, not a project-only Current badge")
	}
	if strings.Contains(html, "private description") || strings.Contains(html, "private-repo-path") || strings.Contains(html, "https://private.example/repo") {
		t.Fatal("sidebar project selector must render identity fields only")
	}

	resultsStart := strings.Index(html, `data-project-selector-results`)
	if resultsStart < 0 {
		t.Fatal("project selector results container is missing")
	}
	defaultIndex := strings.Index(html[resultsStart:], `data-project-id="default"`)
	apiIndex := strings.Index(html[resultsStart:], `data-project-id="payments-api"`)
	if defaultIndex < 0 || apiIndex < 0 || defaultIndex > apiIndex {
		t.Fatal("project selector must preserve the supplied default-first ordering")
	}
}

func TestSidebar_ProjectSelectorEmptyWorkspaceFallback(t *testing.T) {
	var buf bytes.Buffer
	if err := Sidebar(nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render empty Sidebar: %v", err)
	}
	html := buf.String()
	for _, required := range []string{
		`id="project-selector-trigger"`,
		`No projects available`,
		`data-project-selector-no-projects`,
		`aria-controls="project-selector-dialog"`,
		`data-searchable-selector-trigger disabled`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("empty project selector missing %q", required)
		}
	}
	markupEnd := strings.Index(html, "<script>")
	if markupEnd < 0 {
		t.Fatal("sidebar project selector script is missing")
	}
	if strings.Contains(html[:markupEnd], `data-project-selector-option`) {
		t.Fatal("empty project selector must not render a result option")
	}
}

func TestSidebar_ProjectSelectorPreservesRouteMappingAndConfirmation(t *testing.T) {
	projects := []models.Project{{ID: "default", Name: "Default"}, {ID: "other", Name: "Other"}}
	var buf bytes.Buffer
	if err := Sidebar(projects, "default").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}
	html := buf.String()

	for _, path := range []string{
		"/automations",
		"/schedule",
		"/agents",
		"/skills",
		"/models",
		"/chat",
		"/upcoming",
		"/history",
		"/analytics",
		"/alerts",
		"/workers",
		"/insights",
		"/channels",
		"/personality",
	} {
		if !strings.Contains(html, "currentPath.includes('"+path+"')") {
			t.Fatalf("project selector route mapping missing %s", path)
		}
	}
	for _, required := range []string{
		`if (!confirm('You may have unsaved changes. Switch project anyway?'))`,
		`var previousProjectID = sel.value;`,
		`sel.value = previousProjectID;`,
		`openModals.forEach(m => m.close());`,
		`listen(document, 'htmx:beforeSwap'`,
		`target.id === 'main-content'`,
		`document.addEventListener('htmx:afterSwap'`,
		`document.addEventListener('htmx:historyRestore'`,
		`window.addEventListener('popstate'`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("project selector switch lifecycle missing %q", required)
		}
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

}

func TestSidebar_AlertsKeepsUnreadCountSeparateFromSystemUpdateBadge(t *testing.T) {
	projects := []models.Project{{ID: "project-1", Name: "Default"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "project-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()
	for _, required := range []string{
		`id="alert-badge"`,
		`hx-get="/alerts/unread-count?project_id=project-1"`,
		`sidebar-alert-indicators`,
		`id="system-update-nav-badge"`,
		`badge badge-sm badge-primary badge-outline inline-flex items-center sidebar-update-badge hidden`,
		`sidebar-update-badge`,
		`System update available`,
		`>Update</span>`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("alerts nav missing separate update marker snippet: %s", required)
		}
	}
}

func TestSidebar_AutomationsUsesRecognizableOutlineLightningBolt(t *testing.T) {
	projects := []models.Project{{ID: "project-1", Name: "Default"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "project-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()
	start := strings.Index(html, `data-nav-base="/automations"`)
	if start < 0 {
		t.Fatal("Automations navigation link is missing")
	}
	end := strings.Index(html[start:], `</a>`)
	if end < 0 {
		t.Fatal("Automations navigation link is incomplete")
	}
	automationsLink := html[start : start+end]
	for _, marker := range []string{
		`fill="none"`,
		`stroke="currentColor"`,
		`stroke-linecap="round"`,
		`stroke-linejoin="round"`,
		`stroke-width="2"`,
		`d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"`,
	} {
		if !strings.Contains(automationsLink, marker) {
			t.Fatalf("Automations navigation icon is missing outline lightning-bolt marker %s", marker)
		}
	}
	if strings.Contains(automationsLink, `d="M13 10V3L4 14h7v7l9-11h-7z"`) {
		t.Fatal("Automations navigation must not use the ambiguous old zigzag icon")
	}
}

func TestSidebar_RoutesTaskBoardUpdatesThroughSharedTaskEvents(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `'task_board_updated': handleLiveEvent`) {
		t.Fatal("shared live SSE listener map must subscribe to task_board_updated")
	}
	if !strings.Contains(html, "eventType === 'task_board_updated'") {
		t.Fatal("shared live SSE dispatch must route task_board_updated through task listeners")
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
	if strings.Contains(pointerdownBlock, `cancelChatContentRenders`) {
		t.Fatal("sidebar pointerdown must not cancel transcript rendering before navigation is committed")
	}
	beforeRequestBlock := html[beforeRequestStart:beforeSendStart]
	if strings.Contains(beforeRequestBlock, `cancelChatContentRenders`) {
		t.Fatal("HTMX beforeRequest must not cancel transcript rendering before a response is ready to swap")
	}
	if strings.Contains(beforeRequestBlock, `closeMobileDrawer();`) && strings.Contains(beforeRequestBlock, `window.location.pathname !== navBase`) {
		t.Fatal("sidebar must not close the mobile drawer in the real-navigation htmx:beforeRequest path before HTMX sends the request")
	}
	beforeSendBlock := html[beforeSendStart:]
	if !strings.Contains(beforeSendBlock, `closeMobileDrawer()`) {
		t.Fatal("sidebar must close the mobile drawer in htmx:beforeSend after HTMX accepts the nav request")
	}
	if !strings.Contains(beforeSendBlock, `if (event.detail.shouldSwap !== false && target && target.id === 'main-content' && window.cancelChatContentRenders) window.cancelChatContentRenders()`) {
		t.Fatal("committed main-content swaps must cancel obsolete transcript renders")
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

func TestSidebar_CollapseToggleHandlersSharePersistenceHelper(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}

	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Sidebar: %v", err)
	}

	html := buf.String()
	helperStart := strings.Index(html, "function toggleSidebarCollapsed()")
	if helperStart == -1 {
		t.Fatal("sidebar toggle script must define a shared toggleSidebarCollapsed helper")
	}
	helperEnd := strings.Index(html[helperStart:], "btn.addEventListener('click'")
	if helperEnd == -1 {
		t.Fatal("sidebar toggle helper should appear before the click handler")
	}
	helper := html[helperStart : helperStart+helperEnd]

	for _, snippet := range []string{
		"sidebar.classList.toggle('sidebar-collapsed');",
		"var isCollapsed = sidebar.classList.contains('sidebar-collapsed');",
		"localStorage.setItem('sidebar-collapsed', isCollapsed)",
		"persistSidebarPreference(isCollapsed);",
		"updateSidebarToggleA11y(isCollapsed);",
	} {
		if !strings.Contains(helper, snippet) {
			t.Fatalf("shared sidebar toggle helper missing snippet: %s", snippet)
		}
		if strings.Count(html, snippet) != 1 {
			t.Fatalf("sidebar toggle post-action snippet should appear only once, got %d for %s", strings.Count(html, snippet), snippet)
		}
	}

	if strings.Count(html, "toggleSidebarCollapsed();") != 2 {
		t.Fatalf("click and keyboard handlers should be the only callers of toggleSidebarCollapsed, got %d", strings.Count(html, "toggleSidebarCollapsed();"))
	}
	if !strings.Contains(html, "btn.addEventListener('click', function(e) {") || !strings.Contains(html, "e.preventDefault();\n\t\t\t\t\t\ttoggleSidebarCollapsed();") {
		t.Fatal("click handler must prevent default and delegate to shared sidebar toggle helper")
	}
	for _, snippet := range []string{
		"document.addEventListener('keydown', function(e) {",
		"if ((e.ctrlKey || e.metaKey) && e.key === 'b') {",
		"var tag = document.activeElement && document.activeElement.tagName;",
		"if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;",
		"e.preventDefault();\n\t\t\t\t\t\t\ttoggleSidebarCollapsed();",
	} {
		if !strings.Contains(html, snippet) {
			t.Fatalf("keyboard sidebar toggle handler missing snippet: %s", snippet)
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
	for _, snippet := range []string{
		"name.textContent = data.display || data.username",
		"logoutLabel.textContent = 'Log out of this workspace'",
		"data.auth_source === 'hosted_sso'",
	} {
		if !strings.Contains(html, snippet) {
			t.Fatalf("sidebar hosted identity/logout behavior missing: %s", snippet)
		}
	}
	if strings.Contains(html, "name.innerHTML =") || strings.Contains(html, "logoutLabel.innerHTML =") {
		t.Fatal("identity and logout labels must use text-only DOM assignment")
	}
}

func TestSidebar_HostedIdentityUsesOnlyInertTextSinks(t *testing.T) {
	projects := []models.Project{{ID: "p1", Name: "Test"}}
	var buf bytes.Buffer
	if err := Sidebar(projects, "p1").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	start := strings.Index(html, "fetch('/auth/me'")
	if start < 0 {
		t.Fatal("hosted identity browser update block not found")
	}
	end := strings.Index(html[start:], ".catch(function() {})")
	if end < 0 {
		t.Fatal("hosted identity browser update block terminator not found")
	}
	block := html[start : start+end]
	for _, required := range []string{
		"name.textContent = data.display || data.username",
		"avatar.textContent = initial",
		"avatarCollapsed.textContent = initial",
		"logoutLabel.textContent = 'Log out of this workspace'",
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("missing inert identity sink %q", required)
		}
	}
	for _, forbidden := range []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
		"setAttribute('href'", "setAttribute(\"href\"", "setAttribute('src'", "setAttribute(\"src\"",
		"style.cssText", "eval(", "new Function",
	} {
		if strings.Contains(block, forbidden) {
			t.Fatalf("identity update block contains active sink %q", forbidden)
		}
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
		`id="sidebar-logout-label" type="submit" class="text-sm" role="menuitem">Logout</button>`,
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
	if !strings.Contains(html, "|| eventType === 'task_thread_input_steered'") {
		t.Fatal("sidebar dispatcher must forward task thread steering events to task pages")
	}
	if !strings.Contains(html, "'chat_turn_steered': handleLiveEvent") {
		t.Fatal("shared live SSE must subscribe to chat_turn_steered events")
	}
	if !strings.Contains(html, "'chat_thread_input_cancelled': handleLiveEvent") || !strings.Contains(html, "'task_thread_input_cancelled': handleLiveEvent") {
		t.Fatal("shared live SSE must subscribe to pending input cancellation events")
	}
	if !strings.Contains(html, "'task_thread_input_steered': handleLiveEvent") {
		t.Fatal("shared live SSE must subscribe to task thread steering events")
	}
	if !strings.Contains(html, "|| eventType === 'task_lifecycle_execution_changed'") {
		t.Fatal("sidebar dispatcher must forward lifecycle execution changes to task pages")
	}
	if !strings.Contains(html, "'task_lifecycle_execution_changed': handleLiveEvent") {
		t.Fatal("shared live SSE must subscribe to lifecycle execution changes")
	}
}
