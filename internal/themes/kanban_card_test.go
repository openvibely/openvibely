package themes

import "testing"

func TestKanbanCardsStandOutFromColumnsAndStayReadable(t *testing.T) {
	for _, theme := range Catalog() {
		if theme.Source == "OpenVibely built-in" {
			continue
		}
		card, _, _ := parseThemeHexColor(kanbanCardBackground(theme))
		column, _, _ := parseThemeHexColor(theme.CSS["--ov-sidebar-bg"])
		surface, _, _ := parseThemeHexColor(theme.CSS["--ov-surface"])
		if got := rgbDistance(card, column); got < kanbanCardColumnDistance {
			t.Errorf("%s task card blends into column: distance %.1f", theme.ID, got)
		}
		for _, role := range []string{"--ov-text", "--ov-text-muted"} {
			fg, _, _ := parseThemeHexColor(theme.CSS[role])
			if got, floor := contrastRatio(fg, card), min(4.5, contrastRatio(fg, surface)-0.15); got < floor {
				t.Errorf("%s %s contrast on task card %.2f < %.2f", theme.ID, role, got, floor)
			}
		}
	}
}
