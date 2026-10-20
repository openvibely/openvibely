package themes

import "testing"

func TestKanbanColumnsStandApartFromCardsAndPageAndStayReadable(t *testing.T) {
	for _, theme := range Catalog() {
		if theme.Source == "OpenVibely built-in" {
			continue
		}
		column, _, _ := parseThemeHexColor(kanbanColumnBackground(theme))
		card, _, _ := parseThemeHexColor(theme.CSS["--ov-surface"])
		sidebar, _, _ := parseThemeHexColor(theme.CSS["--ov-sidebar-bg"])
		page, _, _ := parseThemeHexColor(theme.CSS["--ov-content-bg"])
		if got := rgbDistance(page, column); got < kanbanColumnPageDistance {
			t.Errorf("%s task column blends into page: distance %.1f", theme.ID, got)
		}
		if got := rgbDistance(card, column); got < kanbanColumnCardDistance {
			t.Errorf("%s task column blends into cards: distance %.1f", theme.ID, got)
		}
		for _, role := range []string{"--ov-text", "--ov-text-muted"} {
			fg, _, _ := parseThemeHexColor(theme.CSS[role])
			if got, floor := contrastRatio(fg, column), min(4.5, contrastRatio(fg, sidebar)-0.15); got < floor {
				t.Errorf("%s %s contrast on task column %.2f < %.2f", theme.ID, role, got, floor)
			}
		}
	}
}
