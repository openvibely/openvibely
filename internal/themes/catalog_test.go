package themes

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
)

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
	requiredSemantic := []string{"bg", "contentBg", "sidebarBg", "surface", "surfaceRaised", "text", "textMuted", "border", "borderStrong", "automationNodeBorder", "automationEdge", "yamlIndentRail", "primary", "primaryForeground", "focus", "link", "inputBg", "inputBorder", "buttonBg", "buttonFg", "buttonHoverBg", "buttonHoverFg", "buttonSecondaryBg", "buttonSecondaryFg", "selectionBg", "hoverBg", "info", "infoForeground", "success", "successForeground", "warning", "warningForeground", "error", "errorForeground", "diffAddBg", "diffAddFg", "diffModifyBg", "diffModifyFg", "diffDeleteBg", "diffDeleteFg", "codeBg", "codeFg"}
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

func TestCatalogAutomationGraphAndYAMLContrastRoles(t *testing.T) {
	for _, theme := range VSCodeCatalog() {
		if got := contrastRatioForTest(theme.Semantic["yamlIndentRail"], theme.Semantic["codeBg"]); got < 1.5 {
			t.Fatalf("theme %s YAML rail contrast = %.2f, want at least 1.50 against code canvas", theme.Name, got)
		}
		if got := contrastRatioForTest(theme.Semantic["automationNodeBorder"], theme.Semantic["surfaceRaised"]); got < 1.5 {
			t.Fatalf("theme %s Automation node border contrast = %.2f, want at least 1.50 against node fill", theme.Name, got)
		}
		if got := contrastRatioForTest(theme.Semantic["automationEdge"], theme.Semantic["surface"]); got < 3.0 {
			t.Fatalf("theme %s Automation connector contrast = %.2f, want at least 3.00 against graph canvas", theme.Name, got)
		}
	}
}

func TestRuntimeCatalogCarriesContrastRolesInCompactBootstrapShape(t *testing.T) {
	type runtimeTheme struct {
		ID    string            `json:"id"`
		CSS   map[string]string `json:"css"`
		Roles [3]string         `json:"r"`
	}
	var runtime []runtimeTheme
	if err := json.Unmarshal([]byte(RuntimeCatalogJSON()), &runtime); err != nil {
		t.Fatalf("runtime catalog JSON: %v", err)
	}
	expected := make(map[string]Theme, len(Catalog()))
	for _, theme := range Catalog() {
		expected[theme.ID] = theme
	}
	if len(runtime) != len(expected) {
		t.Fatalf("runtime catalog has %d themes, want %d", len(runtime), len(expected))
	}
	roleNames := []string{"--ov-automation-node-border", "--ov-automation-edge", "--ov-yaml-indent-rail"}
	for _, theme := range runtime {
		expectedTheme, ok := expected[theme.ID]
		if !ok {
			t.Fatalf("runtime catalog contains unknown theme %s", theme.ID)
		}
		for index, name := range roleNames {
			if theme.Roles[index] == "" {
				t.Fatalf("runtime theme %s is missing compact contrast role %d", theme.ID, index)
			}
			if theme.Roles[index] != expectedTheme.CSS[name] {
				t.Fatalf("runtime theme %s role %s = %q, want %q", theme.ID, name, theme.Roles[index], expectedTheme.CSS[name])
			}
			if _, ok := theme.CSS[name]; ok {
				t.Fatalf("runtime theme %s should keep %s in compact roles, not CSS map", theme.ID, name)
			}
		}
		if strings.HasPrefix(theme.ID, "vscode-") && theme.CSS["--ov-subtle-border"] != expectedTheme.CSS["--ov-subtle-border"] {
			t.Fatalf("runtime theme %s subtle border = %q, want precomputed palette color %q", theme.ID, theme.CSS["--ov-subtle-border"], expectedTheme.CSS["--ov-subtle-border"])
		}
	}
}

func TestVSCodeCatalogProvidesSubtleBorderColorsForLegacyWebViews(t *testing.T) {
	wanted := map[string]bool{"Kimbie Dark": false, "Monokai Dimmed": false}
	for _, theme := range VSCodeCatalog() {
		border := theme.CSS["--ov-subtle-border"]
		if border == "" || strings.Contains(border, "color-mix") {
			t.Fatalf("theme %s subtle border must be a precomputed CSS color, got %q", theme.Name, border)
		}
		ratio := contrastRatioForTest(border, theme.Semantic["surface"])
		if ratio < 1 || ratio > 1.5 {
			t.Fatalf("theme %s subtle border contrast = %.2f, want a visible but subtle value at most 1.50 against card surface", theme.Name, ratio)
		}
		if _, ok := wanted[theme.Name]; ok {
			if border == theme.Semantic["surface"] || border == theme.Semantic["surfaceRaised"] {
				t.Fatalf("theme %s subtle border reused a surface color: %q", theme.Name, border)
			}
			wanted[theme.Name] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("catalog did not include regression palette %s", name)
		}
	}
}

func TestVSCodeCatalogProvidesTranslucentAccentTint(t *testing.T) {
	for _, theme := range VSCodeCatalog() {
		tint := theme.CSS["--ov-accent-tint"]
		_, _, _, alpha, ok := parseHexColorForTest(tint)
		if !ok || len(tint) != 9 {
			t.Fatalf("theme %s accent tint must be a precomputed #RRGGBBAA color, got %q", theme.Name, tint)
		}
		if !strings.HasPrefix(tint, strings.ToUpper(theme.Semantic["focus"][:7])) {
			t.Fatalf("theme %s accent tint %q does not use focus color %q", theme.Name, tint, theme.Semantic["focus"])
		}
		if alpha <= 0 || alpha >= 128 {
			t.Fatalf("theme %s accent tint alpha = %v, want a light translucent tint", theme.Name, alpha)
		}
	}
}

func contrastRatioForTest(foreground, background string) float64 {
	fr, fg, fb, fa, foregroundOK := parseHexColorForTest(foreground)
	br, bg, bb, ba, backgroundOK := parseHexColorForTest(background)
	if !foregroundOK || !backgroundOK {
		return 0
	}
	backgroundAlpha := ba / 255
	baseR, baseG, baseB := br*backgroundAlpha, bg*backgroundAlpha, bb*backgroundAlpha
	foregroundAlpha := fa / 255
	r := fr*foregroundAlpha + baseR*(1-foregroundAlpha)
	g := fg*foregroundAlpha + baseG*(1-foregroundAlpha)
	b := fb*foregroundAlpha + baseB*(1-foregroundAlpha)
	foregroundLuminance := relativeLuminanceForTest(r, g, b)
	backgroundLuminance := relativeLuminanceForTest(baseR, baseG, baseB)
	if foregroundLuminance < backgroundLuminance {
		foregroundLuminance, backgroundLuminance = backgroundLuminance, foregroundLuminance
	}
	return (foregroundLuminance + 0.05) / (backgroundLuminance + 0.05)
}

func parseHexColorForTest(value string) (r, g, b, a float64, ok bool) {
	hexValue := strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(hexValue) == 3 || len(hexValue) == 4 {
		var expanded strings.Builder
		for _, digit := range hexValue {
			expanded.WriteRune(digit)
			expanded.WriteRune(digit)
		}
		hexValue = expanded.String()
	}
	if len(hexValue) != 6 && len(hexValue) != 8 {
		return 0, 0, 0, 0, false
	}
	parse := func(value string) (float64, bool) {
		parsed, err := strconv.ParseUint(value, 16, 8)
		return float64(parsed), err == nil
	}
	r, ok = parse(hexValue[0:2])
	if !ok {
		return 0, 0, 0, 0, false
	}
	g, ok = parse(hexValue[2:4])
	if !ok {
		return 0, 0, 0, 0, false
	}
	b, ok = parse(hexValue[4:6])
	if !ok {
		return 0, 0, 0, 0, false
	}
	a = 255
	if len(hexValue) == 8 {
		a, ok = parse(hexValue[6:8])
		if !ok {
			return 0, 0, 0, 0, false
		}
	}
	return r, g, b, a, true
}

func relativeLuminanceForTest(r, g, b float64) float64 {
	linearize := func(value float64) float64 {
		value /= 255
		if value <= 0.03928 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*linearize(r) + 0.7152*linearize(g) + 0.0722*linearize(b)
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

func TestVSCodeCatalogProvidesReadableDaisyPrimary(t *testing.T) {
	for _, theme := range VSCodeCatalog() {
		primary := theme.CSS["--ov-daisy-primary"]
		content := theme.CSS["--ov-daisy-primary-content"]
		if len(strings.Fields(primary)) != 3 || len(strings.Fields(content)) != 3 {
			t.Fatalf("%s daisy primary = %q / %q, want oklch triplets", theme.ID, primary, content)
		}
	}
	got, content := daisyPrimaryColors("#FFFFFF", "#000000")
	if got != "1.0000 0.0000 0.00" || content != "0.0000 0.0000 0.00" {
		t.Fatalf("white primary = %q with content %q, want white with black text", got, content)
	}
	if _, content := daisyPrimaryColors("#0000FF", "#FFFFFF"); content != "1.0000 0.0000 0.00" {
		t.Fatalf("blue primary content = %q, want white", content)
	}
}
