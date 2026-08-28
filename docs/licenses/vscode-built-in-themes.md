# VS Code Built-In Themes Attribution

OpenVibely includes normalized color data generated from the first-party built-in color theme extensions in the open-source microsoft/vscode repository.

- Upstream: https://github.com/microsoft/vscode.git
- Pinned tag: 1.130.0
- Imported extensions: theme-abyss, theme-defaults, theme-kimbie-dark, theme-monokai, theme-monokai-dimmed, theme-quietlight, theme-red, theme-solarized-dark, theme-solarized-light, theme-tomorrow-night-blue
- Generated catalog: internal/themes/catalog_gen.go
- Update command: `go run ./internal/themes/cmd/generate`

## Bundled Themes

- Dark Modern (dark)
- Light Modern (light)
- Abyss (dark)
- Dark (Visual Studio) (dark)
- Dark 2026 (dark)
- Dark High Contrast (high-contrast-dark)
- Dark+ (dark)
- Kimbie Dark (dark)
- Light (Visual Studio) (light)
- Light 2026 (light)
- Light High Contrast (high-contrast-light)
- Light+ (light)
- Monokai (dark)
- Monokai Dimmed (dark)
- Quiet Light (light)
- Red (dark)
- Solarized Dark (dark)
- Solarized Light (light)
- Tomorrow Night Blue (dark)

## Semantic Color Mapping

Theme generation reads the built-in VS Code extension manifests, resolves each contributed theme JSON file including its `include` chain, and then normalizes VS Code workbench colors into OpenVibely roles. Missing, transparent, or invalid upstream colors are not emitted directly; each role falls back to a theme-kind default or to a value derived from another validated role.

Primary derivation rules:

- Application and content backgrounds prefer VS Code editor/window backgrounds first so the whole OpenVibely canvas follows the selected palette; sidebar, surface, raised surface, foreground, muted foreground, borders, links, inputs, buttons, selection, hover, focus, status, diff, and code-block roles are then selected from matching VS Code workbench color keys when present.
- Transparent upstream colors are skipped so they cannot erase readable surfaces; invalid CSS color values are rejected at generation time.
- Muted text and borders are derived by mixing validated foreground and background colors when VS Code does not provide a direct role.
- Automation node borders, connector colors, and YAML indentation rails are derived from their actual drawing surfaces; when an upstream border or muted color is too close, a theme-aware foreground mix is used to enforce visible contrast.
- Button foregrounds, button hover foregrounds, secondary button colors, and status button foregrounds fall back to validated VS Code button/status keys or to black/white based on the generated background luminance.
- Selection, hover, and diff backgrounds fall back to generated `rgba(...)` colors derived from the accent, success, warning, and error roles.
- Code-block and syntax colors prefer VS Code editor and token scopes, then fall back to bundled light or dark syntax defaults.
- High-contrast-light and high-contrast-dark themes use the same mapping pipeline but retain their high-contrast `kind` in the generated catalog and DOM attribute.

These rules are implemented in `internal/themes/cmd/generate/main.go` and the sanitized output is committed in `internal/themes/catalog_gen.go` for offline runtime use.

## License

The imported upstream theme data is from Visual Studio Code, licensed under the MIT License. The upstream license text at tag 1.130.0 follows.

```text
MIT License

Copyright (c) 2015 - present Microsoft Corporation

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
