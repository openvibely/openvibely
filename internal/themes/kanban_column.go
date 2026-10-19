package themes

const kanbanColumnCardDistance = 14

// kanbanColumnBackground recesses task columns and the title bar below the card
// surface, as in the default themes, since many palettes use one color for both.
func kanbanColumnBackground(theme Theme) string {
	sidebar := theme.CSS["--ov-sidebar-bg"]
	column, _, okColumn := parseThemeHexColor(sidebar)
	card, _, okCard := parseThemeHexColor(theme.CSS["--ov-surface"])
	text, _, okText := parseThemeHexColor(theme.CSS["--ov-text"])
	muted, _, okMuted := parseThemeHexColor(theme.CSS["--ov-text-muted"])
	if !okColumn || !okCard || !okText || !okMuted || rgbDistance(card, column) >= kanbanColumnCardDistance {
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
		if candidate, ok := kanbanColumnStep(column, card, target, readable); ok {
			return candidate
		}
	}
	return sidebar
}

func kanbanColumnStep(column, card, target [3]float64, readable func([3]float64) bool) (string, bool) {
	for step := 1; step <= 100; step++ {
		candidate := hexColor(mixRGB(column, target, float64(step)/100))
		rgb, _, _ := parseThemeHexColor(candidate)
		if !readable(rgb) {
			return "", false
		}
		if rgbDistance(rgb, card) >= kanbanColumnCardDistance {
			return candidate, true
		}
	}
	return "", false
}
