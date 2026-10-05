---
name: openvibely_architecture
type: project
created: 2026-05-09
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: OpenVibely Architecture
---

OpenVibely is an open-source Go application for scheduled tasks and AI execution. The backend uses Echo v4, SQLite (`modernc.org/sqlite`), goose migrations, and a channel-based worker pool; the UI uses server-rendered HTMX, templ, Tailwind, and DaisyUI.

- `internal/server.Start` is the shared backend for server and Wails desktop; backend forking is not intended. Local/server, desktop, and hosted/Docker storage roots differ; `DATABASE_PATH` takes precedence, and hosted storage is explicitly configured under mounted `/data`.
- File-backed production SQLite uses serialized bootstrap, one pooled writer and one query-only reader (`1W + 1R`); isolated in-memory tests share one connection. Migrations finish before opening the reader. Connections use foreign keys, busy timeout, UTC behavior, and WAL support.
- Project deletion revalidates cleanup ownership transactionally, commits relational changes before filesystem cleanup, quarantines ordinary attachments/pending uploads, removes only recognized managed clones, retains user repositories, and reports post-commit cleanup warnings. Runtime channel project-selection caches evict only after successful deletion.
- `worker_settings.max_workers=0` means unlimited. Global/project/model capacity reservations are atomic and released on terminal paths; lowering a limit blocks new admission without cancelling running work. Project caps cannot exceed a finite global cap.
- `tasks.agent_definition_id` selects Agent persona; `tasks.agent_id` selects model configuration. Schedule timing belongs to the schedule row and execution assignment to linked tasks. `clear_context_on_start` is schedule-owned, non-destructive, and defaults true for new schedules.
- Browser and runtime reads/mutations enforce project ownership before exposing prompts, outputs, skills, memory, events, goals, or analytics. `internal/handler` is the Echo boundary; feature files own domain behavior and services own shared policy/authorization.
- Prefer compact, bounded projections and project-scoped indexes for queues, task discovery, and dashboards. Pulse distinguishes runnable pending/queued tasks from persisted blocked dependency tasks; blocked display is bounded while aggregates may cover more rows.
- Durable task chains create blocked child rows for board visibility and activate from the latest parent configuration without rewriting or duplicating already-started children. Automation state transitions must be crash-consistent across task, execution, activity, invocation, reservation, and cancellation records.
- Project Settings should report saved repository-path health read-only, distinguishing missing local repositories from unavailable managed GitHub checkouts. Dedicated-model waiting-work visibility on Workers remains limited.
- Known edge cases: malformed direct schedule recurrence input needs consistent rejection; invalid `run_at` can leave a scheduled task without its schedule row. Do not claim broad suite success over known server-bootstrap/read-only-database or schema-version baseline failures; report exact failures and narrower passing scope.
- Operational events use `Infof`; raw content and high-frequency traces are debug-gated. JSON candidate extraction is centralized, while callers retain schema validation and repair.

Canonical Chat/task-thread, provider, integration, Automation, update, Analytics, worktree, and UI details live in their specific topics.
