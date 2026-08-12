---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-08-12
source: update_memory
source_id: 13e73a7510ce02d1540d8bec24b46855:8717322f46437b40
confidence: high
title: Testing Coverage and Performance
---

OpenVibely has a large Go test suite. Durable quality work should emphasize broad behavioral coverage and production-shaped performance evidence rather than accumulating narrow happy-path tests, raw logs, or task-by-task benchmark notes.

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
- Templates dashboard projection was fixed by selecting bounded card columns and excluding full `default_prompt` except from `GetByID`; preserve the `templateCardColumns`/`scanTemplateCards` pattern.
- Pulse dashboard UpcomingRepo running/pending/scheduled task queries were fixed by projecting bounded prompt previews while preserving task fields, schedule fields, agent-name resolution, ordering, filters, and scheduled-task joins.
- Worktree cleanup projection was fixed for `#288`: `TaskRepo.ListWithWorktrees` selects only worktree-cleanup fields and preserves merged-branch detection, worktree removal, status update, target fallback, descendant-guarded branch deletion, and orphan cleanup.
- Schedule calendar projection was fixed for `#303`: `TaskRepo.ListWithSchedulesByProject()` uses a bounded calendar projection excluding `prompt`, `chain_config`, and `swarm_config` while preserving task/schedule occurrence rendering inputs.
- `#376` is implemented: Personality settings list/HTMX refreshes use compact card projections with bounded prompt previews and lazy edit-detail hydration.
- `#339` is implemented: `idx_automation_transitions_invocation` supports Automation invocation history query plans without temp sorts while preserving indexed work-item performance.
- `#475` is on `main`: stale external PR discovery starts from stale indexed `task_pull_requests.updated_at < cutoff` and preserves deduplicated `(project_id, automation_id)` refresh behavior.
- `#462` implements GitHub Dev Inbox assigned-issue fetch optimization: assigned-issue list entries skip per-issue detail hydration only when explicitly marked complete for task creation; incomplete or unknown-completeness entries hydrate only after repository/issue deduplication and only for that issue.
- Open gap `#269`: `BacklogRepo.GetBacklogTasksForAnalysis` and `GetRecentCompletedTasks` load full prompts even though only short previews are used.
- Open gap `#294`: Insights workflows materialize complete task prompts while retaining only 100-150 character previews.
- Open gap `#300`: Architect Task Plan queries `ListTasksBySession`/`ListTasksByPhase` select unbounded prompts even though the rendered view uses title, phase, priority, and hours.
- Open gap `#314`: `ListAutomationWorkItems` pagination needs created-at ordered indexes for both unfiltered and status-filtered paths while preserving `created_at DESC, id DESC` cursor semantics.
- Open gap `#321`: `TaskRepo.ListSwarmChildren` and `FindSwarmChildByRole` should use bounded projections omitting `prompt` and `chain_config`; full child payloads remain available through `GetByID`.
- Open gap `#327`: Chat context building through `buildChatContext`/`ListByProjectWithCategorySorts` loads full task rows, including large `prompt`, `chain_config`, and `swarm_config`, for every chat message even though only a bounded preview is rendered.
- Open gap `#330`: `AutonomousTriggerService.gatherTaskContext` calls `TaskRepo.ListByProject` three times for active/backlog/completed summaries but only reads title/category/status.
- `#334` Analytics Usage aggregate direction: optimize `llm_usage_events` without persisted localtime bucket columns/triggers. Use project/date-bounded indexed raw `occurred_at` scans and aggregate bucket labels in Go using current `time.Local`, preserving legacy SQLite localtime semantics after timezone changes.
- Open gap `#345`: Skills Analytics aggregate queries over `skill_analytics_events` use computed local-time buckets and temp B-trees; acceptance should add a covering expression/generated-bucket indexing strategy and query-plan regressions while preserving local-time semantics.
- Open gap `#350`: task board refreshes through `ListBoardByProjectWithCategorySorts` still select/parse full `chain_config` and `swarm_config` blobs for every card even though rendered cards need compact chain/swarm metadata.
- Open gap `#355`: Execution Analytics aggregate queries over execution history use date/hour grouping or ordering not covered by current indexes; acceptance should remove temp grouping/ordering sorts while preserving local-time bucket and project-scoping semantics.
- Open gap `#358`: GitHub PR feedback forwarding re-fetches all historical comments/reviews/review comments for every open task PR on unchanged scheduled runs, then filters duplicates after materializing pages.
- Open gap `#364`: Agents page list/render embeds full editable agent bodies in every card. Acceptance should use compact card projections plus lazy edit-detail hydration while preserving edit modal behavior, authorization/project scoping, search semantics, and lifecycle/Automation capability data.
- Runtime `list_agents` (`#416`) should use compact summary projections for web Chat and channel actions while preserving output compatibility, archived exclusion, ordering, project scoping, and full hydration for edit/detail paths. Verify live PR/main state before treating prior local work as shipped.
- Runtime alert listing (`#396`) should use compact alert summary projections for JSON/text/channel `list_alerts`, omitting `body` and `metadata_json` while preserving filtering, pagination, project isolation, and full detail via `get_alert`. Verify live PR/main state before treating prior work as shipped.
- Runtime `list_schedules` discovery (`#412`) uses/should use an ordered schedule discovery index for no-filter first-page requests while preserving filters, pagination, nil-`next_run` ordering, and runtime JSON fields. Verify live publication state before claiming completion.
- Channels Settings webhook cards (`#422`) should use compact card projections plus lazy edit-detail hydration; preserve hydration guards, project scoping, card fields, and migration-number uniqueness. Verify live checks/main state before claiming completion.
- Open gap `#426`: Pattern Library dashboard/search/category list workflows load full `template_text` for compact cards; acceptance should add compact pattern-card projections plus lazy full-template hydration while preserving page-load aggregation, HTMX refresh/search/category/popular/recent paths, ordering, project/library semantics, and full edit/detail behavior.
- Open gap `#444`: Autonomous dashboard Trend Intelligence queries in `trend_repo` should avoid project-scoped temp B-tree sorts and dashboard-hidden blob materialization; acceptance should add order-covering indexes and compact projections while preserving project scoping, ordering, counts, and full detail behavior.
- Open gap `#447`: Architect dashboard/session-template list loading should use compact card projections and order-covering indexes instead of materializing hidden large session/template JSON and forcing temp B-tree sorts.
- Open gap `#457`: Task Changes/code-review rendering rebuilds the same review-comment map inside both inline and split per-file diff loops and lacks an order-covering list index. Acceptance should build the map once per render, add/order-cover list queries where appropriate, and preserve inline/split placement plus refresh behavior.
- Open gap `#465`: Workflow execution history listing loads full execution `context` payloads and sorts with a temp B-tree; acceptance should add compact list/history projection and order-covering index while preserving full detail access, scoping, response fields, ordering, and pagination.
- Chat history indexing/projection (`#481`) adds execution-side chat-history metadata and ordered execution indexes; preserve chronological return order, project/category scoping, cursor/tie-breaker semantics, and active-running lookup behavior. Verify live PR/main state before claiming completion.
- PR `#518` addresses gap `#490`: Automation Live detail `LiveNodeCounts` should use compact `automation_live_activity_states` projection (migration `160`) instead of ranking all `automation_activities` on each detail render. Preserve latest activity per `node_id + state_key`, active invocation fallback, pending thread-input bindings, work-item positions, 24-hour recent completion cutoff, state priority, and `github_inbox` active-position exclusion; `LiveEdgeCounts`, resource summaries, and external state remain separate. Query-plan coverage should avoid full-history `ranked_activities`/temp activity sorting. Migration numbering was corrected to avoid main's lifecycle migration `159`; verify live PR/main state before treating this as shipped.
- PR `#517` addresses gap `#497`: Task Detail Lifecycle tab/API listing should use compact lifecycle execution list columns (`id`, `agent_id`, `when_slot`, `skill_key`, `output_contract`, `status`, `output_json`, `error`, `started_at`, `completed_at`) ordered by `started_at DESC, id DESC` with migration `159` adding `idx_lifecycle_executions_task_started`. Preserve prompt-safe JSON fields, selected skill/memory and task-mode summary extraction, failed-error display, task scoping, unchanged lifecycle-event retrieval, and benchmark/query-plan coverage. Verify live PR/main state before treating this as shipped.
- PR `#515` addresses gap `#504`: Automations portfolio `/automations` should use a compact published-card projection instead of project-wide operational counts plus per-Automation full graph/resource/schedule hydration through `AutomationGraphService.List`. Preserve identity, lifecycle/status/health, adapter/template-update display, project scoping, HTMX refresh behavior, and full detail/live paths. Verify live PR/main state before treating this as shipped.
- Open gap `#513`: Workflow landing/detail API combines unpaginated workflows, templates, and agents while materializing edit-only config/definition/model fields and forcing temp B-tree sorts on list ordering. Evidence from a synthetic production-shaped fixture showed the current `/workflows` response around 8.6 MB versus about 128 KB for a compact projection. Acceptance should add bounded list projections, pagination or payload limits, and order-covering indexes while preserving page-needed JSON fields, project/authorization scoping, detail/edit full-hydration paths, and workflow/template/agent ordering semantics.
- Open gap `#523`: Channels settings webhook agent picker hydrates full `AgentRepo.List` records even though the rendered picker serializes only `{id,name}`. Evidence from 1,000 synthetic large agents showed about `28.38 ms/run` and `40.7 MB allocated/run` for full hydration versus `1.16 ms/run` and `216 KB allocated/run` for a compact `id,name` projection with identical picker JSON size. Acceptance should add a compact picker projection while preserving project scoping, picker ordering, selection behavior, and full agent hydration for edit/detail paths.
- Hosted-Ubuntu dependency-cache benchmark workflow `#228` has strict sample/comparator requirements; do not claim acceptance until actual accepted artifacts exist.

CI and dependency setup:
- `go test ./... -coverprofile=coverage.out -coverpkg=./...` is the primary GitHub Actions coverage/compile gate. It includes `cmd/server`, so do not add a separate default `go build ./cmd/server` step without a distinct documented purpose.
- Preserve generated-file cleanliness checks and templ-generated-file coverage exclusions around the coverage gate.
- The packaged-update CI job runs native `internal/update` tests plus binary and desktop packaged E2E checks across Linux, macOS, and Windows OS/arch rows; Windows ARM is experimental.
- Windows packaged desktop rollback E2E must stop the helper-relaunched restored app before temp-dir cleanup, typically by killing the relaunch port, because a live `openvibely-desktop.exe` locks deletion.
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
- Composer shortcut/modifier browser coverage must exercise Enter idle/active, Meta/Ctrl+Enter idle/active, and Meta/Ctrl-click on Send/Stop with active steering and unavailable/idle steering. Platform coverage must include `navigator.userAgentData.platform === "iOS"` and assert Apple hint/Meta behavior.

Known correctness and coverage gaps:
- Open bug `#448`: Pattern Library `ApplyPattern` with `category=active` bypasses the normal active-task path that checks model availability and submits active tasks to the worker queue.
- Open bug `#443`: Pattern Library `ApplyPattern` treats omitted required form variables as empty strings, allowing tasks with blank substitutions.
- Open bug `#429`: Pattern Library JSON import persists templates without deriving `variables` from `template_text`, so applying imported placeholders can leave raw unresolved template text.
- Open bug `#453`: Pattern Library variable extraction recognizes spaced placeholders like `{{ issue }}`, but apply-time replacement only substitutes tight `{{issue}}` form.
- Open bug `#408`: `parseArchitectJSONFromAI` rejects valid fenced or narrated JSON arrays for Architect task-plan generation because object extraction runs before array extraction and returns the object-unmarshal error immediately.
- `TaskGoalService.ResumeGoalStoppedByUser()` is production-used but lacks dedicated regression coverage. Existing `TestTaskGoalService_UserStopPausePreservesGoalForResume` exercises a different method (`ResumeGoal()`).
- Resolved coverage gap: completed-task Task Detail edit/goal no-rerun regressions assert no worker queue item or running worker state, no lifecycle continuation rows, no schedule-run marker, preserved completed status, and no extra execution rows or `thread_inputs`.
