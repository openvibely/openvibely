---
name: openvibely_architecture
type: project
created: 2026-05-09
updated: 2026-07-28
source: consolidation_and_task_turns
source_id: memory_consolidation_2026_07_27;abf902e6c55aa6881d2525168bc5e41c:47b8db2b38eb30b8
confidence: high
title: OpenVibely Architecture
---

OpenVibely is an open-source Go application for automated task scheduling and AI-powered execution. Users create tasks, schedule them, and have LLM agents execute them automatically. The backend uses Echo v4, SQLite via `modernc.org/sqlite`, goose migrations, and a channel-based worker pool. The frontend is server-rendered with HTMX, templ, Tailwind CSS, and DaisyUI.

Dual mode architecture:
- `internal/server.Start(ctx, cfg)` wires the shared backend and returns a server instance with bound address, base URL, and shutdown handle.
- Local web/server and desktop release-binary runs default DB/repos/uploads and related runtime state to the same user app-data directory, specifically `$HOME/.openvibely` unless an env override applies.
- Hosted/Docker deployments use explicit env-driven storage such as mounted `/data` paths rather than local `$HOME/.openvibely` behavior.
- Desktop mode (`cmd/desktop`) uses `config.LoadWithMode(ModeDesktop)`, ephemeral port `PORT=0`, local repo paths, and Wails WebView loading from the server base URL.
- `make package-desktop-macos` builds a raw intermediate executable and packages it as `OpenVibely.app/Contents/MacOS/OpenVibely`; release packaging pitfalls belong with release-discipline memory.
- `OPENVIBELY_APP_DATA_DIR` is the shared override for the local app-data root when users need web/server and desktop to point at the same runtime state. It is read as a literal path; shell `~` expansion is not performed.
- Env vars override mode defaults. `DATABASE_PATH`, `PROJECT_REPO_ROOT`, and related storage env vars remain explicit paths; `DATABASE_PATH` overrides database location even when `OPENVIBELY_APP_DATA_DIR` is set.
- Desktop defaults to localhost OAuth callback flow (`APP_BASE_URL` unset). Provider OAuth callback selection is independent of where the authorization page opens; hosted workspaces may retain a public `APP_BASE_URL` while forcing `OAUTH_REDIRECT_MODE=localhost_manual` for providers whose registered clients reject non-localhost redirects.
- Desktop/Wails GUI launches, especially on macOS, may not inherit the user's interactive shell `PATH`; task execution relies on centralized environment/PATH construction rather than hardcoded developer-tool paths.
- The packaged desktop binary reads environment variables from `config.env` in the OS-conventional config directory, overridden by `OPENVIBELY_DESKTOP_CONFIG_FILE` when set.
- Desktop external-link handling depends on the actual loaded Wails runtime API because the WebView may be loaded from the local server URL rather than a `wails:` page.
- Desktop file/folder selection UX favors Wails/native dialog APIs because browser-only upload features such as `webkitdirectory` are not consistently reliable across OS-native WebViews.
- Server and desktop share `internal/server`; backend forking is not part of the intended architecture.

Storage and runtime-state pitfalls:
- The SQLite pool is intentionally configured with `MaxOpenConns(1)`, so a long write or cascade monopolizes all database-backed requests; schema changes on high-cardinality deletion paths must account for this responsiveness constraint.
- Every foreign-key and cleanup-ownership lookup on high-cardinality task deletion paths must remain indexed, including `alerts.execution_id`, the self-referencing `lifecycle_executions.parent_execution_id`, task/execution attachment paths, and pending-upload session ID. Nullable-reference indexes are partial where appropriate. Before migration 130, deleting a task with many executions repeatedly scanned unrelated alerts; before migration 132, each cascaded lifecycle execution scanned the full lifecycle table to apply `parent_execution_id ON DELETE SET NULL`. Either scan can monopolize the sole SQLite connection for seconds and freeze unrelated database-backed requests.
- Single-task and UI-exposed completed/backlog/chat bulk deletion use the same per-task repository boundary. Cleanup ownership for finalized task attachments, execution/chat attachments, and queued thread-input pending-upload sessions is captured in the same SQLite transaction as cancellation and cascading task deletion. Real queued Chat rows have no `task_id`; their pending sessions are resolved through `run_execution_id -> executions.task_id`, while shared or ambiguous sessions referenced by another task/project are retained. The manifest query uses indexed task, run-execution, and session-ownership lookups. Because the sole database connection remains reserved through commit, a concurrent upload metadata write either enters the manifest first or resumes after deletion and fails its foreign key.
- Upload filesystem cleanup runs synchronously only after relational deletion commits. Migration 131 adds durable `retired_attachment_sessions` tombstones and SQLite triggers that reject any later `thread_inputs` owner for a retired session. Failed-publication rollback atomically retires an unowned session before removing its directory, while task deletion retires captured sessions in the same transaction as the relational delete; a failed task delete rolls retirement back too. Pending-file publication is also fenced against retirement: upload, failed-publication cleanup, and post-task-deletion cleanup coordinate through a process-wide lock keyed by session ID, and upload checks the indexed tombstone while holding that session fence before creating or appending files. A retired client-supplied session returns HTTP 409 without recreating its directory; unrelated sessions remain independent, and SQLite transactions are not held across filesystem writes. This closes ownership and publication races while keeping filesystem work outside SQLite. Database deletion failure preserves metadata and files; post-delete cleanup failures are surfaced; unsafe pending session IDs and missing configured upload roots fail before cancellation/deletion; and files or sessions still referenced by surviving tasks are retained. API Chat publication rolls back newly written immediate attachment files and empty execution directories when attachment metadata insertion fails, and removes newly written pending-session directories when queued-input metadata insertion fails, including deletion races. Browser publication rollback covers queued Chat, steering Chat, queued task-thread, direct Chat task/create/claim/execution failures, direct task-thread execution creation failures, and stale task-thread not-found submissions. Managed task worktrees are not directly removed by task deletion; existing lineage-aware orphan cleanup owns that lifecycle. Git, worktree, and attachment filesystem operations remain outside the SQLite deletion transaction.
- Storage changes maintain compatibility across Docker/VPS, local server, and desktop deployments.
- Docker/VPS persist under mounted `/data` paths where applicable.
- Local storage migrations preserve existing user state by moving/copying the old local database, SQLite sidecars, repos, uploads, and related runtime directories into `$HOME/.openvibely` when no explicit storage override is set. `OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION` skips this migration.
- `OPENVIBELY_RUNTIME_DIR` is a deprecated `start.sh`-only alias for `OPENVIBELY_APP_DATA_DIR`; it is not read by the binary directly.
- Release binaries use stable app-owned storage rather than source-checkout/current-working-directory paths such as `./openvibely.db` or `./repos`.
- Web/server and desktop local runs are expected to use the same database by default unless env vars explicitly separate them.
- Local runtime-state diagnosis depends on the active process, port, and database path because multiple local/server/desktop instances may use different configured storage roots.

Worker capacity settings:
- The canonical representation for an unlimited global worker limit is `worker_settings.max_workers = 0`. Fresh database initialization and repository fallback use `0`; queued-worker dispatch and task-thread follow-up admission treat it as unbounded rather than zero capacity, API serialization retains `max_workers: 0` while reporting capacity available, and Settings surfaces it as “Unlimited” and round-trips `0`.
- Existing persisted finite limits, including `1`, must remain unchanged. There is intentionally no upgrade migration from `1` to `0` because historical implicit defaults cannot be distinguished reliably from an explicit user selection.
- No environment/config override exists for the global worker limit; it is persisted in settings.
- Task-thread follow-ups enforce finite global worker limits by atomically reserving global and project capacity before provider execution, while preserving cancellable queue waits, exact counter cleanup, and `0 = unlimited` behavior.

OAuth and hosted deployment facts:
- Model OAuth initiate/callback resolves absolute app URLs through shared URL-building behavior: `APP_BASE_URL` first, then forwarded/request host fallback.
- `OAUTH_REDIRECT_MODE` controls the provider-facing callback URI, not where the authorization page opens. `auto` may use the public `APP_BASE_URL`, `hosted` requires it, and `localhost_manual` deliberately uses fixed localhost callbacks even when a public app URL exists.
- Hosted workspace provisioning forces `OAUTH_REDIRECT_MODE=localhost_manual` because the built-in OpenAI and Anthropic OAuth clients require localhost callbacks. OpenAI uses `http://localhost:1455/auth/callback`; Anthropic uses `http://localhost:53692/callback`. Both retain process-local state and PKCE data, then rely on the user pasting the failed localhost callback URL into the Models manual-completion UI.
- The Models OAuth browser-launch location must remain runtime-specific and decoupled from redirect mode: web/server runtime should use ordinary navigation so the remote user's browser follows the provider redirect, while desktop runtime may request a local system-browser launch.
- Historical hosted OAuth regression: Models OAuth clicks were changed to AJAX with unconditional external launch, causing hosted headless containers to call `browser.OpenURL` and return 502 before an otherwise valid manual callback flow began. The working server behavior is normal navigation in the remote user’s browser; hosted `localhost_manual` callback mode must not imply desktop-style browser launch.
- Hosted/server and desktop OAuth launch behavior is separated by authoritative server runtime mode. Web/server renders ordinary project-scoped navigation and ignores forged external-launch requests; desktop requests the background system-browser path. This is independent of provider callback redirect mode and does not rely only on browser-visible Wails globals.
- Desktop OAuth launch mode must come from authoritative server runtime mode in rendered UI, not only browser-side Wails-global detection or a `data-runtime` marker derived from it. The desktop WebView loads the backend over HTTP, where Wails runtime globals may be absent; relying on those globals can misclassify desktop as web and navigate OAuth inside the WebView instead of opening the system browser.
- `start.sh` deliberately uses `${VAR+x}` presence checks to distinguish an unset variable from one explicitly set to an empty string while remaining compatible with macOS Bash 3.2. Its local `ENVIRONMENT=development` default stays unexported when `ENVIRONMENT` was absent so hosted SSO does not mistake the script default for explicit operator authorization of development-mode HTTP; explicitly empty hosted SSO settings are exported so Go configuration rejects them rather than treating SSO as unrequested.
- Hosted workspace SSO is server-only and uses an explicit `hosted_sso` auth mode that takes precedence over rolling-deployment local credentials. Configuration strictly validates canonical hosted origins, immutable instance ID, and a canonical 32-byte base64url HMAC key; desktop mode must reject hosted SSO even when related flags are false, while non-hosted local-auth behavior remains compatible.
- Hosted workspace SSO uses backend authorization-code exchange with PKCE, exact instance/email-verification checks, one-hour host-only versioned `ov_session` cookies, purpose-separated HMAC domains, and an authenticated `ov_sso_browser` binding. Pending transactions are process-local, ten-minute, bounded per browser and globally, and globally rate-limited.
- Hosted SSO injects validated canonical `APP_BASE_URL` into SSO and existing OAuth/absolute-URL consumers, hardens auth-route method override and path-only request logging, defines workspace-local logout with exact-origin CSRF validation, and preserves public callback/webhook routes.
- Process-local pending SSO state requires exactly one application replica per hosted workspace until shared pending storage exists. Rollout requires publishing the client in the workspace image and deliberately recreating/upgrading containers while retaining `/data`; exact `CONTROL_BASE_URL`, stored workspace `AppBaseURL`, and public hostname must agree.
- Hosted control-plane image tags, environment overrides, and deployed image contents are operational state. Verify the live Compose configuration before rollout rather than assuming an override documented in `.env.example` is active.
- Hosted deployments use Docker Compose projects under `/docker/<project>/docker-compose.yml` and route app/docs containers through Traefik with persistent `/data` storage. Exact host inventory is operational state and must be verified live before acting on it.

Live DB inspection facts:
- For read-only diagnosis against the live app DB (`$HOME/.openvibely/openvibely.db`), the `tasks` table has no `role` column; swarm role/state live in `swarm_role`, `swarm_status`, `swarm_config`, `swarm_sequence` (plus `parent_task_id`, `category`, `status`, `worktree_path`, `worktree_branch`, `merge_status`). The `executions` table has no `created_at`/`diff` columns; use `started_at`/`completed_at` for timing and `error_message`/`diff_output` for failure text/diff. Run `PRAGMA table_info(<table>)` first when unsure rather than guessing column names.

Shared conventions:
- `internal/handler` is the Echo HTTP boundary: `handler.go` owns the shared `Handler` dependency graph and route registration, while feature-specific files attach methods for tasks, projects, chat, models, auth, integrations, SSE, worktrees, and HTMX/API surfaces.
- Templates and JS reuse existing shared helpers/utilities and feature-detect optional elements.
- UI code uses existing components/partials and shared CSS tokens instead of duplicating styles.
- Swagger/OpenAPI is generated with `swag init`; generated docs live under `docs/` and the UI is mounted at `/swagger/*`.
- User-facing documentation also lives under `docs/`, generally as concise `*-user-guide.md` Markdown pages that match the existing guide structure. README/docs-site preferences live in `coding_agent_product_discipline.md`.

Local development workflow:
- `make dev` currently delegates directly to `air`; without a repo `.air.toml`, it only provides backend rebuild/restart behavior for Go changes through Air defaults, not browser hot reload.
- Editing `.templ` files requires `templ generate` or a separate `templ generate --watch` process before Go rebuilds see template changes.
- Tailwind/CSS changes require a separate Tailwind build/watch process; `make dev` does not currently run one.

Runtime logging:
- OpenVibely uses `internal/applog` as the runtime logging facade: `Infof` emits operational logs, while `Debugf` is gated by `OPENVIBELY_LOG_LEVEL=debug` and defaults off at info level.
- `start.sh` defaults `OPENVIBELY_LOG_LEVEL` to `info` while preserving env or `.env` overrides.
- App logging across internal/pkg/cmd code should go through `applog` unless a component intentionally owns a `*log.Logger` dependency or uses fatal/setup-only standard-log behavior.
- Raw LLM/user content, high-frequency stream/SSE/diff/poll/routing traces, and OpenAI/Anthropic provider header or rate-limit dumps are debug-level diagnostics, not info logs.
- Streaming persistence errors and final one-time stream lifecycle summaries can remain info-level, but periodic/eager streaming flush success counters during active generation are debug-level noise.
