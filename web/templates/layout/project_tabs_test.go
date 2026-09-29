package layout

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestProjectBrandIsNonSelectableDragRegion(t *testing.T) {
	var buf bytes.Buffer
	if err := DesktopProjectTabs(nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `.project-bar-brand { cursor: default; -webkit-user-select: none; user-select: none; --wails-draggable: drag;`) {
		t.Error("app name must be a non-selectable native drag region, including WebKit")
	}
}

func TestProjectTitlebarDisablesWebKitSelection(t *testing.T) {
	var buf bytes.Buffer
	if err := DesktopProjectTabs(nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `--wails-draggable: drag; cursor: default; -webkit-user-select: none; user-select: none; --project-tab-selected:`) {
		t.Error("titlebar empty drag regions must retain the arrow cursor and disable WebKit selection")
	}
}

func TestProjectTabsReserveSidebarWidth(t *testing.T) {
	var buf bytes.Buffer
	if err := DesktopProjectTabs(nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		`#desktop-project-titlebar { padding-left: 16rem; }`,
		`body.sidebar-collapsed-pending #desktop-project-titlebar { padding-left: 3.5rem; }`,
		`padding-left: max(3.5rem, 128px);`,
	} {
		if !strings.Contains(buf.String(), rule) {
			t.Errorf("missing sidebar-aligned tab layout: %s", rule)
		}
	}
}

func TestProjectTabsPlusHoverPreservesInactiveSeparator(t *testing.T) {
	var buf bytes.Buffer
	if err := DesktopProjectTabs(nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `.desktop-project-tab:not(:has([aria-selected="true"])):has(+ [data-project-selector] #project-selector-trigger:hover)::after { opacity: 0; }`) {
		t.Error("plus hover must not hide the neighboring separator")
	}
}

func TestProjectTabsCleanSelectedShape(t *testing.T) {
	var buf bytes.Buffer
	if err := DesktopProjectTabs(nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "font-weight: 600") {
		t.Error("selected tab must not use bold text")
	}
	for _, want := range []string{
		`var(--project-tab-contour) 10px, var(--project-tab-selected) 10px)`,
		`color-mix(in oklch, oklch(var(--bc)) 70%, white)`,
		`color-mix(in oklch, oklch(var(--bc)) 70%, black)`,
		`[data-theme="light"] #desktop-project-titlebar { --project-tab-contour: var(--ov-l-border); }`,
		`border-width: 1px 2px 7px; padding-top: 0; padding-bottom: 0; padding-left: 14px; padding-right: 38px;`,
		`padding: 0 40px 6px 16px;`,
		`top: calc(50% - 3px);`,
		`--project-tab-contour: oklch(var(--b1))`,
		`box-shadow: inset 0 -2px var(--project-tab-contour)`,
		`border: 2px solid var(--project-tab-contour); border-bottom: 0`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing active-tab contour: %s", want)
		}
	}
}

func TestProjectTabsHaveNoHoverTooltips(t *testing.T) {
	ctx := WithUIPreferences(WithDesktopMode(context.Background(), true), UIPreferences{PinnedProjectIDs: []string{"a"}})
	var buf bytes.Buffer
	if err := DesktopProjectTabs([]models.Project{{ID: "a", Name: "Alpha"}}, "a").Render(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, fragment := range []string{`title="Alpha"`, `title="Close tab"`, `tab.title =`, `close.title =`} {
		if strings.Contains(html, fragment) {
			t.Errorf("unexpected tab tooltip: %s", fragment)
		}
	}
	for _, fragment := range []string{`title="Open project"`, `aria-label="Close project tab: Alpha"`} {
		if !strings.Contains(html, fragment) {
			t.Errorf("missing retained tooltip or accessible label: %s", fragment)
		}
	}
}

func TestProjectTabsPlatformRendering(t *testing.T) {
	projects := []models.Project{{ID: "a", Name: "Alpha"}, {ID: "b", Name: "Beta"}}
	for _, desktop := range []bool{false, true} {
		ctx := WithUIPreferences(WithDesktopMode(context.Background(), desktop), UIPreferences{PinnedProjectIDs: []string{"b", "deleted", "a", "b"}})
		var buf bytes.Buffer
		if err := Base("Tasks", projects, "a").Render(ctx, &buf); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if !strings.Contains(html, `id="desktop-project-titlebar"`) {
			t.Fatalf("wrong titlebar for desktop=%v", desktop)
		}
		if strings.Count(html, `id="project-selector"`) != 1 {
			t.Fatal("must render exactly one shared selector")
		}
		sidebar := html[strings.Index(html, `<aside id="sidebar"`):]
		sidebar = sidebar[:strings.Index(sidebar, `</aside>`)]
		if strings.Contains(sidebar, `id="project-selector"`) == desktop {
			t.Fatal("selector must be in sidebar only on web")
		}
		if desktop {
			for _, want := range []string{`import * as runtime from '/wails/runtime.js'`, `window.wails = runtime`, `data-pinned-projects="[&#34;b&#34;,&#34;a&#34;]"`, `data-project-tab="a"`, `role="tablist"`, `--wails-draggable: drag`, `--wails-draggable: no-drag`, `overflow-x: auto`, `:focus-visible`, `aria-selected="true"`} {
				if !strings.Contains(html, want) {
					t.Errorf("missing %s", want)
				}
			}
		}
	}
}

func TestMacFullscreenPresentationAndGlyphs(t *testing.T) {
	source, err := os.ReadFile("../../../cmd/desktop/fullscreen_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"window:willUseFullScreenPresentationOptions:", "ovConfigureFullscreenDelegate", "NSApplicationPresentationHideMenuBar", "NSApplicationPresentationHideDock", "NSApplicationDidResignActiveNotification", "NSWindowWillExitFullScreenNotification"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("missing fullscreen lifecycle contract: %s", want)
		}
	}
	source, err = os.ReadFile("project_tabs.templ")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"common:WindowFullscreen", "common:WindowUnFullscreen", "data-fullscreen-glyph", "data-restore-glyph"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("missing stateful traffic-light glyph: %s", want)
		}
	}
}

func TestMacTrafficLightGeometry(t *testing.T) {
	source, err := os.ReadFile("project_tabs.templ")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`box-shadow: inset 0 0 0 .5px`,
		`transform: translate(-50%, -50%)`,
		`stroke-width="1" stroke-linecap="round"`,
		`d="M3.5 3.5l5 5m0-5-5 5"`,
		`d="M2.5 6h7"`,
		`d="M2.5 2.5h4l-4 4zm7 7h-4l4-4z"`,
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("missing traffic-light geometry: %s", want)
		}
	}
}

func TestProjectTabsNativeWindowContract(t *testing.T) {
	source, err := os.ReadFile("../../../cmd/desktop/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Frameless: true`, `CornerRadius: 20`, `httputil.NewSingleHostReverseProxy`, `Handler: proxy`} {
		if !strings.Contains(strings.Join(strings.Fields(string(source)), " "), want) {
			t.Errorf("missing native window configuration: %s", want)
		}
	}
	source, err = os.ReadFile("project_tabs.templ")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-wml-window="Close"`, `data-wml-window="Minimise"`, `data-wml-window="ToggleFullscreen"`, `desktop-traffic-lights`, `#ff5f57`, `#febc2e`, `#28c840`} {
		if !strings.Contains(string(source), want) {
			t.Errorf("missing app-drawn native-style control: %s", want)
		}
	}
}

func TestProjectTabsSidebarTogglePlacement(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		ctx := WithDesktopMode(context.Background(), desktop)
		var sidebar, bar bytes.Buffer
		if err := Sidebar(nil, "").Render(ctx, &sidebar); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sidebar.String(), `>OpenVibely</h1>`) {
			t.Error("sidebar must not render the redundant app heading")
		}
		hasToggle := strings.Contains(sidebar.String(), `id="sidebar-collapse-btn"`)
		if hasToggle == desktop {
			t.Errorf("sidebar toggle placement incorrect for desktop=%v", desktop)
		}
		if desktop {
			if err := DesktopProjectTabs(nil, "").Render(ctx, &bar); err != nil {
				t.Fatal(err)
			}
			toggle := strings.Index(bar.String(), `id="sidebar-collapse-btn"`)
			tabs := strings.Index(bar.String(), `id="desktop-project-tabs"`)
			if toggle < 0 || toggle >= tabs {
				t.Error("desktop toggle must precede tabs")
			}
		}
	}
}

func TestWebProjectBarResponsiveShell(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Projects", nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{`id="desktop-project-titlebar"`, `data-platform="web"`, `matchMedia('(min-width: 1024px)')`, `web-project-selector-home`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing responsive web shell %s", want)
		}
	}
	if strings.Contains(html, `<button type="button" data-wml-window=`) {
		t.Error("web must not render native window controls")
	}
}
