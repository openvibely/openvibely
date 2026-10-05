---
name: alerts_and_actionable_notifications
type: project
created: 2026-07-15
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Alerts and Actionable Notifications
---

OpenVibely supports backward-compatible operational alerts and generic approval-based actionable notifications.

- Alerts are project-owned. List/count, detail, read state, deletion, decisions, claims, linkage, and processing enforce ownership server-side. Legacy operational alerts remain project-scoped with `decision=not_required`; ownership is never inferred from current UI project.
- Decision state is separate from read/unread and Automation processing. Actionable records retain source identity, structured metadata, idempotency, claim lease, processing state, and linked implementation task. Deleting a linked task clears its ID but preserves notification history and evidence that a task was linked.
- Human approval authorizes only creation/start of the configured downstream implementation task. It never authorizes merge, release, deployment, destructive remediation, credential changes, or arbitrary execution.
- Scheduled runtime scope derives from persisted `task.ProjectID`. Ordinary scheduled alert listing cannot be redirected with model-supplied project/read filters; ordinary Chat/task runtimes may use optional project equality/read filters.
- Alert mutations use immediate project-scoped transactions and publish invalidations using known identity so deletion cannot suppress its event. Automation inbox scans use explicit ownership bindings and fail closed when bindings are empty/invalid.
- `create_alert` remains an operational alert action. `create_notification` creates a pending project-scoped approval record and binds trusted source-task identity from persisted caller context; model-facing input does not expose idempotency keys. Both are available only on runtime-tool-capable paths; Chat mutations are Orchestrate-only and there is no prose-marker fallback.
- Approved notifications can be atomically claimed, linked to one same-project Backlog/Pending implementation task, and completed only after exact task execution starts. Failure/release/retry and lease ownership must remain durable and project-scoped. Creating/linking does not itself start, complete, merge, release, or deploy work.
- Notification content begins with a short nontechnical `## Summary`, then evidence and implementation detail. Compact cards omit secrets/sensitive metadata; detail supports sanitized inspection, decision/processing status, linked-task navigation, and project-filtered refresh.
- Maintained Automation prompts are point-in-time snapshots; template updates require explicit revision and update/save. Existing Backlog tasks are not retroactively started. Automation topology and ownership are in `automation_graphs.md`.
