package themes

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	KindLight              = "light"
	KindDark               = "dark"
	KindHighContrastLight  = "high-contrast-light"
	KindHighContrastDark   = "high-contrast-dark"
	OpenVibelyLightThemeID = "openvibely-light"
	OpenVibelyDarkThemeID  = "openvibely-dark"
	DefaultLightThemeID    = OpenVibelyLightThemeID
	DefaultDarkThemeID     = OpenVibelyDarkThemeID
)

type Theme struct {
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

func Catalog() []Theme {
	catalog := make([]Theme, 0, ExpectedThemeCount+5+len(paletteSpecs()))
	catalog = append(catalog, OpenVibelyThemes()...)
	catalog = append(catalog, PaletteThemes()...)
	catalog = append(catalog, VSCodeCatalog()...)
	return catalog
}

func VSCodeCatalog() []Theme {
	var catalog []Theme
	_ = json.Unmarshal([]byte(CatalogJSONRaw), &catalog)
	for i := range catalog {
		if catalog[i].CSS == nil {
			catalog[i].CSS = make(map[string]string)
		}
		catalog[i].CSS["--ov-subtle-border"] = subtleBorderColor(catalog[i].Semantic["surface"], catalog[i].Semantic["text"])
		catalog[i].CSS["--ov-accent-tint"] = accentTintColor(catalog[i].Semantic["focus"])
		catalog[i].CSS["--ov-daisy-primary"], catalog[i].CSS["--ov-daisy-primary-content"] = daisyPrimaryColors(catalog[i].Semantic["link"], catalog[i].Semantic["contentBg"])
	}
	return catalog
}

// accentTintColor is a translucent focus color for selected rows and ranges.
func accentTintColor(focus string) string {
	rgb, alpha, ok := parseThemeHexColor(focus)
	if !ok {
		return focus
	}
	return fmt.Sprintf("#%02X%02X%02X%02X", uint8(rgb[0]), uint8(rgb[1]), uint8(rgb[2]), uint8(math.Round(alpha*0.24*255)))
}

// daisyPrimaryColors returns DaisyUI oklch triplets for the theme accent and the
// readable black or white text drawn on it. The link color is used because it is
// already guaranteed readable as text on the page, unlike some button fills.
func daisyPrimaryColors(link, page string) (string, string) {
	rgb, alpha, ok := parseThemeHexColor(link)
	if !ok {
		return "", ""
	}
	if pageRGB, _, pageOK := parseThemeHexColor(page); pageOK && alpha < 1 {
		for i := range rgb {
			rgb[i] = rgb[i]*alpha + pageRGB[i]*(1-alpha)
		}
	}
	content := [3]float64{255, 255, 255}
	if contrastRatio(rgb, content) < contrastRatio(rgb, [3]float64{}) {
		content = [3]float64{}
	}
	return oklchTriplet(rgb), oklchTriplet(content)
}

func oklchTriplet(rgb [3]float64) string {
	var lin [3]float64
	for i, channel := range rgb {
		c := channel / 255
		if c <= 0.04045 {
			lin[i] = c / 12.92
		} else {
			lin[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	l := math.Cbrt(0.4122214708*lin[0] + 0.5363325363*lin[1] + 0.0514459929*lin[2])
	m := math.Cbrt(0.2119034982*lin[0] + 0.6806995451*lin[1] + 0.1073969566*lin[2])
	s := math.Cbrt(0.0883024619*lin[0] + 0.2817188376*lin[1] + 0.6299787005*lin[2])
	okL := 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
	okA := 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	okB := 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	chroma := math.Hypot(okA, okB)
	hue := math.Atan2(okB, okA) * 180 / math.Pi
	if hue < 0 {
		hue += 360
	}
	if chroma < 0.0001 {
		chroma, hue = 0, 0
	}
	return fmt.Sprintf("%.4f %.4f %.2f", okL, chroma, hue)
}

func contrastRatio(a, b [3]float64) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relativeLuminance(rgb [3]float64) float64 {
	var lin [3]float64
	for i, channel := range rgb {
		c := channel / 255
		if c <= 0.03928 {
			lin[i] = c / 12.92
		} else {
			lin[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*lin[0] + 0.7152*lin[1] + 0.0722*lin[2]
}

func subtleBorderColor(surface, text string) string {
	surfaceRGB, surfaceAlpha, surfaceOK := parseThemeHexColor(surface)
	textRGB, textAlpha, textOK := parseThemeHexColor(text)
	if !surfaceOK || !textOK {
		return surface
	}

	const textWeight = 0.08
	surfaceWeight := 1 - textWeight
	alpha := surfaceAlpha*surfaceWeight + textAlpha*textWeight
	if alpha <= 0 {
		return surface
	}
	mixed := [3]uint8{}
	for i := range mixed {
		channel := (surfaceRGB[i]*surfaceAlpha*surfaceWeight + textRGB[i]*textAlpha*textWeight) / alpha
		mixed[i] = uint8(math.Round(channel))
	}
	if alpha < 1 {
		return fmt.Sprintf("#%02X%02X%02X%02X", mixed[0], mixed[1], mixed[2], uint8(math.Round(alpha*255)))
	}
	return fmt.Sprintf("#%02X%02X%02X", mixed[0], mixed[1], mixed[2])
}

func parseThemeHexColor(value string) ([3]float64, float64, bool) {
	var rgb [3]float64
	hex := strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(hex) == 3 || len(hex) == 4 {
		var expanded strings.Builder
		for _, digit := range hex {
			expanded.WriteRune(digit)
			expanded.WriteRune(digit)
		}
		hex = expanded.String()
	}
	if len(hex) != 6 && len(hex) != 8 {
		return rgb, 0, false
	}
	for i := range rgb {
		value, err := strconv.ParseUint(hex[i*2:i*2+2], 16, 8)
		if err != nil {
			return rgb, 0, false
		}
		rgb[i] = float64(value)
	}
	alpha := 1.0
	if len(hex) == 8 {
		value, err := strconv.ParseUint(hex[6:8], 16, 8)
		if err != nil {
			return rgb, 0, false
		}
		alpha = float64(value) / 255
	}
	return rgb, alpha, true
}

func OpenVibelyThemes() []Theme {
	return []Theme{
		openVibelyDarkTheme(),
		openVibelyLightTheme(),
		openVibelyContinuousTheme(),
		openVibelyRaisedTheme(),
		openVibelyElevatedTheme(),
	}
}

// The continuous variant retains native dark styling with flat, bordered chat surfaces.
func openVibelyContinuousTheme() Theme {
	theme := openVibelyDarkTheme()
	theme.ID = "openvibely-dark-continuous"
	theme.Name = "OpenVibely Dark Continuous"
	theme.Source = "OpenVibely experimental"
	theme.SearchText = theme.Name + " dark experimental continuous sidebar"
	return theme
}

// The raised variant retains native dark styling with shadowless chat surfaces.
func openVibelyRaisedTheme() Theme {
	theme := openVibelyDarkTheme()
	theme.ID = "openvibely-dark-raised"
	theme.Name = "OpenVibely Dark Raised"
	theme.Source = "OpenVibely experimental"
	theme.SearchText = theme.Name + " dark experimental raised"
	return theme
}

// The elevated variant retains native dark styling and climbs an even lightness
// ladder: page, cards, controls, then chat and overlays.
func openVibelyElevatedTheme() Theme {
	theme := openVibelyDarkTheme()
	theme.ID = "openvibely-dark-elevated"
	theme.Name = "OpenVibely Dark Elevated"
	theme.Source = "OpenVibely experimental"
	theme.SearchText = theme.Name + " dark experimental elevation"
	return theme
}

func CatalogJSON() string {
	data, err := json.Marshal(Catalog())
	if err != nil {
		return "[]"
	}
	return string(data)
}

var runtimeCatalog struct {
	once sync.Once
	json string
}

func RuntimeCatalogJSON() string {
	runtimeCatalog.once.Do(func() {
		type runtimeTheme struct {
			ID    string            `json:"id"`
			Kind  string            `json:"kind"`
			Mode  string            `json:"mode"`
			CSS   map[string]string `json:"css"`
			Roles [7]string         `json:"r"`
		}
		catalog := Catalog()
		runtime := make([]runtimeTheme, 0, len(catalog))
		for _, theme := range catalog {
			css := make(map[string]string, len(theme.CSS)-3)
			for name, value := range theme.CSS {
				switch name {
				case "--ov-automation-node-border", "--ov-automation-edge", "--ov-yaml-indent-rail":
					continue
				default:
					css[name] = value
				}
			}
			card := scheduleTaskBackground(theme)
			css["--ov-schedule-task-bg"] = card
			css["--ov-schedule-selection-bg"] = scheduleSelectionBackground(theme, card)
			css["--ov-kanban-column-bg"] = kanbanColumnBackground(theme)
			runtime = append(runtime, runtimeTheme{
				ID: theme.ID, Kind: theme.Kind, Mode: theme.Mode, CSS: css,
				Roles: toggleRuntimeRoles(theme),
			})
		}
		data, err := json.Marshal(runtime)
		if err != nil {
			runtimeCatalog.json = "[]"
			return
		}
		runtimeCatalog.json = string(data)
	})
	return runtimeCatalog.json
}

func ByID(id string) (Theme, bool) {
	for _, theme := range Catalog() {
		if theme.ID == id {
			return theme, true
		}
	}
	return Theme{}, false
}

func Names() []string {
	catalog := Catalog()
	names := make([]string, 0, len(catalog))
	for _, theme := range catalog {
		names = append(names, theme.Name)
	}
	sort.Strings(names)
	return names
}

func openVibelyDarkTheme() Theme {
	semantic := map[string]string{
		// Original OpenVibely dark used DaisyUI dark base-200 for the page canvas
		// and base-100 for sidebar/cards/modals. Keep those roles exact so the
		// selectable default theme is a true "switch back" target.
		"bg": "#1D232A", "contentBg": "#191E24", "sidebarBg": "#1D232A", "surface": "#1D232A", "surfaceRaised": "#1D232A",
		"text": "#A6ADBB", "textMuted": "#A6ADBB", "border": "#15191E", "borderStrong": "#303844", "automationNodeBorder": "#303844", "automationEdge": "#A6ADBB", "yamlIndentRail": "#303844",
		"primary": "#646FE4", "primaryForeground": "#FFFFFF", "focus": "#7480FF", "link": "#7480FF",
		"inputBg": "#1D232A", "inputBorder": "#303844", "buttonBg": "#646FE4", "buttonFg": "#FFFFFF",
		"buttonHoverBg": "#646FE4", "buttonHoverFg": "#FFFFFF", "buttonSecondaryBg": "#273140", "buttonSecondaryFg": "#A6ADBB",
		"selectionBg": "rgba(100, 111, 228, 0.32)", "hoverBg": "rgba(255, 255, 255, 0.10)",
		"info": "#00A1D8", "infoForeground": "#FFFFFF", "success": "#00B682", "successForeground": "#000000", "warning": "#E6AF29", "warningForeground": "#000000", "error": "#E66465", "errorForeground": "#000000", "diffAddBg": "#1E3A38", "diffAddFg": "#559B70", "diffModifyBg": "rgba(230, 175, 41, 0.16)", "diffModifyFg": "#E6AF29", "diffDeleteBg": "#3D2C34", "diffDeleteFg": "#BD7076",
		"codeBg": "#1E1E1E", "codeFg": "#D4D4D4",
	}
	syntax := map[string]string{
		"background": "#1E1E1E", "foreground": "#D4D4D4", "comment": "#6A9955", "string": "#CE9178", "number": "#B5CEA8",
		"keyword": "#569CD6", "function": "#DCDCAA", "type": "#4EC9B0", "variable": "#9CDCFE", "title": "#DCDCAA", "error": "#F44747",
	}
	return openVibelyTheme(OpenVibelyDarkThemeID, "OpenVibely Dark", KindDark, "dark", semantic, syntax)
}

func openVibelyLightTheme() Theme {
	semantic := map[string]string{
		// Original OpenVibely light mode used the explicit light aliases from the
		// layout: #F5F5F5 for the app canvas and #FAFAFA for cards, sidebar,
		// dropdowns, inputs, and modals.
		"bg": "#FAFAFA", "contentBg": "#F5F5F5", "sidebarBg": "#FAFAFA", "surface": "#FAFAFA", "surfaceRaised": "#FAFAFA",
		"text": "#1F1F1F", "textMuted": "#616161", "border": "#E5E5E5", "borderStrong": "#CECECE", "automationNodeBorder": "#CECECE", "automationEdge": "#1F1F1F", "yamlIndentRail": "#CECECE",
		"primary": "#7480FF", "primaryForeground": "#FFFFFF", "focus": "#7480FF", "link": "#646FE4",
		"inputBg": "#FAFAFA", "inputBorder": "#CECECE", "buttonBg": "#7480FF", "buttonFg": "#FFFFFF",
		"buttonHoverBg": "#4F3CB8", "buttonHoverFg": "#FFFFFF", "buttonSecondaryBg": "#E8E8E8", "buttonSecondaryFg": "#3B3B3B",
		"selectionBg": "rgba(116, 128, 255, 0.24)", "hoverBg": "#F2F2F2",
		"info": "#2094F3", "infoForeground": "#FFFFFF", "success": "#2EA043", "successForeground": "#FFFFFF", "warning": "#895503", "warningForeground": "#FFFFFF", "error": "#C72E0F", "errorForeground": "#FFFFFF", "diffAddBg": "#DDEDE0", "diffAddFg": "#317A4A", "diffModifyBg": "rgba(187, 128, 9, 0.22)", "diffModifyFg": "#895503", "diffDeleteBg": "#FAE3E1", "diffDeleteFg": "#A65353",
		"codeBg": "#0000001F", "codeFg": "#24292F",
	}
	syntax := map[string]string{
		"background": "#0000001F", "foreground": "#24292F", "comment": "#6A737D", "string": "#032F62", "number": "#005CC5",
		"keyword": "#D73A49", "function": "#6F42C1", "type": "#005CC5", "variable": "#24292F", "title": "#6F42C1", "error": "#D73A49",
	}
	return openVibelyTheme(OpenVibelyLightThemeID, "OpenVibely Light", KindLight, "light", semantic, syntax)
}

func openVibelyTheme(id, name, kind, mode string, semantic, syntax map[string]string) Theme {
	return Theme{
		ID:       id,
		Name:     name,
		Source:   "OpenVibely built-in",
		Kind:     kind,
		Mode:     mode,
		Upstream: "OpenVibely",
		Semantic: semantic,
		Syntax:   syntax,
		Preview: map[string]string{
			"bg": semantic["bg"], "sidebar": semantic["sidebarBg"], "surface": semantic["surface"], "text": semantic["text"], "accent": semantic["primary"],
			"syntaxKeyword": syntax["keyword"], "syntaxString": syntax["string"], "syntaxFunction": syntax["function"], "syntaxComment": syntax["comment"],
		},
		CSS:        themeCSSVars(semantic, syntax),
		SearchText: name + " " + kind + " default OpenVibely built-in native app theme",
	}
}

func themeCSSVars(semantic, syntax map[string]string) map[string]string {
	css := map[string]string{}
	for key, value := range semantic {
		css["--ov-"+camelToKebab(key)] = value
	}
	for key, value := range syntax {
		css["--ov-syntax-"+camelToKebab(key)] = value
	}
	return css
}

func camelToKebab(s string) string {
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
