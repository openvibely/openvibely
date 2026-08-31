---
name: ui_theming
type: project
created: 2026-08-11
updated: 2026-08-30
source: after_complete
source_id: d8ce0caca4111ec20dfc1f3906d85000:54a19951cbe52c56
confidence: high
title: UI Theming
---

OpenVibely has a built-in theme system for application presentation preferences, including native OpenVibely defaults and imported VS Code palettes.

Core theming contracts:
- `/themes` is the dedicated Themes page and is linked from the sidebar. It supports full-page and HTMX fragment rendering. Selected theme is browser/application state, not project configuration. Selecting a theme updates active-card state in place without reordering catalog/cards.
- Selectable catalog prepends native `OpenVibely Dark` (`openvibely-dark`) and `OpenVibely Light` (`openvibely-light`) before imported VS Code themes. They are bundled default/fallback IDs and should restore original pre-VS-Code OpenVibely palettes/styling.
- Dark native canvas/surfaces use original dark values such as page/content `#191E24`, sidebar/cards/modals/inputs `#1D232A`, and base border `#15191E`. Light native canvas uses `#F5F5F5`; surfaces use `#FAFAFA`.
- Built-in VS Code themes are imported offline from open-source `microsoft/vscode` pinned at tag `1.130.0`; reproducible update command is `go run ./internal/themes/cmd/generate`.
- Generated VS Code catalog lives under `internal/themes` and includes 19 first-party VS Code color themes. Full selectable catalog is 21 themes including the two OpenVibely defaults.
- Theme JSON includes/inheritance are resolved at generation time. Runtime values are sanitized OpenVibely semantic colors, syntax colors, and preview colors; the app must not fetch VS Code/GitHub/Marketplace data at runtime.
- DOM state separates compatibility mode from exact palette: `data-theme="light|dark"` for DaisyUI/existing selectors and `data-color-theme="<stable-theme-id>"` for exact selected palette. High-contrast themes retain exact identity.
- Theme selection is an app/system UI preference stored as `app_settings.ui.theme`; default is `openvibely-dark`. `localStorage.theme` is only same-browser/same-origin mirror and early-apply helper, never cross-restart/cross-port authority.
- Legacy `localStorage.theme` values `light`/`dark` migrate/fallback to OpenVibely Light/Dark; invalid exact IDs safely fall back to defaults.
- Page loads must not depend on a browser preference GET before rendering. Full-document server render reads DB setting and embeds early theme state to avoid flash. Ordinary HTMX fragments should not reread app settings for theme preferences.
- User theme changes, including footer mode toggles, update DOM/localStorage immediately and save stable theme ID to DB through background `POST /ui/preferences`; visible changes do not wait for save.
- If frontend later separates from server rendering, DB-backed theme persistence can remain authoritative but no-flash first paint requires server/native-shell bootstrap or equivalent preferences payload.
- Footer sun/moon control toggles between most recently selected light-side and dark-side themes. Light mode toggle track must be light; dark mode keeps dark track. Footer controls must sync after DOM insertion.
- Apply theme CSS variables early before paint. Base layout embeds only compact runtime catalog needed for early application; full catalog remains server-side for Themes page.
- Highlight.js must not load a fixed GitHub Dark stylesheet. Rendered Markdown/code syntax colors derive from selected theme and work for initial/HTMX content.
- Native light-mode Alert inspection inline code uses `--ov-l-surface-active` with `--ov-l-text-strong` through `[data-theme="light"] [data-alert-markdown] :not(pre) > code`; this avoids interpreting DaisyUI's OKLCH `--b2` value through the generic `hsl(var(--b2))` rule. Fenced `<pre><code>` blocks and shared Chat/task-thread bubble styling remain unchanged, and the hydrated notification regression requires at least 4.5:1 computed contrast.
- Running task status uses the shared `.task-state-running` selector and must exactly match the chat send button's normal primary-action color, with one light value and one dark value. Both controls consume one shared primary-action token rather than duplicated literals. On 2026-08-30, the implementation introduced `--ov-primary-action-color` and `chat-send-button`, corrected the native dark normal state to `#7480ff` (the prior `#646fe4` is the native dark hover value), and kept normal declarations non-important so native hover rules remain authoritative. The follow-up now drives Chrome's actual pointer through CDP and verifies computed normal and `:hover` colors for native dark, native light, and imported themes; it confirms the Send color changes on hover while the running icon retains the normal shared token. That interaction exposed a cascade issue in which native generic/primary rules matched imported themes; native generic/primary hover rules and the native light primary base rule are now scoped to their native `data-color-theme` IDs, preserving imported primary/hover behavior without `!important` on shared Send rules. Deterministic templ generation, the server build, all internal tests, all template tests, and `git diff --check` passed. A fresh strict read-only audit of exact head `279f16dc40a1468d0904a0e2d6026013b59a5471` found no material bugs, regressions, or missing requirements, confirmed the worktree clean and the branch `0 behind / 5 ahead` of `main` with no task-side merge commits, and made no workspace changes or validation runs.

Exact imported-theme styling:
- Imported VS Code application/content backgrounds must follow selected exact palette, not DaisyUI fallbacks. `contentBg` should prefer editor/window backgrounds, applied to root/page canvas, `.drawer-content`, and `#main-content`.
- Modal surfaces, action strips, close-button hover/focus, and backdrop treatment should derive from semantic variables such as `--ov-surface-raised`, `--ov-text`, `--ov-border`, `--ov-hover-bg`, and `--ov-focus`.
- Cards, searchable cards, dropdown action cards, borders, dividers, and directional border utilities should use subtle generated surface/border roles. Hover border highlights are reserved for clickable/actionable cards and controls.
- Schedule and task-board surfaces, grid/timeline accents, today highlights, task chips, legend swatches, drag feedback, selected states, dropzones, and legacy link/accent tokens should use generated semantic variables rather than fixed DaisyUI colors.
- Task dropzone drag feedback must preserve dashed-outline/dropzone geometry. Active `.drag-over` overrides must win over transparent baseline overrides without changing geometry.
- Generated semantic variables should be visibly consumed by imported-theme CSS for status feedback, selection, hover, focus, input borders, card borders, modal surfaces, schedule/dropzone states, and diff/review add/modify/delete backgrounds.
- Native `openvibely-*` defaults should not be caught by invasive VS Code layout/surface/border selectors; keep broad exact-theme overrides scoped to `[data-color-theme^="vscode-"]`.
- Chat/task-thread imported-theme bubbles should preserve old/native neutral conversation-surface role rather than primary/accent fills. User and assistant bubbles use the same subtle raised surface/text/border treatment; input boxes use subtle bubble-like borders.
- Chat code/tool panels, Markdown borders/tables, thinking text, tool toggles/status icons, loading dots, task-result buttons, selection counters, STT recording feedback, tabs, toggles, and footer controls should use generated variables under VS Code theme scope.
- Button variants for imported themes should derive from generated semantic variables for primary/accent, secondary/neutral, info/success/warning/error, ghost, link, outline, hover, and focus. Keep CSS compact.
- Analytics OAuth account usage meter colors for imported themes should derive track/fill from generated hover/track and accent/button roles; native defaults keep fixed meter colors.
- Generator and attribution docs should describe semantic fallback/derivation: ordered upstream keys win first, transparent/invalid values are ignored, missing roles derive through theme-aware mixes/alpha/best-text helpers, then fallback to bundled light/dark defaults.
- Automation graph and YAML surfaces are exact-theme UI under imported themes: panels, nodes, connectors, arrows, handles, state colors, delete controls, focus outlines, editor backgrounds, gutters, line numbers, overlays, caret, YAML tokens, diagnostics, dots, and rails should use generated variables.
- YAML indentation rails remain visible by default; when editor is focused only the innermost active group rail switches to focus role.

Resolved contrast contracts (2026-08-28):
- Imported Automation graph/YAML theming emits generated semantic roles `automationNodeBorder`, `automationEdge`, and `yamlIndentRail`, each derived against its actual drawing surface. The generator enforces minimum contrast ratios of `1.5` for node stroke versus node fill, `3.0` for connectors versus graph surface, and `1.5` for YAML rails versus code canvas, using a theme-aware foreground fallback when upstream values collapse into a surface.
- Imported graph nodes consume `--ov-automation-node-border`; live/edit edges and arrows consume `--ov-automation-edge` at full opacity. This covers low-contrast palettes such as Dark Modern, Abyss, Kimbie Dark, and Monokai Dimmed without changing selected/active state roles.
- YAML panel defaults consume `--ov-yaml-indent-rail`; imported rail CSS intentionally does not use `!important`, so the renderer's inline active-rail color can win. Default rails remain visible, and only the innermost active group rail changes to the focus color.
- The early theme bootstrap keeps these three roles in a compact runtime `r` payload and expands them to allowlisted CSS variables, retaining the base-page size budget while the full catalog retains semantic/CSS data. Generated catalog, templ output, and attribution documentation remain source-derived and must stay synchronized through their generators.
- Catalog, rendered CSS, runtime-script, and browser regressions cover the contrast and cascade invariants across imported themes.