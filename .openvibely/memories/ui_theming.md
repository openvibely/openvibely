---
name: ui_theming
type: project
created: 2026-08-11
updated: 2026-09-15
source: consolidation
source_id: memory_consolidation_2026-09-15
confidence: high
title: UI Theming
---

OpenVibely has an application-level theme system with native OpenVibely palettes and imported VS Code color themes. Theme selection is UI preference state, not project configuration.

Catalog and persistence:
- `/themes` is the dedicated full-page/HTMX Themes page. Native `OpenVibely Dark` (`openvibely-dark`) and `OpenVibely Light` (`openvibely-light`) precede 19 imported first-party VS Code themes.
- VS Code themes are imported offline from `microsoft/vscode` tag `1.130.0` with `go run ./internal/themes/cmd/generate`. Includes/inheritance resolve during generation; runtime never fetches GitHub, VS Code, or Marketplace data. Generated output, templ output, and attribution docs stay synchronized.
- `data-theme="light|dark"` is compatibility mode; `data-color-theme="<stable-id>"` selects the exact palette, including high-contrast identity. The authoritative setting is `app_settings.ui.theme`, default `openvibely-dark`. `localStorage.theme` is an early-apply/same-browser mirror; legacy `light`/`dark` map to native palettes and invalid exact IDs fall back safely.
- Full documents read the DB setting and embed compact early theme state for first paint. HTMX fragments do not reread app settings. Changes, including footer toggles, update DOM/localStorage immediately and persist the stable ID asynchronously through `POST /ui/preferences`.
- Footer sun/moon toggles between the most recently selected light and dark themes. Controls resynchronize after DOM insertion. If rendering separates later, a native/bootstrap preferences payload is needed to avoid first-paint flash.
- Theme CSS variables apply before paint. Highlight.js must not load a fixed GitHub Dark stylesheet; Markdown/code colors derive from the selected theme for initial and HTMX content.
- Theme normalization/persistence is duplicated between early and interactive paths in `web/templates/layout/base.templ`; the narrow consolidation remains `#1023` and must preserve synchronous pre-CSS startup and system-theme behavior.

Native and imported styling:
- Native dark retains original surfaces such as page/content `#191E24`, sidebar/cards/modals/inputs `#1D232A`, and border `#15191E`. Native light uses canvas `#F5F5F5` and surfaces `#FAFAFA`.
- Imported themes sanitize semantic, syntax, and preview colors. Ordered upstream keys win; transparent/invalid values are ignored; missing roles derive through theme-aware mixes/alpha/best-text helpers and bundled light/dark fallbacks.
- Imported editor/window `contentBg` covers root/page canvas, drawer content, and `#main-content`. Modals, cards, borders, controls, schedules, task boards, diffs, review states, buttons, focus/hover/selection, inputs, Analytics meters, and Automation graph/YAML surfaces consume generated semantic variables. Broad imported overrides are scoped to `[data-color-theme^="vscode-"]` so native defaults are unaffected.
- Imported Chat/task-thread bubbles remain neutral raised conversation surfaces, not primary/accent fills. Code/tool panels, Markdown tables/borders, thinking text, toggles, status icons, loading dots, tabs, footer controls, graph nodes/connectors, YAML diagnostics/gutters/rails, and overlays use generated roles. YAML rails stay visible by default and focus changes only the innermost active group rail.

Contrast and action colors:
- Generated `automationNodeBorder`, `automationEdge`, and `yamlIndentRail` meet minimum contrast targets of `1.5`, `3.0`, and `1.5` against their actual surfaces. Graph strokes/edges use full-opacity semantic variables; inline active-rail colors may win over imported rail CSS.
- `--ov-primary-action-color` is shared by `.chat-send-button` and `.task-state-running`. Native dark normal is `#7480ff`; imported themes derive it from the selected primary channel. Native generic/primary rules are scoped to native IDs and shared Send rules retain hover behavior.
- Automation and Task Edit controls share `.ov-secondary-action` and historical outlined `btn-outline` construction. Native dark hover is light gray `#c9d0db` with dark text; native light hover is neutral `#CCCCCC` with dark text. Rules support compatibility `data-theme` before JavaScript adds `data-color-theme` while excluding imported themes. A prior pink-hover regression came from changing Automation Edit to `btn-secondary` and broad dark-theme overrides.
- Native light Alert inspection inline code uses `--ov-l-surface-active` and `--ov-l-text-strong`; fenced code and shared Chat/task-thread styling are unaffected.
