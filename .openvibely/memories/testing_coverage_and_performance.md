---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-08-09
source: update_memory
source_id: 13e73a7510ce02d1540d8bec24b46855:c1496bd47e1b4691
confidence: high
title: Testing Coverage and Performance
---

OpenVibely has a large Go test suite. Durable quality work should emphasize broad behavioral coverage and production-shaped performance evidence rather than accumulating narrow happy-path tests or task-by-task benchmark notes.

Coverage direction:
- `internal/service` and `internal/handler` are the broadest coverage seams, especially error, pagination, webhook, retry, scheduling, channel, and orchestration paths.
- `internal/service/llm_service.go` needs controlled LLM caller mocks to avoid flaky tests.
- Generated templ `*_templ.go` files are excluded from coverage summaries while template tests still run.
- Prefer broad behavioral coverage over granular tests around already-covered code. Avoid blanket `t.Parallel()` around shared database setup.
- Shared mutable handler databases are unsuitable because tests mutate global/default/list state. `NewTestDB` uses an immutable serialized SQLite template to create fresh isolated in-memory fixtures while preserving UTC location, foreign keys, busy timeout, `MaxOpenConns(1)`, migrated schema, seed data, cleanup, and default-agent behavior.
- Direct runtime-tool/action tests should exercise project ownership, authorization, pagination, mode restrictions, malformed JSON, and provider-tool-incapable paths.

Performance contracts:
- SQLite uses one open connection, so unindexed scans, query fan-out, and long transactions can stall unrelated database-backed requests. Performance work must measure contention as well as isolated query latency.
- High-cardinality deletion paths must retain indexed foreign-key and cleanup-ownership lookups, especially alerts, lifecycle parent references, thread inputs, and attachment sessions. Preserve query-plan assertions for indexed `SEARCH` operations and production-shaped deletion/concurrency fixtures.
- Attachment cleanup tests must cover Clear Chat, queued/immediate publication rollback, direct Chat/task-thread failures, shared-reference retention, durable session retirement, and both upload-first and retirement-first publication-fence ordering.
- List, dashboard, and discovery surfaces should use bounded projections rather than materializing full prompts, bodies, secrets, metadata, or configuration blobs when only compact summaries are rendered.
- `list_tasks` discovery uses a private compact projection. Preserve count/page shape, relevance ordering, filters, project isolation, chat exclusion, pagination, nullable summaries, UTC timestamp serialization, and production-sized query/response/latency evidence.
- Models-page initial rendering uses compact card projections, fetches one authorized full record for Edit, and guards stale edit-detail responses. Preserve production-sized handler benchmarks and lazy secret/edit hydration.
- `ExecutionStreamHub` steady-state publication uses lazily rebuilt immutable subscriber snapshots. Preserve zero-allocation steady-state publishing and concurrent Publish/Unsubscribe/Close coverage.
- Performance changes must preserve ordering, authorization, cancellation, retries, lifecycle behavior, persistence, and projection semantics. Use representative fixtures, query plans, SQL/process/I/O counts, allocations, response sizes, and contention measurements rather than only microbenchmarks.
- Durable cost centers include handler/service tests, streaming parser and chunk reassembly, repeated database/context setup, and DB/git/worktree-heavy services.

Known bounded-projection history and current gaps:
- Templates dashboard projection was fixed by selecting bounded card columns and excluding full `default_prompt` except from `GetByID`; benchmark evidence showed large allocation and transfer reductions. Preserve the `templateCardColumns`/`scanTemplateCards` pattern.
- Pulse dashboard UpcomingRepo running/pending/scheduled task queries were fixed by projecting `SUBSTR(t.prompt, 1, 200)` for UI previews, preserving task fields, schedule fields, agent-name resolution, ordering, filters, and scheduled-task joins.
- Open gap `#269`: `BacklogRepo.GetBacklogTasksForAnalysis` and `GetRecentCompletedTasks` load full prompts even though only short previews are used.
- Worktree cleanup projection was fixed for `#288`: `TaskRepo.ListWithWorktrees` now selects only IDs, project ID, status, worktree path/branch, merge target branch, and merge status for `WorktreeService.CleanupMergedWorktrees`; tests cover populated and empty merge-target cleanup decisions plus non-terminal descendant branch preservation, with benchmark evidence on 300 tasks carrying 32KiB prompt/config blobs.
- Schedule calendar projection was fixed for `#303`: `TaskRepo.ListWithSchedulesByProject()` now uses a bounded calendar projection that excludes `prompt`, `chain_config`, and `swarm_config` while preserving task title/id, schedule metadata, disabled/repeat state, occurrence placement inputs, and automation schedule name overrides. Regression coverage asserts the unbounded fields are not populated, and repository benchmarks compare the old full-row shape against 20x512B and 300x32KiB fixtures.
- Open gap `#294`: Insights workflows materialize complete task prompts while retaining only 100-150 character previews.
- Open gap `#300`: Architect Task Plan queries `ListTasksBySession`/`ListTasksByPhase` select unbounded prompts even though the rendered view uses title, phase, priority, and hours.
- Open gap `#327`: Chat context building through `buildChatContext`/`ListByProjectWithCategorySorts` loads full task rows, including large `prompt`, `chain_config`, and `swarm_config` blobs, for every chat message even though only a bounded preview is rendered. Benchmark evidence at 300 tasks with 32KiB payloads showed about 17x latency and 41x memory overhead compared with a bounded projection.
- Open gap `#330`: `AutonomousTriggerService.gatherTaskContext` calls `TaskRepo.ListByProject` three times for active/backlog/completed task summaries but only reads title/category/status; full rows include `prompt`, `chain_config`, and `swarm_config`. Benchmark evidence at 300 tasks with 32KiB prompt and 4KiB chain config showed about 91x latency and 124x memory overhead compared with a bounded projection.
- Open gap `#350`: task board refreshes through `ListBoardByProjectWithCategorySorts` still select and parse full `chain_config` and `swarm_config` blobs for every card even though the rendered board only needs compact chain-enabled and swarm role/status metadata. Temporary repository benchmark evidence at 500 task cards carrying 32KiB chain configs and 32KiB swarm configs showed the current projection at about 80-83ms/op and 77.7MB/op versus a compact projection at about 13.8ms/op and 2.8MB/op with identical rendered response size.
- Open gap `#334`: Analytics Usage page aggregate queries over `llm_usage_events` (`GetDailyUsage`, `GetUsageRateBuckets`, `GetModelUsageBreakdown`, and related usage endpoints) use computed `date(occurred_at, 'localtime')` grouping not covered by the existing `(project_id, occurred_at)` index, causing SQLite `USE TEMP B-TREE FOR GROUP BY` on all six aggregate queries. Benchmark evidence at 50k usage rows / 3 projects showed a simulated full Usage page load at about 169ms/op; acceptance should add a covering expression/generated-bucket indexing strategy and query-plan regressions that remove temp GROUP BY sorts while preserving local-time bucket semantics.
- Open gap `#345`: Skills Analytics dashboard aggregate queries (`GetUsageOverTime`, `GetTopSkills`, `GetAgentUsage`, `GetSelectionFollowThrough`) use the same computed local-time bucket pattern over `skill_analytics_events`, so the existing `(project_id, created_at)` index does not cover grouping/ordering and SQLite uses temp B-trees for `GROUP BY`/`ORDER BY`. Benchmark evidence at 50k skill analytics events showed `GetUsageOverTime` at about 102ms/op; acceptance should add a covering expression/generated-bucket indexing strategy and query-plan regressions that remove temp sorts while preserving local-time bucket semantics.
- Open gap `#355`: Execution Analytics dashboard aggregate queries over execution history for success/failure rates, hourly trends, status breakdowns, and related endpoints use date/hour grouping or ordering shapes not covered by the current task/status/started-at indexes, causing SQLite temp B-trees while the Analytics page starts seven execution-history requests concurrently. Temporary migrated-DB evidence at 50k executions for one project showed about 116 ms median for success/failure rates and about 84 ms for hourly trends before browser rendering; acceptance should add covering expression/generated-bucket indexing or pre-aggregation with query-plan regressions that remove temp grouping/ordering sorts while preserving local-time bucket and project-scoping semantics.
- Open gap `#339`: invocation-scoped Automation transition history queries lack a covering `(automation_id, invocation_id, occurred_at)`-style index, causing a full automation-scoped scan and temp B-tree sort when filtering transition history for one invocation. Benchmark evidence at 500 invocations × 20 transitions (10k rows) showed about 1.5ms/op versus about 49µs/op for the equivalent indexed work-item-scoped path, about a 31x latency gap. Acceptance should add the covering index and query-plan regressions that remove the scan/temp sort while preserving transition ordering and scoping semantics.
- Local `#314` work-item history query performance implementation adds created-at ordered indexes for both unfiltered and status-filtered `ListAutomationWorkItems` pagination while preserving `created_at DESC, id DESC` cursor semantics. Regression coverage asserts indexed query plans without `USE TEMP B-TREE FOR ORDER BY`; benchmark evidence at 10k rows showed unfiltered temp sort ~7.8ms/op vs indexed ~0.056ms/op and status-filtered temp sort ~1.1ms/op vs indexed ~0.058ms/op. The work-item-history index migration was renumbered to goose version `148` on 2026-08-08 after `147` was occupied by GitHub issue task provenance.
- Local implementation for `#321` on 2026-08-08 introduced bounded swarm-child projections in `TaskRepo.ListSwarmChildren` and `FindSwarmChildByRole` that omit `prompt` and `chain_config` while preserving orchestration/rendering metadata; full child payloads remain available through `GetByID`. Regression tests assert the query projection and field preservation, and repository benchmarks compare the old full-row shape against 10x512B/256B and 50x32KiB/4KiB fixtures. A follow-up fix on 2026-08-08 addressed the audited preservation bug by reloading full worker/planner rows before full-row updates in swarm follow-up planner handling; service regressions cover existing `chain_config` preservation for planner parent follow-up and worker rerun updates.
- Hosted-Ubuntu dependency-cache benchmark workflow `#228` has strict sample/comparator requirements; do not claim acceptance until actual accepted artifacts exist.

CI and dependency setup:
- `go test ./... -coverprofile=coverage.out -coverpkg=./...` is the primary GitHub Actions coverage/compile gate. It includes `cmd/server`, so do not add a separate default `go build ./cmd/server` step without a distinct documented purpose.
- Preserve generated-file cleanliness checks and templ-generated-file coverage exclusions around the coverage gate.
- The packaged-update native CI matrix intentionally runs macOS and Windows only; aggregate Ubuntu `go test ./...` already covers `internal/update` on Linux.
- GitHub Actions caches Linux desktop APT archives plus a one-day metadata cache. Cache keys retain runner identity, architecture, and package-manifest digest; action references are pinned to full commit SHAs.
- Cache restores are `continue-on-error`; any non-success restore is untrusted and forces normal `apt-get update`/install. Valid cache hits preflight cached lists and use `apt-get install --no-download`; cold/stale/invalid/partial/failed restores recover normally.
- Production workflow retains machine-readable dependency-setup and complete-job timing artifacts, including cache restore outcomes, metadata validation, APT operations, archive metrics, runner identity, and benchmark labels.

Validation conventions:
- Prefer `make test`, `make test-cover`, or `go test ./... -count=1 -timeout 120s` for authoritative full validation. Raw 60-second full-suite runs can time out under load.
- Use readiness polling instead of fixed sleeps. Timing-sensitive tests may use `testing.Short()` guards.
- Distinguish repeatable touched-scope regressions from environmental failures such as desktop/config PATH, macOS Wails linker warnings, SQLite locks, date-sensitive schedules, and plugin marketplace network timeouts.
- `TMPDIR=/private/tmp` is the established fallback when default-temp plugin cloning hangs.
- Real-browser coverage is required when correctness depends on HTMX history/restoration, streaming offsets, transcript scrolling/hydration, drafts, pending attachments, stale-response races, live-refresh ordering, drag/drop, keyboard shortcuts, or other DOM timing. Handler HTML inspection alone is insufficient.
- Browser performance fixtures should run over localhost HTTP rather than `file://`, and harnesses should expose an explicit DOM pass/fail marker because headless Chrome may leave its parent process alive.
- External HTTP retry tests inject clock/backoff hooks so retries run without real sleeps; provider-specific tests cover classification, replay safety, and metadata preservation.
- Handler `NewTestContext` enables local repo paths by default; tests requiring disabled local paths must call `SetLocalRepoPathEnabled(false)`.
- Chat mode selector tests use `chat-select-change` custom events with `e.detail.value`, not native select `change` semantics.
- Malformed-JSON logs in service error-path tests are expected unless an assertion or package also fails.
- Concurrency tests should assert final/current observable state rather than requiring notification delivery to precede operation return.
- Composer shortcut/modifier browser coverage must exercise every state combination: Enter idle/active; Meta/Ctrl+Enter idle and active; Meta/Ctrl-click on Send/Stop with active steering and unavailable/idle steering. Platform coverage must include `navigator.userAgentData.platform === "iOS"` and assert Apple hint/Meta behavior.

Known coverage gap:
- `TaskGoalService.ResumeGoalStoppedByUser()` is production-used but lacks dedicated regression coverage. Existing `TestTaskGoalService_UserStopPausePreservesGoalForResume` exercises a different method (`ResumeGoal()`), so task resumption can regress silently.
- Resolved coverage gap (2026-08-08): completed-task Task Detail edit/goal no-rerun regressions now assert no worker queue item or running worker state, no lifecycle continuation rows, and no schedule-run marker, in addition to preserving completed status and checking no extra execution rows or `thread_inputs`.
