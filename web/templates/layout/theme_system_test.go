package layout

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/themes"
)

func TestBaseIncludesBuiltInThemeSystem(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render Base: %v", err)
	}
	html := buf.String()
	for _, fragment := range []string{
		`data-color-theme="` + themes.DefaultDarkThemeID + `"`,
		`id="ov-theme-catalog"`,
		`data-catalog=`,
		`getAttribute('data-catalog')`,
		`openvibely.colorTheme`,
		`openvibely.lastLightTheme`,
		`openvibely.lastDarkTheme`,
		`var rawSaved = document.documentElement.getAttribute('data-ui-theme') || '';`,
		`var saved = normalizeStoredTheme(rawSaved);`,
		`persistUIPreference({ theme: saved });`,
		`data-color-theme-kind`,
		`window.openVibelyThemes.toggleMode`,
		`window.openVibelyThemes.applySystemMode`,
		`window.openVibelyThemes.syncThemeControls`,
		`[data-ov-theme-layer="custom"]{background-color:var(--ov-content-bg)}`,
		`[data-ov-theme-layer="custom"] #main-content { background-color: var(--ov-content-bg) !important; color: var(--ov-text); }`,
		`[data-color-theme] .hljs * { color: var(--ov-syntax-foreground); }`,
		`[data-color-theme] .hljs-keyword`,
		`var(--ov-syntax-keyword)`,
		`[data-color-theme] pre { background-color: var(--ov-syntax-background); }`,
		`[data-ov-theme-layer="custom"] ::selection { background-color: var(--ov-selection-bg);`,
		`border-color: var(--ov-input-border);`,
		`outline-color: var(--ov-focus);`,
		`[data-ov-theme-layer="custom"] .badge-info`,
		`color: var(--ov-info) !important;`,
		`color: var(--ov-success) !important;`,
		`color: var(--ov-warning) !important;`,
		`color: var(--ov-error) !important;`,
		`background-color: var(--ov-hover-bg) !important;`,
		`var(--ov-diff-modify-bg)`,
		`[data-ov-theme-layer="custom"] .card,`,
		`[data-ov-theme-layer="custom"] .dropdown-content:not(.ov-menu){`,
		`--ov-menu-surface: var(--ov-surface-raised);`,
		`--ov-diff-del-fg: var(--ov-diff-delete-fg);`,
		`--ov-diff-del-bg: var(--ov-diff-delete-bg);`,
		`--ov-border-subtle: var(--ov-subtle-border);`,
		`html[data-ov-theme-layer="custom"] .ov-menu { --ov-menu-active: var(--ov-hover-bg);`,
		`html[data-ov-theme-layer="custom"] #desktop-project-titlebar { background-color: var(--ov-sidebar-bg) !important;`,
		`html[data-ov-theme-layer="custom"] #schedule-content .schedule-day-selected { background: var(--ov-accent-tint) !important; }`,
		`[data-ov-theme-layer="custom"] .schedule-datetime-popup [data-time-selected=true] { background: var(--ov-button-bg) !important;`,
		`background-color:var(--ov-surface)!important;border-color:var(--ov-subtle-border)!important`,
		`[data-ov-theme-layer="custom"] .hover\:border-primary\/40:hover`,
		`border-color:var(--ov-subtle-border)!important`,
		`[data-ov-theme-layer="custom"] .border-base-300\/80,`,
		`[data-ov-theme-layer="custom"] .divide-base-300\/70 > :not([hidden]) ~ :not([hidden])`,
		`[data-ov-theme-layer="custom"] .modal-box {`,
		`background-color: var(--ov-surface-raised) !important;`,
		`color: var(--ov-text) !important;`,
		`border: 1px solid var(--ov-border) !important;`,
		`[data-ov-theme-layer="custom"] .modal-box .modal-action`,
		`[data-ov-theme-layer="custom"] .modal-box .bg-base-200`,
		`[data-ov-theme-layer="custom"] .ov-modal-close:hover`,
		`background-color: var(--ov-hover-bg) !important;`,
		`[data-theme="light"][data-ov-theme-layer="custom"] dialog.modal::backdrop`,
		`[data-ov-theme-layer="custom"] [data-automation-yaml-panel] {`,
		`--ov-yaml-indent-dot: var(--ov-yaml-indent-rail);`,
		`--ov-yaml-indent-active: var(--ov-focus);`,
		`[data-ov-theme-layer="custom"] [data-automation-yaml-indent-rail]{background-color:var(--ov-yaml-indent-rail,var(--ov-border-strong))}`,
		`background-color: var(--ov-code-bg) !important;`,
		`font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace !important;`,
		`[data-ov-theme-layer="custom"] [data-automation-yaml-key] { color: var(--ov-syntax-keyword) !important; }`,
		`[data-ov-theme-layer="custom"] [data-automation-yaml-value] { color: var(--ov-syntax-string) !important; }`,
		`[data-ov-theme-layer="custom"] [data-automation-yaml-scalar] { color: var(--ov-syntax-number) !important; }`,
		`[data-ov-theme-layer="custom"] [data-automation-yaml-comment] { color: var(--ov-syntax-comment) !important; }`,
		`[data-ov-theme-layer="custom"] [data-automation-yaml-editor] { caret-color: var(--ov-syntax-foreground) !important;`,
		`--ov-link-color: var(--ov-link);`,
		`--ov-account-limit-track: var(--ov-hover-bg);`,
		`--ov-account-limit-fill: var(--ov-button-bg);`,
		`--ov-schedule-grid-border: var(--ov-border);`,
		`--ov-schedule-task-bg: var(--ov-surface-raised);`,
		`--ov-schedule-task-border: var(--ov-link);`,
		`--ov-schedule-drop-valid-bg: var(--ov-selection-bg);`,
		`[data-ov-theme-layer="custom"] .drag-over { border-color: var(--ov-focus) !important; background-color: var(--ov-selection-bg) !important; }`,
		`[data-ov-theme-layer="custom"] .task-selected {`,
		`[data-ov-theme-layer="custom"] .category-drop-zone,`,
		`[data-ov-theme-layer="custom"] .category-drop-zone.drag-over,`,
		`[data-ov-theme-layer="custom"] .task-drop-zone.drag-over { border-color: var(--ov-focus) !important; background-color: var(--ov-selection-bg) !important; }`,
		`[data-ov-theme-layer="custom"] #schedule-timeline-container { background-color: var(--ov-surface) !important; border-color: var(--ov-border) !important; }`, `[data-ov-theme-layer="custom"] #schedule-content .bg-indigo-600 { background-color: var(--ov-schedule-task-bg) !important; }`,
		`[data-ov-theme-layer="custom"] #schedule-content .border-indigo-500 { border-color: var(--ov-schedule-task-border) !important; }`,
		`[data-ov-theme-layer="custom"] .chat-bubble-user-msg,`,
		`[data-ov-theme-layer="custom"] .chat-bubble-assistant-msg{`,
		`--chat-user-bg:var(--ov-surface-raised);`,
		`background:var(--ov-surface-raised)!important;`,
		`color:var(--ov-text)!important;`,
		`--chat-user-border:var(--ov-subtle-border);`,
		`border:1px solid var(--ov-subtle-border)!important;`,
		`[data-ov-theme-layer="custom"] .chat-input-container{background:var(--ov-surface-raised)!important;border:1px solid var(--ov-subtle-border)!important;`,
		`[data-ov-theme-layer="custom"] .chat-markdown blockquote{border-left-color:var(--ov-subtle-border)!important;`, `[data-ov-theme-layer="custom"] .stream-thinking summary{color:var(--ov-text-muted)!important`,
		`[data-ov-theme-layer="custom"] .stream-tool-body{background:var(--ov-surface)!important;`,
		`[data-ov-theme-layer="custom"] .ov-loading-dots{--ov-loading-dot-color:var(--ov-button-bg);`,
		`[data-ov-theme-layer="custom"] .ov-task-result-start-btn{color:var(--ov-link)!important`,
		`[data-ov-theme-layer="custom"] #selection-counter{background:var(--ov-button-bg)!important;`,
		`[data-ov-theme-layer="custom"] .stt-recording{background:var(--ov-diff-delete-bg)!important;animation-name:ov-stt-pulse-vscode`,
		`@keyframes ov-stt-pulse-vscode{`,
		`[data-ov-theme-layer="custom"] .tabs-bordered,`,
		`[data-ov-theme-layer="custom"] .toggle,[data-ov-theme-layer="custom"] .toggle:hover,[data-ov-theme-layer="custom"] .toggle:focus{--tglbg:var(--ov-input-bg)!important;`,
		`[data-ov-theme-layer="custom"] .theme-toggle-pill{background:var(--ov-surface-raised)!important;border:1px solid var(--ov-border)!important`,
		`[data-ov-theme-layer="custom"] .btn-primary,`,
		`[data-ov-theme-layer="custom"] .ov-badge-default{--ov-btn-bg:var(--ov-button-bg);`,
		`--ov-btn-hover-bg:var(--ov-button-hover-bg);`,
		`[data-ov-theme-layer="custom"] .btn-secondary,`,
		`--ov-btn-bg:var(--ov-button-secondary-bg);`,
		`[data-ov-theme-layer="custom"] .btn-info{--ov-btn-bg:var(--ov-info);--ov-btn-fg:var(--ov-info-foreground);`,
		`[data-ov-theme-layer="custom"] .btn-success{--ov-btn-bg:var(--ov-success);--ov-btn-fg:var(--ov-success-foreground);`,
		`[data-ov-theme-layer="custom"] .btn-warning{--ov-btn-bg:var(--ov-warning);--ov-btn-fg:var(--ov-warning-foreground);`,
		`[data-ov-theme-layer="custom"] .btn-error{--ov-btn-bg:var(--ov-error);--ov-btn-fg:var(--ov-error-foreground);`,
		`[data-ov-theme-layer="custom"] .btn:not(.btn-link):not(.btn-ghost):not(.btn-outline),`,
		`background:var(--ov-btn-bg,var(--ov-surface-raised))!important;`,
		`background:var(--ov-btn-hover-bg,var(--ov-hover-bg))!important;`,
		`[data-ov-theme-layer="custom"] .btn-outline:hover:not(:disabled):not(.btn-disabled){background:var(--ov-btn-hover-bg,var(--ov-button-hover-bg))!important;`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("base theme system missing %q", fragment)
		}
	}
	if strings.Contains(html, `[data-ov-theme-layer="custom"] [data-automation-yaml-indent-rail]{background-color:var(--ov-yaml-indent-rail,var(--ov-border-strong))!important}`) {
		t.Fatal("imported-theme YAML rail CSS must not override the active inline rail color with !important")
	}
	if strings.Contains(html, `[data-ov-theme-layer="custom"]{--ov-subtle-border:var(--ov-surface-raised)`) {
		t.Fatal("imported-theme subtle borders must not inherit the raised-surface color")
	}
	if strings.Contains(html, `--ov-subtle-border:color-mix(`) {
		t.Fatal("imported-theme subtle borders must use precomputed palette colors for older webviews")
	}
	if strings.Contains(html, "highlight-github-dark.min.css") {
		t.Fatal("base layout must not load the fixed GitHub Dark Highlight.js theme")
	}
	if strings.Contains(html, `[data-ov-theme-layer="custom"] [data-automation-yaml-indent-rail]{background-color:var(--ov-yaml-indent-rail,var(--ov-subtle-border))`) {
		t.Fatal("imported-theme YAML rails must not fall back to the surface-raised alias")
	}
	chatUserStart := strings.Index(html, `[data-ov-theme-layer="custom"] .chat-bubble-user-msg,[data-ov-theme-layer="custom"] .chat-bubble-assistant-msg{`)
	if chatUserStart < 0 {
		t.Fatal("expected imported-theme shared chat-bubble override")
	}
	chatUserEnd := strings.Index(html[chatUserStart:], `[data-ov-theme-layer="custom"] .chat-input-container`)
	if chatUserEnd < 0 {
		t.Fatal("expected imported-theme chat input override after bubble styles")
	}
	chatUserCSS := html[chatUserStart : chatUserStart+chatUserEnd]
	if strings.Contains(chatUserCSS, `background-color: var(--ov-button-bg)`) || strings.Contains(chatUserCSS, `background:var(--ov-button-bg)`) || strings.Contains(chatUserCSS, `color: var(--ov-button-fg)`) || strings.Contains(chatUserCSS, `color:var(--ov-button-fg)`) {
		t.Fatal("imported-theme user chat bubbles should stay neutral surfaces, not primary/accent button fills")
	}
	if strings.Contains(chatUserCSS, `.chat-bubble-user-msg{`) || strings.Contains(chatUserCSS, `--ov-border-strong`) {
		t.Fatal("imported-theme user chat bubbles should not receive a separate high-contrast border")
	}
	chatInputStart := strings.Index(html, `[data-ov-theme-layer="custom"] .chat-input-container{`)
	if chatInputStart < 0 {
		t.Fatal("expected imported-theme chat input CSS block")
	}
	chatInputEnd := strings.Index(html[chatInputStart:], `[data-ov-theme-layer="custom"] .chat-bubble-user-msg code`)
	if chatInputEnd < 0 {
		t.Fatal("expected imported-theme chat input CSS block before chat code styles")
	}
	chatInputCSS := html[chatInputStart : chatInputStart+chatInputEnd]
	if strings.Contains(chatInputCSS, `border:1px solid var(--ov-border)`) {
		t.Fatal("imported-theme chat input should use the same subtle border as chat bubbles")
	}
	dropzoneBaseline := strings.Index(html, `[data-ov-theme-layer="custom"] .task-drop-zone { border-color: transparent !important; }`)
	dropzoneDragOver := strings.Index(html, `[data-ov-theme-layer="custom"] .task-drop-zone.drag-over { border-color: var(--ov-focus) !important; background-color: var(--ov-selection-bg) !important; }`)
	if dropzoneBaseline < 0 || dropzoneDragOver < 0 || dropzoneDragOver < dropzoneBaseline {
		t.Fatal("imported-theme task dropzone drag-over border must override the transparent baseline while preserving dashed geometry")
	}
	for _, broadOverride := range []string{`[data-color-theme] { background-color: var(--ov-content-bg); }`,
		`[data-color-theme] .sidebar-aside { background-color: var(--ov-sidebar-bg); border-color: var(--ov-border); }`,
		`[data-color-theme] .card,`,
		`[data-color-theme] .border-base-300,`,
		`[data-color-theme] .modal-box {`,
		`[data-color-theme] .drag-over {`,
		`[data-color-theme] .task-selected {`,
		`[data-color-theme] .category-drop-zone,`,
		`[data-color-theme] #schedule-timeline-container {`,
		`[data-color-theme] [data-automation-yaml-panel] {`,
		`[data-color-theme] .chat-bubble-user-msg {`,
		`[data-color-theme] .chat-bubble-assistant-msg {`,
		`[data-color-theme] .chat-input-container {`,
		`[data-color-theme] .stream-thinking summary {`,
		`[data-color-theme] .ov-loading-dots {`,
		`[data-color-theme] .ov-task-result-start-btn {`,
		`[data-color-theme] #selection-counter {`,
		`[data-color-theme] .toggle {`,
		`[data-color-theme] .theme-toggle-pill {`,
		"[data-color-theme] {\n--ov-account-limit-track:",
		"[data-color-theme] {\n--ov-account-limit-fill:",
		`[data-color-theme] .btn-primary,`, `[data-color-theme] .btn-ghost:hover,`,
	} {
		if strings.Contains(html, broadOverride) {
			t.Fatalf("native OpenVibely defaults must not be captured by broad exact-theme override %q", broadOverride)
		}
	}
}

func TestImportedThemeSubtleBordersBlendWithCardSurface(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render Base: %v", err)
	}
	html := buf.String()
	for _, fragment := range []string{
		`[data-ov-theme-layer="custom"]{background-color:var(--ov-content-bg)}`,
		`[data-ov-theme-layer="custom"] .card,`,
		`[data-ov-theme-layer="custom"] .dropdown-content:not(.ov-menu){background-color:var(--ov-surface)!important;border-color:var(--ov-subtle-border)!important}`,
		`[data-color-theme] .hljs * { color: var(--ov-syntax-foreground); }`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("imported-theme styling missing %q", fragment)
		}
	}
	if strings.Contains(html, `--ov-subtle-border:color-mix(`) {
		t.Fatal("imported-theme subtle borders must use precomputed palette colors for older webviews")
	}
	if strings.Contains(html, "highlight-github-dark.min.css") {
		t.Fatal("base layout must not load the fixed GitHub Dark Highlight.js theme")
	}
	if strings.Contains(html, `[data-ov-theme-layer="custom"]{--ov-subtle-border:var(--ov-surface-raised)`) {
		t.Fatal("imported-theme subtle borders must not inherit the raised-surface color")
	}
}

func TestBuiltInThemeRuntimeMigratesPersistsAndToggles(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render Base: %v", err)
	}
	html := buf.String()
	marker := "var exactKey = 'openvibely.colorTheme';"
	idx := strings.Index(html, marker)
	if idx < 0 {
		t.Fatal("theme init script not rendered")
	}
	start := strings.LastIndex(html[:idx], "<script>")
	if start < 0 {
		t.Fatal("theme init script start not found")
	}
	start += len("<script>")
	endRel := strings.Index(html[start:], "</script>")
	if endRel < 0 {
		t.Fatal("theme init script end not found")
	}
	themeScript := html[start : start+endRel]
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the rendered built-in theme script")
	}
	js := `
	const themeScript = ` + strconv.Quote(themeScript) + `;
	const catalogJSON = ` + strconv.Quote(themes.RuntimeCatalogJSON()) + `;
	function toggleEl() { return { attrs: {}, setAttribute(k, v) { this.attrs[k] = String(v); } }; }
	function run(initialStore, systemLight, initialToggles) {
	  const store = Object.assign({}, initialStore || {});
	  const attrs = {};
	  const styleVars = {};
	  const listeners = {};
	  const toggles = initialToggles || [];
	  global.localStorage = {
	    getItem(k) { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
	    setItem(k, v) { store[k] = String(v); },
	    removeItem(k) { delete store[k]; }
	  };
	  global.window = { location: { protocol: 'http:' }, matchMedia() { return { matches: !!systemLight }; } };
	  global.document = {
	    readyState: 'loading',
	    addEventListener(name, fn) { listeners[name] = fn; },
	    documentElement: {
	      setAttribute(k, v) { attrs[k] = String(v); },
	      getAttribute(k) { return attrs[k] || ''; },
	      style: { setProperty(k, v) { if (!/^--ov-[a-z0-9-]+$/.test(k)) throw new Error('unsafe css var ' + k); styleVars[k] = String(v); } }
	    },
	    getElementById(id) { return id === 'ov-theme-catalog' ? { getAttribute() { return catalogJSON; } } : null; },
	    querySelectorAll(selector) { return selector === '[data-theme-mode-toggle]' ? toggles : []; }
	  };
	  eval(themeScript);
	  return { store, attrs, styleVars, toggles, listeners, api: window.openVibelyThemes };
	}let legacy = run({ theme: 'light' }, false);
if (legacy.attrs['data-theme'] !== 'light') throw new Error('legacy light mode did not migrate to light mode');
if (legacy.attrs['data-color-theme'] !== 'openvibely-light') throw new Error('legacy light mode did not choose OpenVibely light theme');
if (legacy.attrs['data-ov-theme-layer'] !== 'native') throw new Error('OpenVibely light should use the native theme layer');
if (Object.prototype.hasOwnProperty.call(legacy.store, 'openvibely.colorTheme')) throw new Error('legacy migration should not create an exact theme preference');
if (!legacy.styleVars['--ov-bg'] || !legacy.styleVars['--ov-syntax-keyword'] || !legacy.styleVars['--ov-automation-node-border'] || !legacy.styleVars['--ov-automation-edge'] || !legacy.styleVars['--ov-yaml-indent-rail']) throw new Error('theme css variables were not applied before paint');
let lateToggle = toggleEl();legacy.toggles.push(lateToggle);
legacy.api.syncThemeControls();
if (lateToggle.attrs['aria-pressed'] !== 'false') throw new Error('late light-mode toggle aria state was not synchronized');
if (lateToggle.attrs['title'] !== 'Switch to last dark theme') throw new Error('late light-mode toggle title was not synchronized');
if (lateToggle.attrs['aria-label'] !== 'Switch to last dark theme') throw new Error('late light-mode toggle aria label was not synchronized');
if (lateToggle.attrs['data-tip'] !== 'Switch to last dark theme') throw new Error('late light-mode toggle tooltip was not synchronized');
let invalid = run({ 'openvibely.colorTheme': 'missing-theme-id' }, false);
if (Object.prototype.hasOwnProperty.call(invalid.store, 'openvibely.colorTheme')) throw new Error('invalid exact theme id was not removed');
if (invalid.attrs['data-color-theme'] !== 'openvibely-dark') throw new Error('invalid exact theme did not fall back to OpenVibely dark');
let selected = run({}, false, [toggleEl()]);
selected.api.apply('vscode-default-high-contrast');
if (selected.attrs['data-theme'] !== 'dark') throw new Error('high-contrast dark did not retain dark mode');
if (selected.attrs['data-color-theme-kind'] !== 'high-contrast-dark') throw new Error('high-contrast dark kind was lost');
if (selected.attrs['data-ov-theme-layer'] !== 'custom') throw new Error('imported theme did not enable the custom theme layer');
selected.api.apply('catppuccin-mocha');
if (selected.attrs['data-ov-theme-layer'] !== 'custom' || selected.attrs['data-color-theme'] !== 'catppuccin-mocha') throw new Error('palette theme did not enable the custom theme layer');
selected.api.apply('openvibely-dark');
if (selected.attrs['data-ov-theme-layer'] !== 'native') throw new Error('OpenVibely default did not restore the native theme layer');
selected.api.apply('vscode-default-high-contrast');
if (selected.store['openvibely.colorTheme'] !== 'vscode-default-high-contrast') throw new Error('selection did not persist exact high-contrast theme');
selected.store['openvibely.lastLightTheme'] = 'vscode-default-high-contrast-light';
selected.api.toggleMode();
if (selected.attrs['data-theme'] !== 'light') throw new Error('toggle did not switch to light mode');
if (selected.attrs['data-color-theme'] !== 'vscode-default-high-contrast-light') throw new Error('toggle did not use remembered light theme');
if (selected.attrs['data-color-theme-kind'] !== 'high-contrast-light') throw new Error('high-contrast light kind was lost');
if (selected.store['openvibely.colorTheme'] !== 'vscode-default-high-contrast-light') throw new Error('toggle did not persist exact light theme');
if (selected.toggles[0].attrs['aria-pressed'] !== 'false') throw new Error('toggle aria state did not reflect light mode');
`
	if output, err := exec.Command(node, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("rendered built-in theme runtime failed: %v\n%s", err, output)
	}
}

func TestVSCodeThemeAttributionDocumentsFallbackRules(t *testing.T) {
	data, err := os.ReadFile("../../../docs/licenses/vscode-built-in-themes.md")
	if err != nil {
		t.Fatalf("read attribution doc: %v", err)
	}
	doc := string(data)
	for _, fragment := range []string{
		"## Semantic Color Mapping",
		"Missing, transparent, or invalid upstream colors are not emitted directly",
		"Muted text and borders are derived by mixing validated foreground and background colors",
		"Button foregrounds, button hover foregrounds, secondary button colors, and status button foregrounds fall back",
		"Selection, hover, and diff backgrounds fall back to generated `rgba(...)` colors",
		"Code-block and syntax colors prefer VS Code editor and token scopes",
	} {
		if !strings.Contains(doc, fragment) {
			t.Fatalf("attribution doc missing fallback/derivation fragment %q", fragment)
		}
	}
}

func TestSidebarThemesLinkIsMainNavigationItem(t *testing.T) {
	var buf bytes.Buffer
	if err := Sidebar([]models.Project{}, "project-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render Sidebar: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `data-nav-base="/themes"`) || !strings.Contains(html, `>Themes</span>`) {
		t.Fatal("sidebar missing main Themes nav item")
	}
	footerStart := strings.Index(html, `id="sidebar-user-menu"`)
	if footerStart == -1 {
		t.Fatal("sidebar user menu not rendered")
	}
	if strings.Contains(html[footerStart:], `data-nav-base="/themes"`) {
		t.Fatal("Themes nav item must not be rendered inside the footer user menu")
	}
}

func TestSidebarFooterToggleUsesThemeModeSwitch(t *testing.T) {
	var buf bytes.Buffer
	if err := Sidebar([]models.Project{}, "project-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render Sidebar: %v", err)
	}
	html := buf.String()
	for _, fragment := range []string{
		`data-theme-mode-toggle`,
		`Switch theme mode`,
		`aria-pressed="false"`,
		`aria-label="Switch theme mode"`,
		`onkeydown="if (event.key === 'Enter' || event.key === ' ')`,
		`window.toggleTheme(); return false;`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("sidebar theme toggle missing %q", fragment)
		}
	}
}
