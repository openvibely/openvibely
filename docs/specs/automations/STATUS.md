# Automation Implementation Status

## Current Phase

Phase 2: Invocations, Work Items, And Live Position (not started).

## Status

Phase 1 complete and clean on 2026-07-18.

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

## Changed Files

- Migration/model/repository: `internal/database/migrations/113_automation_definitions.sql`, `internal/models/automation.go`, `internal/repository/automation_repo.go`, and migration tests.
- Services/runtime/wiring: Automation adapter/registration/graph services, lifecycle-selected skill context, LLM bootstrap runtime tools, task-thread runtime composition, handler/server wiring.
- UI: Automation handlers, tests, portfolio/detail templates and generated output, sidebar source and generated output.
- Maintained setup guidance: Native and GitHub autonomous SDLC bootstrap skill bodies.

## Current Decisions

- The feature vision is governed by [VISION.md](./VISION.md).
- The technical contract is governed by [IMPLEMENTATION.md](./IMPLEMENTATION.md).
- Implementation proceeds one phase at a time in the order defined by the runbook.
- Existing OpenVibely domain services and records remain authoritative.
- Backward compatibility is not required: pre-feature resources are never detected or backfilled into Automations.
- Automations appear only through explicit publication or maintained Native/GitHub setup registration.
- The Automations page offers Template, Describe It, and Blank creation paths; it does not offer Register Existing.

## Validation

- `templ generate`: passed.
- `go build ./cmd/server`: passed.
- Focused `go test ./internal/database ./internal/service ./internal/handler -run 'TestMigration113|TestAutomation' -count=1 -timeout 120s`: passed.
- Full Phase 1 `go test ./internal/... -count=1 -timeout 60s`: passed after updating four existing migration-head assertions from version 112 to 113.

## Audit Findings And Repairs

- Repaired a dedicated-connection deadlock by loading the published return snapshot on the publication connection before commit/release.
- Added real successful runtime-action execution coverage after the first audit found only capability-scoping coverage.
- Added simultaneous registration coverage proving one claimant wins an exclusive trigger schedule and the losing publication rolls back completely.
- Rejected `shared` relation metadata for exclusive trigger schedules.
- Made idempotent setup reruns refresh stable Automation display metadata without creating a new version.
- Confirmed there is no Register Existing surface, legacy discovery, title/prompt matching, heuristic inference, migration, confidence scoring, or pre-feature graph backfill.

## Open Findings Or Blockers

- None for Phase 1.

## Remaining Phases

- Phase 2: Invocations, Work Items, And Live Position.
- Phase 3: History, Replay, And Metrics.
- Phase 4: Templates And Visual Builder.

## Exact Next Action

Inspect the current Scheduler, `TaskRepo`, `ThreadInputRepo`, `WorkerService`, Alert, and GitHub tests; convert the Phase 2 requirements and acceptance criteria into an explicit checklist before adding runtime provenance tables.

## Update Contract

After every implementation phase, record:

- completed requirements and acceptance items;
- changed files and migrations;
- tests and validation results;
- audit findings and repairs;
- unresolved decisions or blockers;
- the exact next phase or action.
