---
name: alerts_and_actionable_notifications
type: project
created: 2026-07-15
updated: 2026-09-03
source: consolidation
source_id: memory_consolidation_2026-09-03
confidence: high
title: Alerts and Actionable Notifications
---

OpenVibely supports backward-compatible operational alerts and generic approval-based actionable notifications.

Model and authorization:
- Alerts are project-owned. List/count, ID reads, read state, deletion, decisions, claims, linkage, and processing enforce project ownership server-side. Legacy operational rows retain persisted project IDs and migrate as project-scoped, `decision=not_required`, `processing=not_applicable`; ownership is never inferred from the active UI project.
- Migration 135 indexes project-scoped stable list order as `alerts(project_id, created_at DESC, id DESC)` and removes the redundant single-column project index.
- Actionable notification decision state is separate from read/unread and Automation processing. Records include project/scope, type, content, source/task identity, timestamps, structured metadata, optional backend idempotency key, claimant/lease expiry, processing/failure state, and linked implementation task. Deleting a task keeps alerts as historical rows while nulling task/execution references.
- Human approval authorizes only creation/start of the configured downstream implementation task. It never authorizes merge, release, deployment, destructive remediation, credential changes, or arbitrary execution.
- Scheduled runtimes derive scope from persisted `task.ProjectID`; scheduled `list_alerts` hides `project_id`/`read` from the model and cannot be redirected, while ordinary Chat/task runtimes retain optional project equality and read filters.
- Alert lifecycle mutations publish project-scoped invalidation events and use shared immediate-transaction scaffolding; mutation-specific validation/SQL/projections stay local. Decision publication uses the known project/alert identity so immediate deletion cannot suppress the invalidation.

Runtime contracts:
- `create_alert` preserves the operational contract: required title, default `custom` type, optional message/severity/operational type/same-project task, and no approval/processing state.
- `create_notification` creates a pending project-scoped actionable record, binds trusted source-task identity from persisted caller context, accepts structured metadata, and supports backend/direct-caller idempotency. Model-facing input does not expose `idempotency_key`.
- Initial, scheduled, task-thread, web/API Chat, Slack, Telegram, Discord, and Email runtimes expose notification creation only when the provider/auth path supports runtime tools. Chat reads/mutations are Orchestrate-only; Plan remains read-only; no bracket fallback exists.
- Listing, detail, atomic lease claims, implementation-task creation/linkage, completion/failure, claim release/retry, and explicit pending decisions are supported. `lease_seconds` is bounded to `1..86400`; omission uses repository default. `decide_alert` records approved/rejected/dismissed user decisions; only approved notifications remain claimable by Native inbox flow.
- `create_alert_implementation_task` requires nonempty title/prompt and a notification claimed by the persisted caller task. It returns an existing linked task or transactionally creates/links a same-project Backlog/Pending system-agent task, marks processing linked, and clears the claim; it does not start, complete, merge, release, or deploy. Native inboxes scan every page/read state before mutation, create/link exactly one task, execute the exact linked ID, and mark complete only after execution starts.
- Failed Native creation/linkage/execution records processing failure; release is valid only before linkage. Open gaps are repeated task-row creation in the shared transactional path (`#835`), completion from merely claimed state without a linked task (`#352`), and Automation-bound non-Native-Inbox tasks receiving unscoped project-wide notification lists when no inbox binding exists (`#612`).
- Custom Automation graphs reuse this approval lifecycle; topology and ownership belong in `automation_graphs.md`.

Alerts UI:
- Alerts supports inspection, approve/reject controls, decision/processing badges, claimant/failure details, linked-task navigation, project context, and project-filtered live refresh. Pending summaries should be scannable without expansion; detail provides full evidence/metadata/copy.
- Runtime lists and browser `/alerts` use compact summaries without body/metadata; project-scoped detail hydration loads one full record. Browser mutations replace only inner `#alerts-content` inside the mounted `#alerts-container`, preserving search/reading position and unread state. Shared refresh behavior should cover decisions, mark-read, delete-one/last, and delete-all without changing mutation semantics.
- Inspection reuses shared Chat/task-thread Markdown parsing, sanitization, code-copy, and link behavior. Raw body is carried as Base64 UTF-8 and decoded immediately before clipboard write, preserving LF, CRLF, and bare-CR bytes; empty bodies have no copy control and parser failures render escaped inert text. Native light inline code uses dedicated contrast variables; fenced code remains unchanged.
- New alerts are never auto-focused. Deletion moves focus with `preventScroll` to the next/previous visible delete control. Scroll restoration uses a surviving intersecting row after search/geometry settle and suppresses HTMX show-to-top. `#system-update-card` is API-authoritative and `hx-preserve`d during Alerts swaps.
- Every saved outbound target and alert action preserves displayed project scope. Details and settings do not put secrets or sensitive metadata in compact card attributes. Notification content begins with a short nontechnical `## Summary`, followed by evidence and implementation detail.
- Open UI gaps include decision/claim timing visibility (`#847`), source-task navigation when `TaskID` is empty but `SourceTaskID` exists (`#870`), and browser Dismiss for pending actionable notifications (`#944`).
- Maintained Automation prompts are point-in-time snapshots; existing saved Native/GitHub inbox Automations need explicit update/edit/save or recreation after corrected defaults, and existing Backlog tasks are not retroactively started. Model/migration/lease documentation is in `docs/openvibely-native-autonomous-sdlc-user-guide.md`.
