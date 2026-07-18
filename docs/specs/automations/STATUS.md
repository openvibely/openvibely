# Automation Implementation Status

## Current Phase

Complete: Phases 1-4 and final Definition of Done audit.

## Status

Phases 1-3 are complete at checkpoints `6ff0e9a`, `2a8f511`, and `24286c1`. Phase 4 implementation, phase validation, repository-wide validation, and the concrete Definition of Done audit completed on 2026-07-18; this ledger is part of the final Phase 4 checkpoint candidate.

## Phase 4 Checklist

- [x] Add publication attempts, publication steps, and Chat confirmation receipts with composite Automation/version ownership, idempotent attempt/step keys, transactional receipt consumption, indexes, project cascade, and migration up/down coverage.
- [x] Add a strict versioned structured draft candidate/schema with bounded known fields; reject generated database/project IDs, URLs, executable code, SQL, arbitrary tools, unknown configuration, duplicate keys, dangling/cross-version edges, unsupported cycles, and oversized graphs/configuration.
- [x] Add canonical Native SDLC, GitHub SDLC, and Vision Driver templates accepted by registered adapters; every publishable topology must map to one adapter and no generic Automation edge interpreter may be introduced.
- [x] Build a bounded, deterministic, project-scoped capability snapshot for agents, skills, integrations, source files, and reusable resources without credentials, prompts, worktree contents, private messages, provider identity, or unbounded listings.
- [x] Normalize Template, Describe It, Blank, fixed Chat candidate, and active-version clone inputs through one deterministic draft service with server-generated IDs/layout and shared validation.
- [x] Use the provider-neutral existing no-tools model and usage path for described generation, strict JSON output, and at most one bounded repair attempt; preview must remain ephemeral and draft creation must persist only definition data.
- [x] Persist mutable draft versions/nodes/edges while keeping published topology immutable; clone active versions into new drafts and retain all historical invocation/work-item version references.
- [x] Add constrained schema-driven node/edge editing and validation with explicit affected-node and summary errors; saving or validating a draft must create no task, schedule, Alert, issue, execution, goal, Workflow, or PR.
- [x] Build exact canonical publication plans with create/reuse/update/disable/unchanged effects and SHA-256 revisions over normalized topology, adapter/compiler versions, dependencies, integrations, and ordered effects while excluding volatile runtime state.
- [x] Add golden canonical-plan revision fixtures and reject non-finite/unknown values, changed drafts/dependencies, stale revisions, and unsupported topologies before publication mutation.
- [x] Add a resumable idempotent compiler journal that persists each step before mutation, reconciles pending/running/ambiguous/failed steps by stable target key, uses existing Task/TaskRepo, ScheduleRepo cadence, Alert/goal/Workflow/GitHub boundaries, and never duplicates runtime resources after retries/crashes.
- [x] Publish only after every required resource and membership exists; failed publication must keep the draft unpublished and prior version active while returning exact created/reused resources.
- [x] Add safe pause/resume/archive behavior that disables/enables only exclusively owned trigger schedules, retains paused ownership, never disables shared worker/inbox resources, and preserves all history.
- [x] Add host-issued 30-minute signed Chat confirmation receipts bound to project, automation, version, plan revision, principal, thread, stored plan message, and a later affirmative user input; reject same-turn, expired, stale, cross-scope, ambiguous, negative, and replayed confirmations.
- [x] Register `DomainAutomations` plus `preview_automation_description`, `create_automation_draft`, `plan_automation_publication`, and `publish_automation_draft` in the canonical Chat registry with project-aware surface/mode policies and the existing typed handler map; ordinary task agents receive no definition mutation capability.
- [x] Ensure preview/planning do not persist definitions/runtime resources or invoke ordinary mutation actions; draft creation persists no runtime resources; publish reports success only after resources and published version are durable.
- [x] Add Automations-page `New Automation` with Template, Describe It, and Blank paths only, draft graph/editor, assumptions/warnings/errors, publish preview, explicit web confirmation, failure preservation, and success navigation to Live.
- [x] Add direct-load/HTMX-safe builder behavior, accessible keyboard/status labels, responsive/mobile/Wails layouts, reduced-motion behavior, bounded payloads, and generated templ output.
- [x] Add migration/repository/service/compiler/handler/Chat registry/executor/UI tests for all draft sources, deterministic normalization, no-mutation boundaries, stale/failed/idempotent publication, confirmation attacks, project isolation, adapter ownership, lifecycle safety, and page/Chat parity using one fixed candidate.
- [x] Run Phase 4 and full validation chains, perform a fresh phase and Definition of Done audit against concrete code/tests, repair every material finding, update this file and managed memory, and create the final checkpoint.

## Phase 3 Checklist

- [x] Add project-scoped, bounded invocation history ordered by effective occurrence time `COALESCE(scheduled_for, started_at, created_at), id` with an opaque stable cursor and a maximum initial page of 50.
- [x] Add project-scoped, bounded work-item history ordered by `(created_at, id)`, optional status filtering, and an opaque stable cursor.
- [x] Add project-scoped invocation graph detail that includes only activities and transitions carrying the selected invocation ID while retaining the invocation's immutable version topology.
- [x] Add project-scoped work-item history spanning every related invocation, with bounded activities and stable cursor-paginated transitions ordered by `(occurred_at, id)`.
- [x] Add transition replay frames derived solely from persisted append-only transitions; do not query current Task, Alert, execution, issue, or PR state to reconstruct historical positions.
- [x] Define funnel conversion as persisted `entered` transitions at each version node divided by the first topology node with a persisted entry, and define node duration as the elapsed time from the first persisted transition into a node to the next persisted transition for that work item.
- [x] Add bounded set-based funnel, average-duration, recent-failure, and bottleneck summaries with no per-node or per-work-item query loop.
- [x] Compute and persist Automation health independently from lifecycle: unknown before a completed invocation, healthy after successful trigger/dispatch with no active systemic condition, degraded for isolated recent failures/blocked work, and unhealthy for repeated trigger/dispatch failures.
- [x] Render History navigation, invocation selection, cross-invocation work-item selection, replay controls/timeline, failure and bottleneck summaries, and Chart.js funnel/duration charts with direct-load and HTMX-safe initialization/cleanup.
- [x] Keep all payloads compact and escaped; omit prompts, model output, diffs, Alert bodies, credentials, and provider identity metadata.
- [x] Reject foreign-project automation/invocation/work-item IDs and invalid/tampered cursors according to existing handler conventions.
- [x] Add repository/service/handler/UI tests for stable pagination under ties, occurrence isolation, cross-invocation lifetime, persisted-transition replay, defined metric boundaries, health/lifecycle independence, empty/partial states, and project isolation.
- [x] Run Phase 3 generation/build/tests, perform a fresh contract audit, repair all material findings, update this file with concrete evidence, and create a checkpoint commit before Phase 4.

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
- Added bounded project-scoped invocation, work-item, activity, and transition history with collection/filter-bound opaque cursors and stable tie ordering.
- Added immutable-version invocation graphs, set-based touched-node projection, cross-invocation work-item timelines, and deterministic replay frames derived only from append-only persisted transitions.
- Added set-based funnel, first-arrival-to-next-transition duration, recent-failure, and current bottleneck metrics plus persisted health evaluation independent from lifecycle state.
- Added direct-load and HTMX History, invocation, and work-item views with Chart.js lifecycle cleanup, activity/transition paging, replay controls, compact escaped payloads, empty/partial states, and project isolation.
- Added migration `115_automation_publication.sql` with draft metadata, leased publication attempts, ordered idempotent steps, host confirmation receipts/input markers, composite ownership, indexes, project cascade, and reversible down behavior.
- Added one strict deterministic draft schema/service shared by Template, Describe It, Blank, fixed Chat candidates, and published-version cloning, with canonical Native/GitHub/Vision templates, server layout, bounded capability snapshots, no-tools model generation, one repair, and unsafe/oversized/non-finite rejection.
- Added mutable draft persistence and constrained builder editing without runtime mutation, immutable published topology retention, exact canonical publication plans and golden revisions, stale draft/dependency protection, and volatile-state exclusion.
- Added leased resumable compilation through existing TaskService/TaskRepo and ScheduleRepo behavior, disabled-until-published trigger creation, stable target reconciliation, partial-resource evidence, atomic membership/ownership publication, prior-version preservation, and immediate Live topology switching.
- Added safe pause/resume/archive semantics that touch only exclusive trigger owners, preserve configured-disabled triggers, retain paused ownership, release archived ownership, and leave shared worker/inbox resources untouched.
- Added 30-minute host-signed Chat/web confirmation receipts, durable post-plan issuance, exact later-command marking, scope/expiry/replay enforcement, and canonical registry/executor actions for preview, draft, plan, and confirmed publish.
- Added Automations-page Template, Describe It, and Blank paths only, generation progress/errors, draft graph/editor, assumptions/warnings, explicit publication preview/confirmation, partial-failure preservation, HTMX/direct-load behavior, and successful Live navigation.
- Kept maintained setup registration explicitly restricted to Native/GitHub even though Vision Driver is a registered draft-publication adapter; no Register Existing, detection, inference, migration, scoring, or backfill path exists.

## Changed Files

- Phase 1 migration/model/repository: `113_automation_definitions.sql`, Automation definition models/repository, adapter/registration services, and migration tests.
- Phase 2 migration/model/repository: `114_automation_runtime.sql`, Automation runtime models/repositories, execution/task/thread-input integration, Alert projection transactions, and migration/runtime tests.
- Runtime/services/wiring: Scheduler ownership routing, prepared Worker submission, dispatcher/reconciler, execution finalization, server-derived context propagation, Alert and GitHub runtime provenance, compact broadcaster events, and server lifecycle wiring.
- UI/handlers: project-scoped live/definition/node-resource handlers, Automation tests, portfolio/live/definition templates and generated output, sidebar navigation/project switching/SSE forwarding.
- Phase 3 history/replay/metrics: Automation history models and repository, graph-service history methods, reconciler health evaluation, History/invocation/work-item handlers and routes, History/replay/Chart.js templates, generated output, and focused service/handler tests.
- Phase 4 definition/publication: migration 115, draft/publication/confirmation models and repositories, capability/draft/generation/planner/compiler/confirmation/lifecycle services, canonical Chat registry/executor wiring, builder/lifecycle handlers and routes, server wiring, task/schedule/agent bounded repository extensions, Automations templates/generated output, and migration/service/handler/registry regressions.
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
- Phase 3 focused `go test ./internal/service -run 'TestAutomationHistory' -count=1 -timeout 120s` and `go test ./internal/handler -run 'TestAutomationPagesRenderRegisteredDefinitionsAndEnforceProject' -count=1 -timeout 120s`: passed after audit repairs.
- Phase 3 `go test ./internal/repository ./internal/service ./internal/handler -run 'TestAutomation' -count=1 -timeout 180s`: passed.
- Phase 3 final `templ generate`, `go build ./cmd/server`, and `go test ./internal/... -count=1 -timeout 60s`: passed every package; `internal/handler` completed in 40.104s and `internal/service` in 42.678s.
- Phase 4 focused `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol -run 'TestMigration115|TestAutomation' -count=1 -timeout 180s`: passed.
- Phase 4 final `templ generate`, `go build ./cmd/server`, and `go test ./internal/... -count=1 -timeout 60s`: passed every package; `internal/handler` completed in 41.289s and `internal/service` in 44.097s.
- Final checkpoint revalidation after interrupted response: `templ generate`, `go build ./cmd/server`, focused `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol -run 'TestMigration115|TestAutomation' -count=1 -timeout 180s`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`: passed every package. The macOS desktop linker emitted only the documented newer-SDK warning and exited successfully.

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
- Repaired health evaluation so stale historical failures do not keep an automation degraded after newer successful terminal invocations; health now uses the latest three terminal results plus current blocked/failed positions without changing lifecycle.
- Repaired aggregate failure timestamp scanning from SQLite text and added recent-failure plus waiting-bottleneck assertions.
- Repaired duration metrics to start at the first persisted arrival of any state, so waiting/blocked human-gate time is measured until the next work-item transition.
- Reduced replay JSON to state, occurrence time, and positions so transition metadata, event keys, and internal resource identifiers do not enter the browser DOM.
- Added stable tie-pagination and cursor-binding coverage for invocations, work items, activities, and transitions; invalid cursors/status filters now return controlled 400 responses.
- Decoupled invocation graph highlighting from paginated rows through one bounded set-based touched-node query and exposed independent activity pagination for invocation/work-item history.
- Added direct-load and HTMX assertions for all three History surfaces, Chart.js cleanup, replay controls, empty states, compact payloads, and foreign-project 404 isolation.
- Repaired publication concurrency by acquiring the existing durable attempt lease before compiler effects; concurrent compilers now serialize and retries reuse the completed attempt.
- Preserved already-journaled resource IDs on ambiguous status updates and reconciled committed tasks by stable compiler identity, so partial failures report exact visible resources and retry without duplication.
- Repaired lifecycle resume to restore each published trigger's configured enabled state instead of enabling every owned schedule.
- Added failed replacement evidence proving the prior published version and trigger remain active, then verified successful retry creates one new cadence-specific trigger, disables/releases the superseded trigger, and reuses the task.
- Expanded canonical plan dependencies to include GitHub inbox enabled state, added GitHub and Vision golden hashes, and proved layout/messages/next-run/last-run do not change a revision while compilation configuration does.
- Enforced one shared 64 KiB candidate bound for raw and in-memory/page inputs, rejected non-finite JSON plus URL/executable/SQL values, and retained strict unknown-field/topology/config rejection.
- Added independent tampered-token, project, version, principal, thread, same-turn, ambiguous-command, unmarked-input, replay, and expiry confirmation assertions.
- Added end-to-end canonical runtime execution of all four Automation Chat actions through the shared handler/executor, including no-tools preview, draft-only persistence, durably stored plan, host-marked later confirmation, and truthful durable publication.
- Added visible described-generation progress and in-page validation failure rendering, project-scoped draft/plan/publish 404 isolation, and partial publication resource rendering.
- Repaired the maintained registration boundary after Vision Driver joined the shared adapter registry: registration remains Native/GitHub-only, while Vision is available only through explicit draft publication.
- Reconfirmed no generic edge interpreter or parallel task/worker/queue/Workflow/Alert/GitHub system was introduced.

## Definition Of Done Audit

1. Multiple project graphs: `TestAutomationRegistrationExplicitIdentityAndIsolation` and portfolio handler assertions cover multiple independent cards.
2. Explicit Native/GitHub registration: maintained adapter/runtime tests use actual IDs and reject Vision/custom registration; no title-derived identity exists.
3. Concurrent Live states: `TestAutomationLiveDisplayStatePrecedencePreservesMixedCounters` and overlapping-invocation tests cover running, waiting-human, blocked, failed, and recent completion.
4. Resource drill-down: Automation page and node-resource handler tests link compact native/GitHub resources to their authoritative surfaces.
5. Reload/restart durability: atomic dispatch, acknowledged-prepared-execution, and terminal reconciliation tests rebuild exact persisted positions.
6. Immutable historical versions: clone, invocation-history, work-item lifetime, and replay tests retain origin/version topology.
7. Shared provenance: shared-inbox, multi-binding, old-work-item/new-invocation, child, and thread-input tests preserve causal identity.
8. Project isolation: definition/live/history/draft/plan/publish plus composite-constraint and foreign-ID tests reject cross-project access.
9. Human boundaries: Native Alert and GitHub assignment/PR provenance tests preserve existing approval, review, merge, release, and deployment authorization.
10. Existing primitives: Scheduler, TaskRepo, ThreadInputRepo, WorkerService, AlertService, goal/lineage, GitHub linkage, and broadcaster paths are extended; no hidden engine exists.
11. Shared definition model: all templates and builder publication persist the same versions/nodes/edges used by Live and History.
12. Explicit creation paths: builder tests expose only Template, Describe It, and Blank; maintained registration remains Native/GitHub-only.
13. Chat creation/confirmation: canonical runtime and confirmation attack tests cover persisted draft, visible plan, later exact command, and activation.
14. Page/Chat parity: fixed-candidate handler assertions and shared draft/planner/compiler services prove identical normalization/validation/publication behavior.
15. Registered topology ownership: all three templates validate through registered adapters; arbitrary nodes, edges, conditions, cycles/topologies, tools, code, SQL, URLs, and unknown config are rejected.
16. Idempotency: confirmation replay, leased publication, ambiguous task recovery, schedule journaling, trigger ownership, dispatch, work-item/activity/event keys, and concurrent polling tests cover retries/crashes.
17. Validation breadth: migration, repository, service, handler, Chat registry/executor, runtime, SSE/live UI, accessibility/status, restart, generated-template, desktop, and full repository suites pass.

## Post-Completion UI Repairs

- Replaced unsupported SVG `fill-*`/`stroke-*` theme utilities with Automation graph classes backed directly by DaisyUI theme variables across Live, Definition, invocation, replay, and draft graphs. Dark-mode node surfaces and labels no longer fall back to black-on-black, and status colors remain non-color-labeled and keyboard-focusable.
- Repaired Automation navigation after HTMX history restoration: portfolio cards, `New Automation`, and every `← Automations` control now use the shared `openVibelyNavigate` history owner, while draft create/clone responses push the canonical persisted draft URL instead of caching draft DOM under the source page URL.
- Added handler assertions for theme-safe SVG markup and canonical draft `HX-Push-Url`, plus `TestAutomationGraphThemeAndHistoryNavigationInChrome`, which verifies computed graph contrast, browser Back restoration, in-page return, and repeated card navigation using production HTMX 2.0.4.
- Validation passed: `templ generate`, `go build ./cmd/server`, focused Automation handler tests, the Chrome navigation regression, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`.

## Open Findings Or Blockers

- No code, validation, or Definition of Done blockers remain.
- The selected managed-memory surface is read-only in this task runtime; this repository status ledger contains the current authoritative completion evidence.

## Remaining Phases

- None.

## Exact Next Action

Verify the final Phase 4 checkpoint commit and clean worktree, then allow Goal Agent to evaluate the persisted goal against the concrete implementation and validation evidence.

## Update Contract

After every implementation phase, record:

- completed requirements and acceptance items;
- changed files and migrations;
- tests and validation results;
- audit findings and repairs;
- unresolved decisions or blockers;
- the exact next phase or action.
