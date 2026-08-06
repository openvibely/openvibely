---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-08-04
source: consolidation
source_id: memory_consolidation_2026_08_04
confidence: high
title: Testing Coverage and Performance
---

OpenVibely has a large Go test suite. Durable quality work should emphasize broad behavioral coverage and production-shaped performance evidence rather than accumulating narrow happy-path tests or issue-by-issue benchmark notes.

Coverage direction:
- `internal/service` and `internal/handler` are the broadest coverage seams, especially error, pagination, webhook, retry, and large orchestration paths. `internal/service/llm_service.go` needs controlled LLM caller mocks to avoid flaky tests.
- Generated templ `*_templ.go` files are excluded from coverage summaries while template tests still run.
- Prefer broader behavioral coverage over adding granular tests to already-covered code. Avoid blanket `t.Parallel()` around shared database setup.
- A shared mutable handler database is unsuitable because tests mutate global/default/list state. `NewTestDB` uses an immutable serialized SQLite template to create fresh isolated in-memory fixtures while preserving `_loc=UTC`, foreign keys, busy timeout, `MaxOpenConns(1)`, migrated schema, seed data, cleanup, and default-agent behavior.

Performance contracts:
- SQLite uses one open connection, so unindexed scans, query fan-out, and long transactions can stall unrelated database-backed requests. Performance work must measure contention as well as isolated query latency.
- High-cardinality deletion paths must retain indexed foreign-key and cleanup-ownership lookups, especially alerts, lifecycle parent references, thread inputs, and attachment sessions. Preserve query-plan assertions for indexed `SEARCH` operations and production-shaped deletion/concurrency fixtures.
- Attachment cleanup tests must cover Clear Chat, queued/immediate publication rollback, direct Chat/task-thread failures, shared-reference retention, durable session retirement, and both upload-first and retirement-first publication-fence orderings.
- List, dashboard, and discovery surfaces should use bounded projections rather than materializing full prompts, bodies, secrets, metadata, or configuration blobs when only compact summaries are rendered. Validate query count, response size, allocations, latency, ordering, authorization, pagination, and project isolation with production-sized payloads.
- `ExecutionStreamHub` steady-state publication uses lazily rebuilt immutable subscriber snapshots. Preserve zero-allocation steady-state publishing and concurrent Publish/Unsubscribe/Close coverage.
- Models-page initial rendering uses compact card projections, fetches one authorized full record for Edit, and guards stale edit-detail responses. Preserve production-sized handler benchmarks and lazy secret/edit hydration.
- Performance changes must preserve ordering, authorization, cancellation, retries, lifecycle behavior, persistence, and projection semantics. Use representative fixtures, query plans, SQL/process/I/O counts, allocations, response sizes, and contention measurements rather than relying only on microbenchmarks.
- Durable cost centers include handler/service tests, streaming parser and chunk reassembly, repeated database/context setup, and DB/git/worktree-heavy services.
- GitHub Actions caches downloaded Linux desktop apt archives, not installed system state. Cache keys include runner identity, architecture, and the package-manifest digest; `apt-get update` and installation remain unconditional. Workflow action references are pinned to full commit SHAs.

Validation conventions:
- Prefer `make test`, `make test-cover`, or `go test ./... -count=1 -timeout 120s` for authoritative full validation. Raw 60-second full-suite runs can time out under load.
- Use readiness polling instead of fixed sleeps. Timing-sensitive tests may use `testing.Short()` guards.
- Distinguish repeatable touched-scope regressions from environmental failures such as desktop/config `PATH`, macOS Wails linker warnings, SQLite locks, date-sensitive schedules, and plugin marketplace network timeouts. `TMPDIR=/private/tmp` is the established fallback when default-temp plugin cloning hangs.
- Real-browser coverage is required when correctness depends on HTMX history/restoration, streaming offsets, transcript scrolling/hydration, drafts, pending attachments, stale-response races, live-refresh ordering, drag/drop, or other DOM timing. Handler HTML inspection alone is insufficient.
- Browser performance fixtures should run over localhost HTTP rather than `file://`, and harnesses should expose an explicit DOM pass/fail marker because headless Chrome may leave its parent process alive.
- External HTTP retry tests inject clock/backoff hooks so retries run without real sleeps; provider-specific tests cover classification, replay safety, and metadata preservation.
- Handler `NewTestContext` enables local repo paths by default; tests requiring them disabled must call `SetLocalRepoPathEnabled(false)`.
- Chat mode selector tests use `chat-select-change` custom events with `e.detail.value`, not native select `change` semantics.
- Malformed-JSON logs in service error-path tests are expected unless an assertion or package also fails.
- Concurrency tests should assert final/current observable state rather than requiring notification delivery to precede operation return.
- Composer shortcut/modifier browser coverage (e.g. `chat_composer_shortcuts_browser_test.go`) must exercise every state combination, not just the common cases: plain Enter idle/active, explicit Meta/Ctrl+Enter both idle (safe single-send fallback) and active (real queue), and Meta/Ctrl-click on Send both when steering is active and when steering is unavailable/idle (safe fallback). Audits have flagged missing idle-state modifier coverage as a material gap in this suite before.
