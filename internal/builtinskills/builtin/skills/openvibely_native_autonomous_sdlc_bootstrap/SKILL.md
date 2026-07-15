---
kind: openvibely.agent_skill
version: 1
skill:
    key: openvibely_native_autonomous_sdlc_bootstrap
    name: OpenVibely Native Autonomous SDLC Bootstrap
    scope: global
    description: Bootstrap a review-gated autonomous SDLC loop using project-scoped OpenVibely notifications, visible scheduled tasks, and implementation tasks.
---

# OpenVibely Native Autonomous SDLC Bootstrap

Use this skill when the user wants autonomous suggestions reviewed on OpenVibely's Alerts page instead of using GitHub issues as the mailbox. Keep the notification API generic: suggestions are ordinary actionable notifications, and the same tools can support product, operations, maintenance, or other approval-gated workflows.

## Safety Boundary

Human approval authorizes creation of an OpenVibely implementation task only. It does not authorize merge, release, deployment, destructive remediation, credential changes, or any other higher-risk action. Those actions retain their existing review and permission boundaries.

## Bootstrap

1. Create one visible suggestion-producing task and one visible notification-inbox task in the current project. Recurrence comes from schedules, so do not set persisted goals on recurring loop tasks unless the user explicitly requests goal-driven continuation.
2. Schedule the suggestion producer at the requested audit cadence. Its prompt should inspect one focused area, avoid duplicates, and call `create_alert` with a stable `idempotency_key`, a generic `type`, a concise title/message, a detailed body, and structured metadata. It must not create implementation tasks or modify code.
3. Schedule the notification inbox, commonly hourly. Its prompt must call `list_alerts` with `decision_state=approved`, then inspect each result with `get_alert` before attempting `claim_alert`.
4. For each claimed notification, call `create_alert_implementation_task`. This operation atomically creates and links one Backlog task, and is idempotent on retries. Put the notification ID, reviewed body, metadata, acceptance criteria, and the approval boundary in the task prompt.
5. After successful linkage, call `complete_alert_processing`. If work cannot be linked, call `fail_alert_processing` with a concise retry diagnostic. Use `release_alert_claim` only when no implementation task was linked and another scan should retry immediately.
6. Report the visible tasks and schedules created, plus any missing runtime-tool or model capability.

Do not supply another project's `project_id`. Runtime tools bind to the executing task's persisted project and reject mismatches. Do not derive project ownership from the active browser project.

## Suggestion Producer Prompt

```text
Inspect one focused project area for a small, reviewable improvement. Do not modify code and do not create implementation tasks.

For each actionable suggestion, call `create_alert` with:
- a generic type such as `product_suggestion`, `bug_suggestion`, `performance_suggestion`, or `maintenance_suggestion`;
- a concise title and message;
- a detailed body with evidence, scope, risk, and acceptance criteria;
- structured metadata identifying the inspected component and evidence;
- a stable idempotency key derived from the project-independent finding identity.

The notification will remain pending until a human approves or rejects it on Alerts. Approval authorizes task creation only, not merge, release, or deployment.
```

## Notification Inbox Prompt

```text
Process approved actionable notifications for this scheduled task's own project.

Call `list_alerts` with `decision_state=approved`, `implementation_task_linked=false`, a bounded limit, and stable pagination. Do not pass a different project ID. For each result, call `get_alert` and inspect the full body and metadata before claiming it.

Call `claim_alert` for each notification you can process. If the claim succeeds, call `create_alert_implementation_task` with a focused Backlog task title and prompt. Include the notification ID, reviewed context, acceptance criteria, and the rule that human approval authorized task creation only. The operation atomically links at most one task and is safe to retry after a crash.

Call `complete_alert_processing` after the implementation task is linked. If creation/linkage fails before a task is linked, call `fail_alert_processing` with a concise error so a later scan can retry. Release a claim only when no task was linked and immediate retry by another scan is appropriate.
```

## Recovery

Claims are leases. An expired unlinked claim can be acquired by a later scan. Failed unlinked processing is retryable. Once an implementation task is linked, repeated task-creation calls return the same task and never create a duplicate. Use the linked task shown on Alerts as the durable continuation record.
