---
name: testing_coverage_and_performance
type: project
created: 2026-06-07
updated: 2026-09-20
source: consolidation
source_id: memory_consolidation_2026-09-20
confidence: high
title: Testing Coverage and Performance
---

OpenVibely quality work prioritizes behavioral coverage, exact-state validation, and production-shaped performance evidence over narrow happy paths, raw logs, task-by-task benchmark notes, or transient hosted-check snapshots.

Coverage and fixtures:
- `internal/service` and `internal/handler` are the broadest seams. Exercise authorization, project ownership, malformed inputs, pagination, retries, scheduling, channels, and orchestration at their service or endpoint boundary. Use controlled caller mocks around `LLMService` to avoid flaky tests.
- `NewTestDB` uses isolated in-memory fixtures from an immutable serialized SQLite template with UTC, foreign keys, busy timeout, one connection, migrated schema, seed data, cleanup, and default-agent behavior. Avoid blanket `t.Parallel()` when shared DB setup is involved.
- Prefer end-to-end contract tests when behavior crosses UI, handler, service, database, or provider boundaries. Keep generated templ output out of coverage summaries while still running template tests; prefer meaningful behavior assertions over redundant line coverage.
- Provider tests should cover request construction and normalization, usage accounting, cancellation/retry, tool authorization, context budgeting, failure/refusal handling, and transport-specific compaction with injected callers or protocol-faithful local fixtures.
- UI interaction evidence for drag/drop, keyboard, focus, selection, popovers, uploads, composer behavior, and scrolling requires native browser input. Synthetic events are supplemental. Live-SSE tests should exercise real routing, project/task isolation, reconnect ordering, slow consumers, and cleanup.
- Large-output rendering, thread history, discovery/list projections, Analytics, and other bounded-read claims need fixtures large enough to expose memory, payload, or query-plan regressions. Assert selected/scanned data and rendered-byte parity where relevant.

Performance and persistence:
- SQLite topology claims require representative file-backed fixtures using multiple physical connections and real repositories, including production `1W + 1R`, pragmas, WAL pressure, atomic writes, lock waits, timeout restoration, claims, leases, and cleanup. Short runs that do not outlast the output flush interval are not topology evidence.
- Projection claims need direct and end-to-end small/large cardinalities with latency, allocations, query/wait counts, response bytes, and query-plan evidence. A narrow microbenchmark or simplified migration query is insufficient.
- Every pooled writer/`RETURNING` path should cover deadline, cancellation, early cancellation, timeout restoration, and failure cleanup. Use deterministic readiness polling instead of fixed sleeps. Run race detection only for a concrete production race invariant or explicit requirement, not for performance gates.

Validation and audits:
- Prefer `make test`, `make test-cover`, or focused `go test` with `-count=1 -timeout 120s`; full CI-style coverage uses `OPENVIBELY_SKIP_BROWSER_PERF=1 go test ./... -count=1 -timeout 240s -coverpkg=./...` including `cmd/server`.
- Distinguish touched-scope defects from environment and known baseline failures. Report narrower passing scope and exact broad failures; local success does not replace exact-head hosted checks or live publication evidence.
- An exact-head hosted check that is failed or pending blocks a clean publication verdict. If logs or annotations cannot establish cause, leave it unresolved rather than classifying it as baseline or transient.
- Strict read-only audits do not run builds, tests, generation, formatting, benchmarks, fetches, commits, pushes, temporary-file redirection, or other write-capable validation. Disclose skipped checks.
- Keep generated artifacts synchronized and clean; packaged update validation must cover supported platform replacement, health/version checks, rollback, invalid signatures, interruption, and recovery.
