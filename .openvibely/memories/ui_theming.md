---
name: ui_theming
type: project
created: 2026-08-11
updated: 2026-09-03
source: consolidation
source_id: memory_consolidation_2026-09-03
confidence: high
title: UI Theming
---

OpenVibely has an application-level theme system with native OpenVibely palettes and imported VS Code color themes. Theme selection is UI preference state, not project configuration.

Catalog and persistence:
- `/themes` is the dedicated full-page/HTMX Themes page. Native `OpenVibely Dark` (`openvibely-dark`) and `OpenVibely Light` (`openvibely-light`) precede imported themes and restore the original OpenVibely palettes. The catalog contains 19 first-party VS Code themes plus the two native defaults.
- VS Code themes are imported offline from `microsoft/vscode` tag `1.130.0` with `go run ./internal/themes/cmd/generate`. Includes/inheritance resolve during generation; runtime does not fetch GitHub, VS Code, or Marketplace data. Generated output, templ output, and attribution docs are source-derived and must stay synchronized.
- `data-theme="light|dark"` is compatibility mode; `data-color-theme="<stable-id>"` selects the exact palette, including high-contrast identity. The authoritative setting is `app_settings.ui.theme`, defaulting to `openvibely-dark`. `localStorage.theme` is only an early-apply/same-browser mirror; legacy `light`/`dark` values map to native palettes and invalid exact IDs fall back safely.
- Full-document server rendering reads the DB setting and embeds compact early theme state, so first paint does not depend on a preference GET. Ordinary HTMX fragments do not reread app settings. User changes, including footer toggles, update DOM/localStorage immediately and persist the stable ID asynchronously through `POST /ui/preferences`.
- Footer sun/moon toggles between the most recently selected light and dark themes. Light uses a light track and dark uses a dark track; controls must resynchronize after DOM insertion. If rendering later separates from the server, a native/bootstrap preferences payload is required to avoid first-paint flash.
- Theme CSS variables apply before paint. The bootstrap carries only the compact runtime catalog needed for early application; the full catalog remains server-side. Highlight.js must not load a fixed GitHub Dark stylesheet; Markdown/code colors derive from the selected theme for initial and HTMX content.

Native palettes and imported styling:
- Native dark retains original surfaces such as page/content `#191E24`, sidebar/cards/modals/inputs `#1D232A`, and border `#15191E`; native light uses canvas `#F5F5F5` and surfaces `#FAFAFA`.
- Imported themes sanitize generated semantic, syntax, and preview colors. Ordered upstream keys win; transparent/invalid values are ignored; missing roles derive through theme-aware mixes/alpha/best-text helpers and bundled light/dark fallbacks.
- Imported backgrounds use exact editor/window `contentBg` on root/page canvas, `.drawer-content`, and `#main-content`. Modals, cards, borders, dividers, controls, schedule/task-board surfaces, diffs, review states, buttons, focus/hover/selection, inputs, Analytics meters, and Automation graph/YAML surfaces consume generated semantic variables. Broad imported overrides are scoped to `[data-color-theme^="vscode-"]` so native defaults are not caught.
- Imported Chat/task-thread bubbles remain neutral raised conversation surfaces rather than primary/accent fills. Code/tool panels, Markdown tables/borders, thinking text, toggles, status icons, loading dots, tabs, footer controls, graph nodes/connectors, YAML diagnostics, gutters, rails, and overlays use generated roles.
- Task dropzones preserve dashed geometry and active drag feedback. YAML rails remain visible by default; focus changes only the innermost active group rail. YAML editor styling is shared by editable/read-only Automation panels and does not wrap long lines.

Contrast and shared action colors:
- Generated graph/YAML roles `automationNodeBorder`, `automationEdge`, and `yamlIndentRail` are derived against their actual surfaces with minimum contrast targets of `1.5`, `3.0`, and `1.5` respectively. Graph strokes/edges use full-opacity semantic variables; inline active-rail colors can win because imported rail CSS is not `!important`.
- `--ov-primary-action-color` is the shared normal color for `.chat-send-button` and `.task-state-running`. Native dark normal is `#7480ff` and native light has its corresponding native value; imported themes derive the token from their selected primary channel. Native generic/primary rules are scoped to native IDs so imported primary and hover behavior remains authoritative, and shared Send rules do not suppress hover.
- Native light Alert inspection inline code uses `--ov-l-surface-active` and `--ov-l-text-strong` under `[data-theme="light"] [data-alert-markdown] :not(pre) > code`; fenced code and shared Chat/task-thread styling are unaffected. Browser coverage protects at least 4.5:1 computed contrast after detail hydration.
