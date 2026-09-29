package layout

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

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
		`[data-theme="light"] #desktop-project-titlebar { --project-tab-contour: #FAFAFA; }`,
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
		if strings.Contains(html, `id="desktop-project-titlebar"`) != desktop {
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
