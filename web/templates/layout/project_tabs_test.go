package layout

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

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
			for _, want := range []string{`data-pinned-projects="[&#34;b&#34;,&#34;a&#34;]"`, `data-project-tab="a"`, `role="tablist"`, `--wails-draggable: drag`, `--wails-draggable: no-drag`, `overflow-x: auto`, `:focus-visible`, `aria-selected="true"`} {
				if !strings.Contains(html, want) {
					t.Errorf("missing %s", want)
				}
			}
		}
	}
}

func TestProjectTabsNativeWindowContract(t *testing.T) {
	source, err := os.ReadFile("../../../cmd/desktop/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Frameless: runtime.GOOS != "darwin"`, `application.MacTitleBarHidden`} {
		if !strings.Contains(string(source), want) {
			t.Errorf("missing native window configuration: %s", want)
		}
	}
	source, err = os.ReadFile("project_tabs.templ")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`if runtime.GOOS != "darwin"`, `data-wml-window="Minimise"`, `data-wml-window="ToggleMaximise"`, `data-wml-window="Close"`, `padding-left: 80px`} {
		if !strings.Contains(string(source), want) {
			t.Errorf("missing native window contract: %s", want)
		}
	}
}
