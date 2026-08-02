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
Use the OpenVibely Native Autonomous SDLC Bootstrap skill. Create one visible scheduled task for each maintained loop role: Vision Suggestions, Bug Finder, Optimization Finder, Redundancy Finder, Notification Inbox, and Loop Auditor. Do not create separate runner tasks. Discovery tasks must create reviewable notifications only, use stable idempotency keys, and never modify code or create implementation tasks. Native notification idempotency is the duplicate-prevention boundary; do not list, search, or inspect GitHub issues for duplicate detection. The inbox must inspect approved notifications, claim them, and atomically create linked Backlog implementation tasks. The auditor must report loop-health findings through notifications without changing implementation work. Do not authorize merge, release, or deployment. Register the created tasks and schedules as the maintained Native SDLC Automation.
```

The six scheduled tasks have separate responsibilities:

- `Native Vision Suggestions`, usually daily, compares a focused area with project vision or source-of-truth files and creates small reviewable feature notifications.
- `Native Bug Finder`, usually daily, inspects a focused component for likely defects, edge cases, broken behavior, or missing regression coverage.
- `Native Optimization Finder`, usually daily, looks for measurable performance, latency, memory, build, or workflow improvements.
- `Native Redundancy Finder`, usually daily, identifies duplicated or redundant code that can be made generic without over-engineering.
- `Native Notification Inbox`, commonly hourly, turns approved notifications into at most one linked Backlog implementation task each.
- `Native Loop Auditor`, usually weekly, checks stale notifications, expired or failed claims, missing task links, duplicate implementation work, and blocked tasks.

Each role is one visible scheduled task; do not create separate runner tasks. Recurring loop tasks should not receive persisted goals by default because their schedules drive recurrence. Implementation tasks may use normal task goals, worktrees, review, and PR flows independently.

## Discovery And Review

Vision Suggestions and the three finders inspect one focused area per run and vary that area over time. Give each finder its own prompt; never use one shared three-role menu and expect it to infer its identity from the task title: Bug Finder requires a concrete correctness failure path and creates only `bug_suggestion`; Optimization Finder requires measurable evidence or a measurement plan with before-and-after criteria and creates only `performance_suggestion`; Redundancy Finder identifies repeated locations and the smallest safe consolidation and creates only `maintenance_suggestion`. Every notification body starts with `## Summary`: 2-4 plain-language sentences explaining the finding, one concrete example a user would notice, and why it matters. The full technical analysis remains below it for the implementation agent, including inspected components, evidence and failure paths, expected versus actual behavior, risk, suggested implementation direction, acceptance criteria, file/symbol references, and regression cases. Notifications also carry a readable title and message, structured metadata, and a stable project-independent idempotency key. Native notification idempotency is the duplicate-prevention boundary. Do not list, search, or inspect GitHub issues for duplicate detection. Finders must not create implementation tasks or modify code.

Notifications remain pending until a human approves or rejects them on Alerts. Approval authorizes creating and starting the linked implementation task. The implementation task is expected to edit the repository, add or update tests, and run validation. Approval does not authorize merge, release, deployment, destructive remediation, or credential changes.

## Inbox And Recovery

Notification Inbox instructions: Call `list_alerts` without `project_id`, using `decision_state=approved` and `implementation_task_linked=false`, and do not pass the `read` filter so both read and unread approved notifications remain eligible. The runtime automatically uses the scheduled task's persisted project. Never reuse a project ID from prior messages, examples, memory, or tool output. Before any claim or linkage mutation, collect every eligible result from all stable pages; mutating the filtered set before later offsets are fetched can skip notifications. After the complete paginated snapshot is collected, inspect every collected result with `get_alert`, and then attempt `claim_alert`. For each successful claim, call `create_alert_implementation_task` with the reviewed notification ID, body, metadata, and acceptance criteria. The created task is the implementation task: its prompt directly tells it to implement the reviewed change, add or update tests, and run validation; it must not create or look for another implementation task, run notification intake, or call `get_alert`. The prompt preserves the boundary against merge, release, deployment, destructive remediation, and credential changes without saying that implementation itself is unauthorized. Use the returned `implementation_task_id` to call `execute_tasks` with that exact task ID so implementation starts immediately. Only after `execute_tasks` succeeds, call `complete_alert_processing`.

If task creation, linkage, or execution fails, the inbox calls `fail_alert_processing` with a concise recovery diagnostic and does not report processing complete. It uses `release_alert_claim` only when no implementation task was linked and another scan should retry immediately. Claims are leases, failed or expired unlinked work is retryable, and repeated task-creation calls return the already linked task rather than creating a duplicate. The inbox omits `project_id`; runtime binds every action to its persisted task project and rejects any mismatch.

The Loop Auditor reports findings through new Native notifications. Native notification and task state is authoritative for duplicate checks; it does not list, search, or inspect GitHub issues. It does not alter implementation work or bypass human approval.

## Automation Registration

After all six tasks and schedules exist, the bootstrap calls `register_automation_resources` once with adapter `native_sdlc`, stable key `native-sdlc/default`, and the actual resource IDs. Each task and its schedule use the same node key: `vision_suggestions`, `bug_finder`, `optimization_finder`, `redundancy_finder`, `inbox`, and `auditor`.

Registration publishes only these explicitly created maintained resources. It does not infer, migrate, or backfill legacy tasks or schedules. A setup rerun reuses the same Automation identity. The bootstrap reports the visible tasks and schedules, the Automation URL, and any missing runtime-tool or model capability.
