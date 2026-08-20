---
name: openvibely_architecture
type: project
created: 2026-05-09
updated: 2026-08-19
source: after_complete
source_id: ce58dc08ee6cf3e3ca6d281fba596f35:9c3fcd168a95360b
confidence: high
title: OpenVibely Architecture
---

OpenVibely is an open-source Go application for automated task scheduling and AI-powered execution. Users create tasks, schedule them, and have LLM agents execute them automatically. The backend uses Echo v4, SQLite via `modernc.org/sqlite`, goose migrations, and a channel-based worker pool. The frontend is server-rendered with HTMX, templ, Tailwind CSS, and DaisyUI.

Server, desktop, and storage architecture:
- `internal/server.Start(ctx, cfg)` wires the shared backend and returns a server instance with bound address, base URL, and shutdown handle. Server and desktop share `internal/server`; backend forking is not intended.
- Local web/server release-binary runs default DB/repos/uploads/runtime state to `$HOME/.openvibely` unless an env override applies. Desktop defaults to OS application-data directories: macOS `~/Library/Application Support/OpenVibely`, Windows `%LOCALAPPDATA%\OpenVibely` with `%APPDATA%` fallback, and Linux `$XDG_DATA_HOME/openvibely` with `~/.local/share/openvibely` fallback.
- Hosted/Docker deployments use explicit env-driven storage such as mounted `/data` paths. Docker/VPS persist under mounted `/data` where applicable.
- Desktop mode uses `config.LoadWithMode(ModeDesktop)`, ephemeral `PORT=0`, local repo paths, and Wails WebView loading from the server base URL.
- `OPENVIBELY_APP_DATA_DIR` overrides the local app-data root and can deliberately point web/server and desktop at the same runtime state. It is a literal path; shell `~` expansion is not performed. `DATABASE_PATH` overrides database location even when `OPENVIBELY_APP_DATA_DIR` is set.
- Packaged desktop binaries read environment variables from `config.env` in the OS-conventional config directory, overridden by `OPENVIBELY_DESKTOP_CONFIG_FILE` when set.
- Desktop/Wails GUI launches may not inherit interactive shell `PATH`; task execution relies on centralized environment/PATH construction rather than hardcoded developer-tool paths.
- Desktop pages expose authoritative server runtime mode through render context/layout markers. Chat/task-thread safe external links use this marker plus `/open-external` rather than relying only on browser-visible Wails globals.
- Desktop file/folder selection UX favors Wails/native dialog APIs because browser-only upload features such as `webkitdirectory` are unreliable across native WebViews.

SQLite and runtime-state contracts:
- SQLite is intentionally configured with `MaxOpenConns(1)`, so long writes, cascades, full `VACUUM`, incremental vacuum, unindexed scans, and query fan-out can stall unrelated database-backed requests.
- SQLite startup enables `auto_vacuum=INCREMENTAL` before migrations through a one-time bootstrap that may require a full `VACUUM` when transitioning an existing database. That initial rebuild runs before the server context exists and can block startup for large databases; startup logs should warn before it begins. A background incremental reclaimer later runs short cancellable batches.
- High-cardinality task deletion paths must keep foreign-key and cleanup-ownership lookups indexed, especially `alerts.execution_id`, lifecycle parent references, task/execution attachments, and pending-upload session IDs.
- Single-task deletion, UI bulk deletion, Chat clearing, and non-default project deletion use a shared per-task cleanup boundary. Project deletion routes project-owned tasks through `TaskService` cleanup before deleting the project row while default-project deletion remains blocked.
- Attachment cleanup ownership is captured in the same SQLite transaction as cancellation/cascading task deletion. Filesystem cleanup runs only after relational deletion commits. Pending-file publication and retirement coordinate through a process-wide session fence plus durable `retired_attachment_sessions` tombstones; retired client-supplied sessions return HTTP 409.
- Startup orphaned-file reconciliation for task and chat attachments shares repository-level walk/delete/prune helpers while preserving task-vs-chat upload-root boundaries.
- Storage changes must remain compatible across Docker/VPS, local server, and desktop deployments.
- Local storage migrations preserve existing user state by moving/copying old local database, SQLite sidecars, repos, uploads, and related runtime directories into `$HOME/.openvibely` when no explicit storage override is set. `OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION` skips this migration.
- `OPENVIBELY_RUNTIME_DIR` is a deprecated `start.sh`-only alias for `OPENVIBELY_APP_DATA_DIR`; the binary does not read it directly.
- Release binaries use stable app-owned storage rather than source-checkout/current-working-directory paths such as `./openvibely.db` or `./repos`.
- Local runtime-state diagnosis depends on the active process, port, and database path because multiple local/server/desktop instances may use different configured storage roots.

Worker and scheduling contracts:
- Unlimited global worker limit is represented as `worker_settings.max_workers = 0`. Fresh DB initialization and repository fallback use `0`; dispatch and follow-up admission treat it as unbounded, API serialization preserves `0`, and Settings surfaces it as “Unlimited.” Existing finite limits must not be migrated to `0`.
- No env/config override exists for global worker limit; it is persisted in settings. Task-thread follow-ups enforce finite global limits by atomically reserving global and project capacity before provider execution.
- Schedule repeat intervals have canonical bounded-positive contract `1..365` for every recurrence unit, enforced across browser handlers, repositories, Automation persistence/compilation, web runtime tools, and channel construction. Corrupt persisted intervals must not block later valid work.
- Open bug `#116`: direct schedule create/update handlers can coerce malformed/overflowing repeat intervals to `1` or surface unsupported recurrence types as repository errors instead of controlled HTTP 400 responses.
- New schedule creation is normalized through `ScheduleActionService.CreateForTask` for browser, scheduled-task creation, runtime, and channel paths. Browser routes use absolute-time form methods; runtime/channel methods keep HH:MM tool contracts.
- Creating a one-time schedule for a completed/failed task through browser or runtime resets terminal non-running targets to pending so the due scheduler can submit them. Running targets remain running; recurring already-scheduled completed/failed tasks preserve scheduler-time reset behavior.
- Initial `NextRun` for newly created schedules follows the repository lower-level rule `NextRun = RunAt`, including recurring schedules whose `run_at` is already in the past.
- Direct schedule create/update non-HTMX redirects share a task-schedules redirect helper preserving project scope.
- Open bug `#714`: browser/API schedule management endpoints registered globally by schedule ID can update, toggle, delete, or drag/drop-reschedule schedules outside the selected/current project; runtime-tool schedule paths already enforce project ownership.

OAuth and hosted deployment facts:
- Model OAuth initiate/callback resolves absolute app URLs through `APP_BASE_URL` first, then forwarded/request host fallback.
- `OAUTH_REDIRECT_MODE` controls the provider-facing callback URI, not where the authorization page opens. `auto` may use public `APP_BASE_URL`, `hosted` requires it, and `localhost_manual` deliberately uses localhost callbacks even when a public app URL exists.
- Hosted workspace provisioning forces `OAUTH_REDIRECT_MODE=localhost_manual` because built-in OpenAI and Anthropic OAuth clients require localhost callbacks. Users paste failed localhost callback URLs into the Models manual-completion UI.
- OAuth browser-launch behavior is runtime-specific and decoupled from redirect mode: web/server uses ordinary navigation in the remote browser; desktop may request local system-browser launch. Hosted `localhost_manual` must not imply desktop-style external launch.
- OAuth callback completion is centralized in one transport-neutral handler operation that consumes pending flow state exactly once and performs exchange/token persistence; rendering/logging remain adapter-specific.
- `start.sh` uses `${VAR+x}` presence checks so unset and explicitly empty env vars differ while staying compatible with macOS Bash 3.2.
- Hosted workspace SSO is server-only, uses explicit `hosted_sso` auth mode, validates canonical origins/instance/HMAC key, and must be rejected in desktop mode. Process-local pending SSO state requires exactly one app replica per hosted workspace until shared pending storage exists.
- Hosted deployments use Docker Compose projects under `/docker/<project>/docker-compose.yml`, Traefik routing, and persistent `/data`; live Compose/image state must be verified before rollout.

Docker image direction:
- OpenVibely publishes one server-and-coding-agent image because the server executes coding agents; there is no separate remote-executor architecture that justifies a minimal server-only image.
- The runtime image includes common Go, Node.js, Python, Rust, Java, Ruby, and C/C++ toolchains and excludes Podman/Buildah. Fedora's shorter support cycle requires regular base-version upgrades.
- Final OCI user is `10001:10001`; entrypoint override remains non-root and mounted `/data` must already be writable by that UID/GID. Runtime ownership mutation and legacy-volume migration guidance are intentionally excluded.
- The image removes `sudo` and setuid/setgid bits after package installation. Non-root execution is defense in depth, not a complete sandbox.

Diagnostics, handlers, and local development:
- When the user asks about local SQLite size or vacuum behavior, inspect live DB `$HOME/.openvibely/openvibely.db` read-only, including sidecars. Do not run checkpoints, vacuum, or modifying diagnostics unless explicitly asked.
- For read-only live DB diagnostics, inspect schema with `PRAGMA table_info(<table>)` before writing SQL. Remember `tasks` uses `created_at`, not `started_at`; execution timing is on `executions`; swarm role/state columns are on `tasks`; execution failure/diff fields are `error_message` and `diff_output`.
- `internal/handler` is the Echo HTTP boundary. `handler.go` owns shared dependency graph and route registration; feature-specific files attach tasks, projects, chat, models, auth, integrations, SSE, worktrees, and HTMX/API methods.
- Swagger/OpenAPI is generated with `swag init`; generated docs live under `docs/`, with UI at `/swagger/*`.
- User-facing docs generally live under `docs/` as concise `*-user-guide.md` Markdown pages.
- `make dev` currently delegates directly to `air`; without `.air.toml`, it provides backend rebuild/restart only, not browser hot reload.
- Editing `.templ` files requires `templ generate` or a watch process. Tailwind/CSS changes require a separate Tailwind build/watch process.
- OpenVibely logging uses `internal/applog`: `Infof` for operational logs and `Debugf` gated by `OPENVIBELY_LOG_LEVEL=debug`. Raw LLM/user content, high-frequency stream/SSE/diff/poll/routing traces, and provider headers/rate-limit dumps are debug-level diagnostics, not info logs.
