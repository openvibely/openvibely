package themes

const kanbanCardColumnDistance = 14

// kanbanCardBackground keeps task cards a visible step lighter than their
// column, as in the default themes, since many palettes use one color for both
// surfaces. When lightening would hurt text contrast, cards step darker.
func kanbanCardBackground(theme Theme) string {
	surface := theme.CSS["--ov-surface"]
	card, _, okCard := parseThemeHexColor(surface)
	column, _, okColumn := parseThemeHexColor(theme.CSS["--ov-sidebar-bg"])
	text, _, okText := parseThemeHexColor(theme.CSS["--ov-text"])
	muted, _, okMuted := parseThemeHexColor(theme.CSS["--ov-text-muted"])
	if !okCard || !okColumn || !okText || !okMuted || rgbDistance(card, column) >= kanbanCardColumnDistance {
		return surface
	}
	readable := func(rgb [3]float64) bool {
		for _, fg := range [][3]float64{text, muted} {
			if contrastRatio(fg, rgb) < min(4.5, contrastRatio(fg, card)-0.15) {
				return false
			}
		}
		return true
	}
	if candidate, ok := kanbanCardStep(column, [3]float64{255, 255, 255}, readable); ok {
		return candidate
	}
	if candidate, ok := kanbanCardStep(column, [3]float64{}, readable); ok {
		return candidate
	}
	return surface
}

func kanbanCardStep(column, target [3]float64, readable func([3]float64) bool) (string, bool) {
	for step := 1; step <= 100; step++ {
		candidate := hexColor(mixRGB(column, target, float64(step)/100))
		rgb, _, _ := parseThemeHexColor(candidate)
		if !readable(rgb) {
			return "", false
		}
		if rgbDistance(rgb, column) >= kanbanCardColumnDistance {
			return candidate, true
		}
	}
	return "", false
}
