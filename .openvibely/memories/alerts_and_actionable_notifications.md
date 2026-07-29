---
name: alerts_and_actionable_notifications
type: project
created: 2026-07-15
updated: 2026-07-27
source: consolidation
source_id: memory_consolidation_2026_07_20
confidence: high
title: Alerts and Actionable Notifications
---

OpenVibely supports backward-compatible operational alerts and generic approval-based actionable notifications.

Durable model and migration facts:
- Alerts are project-owned. List/count, ID-based reads, read-state, delete, decision, claim, linkage, and processing operations enforce project ownership server-side.
- Existing operational alerts retain their persisted project IDs. Migration does not infer ownership from the active UI project or introduce implicit global visibility; legacy rows are backfilled as `scope=project`, `decision=not_required`, and `processing=not_applicable`.
- Actionable notification decision state is separate from read/unread and automation processing state. Notifications carry project/scope, type, title/message/body, source and source-task identity, timestamps, structured metadata, optional project-scoped idempotency key, lease claimant/time, processing/failure state, and linked implementation task.
- Deleting a task intentionally retains associated alerts as historical records while nulling their `task_id`, `source_task_id`, and `execution_id` references. Those alerts must be deleted separately if no longer wanted.
- Human approval authorizes downstream task creation only. It does not authorize merge, release, deployment, or other higher-risk actions.

Authorization, concurrency, and runtime facts:
- Scheduled execution context uses persisted `task.ProjectID`, not process-global or current UI project state. A supplied `project_id` is only an equality assertion and is rejected when it differs from the caller's authorized project.
- `create_alert` preserves the legacy operational-alert contract: title is required, type defaults to `custom`, and message, severity, operational type, and same-project `task_id` remain optional. Operational alerts use `decision=not_required` and `processing=not_applicable` rather than entering approval workflow.
- `create_notification` creates a pending project-scoped actionable notification, binds source-task identity from the persisted caller task, accepts structured metadata, and supports project-scoped idempotency keys.
- Initial tasks, scheduled tasks, ordinary task-thread follow-ups, ordinary web/API Chat, Slack, Telegram, Discord, and Email expose `create_notification` when the selected provider/auth path supports runtime tools. Dispatch derives project and trusted source-task identity from the persisted execution context.
- Ordinary Chat exposes the full notification lifecycle in Orchestrate mode and only read operations such as `list_alerts` and `get_alert` in Plan mode. Runtime-tool-incapable provider/auth paths receive no notification tools and have no bracket-marker fallback.
- The structured runtime surface covers stable filtered/paginated listing, detail, atomic claim, atomic implementation-task creation/linkage, explicit linkage, processing completion/failure, and claim release/retry.
- Claims are lease-based and atomic. Stale leases and failed attempts can be recovered. `create_alert_implementation_task` requires a non-empty title and prompt and a notification currently claimed by the persisted caller task; out-of-range priority defaults to 2. In one SQLite `BEGIN IMMEDIATE` transaction it returns the already-linked task or creates a same-project `backlog`/`pending` task with `created_via=system_agent`, links it, marks processing `implementation_task_linked`, and clears claim expiry. It does not start the task, mark processing complete, merge, release, or deploy.
- Slack, Telegram, and Discord first-turn channel runtimes are constructed only after the channel Chat task is persisted, so channel lifecycle handlers receive trusted caller identity. Email uses the generic executor with its persisted Chat task.
- All alert lifecycle mutations publish the existing project-scoped alert invalidation event, including claim, release, explicit linkage, atomic task creation, completion, failure, read, and delete operations.
- Custom Automation graphs support Native Alert approval handoffs through the existing Alert runtime: a connected Agent task receives deterministic `create_notification` instructions from its immutable published Automation version, the Alert is created only when the task runs, and pending/approved/rejected state is projected onto the exact configured notification, human-gate, and outcome nodes. Publication creates no Alert and approval still grants no merge, release, or deploy authority.
- Automation-bound idempotent notification retries may reuse an Alert only when the same persisted Automation source already owns the creation transition; a same-project, same-key Alert created outside that Automation is rejected rather than adopted.

Product surfaces:
- The Alerts page supports inspection, approve/reject controls for pending notifications, decision and processing badges, claimant/failure details, linked-task navigation, project context, and project-filtered live refresh. Deleting one alert or all alerts for the selected project physically removes those rows and refreshes the list and unread badge; marking read only changes `is_read`, and dismissing only changes decision state.
- The Alerts page currently fetches only the newest 100 project alerts, while search is client-side and decision-state filters and pagination are absent. Older pending approvals can therefore become unreachable behind newer operational alerts; the durable product direction is server-side filtering/pagination so pending human decisions remain reachable.
- The bundled `openvibely_native_autonomous_sdlc_bootstrap` skill provides an OpenVibely-native alternative to the GitHub-backed workflow. Suggestion producers use `create_notification`; scheduled inbox tasks inspect approved notifications, claim them, and create one atomically linked implementation task.
- The model, migration, authorization boundaries, tool contracts, lease recovery, and schedule configuration are documented in `docs/openvibely-native-autonomous-sdlc-user-guide.md`.
