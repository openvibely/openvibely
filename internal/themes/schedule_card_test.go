package themes

import "testing"

func TestScheduleCardsAreTintedReadableAndDistinctFromSelection(t *testing.T) {
	for _, theme := range Catalog() {
		if theme.Source == "OpenVibely built-in" {
			continue
		}
		card := scheduleTaskBackground(theme)
		if card == theme.CSS["--ov-surface-raised"] {
			t.Errorf("%s schedule card is not tinted", theme.ID)
		}
		text, _, _ := parseThemeHexColor(theme.CSS["--ov-text"])
		raised, _, _ := parseThemeHexColor(theme.CSS["--ov-surface-raised"])
		bg, _, _ := parseThemeHexColor(card)
		surface, _, _ := parseThemeHexColor(theme.CSS["--ov-surface"])
		focus, alpha, _ := parseThemeHexColor(scheduleSelectionBackground(theme, card))
		if got := rgbDistance(mixRGB(surface, focus, alpha), bg); got < scheduleCardSelectionDistance {
			t.Errorf("%s schedule card blends into selected day: distance %.1f", theme.ID, got)
		}
		if got, floor := contrastRatio(text, bg), min(4.5, contrastRatio(text, raised)-0.15); got < floor {
			t.Errorf("%s schedule card text contrast %.2f < %.2f", theme.ID, got, floor)
		}
	}
}
