package themes

const (
	kanbanColumnCardDistance = 14
	kanbanColumnPageDistance = 14
)

// kanbanColumnBackground keeps task columns and the title bar visibly apart from
// both the cards and the page, as in the default themes, since many palettes
// use one color for all three.
func kanbanColumnBackground(theme Theme) string {
	sidebar := theme.CSS["--ov-sidebar-bg"]
	column, _, okColumn := parseThemeHexColor(sidebar)
	card, _, okCard := parseThemeHexColor(theme.CSS["--ov-surface"])
	page, _, okPage := parseThemeHexColor(theme.CSS["--ov-content-bg"])
	text, _, okText := parseThemeHexColor(theme.CSS["--ov-text"])
	muted, _, okMuted := parseThemeHexColor(theme.CSS["--ov-text-muted"])
	if !okColumn || !okCard || !okPage || !okText || !okMuted {
		return sidebar
	}
	distinct := func(rgb [3]float64) bool {
		return rgbDistance(rgb, card) >= kanbanColumnCardDistance && rgbDistance(rgb, page) >= kanbanColumnPageDistance
	}
	if distinct(column) {
		return sidebar
	}
	readable := func(rgb [3]float64) bool {
		for _, fg := range [][3]float64{text, muted} {
			if contrastRatio(fg, rgb) < min(4.5, contrastRatio(fg, column)-0.15) {
				return false
			}
		}
		return true
	}
	for _, target := range [][3]float64{{}, {255, 255, 255}} {
		if candidate, ok := kanbanColumnStep(column, target, readable, distinct); ok {
			return candidate
		}
	}
	return sidebar
}

func kanbanColumnStep(column, target [3]float64, readable, distinct func([3]float64) bool) (string, bool) {
	for step := 1; step <= 100; step++ {
		candidate := hexColor(mixRGB(column, target, float64(step)/100))
		rgb, _, _ := parseThemeHexColor(candidate)
		if !readable(rgb) {
			return "", false
		}
		if distinct(rgb) {
			return candidate, true
		}
	}
	return "", false
}
