---
name: alerts_and_actionable_notifications
type: project
created: 2026-07-15
updated: 2026-08-19
source: consolidation
source_id: memory_consolidation_2026_08_19
confidence: high
title: Alerts and Actionable Notifications
---

OpenVibely supports backward-compatible operational alerts and generic approval-based actionable notifications.

Durable model and authorization facts:
- Alerts are project-owned. List/count, ID-based reads, read-state, delete, decision, claim, linkage, and processing operations enforce project ownership server-side.
- Existing operational alerts retain persisted project IDs. Migration does not infer ownership from active UI project or create implicit global visibility; legacy rows are backfilled as `scope=project`, `decision=not_required`, and `processing=not_applicable`.
- Actionable notification decision state is separate from read/unread and automation processing state. Notifications carry project/scope, type, content, source/task identity, timestamps, structured metadata, optional backend idempotency key, lease claimant/time, processing/failure state, and linked implementation task.
- Deleting a task intentionally retains associated alerts as historical records while nulling task/execution references; those alerts must be deleted separately if no longer wanted.
- Migration 135 indexes project-scoped stable list order as `alerts(project_id, created_at DESC, id DESC)` and removes the redundant single-column project index.
- Human approval authorizes creating and starting the configured downstream implementation task only. It does not authorize merge, release, deployment, destructive remediation, credential changes, or other higher-risk actions.
- Scheduled execution context uses persisted `task.ProjectID`. Scheduled Task runtimes specialize `list_alerts` so `project_id` and `read` are not model-visible and strip injected defaults; these scans cannot be redirected and always include read/unread states.
- Ordinary Chat and non-scheduled Task runtimes retain optional `project_id` equality assertions and `read` filtering.

Runtime tool contracts:
- `create_alert` preserves the legacy operational-alert contract: title required; type defaults to `custom`; message/severity/operational type/same-project `task_id` optional; operational alerts use `decision=not_required` and `processing=not_applicable`.
- `create_notification` creates a pending project-scoped actionable notification, binds trusted source-task identity from persisted caller task, accepts structured metadata, and keeps optional backend/direct-caller idempotency support. The model-facing schema ignores hidden `idempotency_key` input.
- Native SDLC duplicate prevention is existing-work-first, not idempotency-key based. Finders inspect existing notifications and hydrate likely matches before creating at most one new notification.
- Initial tasks, scheduled tasks, task-thread follow-ups, web/API Chat, Slack, Telegram, Discord, and Email expose `create_notification` when the selected provider/auth path supports runtime tools. Dispatch derives project/source identity from persisted execution context.
- Ordinary Chat exposes full notification lifecycle in Orchestrate mode and only read operations in Plan mode. Runtime-tool-incapable providers receive no notification tools and no bracket-marker fallback.
- The structured runtime surface covers filtered/paginated listing, detail, atomic claim, implementation-task creation/linkage, explicit linkage, completion/failure, and claim release/retry.
- Claims are lease-based and atomic. Explicit `lease_seconds` is validated as `1..86400`; omitted uses repository default.
- `create_alert_implementation_task` requires a non-empty title/prompt and a notification currently claimed by the persisted caller task. It transactionally returns an already-linked task or creates a same-project Backlog/Pending system-agent task, links it, marks processing `implementation_task_linked`, and clears claim expiry. It does not start the task, mark processing complete, merge, release, or deploy.
- Maintained Native inbox execution lists approved notifications without a `read` filter, atomically creates/links one Backlog implementation task, executes the exact returned Task ID, and marks processing complete only after execution starts.
- Because linkage removes rows from the unlinked result set, maintained and custom prompts must collect every stable paginated page before any claim/linkage/processing mutation; advancing offsets after mutation can skip eligible notifications.
- Task-thread runtimes, including scheduled inbox runs, expose project-scoped `execute_tasks` required by Native inbox flow.
- Native Automation notification eligibility is Automation-owned: durable ownership is effectively `project_id + automation_id + alert_id`, and a current active same-project Native inbox for that Automation supplies live processing authority.
- Generated/compiled Native inbox guidance requires calls without `project_id` or `read`, forbids project-ID discovery/reuse, executes the exact linked Backlog task, and completes processing only after execution starts. Scheduled runtime enforcement removes those fields from schema and discards injected values.
- If Native creation, linkage, or execution fails, the inbox records failed processing rather than reporting completion; claim release remains valid only before any task is linked.
- Open gap `#352`: `complete_alert_processing` can mark an approved notification completed from a merely claimed state without requiring a linked implementation task.
- Open gap `#612`: Automation-bound non-Native-Inbox tasks can list project-wide notification summaries because zero Native Inbox bindings are treated as unscoped project list; fix should fail closed or return no rows.
- Slack, Telegram, Discord, and Email first-turn channel runtimes are constructed only after the channel Chat task is persisted, so channel lifecycle handlers receive trusted caller identity.
- Alert lifecycle mutations publish project-scoped alert invalidation events.
- Alert approval/claim/release/linkage/task creation/processing/idempotent creation/Automation rebind mutations use shared immediate-transaction scaffolding while mutation-specific SQL/validation/projections stay local.
- `AlertService.SetDecision` publishes invalidation from known project/alert IDs after successful decision update without post-update hydration, preserving delivery even if the alert is deleted immediately after mutation.
- Custom Automation graphs reuse the canonical Alert approval lifecycle; Automation-specific topology and authorization contracts live in `automation_graphs.md`.

Alerts UI contracts:
- The Alerts page supports inspection, approve/reject controls, decision/processing badges, claimant/failure details, linked-task navigation, project context, and project-filtered live refresh.
- The shared inspect surface includes an accessible copy-to-clipboard action when `Body` is non-empty. It copies exact body text only, without title/message/IDs/states/metadata/project/diagnostics.
- Inspect body/metadata and copy control share one relative content block with compact overlaid icon, reserved right padding, and no separate copy row. Copy feedback remains local with no redundant toasts.
- Deleting one alert or all alerts for the selected project physically removes rows and refreshes list/unread badge. Marking read only changes `is_read`; dismissing only changes decision.
- Alerts page mutations and live additions preserve reading position by keeping `#alerts-container` mounted and replacing only inner `#alerts-content` via HTMX `hx-select`.
- Scroll restoration uses a viewport-intersecting surviving alert row, scopes settle state to the swapped fragment, reapplies card search before geometry restoration, and suppresses HTMX show-to-top.
- Newly inserted alerts/notifications are never auto-focused. Only deletion transfers focus, using `preventScroll`, to the next/previous visible delete control.
- `#system-update-card` is independently API-authoritative and uses `hx-preserve` so Alerts swaps retain update-card state. Its poll forwards snapshots to the shared update snapshot handler.
- Browser Alerts mutation response tails should share one private refresh helper for approve/reject/dismiss, mark-read, mark-all-read, delete-one/delete-last, and delete-all while preserving mutation-specific behavior.
- Alerts page currently fetches only the newest 100 project alerts while search is client-side and decision-state filters/pagination are absent. Older pending approvals can become unreachable behind newer operational alerts; product direction is server-side filtering/pagination.
- Notification bodies should start with a short nontechnical `## Summary` section followed by technical evidence and implementation detail.
- Alerts UI should make pending approval summaries scanable without requiring expansion; detail expansion remains useful for full evidence/metadata/copy.
- Runtime alert listing should use compact alert summaries excluding `body` and `metadata_json`, preserving filters/project isolation/Automation inbox scoping/ordering/pagination and `get_alert` detail hydration.
- Browser Alerts list/detail hydration should keep initial `/alerts` and mutation refreshes on compact summaries; `GET /alerts/:id/details` loads one project-scoped full detail with exact-body copy.
- Maintained Automation prompts are point-in-time snapshots. Existing saved Native/GitHub inbox Automations must be explicitly edited/saved or recreated after corrected defaults; already-created Backlog tasks are not retroactively started.
- Notification model, migration, authorization boundaries, tool contracts, lease recovery, and schedule configuration are documented in `docs/openvibely-native-autonomous-sdlc-user-guide.md`.
