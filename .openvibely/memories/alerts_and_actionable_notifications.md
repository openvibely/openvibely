---
name: alerts_and_actionable_notifications
type: project
created: 2026-07-15
updated: 2026-07-17
source: task_followup
source_id: 22989609f707f44e0c687dc1babfef8c
confidence: high
title: Alerts and Actionable Notifications
---

OpenVibely supports backward-compatible operational alerts and generic approval-based actionable notifications. A fresh, separate, strictly read-only audit on 2026-07-16 inspected final task `HEAD` `f41c330883f78a8d19e9e88c5a511a22425edd2e`, seven commits ahead and zero behind local `main` `da1ae2103d11c544fea6b97e169ec6ffe8f0b5ec`. It found no material bugs, regressions, lifecycle inconsistencies, integration issues, or missing original requirements, completing the implementation and audit gate.

Durable model and migration facts:
- The pre-notification baseline already required `alerts.project_id`; list/count operations were project-filtered, but ID-based reads and mutations had lacked project constraints. Detail, read-state, delete, decision, claim, linkage, and processing paths now enforce project ownership server-side.
- Existing operational alerts retain their persisted project IDs. Migration does not infer ownership from the active UI project and does not introduce implicit global visibility; legacy rows are backfilled as `scope=project`, `decision=not_required`, and `processing=not_applicable`.
- Actionable notification decision state is separate from read/unread and automation processing state. Notifications carry project/scope, type, title/message/body, source and source-task identity, timestamps, structured metadata, optional project-scoped idempotency key, lease claimant/time, processing/failure state, and linked implementation task.
- Human approval authorizes downstream task creation only. It does not authorize merge, release, deployment, or other higher-risk actions.

Authorization, concurrency, and runtime facts:
- Scheduled execution context uses persisted `task.ProjectID`, not process-global or current UI project state. A supplied `project_id` is only an equality assertion and is rejected when it differs from the caller's authorized project.
- `create_alert` preserves the legacy operational-alert contract: title is required, type defaults to `custom`, and message, severity, operational type, and same-project `task_id` remain optional. Operational alerts use `decision=not_required` and `processing=not_applicable` rather than entering approval workflow.
- `create_notification` is the generic actionable-notification tool. It creates a pending project-scoped notification, binds source-task identity from the persisted caller task, accepts structured metadata, and supports project-scoped idempotency keys.
- Initial tasks, scheduled tasks, ordinary task-thread follow-ups, ordinary web/API Chat, Slack, Telegram, Discord, and Email expose `create_notification` when the selected provider/auth path supports runtime tools. Task-thread definitions and capability summaries include it by default, and dispatch derives the project and trusted source-task identity from the persisted follow-up, Chat backing task, or channel task context.
- Ordinary Chat exposes the full notification lifecycle in Orchestrate mode and uses each turn's persisted backing Chat task for trusted source/claimant identity. Plan mode exposes only read operations such as `list_alerts` and `get_alert`; notification mutations are blocked. Runtime-tool-incapable provider/auth paths receive none of these tools and have no bracket-marker fallback.
- The structured runtime surface also covers stable filtered/paginated listing, detail, atomic claim, atomic implementation-task creation/linkage, explicit linkage, processing completion/failure, and claim release/retry.
- Claims are lease-based and atomic. Stale leases and failed attempts can be recovered. `create_alert_implementation_task` requires a non-empty title and prompt and a notification currently claimed by the persisted caller task; out-of-range priority defaults to 2. In one SQLite `BEGIN IMMEDIATE` transaction it either returns the already-linked task or creates a same-project `backlog`/`pending` task with `created_via=system_agent`, stores its ID on the notification, changes processing to `implementation_task_linked`, and clears claim expiry. It does not start the task, mark processing complete, merge, release, or deploy. This atomic persisted linkage prevents duplicates across crashes, retries, and competing scans.
- Slack, Telegram, and Discord first-turn channel runtimes are constructed only after the channel Chat task is persisted, so channel-priority lifecycle handlers receive that task ID as trusted caller/claimant identity. Email uses the generic executor with the persisted Chat task. Channel notification creation therefore records trusted source-task linkage.
- All alert lifecycle mutations publish the existing project-scoped alert invalidation event, including claim, release, explicit linkage, atomic task creation, completion, failure, read, and delete operations.

Product surfaces:
- The Alerts page supports inspection, approve/reject controls for pending notifications, decision and processing badges, claimant/failure details, linked-task navigation, project context, and project-filtered live refresh. Existing operational alert read/delete behavior remains supported.
- The bundled `openvibely_native_autonomous_sdlc_bootstrap` skill is present after the 2026-07-16 rebase and provides an OpenVibely-native alternative to the retained GitHub-backed workflow. Suggestion producers use `create_notification`; scheduled inbox tasks list and inspect approved notifications, claim them, and create one atomically linked implementation task. The required approval, claim, linkage, completion, and failure runtime operations are available on the rebased capability surface.
- The model, migration, authorization boundaries, tool contracts, lease recovery, and schedule configuration are documented in `docs/openvibely-native-autonomous-sdlc-user-guide.md`.

Validation and audit evidence:
- Coverage includes legacy `create_alert`; initial, scheduled, channel, and task-thread `create_notification` exposure; task-thread capability and dispatch with persisted project/source-task identity; all list filters and pagination; foreign-project lifecycle rejection; stale-claim and failure/release recovery; explicit linkage; lifecycle invalidation; handler visibility and reject/approval behavior; integrated HTTP visibility-to-approval-to-linkage flow; and competing implementation-task creation idempotency.
- After the 2026-07-16 rebase and semantic conflict resolution, `templ generate`, `go build ./...`, full uncached `go test ./... -count=1 -timeout 300s`, `go vet ./...`, conflict-marker checks, and `git diff --check` passed. Template generation produced no changes; the only extra output was the known non-failing macOS desktop linker SDK-version warning.
- The final strictly read-only audit inspected `HEAD` `f41c330883f78a8d19e9e88c5a511a22425edd2e` against local `main` `da1ae2103d11c544fea6b97e169ec6ffe8f0b5ec`. It found no material code, migration, authorization, runtime, concurrency, UI, test, documentation, skill, integration, or lifecycle-memory issue. Builds and tests were not rerun during that audit; the preceding modifying turns' successful validation remains the build/test evidence.
- The current merge result preserves `main`'s newer `list_tasks`, channel action-context, shared authorization, runtime composition, and unrelated chat shared-test update while retaining project-scoped notification handlers, persisted caller-task identity, and server-side project isolation.
- The managed service, canonical, and tracked Alerts, product-vision, and memory-index artifacts were byte-identical and semantically consistent during the final audit; the prior lifecycle-memory blocker is resolved and no further audit remains required for this task.
