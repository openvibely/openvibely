---
name: alerts_and_actionable_notifications
type: project
created: 2026-07-15
updated: 2026-07-15
source: task_audit
source_id: 22989609f707f44e0c687dc1babfef8c
confidence: high
title: Alerts and Actionable Notifications
---

OpenVibely supports backward-compatible operational alerts and generic approval-based actionable notifications at implementation head `c0cdb016f9c1b4fddc54ee69f60ad9d2fafacd77`, with synchronized lifecycle memory at task head `fb31cf476224a2b881321ed7b21a63f0a525d557`. Implementation and authoritative validation completed on 2026-07-15, and the required fresh separate strictly read-only audit found no material bugs, regressions, stale lifecycle artifacts, or missing original requirements.

Durable model and migration facts:
- The pre-notification baseline already required `alerts.project_id`; list/count operations were project-filtered, but ID-based reads and mutations had lacked project constraints. Detail, read-state, delete, decision, claim, linkage, and processing paths now enforce project ownership server-side.
- Existing operational alerts retain their persisted project IDs. Migration does not infer ownership from the active UI project and does not introduce implicit global visibility; legacy rows are backfilled as `scope=project`, `decision=not_required`, and `processing=not_applicable`.
- Actionable notification decision state is separate from read/unread and automation processing state. Notifications carry project/scope, type, title/message/body, source and source-task identity, timestamps, structured metadata, optional project-scoped idempotency key, lease claimant/time, processing/failure state, and linked implementation task.
- Human approval authorizes downstream task creation only. It does not authorize merge, release, deployment, or other higher-risk actions.

Authorization, concurrency, and runtime facts:
- Scheduled execution context uses persisted `task.ProjectID`, not process-global or current UI project state. A supplied `project_id` is only an equality assertion and is rejected when it differs from the caller's authorized project.
- `create_alert` preserves the legacy operational-alert contract: title is required, type defaults to `custom`, and message, severity, operational type, and same-project `task_id` remain optional. Operational alerts use `decision=not_required` and `processing=not_applicable` rather than entering approval workflow.
- `create_notification` is the generic actionable-notification tool. It creates a pending project-scoped notification, binds source-task identity from the persisted caller task, accepts structured metadata, and supports project-scoped idempotency keys.
- Initial tasks, scheduled tasks, ordinary task-thread follow-ups, Slack, Telegram, and Discord expose `create_notification`. Task-thread definitions and capability summaries include it by default, and dispatch derives the project and trusted source-task identity from the persisted follow-up task context.
- The structured runtime surface also covers stable filtered/paginated listing, detail, atomic claim, atomic implementation-task creation/linkage, explicit linkage, processing completion/failure, and claim release/retry.
- Claims are lease-based and atomic. Stale leases and failed attempts can be recovered. Persisted linkage and atomic notification-to-Backlog-task creation prevent duplicate implementation tasks across crashes, retries, and competing scans.
- Slack, Telegram, and Discord first-turn channel runtimes are constructed only after the channel Chat task is persisted, so channel-priority lifecycle handlers receive that task ID as trusted caller/claimant identity. Channel notification creation therefore also records trusted source-task linkage.
- All alert lifecycle mutations publish the existing project-scoped alert invalidation event, including claim, release, explicit linkage, atomic task creation, completion, failure, read, and delete operations.

Product surfaces:
- The Alerts page supports inspection, approve/reject controls for pending notifications, decision and processing badges, claimant/failure details, linked-task navigation, project context, and project-filtered live refresh. Existing operational alert read/delete behavior remains supported.
- The former bundled `openvibely_native_autonomous_sdlc_bootstrap` used `create_notification` plus scheduled inbox tasks to claim approved notifications and create atomically linked implementation tasks. It was archived during skill-library maintenance after its required approval, claim, task-linking, completion, and failure operations were no longer available on the current capability surface; do not rely on that bootstrap unless those tool contracts are restored.
- The model, migration, authorization boundaries, tool contracts, lease recovery, and schedule configuration are documented in `docs/openvibely-native-autonomous-sdlc-user-guide.md`.

Final validation and audit evidence as of 2026-07-15:
- Coverage includes legacy `create_alert`; initial, scheduled, channel, and task-thread `create_notification` exposure; task-thread capability and dispatch with persisted project/source-task identity; all list filters and pagination; foreign-project lifecycle rejection; stale-claim and failure/release recovery; explicit linkage; lifecycle invalidation; handler visibility and reject/approval behavior; integrated HTTP visibility-to-approval-to-linkage flow; and competing implementation-task creation idempotency.
- After the task-thread fix, `templ generate`, `go build ./...`, full uncached `go test ./... -count=1 -timeout 300s`, `go vet ./...`, and `git diff --check` passed. The only extra output was the known non-failing macOS desktop linker SDK-version warning.
- The final qualifying strictly read-only audit inspected the complete 36-file feature range at clean task head `fb31cf476224a2b881321ed7b21a63f0a525d557`. It directly verified migration/backfill, project-scoped storage and mutations, runtime schemas and dispatch, scheduled/channel/task-thread persisted caller identity, lifecycle invalidation, concurrency/idempotency, handlers/UI, tests, bundled skill, documentation, and managed-memory/index synchronization, and found no material issue.
- At the final audit, the managed-memory service view, canonical project topic, tracked worktree topic, and indexes were synchronized, with exactly one Alerts index entry.
