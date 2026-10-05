---
name: realtime_and_frontend_patterns
type: project
created: 2026-05-09
updated: 2026-09-27
source: update_memory
source_id: 92cdae0607f5fd5e6f65f1528d70e410:5484e45af90ec8c2
confidence: high
title: Realtime and Frontend Patterns
---

OpenVibely uses server-rendered HTMX/templ UI with shared SSE invalidations. Durable rule: SSE announces change; server-rendered fragments remain authoritative state.

- `/events/live` multiplexes project/task-scoped invalidations; clients retarget on navigation/project changes. `/events/chat/:exec_id` streams tokens, with SQLite as reconnect source and a bounded in-memory hub for hot deltas. Terminal events follow durable writes; cancellation, slow consumers, and disconnects must clean subscribers.
- Chat/task-thread streaming batches DOM updates and reconciles terminal state from persisted status/output. Start/input events must not replace a live stream with stale reloads. File-change events are invalidations; browsers fetch authoritative diff fragments. Changes stays lazy unless selected.
- Rendered user/provider content is bounded and sanitized. Markdown/code hydration is shared, asynchronous for large content, and falls back to escaped plaintext on failure. Raw HTML/unsafe URLs remain inert. Task-thread preview bounds and lazy exact-execution ownership checks are in `chat_thread_system.md`.
- Shared composer, navigation, selectors, cards, and dialogs should preserve accessible focus, keyboard behavior, scroll/history state, and clean up listeners/requests on HTMX replacement. Avoid focus theft when a dialog, menu, another control, or intentional history restoration owns interaction. Use native browser input for behavioral evidence; synthetic events are supplemental.
- Project selection is URL-authoritative per browser tab; unscoped HTMX requests inherit that selected project. The user expects desktop project tabs to be implicitly pinned: opening a project should add it to the persisted tab set, tab order should persist, and closing a tab should remove it from the saved set, so opened tabs survive restarts. Per-project navigation state is maintained in these tabs, while each project opened for the first time starts at Chat. Desktop app-drawn titlebar controls/tabs are intentional. The user expects macOS controls to look convincingly native; current SVG approximation and native hover behavior remain unverified. Wails/WebKit startup URLs should target final pages directly, not redirects.
- Task selectors are bounded, project-scoped, and status-aware. Search must constrain results; a current task cannot displace eligible running results outside its natural section. Server-rendered selector rows refresh from shared task invalidations. Preserve return origins where navigation depends on them.
- Cards and collection browsers share response/pagination conventions while keeping page-specific loading and filtering. Lazy details must be authorized. Sensitive metadata and secrets must not appear in compact attributes. Destructive actions use accessible confirmation, duplicate-submit protection, authoritative refresh, and sensible focus restoration.
- Kanban drag/drop supports keyboard and pointer input, optimistic placement with authoritative reconciliation/rollback, generation/request fencing, and preserved focus/selection. Merge failure returns retryable refreshed state. Native browser interaction is required for evidence.
- Task Detail keeps the thread mounted when switching to Changes; preserve draft, attachments, scroll, and streaming. Details/schedules/chaining/lifecycle belong in a resizable inspector. In the desktop app, the details panel starts below the titlebar/menu divider and must not overlap the task thread; preserve this layout in both windowed and fullscreen modes. The unsaved Add Task workspace begins with a ready composer and creates no task until first accepted send.
- Analytics uses six URL-persisted views: Overview, Outcomes, Agents & Models, Automations, Learning, Usage. Tabs/filter state restore through browser history; non-selected views load lazily and requests abort when leaving. KPI/funnel cards and charts are display-only, not drill-down links; explicit tabs, filters, tables, and evidence links are intended interactions. See `usage_analytics.md` for data contracts.
- Pulse distinguishes running/active waiting work from blocked dependencies. Stop is available only for eligible current-project active tasks; blocked tasks are read-only and bounded for display with aggregate counts kept separately.
- Automation YAML panels share syntax/indentation behavior; theme-specific rules belong in `ui_theming.md`. Schedule and task actions should reuse project-owned routes and accessible confirmation patterns.
