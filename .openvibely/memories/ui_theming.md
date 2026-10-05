---
name: ui_theming
type: project
created: 2026-08-11
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: UI Theming
---

OpenVibely has application-level native palettes and imported VS Code color themes. Theme selection is UI preference state, not project configuration.

- `/themes` is the full-page/HTMX theme catalog. Native OpenVibely Dark/Light palettes precede bundled VS Code themes. Imported themes are generated offline from a pinned upstream source; runtime never fetches GitHub, VS Code, or Marketplace data. Generated output, templ output, and attribution docs stay synchronized.
- `app_settings.ui.theme` is authoritative and stores a stable theme ID. `data-theme="light|dark"` is compatibility mode; `data-color-theme` selects exact palettes. `localStorage.theme` is an early-apply/same-browser mirror; legacy light/dark values map to native themes and invalid IDs fall back safely.
- Full documents embed compact early theme state to avoid first-paint flash. HTMX fragments do not reread app settings. Theme changes update DOM/localStorage immediately and persist asynchronously through `/ui/preferences`; footer toggles switch between the most recently selected light and dark themes.
- CSS variables apply before paint. Syntax highlighting and Markdown/code colors follow the selected theme; do not load a fixed GitHub Dark stylesheet. Imported semantic/syntax colors are sanitized; transparent/invalid values are ignored and missing roles derive from theme-aware fallbacks.
- Imported editor/window background colors apply to page canvas and main content. Modals, cards, inputs, status/hover/focus, Analytics, diffs, Automation graph/YAML surfaces use semantic variables. Scope imported overrides to imported themes so native palettes are unchanged.
- Imported Chat/thread bubbles stay neutral raised conversation surfaces rather than primary/accent fills. Contrast-sensitive graph edges, node borders, indentation rails, and controls must use generated semantic roles and meet their contrast targets.
- Theme normalization is duplicated between early and interactive paths in `web/templates/layout/base.templ`; any consolidation must preserve synchronous pre-CSS startup and system-theme behavior.
