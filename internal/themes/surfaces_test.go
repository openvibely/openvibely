package themes

import "testing"

func TestBundledSurfaceArrangements(t *testing.T) {
	for _, theme := range Catalog() {
		if theme.ID == OpenVibelyDarkThemeID {
			continue
		}
		t.Run(theme.ID, func(t *testing.T) {
			css := theme.SurfaceCSS
			page, card, frame := css["--ov-surface-default-page"], css["--ov-surface-default-card"], css["--ov-surface-layered-frame"]
			if page == "" || card == "" || frame == "" {
				t.Fatal("missing theme-owned surface palette")
			}
			luminance := func(c string) float64 {
				rgb, _, ok := parseThemeHexColor(c)
				if !ok {
					t.Fatalf("invalid color %q", c)
				}
				return relativeLuminance(rgb)
			}
			if theme.Mode == "light" {
				if !(luminance(frame) < luminance(card) && luminance(card) < luminance(page)) {
					t.Fatal("light surfaces must brighten from frame to card to page")
				}
			} else if !(luminance(page) < luminance(card) && luminance(card) < luminance(frame)) {
				t.Fatal("dark surfaces must brighten from page to card to frame")
			}
			if css["--ov-surface-default-rail"] != page || css["--ov-surface-default-frame"] != card || css["--ov-surface-layered-page"] != page || css["--ov-surface-layered-card"] != card {
				t.Fatal("treatments must change frame and rail, not page and card")
			}
			text, _, _ := parseThemeHexColor(theme.Semantic["text"])
			// Preserve upstream softer text where it already falls below 4.5.
			floor := 4.5
			for _, role := range []string{"contentBg", "surface"} {
				rgb, _, _ := parseThemeHexColor(theme.Semantic[role])
				if ratio := contrastRatio(text, rgb); ratio < floor {
					floor = ratio
				}
			}
			for _, c := range []string{page, card, frame} {
				rgb, _, _ := parseThemeHexColor(c)
				if contrastRatio(text, rgb)+0.001 < floor {
					t.Errorf("text on %s has insufficient contrast: %.2f", c, contrastRatio(text, rgb))
				}
			}
			if theme.Kind == KindHighContrastDark || theme.Kind == KindHighContrastLight {
				if css["--ov-surface-layered-border"] != theme.Semantic["border"] {
					t.Fatal("high contrast outline lost")
				}
			}
		})
	}
}
