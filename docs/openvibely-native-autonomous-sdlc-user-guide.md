# OpenVibely-Native Autonomous SDLC User Guide

OpenVibely can use project-scoped actionable notifications as a review mailbox. Agents and scheduled tasks submit suggestions to Alerts, a human approves or rejects them, and a project-scoped scheduled inbox creates at most one linked implementation task for each approved notification. GitHub-backed workflows remain available as an alternative.

## Existing Alerts Audit and Migration

Before this workflow, alerts already had a required `project_id`; operational task-failure and follow-up alerts were created from each task's persisted project, list/count queries were project-filtered, the Alerts page selected a project, and `alert_created` events carried a project ID for live badge/page invalidation. Runtime create/list actions also received a request-scoped project. The unsafe gap was that ID-based detail, mark-read, and delete operations queried only by alert ID, so a known foreign-project ID could bypass list isolation.

Migration `112_alert_notifications.sql` preserves every existing row and its persisted `project_id`. Existing operational rows become `scope=project`, copy `message` into `body`, use `source=operational`, and receive `decision_state=not_required` plus `processing_state=not_applicable`. No ownership is inferred from the active browser project, no row is reassigned to a default project, and no nullable/global alert scope is introduced. OpenVibely has no ordinary cross-project/global Alerts view; the only unscoped read is an explicitly named internal administrative method and is not exposed to HTTP or runtime tools.

## Notification Model

Actionable notifications use the existing `alerts` table with additional structured fields:

- Identity and scope: ID, required project ID, project scope, type, severity, title, message, detailed body, source, source task, optional related task/execution, metadata, idempotency key, and timestamps.
- Human decision: `pending`, `approved`, `rejected`, or `dismissed`. Operational alerts use `not_required`. Read/unread remains separate.
- Automation processing: `unclaimed`, `claimed`, `implementation_task_linked`, `completed`, or `failed`. Operational alerts use `not_applicable`.
- Recovery/linkage: claimant task ID, claim/expiry times, processing error, and linked implementation task ID.

Approval permits implementation-task creation only. It does not approve merge, release, deployment, history rewriting, credential use, or other higher-risk actions.

## Alerts Page

Select a project and open **Alerts**. Each card identifies project scope and operational/actionable state. Expand **Inspect notification** to review the full body and structured metadata. Pending notifications expose **Approve** and **Reject**. Cards also show claim, processing, error, and linked-task state. Existing read, mark-all-read, delete, task-failure, and follow-up-alert behavior remains available.

All list, detail, decision, read, delete, claim, and linkage operations enforce the selected or executing project in SQL. Supplying another `project_id` to a runtime tool is rejected rather than changing context.

## Runtime Tools

The generic runtime surface includes:

- `create_alert`: preserves the existing operational-alert contract (`title` required; optional message, severity, operational type, and task ID).
- `create_notification`: creates a pending actionable notification; project and source task are bound server-side. Optional `idempotency_key` is unique per project.
- `list_alerts`: returns structured JSON in `created_at DESC, id DESC` order with `limit` (1-100), `offset`, and filters for project assertion, decision state, processing state, type, source, read state, and implementation-task linkage.
- `get_alert`: returns one structured same-project notification for inspection.
- `claim_alert`: atomically leases an approved notification to the executing persisted task. Unlinked stale claims and failed attempts can be retried.
- `create_alert_implementation_task`: atomically creates one Backlog task and links it to the caller's claim. Repeated calls return the existing linked task.
- `link_alert_implementation_task`: links an existing same-project task when a workflow intentionally created it separately.
- `complete_alert_processing` and `fail_alert_processing`: record terminal processing state and diagnostics.
- `release_alert_claim`: releases only the current task's unlinked claim for immediate retry.

Scheduled and initial task runtimes derive project and claimant from the persisted task, never from a process-global or currently displayed UI project. Claims default to a 30-minute lease and are bounded to 24 hours. A failed unlinked notification is claimable again; an expired unlinked claim is recoverable by another scheduled scan.

## Configure the Native Loop

Run a visible bootstrap task or task-thread turn with the bundled `openvibely_native_autonomous_sdlc_bootstrap` skill and ask:

```text
Use the OpenVibely Native Autonomous SDLC Bootstrap skill. Create a scheduled suggestion producer and a project-scoped approved-notification inbox for this project. Suggestions must use stable idempotency keys. The inbox must inspect, claim, and atomically create linked Backlog implementation tasks. Do not authorize merge, release, or deployment.
```

A typical setup schedules the suggestion producer daily and the approved-notification inbox hourly. Recurring loop tasks should not receive persisted goals by default; their schedules drive recurrence. Implementation tasks may use normal task goals, worktrees, review, and PR flows independently.

The inbox prompt should list `decision_state=approved` notifications, inspect each with `get_alert`, claim it, call `create_alert_implementation_task`, and then mark processing complete. On pre-link failure it should mark processing failed or release the claim. It must omit `project_id` or repeat only its own project ID.
