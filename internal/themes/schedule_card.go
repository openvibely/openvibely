package themes

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
	for amount := 0.24; amount > 0; amount -= 0.02 {
		card := hexColor(mixRGB(raised, accent, amount))
		rgb, _, _ := parseThemeHexColor(card)
		if contrastRatio(text, rgb) >= floor {
			return card
		}
	}
	return hexColor(raised)
}
