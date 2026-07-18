# Automation Implementation Status

## Current Phase

Phase 2: Invocations, Work Items, And Live Position (complete).

## Status

Phase 1 complete at checkpoint `6ff0e9a`. Phase 2 implementation, validation, fresh contract audit, and clean checkpoint completed on 2026-07-18.

## Phase 2 Checklist

- [x] Add `executions.dispatch_id` and durable invocation, leased outbox, task-run reservation, work-item, position, thread-input binding, activity, activity-resource, and append-only transition tables in the required dependency order.
- [x] Enforce composite project/automation/version/node/invocation/work-item/activity/edge parents, nullable-reference semantics, canonical key uniqueness, project cascade, query indexes, and migration up/down coverage with foreign keys enabled.
- [x] Add server-derived `AutomationContext`, multi-binding `AutomationBinding`, and prepared `AutomationDispatchEnvelope` models; never accept arbitrary runtime project/automation/work-item identity.
- [x] Add one atomic Automation-owned schedule claim that verifies expected due time, creates or resolves one occurrence, creates outbox/reservation or a terminal skipped invocation, applies existing recurring task eligibility/category rules, and compare-and-swap advances the schedule.
- [x] Preserve ordinary non-Automation schedule behavior; coalesce running/reserved Automation occurrences, clear skipped one-time `next_run`, and advance recurring schedules to the first future cadence boundary.
- [x] Add owner-only leased outbox claim/renew/retry/failure behavior with bounded backoff and stable dispatch identity.
- [x] Extend `TaskRepo` with one transactional Automation dispatch claim that validates reservation/lease, atomically claims the existing task, and creates or resolves exactly one execution by `dispatch_id`.
- [x] Add `WorkerService.SubmitPrepared` using existing global/project/model capacity, cancellation, lifecycle, LLM/tool execution, completion, broadcaster, and cleanup semantics; do not add a second worker pool or executor.
- [x] Finalize invocation/outbox/activity state and release reservations from terminal existing-worker completion paths; recover a crash after DB claim without duplicate execution or activity.
- [x] Extend `ThreadInputRepo.ClaimQueuedForTaskExecution` to load durable bindings and upsert Automation activities while retaining existing FIFO, cancellation, retargeting, stale-turn, and guarded-promotion behavior.
- [x] Add canonical idempotent work-item/activity/transition repository operations with transactional position/status projection and stable bounded queries.
- [x] Instrument Native Alert creation, decision, claim/implementation linkage, processing completion/failure, and implementation execution without changing Alert authority or approval boundaries.
- [x] Instrument GitHub issue creation and task/PR linkage using canonical persisted repository-qualified identities and external-call reconciliation rules; never infer provenance from prompts, titles, or issue URLs.
- [x] Preserve one work item across invocations/versions, bind activities to the work item's immutable origin topology, and support several Automation bindings on one shared inbox execution.
- [x] Add a set-based live graph projection with deterministic display-state precedence, concurrent invocation/work-item counters, waiting-human distinction, recent completion cutoff, bounded node resources, and no implicit GitHub reads.
- [x] Add Automation invalidation event types through the existing broadcaster/SSE path and periodic projection reconciliation that never overwrites authoritative Task, Execution, Alert, Goal, Workflow, thread-input, issue, or PR state.
- [x] Add project-scoped live graph and node-resource handler/UI paths with escaped compact summaries, no prompts/outputs/diffs/secrets, periodic visible-page refresh, and direct-load/HTMX behavior.
- [x] Add focused migration, repository concurrency/idempotency, scheduler, dispatcher/worker, thread-input, Alert, GitHub, projection, handler, UI, restart/reconciliation, no-inference, and foreign-project tests mapped to the Phase 2 runbook.
- [x] Run Phase 2 generation/build/tests, perform a fresh contract audit, repair all material findings, update this file with concrete evidence, and create a checkpoint commit before Phase 3.

## Phase 1 Checklist

- [x] Add project-owned Automation definitions with lifecycle and health metadata.
- [x] Add immutable published versions, nodes, edges, definition-resource memberships, and exclusive trigger ownership with composite parent constraints.
- [x] Add migration up/down coverage, project cascade coverage, and required Phase 1 indexes.
- [x] Implement project-scoped repository list/detail queries and atomic registered publication.
- [x] Reject foreign-project resources, unsupported adapter keys, malformed topology bindings, duplicate memberships, and concurrent trigger ownership.
- [x] Keep published definitions immutable and expose no ordinary delete path.
- [x] Add canonical maintained `native_sdlc` and `github_sdlc` adapter templates.
- [x] Add idempotent registration by stable project-scoped key using actual task/schedule IDs only.
- [x] Update maintained Native/GitHub bootstrap flows to register their newly created resources explicitly.
- [x] Render a bounded project-scoped Automations portfolio and Automation Definition detail view.
- [x] Render current persisted task/schedule resource summaries without title, prompt, lineage, Alert, or GitHub inference.
- [x] Allow worker/inbox task memberships across automations while keeping active trigger schedules exclusive.
- [x] Enforce foreign-project isolation in repository, service, handler, and UI paths.
- [x] Add focused repository, service, handler, UI, bootstrap-capability, and no-inference tests.
- [x] Run Phase 1 generation/build/tests, perform a fresh contract audit, repair all material findings, update this file, and checkpoint commit.

## Completed

- Added migration `113_automation_definitions.sql` with definitions, versions, nodes, edges, definition resources, exclusive trigger owners, composite ownership constraints, indexes, and reversible down migration.
- Added Automation domain models, project-scoped repository reads, bounded resource summaries, and one resumable-safe `BEGIN IMMEDIATE` registered-publication transaction.
- Added maintained Native SDLC and GitHub SDLC adapter templates plus stable-key, same-project, actual-resource registration.
- Added selected-bootstrap-skill-only `register_automation_resources` runtime capability to initial task and task-thread execution paths; ordinary tasks do not receive it.
- Updated the maintained Native/GitHub bootstrap skills with canonical node bindings and explicit registration steps.
- Added project-scoped portfolio and read-only SVG Definition views, native task/schedule links, HTMX navigation, generated templ output, and sidebar navigation.
- Added migration, repository/service, concurrency, runtime capability, no-inference, handler, UI, shared-resource, cascade, and foreign-project isolation tests.
- Added migration `114_automation_runtime.sql` with unique execution dispatch identity, invocation/outbox/reservation state, long-lived work items and positions, queued-input bindings, activities/resources, append-only transitions, composite ownership constraints, indexes, and reversible down behavior.
- Added atomic Automation schedule occurrence claims, coalesced skipped occurrences, owner-only outbox leasing/backoff, transactional task/execution claiming, and prepared dispatch submission through the existing `WorkerService` capacity/lifecycle/cancellation/completion pipeline.
- Added restart reconciliation for precreated and submitted executions, terminal dispatch/invocation/activity finalization, exact execution projection repair, reservation cleanup, and ordinary-worker exclusion for Automation-reserved tasks.
- Added server-derived multi-binding context propagation through scheduled execution, child lineage, `send_to_task`, queued-input promotion, and shared inbox execution without adding a second queue or executor.
- Added transactional Native Alert provenance from creation through approval, claim/release/reclaim, implementation linkage, processing, execution, and completion while retaining Alert authority and approval semantics.
- Added repository-qualified GitHub issue/PR identities, pre-call external activity reservation, ambiguous-mutation refusal, exact discovered-issue-to-task binding, issue-to-implementation-to-review transitions, and no prompt/title/URL inference.
- Added set-based live graph counters/edge activity, deterministic display precedence, compatible prior-version mapping plus visible unmapped work, bounded node resources, compact Automation events, direct/HTMX live UI, event debounce, and visible-page periodic refresh.

## Changed Files

- Phase 1 migration/model/repository: `113_automation_definitions.sql`, Automation definition models/repository, adapter/registration services, and migration tests.
- Phase 2 migration/model/repository: `114_automation_runtime.sql`, Automation runtime models/repositories, execution/task/thread-input integration, Alert projection transactions, and migration/runtime tests.
- Runtime/services/wiring: Scheduler ownership routing, prepared Worker submission, dispatcher/reconciler, execution finalization, server-derived context propagation, Alert and GitHub runtime provenance, compact broadcaster events, and server lifecycle wiring.
- UI/handlers: project-scoped live/definition/node-resource handlers, Automation tests, portfolio/live/definition templates and generated output, sidebar navigation/project switching/SSE forwarding.
- Maintained setup guidance and schemas: Native/GitHub bootstrap skill bodies and exact GitHub source-issue fields on the existing `create_task` action.

## Current Decisions

- The feature vision is governed by [VISION.md](./VISION.md).
- The technical contract is governed by [IMPLEMENTATION.md](./IMPLEMENTATION.md).
- Implementation proceeds one phase at a time in the order defined by the runbook.
- Existing OpenVibely domain services and records remain authoritative.
- Backward compatibility is not required: pre-feature resources are never detected or backfilled into Automations.
- Automations appear only through explicit publication or maintained Native/GitHub setup registration.
- The Automations page offers Template, Describe It, and Blank creation paths; it does not offer Register Existing.

## Validation

- Phase 1 `templ generate`, `go build ./cmd/server`, focused Automation/migration tests, and `go test ./internal/... -count=1 -timeout 60s`: passed.
- Phase 2 focused `go test ./internal/database ./internal/repository ./internal/service ./internal/handler -run 'TestMigration114|TestAutomation' -count=1 -timeout 180s`: passed.
- Phase 2 `templ generate` and `go build ./cmd/server`: passed after all audit repairs.
- Phase 2 full `go test ./internal/... -count=1 -timeout 60s`: passed. The first broad attempt found a touched GitHub bootstrap wording assertion; restoring the required sentence while retaining explicit source-issue fields repaired it, and the fresh full rerun passed every package.

## Audit Findings And Repairs

- Repaired a dedicated-connection deadlock by loading the published return snapshot on the publication connection before commit/release.
- Added real successful runtime-action execution coverage after the first audit found only capability-scoping coverage.
- Added simultaneous registration coverage proving one claimant wins an exclusive trigger schedule and the losing publication rolls back completely.
- Rejected `shared` relation metadata for exclusive trigger schedules.
- Made idempotent setup reruns refresh stable Automation display metadata without creating a new version.
- Confirmed there is no Register Existing surface, legacy discovery, title/prompt matching, heuristic inference, migration, confidence scoring, or pre-feature graph backfill.
- Repaired parallel-branch completion so one terminal branch removes only its own position and a work item closes only after all positions, nonterminal activities, Alert claims, and queued inputs are gone.
- Excluded Automation-reserved tasks from ordinary Active-task discovery and `ClaimTask`, preventing Scheduler/Worker races that bypass prepared dispatch ownership.
- Closed stale Native Alert claim activities on release and reclaim, leaving exactly one current claim and preserving correct live counters.
- Replaced brittle duplicate-submit string matching with a typed worker sentinel; reclaimed dispatches already present in the worker queue are acknowledged and remain restart-recoverable instead of consuming failure attempts.
- Added concurrent schedule-poller and shared-task reservation coverage, post-ack reconciler resubmission coverage, actual issue-linked implementation PR coverage, thread-input project-cascade evidence, and direct-load plus HTMX live-page evidence.
- Repaired Automations project switching and normalized the touched shared SSE listener while retaining existing task/chat/file event behavior.
- Restored the established GitHub bootstrap instruction required by catalog tests while keeping exact source issue/repository provenance fields.

## Open Findings Or Blockers

- None for Phases 1-2.

## Remaining Phases

- Phase 3: History, Replay, And Metrics.
- Phase 4: Templates And Visual Builder.

## Exact Next Action

Inspect the current Automation live projection, Analytics/Chart.js patterns, pagination handlers, and persisted transition queries; convert Phase 3 history/replay/metrics/health requirements into an explicit checklist before implementing invocation list/detail, work-item timeline, replay, funnel/duration metrics, and health projections.

## Update Contract

After every implementation phase, record:

- completed requirements and acceptance items;
- changed files and migrations;
- tests and validation results;
- audit findings and repairs;
- unresolved decisions or blockers;
- the exact next phase or action.
