---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-07-28
source: consolidation_and_task_turns
source_id: memory_consolidation_2026_07_27;6d635b227f77f9fc3bc8e9e9fd27c645:70032705fa1323f1;6d635b227f77f9fc3bc8e9e9fd27c645:345984107597ce09;abf902e6c55aa6881d2525168bc5e41c:47b8db2b38eb30b8
confidence: high
title: Testing Coverage and Performance
---

OpenVibely has a large Go test suite whose measured coverage is limited more by subsystem breadth gaps and generated templ output than by test count.

Coverage direction:
- `internal/service` and `internal/handler` remain the largest coverage gaps, especially error, pagination, webhook, retry, and large service/handler paths.
- Generated templ `*_templ.go` files are excluded from coverage summaries while template tests still run.
- The highest-value remaining target is `service/llm_service.go`; it needs controlled LLM caller mocks to avoid flaky tests. Existing workflow, Telegram, and provider-adapter suites should broaden beyond happy paths.
- Existing tests are generally useful; prefer broader behavioral coverage over adding more granular tests to already-covered code. Avoid blanket `t.Parallel()` around shared database setup.
- A shared handler `TestMain` database is unsuitable because many tests mutate global/default/list state. `NewTestDB` caches migrations; safe sharing would require transaction rollback isolation.

Current performance seams:
- Task deletion performance is guarded by migration-130 and migration-132 query-plan tests plus a representative concurrency fixture. Migration 130 indexes task/execution foreign keys and cleanup ownership lookups, including `alerts.execution_id`; migration 132 indexes the self-referencing `lifecycle_executions.parent_execution_id` foreign key. Without the lifecycle parent index, deleting each lifecycle row scanned the entire lifecycle table, so production-shaped histories caused quadratic cascade work: a live deletion held the sole SQLite connection for 24.86s, and a deterministic fixture took 13.09s while blocking an unrelated query for the same duration. The fixture now covers 2,000 executions, 2,000 lifecycle runs, 20,000 lifecycle events, 50,000 unrelated lifecycle runs and alerts, 500 queued thread inputs, 100 attachment rows, schedules, goals, analytics, retained alert references, and swarm relationships; with both index migrations, deletion and unrelated-query delay are approximately 127ms and 126ms. Keep query-plan assertions for indexed `SEARCH` operations, especially `alerts.execution_id`, `lifecycle_executions.parent_execution_id`, and cleanup-ownership lookups, because the single-connection SQLite pool turns scans into server-wide stalls. Service regressions cover single and completed/backlog/chat bulk cleanup of finalized task attachments, execution/chat attachments, and pending-upload sessions; concurrent upload publication at the cancellation boundary; unsafe session IDs and missing upload-root rejection before cancellation/deletion; durable-delete failure preservation; surfaced post-delete cleanup failures; shared-reference retention; and managed-worktree preservation.
- Attachment cleanup coverage should retain production-shaped Clear Chat deletion, queued/immediate publication rollback, direct Chat/task-thread failures, shared-reference preservation, durable session retirement, and per-session publication fencing. Deterministic concurrency tests must cover upload-first and retirement-first ordering; query-plan coverage must require indexed task, run-execution, and shared-session lookups with no `thread_inputs` scan.
- Open issues track inefficient queued-input recovery indexing (`#22`), managed-worktree diff snapshots (`#31`), task-thread polling projections (`#39`), worker queue dispatch scans (`#42`), per-execution SSE catch-up reads (`#46`), GitHub PR-feedback forwarding (`#53`), assigned-issue PR lookups (`#58`), idle scheduler logging (`#63`), due-schedule task lookup fan-out (`#70`), redundant CI compilation (`#73`), Automation portfolio query fan-out (`#74`), and Channels page settings-read fan-out (`#80`, approximately 50 `app_settings` queries per render). Verify issue state before acting.
- A confirmed but unpublished agent-plugin performance issue exists in `DiscoverState`: each valid marketplace manifest is read and JSON-parsed three times per plugin-state request, during directory validation, state augmentation, and a separate validating scan used only to decide default seeding. `/agents/plugins/state` invokes this path when the Agents plugin UI opens and after marketplace/plugin mutations; plugin validation and reset flows also use it. A bounded fix should parse manifests once and reuse the projection while preserving invalid-manifest handling, ordering, installed-cache behavior, and default seeding. Add I/O-count instrumentation plus representative latency/allocation benchmarks. The 2026-07-27 audit found no duplicate among 85 public issue/PR objects, but no issue was filed because authenticated repository-wide duplicate search was unavailable; verify current code and issue state before publishing or implementing.
- Performance fixes must preserve existing ordering, authorization, cancellation, retry, lifecycle, persistence, and projection semantics. Validate with representative fixtures and query plans rather than relying only on microbenchmarks.
- Durable cost centers are handler/service tests, streaming parser and chunk-reassembly coverage, repeated test database/context setup, and DB/git/worktree-heavy service tests.

Validation conventions:
- Prefer `make test`, `make test-cover`, or `go test ./... -count=1 -timeout 120s` for authoritative full validation. Raw 60-second full-suite runs can time out under load.
- Use readiness polling instead of fixed sleeps. Timing-sensitive tests may use `testing.Short()` guards.
- Full-suite failures can be environmental, including desktop/config `PATH`, macOS Wails linker warnings, SQLite locks, date-sensitive scheduling tests, and plugin marketplace network timeouts. Distinguish repeatable touched-scope regressions from environmental failures; `TMPDIR=/private/tmp` is the established fallback when default temp-directory plugin cloning hangs.
- Browser performance fixtures should run over localhost HTTP rather than `file://`, and harnesses should expose an explicit DOM pass/fail marker because headless Chrome may leave its parent process alive.
- Real-browser coverage is required for HTMX history/restoration, streaming offsets, transcript scrolling/hydration, drafts, pending attachments, and stale-response races where DOM timing is part of correctness. Canonical rendering behavior lives in `realtime_and_frontend_patterns.md`.
- Shared external HTTP retry tests inject clock/backoff hooks so retry behavior is tested without real sleeps; provider-specific tests cover classification, replay safety, and metadata preservation.
- Handler `NewTestContext` enables local repo paths by default. Tests requiring them disabled must explicitly call `SetLocalRepoPathEnabled(false)`.
- Chat mode selector tests use `chat-select-change` custom events with `e.detail.value`, not native select `change` semantics.
- Malformed-JSON logs in service error-path tests are expected unless an assertion or package also fails.
- Concurrency tests should assert final/current observable state rather than requiring notification delivery to precede operation return.
