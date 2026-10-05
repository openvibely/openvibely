---
name: automation_graphs
type: project
created: 2026-07-18
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Automation Graphs
---

Automation Graphs is OpenVibely's project-scoped, reviewable orchestration surface. It compiles visible Tasks, Schedules, Alerts/GitHub handoffs, workers, queues, and provenance; it is not a separate graph executor, queue, worker, or arbitrary-code runtime.

- Canonical YAML is the only configuration-authoring surface for templates, Describe, Custom, Chat save, and edit. Graph mode edits browser-local topology/layout only. YAML version 1 rejects malformed/unsafe schema, invalid topology/capabilities, and foreign-project references before resource effects.
- Chat save follows PreviewSave/Save. Run/pause/resume/template-update/delete are Orchestrate-only. Existing graph-backed Automations serialize to YAML on read; preview/save does not silently migrate or rewrite point-in-time definitions/resources. Maintained template changes require revision and explicit update/save; saved configurations are snapshots.
- Save atomically persists identity/current graph, materialized tasks/schedules, topology, and projections. Invalid saves have no resource effects. Pause/archive disables schedules and demotes pending current-graph work; active executions and unrelated/shared resources must be preserved.
- Deletion ownership comes from explicit trigger-owner records, never title/category. Remove owned schedules and delete a trigger task only when no surviving schedule/Automation reference uses it. Project scope and in-flight guards precede mutation.
- Runtime uses ordinary Tasks, Scheduler, Alerts, GitHub, workers, queues, and `thread_inputs`. Durable invocations/leases/reservations/work items/activities support recovery and visible projection. Claims/failures must be crash-consistent; cancellation wins. Run Now does not alter schedule timing and is idempotent for owned schedule occurrences.
- Automation-owned scheduled work remains ordinary generic tasks. Service authorization is decisive. Approval only authorizes configured implementation handoff, never merge/release/deploy. Native handoff uses atomic claim, Backlog creation/linkage, exact linked-task execution, and completion-after-start.
- Native ownership is project + Automation + alert; GitHub inbox work is reconciled from current issue assignment/provenance. GitHub topology and Native approval topology must not mix. Assignment to PAT owner/configured Authorized Users authorizes implementation, not PR approval/merge.
- Duplicate prevention is existing-work-first: list candidates, hydrate likely matches, skip covered findings, continue searching, and create at most one new finding. Unstable model-authored keys/title hashes are not authority; direct-caller idempotency is separate.
- GitHub actions use selected-project repository identity, require usable authorization, matching branch/head, required PR sections, and exact source-issue provenance. Local work does not prove publication. Runtime list/detail tools return bounded compact project-scoped summaries; full graph/details stay on UI surfaces.
- Maintained Native SDLC and GitHub SDLC are starting templates; Custom is the runnable blank builder. Template/Describe load YAML without creating resources. Duplicate starts as identity-free local draft. Only supported adapters may be materialized; unknown/legacy types fail closed.
- Remaining gaps include accessible Automation run/work-item history, non-persisting impact previews for update/delete, bounded contention-aware external refresh, and repair of stale-origin/task projections without deleting active work. Avoid weak per-Automation `skills`/`source_files` hints; lifecycle Skill Curator routes skills and Vision Suggestions should verify/read root `VISION.md`.

Approval and notification details live in `alerts_and_actionable_notifications.md`; task-chain materialization lives in `openvibely_architecture.md`.
