package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/themes"
)

func TestThemesContentRendersCatalogCardsAndSearchMetadata(t *testing.T) {
	var buf bytes.Buffer
	if err := ThemesContent(themes.Catalog()).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ThemesContent: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `data-card-search="themes"`) || !strings.Contains(html, "Search themes...") {
		t.Fatalf("themes page must use shared card search input")
	}
	if got, want := strings.Count(html, `data-theme-id=`), len(themes.Catalog())-18; got != want {
		t.Fatalf("theme card count = %d, want %d", got, want)
	}
	for _, theme := range themes.Catalog() {
		if !themeVisibleInPicker(theme.ID) {
			if strings.Contains(html, `data-theme-id="`+theme.ID+`"`) {
				t.Fatalf("hidden theme %s remains visible", theme.Name)
			}
			continue
		}
		if !strings.Contains(html, `data-theme-id="`+theme.ID+`"`) {
			t.Fatalf("rendered Themes page missing palette theme %q", theme.Name)
		}
	}
	for _, fragment := range []string{
		`data-theme-id="` + themes.OpenVibelyDarkThemeID + `"`,
		`data-theme-id="` + themes.OpenVibelyLightThemeID + `"`,
		`OpenVibely Dark`,
		`OpenVibely Light`,
		`OpenVibely built-in`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("rendered Themes page missing OpenVibely default fragment %q", fragment)
		}
	}
	if strings.Contains(html, `data-theme-id="openvibely-dark-flat`) {
		t.Fatal("Themes page must not list removed OpenVibely Dark Flat variants")
	}
	if !strings.Contains(html, `data-page-header`) || strings.Contains(html, "Choose an OpenVibely") {
		t.Fatal("Themes must use the shared page header without a subtitle")
	}
	for _, fragment := range []string{
		`data-search-text=`,
		`VS Code built-in theme-defaults`,
		`data-theme-active-badge`,
		`window.openVibelyThemes.refreshThemeCards = refreshThemeCards`,
		`window.openVibelyThemes.apply(id)`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("themes page missing behavior fragment %q", fragment)
		}
	}
	for _, fragment := range []string{`grid.appendChild(card)`, `cards.sort(function`} {
		if strings.Contains(html, fragment) {
			t.Fatalf("theme selection must not reorder cards; found %q", fragment)
		}
	}
}

func TestThemeCardPreviewUsesOwnPaletteNotRootVariables(t *testing.T) {
	catalog := themes.Catalog()
	if len(catalog) < 2 {
		t.Fatal("theme catalog too small")
	}
	var buf bytes.Buffer
	if err := ThemesContent(catalog[:2]).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ThemesContent: %v", err)
	}
	html := buf.String()
	first := catalog[0]
	second := catalog[1]
	for _, color := range []string{first.Semantic["bg"], first.Semantic["sidebarBg"], first.Semantic["primary"], first.Syntax["keyword"], second.Semantic["bg"], second.Syntax["string"]} {
		if color == "" || !strings.Contains(html, color) {
			t.Fatalf("preview did not contain generated palette color %q", color)
		}
	}
	if strings.Contains(html, `style="background:var(--ov-`) || strings.Contains(html, `style="color:var(--ov-syntax`) {
		t.Fatal("theme previews must not use current root CSS variables")
	}
}

func TestImportedThemePreviewSurfaceFallbacks(t *testing.T) {
	for _, mode := range []string{"dark", "light"} {
		theme := themes.Theme{Mode: mode, Semantic: map[string]string{"contentBg": "#202020", "surface": "#303030", "surfaceRaised": "#404040", "text": "#eeeeee"}, CSS: map[string]string{"--ov-kanban-column-bg": "#101010"}}
		got := previewOuterStyle(theme)
		page := "color-mix(in srgb, #202020 92%, black)"
		if mode == "light" {
			page = "color-mix(in srgb, #202020 94%, #eeeeee)"
		}
		for _, want := range []string{"--tp-default-frame:#101010", "--tp-default-page:#202020", "--tp-default-rail:#303030", "--tp-layered-frame:color-mix(in srgb, #303030 94%, #eeeeee)", "--tp-layered-page:" + page} {
			if !strings.Contains(got, want) {
				t.Errorf("%s preview missing %s: %s", mode, want, got)
			}
		}
	}
}
