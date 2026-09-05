---
name: openvibely_architecture
type: project
created: 2026-05-09
updated: 2026-09-03
source: consolidation
source_id: memory_consolidation_2026-09-03
confidence: high
title: OpenVibely Architecture
---

OpenVibely is an open-source Go application for scheduled tasks and AI execution. The backend uses Echo v4, SQLite through `modernc.org/sqlite`, goose migrations, and a channel-based worker pool. The UI is server-rendered with HTMX, templ, Tailwind CSS, and DaisyUI.

Server, desktop, and storage:
- `internal/server.Start(ctx, cfg)` wires the shared backend and returns address, base URL, and shutdown handle. Desktop and server share `internal/server`; backend forking is not intended.
- The Handler owns the synchronized current X service. Shutdown and reconfiguration operate on the installed service, with configuration-generation fencing preventing stale pollers from handling mentions or completing handoffs.
- Local/server storage defaults to `$HOME/.openvibely`; desktop uses `~/Library/Application Support/OpenVibely` on macOS, `%LOCALAPPDATA%\OpenVibely` with `%APPDATA%` fallback on Windows, and `$XDG_DATA_HOME/openvibely` with `~/.local/share/openvibely` fallback on Linux. Hosted/Docker uses explicit env-driven storage under mounted `/data`. Desktop uses `ModeDesktop`, `PORT=0`, local repos, and a Wails WebView. `OPENVIBELY_APP_DATA_DIR` is literal, `DATABASE_PATH` still wins for the database, and `OPENVIBELY_RUNTIME_DIR` is only a deprecated `start.sh` alias.
- Packaged desktop binaries load `config.env` from the OS config directory unless `OPENVIBELY_DESKTOP_CONFIG_FILE` overrides it. Desktop startup constructs its own environment because shell `PATH` is not reliable. Safe external links use runtime markers and `/open-external`; native dialogs are preferred when WebView uploads are unreliable.

SQLite and runtime state:
- File-backed production SQLite uses a dedicated `1W + 1R` topology after serialized bootstrap: one pooled writer connection and one query-only reader. Isolated `:memory:` fixtures use one shared connection. Each physical file-backed connection receives foreign keys, a five-second busy timeout, UTC datetime behavior, and preserved caller query/pragma parameters; WAL permits readers beside the writer.
- Bootstrap and goose migrations run through the held writer before opening the reader. Startup failure unregisters mappings and closes both handles, including late failures. Initial auto-vacuum conversion may require a warned pre-context `VACUUM`.
- Bounded writer and `RETURNING` helpers are cancellation/deadline-aware, restore and verify timeouts, classify retries, clean up, and discard poisoned physical connections. Transaction-owned executors remain caller-scoped. Task-goal updates and attachment creation are part of this writer invariant.
- High-cardinality deletion keeps ownership indexes for alerts, lifecycle parents, task/execution attachments, and pending-upload sessions. Relational attachment cleanup commits before filesystem cleanup; session fences and retirement tombstones protect late uploads. Local storage migration preserves databases, SQLite sidecars, repos, uploads, and runtime directories; release binaries never use checkout-relative storage.

Workers, tasks, and schedules:
- `worker_settings.max_workers=0` means unlimited and the API/UI display `Unlimited`. Global, project, and model reservations are atomic and released on completion, failure, cancellation, claim failure, or panic. Project limits may be any positive integer, must not exceed a finite global limit, and are independent caps rather than a sum constraint; malformed, negative, nonnumeric, or out-of-range inputs fail before side effects. Empty/zero clears a project cap and an increase triggers dispatch; lowering the global limit does not cancel running work, but blocks new admissions at the actual global ceiling and flags stale project caps in the Workers UI. There is no environment override.
- Repeat intervals are positive `1..365` for every recurrence unit across browser, repository, Automation, runtime, and channel paths. Direct schedule-handler coercion of malformed/overflow intervals or unsupported recurrence types remains open as `#116`.
- Scheduled-task creation has an open orphan risk in `#947`: invalid or missing `run_at` can escape the expected HTTP-error path and persist a scheduled task without a schedule row. Validation must reject before task persistence and cover both no-task and no-schedule side effects. This is distinct from `#116`, `#169`, and `#357`.
- Schedule timing belongs to schedule rows; execution assignment belongs to linked tasks. `tasks.agent_definition_id` is the primary Agent and `tasks.agent_id` is the model config. `clear_context_on_start` is schedule-owned and non-destructive; new schedules default true. `ScheduleActionService.CreateForTask` is the normalization boundary. One-time scheduling resets a terminal task to pending unless running and initially uses `NextRun=RunAt`, including past recurring run times.
- Grouped schedule movement is one project-scoped atomic request: reload enabled rows in the writer transaction, validate ownership/disabled members, reconstruct local wall-clock fields across DST, change only `run_at`/`next_run`, and roll back on mismatch/error. Single-card rescheduling uses task ownership checks.
- Automation claim/failure transitions are crash-consistent across execution, task, activity, invocation, outbox, reservation, and cancellation state. Committed transitions publish project-scoped board invalidations; eligible failed/completed Automation work may be reset during later admission.

OAuth, hosted deployment, and Docker:
- OAuth origins prefer `APP_BASE_URL`, then forwarded/request host. `OAUTH_REDIRECT_MODE` controls callback URIs; hosted workspace provisioning forces `localhost_manual` for built-in OpenAI/Anthropic OAuth.
- Callback state is consumed once and tokens are written only when provider, method, target, and config revision still match. Hosted SSO is server-only with canonical-origin/HMAC validation and process-local pending state, so one replica per workspace is required.
- Hosted cookies are centralized (`HttpOnly`, `SameSite=Lax`, configured `Secure`) and remain separate from local-auth cookies. Hosted deployments use Compose under `/docker/<project>/docker-compose.yml`, Traefik, and persistent `/data`.
- Local login rejects raw or encoded backslashes in `next` values. Authentication cookies are versioned with subsecond expiry while legacy token verification remains supported.
- One server/coding-agent Docker image is intentional. It runs as UID/GID `10001:10001`; mounted `/data` must already be writable by that identity. It includes common Go, Node.js, Python, Rust, Java, Ruby, and C/C++ toolchains but is not a complete sandbox.

Reflection, ownership, and handlers:
- Reflection `hour`, `day`, and `week` are rolling windows; `day` means the last 24 hours. Change statistics prefer app-produced `task_commit_stats` and use Git only for the true pre-stats range. Shared bounded numstat parsing covers task output, safety, dirty-worktree, merge/squash, conflict, rebase, and GitHub publication commits; fast-forward merges add no extra row, and generated artifacts later absent from the maintained branch are pollution.
- `internal/handler` is the Echo boundary: `handler.go` owns dependencies/routes while feature files own task, project, chat, model, auth, integration, SSE, worktree, HTMX, and API behavior.
- Compact project-aware projections are used for polling, metrics, detail lists, upcoming work, and the Schedule primary-Agent selector. The selector returns only `id`, `name`, and `model` after SQL filters enabled/selectable/non-archived/generated/project-or-global availability; full Agent reads remain for Task Detail/configuration.
- Scheduler active admission uses compact `TaskRepo.ListActivePendingAdmissions` metadata and leaves authoritative full hydration to `WorkerService.dispatchNext`, preserving exclusions, ordering, swarm-parent routing, and claim semantics. `UpcomingRepo` shares canonical projections and a bounded helper across running, pending, and scheduled lists without changing response shapes.
- Browser execution, review, lifecycle, goal, Insights, and configuration endpoints enforce project ownership before reading or mutating data and return controlled non-success responses without leaking prompts, outputs, skills, memory, events, goals, or analytics. Task-goal browser routes require an explicit non-empty `project_id` or selected-project boundary.
- Explicit task ID/title resolution is centralized in `internal/service/task_reference_resolver.go`; web/channel `current` normalization remains adapter-owned. Lifecycle activity is project-scoped and prompt-safe; realtime details belong in `realtime_and_frontend_patterns.md`.

Diagnostics and development:
- The repository targets Go `1.27.0`, canonicalized by `go.mod` and CI. Air is pinned to `v1.67.4`; `make dev` delegates to Air, and templ/Tailwind changes require their generators/watchers. `make build`, `make build-desktop`, and `make run` share conservative generated-output freshness checks, including Swagger inputs.
- Uncached broad suites have recurring baseline failures, including server-bootstrap protected-agent/read-only-database setup and a clean-main schedule viewport test. Report exact failures with the narrower passing scope rather than claiming the broad suite passed.
- `internal/applog` uses `Infof` for operational events and debug-gated `Debugf` for raw LLM/user content and high-frequency stream/SSE/diff/poll/provider traces. `internal/util/json.go` centralizes fenced/balanced JSON candidate extraction; callers retain schema validation and repair.
