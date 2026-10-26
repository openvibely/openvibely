# Theme surface treatments

Each bundled palette and VS Code theme supplies `SurfaceCSS` from `internal/themes/surfaces.go`. OpenVibely Dark retains its existing definition in `catalog.go`. This is part of the theme catalog, not a collection of theme-ID overrides in the layout stylesheet.

The three shades are deliberately mapped to OpenVibely roles rather than copying an editor's sidebar/title-bar assignments:

| Role | Default | Layered |
| --- | --- | --- |
| Page | Page shade | Same page shade |
| Task list | Page shade | Card shade |
| Cards | Card shade | Same card shade |
| Outer frame | Card shade | Frame shade |
| Card outline | Theme outline | Frame shade (high-contrast themes keep their explicit outline) |

Page, card, and frame shades increase in brightness. In light themes this means a softly tinted page and lighter cards/frame. Default removes the extra rail/frame separation even if the upstream theme is already layered. Layered selects the defined shades directly; it never repeatedly brightens an already-layered theme.

Text, accents, status colors, syntax, and diff colors are unchanged. Code backgrounds retain their existing theme values. Mobile uses the same page/card colors as desktop and the Appearance previews. User-imported themes still use the existing generic fallback; they are not part of this bundled-theme mapping.

## Sources and adaptation

- [Catppuccin](https://github.com/catppuccin/catppuccin): Mocha uses mantle/base/surface0; Latte uses crust/mantle/base.
- [Tokyo Night](https://github.com/tokyo-night/tokyo-night-vscode-theme): night and storm background shades.
- [Nord](https://www.nordtheme.com/docs/colors-and-palettes/): nord0/nord1/nord2.
- [Dracula](https://draculatheme.com/contribute): existing dark and background shades, with a softer intermediate frame than the bright selection color.
- [Rosé Pine](https://rosepinetheme.com/palette/): base/surface/overlay; Dawn uses its warm low/background/surface shades.
- [GitHub Primer](https://github.com/primer/primitives): existing neutral background shades.
- VS Code: the repository's existing pinned catalog (`PinnedVSCodeRepository`, `PinnedVSCodeRef` in `catalog_gen.go`) remains the source of all semantic/syntax colors. Its title-bar and sidebar assignments are remapped for OpenVibely. The generator and upstream snapshot are unchanged.

Some themes do not provide three suitable background shades. Their explicit palettes include restrained intermediate shades rather than reusing bright selection colors. Abyss and Solarized Dark keep their frames dark enough for their softer text. Solarized Light retains its original text color and does not reduce contrast below its original card background. High-contrast themes use near-black/near-white steps and retain their theme outlines.

These surface mappings are OpenVibely adaptations, not claims that upstream authors prescribe this application layout. Tests check every bundled mapping, desktop/mobile previews, and preservation of accent/status/syntax colors when switching treatments.
