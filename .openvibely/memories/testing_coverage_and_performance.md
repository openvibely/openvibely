---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-08-06
source: task
source_id: d9522af5d1b8d1b9a3c942538158a0c0
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
- List, dashboard, and discovery surfaces should use bounded projections rather than materializing full prompts, bodies, secrets, metadata, or configuration blobs when only compact summaries are rendered. `list_tasks` discovery specifically uses a private eight-column projection; preserve its count/page query shape, relevance ordering, filters, project isolation, chat exclusion, pagination, nullable summary output, and UTC timestamp serialization. Validate query count, response size, allocations, latency, ordering, authorization, pagination, and project isolation with production-sized payloads.
- `ExecutionStreamHub` steady-state publication uses lazily rebuilt immutable subscriber snapshots. Preserve zero-allocation steady-state publishing and concurrent Publish/Unsubscribe/Close coverage.
- Models-page initial rendering uses compact card projections, fetches one authorized full record for Edit, and guards stale edit-detail responses. Preserve production-sized handler benchmarks and lazy secret/edit hydration.
- Performance changes must preserve ordering, authorization, cancellation, retries, lifecycle behavior, persistence, and projection semantics. Use representative fixtures, query plans, SQL/process/I/O counts, allocations, response sizes, and contention measurements rather than relying only on microbenchmarks.
- Durable cost centers include handler/service tests, streaming parser and chunk reassembly, repeated database/context setup, and DB/git/worktree-heavy services.
- GitHub Actions caches downloaded Linux desktop APT archives and a one-day runner/package-scoped APT metadata cache; archive keys retain runner identity, architecture, and package-manifest digest, and action references are pinned to full commit SHAs. Archive and metadata cache restores are `continue-on-error`; any non-success restore outcome is treated as untrusted and forces the normal `apt-get update`/install recovery path. A valid cache hit must preflight cached lists and use `apt-get install --no-download`; cold, stale, invalid, partial, or failed restores must update APT and perform a normal install. The production workflow retains machine-readable dependency-setup and complete-job timing artifacts, including cache restore outcomes, metadata validation, APT operations, archive download metrics, runner identity, and benchmark labels.
- Issue #228 has a manual hosted-Ubuntu benchmark workflow that warms the cache, collects 10 baseline cache-hit samples, 10 candidate cache-hit samples, and three candidate cold/partial recovery samples on the same runner image. Its comparator rejects insufficient, duplicated, malformed, invalid-cache, or regressing evidence; acceptance requires candidate dependency-setup p50 and p95 improvement of `>=20%` and no candidate complete-job p50/p95 regression. Complete-job timing starts before checkout and workflow validation, and cold cache keys include a hash of each sample identity so concurrent cold samples cannot share caches. Workflow invariants and comparator regression tests protect these contracts. Hosted benchmark evidence is still pending; do not claim #228 acceptance until an actual run produces accepted artifacts.
- The Ubuntu aggregate `go test ./...` workflow already executes `internal/update`; a separate Ubuntu `internal/update` matrix leg duplicated that native test coverage. Issue #254 removed only that Ubuntu entry from `packaged-update-native.strategy.matrix.os` in `.github/workflows/test.yml` (now `[macos-latest, windows-latest]`), keeping the aggregate Ubuntu `test` job command unchanged so coverage collection and Linux `internal/update` execution remain intact; PR #259 implements this.

Validation conventions:
- `go test ./... -coverprofile=coverage.out -coverpkg=./...` is the primary GitHub Actions coverage/compile gate. It includes `cmd/server`, so do not add a separate default `go build ./cmd/server` step without a distinct documented configuration or artifact purpose; preserve generated-file cleanliness checks and templ-generated-file coverage exclusions around this gate.
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
- Composer shortcut/modifier browser coverage (e.g. `chat_composer_shortcuts_browser_test.go`) must exercise every state combination, not just common cases: plain Enter idle/active; Meta/Ctrl+Enter idle (safe single-send fallback) and active (guarded steering); and Meta/Ctrl-click on Send/Stop when steering is active and when it is unavailable/idle. Platform coverage must include the `navigator.userAgentData.platform === "iOS"` path, asserting Apple hint/tooltip and Meta behavior rather than Ctrl. Audits have flagged missing idle-state modifier coverage as a material gap in this suite before.
