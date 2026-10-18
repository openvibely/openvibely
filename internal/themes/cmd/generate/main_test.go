package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeSemanticUsesEditorBackgroundForAppContent(t *testing.T) {
	semantic := normalizeSemantic("dark", map[string]string{
		"editor.background":    "#390000",
		"editor.foreground":    "#F8F8F8",
		"sideBar.background":   "#330000",
		"button.background":    "#883333",
		"dropdown.background":  "#580000",
		"list.hoverBackground": "#800000",
	})
	if semantic["bg"] != "#390000" {
		t.Fatalf("bg = %s, want VS Code editor background", semantic["bg"])
	}
	if semantic["contentBg"] != "#390000" {
		t.Fatalf("contentBg = %s, want editor background instead of generic fallback", semantic["contentBg"])
	}
	if semantic["sidebarBg"] != "#330000" {
		t.Fatalf("sidebarBg = %s, want sidebar background", semantic["sidebarBg"])
	}
}

func TestNormalizeSemanticDerivesVisibleGraphAndYAMLRoles(t *testing.T) {
	semantic := normalizeSemantic("dark", map[string]string{
		"editor.background":           "#000C18",
		"textCodeBlock.background":    "#000C18",
		"editor.foreground":           "#6688CC",
		"descriptionForeground":       "#425D8D",
		"contrastBorder":              "#2B2B4A",
		"dropdown.background":         "#181F2F",
		"editorLineNumber.foreground": "#425D8D",
	})

	for _, role := range []struct {
		name       string
		background string
		minimum    float64
	}{
		{name: "yamlIndentRail", background: "codeBg", minimum: 1.5},
		{name: "automationNodeBorder", background: "surfaceRaised", minimum: 1.5},
		{name: "automationEdge", background: "surface", minimum: 3.0},
	} {
		if got := contrastRatio(semantic[role.name], semantic[role.background]); got < role.minimum {
			t.Fatalf("%s contrast = %.2f, want at least %.2f", role.name, got, role.minimum)
		}
		if semantic[role.name] == semantic[role.background] {
			t.Fatalf("%s must not reuse its drawing surface color %s", role.name, semantic[role.background])
		}
	}
}

func TestResolveThemeResolvesIncludesAndOverrides(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.json")
	child := filepath.Join(dir, "child.json")
	if err := os.WriteFile(base, []byte(`{
		// JSONC comment proves the generator accepts VS Code theme files.
		"colors": {
			"editor.background": "#111111",
			"editor.foreground": "#eeeeee",
			"button.background": "#123456"
		},
		"tokenColors": [
			{"scope": "comment", "settings": {"foreground": "#777777"}},
		]
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte(`{
		"include": "./base.json",
		"colors": {"button.background": "#abcdef"},
		"tokenColors": [{"scope": "string", "settings": {"foreground": "#00ff00"}}]
	}`), 0644); err != nil {
		t.Fatal(err)
	}
	resolved, includes, err := resolveTheme(child, map[string]bool{})
	if err != nil {
		t.Fatalf("resolveTheme returned error: %v", err)
	}
	if includes != 1 {
		t.Fatalf("include count = %d, want 1", includes)
	}
	if resolved.Colors["editor.background"] != "#111111" {
		t.Fatalf("base color was not inherited: %#v", resolved.Colors)
	}
	if resolved.Colors["button.background"] != "#abcdef" {
		t.Fatalf("child color did not override base: %#v", resolved.Colors)
	}
	if len(resolved.TokenColors) != 2 {
		t.Fatalf("token colors were not appended across include chain: got %d", len(resolved.TokenColors))
	}
}

func TestNormalizeSyntaxPrefersMostSpecificScopeOverSubstringMatches(t *testing.T) {
	var tokens []tokenColor
	add := func(scope any, fg string) {
		tc := tokenColor{Scope: scope}
		tc.Settings.Foreground = fg
		tokens = append(tokens, tc)
	}
	add("string", "#CE9178")
	add([]any{"keyword", "storage.type"}, "#569CD6")
	add("keyword.control", "#C586C0")
	add([]any{"entity.name.type", "support.class"}, "#4EC9B0")
	add("storage.type.string.go", "#4EC9B0")
	add("keyword.operator.class", "#D16969")
	add("meta.embedded string", "#123456")
	add("variable.other.readwrite", "#9CDCFE")
	add("string.quoted.docstring.multi.python", "#D16969")

	syntax := normalizeSyntax("dark", map[string]string{}, tokens)
	for role, want := range map[string]string{"string": "#CE9178", "keyword": "#C586C0", "type": "#4EC9B0", "variable": "#9CDCFE"} {
		if syntax[role] != want {
			t.Fatalf("%s = %s, want %s", role, syntax[role], want)
		}
	}
}

func TestNormalizeSemanticSelectionKeepsTextReadable(t *testing.T) {
	semantic := normalizeSemantic("high-contrast-dark", map[string]string{
		"editor.background":          "#000000",
		"foreground":                 "#FFFFFF",
		"editor.selectionBackground": "#FFFFFF",
		"selection.background":       "#008000",
	})
	if semantic["selectionBg"] != "#008000" {
		t.Fatalf("selectionBg = %s, want readable selection.background fallback", semantic["selectionBg"])
	}

	semantic = normalizeSemantic("dark", map[string]string{
		"editor.background":              "#000C18",
		"foreground":                     "#6688CC",
		"editor.selectionBackground":     "#770811",
		"list.activeSelectionBackground": "#08286B",
	})
	if semantic["selectionBg"] != "#08286B" {
		t.Fatalf("selectionBg = %s, want list selection color", semantic["selectionBg"])
	}
}

func TestNormalizeSemanticLinkStaysReadableWithoutTextLinkColor(t *testing.T) {
	semantic := normalizeSemantic("dark", map[string]string{
		"editor.background": "#000C18",
		"foreground":        "#6688CC",
		"button.background": "#2B3C5D",
		"focusBorder":       "#596F99",
	})
	if semantic["link"] == "#2B3C5D" {
		t.Fatal("link fell back to the dark button background")
	}
	if got := contrastRatio(semantic["link"], semantic["contentBg"]); got < 4.5 {
		t.Fatalf("link %s contrast = %.2f, want >= 4.5", semantic["link"], got)
	}

	semantic = normalizeSemantic("dark", map[string]string{
		"editor.background":   "#1E1E1E",
		"textLink.foreground": "#3794FF",
		"button.background":   "#0E639C",
	})
	if semantic["link"] != "#3794FF" {
		t.Fatalf("link = %s, want readable textLink.foreground", semantic["link"])
	}
}

func TestNormalizeSemanticErrorSkipsNearWhiteTint(t *testing.T) {
	semantic := normalizeSemantic("dark", map[string]string{
		"editor.background":      "#390000",
		"foreground":             "#F8F8F8",
		"errorForeground":        "#FFEAEA",
		"editorError.foreground": "#FF6666",
	})
	if semantic["error"] != "#FF6666" {
		t.Fatalf("error = %s, want editorError.foreground", semantic["error"])
	}

	semantic = normalizeSemantic("dark", map[string]string{
		"editor.background": "#1E1E1E",
		"errorForeground":   "#F48771",
	})
	if semantic["error"] != "#F48771" {
		t.Fatalf("error = %s, want themed errorForeground", semantic["error"])
	}
}

func TestBestTextPicksHigherContrastForStatusColors(t *testing.T) {
	for _, tc := range []struct{ bg, want string }{
		{"#F85149", "#000000"},
		{"#3794FF", "#000000"},
		{"#0E639C", "#FFFFFF"},
		{"#1E1E1E", "#FFFFFF"},
		{"#FFFFFF", "#000000"},
	} {
		if got := bestText(tc.bg); got != tc.want {
			t.Errorf("bestText(%s) = %s, want %s", tc.bg, got, tc.want)
		}
	}
}

func TestNormalizeSemanticStatusForegroundsAreReadable(t *testing.T) {
	semantic := normalizeSemantic("dark", map[string]string{
		"editor.background": "#1E1E1E",
		"editor.foreground": "#D4D4D4",
		"button.background": "#2AA19899",
		"errorForeground":   "#F85149",
	})
	if got := semantic["errorForeground"]; got != "#000000" {
		t.Fatalf("errorForeground = %s, want #000000 on #F85149", got)
	}
	for _, pair := range [][2]string{{"errorForeground", "error"}, {"infoForeground", "info"}, {"successForeground", "success"}, {"warningForeground", "warning"}, {"primaryForeground", "primary"}} {
		bg := composite(semantic[pair[1]], semantic["contentBg"])
		if ratio := contrastRatio(semantic[pair[0]], bg); ratio < 4.5 {
			t.Errorf("%s %s on %s contrast %.2f < 4.5", pair[0], semantic[pair[0]], bg, ratio)
		}
	}
}
