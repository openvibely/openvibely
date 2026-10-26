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

Dark themes brighten from page to card to frame. Light themes reverse that order: a softly shaded frame, lighter cards/task list, and the brightest page. Default removes the extra rail/frame separation even if the upstream theme is already layered. Layered selects the defined shades directly; it never repeatedly brightens an already-layered theme.

Text, accents, status colors, syntax, and diff colors are unchanged. Code backgrounds retain their existing theme values except Light Modern: its upstream code and card backgrounds are identical, so conversation code boxes use its existing `list.hoverBackground` shade (`#F2F2F2`) to remain distinct inside assistant bubbles. Mobile uses the same page/card colors as desktop and the Appearance previews. User-imported themes still use the existing generic fallback; they are not part of this bundled-theme mapping.

## Sources and adaptation

- [Catppuccin](https://github.com/catppuccin/catppuccin): Mocha uses mantle/base/surface0 for page/card/frame; Latte uses base/mantle/crust.
- [Tokyo Night](https://github.com/tokyo-night/tokyo-night-vscode-theme): night and storm background shades.
- [Nord](https://www.nordtheme.com/docs/colors-and-palettes/): nord0/nord1/nord2.
- [Dracula](https://github.com/dracula/visual-studio-code/blob/master/src/dracula.yml): BGDark/BG/BGLight; all three are upstream shades.
- [Rosé Pine](https://rosepinetheme.com/palette/): base/surface/overlay; Dawn uses its warm low/background/surface shades.
- [GitHub Primer](https://github.com/primer/primitives): neutral background shades from the published `@primer/primitives` 7.0.0 palette (including Light `#EAEEF2`).
- VS Code: the repository's existing pinned catalog (`PinnedVSCodeRepository`, `PinnedVSCodeRef` in `catalog_gen.go`) remains the source of all semantic/syntax colors. Its title-bar and sidebar assignments are remapped for OpenVibely. The generator and upstream snapshot are unchanged.

Surface colors reuse upstream shades rather than interpolating new ones. The reviewed VS Code mappings use the pinned 1.130.0 theme files (including their `include` parents). Specific replacements:

| Theme | Upstream roles used for page / card / frame |
| --- | --- |
| Abyss | editor.background / titleBar.activeBackground / input.background |
| Light Modern | editor.background / sideBar.background / list.hoverBackground |
| Classic Dark and Dark+ | editor.background / menu.background / default tab.inactiveBackground |
| Quiet Light | editor.background / sideBar.background / activityBar.background |
| Solarized Dark | sideBar.background / editor.background / titleBar.activeBackground |
| Solarized Light | editor.background / notebook.cellEditorBackground / sideBar.background |

Classic Dark's inactive-tab shade comes from [VS Code's workbench defaults](https://github.com/microsoft/vscode/blob/1.130.0/src/vs/workbench/common/theme.ts), rather than a color added by OpenVibely. High-contrast themes are deliberately exempt from the three-shade requirement: both treatments retain pure black or white and the original explicit outlines. Inventing gray backgrounds just to distinguish the treatments would change their intended contrast.

OpenVibely-branded palettes are our own designs. Imported themes still have generic CSS fallbacks, and the optional conversation color-step effect still blends colors. Shared surface CSS remains necessary to map the selected theme's roles onto app components; this is not an unmodified upstream editor layout.

These surface mappings are OpenVibely adaptations, not claims that upstream authors prescribe this application layout. Tests check every bundled mapping, desktop/mobile previews, and preservation of accent/status/syntax colors when switching treatments.
