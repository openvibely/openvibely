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
