---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-08-19
source: after_complete
source_id: 3eb5ad78b50aad57c595d4075d836a08:24dc26b3af065014
confidence: high
title: Testing Coverage and Performance
---

OpenVibely has a large Go test suite. Durable quality work should emphasize broad behavioral coverage and production-shaped performance evidence rather than accumulating narrow happy-path tests, raw logs, or task-by-task benchmark notes.

Coverage direction:
- `internal/service` and `internal/handler` are the broadest coverage seams, especially error, pagination, webhook, retry, scheduling, channel, and orchestration paths.
- `internal/service/llm_service.go` needs controlled LLM caller mocks to avoid flaky tests.
- Generated templ `*_templ.go` files are excluded from coverage summaries while template tests still run.
- Prefer broad behavioral coverage over granular tests around already-covered code. Avoid blanket `t.Parallel()` around shared database setup.
- For Codecov-targeted coverage work, do not estimate acceptance from local Go cover-profile statement percentages alone. Compare covered line ranges against Codecov API `line_coverage`, use Codecov's denominator, and verify hosted report for the exact commit.
- Shared mutable handler databases are unsuitable because tests mutate global/default/list state. `NewTestDB` uses an immutable serialized SQLite template to create fresh isolated in-memory fixtures while preserving UTC location, foreign keys, busy timeout, `MaxOpenConns(1)`, migrated schema, seed data, cleanup, and default-agent behavior.
- Direct runtime-tool/action tests should exercise project ownership, authorization, pagination, mode restrictions, malformed JSON, and provider-tool-incapable paths.

Performance contracts:
- SQLite uses one open connection, so unindexed scans, query fan-out, and long transactions can stall unrelated database-backed requests. Performance work must measure contention as well as isolated query latency.
- High-cardinality deletion paths must retain indexed foreign-key and cleanup-ownership lookups for alerts, lifecycle parent references, thread inputs, and attachment sessions. Preserve query-plan assertions for indexed `SEARCH` operations and production-shaped deletion/concurrency fixtures.
- Attachment cleanup tests must cover Clear Chat, queued/immediate publication rollback, direct Chat/task-thread failures, shared-reference retention, durable session retirement, and both upload-first and retirement-first publication-fence ordering.
- List, dashboard, and discovery surfaces should use bounded projections rather than materializing full prompts, bodies, secrets, metadata, or configuration blobs when only compact summaries are rendered.
- `list_tasks` discovery uses a compact projection preserving count/page shape, relevance ordering, filters, project isolation, chat exclusion, pagination, nullable summaries, UTC timestamp serialization, and production-sized query/response/latency evidence.
- Models-page initial rendering uses compact card projections, fetches one authorized full record for Edit, and guards stale edit-detail responses.
- Model availability checks should use a true existence query (`SELECT EXISTS`) rather than hydrating full model lists for hot task/chat gating paths.
- Update drain queued-count queries over pending queued `thread_inputs` should use a sparse partial index while preserving `drain.queued_total` semantics and existing promotion ordering indexes.
- Shared page-shell current-project fallback should use compact project selector rows (`id`, `name`, `is_default`) ordered by default/name/id, with a covering selector-order index; full project hydration remains for management/API/runtime paths.
- `ExecutionStreamHub` steady-state publication uses lazily rebuilt immutable subscriber snapshots. Preserve zero-allocation steady-state publishing and concurrent Publish/Unsubscribe/Close coverage.
- Performance changes must preserve ordering, authorization, cancellation, retries, lifecycle behavior, persistence, and projection semantics. Use representative fixtures, query plans, SQL/process/I/O counts, allocations, response sizes, and contention measurements.
- Durable cost centers include handler/service tests, streaming parser and chunk reassembly, repeated database/context setup, and DB/git/worktree-heavy services.

Known bounded-projection history and gaps:
- Implemented bounded projections should be preserved for Templates dashboard cards, Pulse Upcoming summaries, worktree cleanup scans, Schedule calendar, Personality cards, Automation transition indexes, stale external PR discovery, GitHub Dev Inbox assigned-issue fetching, compact API Chat model selection, Agent dialog model picker/save/generate paths, compact standalone Skills rendering, browser Alerts list/detail summaries, task-detail metrics/status polling, project-dialog model dropdowns, Reflection recent executions, channel target lookup indexes, and task-turn skill catalog enabled-state filtering.
- Open gap `#269`: Backlog analysis loads full prompts even though only previews are used.
- Open gap `#294`: Insights workflows materialize complete task prompts while retaining only short previews.
- Open gap `#300`: Architect task-plan queries select unbounded prompts though rendered view uses compact metadata.
- Open gap `#314`: `ListAutomationWorkItems` pagination needs created-at ordered indexes for unfiltered and status-filtered paths.
- Open gap `#321`: swarm child list/find paths should use bounded projections omitting prompt/chain config.
- Open gap `#327`: Chat context building loads full task rows, including large prompt/chain/swarm blobs, for every message.
- Open gap `#330`: autonomous trigger task-context gathering calls full task lists where title/category/status summaries suffice.
- Open gap `#345`: Skills Analytics aggregates use computed local-time buckets and temp B-trees; needs indexed bucket strategy preserving local-time semantics.
- Open gap `#565`: Skill Analytics enabled-skill discovery calls full `AgentRepo.List`; should use compact agent identity/catalog fields.
- Open gap `#572`: Agents plugin state refresh rereads marketplace manifests/install caches and reparses installed plugin configs on New/Edit modal opens; should cache/incrementally refresh while preserving correctness.
- Open gap `#594`: agent-owned skill editing eagerly reads/parses every `SKILL.md` on modal open; should use compact summaries plus lazy single-skill body hydration.
- Open gap `#668`: scoped-file `read_file` previews read/split entire target files before offset/limit; should stream or otherwise bound reads while preserving scoped authorization and output format.
- Open gap `#350`: task-board refreshes still select/parse full chain/swarm blobs for every card.
- Open gap `#355`: Execution Analytics aggregates use date/hour grouping/order not covered by indexes.
- Open gap `#358`: GitHub PR feedback forwarding re-fetches all historical comments/reviews/review comments on unchanged scheduled runs.
- Open gap `#457`: Task Changes/code-review rendering rebuilds review-comment maps inside both inline/split file loops and lacks an order-covering list index.
- Open gap `#490`: Automation Live detail should use compact `automation_live_activity_states` projection instead of ranking full activities history on every render.
- Open gap `#497`: Task Detail Lifecycle tab/API listing should use compact lifecycle execution list columns while keeping event detail retrieval unchanged.
- Open gap `#504`: Automations portfolio should use compact published-card projections instead of project-wide operational counts plus full graph/resource/schedule hydration.
- Open gap `#529`: inbound Email polling does redundant receipt-table work on accepted success path.
- Open gap `#541`: task detail Execution History polling should use bounded latest-window response plus older-execution paging, with benchmark acceptance rather than assumption.
- Open gap `#546`: Models HTMX mutation refreshes should use compact card projections and full detail only for edit-detail path.
- Resolved `#706`: Chat/task-thread transcript windows moved completed/error assistant-bubble rendering from repeated per-bubble inline scripts to compact static raw-content markup plus the shared hydrator. The retained regression fixture for 100 completed assistant messages with 128-byte outputs reported `39,000` rendered bytes versus the prior roughly `215,600` byte probe, with `262` bytes fixed overhead per bubble.
- Open gap `#716`: GitHub PR branch publication currently runs a per-file `git ls-files -s -- <path>` mode lookup during change collection before remote upload; a synthetic repo with 1,000 modified tracked files plus 1,000 untracked files measured median `13.24s` and `2,002` git subprocesses versus a batched-mode comparator at median `135.82ms` and `3` subprocesses. Fix should batch/local-cache mode lookup separately from existing remote blob-upload parallelization while preserving publish semantics.
- Open gap `#555`: Channels settings page render should batch direct `app_settings` reads through one `SettingsRepo.GetMany` snapshot.
- Open gap `#623`: Automation history dashboard loads full graph definition though only automation identity and `published_version_id` are used.
- Hosted-Ubuntu dependency-cache benchmark workflow `#228` has strict sample/comparator requirements; do not claim acceptance until actual accepted artifacts exist.

CI and dependency setup:
- `go test ./... -coverprofile=coverage.out -coverpkg=./...` is the primary GitHub Actions coverage/compile gate and includes `cmd/server`; do not add a separate default `go build ./cmd/server` step without a distinct purpose.
- Preserve generated-file cleanliness checks and templ-generated-file coverage exclusions around the coverage gate.
- The packaged-update CI job runs native `internal/update` tests plus binary and desktop packaged E2E checks across Linux, macOS, and Windows OS/arch rows; Windows ARM is experimental.
- Windows packaged desktop rollback E2E must stop the helper-relaunched restored app before temp-dir cleanup because a live exe locks deletion.
- GitHub Actions caches Linux desktop APT archives plus one-day metadata cache. Cache keys retain runner identity, architecture, and package-manifest digest; action refs are pinned to full commit SHAs.
- Cache restores are `continue-on-error`; any non-success restore is untrusted and forces normal install. Valid hits preflight cached lists and use `apt-get install --no-download`; stale/partial/failed restores recover normally.
- Production workflow retains machine-readable dependency-setup and complete-job timing artifacts.

Validation conventions:
- Prefer `make test`, `make test-cover`, or `go test ./... -count=1 -timeout 120s` for authoritative validation. Raw 60-second full-suite runs can time out under load.
- Use readiness polling instead of fixed sleeps. Timing-sensitive tests may use `testing.Short()` guards.
- Distinguish repeatable touched-scope regressions from environmental failures such as desktop/config PATH, macOS Wails linker warnings, SQLite locks, date-sensitive schedules, and plugin marketplace network timeouts.
- `TMPDIR=/private/tmp` is the established macOS fallback when default-temp behavior destabilizes validation. If exact failing test/package passes independently and full suite passes with this TMPDIR, treat default-temp failure as environmental rather than touched-scope regression.
- Real-browser coverage is required for HTMX history/restoration, streaming offsets, transcript scrolling/hydration, drafts, pending attachments, stale-response races, live refresh, drag/drop, keyboard shortcuts, and other DOM timing. Handler HTML inspection alone is insufficient.
- Browser performance fixtures should run over localhost HTTP, not `file://`, and expose explicit DOM pass/fail markers.
- External HTTP retry tests inject clock/backoff hooks so retries run without real sleeps.
- Handler `NewTestContext` enables local repo paths by default; tests requiring disabled local paths must call `SetLocalRepoPathEnabled(false)`.
- Chat mode selector tests use `chat-select-change` custom events with `e.detail.value`, not native select `change` semantics.
- Malformed-JSON logs in service error-path tests are expected unless an assertion/package also fails.
- Concurrency tests should assert final/current observable state rather than requiring notification delivery before operation return.
- Composer shortcut/modifier browser coverage must exercise Enter idle/active, Meta/Ctrl+Enter idle/active, and Meta/Ctrl-click on Send/Stop, including platform coverage for `navigator.userAgentData.platform === "iOS"`.

Known correctness and coverage gaps:
- `TaskGoalService.ResumeGoalStoppedByUser()` is production-used but lacks dedicated regression coverage; existing user-stop tests exercise a different method.
- Completed-task Task Detail edit/goal no-rerun regressions should assert no worker queue item/running worker state, no lifecycle continuation rows, no schedule-run marker, preserved completed status, and no extra execution/thread-input rows.
