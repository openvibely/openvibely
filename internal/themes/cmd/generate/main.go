package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const vscodeRef = "1.130.0"
const vscodeRepo = "https://github.com/microsoft/vscode.git"

var requiredSemantic = []string{"bg", "contentBg", "sidebarBg", "surface", "surfaceRaised", "text", "textMuted", "border", "borderStrong", "automationNodeBorder", "automationEdge", "yamlIndentRail", "primary", "primaryForeground", "focus", "link", "inputBg", "inputBorder", "buttonBg", "buttonFg", "buttonHoverBg", "buttonHoverFg", "buttonSecondaryBg", "buttonSecondaryFg", "selectionBg", "hoverBg", "info", "infoForeground", "success", "successForeground", "warning", "warningForeground", "error", "errorForeground", "diffAddBg", "diffAddFg", "diffModifyBg", "diffModifyFg", "diffDeleteBg", "diffDeleteFg", "codeBg", "codeFg"}
var cssVarName = regexp.MustCompile(`^--ov-[a-z0-9-]+$`)

type manifest struct {
	Publisher   string `json:"publisher"`
	Name        string `json:"name"`
	Contributes struct {
		Themes []themeContribution `json:"themes"`
	} `json:"contributes"`
}
type themeContribution struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	UITheme string `json:"uiTheme"`
	Path    string `json:"path"`
}

type themeJSON struct {
	Name                string            `json:"name"`
	Include             string            `json:"include"`
	Colors              map[string]string `json:"colors"`
	TokenColors         []tokenColor      `json:"tokenColors"`
	SemanticTokenColors map[string]any    `json:"semanticTokenColors"`
}
type tokenColor struct {
	Name     string `json:"name"`
	Scope    any    `json:"scope"`
	Settings struct {
		Foreground string `json:"foreground"`
		FontStyle  string `json:"fontStyle"`
	} `json:"settings"`
}

type outTheme struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Source     string            `json:"source"`
	Kind       string            `json:"kind"`
	Mode       string            `json:"mode"`
	Upstream   string            `json:"upstream"`
	Semantic   map[string]string `json:"semantic"`
	Syntax     map[string]string `json:"syntax"`
	Preview    map[string]string `json:"preview"`
	CSS        map[string]string `json:"css"`
	SearchText string            `json:"searchText"`
}

func main() {
	root := os.Getenv("VSCODE_SRC")
	cleanup := func() {}
	if root == "" {
		var err error
		root, cleanup, err = cloneVSCode()
		if err != nil {
			fatal(err)
		}
	}
	defer cleanup()
	catalog, names, err := buildCatalog(root)
	if err != nil {
		fatal(err)
	}
	if err := writeCatalog(catalog, names); err != nil {
		fatal(err)
	}
	if err := writeAttribution(root, catalog); err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

func cloneVSCode() (string, func(), error) {
	tmp, err := os.MkdirTemp("", "openvibely-vscode-themes-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	dst := filepath.Join(tmp, "vscode")
	cmd := exec.Command("git", "clone", "--depth", "1", "--branch", vscodeRef, "--filter=blob:none", "--sparse", vscodeRepo, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("clone vscode %s: %w\n%s", vscodeRef, err, out)
	}
	cmd = exec.Command("git", "-C", dst, "sparse-checkout", "set", "--no-cone", "/extensions/theme-*", "/LICENSE.txt", "/ThirdPartyNotices.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("sparse checkout vscode themes: %w\n%s", err, out)
	}
	return dst, cleanup, nil
}

func buildCatalog(root string) ([]outTheme, []string, error) {
	extRoot := filepath.Join(root, "extensions")
	entries, err := os.ReadDir(extRoot)
	if err != nil {
		return nil, nil, err
	}
	var catalog []outTheme
	seen := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "theme-") {
			continue
		}
		extDir := filepath.Join(extRoot, entry.Name())
		pkgPath := filepath.Join(extDir, "package.json")
		data, err := os.ReadFile(pkgPath)
		if err != nil {
			continue
		}
		var pkg manifest
		if err := json.Unmarshal(stripJSONC(data), &pkg); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", pkgPath, err)
		}
		if len(pkg.Contributes.Themes) == 0 {
			continue
		}
		nls := loadNLS(filepath.Join(extDir, "package.nls.json"))
		for _, contrib := range pkg.Contributes.Themes {
			label := localize(contrib.Label, nls)
			path := filepath.Clean(filepath.Join(extDir, contrib.Path))
			merged, includeCount, err := resolveTheme(path, map[string]bool{})
			if err != nil {
				return nil, nil, err
			}
			id := "vscode-" + slugify(firstNonEmpty(contrib.ID, label, merged.Name))
			if seen[id] {
				return nil, nil, fmt.Errorf("duplicate generated theme id %q", id)
			}
			seen[id] = true
			kind, mode := normalizeKind(contrib.UITheme)
			semantic := normalizeSemantic(kind, merged.Colors)
			syntax := normalizeSyntax(kind, merged.Colors, merged.TokenColors)
			preview := map[string]string{"bg": semantic["bg"], "sidebar": semantic["sidebarBg"], "surface": semantic["surface"], "text": semantic["text"], "accent": semantic["primary"], "syntaxKeyword": syntax["keyword"], "syntaxString": syntax["string"], "syntaxFunction": syntax["function"], "syntaxComment": syntax["comment"]}
			css := cssVars(semantic, syntax)
			search := strings.Join([]string{label, kind, strings.ReplaceAll(kind, "-", " "), "VS Code", entry.Name(), pkg.Name}, " ")
			catalog = append(catalog, outTheme{ID: id, Name: label, Source: "VS Code built-in " + entry.Name(), Kind: kind, Mode: mode, Upstream: fmt.Sprintf("%s@%s:%s", vscodeRepo, vscodeRef, relSlash(root, path)), Semantic: semantic, Syntax: syntax, Preview: preview, CSS: css, SearchText: search})
			_ = includeCount
		}
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	order := []string{"vscode-dark-modern", "vscode-light-modern"}
	catalog = stablePromote(catalog, order)
	names := make([]string, len(catalog))
	for i := range catalog {
		names[i] = catalog[i].Name
	}
	return catalog, names, nil
}

func stripJSONC(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(data) && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' && data[i] != '\r' {
				i++
			}
			if i < len(data) {
				out = append(out, data[i])
			}
			continue
		}
		if ch == '/' && i+1 < len(data) && data[i+1] == '*' {
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, ch)
	}
	out = removeTrailingCommas(out)
	return out
}

func removeTrailingCommas(data []byte) []byte {
	var out []byte
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\n' || data[j] == '\r' || data[j] == '\t') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out = append(out, ch)
	}
	return out
}

func loadNLS(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	_ = json.Unmarshal(stripJSONC(data), &m)
	return m
}
func localize(s string, nls map[string]string) string {
	if strings.HasPrefix(s, "%") && strings.HasSuffix(s, "%") {
		if v := nls[strings.Trim(s, "%")]; v != "" {
			return v
		}
	}
	return s
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return "theme"
}
func relSlash(root, path string) string {
	rel, _ := filepath.Rel(root, path)
	return filepath.ToSlash(rel)
}

func resolveTheme(path string, stack map[string]bool) (themeJSON, int, error) {
	if stack[path] {
		return themeJSON{}, 0, fmt.Errorf("theme include cycle at %s", path)
	}
	stack[path] = true
	data, err := os.ReadFile(path)
	if err != nil {
		return themeJSON{}, 0, err
	}
	var cur themeJSON
	if err := json.Unmarshal(stripJSONC(data), &cur); err != nil {
		return themeJSON{}, 0, fmt.Errorf("parse %s: %w", path, err)
	}
	if cur.Colors == nil {
		cur.Colors = map[string]string{}
	}
	count := 0
	if strings.TrimSpace(cur.Include) != "" {
		incPath := filepath.Clean(filepath.Join(filepath.Dir(path), cur.Include))
		base, incCount, err := resolveTheme(incPath, stack)
		if err != nil {
			return themeJSON{}, 0, err
		}
		for k, v := range cur.Colors {
			base.Colors[k] = v
		}
		base.TokenColors = append(base.TokenColors, cur.TokenColors...)
		for k, v := range cur.SemanticTokenColors {
			if base.SemanticTokenColors == nil {
				base.SemanticTokenColors = map[string]any{}
			}
			base.SemanticTokenColors[k] = v
		}
		if cur.Name != "" {
			base.Name = cur.Name
		}
		cur = base
		count = incCount + 1
	}
	delete(stack, path)
	return cur, count, nil
}

func normalizeKind(ui string) (string, string) {
	switch strings.ToLower(ui) {
	case "vs":
		return "light", "light"
	case "hc-light":
		return "high-contrast-light", "light"
	case "hc-black":
		return "high-contrast-dark", "dark"
	default:
		return "dark", "dark"
	}
}

func normalizeSemantic(kind string, c map[string]string) map[string]string {
	dark := kind == "dark" || kind == "high-contrast-dark"
	fb := defaults(dark)
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := cleanColor(c[k]); v != "" && !transparent(v) {
				return v
			}
		}
		return ""
	}
	m := map[string]string{}
	m["bg"] = fallback(pick("editor.background", "window.background"), fb["bg"])
	m["contentBg"] = fallback(pick("editor.background", "window.background", "editorPane.background", "panel.background"), m["bg"])
	m["sidebarBg"] = fallback(pick("sideBar.background", "activityBar.background"), fb["sidebarBg"])
	m["surface"] = fallback(pick("editorGroupHeader.tabsBackground", "panel.background", "dropdown.background", "sideBarSectionHeader.background"), m["contentBg"])
	m["surfaceRaised"] = fallback(pick("dropdown.background", "menu.background", "quickInput.background"), m["surface"])
	m["text"] = fallback(pick("foreground", "editor.foreground"), fb["text"])
	m["textMuted"] = fallback(pick("descriptionForeground", "disabledForeground", "input.placeholderForeground"), mix(m["text"], m["bg"], .35))
	m["border"] = fallback(pick("contrastBorder", "widget.border", "panel.border", "sideBar.border"), mix(m["text"], m["bg"], .18))
	m["borderStrong"] = fallback(pick("contrastActiveBorder", "focusBorder"), mix(m["text"], m["bg"], .35))
	m["primary"] = fallback(pick("button.background", "textLink.foreground", "focusBorder", "activityBarBadge.background"), fb["primary"])
	m["primaryForeground"] = fallback(pick("button.foreground", "activityBarBadge.foreground"), bestText(m["primary"]))
	m["focus"] = fallback(pick("focusBorder", "contrastActiveBorder"), m["primary"])
	m["link"] = fallback(pick("textLink.foreground", "textLink.activeForeground"), m["primary"])
	m["inputBg"] = fallback(pick("input.background", "dropdown.background"), m["surfaceRaised"])
	m["inputBorder"] = fallback(pick("input.border", "dropdown.border"), m["border"])
	m["buttonBg"] = m["primary"]
	m["buttonFg"] = m["primaryForeground"]
	m["selectionBg"] = alpha(m["primary"], .32)
	for _, k := range []string{"list.activeSelectionBackground", "editor.selectionBackground", "selection.background"} {
		if v := pick(k); v != "" && contrastRatio(m["text"], composite(v, m["contentBg"])) >= 3 {
			m["selectionBg"] = v
			break
		}
	}
	m["hoverBg"] = fallback(pick("list.hoverBackground", "toolbar.hoverBackground", "editor.hoverHighlightBackground"), alpha(m["primary"], .16))
	m["buttonHoverBg"] = fallback(pick("button.hoverBackground"), m["focus"])
	m["buttonHoverFg"] = fallback(pick("button.foreground", "activityBarBadge.foreground"), bestText(m["buttonHoverBg"]))
	m["buttonSecondaryBg"] = fallback(pick("button.secondaryBackground"), m["surfaceRaised"])
	m["buttonSecondaryFg"] = fallback(pick("button.secondaryForeground"), m["text"])
	m["info"] = fallback(pick("notificationsInfoIcon.foreground", "editorInfo.foreground", "problemsInfoIcon.foreground"), fb["info"])
	m["infoForeground"] = bestText(m["info"])
	m["success"] = fallback(pick("testing.iconPassed", "terminal.ansiGreen", "charts.green"), fb["success"])
	m["successForeground"] = bestText(m["success"])
	m["warning"] = fallback(pick("notificationsWarningIcon.foreground", "editorWarning.foreground", "problemsWarningIcon.foreground"), fb["warning"])
	m["warningForeground"] = bestText(m["warning"])
	m["error"] = fallback(pick("errorForeground", "notificationsErrorIcon.foreground", "editorError.foreground"), fb["error"])
	m["errorForeground"] = bestText(m["error"])
	m["diffAddBg"] = fallback(pick("diffEditor.insertedTextBackground", "diffEditor.insertedLineBackground"), alpha(m["success"], .18))
	m["diffAddFg"] = m["success"]
	m["diffModifyBg"] = fallback(pick("diffEditor.unchangedRegionBackground"), alpha(m["warning"], .14))
	m["diffModifyFg"] = m["warning"]
	m["diffDeleteBg"] = fallback(pick("diffEditor.removedTextBackground", "diffEditor.removedLineBackground"), alpha(m["error"], .18))
	m["diffDeleteFg"] = m["error"]
	m["codeBg"] = fallback(pick("textCodeBlock.background", "editor.background"), m["bg"])
	m["codeFg"] = fallback(pick("editor.foreground", "foreground"), m["text"])
	m["yamlIndentRail"] = ensureContrast(fallback(pick("editorIndentGuide.background"), m["border"]), m["codeBg"], m["text"], 1.5)
	m["automationNodeBorder"] = ensureContrast(m["border"], m["surfaceRaised"], m["text"], 1.5)
	m["automationEdge"] = ensureContrast(fallback(pick("editorLineNumber.foreground"), m["textMuted"]), m["surface"], m["text"], 3.0)
	for _, k := range requiredSemantic {
		if !validCSSColor(m[k]) {
			m[k] = fb[k]
		}
	}
	return m
}

func normalizeSyntax(kind string, colors map[string]string, tokens []tokenColor) map[string]string {
	dark := kind == "dark" || kind == "high-contrast-dark"
	fb := syntaxDefaults(dark)
	m := map[string]string{"background": fallback(cleanColor(colors["editor.background"]), fb["background"]), "foreground": fallback(cleanColor(colors["editor.foreground"]), fb["foreground"])}
	for k, v := range fb {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	for role, target := range syntaxRoleScopes {
		if fg := resolveTokenColor(tokens, target); fg != "" {
			m[role] = fg
		}
	}
	for k, v := range fb {
		if !validCSSColor(m[k]) {
			m[k] = v
		}
	}
	return m
}

// Resolved like TextMate: longest matching scope prefix wins, later rules break ties.
var syntaxRoleScopes = map[string]string{
	"comment":  "comment.line.double-slash",
	"string":   "string.quoted.double",
	"number":   "constant.numeric.decimal",
	"keyword":  "keyword.control",
	"function": "entity.name.function",
	"type":     "entity.name.type.class",
	"variable": "variable.other.readwrite",
	"error":    "invalid.illegal",
	"title":    "markup.heading",
}

func resolveTokenColor(tokens []tokenColor, target string) string {
	best, bestLen := "", 0
	for _, tc := range tokens {
		fg := cleanColor(tc.Settings.Foreground)
		if fg == "" || transparent(fg) {
			continue
		}
		for _, scope := range scopes(tc.Scope) {
			s := strings.ToLower(strings.TrimSpace(scope))
			if s == "" || strings.ContainsAny(s, " >") {
				continue
			}
			if (s == target || strings.HasPrefix(target, s+".")) && len(s) >= bestLen {
				best, bestLen = fg, len(s)
			}
		}
	}
	return best
}

func scopes(v any) []string {
	switch x := v.(type) {
	case string:
		return strings.Split(x, ",")
	case []any:
		out := []string{}
		for _, it := range x {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func cssVars(semantic, syntax map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range semantic {
		name := "--ov-" + camelToKebab(k)
		if cssVarName.MatchString(name) {
			m[name] = v
		}
	}
	for k, v := range syntax {
		name := "--ov-syntax-" + camelToKebab(k)
		if cssVarName.MatchString(name) {
			m[name] = v
		}
	}
	return m
}
func camelToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + 'a' - 'A')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func defaults(dark bool) map[string]string {
	if dark {
		return map[string]string{"bg": "#1e1e1e", "contentBg": "#252526", "sidebarBg": "#181818", "surface": "#252526", "surfaceRaised": "#2d2d30", "text": "#cccccc", "textMuted": "#9d9d9d", "border": "#3c3c3c", "borderStrong": "#6f6f6f", "automationNodeBorder": "#6f6f6f", "automationEdge": "#cccccc", "yamlIndentRail": "#6f6f6f", "primary": "#0078d4", "primaryForeground": "#ffffff", "focus": "#007fd4", "link": "#3794ff", "info": "#3794ff", "success": "#89d185", "warning": "#cca700", "error": "#f85149", "codeBg": "#1e1e1e", "codeFg": "#d4d4d4"}
	}
	return map[string]string{"bg": "#ffffff", "contentBg": "#f3f3f3", "sidebarBg": "#f3f3f3", "surface": "#ffffff", "surfaceRaised": "#ffffff", "text": "#1f1f1f", "textMuted": "#616161", "border": "#e5e5e5", "borderStrong": "#cccccc", "automationNodeBorder": "#cccccc", "automationEdge": "#1f1f1f", "yamlIndentRail": "#cccccc", "primary": "#005fb8", "primaryForeground": "#ffffff", "focus": "#0078d4", "link": "#006ab1", "info": "#0078d4", "success": "#16825d", "warning": "#9d5d00", "error": "#d13438", "codeBg": "#f6f8fa", "codeFg": "#24292f"}
}
func syntaxDefaults(dark bool) map[string]string {
	if dark {
		return map[string]string{"background": "#1e1e1e", "foreground": "#d4d4d4", "comment": "#6a9955", "string": "#ce9178", "number": "#b5cea8", "keyword": "#569cd6", "function": "#dcdcaa", "type": "#4ec9b0", "variable": "#9cdcfe", "title": "#dcdcaa", "error": "#f44747"}
	}
	return map[string]string{"background": "#ffffff", "foreground": "#24292f", "comment": "#6a737d", "string": "#032f62", "number": "#005cc5", "keyword": "#d73a49", "function": "#6f42c1", "type": "#005cc5", "variable": "#24292f", "title": "#6f42c1", "error": "#d73a49"}
}

func cleanColor(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.EqualFold(s, "transparent") {
		return "transparent"
	}
	if strings.HasPrefix(s, "#") {
		h := strings.TrimPrefix(s, "#")
		if len(h) == 3 {
			return "#" + strings.Repeat(string(h[0]), 2) + strings.Repeat(string(h[1]), 2) + strings.Repeat(string(h[2]), 2)
		}
		if len(h) == 4 {
			return "#" + strings.Repeat(string(h[0]), 2) + strings.Repeat(string(h[1]), 2) + strings.Repeat(string(h[2]), 2) + strings.Repeat(string(h[3]), 2)
		}
		if len(h) == 6 || len(h) == 8 {
			for _, r := range h {
				if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
					return ""
				}
			}
			return "#" + strings.ToUpper(h)
		}
	}
	return ""
}

func validCSSColor(s string) bool {
	s = strings.TrimSpace(s)
	if cleanColor(s) != "" {
		return true
	}
	if strings.HasPrefix(s, "rgba(") && strings.HasSuffix(s, ")") {
		return true
	}
	return false
}

func transparent(s string) bool {
	s = cleanColor(s)
	return s == "transparent" || (len(s) == 9 && strings.EqualFold(s[7:], "00"))
}
func fallback(v, fb string) string {
	if validCSSColor(v) && !transparent(v) {
		if h := cleanColor(v); h != "" {
			return h
		}
		return strings.TrimSpace(v)
	}
	if validCSSColor(fb) {
		if h := cleanColor(fb); h != "" {
			return h
		}
		return strings.TrimSpace(fb)
	}
	return "#000000"
}
func alpha(hex string, a float64) string {
	r, g, b, _ := rgba(hex)
	return fmt.Sprintf("rgba(%d, %d, %d, %.2f)", r, g, b, a)
}
func mix(a, b string, t float64) string {
	ar, ag, ab, _ := rgba(a)
	br, bg, bb, _ := rgba(b)
	r := int(math.Round(float64(ar)*(1-t) + float64(br)*t))
	g := int(math.Round(float64(ag)*(1-t) + float64(bg)*t))
	bl := int(math.Round(float64(ab)*(1-t) + float64(bb)*t))
	return fmt.Sprintf("#%02X%02X%02X", r, g, bl)
}

func ensureContrast(preferred, background, fallbackColor string, minimum float64) string {
	background = opaqueColor(background)
	preferred = opaqueColor(preferred)
	if contrastRatio(preferred, background) >= minimum {
		return preferred
	}

	fallbackColor = opaqueColor(fallbackColor)
	if contrastRatio(fallbackColor, background) < minimum {
		if contrastRatio("#000000", background) >= contrastRatio("#FFFFFF", background) {
			fallbackColor = "#000000"
		} else {
			fallbackColor = "#FFFFFF"
		}
	}

	// Move the high-contrast fallback toward the drawing surface until it is
	// just strong enough to remain visible. This keeps ordinary rails and
	// borders subtle while preventing upstream colors that match the surface
	// from disappearing entirely.
	for step := 1000; step >= 0; step-- {
		candidate := mix(fallbackColor, background, float64(step)/1000)
		if contrastRatio(candidate, background) >= minimum {
			return candidate
		}
	}
	return fallbackColor
}

func composite(top, base string) string {
	tr, tg, tb, ta := rgba(top)
	br, bg, bb, _ := rgba(base)
	a := float64(ta) / 255
	blend := func(t, b int) int { return int(math.Round(float64(t)*a + float64(b)*(1-a))) }
	return fmt.Sprintf("#%02X%02X%02X", blend(tr, br), blend(tg, bg), blend(tb, bb))
}

func opaqueColor(color string) string {
	r, g, b, _ := rgba(color)
	return fmt.Sprintf("#%02X%02X%02X", r, g, b)
}

func contrastRatio(foreground, background string) float64 {
	fr, fg, fb, fa := rgba(foreground)
	br, bg, bb, ba := rgba(background)
	backgroundAlpha := float64(ba) / 255
	baseR := float64(br) * backgroundAlpha
	baseG := float64(bg) * backgroundAlpha
	baseB := float64(bb) * backgroundAlpha
	foregroundAlpha := float64(fa) / 255
	r := float64(fr)*foregroundAlpha + baseR*(1-foregroundAlpha)
	g := float64(fg)*foregroundAlpha + baseG*(1-foregroundAlpha)
	b := float64(fb)*foregroundAlpha + baseB*(1-foregroundAlpha)
	foregroundLuminance := relativeLuminance(r, g, b)
	backgroundLuminance := relativeLuminance(baseR, baseG, baseB)
	if foregroundLuminance < backgroundLuminance {
		foregroundLuminance, backgroundLuminance = backgroundLuminance, foregroundLuminance
	}
	return (foregroundLuminance + 0.05) / (backgroundLuminance + 0.05)
}

func relativeLuminance(r, g, b float64) float64 {
	linearize := func(value float64) float64 {
		value /= 255
		if value <= 0.03928 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*linearize(r) + 0.7152*linearize(g) + 0.0722*linearize(b)
}

func bestText(bg string) string {
	r, g, b, _ := rgba(bg)
	lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 255
	if lum > .55 {
		return "#000000"
	}
	return "#FFFFFF"
}
func rgba(hex string) (int, int, int, int) {
	h := strings.TrimPrefix(cleanColor(hex), "#")
	if len(h) < 6 {
		return 0, 0, 0, 255
	}
	var r, g, b, a int = 0, 0, 0, 255
	fmt.Sscanf(h[:2], "%02X", &r)
	fmt.Sscanf(h[2:4], "%02X", &g)
	fmt.Sscanf(h[4:6], "%02X", &b)
	if len(h) == 8 {
		fmt.Sscanf(h[6:8], "%02X", &a)
	}
	return r, g, b, a
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func stablePromote(c []outTheme, ids []string) []outTheme {
	out := []outTheme{}
	used := map[string]bool{}
	for _, id := range ids {
		for _, t := range c {
			if t.ID == id {
				out = append(out, t)
				used[id] = true
			}
		}
	}
	for _, t := range c {
		if !used[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

func writeCatalog(catalog []outTheme, names []string) error {
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by go run ./internal/themes/cmd/generate; DO NOT EDIT.\npackage themes\n\nconst PinnedVSCodeRef = %q\nconst PinnedVSCodeRepository = %q\nconst ExpectedThemeCount = %d\n\nvar ExpectedThemeNames = []string{", vscodeRef, vscodeRepo, len(catalog))
	for _, name := range names {
		fmt.Fprintf(&b, "\n\t%q,", name)
	}
	fmt.Fprintf(&b, "\n}\n\nconst CatalogJSONRaw = `%s`\n", data)
	return os.WriteFile(filepath.Join("internal", "themes", "catalog_gen.go"), b.Bytes(), 0644)
}

func writeAttribution(root string, catalog []outTheme) error {
	_ = os.MkdirAll(filepath.Join("docs", "licenses"), 0755)
	lic, err := os.ReadFile(filepath.Join(root, "LICENSE.txt"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# VS Code Built-In Themes Attribution\n\nOpenVibely includes normalized color data generated from the first-party built-in color theme extensions in the open-source microsoft/vscode repository.\n\n- Upstream: %s\n- Pinned tag: %s\n- Imported extensions: ", vscodeRepo, vscodeRef)
	exts := map[string]bool{}
	for _, t := range catalog {
		parts := strings.Fields(strings.TrimPrefix(t.Source, "VS Code built-in "))
		if len(parts) > 0 {
			exts[parts[0]] = true
		}
	}
	keys := []string{}
	for k := range exts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&b, "%s\n- Generated catalog: internal/themes/catalog_gen.go\n- Update command: `go run ./internal/themes/cmd/generate`\n\n## Bundled Themes\n\n", strings.Join(keys, ", "))
	for _, t := range catalog {
		fmt.Fprintf(&b, "- %s (%s)\n", t.Name, t.Kind)
	}
	fmt.Fprintf(&b, "\n## Semantic Color Mapping\n\nTheme generation reads the built-in VS Code extension manifests, resolves each contributed theme JSON file including its `include` chain, and then normalizes VS Code workbench colors into OpenVibely roles. Missing, transparent, or invalid upstream colors are not emitted directly; each role falls back to a theme-kind default or to a value derived from another validated role.\n\nPrimary derivation rules:\n\n- Application and content backgrounds prefer VS Code editor/window backgrounds first so the whole OpenVibely canvas follows the selected palette; sidebar, surface, raised surface, foreground, muted foreground, borders, links, inputs, buttons, selection, hover, focus, status, diff, and code-block roles are then selected from matching VS Code workbench color keys when present.\n- Transparent upstream colors are skipped so they cannot erase readable surfaces; invalid CSS color values are rejected at generation time.\n- Muted text and borders are derived by mixing validated foreground and background colors when VS Code does not provide a direct role.\n- Automation node borders, connector colors, and YAML indentation rails are derived from their actual drawing surfaces; when an upstream border or muted color is too close, a theme-aware foreground mix is used to enforce visible contrast.\n- Button foregrounds, button hover foregrounds, secondary button colors, and status button foregrounds fall back to validated VS Code button/status keys or to black/white based on the generated background luminance.\n- Selection, hover, and diff backgrounds fall back to generated `rgba(...)` colors derived from the accent, success, warning, and error roles.\n- Code-block and syntax colors prefer VS Code editor and token scopes, then fall back to bundled light or dark syntax defaults.\n- High-contrast-light and high-contrast-dark themes use the same mapping pipeline but retain their high-contrast `kind` in the generated catalog and DOM attribute.\n\nThese rules are implemented in `internal/themes/cmd/generate/main.go` and the sanitized output is committed in `internal/themes/catalog_gen.go` for offline runtime use.\n")
	licenseText := strings.TrimSpace(string(lic))
	licenseText = strings.ReplaceAll(licenseText, "\r\n", "\n")
	licenseText = strings.ReplaceAll(licenseText, "\r", "\n")
	fmt.Fprintf(&b, "\n## License\n\nThe imported upstream theme data is from Visual Studio Code, licensed under the MIT License. The upstream license text at tag %s follows.\n\n```text\n%s\n```\n", vscodeRef, licenseText)
	return os.WriteFile(filepath.Join("docs", "licenses", "vscode-built-in-themes.md"), b.Bytes(), 0644)
}
