---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-07-15
source: consolidation
source_id: memory_consolidation_2026_07_15
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
- CI optimization `openvibely/openvibely#26` was implemented in PR `openvibely/openvibely#34`: the Linux desktop job caches downloaded `.deb` archives with `actions/cache` pinned to commit `0057852bfaa89a56745cba8c7296529d2fc39830` (`v4.3.0`). Cache keys include `ImageOS`, `ImageVersion`, runner architecture, and a SHA-256 of the exact dependency list; both cache hits and misses still run `apt-get update` and `apt-get install`, preserving dpkg authority and cold-cache correctness. The pre-change baseline was 20–40 seconds across 10 measured runs with a 27.5-second median (16.7% of median workflow duration). Hosted validation remains outstanding after merge: measure 10 successful warm runs and confirm at least a 15-second median total-duration reduction, along with cache overhead and cold-run timing.
- Real-time Task Changes snapshot optimization is tracked in `openvibely/openvibely#31`: the managed-worktree snapshot path launches six Git subprocesses every two seconds, while one-time invariant worktree/target validation reduces equivalent tracked and untracked diff capture to two subprocesses. A corrected 300-sample disposable benchmark measured median capture time falling from about 94.9 ms to 68.5 ms (about 28%); preserve snapshot contents, direct-checkout behavior, and final persistence semantics when implementing it.
- Durable test-cost centers are `internal/handler`, `internal/service`, streaming parser/chunk reassembly coverage, repeated test DB/context setup, and DB/git/worktree-heavy service tests. Prefer readiness polling over fixed sleeps; timing-sensitive tests may use `testing.Short()` guards.
- `pkg/openai_client` and `pkg/anthropic_client` retry logic expose package-level `clockAfter = time.After` seams so tests can bypass real retry sleeps without changing production behavior.
- A shared handler `TestMain` database is intentionally unsuitable because many handler tests mutate/query global, default, and list state; `NewTestDB` caches migrations, while safe shared-DB use would require transaction rollback isolation.
- Prefer `make test`, `make test-cover`, or `go test ./... -count=1 -timeout 120s` for authoritative full validation. Raw 60s full-suite runs can time out in `internal/handler` or `internal/service` under load.
- Full-suite failures can include environmental desktop/config `PATH` issues, macOS Wails linker warnings, SQLite locks, date-sensitive scheduling tests, and plugin marketplace clone/network timeouts. Distinguish repeatable touched-scope regressions from environmental failures; when default temp-directory runs hang during plugin cloning, `TMPDIR=/private/tmp` has been the stable full-suite fallback.
- Handler `NewTestContext` now calls `h.SetLocalRepoPathEnabled(true)`, matching older `setupHandlerTest` default. Tests needing local paths disabled should explicitly call `tc.handler.SetLocalRepoPathEnabled(false)` after creating context.
- Chat mode selector tests should reflect the custom portal select implementation: hidden form input updates are driven by `chat-select-change` custom events carrying `e.detail.value`, not native `change` events or `this.value`.
- `internal/service` tests can intentionally emit malformed-JSON logs when exercising error paths; treat them as expected unless paired with a failing assertion/package result.
- Concurrency regression tests with buffered notification channels should wait on final/current observable state rather than requiring a notification to be drained before an operation returns; goroutine notification delivery and operation completion can race. `TestTelegramServiceConcurrentUpdateTokenIsSerialized` follows this pattern by tracking the last-completed token update, asserting the service's current bot token, and waiting for that specific current-token poller.
