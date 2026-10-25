package themes

import (
	"fmt"
	"math"
)

// paletteSpec holds the few anchor colors of a palette theme. Every other app
// role is derived from them with the same layering rules as the OpenVibely
// defaults: a darker canvas, lighter cards, soft borders and tinted states.
type paletteSpec struct {
	id, name, source, upstream, mode string
	canvas, column, surface, raised  string
	text                             string
	muted, accent, link, codeBg      string
	tabDivider                       string
	shellRail, shellRowSelected      string
	info, success, warning, error    string
	syntax                           map[string]string
}

func PaletteThemes() []Theme {
	specs := paletteSpecs()
	out := make([]Theme, 0, len(specs))
	for _, spec := range specs {
		out = append(out, paletteTheme(spec))
	}
	return out
}

func paletteSpecs() []paletteSpec {
	return []paletteSpec{
		{
			id: "openvibely-graphite", name: "OpenVibely Graphite", source: "OpenVibely palette", upstream: "OpenVibely", mode: "dark",
			canvas: "#111111", column: "#1D1D1D", surface: "#2B2B2B", raised: "#2B2B2B", text: "#E6E6E6", muted: "#9A9A9A", accent: "#7C83F7", codeBg: "#222222",
			shellRail: "#151515", shellRowSelected: "#262626",
			info: "#6EA8FE", success: "#4CC38A", warning: "#E5B454", error: "#F07178",
			syntax: map[string]string{"keyword": "#C792EA", "string": "#A5D6A7", "number": "#F78C6C", "function": "#82AAFF", "type": "#FFCB6B", "variable": "#E6E6E6", "comment": "#7A7A7A", "title": "#82AAFF"},
		},
		{
			id: "openvibely-slate", name: "OpenVibely Slate", source: "OpenVibely palette", upstream: "OpenVibely", mode: "dark",
			canvas: "#1E2329", column: "#16191E", surface: "#252B34", raised: "#2B323C", text: "#D5DAE1", muted: "#949CA8", accent: "#7480FF", codeBg: "#181C21",
			shellRail: "#1A1E23", shellRowSelected: "#2E3353",
			info: "#4EA8DE", success: "#46C08A", warning: "#E6B450", error: "#EF6B73",
			syntax: map[string]string{"keyword": "#C792EA", "string": "#A5D6A7", "number": "#F78C6C", "function": "#82AAFF", "type": "#FFCB6B", "variable": "#D5DAE1", "comment": "#6B7480", "title": "#82AAFF"},
		},
		{
			id: "openvibely-midnight", name: "OpenVibely Midnight", source: "OpenVibely palette", upstream: "OpenVibely", mode: "dark",
			canvas: "#0E1217", column: "#080A0D", surface: "#141A21", raised: "#1B232C", text: "#CDD6E0", muted: "#8A97A6", accent: "#2EC4B6",
			info: "#4EA8DE", success: "#46C08A", warning: "#E6B450", error: "#EF6B73",
			syntax: map[string]string{"keyword": "#C792EA", "string": "#A5D6A7", "number": "#F78C6C", "function": "#82AAFF", "type": "#FFCB6B", "variable": "#CDD6E0", "comment": "#637382", "title": "#82AAFF"},
		},
		{
			id: "openvibely-dusk", name: "OpenVibely Dusk", source: "OpenVibely palette", upstream: "OpenVibely", mode: "dark",
			canvas: "#15131C", column: "#0D0C12", surface: "#1B1924", raised: "#23202E", text: "#D9D4E8", muted: "#9C95B0", accent: "#A78BFA",
			info: "#7AB8F5", success: "#5FCB98", warning: "#E8B860", error: "#EF6F86",
			syntax: map[string]string{"keyword": "#C4A1FF", "string": "#9BD8A6", "number": "#F5A97F", "function": "#7AB8F5", "type": "#F0C674", "variable": "#D9D4E8", "comment": "#6E6886", "title": "#7AB8F5"},
		},
		{
			id: "openvibely-paper", name: "OpenVibely Paper", source: "OpenVibely palette", upstream: "OpenVibely", mode: "light",
			canvas: "#F2F0EB", column: "#E6E5E2", surface: "#FAF9F6", raised: "#FFFFFF", text: "#24221E", muted: "#69645B", accent: "#2563EB",
			info: "#2563EB", success: "#15803D", warning: "#A16207", error: "#DC2626",
			syntax: map[string]string{"keyword": "#A626A4", "string": "#50A14F", "number": "#986801", "function": "#4078F2", "type": "#C18401", "variable": "#E45649", "comment": "#85868D", "title": "#4078F2"},
		},
		{
			id: "catppuccin-mocha", name: "Catppuccin Mocha", source: "Catppuccin palette (MIT)", upstream: "https://github.com/catppuccin/catppuccin", mode: "dark",
			canvas: "#181825", column: "#11111B", surface: "#1E1E2E", text: "#CDD6F4", muted: "#A6ADC8", accent: "#CBA6F7", codeBg: "#11111B",
			info: "#89DCEB", success: "#A6E3A1", warning: "#F9E2AF", error: "#F38BA8",
			syntax: map[string]string{"keyword": "#CBA6F7", "string": "#A6E3A1", "number": "#FAB387", "function": "#89B4FA", "type": "#F9E2AF", "variable": "#CDD6F4", "comment": "#9399B2", "title": "#89B4FA"},
		},
		{
			id: "catppuccin-latte", name: "Catppuccin Latte", source: "Catppuccin palette (MIT)", upstream: "https://github.com/catppuccin/catppuccin", mode: "light",
			canvas: "#E6E9EF", column: "#F9F9FB", surface: "#EFF1F5", raised: "#F6F7FA", text: "#4C4F69", muted: "#6C6F85", accent: "#8839EF", codeBg: "#DCE0E8",
			info: "#1E66F5", success: "#40A02B", warning: "#DF8E1D", error: "#D20F39",
			syntax: map[string]string{"keyword": "#8839EF", "string": "#40A02B", "number": "#FE640B", "function": "#1E66F5", "type": "#DF8E1D", "variable": "#4C4F69", "comment": "#7C7F93", "title": "#1E66F5"},
		},
		{
			id: "tokyo-night", name: "Tokyo Night", source: "Tokyo Night palette (MIT)", upstream: "https://github.com/enkia/tokyo-night-vscode-theme", mode: "dark",
			canvas: "#16161E", column: "#0E0E14", surface: "#1A1B26", text: "#C0CAF5", muted: "#9AA5CE", accent: "#7AA2F7",
			info: "#7DCFFF", success: "#9ECE6A", warning: "#E0AF68", error: "#F7768E",
			syntax: map[string]string{"keyword": "#BB9AF7", "string": "#9ECE6A", "number": "#FF9E64", "function": "#7AA2F7", "type": "#2AC3DE", "variable": "#C0CAF5", "comment": "#565F89", "title": "#7AA2F7"},
		},
		{
			id: "nord", name: "Nord", source: "Nord palette (MIT)", upstream: "https://github.com/nordtheme/nord", mode: "dark",
			canvas: "#2E3440", column: "#272C36", surface: "#3B4252", raised: "#434C5E", text: "#ECEFF4", accent: "#88C0D0",
			info: "#81A1C1", success: "#A3BE8C", warning: "#EBCB8B", error: "#BF616A",
			syntax: map[string]string{"keyword": "#81A1C1", "string": "#A3BE8C", "number": "#B48EAD", "function": "#88C0D0", "type": "#8FBCBB", "variable": "#D8DEE9", "comment": "#7B88A1", "title": "#88C0D0"},
		},
		{
			id: "dracula", name: "Dracula", source: "Dracula palette (MIT)", upstream: "https://github.com/dracula/dracula-theme", mode: "dark",
			canvas: "#21222C", column: "#191A22", surface: "#282A36", text: "#F8F8F2", accent: "#BD93F9", codeBg: "#191A21",
			info: "#8BE9FD", success: "#50FA7B", warning: "#FFB86C", error: "#FF5555",
			syntax: map[string]string{"keyword": "#FF79C6", "string": "#F1FA8C", "number": "#BD93F9", "function": "#50FA7B", "type": "#8BE9FD", "variable": "#F8F8F2", "comment": "#6272A4", "title": "#50FA7B"},
		},
		{
			id: "rose-pine", name: "Rosé Pine", source: "Rosé Pine palette (MIT)", upstream: "https://github.com/rose-pine/rose-pine-theme", mode: "dark",
			canvas: "#191724", column: "#11101A", surface: "#1F1D2E", raised: "#26233A", text: "#E0DEF4", muted: "#908CAA", accent: "#C4A7E7",
			info: "#3E8FB0", success: "#9CCFD8", warning: "#F6C177", error: "#EB6F92",
			syntax: map[string]string{"keyword": "#3E8FB0", "string": "#F6C177", "number": "#F6C177", "function": "#EBBCBA", "type": "#9CCFD8", "variable": "#E0DEF4", "comment": "#6E6A86", "title": "#C4A7E7"},
		},
		{
			id: "rose-pine-dawn", name: "Rosé Pine Dawn", source: "Rosé Pine palette (MIT)", upstream: "https://github.com/rose-pine/rose-pine-theme", mode: "light",
			canvas: "#F2E9E1", column: "#F4EDE8", tabDivider: "#9893A5", surface: "#FAF4ED", raised: "#FFFAF3", text: "#575279", muted: "#797593", accent: "#907AA9",
			info: "#286983", success: "#56949F", warning: "#EA9D34", error: "#B4637A",
			syntax: map[string]string{"keyword": "#286983", "string": "#EA9D34", "number": "#D7827E", "function": "#D7827E", "type": "#56949F", "variable": "#575279", "comment": "#9893A5", "title": "#907AA9"},
		},
		{
			id: "github-dark", name: "GitHub Dark", source: "GitHub Primer palette (MIT)", upstream: "https://github.com/primer/primitives", mode: "dark",
			canvas: "#0D1117", column: "#08090C", surface: "#161B22", raised: "#1C2128", text: "#E6EDF3", muted: "#8B949E", accent: "#2F81F7", link: "#58A6FF",
			info: "#58A6FF", success: "#3FB950", warning: "#D29922", error: "#F85149",
			syntax: map[string]string{"keyword": "#FF7B72", "string": "#A5D6FF", "number": "#79C0FF", "function": "#D2A8FF", "type": "#FFA657", "variable": "#FFA657", "comment": "#8B949E", "title": "#D2A8FF"},
		},
		{
			id: "github-light", name: "GitHub Light", source: "GitHub Primer palette (MIT)", upstream: "https://github.com/primer/primitives", mode: "light",
			canvas: "#F6F8FA", column: "#F0F0F0", surface: "#FFFFFF", raised: "#FFFFFF", text: "#1F2328", muted: "#656D76", accent: "#0969DA",
			info: "#0969DA", success: "#1A7F37", warning: "#9A6700", error: "#D1242F",
			syntax: map[string]string{"keyword": "#CF222E", "string": "#0A3069", "number": "#0550AE", "function": "#8250DF", "type": "#953800", "variable": "#953800", "comment": "#6E7781", "title": "#8250DF"},
		},
	}
}

func paletteTheme(spec paletteSpec) Theme {
	dark := spec.mode == "dark"
	white, black := "#FFFFFF", "#000000"
	raised := spec.raised
	if raised == "" {
		raised = mixHex(spec.surface, spec.text, 0.05)
	}
	muted := spec.muted
	if muted == "" {
		muted = mixHex(spec.text, spec.surface, 0.30)
	}
	muted = ensureContrast(muted, []string{spec.canvas, spec.surface, raised}, 4.5, spec.text)
	readableStatus := func(color string) string {
		return ensureContrast(color, []string{spec.canvas, spec.surface}, 3, spec.text)
	}
	spec.info, spec.success, spec.warning, spec.error = readableStatus(spec.info), readableStatus(spec.success), readableStatus(spec.warning), readableStatus(spec.error)
	codeBg := spec.codeBg
	if codeBg == "" {
		if dark {
			codeBg = mixHex(spec.canvas, black, 0.18)
		} else {
			codeBg = mixHex(spec.canvas, spec.text, 0.04)
		}
	}
	border := mixHex(spec.surface, spec.text, 0.10)
	borderStrong := ensureContrast(mixHex(spec.surface, spec.text, 0.20), []string{raised, codeBg}, 1.5, spec.text)
	link := spec.link
	if link == "" {
		link = spec.accent
	}
	link = ensureContrast(link, []string{spec.canvas, spec.surface}, 4.5, spec.text)
	hoverToward := black
	if dark {
		hoverToward = white
	}
	buttonHover := mixHex(spec.accent, hoverToward, 0.12)
	semantic := map[string]string{
		"bg": spec.surface, "contentBg": spec.canvas, "sidebarBg": spec.surface, "surface": spec.surface, "surfaceRaised": raised,
		"text": spec.text, "textMuted": muted, "border": border, "borderStrong": borderStrong,
		"automationNodeBorder": borderStrong, "automationEdge": ensureContrast(muted, []string{spec.surface}, 3, spec.text), "yamlIndentRail": borderStrong,
		"primary": spec.accent, "primaryForeground": readableOn(spec.accent), "focus": spec.accent, "link": link,
		"inputBg": spec.surface, "inputBorder": borderStrong,
		"buttonBg": spec.accent, "buttonFg": readableOn(spec.accent), "buttonHoverBg": buttonHover, "buttonHoverFg": readableOn(buttonHover),
		"buttonSecondaryBg": mixHex(spec.surface, spec.text, 0.08), "buttonSecondaryFg": spec.text,
		"selectionBg": withAlpha(spec.accent, 0.28), "hoverBg": withAlpha(spec.text, 0.07),
		"info": spec.info, "infoForeground": readableOn(spec.info),
		"success": spec.success, "successForeground": readableOn(spec.success),
		"warning": spec.warning, "warningForeground": readableOn(spec.warning),
		"error": spec.error, "errorForeground": readableOn(spec.error),
		"diffAddBg": mixHex(spec.canvas, spec.success, 0.16), "diffAddFg": spec.success,
		"diffModifyBg": withAlpha(spec.warning, 0.16), "diffModifyFg": spec.warning,
		"diffDeleteBg": mixHex(spec.canvas, spec.error, 0.16), "diffDeleteFg": spec.error,
		"codeBg": codeBg, "codeFg": spec.text,
	}
	syntax := map[string]string{"background": codeBg, "foreground": spec.text}
	tokens := map[string]string{"error": spec.error}
	for key, value := range spec.syntax {
		tokens[key] = value
	}
	for key, value := range tokens {
		minimum := 4.5
		if key == "comment" {
			minimum = 3
		}
		syntax[key] = ensureContrast(value, []string{codeBg}, minimum, spec.text)
	}
	css := themeCSSVars(semantic, syntax)
	css["--ov-subtle-border"] = border
	css["--ov-kanban-column-bg"] = spec.column
	if spec.tabDivider != "" {
		css["--ov-project-tab-divider"] = spec.tabDivider
	}
	css["--ov-accent-tint"] = accentTintColor(spec.accent)
	if spec.shellRail != "" {
		css["--ov-shell-rail"] = spec.shellRail
	}
	if spec.shellRowSelected != "" {
		css["--ov-shell-row-selected"] = spec.shellRowSelected
	}
	css["--ov-daisy-primary"], css["--ov-daisy-primary-content"] = daisyPrimaryColors(link, spec.canvas)
	kind := KindLight
	if dark {
		kind = KindDark
	}
	return Theme{
		ID: spec.id, Name: spec.name, Source: spec.source, Kind: kind, Mode: spec.mode, Upstream: spec.upstream,
		Semantic: semantic, Syntax: syntax, CSS: css,
		Preview: map[string]string{
			"bg": semantic["bg"], "sidebar": semantic["sidebarBg"], "surface": semantic["surface"], "text": semantic["text"], "accent": semantic["primary"],
			"syntaxKeyword": syntax["keyword"], "syntaxString": syntax["string"], "syntaxFunction": syntax["function"], "syntaxComment": syntax["comment"],
		},
		SearchText: spec.name + " " + kind + " " + spec.source + " palette app theme",
	}
}

func mixHex(from, to string, amount float64) string {
	a, _, _ := parseThemeHexColor(from)
	b, _, _ := parseThemeHexColor(to)
	return hexColor(mixRGB(a, b, amount))
}

func mixRGB(a, b [3]float64, amount float64) [3]float64 {
	var out [3]float64
	for i := range out {
		out[i] = a[i] + (b[i]-a[i])*amount
	}
	return out
}

func hexColor(rgb [3]float64) string {
	return fmt.Sprintf("#%02X%02X%02X", uint8(math.Round(rgb[0])), uint8(math.Round(rgb[1])), uint8(math.Round(rgb[2])))
}

func withAlpha(color string, alpha float64) string {
	rgb, _, _ := parseThemeHexColor(color)
	return fmt.Sprintf("%s%02X", hexColor(rgb), uint8(math.Round(alpha*255)))
}

// readableOn keeps white text when it clearly reads, otherwise picks the better of black or white.
func readableOn(background string) string {
	rgb, _, _ := parseThemeHexColor(background)
	white, black := [3]float64{255, 255, 255}, [3]float64{}
	if contrastRatio(rgb, white) >= 4.5 || contrastRatio(rgb, white) >= contrastRatio(rgb, black) {
		return "#FFFFFF"
	}
	return "#000000"
}

// ensureContrast moves color toward target until it reaches ratio against every background.
func ensureContrast(color string, backgrounds []string, ratio float64, target string) string {
	rgb, _, _ := parseThemeHexColor(color)
	goal, _, _ := parseThemeHexColor(target)
	for step := 0; step <= 40; step++ {
		candidate, _, _ := parseThemeHexColor(hexColor(mixRGB(rgb, goal, float64(step)/40)))
		ok := true
		for _, background := range backgrounds {
			bg, _, _ := parseThemeHexColor(background)
			if contrastRatio(candidate, bg) < ratio {
				ok = false
				break
			}
		}
		if ok {
			return hexColor(candidate)
		}
	}
	return target
}
