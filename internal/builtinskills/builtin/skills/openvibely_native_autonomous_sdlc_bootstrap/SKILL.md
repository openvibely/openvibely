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

1. Create one visible scheduled OpenVibely task for each maintained loop role: Vision Suggestions, Bug Finder, Optimization Finder, Redundancy Finder, Notification Inbox, and Loop Auditor. Do not create separate runner tasks. The task attached to the schedule owns the loop prompt. Recurrence comes from schedules, so do not set persisted goals on recurring loop tasks unless the user explicitly requests goal-driven continuation.
2. Schedule Vision Suggestions and the three finder tasks at the requested audit cadence, usually daily. Their prompts should inspect one focused area, avoid duplicates, and call `create_notification` with a stable `idempotency_key`, a generic `type`, a concise title/message, a detailed body, and structured metadata. They must not create implementation tasks or modify code.
3. Schedule the Notification Inbox, commonly hourly. Its prompt must call `list_alerts` with `decision_state=approved`, then inspect each result with `get_alert` before attempting `claim_alert`.
4. For each claimed notification, call `create_alert_implementation_task`. This operation atomically creates and links one Backlog task, and is idempotent on retries. Put the notification ID, reviewed body, metadata, acceptance criteria, and the approval boundary in the task prompt.
5. After successful linkage, call `complete_alert_processing`. If work cannot be linked, call `fail_alert_processing` with a concise retry diagnostic. Use `release_alert_claim` only when no implementation task was linked and another scan should retry immediately.
6. Schedule the Loop Auditor, usually weekly. It should inspect stale notifications, expired or failed claims, missing notification/task links, duplicate implementation work, and blocked tasks. It reports findings through Native notifications and does not bypass approval or alter implementation work itself.
7. After all tasks and schedules exist, call `register_automation_resources` once with `adapter_key: native_sdlc`, stable key `native-sdlc/default`, and the actual IDs. Bind both the task and its schedule to the same node key: `vision_suggestions`, `bug_finder`, `optimization_finder`, `redundancy_finder`, `inbox`, and `auditor`. Do not use separate trigger node keys, pass topology JSON, or infer old resources. A setup rerun reuses the same Automation identity.
8. Report the visible tasks and schedules created, the returned Automation URL, plus any missing runtime-tool or model capability.

Do not supply another project's `project_id`. Runtime tools bind to the executing task's persisted project and reject mismatches. Do not derive project ownership from the active browser project.

## Suggested Visible Tasks

- `Native Vision Suggestions`, daily. Reads project vision/source-of-truth files and creates reviewable feature notifications only.
- `Native Notification Inbox`, hourly. Processes approved notifications into one linked implementation task each.
- `Native Bug Finder`, daily. Audits a focused component for likely defects and creates bug notifications only.
- `Native Optimization Finder`, daily. Looks for measurable performance or workflow improvements and creates optimization notifications only.
- `Native Redundancy Finder`, daily. Looks for duplicated or redundant code and creates maintenance notifications only.
- `Native Loop Auditor`, weekly. Reviews stale notifications, claims, missing task links, duplicate tasks, and blocked work.

## Discovery Prompt Pattern

```text
Choose one focused project component or workflow to inspect this run. Vary the component over time instead of repeatedly auditing the same files. Do not modify code and do not create implementation tasks.

Use this task's role as its scope:
- Vision Suggestions: small, reviewable gaps against the project vision or source-of-truth files.
- Bug Finder: likely defects, edge-case failures, broken behavior, or missing regression coverage.
- Optimization Finder: measurable performance, latency, memory, build, or workflow efficiency improvements.
- Redundancy Finder: duplicated or redundant code that could be made generic without over-engineering.

For each actionable finding, call `create_notification` with:
- a generic type such as `product_suggestion`, `bug_suggestion`, `performance_suggestion`, or `maintenance_suggestion`;
- a concise title and message;
- a detailed body with evidence, scope, risk, and acceptance criteria;
- structured metadata identifying the inspected component and evidence;
- a stable idempotency key derived from the project-independent finding identity.

The notification remains pending until a human approves or rejects it on Alerts. Approval authorizes task creation only, not merge, release, or deployment.
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
