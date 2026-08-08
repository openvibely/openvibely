---
kind: openvibely.agent_skill
version: 1
skill:
    key: openvibely_native_autonomous_sdlc_bootstrap
    name: OpenVibely Native Autonomous SDLC Bootstrap
    scope: global
    enabled: false
    description: Bootstrap a review-gated autonomous SDLC loop using project-scoped OpenVibely notifications, visible scheduled tasks, and implementation tasks.
---

# OpenVibely Native Autonomous SDLC Bootstrap

Use this skill when the user wants autonomous suggestions reviewed on OpenVibely's Alerts page instead of using GitHub issues as the mailbox. Keep the notification API generic: suggestions are ordinary actionable notifications, and the same tools can support product, operations, maintenance, or other approval-gated workflows.

## Safety Boundary

Human approval authorizes creating and starting the linked OpenVibely implementation task. It does not authorize merge, release, deployment, destructive remediation, credential changes, or any other higher-risk action. Those actions retain their existing review and permission boundaries.

## Bootstrap

1. Create one visible scheduled OpenVibely task for each maintained loop role: Vision Suggestions, Bug Finder, Optimization Finder, Redundancy Finder, and Notification Inbox. Do not create separate runner tasks. The task attached to the schedule owns the loop prompt. Recurrence comes from schedules, so do not set persisted goals on recurring loop tasks unless the user explicitly requests goal-driven continuation.
2. Schedule Vision Suggestions and the three finder tasks at the requested audit cadence, usually daily. Their prompts should inspect one focused area and call `create_notification` with a stable `idempotency_key` in the form `<finder role>:<primary file or component>:<stable symbol or behavior>`; reuse the exact key for a reworded finding or retry and never include run IDs, dates, timestamps, or random values. a generic `type`, a concise title/message, a detailed body, and structured metadata. Native notification idempotency is the duplicate-prevention boundary. Do not list, search, or inspect GitHub issues for duplicate detection. They must not create implementation tasks or modify code.
3. Schedule the Notification Inbox, commonly hourly. Its prompt must call `list_alerts` without `project_id`, using `decision_state=approved` and `implementation_task_linked=false`. Before any claim or linkage mutation, collect every eligible result from all stable pages; mutating the filtered set before later offsets are fetched can skip notifications. After the complete snapshot is collected, inspect each result with `get_alert` before attempting `claim_alert`. The runtime automatically uses the scheduled task's persisted project. Never reuse a project ID from prior messages, examples, memory, or tool output.
4. For each claimed notification, call `create_alert_implementation_task`. This operation atomically creates and links one Backlog task, and is idempotent on retries. The created task is the implementation task. Put the notification ID, reviewed body, metadata, acceptance criteria, and a direct instruction to implement the reviewed change, add or update tests, and run required validation in its prompt. State that it is already the linked implementation task, must not create or look for another implementation task, and must not run notification intake or call `get_alert`. Human approval authorizes creating and starting that task, but not merge, release, deployment, destructive remediation, or credential changes. Never tell the created task that it lacks authorization to implement.
5. Use the returned `implementation_task_id` to call `execute_tasks` with that exact ID. Only after execution starts, call `complete_alert_processing`. If creation, linkage, or execution fails, call `fail_alert_processing` with a concise retry diagnostic. Use `release_alert_claim` only when no implementation task was linked and another scan should retry immediately.
6. After all tasks and schedules exist, call `register_automation_resources` once with `adapter_key: native_sdlc`, stable key `native-sdlc/default`, and the actual IDs. Bind both the task and its schedule to the same node key: `vision_suggestions`, `bug_finder`, `optimization_finder`, `redundancy_finder`, and `inbox`. Do not use separate trigger node keys, pass topology JSON, or infer old resources. A setup rerun reuses the same Automation identity.
7. Report the visible tasks and schedules created, the returned Automation URL, plus any missing runtime-tool or model capability.

Do not supply another project's `project_id`. Runtime tools bind to the executing task's persisted project and reject mismatches. Do not derive project ownership from the active browser project.

## Suggested Visible Tasks

- `Native Vision Suggestions`, daily. Reads project vision/source-of-truth files and creates reviewable feature notifications only.
- `Native Notification Inbox`, hourly. Processes approved notifications into one linked implementation task each.
- `Native Bug Finder`, daily. Audits a focused component for likely defects and creates bug notifications only.
- `Native Optimization Finder`, daily. Looks for measurable performance or workflow improvements and creates optimization notifications only.
- `Native Redundancy Finder`, daily. Looks for duplicated or redundant code and creates maintenance notifications only.

## Role-Specific Discovery Prompts

Give each finder its own prompt. Never use one shared three-role menu and expect it to infer its identity from the task title.

- Bug Finder: start with `You are the Bug Finder.` Inspect only likely correctness defects, edge-case failures, broken behavior, or missing regression coverage. Require a concrete failure path, expected versus actual behavior, and needed regression coverage. Create only `bug_suggestion` notifications.
- Optimization Finder: start with `You are the Optimization Finder.` Inspect only measurable performance, latency, throughput, memory, build, or workflow efficiency bottlenecks. Require evidence or a measurement plan and before-and-after criteria. Create only `performance_suggestion` notifications.
- Redundancy Finder: start with `You are the Redundancy Finder.` Inspect only demonstrated duplicated or redundant code, configuration, or workflow logic. Name the repeated locations and propose the smallest safe consolidation without over-engineering. Create only `maintenance_suggestion` notifications.

Every discovery prompt must choose one focused project component or workflow, vary it over time, and forbid modifying code or creating implementation tasks. Require every body to start with `## Summary`: 2-4 plain-language sentences explaining the finding, one concrete example a user would notice, and why it matters. After that summary, preserve all details useful to an implementation agent, including the inspected component, evidence and failure paths, expected versus actual behavior, risk, suggested implementation direction, acceptance criteria, file/symbol references, and regression cases. Do not list, search, or inspect GitHub issues for duplicate detection. Native notification idempotency is the duplicate-prevention boundary. For every actionable finding, call `create_notification` with the role's exact type, a readable title and message, the full body, structured metadata, and a stable idempotency key in the form `<finder role>:<primary file or component>:<stable symbol or behavior>`. Reuse the exact key for a reworded finding or retry; never include run IDs, dates, timestamps, or random values.

The notification remains pending until a human approves or rejects it on Alerts. Approval authorizes task creation only, not merge, release, or deployment.

## Notification Inbox Prompt

```text
Process approved actionable notifications for this scheduled task's own project.

Call `list_alerts` without `project_id`, using `decision_state=approved`, `implementation_task_linked=false`, a bounded limit, and stable pagination. Do not pass the `read` filter: both read and unread approved notifications are eligible for implementation. The runtime automatically uses this scheduled task's persisted project. Never reuse a project ID from prior messages, examples, memory, or tool output. Before calling `claim_alert`, collect every eligible result from all pages by following the returned pagination offsets. Do not claim, link, or process any notification while paginating because linkage removes rows from this filtered result set and advancing an offset after mutation can skip notifications. Only after the complete paginated snapshot is collected, call `get_alert` for each collected notification and inspect the full body and metadata before claiming it.

Call `claim_alert` for each notification you can process. If the claim succeeds, call `create_alert_implementation_task` with a focused Backlog task title and prompt. The created task is the implementation task. Its prompt must include the notification ID, reviewed context, acceptance criteria, and directly instruct it to implement the reviewed change in its repository, add or update tests, and run the required validation. State that it is already the linked implementation task, must not create or look for another implementation task, and must not run notification intake or call `get_alert`. Human approval authorizes creating and starting that implementation task; it does not authorize merge, release, deployment, destructive remediation, or credential changes. Do not use wording that says the created task lacks authorization to implement. The operation atomically links at most one task and is safe to retry after a crash. Use the returned `implementation_task_id` to call `execute_tasks` with that exact task ID so approved work starts immediately. Do not leave the created task waiting in Backlog.

Only after `execute_tasks` succeeds, call `complete_alert_processing`. If creation, linkage, or task execution fails, call `fail_alert_processing` with a concise error so the linked task can be inspected and recovered; do not report processing complete. Release a claim only when no task was linked and immediate retry by another scan is appropriate.
```

## Recovery

Claims are leases. An expired unlinked claim can be acquired by a later scan. Failed unlinked processing is retryable. Once an implementation task is linked, repeated task-creation calls return the same task and never create a duplicate. Use the linked task shown on Alerts as the durable continuation record.
