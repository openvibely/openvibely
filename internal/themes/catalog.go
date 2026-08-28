package themes

import (
	"encoding/json"
	"sort"
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
	catalog := make([]Theme, 0, ExpectedThemeCount+2)
	catalog = append(catalog, OpenVibelyThemes()...)
	catalog = append(catalog, VSCodeCatalog()...)
	return catalog
}

func VSCodeCatalog() []Theme {
	var catalog []Theme
	_ = json.Unmarshal([]byte(CatalogJSONRaw), &catalog)
	return catalog
}

func OpenVibelyThemes() []Theme {
	return []Theme{
		openVibelyDarkTheme(),
		openVibelyLightTheme(),
	}
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
			Roles [3]string         `json:"r"`
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
			runtime = append(runtime, runtimeTheme{
				ID: theme.ID, Kind: theme.Kind, Mode: theme.Mode, CSS: css,
				Roles: [3]string{theme.CSS["--ov-automation-node-border"], theme.CSS["--ov-automation-edge"], theme.CSS["--ov-yaml-indent-rail"]},
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
