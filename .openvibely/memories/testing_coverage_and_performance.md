---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-08-23
source: consolidation
source_id: memory_consolidation_2026_08_23
confidence: high
title: Testing Coverage and Performance
---

OpenVibely has a large Go test suite. Durable quality work should emphasize broad behavioral coverage and production-shaped performance evidence rather than narrow happy paths, raw logs, or task-by-task benchmark notes.

Coverage direction:
- `internal/service` and `internal/handler` are the broadest coverage seams, especially error, pagination, webhook, retry, scheduling, channel, and orchestration paths.
- `internal/service/llm_service.go` needs controlled LLM caller mocks to avoid flaky tests.
- Generated templ `*_templ.go` files are excluded from coverage summaries while template tests still run.
- Prefer broad behavioral coverage over granular tests around already-covered code. Avoid blanket `t.Parallel()` around shared database setup.
- Codecov acceptance uses the hosted report's `line_coverage` denominator for the exact commit. Local cover-profile percentages are not sufficient, and configuration/profile workarounds are not acceptable.
- `NewTestDB` creates fresh isolated in-memory fixtures from an immutable serialized SQLite template while preserving UTC location, foreign keys, busy timeout, `MaxOpenConns(1)`, migrated schema, seed data, cleanup, and default-agent behavior.
- Direct runtime-tool/action tests should cover project ownership, authorization, pagination, mode restrictions, malformed JSON, and provider-tool-incapable paths.

Performance contracts:
- SQLite uses one open connection, so unindexed scans, query fan-out, and long transactions can stall unrelated database-backed requests. Measure contention as well as isolated query latency.
- High-cardinality deletion paths retain indexed foreign-key and cleanup-ownership lookups for alerts, lifecycle parent references, thread inputs, and attachment sessions. Preserve query-plan assertions for indexed `SEARCH` operations and production-shaped deletion/concurrency fixtures.
- Attachment cleanup tests cover Clear Chat, queued/immediate publication rollback, direct Chat/task-thread failures, shared-reference retention, durable session retirement, and both upload-first and retirement-first publication-fence ordering.
- List, dashboard, and discovery surfaces use bounded projections instead of materializing prompts, bodies, secrets, metadata, or configuration blobs when only compact summaries are rendered.
- `list_tasks` tests preserve count/page shape, relevance ordering, normalized filter echoing, exhausted-empty notes, project isolation, chat exclusion, pagination, nullable summaries, UTC serialization, duplicate empty-query guarding, and production-sized query/response evidence.
- Model availability checks use `SELECT EXISTS` rather than hydrating full model lists on hot task/chat gating paths.
- Update-drain pending queued-count queries use a sparse partial index while preserving `drain.queued_total` and promotion ordering.
- Shared page-shell project fallback uses compact selector rows (`id`, `name`, `is_default`) with a covering selector-order index; full project hydration remains for management/API/runtime paths.
- `ExecutionStreamHub` steady-state publication uses lazily rebuilt immutable subscriber snapshots. Preserve zero-allocation steady-state publishing and concurrent Publish/Unsubscribe/Close coverage.
- Performance changes must preserve ordering, authorization, cancellation, retries, lifecycle behavior, persistence, and projection semantics. Use representative fixtures, query plans, SQL/process/I/O counts, allocations, response sizes, and contention measurements.
- Durable cost centers include handler/service tests, streaming parser and chunk reassembly, repeated database/context setup, and DB/git/worktree-heavy services.

Implemented bounded/projection paths to preserve:
- Templates, Pulse Upcoming, worktree cleanup, Schedule calendar, Personality cards, Automation transition indexes, stale external PR discovery, GitHub Dev Inbox assigned-issue discovery, Chat model selection, Agent dialog model pickers, standalone Skills, Alerts, task-detail metrics, Reflection recent executions, channel target lookup, and task-turn skill-catalog enabled-state filtering use compact projections where only summaries are needed.
- Runtime `list_models`, `get_model`, and `view_settings` use compact model summaries; runtime `view_usage_analytics` uses compact local-only account/usage data without credentials or dashboard-only aggregates.
- Worker model-capacity paths use compact model rows and project-capacity counts use a category/status/project index.
- API Chat status polling uses compact execution status plus one batched non-Chat task-ID lookup while preserving marker order, duplicates, queued/promoted state, and Chat-task exclusion.
- Startup attachment reconciliation uses unordered path reads for set building and preserves task/chat upload-root boundaries.
- Browser and channel `create_task` model selection uses compact rows containing only the fields needed for fallback, explicit selection, and `auto_start_tasks` behavior.
- Chat queued-input recovery uses a sparse `scope`/`input_status`/`input_mode`/`project_id` keyset path and stable project paging.
- Browser Chat prompt-context model loading uses compact selection options after the full execution model is selected.
- Reflection changed-file scanning validates the complete JSON array before emitting filenames, so malformed rows contribute no file/type counts.
- Task-thread polling omits large prompt/chain/swarm blobs while full detail and history paths retain full hydration.
- Schedule page model dropdowns use compact badge options; execution, editing, credentials, and persistence retain full model loading.
- Multipart uploads use endpoint-aware caps and a raw stream limiter that handles split headers, invalid boundary-looking payload prefixes, and Go-compatible delimiter whitespace without rejecting valid multi-file requests.
- Completed/error transcript bubbles use static raw-content markup and one shared hydrator rather than repeated inline scripts.
- GitHub PR publication batches tracked mode lookup, skips untracked symlinks, and preserves deterministic mode/order behavior.
- Read-only `list_channels` uses aggregate authorization/target summaries while Settings and routing paths retain detail projections.

Open performance gaps:
- Backlog, Insights, Architect, Automation work-item/history/portfolio, swarm-child, Chat-context, autonomous-trigger, Skill Analytics, plugin-state, agent-owned-skill, scoped-file, task-board, execution-analytics, PR-feedback, code-review, Automation Live, Email receipt, task-detail execution-history, Models refresh, Channels settings, and Automation history paths still have the bounded-projection or indexing gaps tracked by issues `#269`, `#294`, `#300`, `#314`, `#321`, `#327`, `#330`, `#345`, `#350`, `#355`, `#358`, `#457`, `#490`, `#504`, `#529`, `#541`, `#546`, `#555`, `#565`, `#572`, `#594`, `#623`, `#668`, `#840`.
- Hosted-Ubuntu dependency-cache workflow `#228` has strict sample/comparator requirements; do not claim acceptance until accepted artifacts exist.

CI and dependency setup:
- `OPENVIBELY_SKIP_BROWSER_PERF=1 go test ./... -count=1 -timeout 240s -coverpkg=./... -coverprofile=coverage.txt` is the primary GitHub Actions coverage/compile gate and includes `cmd/server`; do not add a separate default server build without a distinct purpose.
- Preserve generated-file cleanliness checks and templ-generated-file coverage exclusions around the coverage gate.
- Packaged-update CI runs native `internal/update` tests plus binary and desktop packaged E2E checks across Linux, macOS, and Windows OS/arch rows; Windows ARM is experimental.
- Windows packaged desktop rollback E2E must stop the helper-relaunched restored app before temporary-directory cleanup.
- GitHub Actions caches Linux desktop APT archives plus one-day metadata. Cache keys retain runner, architecture, and package-manifest identity; action refs are pinned to full commit SHAs.
- Cache restores are `continue-on-error`; non-success restores are untrusted and force normal installation. Valid hits preflight cached lists and use `apt-get install --no-download`; stale or partial restores recover normally.
- Production workflow retains machine-readable dependency-setup and complete-job timing artifacts.

Validation conventions:
- Prefer `make test`, `make test-cover`, or `go test ./... -count=1 -timeout 120s`. Raw 60-second full-suite runs can time out under load.
- Use readiness polling instead of fixed sleeps. Timing-sensitive tests may use `testing.Short()` guards.
- Distinguish touched-scope regressions from environmental failures such as desktop/config PATH, macOS Wails linker warnings, SQLite locks, date-sensitive schedules, and plugin-marketplace network timeouts.
- On macOS, `TMPDIR=/private/tmp` is the established fallback when default temporary-directory behavior destabilizes validation. A repeatable `/usr/bin/git` exit `69` indicates the Xcode license may need acceptance; verify Git and rerun before treating it as a product failure.
- Real-browser coverage is required for HTMX history/restoration, streaming offsets, transcript scrolling/hydration, drafts, pending attachments, stale-response races, live refresh, drag/drop, keyboard shortcuts, and DOM timing. Desktop/Wails fixes also need native WebKit or equivalent packaged-desktop evidence; markup inspection and generic Chromium simulation are insufficient.
- Browser performance fixtures run over localhost HTTP, not `file://`, and expose explicit DOM pass/fail markers.
- External HTTP retry tests inject clock/backoff hooks so retries run without real sleeps.
- Handler `NewTestContext` enables local repo paths by default; tests requiring disabled local paths must call `SetLocalRepoPathEnabled(false)`.
- Chat mode selector tests use `chat-select-change` with `e.detail.value`, not native select `change` semantics.
- Malformed-JSON logs in service error-path tests are expected unless an assertion/package also fails.
- Concurrency tests assert final/current observable state rather than requiring notification delivery before operation return.
- Composer shortcut/modifier browser coverage exercises Enter idle/active, Meta/Ctrl+Enter idle/active, Meta/Ctrl-click on Send/Stop, and iOS platform detection.

Known correctness and coverage gaps:
- `TaskGoalService.ResumeGoalStoppedByUser()` is production-used but lacks dedicated regression coverage; existing user-stop tests exercise a different method.
- Completed-task Task Detail edit/goal no-rerun regressions should assert no worker queue item/running worker state, no lifecycle continuation rows, no schedule-run marker, preserved completed status, and no extra execution/thread-input rows.
