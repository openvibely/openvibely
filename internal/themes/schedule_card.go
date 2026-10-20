package themes

import (
	"fmt"
	"math"
)

const scheduleCardSelectionDistance = 24

// scheduleTaskBackground tints the raised surface with the accent, backing off
// where the theme's own text would lose readability on the tinted card.
func scheduleTaskBackground(theme Theme) string {
	raised, _, okRaised := parseThemeHexColor(theme.CSS["--ov-surface-raised"])
	accent, _, okAccent := parseThemeHexColor(theme.CSS["--ov-link"])
	text, _, okText := parseThemeHexColor(theme.CSS["--ov-text"])
	if !okRaised || !okAccent || !okText {
		return theme.CSS["--ov-surface-raised"]
	}
	floor := min(4.5, contrastRatio(text, raised)-0.15)
	for amount := 0.30; amount > 0; amount -= 0.02 {
		card := hexColor(mixRGB(raised, accent, amount))
		rgb, _, _ := parseThemeHexColor(card)
		if contrastRatio(text, rgb) >= floor {
			return card
		}
	}
	return hexColor(raised)
}

// scheduleSelectionBackground is a translucent accent wash for selected days,
// nudged from the default strength until cards inside the selected column
// remain visibly distinct.
func scheduleSelectionBackground(theme Theme, card string) string {
	surface, _, okSurface := parseThemeHexColor(theme.CSS["--ov-surface"])
	focus, _, okFocus := parseThemeHexColor(theme.CSS["--ov-focus"])
	cardRGB, _, okCard := parseThemeHexColor(card)
	if !okSurface || !okFocus || !okCard {
		return theme.CSS["--ov-accent-tint"]
	}
	alpha := 0.16
	for step := 0; step <= 16; step++ {
		candidate := 0.16 + float64((step+1)/2)*0.01
		if step%2 == 1 {
			candidate = 0.16 - float64((step+1)/2)*0.01
		}
		candidate = math.Round(candidate*255) / 255
		if rgbDistance(mixRGB(surface, focus, candidate), cardRGB) >= scheduleCardSelectionDistance {
			alpha = candidate
			break
		}
	}
	return fmt.Sprintf("#%02X%02X%02X%02X", uint8(focus[0]), uint8(focus[1]), uint8(focus[2]), uint8(math.Round(alpha*255)))
}

func rgbDistance(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}
