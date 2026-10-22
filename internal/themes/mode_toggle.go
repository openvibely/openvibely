package themes

// toggleRuntimeRoles keeps control-specific colors out of the full CSS catalog.
func toggleRuntimeRoles(theme Theme) [7]string {
	accent := theme.CSS["--ov-link"]
	sidebar := theme.CSS["--ov-sidebar-bg"]
	track := mixHex(sidebar, "#FFFFFF", 0.10)
	target := "#FFFFFF"
	if theme.Mode == "light" {
		track = mixHex(sidebar, "#000000", 0.08)
		target = "#000000"
	}
	// A pale, gently tinted circle and desaturated accent preserve the native
	// control's visual weight instead of borrowing saturated button fills.
	thumb := mixHex("#E6E6E6", accent, 0.16)
	active := ensureContrast(mixHex(accent, "#686868", 0.45), []string{thumb}, 4.5, "#202020")
	inactive := mixHex(track, target, 0.55)
	bg, _, _ := parseThemeHexColor(track)
	start, _, _ := parseThemeHexColor(inactive)
	goal, _, _ := parseThemeHexColor(target)
	// The existing inactive icon opacity is 0.5. Check the displayed color,
	// rather than claiming contrast for the unfaded token.
	for step := 0; step <= 40; step++ {
		inactive = hexColor(mixRGB(start, goal, float64(step)/40))
		candidate, _, _ := parseThemeHexColor(inactive)
		if contrastRatio(mixRGB(bg, candidate, 0.5), bg) >= 3 {
			break
		}
	}
	return [7]string{theme.CSS["--ov-automation-node-border"], theme.CSS["--ov-automation-edge"], theme.CSS["--ov-yaml-indent-rail"], track, thumb, active, inactive}
}
