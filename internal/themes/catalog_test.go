package themes

import "testing"

func TestCatalogContainsOpenVibelyDefaultsAndPinnedVSCodeThemes(t *testing.T) {
	catalog := Catalog()
	if len(catalog) != ExpectedThemeCount+2 {
		t.Fatalf("catalog has %d themes, want %d", len(catalog), ExpectedThemeCount+2)
	}
	if catalog[0].ID != OpenVibelyDarkThemeID || catalog[1].ID != OpenVibelyLightThemeID {
		t.Fatalf("OpenVibely defaults should be first in catalog, got %q then %q", catalog[0].ID, catalog[1].ID)
	}
	if len(VSCodeCatalog()) != ExpectedThemeCount {
		t.Fatalf("VS Code catalog has %d themes, want %d", len(VSCodeCatalog()), ExpectedThemeCount)
	}
	seenNames := map[string]bool{}
	for _, theme := range catalog {
		seenNames[theme.Name] = true
	}
	for _, name := range []string{"OpenVibely Dark", "OpenVibely Light"} {
		if !seenNames[name] {
			t.Fatalf("catalog missing OpenVibely default theme %q", name)
		}
	}
	for _, name := range ExpectedThemeNames {
		if !seenNames[name] {
			t.Fatalf("catalog missing pinned VS Code theme %q", name)
		}
	}
}

func TestCatalogThemeIDsKindsAndRequiredColors(t *testing.T) {
	validKinds := map[string]bool{
		KindLight: true, KindDark: true, KindHighContrastLight: true, KindHighContrastDark: true,
	}
	requiredSemantic := []string{"bg", "contentBg", "sidebarBg", "surface", "surfaceRaised", "text", "textMuted", "border", "borderStrong", "primary", "primaryForeground", "focus", "link", "inputBg", "inputBorder", "buttonBg", "buttonFg", "buttonHoverBg", "buttonHoverFg", "buttonSecondaryBg", "buttonSecondaryFg", "selectionBg", "hoverBg", "info", "infoForeground", "success", "successForeground", "warning", "warningForeground", "error", "errorForeground", "diffAddBg", "diffAddFg", "diffModifyBg", "diffModifyFg", "diffDeleteBg", "diffDeleteFg", "codeBg", "codeFg"}
	requiredSyntax := []string{"background", "foreground", "comment", "string", "number", "keyword", "function", "type", "variable", "title", "error"}
	seen := map[string]string{}
	for _, theme := range Catalog() {
		if theme.ID == "" || seen[theme.ID] != "" {
			t.Fatalf("theme IDs must be stable and unique; duplicate/empty ID %q for %q and %q", theme.ID, seen[theme.ID], theme.Name)
		}
		seen[theme.ID] = theme.Name
		if !validKinds[theme.Kind] {
			t.Fatalf("theme %s has invalid kind %q", theme.Name, theme.Kind)
		}
		if theme.Mode != "light" && theme.Mode != "dark" {
			t.Fatalf("theme %s has invalid mode %q", theme.Name, theme.Mode)
		}
		for _, key := range requiredSemantic {
			if theme.Semantic[key] == "" {
				t.Fatalf("theme %s missing semantic color %s", theme.Name, key)
			}
			if theme.CSS["--ov-"+camelToKebabForTest(key)] == "" {
				t.Fatalf("theme %s missing CSS var for semantic color %s", theme.Name, key)
			}
		}
		for _, key := range requiredSyntax {
			if theme.Syntax[key] == "" {
				t.Fatalf("theme %s missing syntax color %s", theme.Name, key)
			}
			if theme.CSS["--ov-syntax-"+camelToKebabForTest(key)] == "" {
				t.Fatalf("theme %s missing CSS var for syntax color %s", theme.Name, key)
			}
		}
		if theme.SearchText == "" || theme.Preview["bg"] == "" || theme.Preview["syntaxKeyword"] == "" {
			t.Fatalf("theme %s missing search metadata or preview palette", theme.Name)
		}
	}
	if _, ok := seen[DefaultLightThemeID]; !ok {
		t.Fatalf("missing default light theme %s", DefaultLightThemeID)
	}
	if _, ok := seen[DefaultDarkThemeID]; !ok {
		t.Fatalf("missing default dark theme %s", DefaultDarkThemeID)
	}
	if DefaultLightThemeID != OpenVibelyLightThemeID || DefaultDarkThemeID != OpenVibelyDarkThemeID {
		t.Fatalf("default fallback IDs must point at OpenVibely themes, got %s/%s", DefaultLightThemeID, DefaultDarkThemeID)
	}
}

func TestOpenVibelyDefaultThemesMatchOriginalSurfaceRoles(t *testing.T) {
	dark, ok := ByID(OpenVibelyDarkThemeID)
	if !ok {
		t.Fatal("missing OpenVibely Dark theme")
	}
	for key, want := range map[string]string{
		"contentBg":     "#191E24",
		"sidebarBg":     "#1D232A",
		"surface":       "#1D232A",
		"surfaceRaised": "#1D232A",
		"inputBg":       "#1D232A",
		"border":        "#15191E",
	} {
		if got := dark.Semantic[key]; got != want {
			t.Fatalf("OpenVibely Dark semantic %s = %s, want original value %s", key, got, want)
		}
		cssKey := "--ov-" + camelToKebabForTest(key)
		if got := dark.CSS[cssKey]; got != want {
			t.Fatalf("OpenVibely Dark CSS %s = %s, want original value %s", cssKey, got, want)
		}
	}
	if dark.Semantic["surface"] == "#2B3039" || dark.Semantic["surfaceRaised"] == "#2B3039" {
		t.Fatal("OpenVibely Dark must not use chat-bubble gray as the default card/modal surface")
	}

	light, ok := ByID(OpenVibelyLightThemeID)
	if !ok {
		t.Fatal("missing OpenVibely Light theme")
	}
	for key, want := range map[string]string{
		"contentBg":     "#F5F5F5",
		"sidebarBg":     "#FAFAFA",
		"surface":       "#FAFAFA",
		"surfaceRaised": "#FAFAFA",
		"inputBg":       "#FAFAFA",
		"codeBg":        "#0000001F",
	} {
		if got := light.Semantic[key]; got != want {
			t.Fatalf("OpenVibely Light semantic %s = %s, want original value %s", key, got, want)
		}
		cssKey := "--ov-" + camelToKebabForTest(key)
		if got := light.CSS[cssKey]; got != want {
			t.Fatalf("OpenVibely Light CSS %s = %s, want original value %s", cssKey, got, want)
		}
	}
	if light.Semantic["surface"] == "#FFFFFF" || light.Semantic["inputBg"] == "#FFFFFF" {
		t.Fatal("OpenVibely Light must use the original #FAFAFA app surface, not plain white")
	}
}

func TestCatalogRedThemeContentBackgroundUsesPalette(t *testing.T) {
	red, ok := ByID("vscode-red")
	if !ok {
		t.Fatal("missing Red theme")
	}
	if red.Semantic["bg"] != "#390000" {
		t.Fatalf("Red bg = %s, want upstream editor background", red.Semantic["bg"])
	}
	if red.Semantic["contentBg"] != "#390000" {
		t.Fatalf("Red contentBg = %s, want upstream editor background instead of dark fallback", red.Semantic["contentBg"])
	}
	if red.CSS["--ov-content-bg"] != "#390000" {
		t.Fatalf("Red --ov-content-bg = %s, want #390000", red.CSS["--ov-content-bg"])
	}
}

func TestCatalogHighContrastModeIdentity(t *testing.T) {
	darkHC, ok := ByID("vscode-default-high-contrast")
	if !ok || darkHC.Kind != KindHighContrastDark || darkHC.Mode != "dark" {
		t.Fatalf("dark high contrast identity not preserved: %#v", darkHC)
	}
	lightHC, ok := ByID("vscode-default-high-contrast-light")
	if !ok || lightHC.Kind != KindHighContrastLight || lightHC.Mode != "light" {
		t.Fatalf("light high contrast identity not preserved: %#v", lightHC)
	}
}

func camelToKebabForTest(s string) string {
	out := make([]rune, 0, len(s)+4)
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out = append(out, '-')
			}
			out = append(out, r+'a'-'A')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}
