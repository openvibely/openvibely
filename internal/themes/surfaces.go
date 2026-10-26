package themes

// surfacePalettes maps each bundled theme to page, card, and outer-frame
// shades. These are app roles, not editor/sidebar roles: Default shares its
// page with the rail and its card with the frame; Layered separates the rail
// and frame. Upstream accents, syntax, and semantic colors remain untouched.
// See docs/theme-surfaces.md for palette sources and adaptation rules.
var surfacePalettes = map[string][3]string{
	"openvibely-graphite":                {"#111111", "#1D1D1D", "#2B2B2B"},
	"openvibely-layered":                 {"#15191E", "#191E24", "#1D232A"},
	"openvibely-slate":                   {"#1E2329", "#252B34", "#2B323C"},
	"openvibely-midnight":                {"#0E1217", "#141A21", "#1B232C"},
	"openvibely-dusk":                    {"#15131C", "#1B1924", "#23202E"},
	"openvibely-paper":                   {"#F2F0EB", "#FAF9F6", "#FFFFFF"},
	"catppuccin-mocha":                   {"#181825", "#1E1E2E", "#313244"},
	"catppuccin-latte":                   {"#DCE0E8", "#E6E9EF", "#EFF1F5"},
	"tokyo-night":                        {"#16161E", "#1A1B26", "#24283B"},
	"nord":                               {"#2E3440", "#3B4252", "#434C5E"},
	"dracula":                            {"#21222C", "#282A36", "#343746"},
	"rose-pine":                          {"#191724", "#1F1D2E", "#26233A"},
	"rose-pine-dawn":                     {"#F2E9E1", "#FAF4ED", "#FFFAF3"},
	"github-dark":                        {"#0D1117", "#161B22", "#21262D"},
	"github-light":                       {"#EAEEF2", "#F6F8FA", "#FFFFFF"},
	"vscode-dark-modern":                 {"#181818", "#1F1F1F", "#313131"},
	"vscode-light-modern":                {"#EEEEEE", "#F8F8F8", "#FFFFFF"},
	"vscode-abyss":                       {"#000C18", "#101827", "#181F2F"},
	"vscode-visual-studio-dark":          {"#181818", "#1E1E1E", "#252526"},
	"vscode-dark-2026":                   {"#121314", "#191A1B", "#242526"},
	"vscode-default-high-contrast":       {"#000000", "#080808", "#101010"},
	"vscode-dark":                        {"#181818", "#1E1E1E", "#252526"},
	"vscode-kimbie-dark":                 {"#221A0F", "#362712", "#51412C"},
	"vscode-visual-studio-light":         {"#E8E8E8", "#F3F3F3", "#FFFFFF"},
	"vscode-light-2026":                  {"#EAEAEA", "#FAFAFD", "#FFFFFF"},
	"vscode-default-high-contrast-light": {"#EFEFEF", "#F7F7F7", "#FFFFFF"},
	"vscode-light":                       {"#E8E8E8", "#F3F3F3", "#FFFFFF"},
	"vscode-monokai":                     {"#1E1F1C", "#272822", "#414339"},
	"vscode-monokai-dimmed":              {"#1E1E1E", "#282828", "#353535"},
	"vscode-quiet-light":                 {"#E8E8E8", "#F2F2F2", "#F5F5F5"},
	"vscode-red":                         {"#330000", "#390000", "#580000"},
	"vscode-solarized-dark":              {"#00212B", "#002632", "#002B36"},
	"vscode-solarized-light":             {"#EEE8D5", "#F7F0DD", "#FDF6E3"},
	"vscode-tomorrow-night-blue":         {"#001733", "#001C40", "#002451"},
}

func applySurfacePalette(theme *Theme) {
	shades, ok := surfacePalettes[theme.ID]
	if !ok {
		return
	}
	page, card, frame := shades[0], shades[1], shades[2]
	rgb, _, _ := parseThemeHexColor(card)
	theme.SurfaceCSS = map[string]string{
		"--ov-surface-default-rail":  page,
		"--ov-surface-default-frame": card,
		"--ov-surface-default-page":  page,
		"--ov-surface-default-card":  card,
		"--ov-surface-layered-frame": frame,
		"--ov-surface-layered-page":  page,
		"--ov-surface-layered-card":  card,
		"--ov-surface-desktop-base":  oklchTriplet(rgb),
		"--ov-surface-code":          theme.Semantic["codeBg"],
	}
	// High-contrast themes must retain their explicit outlines in either mode.
	if theme.Kind == KindHighContrastDark || theme.Kind == KindHighContrastLight {
		theme.SurfaceCSS["--ov-surface-layered-border"] = theme.Semantic["border"]
	}
}
