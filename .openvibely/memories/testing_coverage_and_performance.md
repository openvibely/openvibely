---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-07-27
source: task_conversation
source_id: d2c183e2800ecda82331c155ba294e70:abc4dd9738e95601
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
- Open issues track inefficient queued-input recovery indexing (`#22`), managed-worktree diff snapshots (`#31`), task-thread polling projections (`#39`), worker queue dispatch scans (`#42`), per-execution SSE catch-up reads (`#46`), GitHub PR-feedback forwarding (`#53`), assigned-issue PR lookups (`#58`), idle scheduler logging (`#63`), due-schedule task lookup fan-out (`#70`), redundant CI compilation (`#73`), Automation portfolio query fan-out (`#74`), and Channels page settings-read fan-out (`#80`, approximately 50 `app_settings` queries per render). Verify issue state before acting.
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
