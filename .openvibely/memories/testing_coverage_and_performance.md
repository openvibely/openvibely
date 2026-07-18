---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-07-17
source: consolidation
source_id: memory_consolidation_2026_07_17
confidence: high
title: Testing Coverage and Performance
---

OpenVibely has a large Go test suite whose measured coverage is limited more by breadth gaps and generated templ output than by lack of test count.

Coverage decisions:
- Test coverage is broad in count but uneven by subsystem: granular happy-path tests cluster in already-tested files while large service/handler files and error, pagination, webhook, and retry paths remain the main gaps.
- `internal/service` and `internal/handler` are the largest durable coverage drains by function count.
- Generated templ `*_templ.go` files are excluded from coverage summaries while template tests still run. `Makefile` and GitHub Actions coverage summaries filter generated templ output before reporting.

Durable coverage priorities:
- Highest-ROI remaining coverage target is `service/llm_service.go`; it requires careful LLM caller mocking to avoid flaky tests.
- Expand existing `workflow_service_test.go` and `telegram_service_test.go` beyond happy paths, especially error, pagination, webhook, and retry paths.
- Add sparse LLM adapter tests for `internal/llm/anthropic`, `internal/llm/openai`, `internal/llm/ollama`, and `internal/llm/workflow`.
- Existing tests are mostly not wrong; the durable issue is narrow breadth, not excessive count. Avoid blanket `t.Parallel()` changes around shared DB setup.
- Outbound DM permissions coverage is in place for Discord user-DM target persistence, saved Discord user-DM test dispatch, Authorized Users add/delete isolation from outbound targets, and outbound target save/delete isolation from Slack/Discord authorized users.
- Channel project-switch persistence coverage is in place for Slack and Telegram runtime `switch_project` writes plus later active-project resolution. Web/API `switch_project` is guarded as informational-only and must not write Discord/Slack/Telegram/Email active-project tables.
- The canonical outbound direct-message target syntax in chat-control assertions is `platform:user:<id>`.

Runtime and validation facts:
- Known performance opportunity tracked in `openvibely/openvibely#22`: queued task-thread startup recovery filters `thread_inputs` by `scope`, `input_mode`, and `input_status`, but the existing pending-task index begins with `task_id`, so SQLite can scan that index and use a temporary sort across historical inputs. A 200,000-row fixture with 100 recoverable rows measured the current query at about 10 ms versus under 1 ms with a recovery-aligned covering index; validate the eventual index with `EXPLAIN QUERY PLAN`, representative startup benchmarks, and index write/storage tradeoffs.
- The Linux desktop CI job caches downloaded `.deb` archives with checksum-pinned `actions/cache`. Cache keys include `ImageOS`, `ImageVersion`, runner architecture, and a SHA-256 of the exact dependency list; cache hits and misses still run `apt-get update` and `apt-get install`, preserving dpkg authority and cold-cache correctness. The pre-change baseline was 20–40 seconds across 10 measured runs with a 27.5-second median. Hosted warm-run validation remains outstanding: confirm at least a 15-second median total-duration reduction and record cache overhead and cold-run timing.
- Real-time Task Changes snapshot optimization is tracked in `openvibely/openvibely#31`: the managed-worktree snapshot path launches six Git subprocesses every two seconds, while one-time invariant worktree/target validation reduces equivalent tracked and untracked diff capture to two subprocesses. A corrected 300-sample disposable benchmark measured median capture time falling from about 94.9 ms to 68.5 ms (about 28%); preserve snapshot contents, direct-checkout behavior, and final persistence semantics when implementing it.
- Task-thread live-polling optimization is tracked in `openvibely/openvibely#39`: `GET /tasks/:taskId/thread?poll=1` HTML correctly omits terminal execution output for already-preserved rows, but the DB query still fetches full `output`/`error_message` text for every execution in the window on each 3-second poll before the template discards it. A disposable SQLite fixture with five executions and about 120 KiB output on four terminal rows measured about 480 KiB fetched/scanned per poll versus about 21 bytes with a status-aware projection.
- Worker queue dispatch optimization is tracked in `openvibely/openvibely#42`: `dispatchNext` holds the queue mutex while scanning every queued task and performing task/project-capacity SQLite lookups for each item. When one project slot is occupied, 100 sequential same-project blocked submissions rescan queue lengths 1 through 100 and perform at least 10,100 SQLite reads, versus roughly 200 admission reads if each submission evaluates only the newly added item. Preserve capacity semantics and validate reduced reads, mutex hold time, and dispatch behavior under blocked-project load.
- Large task-thread rendering regressions cover worker-capable Markdown preparation, bounded marker discovery, cancellable/revision-aware hydration, polling without unchanged remorphs, independent tool-row state, CommonMark line endings, chunk-boundary metadata, and collapsed large-output previews. Canonical rendering behavior lives in `realtime_and_frontend_patterns.md`.
- A representative headless-Chrome fixture exceeds 700 KiB with 117 tools and oversized/bare-CR outputs; its recorded post-fix initial hydration was about 190 ms with no observed 50 ms long task. Browser performance fixtures should run over localhost HTTP because `file://` exercises the renderer's Blob-worker fallback, and harnesses should use an explicit DOM pass/fail marker because headless Chrome may leave its parent process alive after completion.
- Durable test-cost centers are `internal/handler`, `internal/service`, streaming parser/chunk reassembly coverage, repeated test DB/context setup, and DB/git/worktree-heavy service tests. Prefer readiness polling over fixed sleeps; timing-sensitive tests may use `testing.Short()` guards.
- Shared external HTTP retry tests inject `internal/httpretry.Policy.After` and `Policy.Now` so backoff and `Retry-After` behavior can be validated without real sleeps or wall-clock dependence; provider-specific tests cover integration, response classification, stream replay safety, and retry metadata preservation at their client boundaries.
- A shared handler `TestMain` database is intentionally unsuitable because many handler tests mutate/query global, default, and list state; `NewTestDB` caches migrations, while safe shared-DB use would require transaction rollback isolation.
- Prefer `make test`, `make test-cover`, or `go test ./... -count=1 -timeout 120s` for authoritative full validation. Raw 60s full-suite runs can time out in `internal/handler` or `internal/service` under load.
- Full-suite failures can include environmental desktop/config `PATH` issues, macOS Wails linker warnings, SQLite locks, date-sensitive scheduling tests, and plugin marketplace clone/network timeouts. Distinguish repeatable touched-scope regressions from environmental failures; when default temp-directory runs hang during plugin cloning, `TMPDIR=/private/tmp` has been the stable full-suite fallback.
- Handler `NewTestContext` now calls `h.SetLocalRepoPathEnabled(true)`, matching older `setupHandlerTest` default. Tests needing local paths disabled should explicitly call `tc.handler.SetLocalRepoPathEnabled(false)` after creating context.
- Chat mode selector tests should reflect the custom portal select implementation: hidden form input updates are driven by `chat-select-change` custom events carrying `e.detail.value`, not native `change` events or `this.value`.
- `internal/service` tests can intentionally emit malformed-JSON logs when exercising error paths; treat them as expected unless paired with a failing assertion/package result.
- Concurrency regression tests with buffered notification channels should wait on final/current observable state rather than requiring a notification to be drained before an operation returns; goroutine notification delivery and operation completion can race. `TestTelegramServiceConcurrentUpdateTokenIsSerialized` follows this pattern by tracking the last-completed token update, asserting the service's current bot token, and waiting for that specific current-token poller.
