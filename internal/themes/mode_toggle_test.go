package themes

import "testing"

func TestModeTogglePaletteContrast(t *testing.T) {
	for _, theme := range Catalog() {
		t.Run(theme.ID, func(t *testing.T) {
			roles := toggleRuntimeRoles(theme)
			track, _, _ := parseThemeHexColor(roles[3])
			thumb, _, _ := parseThemeHexColor(roles[4])
			active, _, _ := parseThemeHexColor(roles[5])
			inactive, _, _ := parseThemeHexColor(roles[6])
			if ratio := contrastRatio(active, thumb); ratio < 4.5 {
				t.Errorf("selected contrast %.2f", ratio)
			}
			if ratio := contrastRatio(mixRGB(track, inactive, .5), track); ratio < 3 {
				t.Errorf("faded inactive contrast %.2f", ratio)
			}
			for _, channel := range thumb {
				if channel < 190 {
					t.Errorf("circle is not pale: %s", roles[4])
					break
				}
			}
		})
	}
}
