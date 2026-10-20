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
