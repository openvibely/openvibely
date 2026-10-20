package themes

import "testing"

func TestEveryAppliedThemeDefinesItsKanbanColumnColor(t *testing.T) {
	native := map[string]bool{}
	for _, theme := range OpenVibelyThemes() {
		native[theme.ID] = true
	}
	for _, theme := range Catalog() {
		if native[theme.ID] {
			continue
		}
		if _, _, ok := parseThemeHexColor(theme.CSS["--ov-kanban-column-bg"]); !ok {
			t.Errorf("%s has no kanban column color", theme.ID)
		}
	}
}

func TestRosePineDawnTabDividerStandsOutOnTitleBar(t *testing.T) {
	theme, ok := ByID("rose-pine-dawn")
	if !ok {
		t.Fatal("rose-pine-dawn missing")
	}
	divider, _, _ := parseThemeHexColor(theme.CSS["--ov-project-tab-divider"])
	column, _, _ := parseThemeHexColor(theme.CSS["--ov-kanban-column-bg"])
	if got := contrastRatio(divider, column); got < 1.3 {
		t.Errorf("tab divider contrast on title bar %.2f < 1.3", got)
	}
}
