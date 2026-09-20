---
name: alerts_and_actionable_notifications
type: project
created: 2026-07-15
updated: 2026-09-19
source: after_complete
source_id: 49188575891ddb78930b68fd97cb9ca5:64c95c2989687faf
confidence: high
title: Alerts and Actionable Notifications
---

OpenVibely supports backward-compatible operational alerts and generic approval-based actionable notifications.

Model and authorization:
- Alerts are project-owned. List/count, ID reads, read state, deletion, decisions, claims, linkage, and processing enforce ownership server-side. Legacy operational rows retain project IDs and migrate as project-scoped, `decision=not_required`, `processing=not_applicable`; ownership is never inferred from the active UI project.
- Actionable notification decision state is separate from read/unread and Automation processing. Records include project/scope, content/source identity, timestamps, structured metadata, idempotency, claim lease, processing state, and linked implementation task. Deleting a linked task clears its ID but preserves alert history and durable `implementation_task_was_linked`; completion alone is not assignment evidence.
- Human approval authorizes only creation/start of the configured downstream implementation task. It never authorizes merge, release, deployment, destructive remediation, credential changes, or arbitrary execution.
- Scheduled runtimes derive scope from persisted `task.ProjectID`. Ordinary scheduled `list_alerts` hides `project_id`/`read` and cannot be redirected; ordinary Chat/task runtimes retain optional project equality/read filters. Native Approved Inbox behavior is directed by its maintained prompt and generic tool descriptions.
- Alert mutations use immediate project-scoped transactions and publish project-scoped invalidations using known identity, so deletion cannot suppress the event.

Runtime contracts:
- `create_alert` preserves operational behavior: required title, default `custom` type, optional message/severity/operational type/same-project task, and no approval/processing state.
- `create_notification` creates a pending project-scoped actionable record, binds trusted source-task identity from persisted caller context, accepts structured metadata, and supports backend/direct-caller idempotency. Model-facing input does not expose `idempotency_key`. Its schema and handler contract are character-oriented for title/type/message/body/source limits, so runtime validation must count Unicode characters rather than UTF-8 bytes while preserving ASCII boundary behavior (`#1233`).
- Initial, scheduled, task-thread, web/API Chat, Slack, Telegram, Discord, and Email runtimes expose notification creation only when provider/auth paths support runtime tools. Chat mutation tools are Orchestrate-only; Plan remains read-only; no bracket fallback exists.
- Listing, detail, atomic lease claims, implementation-task creation/linkage, completion/failure, claim release/retry, and pending decisions are supported. `lease_seconds` is bounded to `1..86400`; omission uses repository default. Only approved notifications remain claimable by Native inbox flow. Terminal processing notes for `complete_alert_processing` and `fail_alert_processing` advertise character-oriented `maxLength: 2000`; validation must count Unicode characters rather than UTF-8 bytes while preserving ASCII boundary behavior. PR `#1278` proposes the fix/regressions for `#1272`; verify merge/target-branch state before treating it as shipped.
- `create_alert_implementation_task` requires nonempty title/prompt and a notification claimed by the persisted caller task. It returns an existing linked task or transactionally creates/links a same-project Backlog/Pending system-agent task, marks processing linked, and clears the claim. It does not start, complete, merge, release, or deploy. Native inbox scans all pages/read states, creates/links once, executes the exact linked ID, and marks complete only after execution starts.
- Failed creation/linkage/execution records processing failure; release is valid only before linkage. Repeated task-row creation in shared transactional paths remains `#835`. Issue `#1215` is implemented by a shared owned-notification mutation preflight path for `claim_alert`, `create_alert_implementation_task`, `link_alert_implementation_task`, `complete_alert_processing`, `fail_alert_processing`, and `release_alert_claim`: project assertion, persisted caller task, alert service availability, and Automation inbox ownership are centralized while tool-specific validations and response shapes stay local. Approved Inbox discovery must filter approved, unclaimed, and unlinked state while omitting unrelated lifecycle fields; failed-notification retry uses a separate recovery query.
- Automation-scoped `list_alerts` and `list_existing_automation_notifications` summary pages use mailbox ownership rows as the driving set when inbox bindings are present, then join alerts for filters, ordering, and pagination. Empty/invalid bindings fail closed, while unscoped project/browser listing stays on the generic project-scoped path (`#1247`). Their schemas require `limit >= 1`, but explicit `limit: 0` is currently coerced to the default page size instead of rejected (`#1276`).
- Custom Automation graphs reuse this approval lifecycle; topology and ownership belong in `automation_graphs.md`.

Alerts UI:
- Alerts supports inspection, approve/reject, decision/processing badges, claimant/failure details, linked-task navigation, project context, project-filtered live refresh, search/sort/filter, and contextual bulk actions. Pending summaries are scannable; detail carries full evidence/metadata/copy.
- Filter state persists through pagination/mutation refreshes. Processing filters and the dedicated Implementation task filter are conjunctive; linkage means a task was ever assigned, even if its row is later deleted. Backend compatibility may accept legacy linkage filter values not shown in the UI.
- Browser mutations refresh authoritative inner content while preserving search/reading position/unread state. Live results replace only list/pagination markup, update header/unread count out of band, preserve toolbar focus/popovers, and rerun shared card selection. New alerts are never auto-focused; deletion moves focus to the next/previous visible control.
- Inspection reuses shared Markdown/sanitization/code-copy/link handling. Raw bodies use Base64 UTF-8 immediately before clipboard writes to preserve LF, CRLF, and bare CR; parser failure renders escaped inert text. Sensitive metadata and secrets never appear in compact card attributes.
- Direct task-linked cards are semantic, keyboard-reachable links to task history with repeat-safe Enter/Space activation; nested controls stop propagation. Notification content begins with a short nontechnical `## Summary`, followed by evidence and implementation detail.
- Maintained Automation prompts are point-in-time snapshots. Native SDLC template revision is `11`; saved Automations require explicit template update and existing Backlog tasks are not retroactively started.
