---
kind: openvibely.agent_skill
version: 9
skill:
    key: openvibely_project_guidance
    name: OpenVibely Project Guidance
    scope: project
    description: Static coding-agent guidance for working in the OpenVibely repository.
---

# OpenVibely Project Guidance

Use this project-managed skill for coding-agent work in the OpenVibely repository or closely related sibling repos. Durable project context, feature decisions, user preferences, feedback, and task/chat lessons belong in managed memory under this repository's `.openvibely/memories/` directory, not root markdown instruction files.

## Stop First

- Never create documentation or summary files for your work. Do not create `*_FIX.md`, `*_SUMMARY.md`, `*_VERIFICATION.md`, `README_*.md`, `TECHNICAL_*.md`, `ACTION_PLAN_*.md`, `FINDINGS_*.md`, `INVESTIGATION_*.md`, `COMMIT_MESSAGE.txt`, QA checklist files, or similar summary markdown. The code and commit messages are the documentation.
- Before editing code, verify the actual filesystem target. Run or otherwise confirm `pwd`, `git status --short --branch`, and `git worktree list` when the task mentions an isolated worktree, follow-up task branch, or explicit worktree path. If the current tool working directory is not the assigned worktree, use absolute paths into the assigned worktree for file tools and `git -C <worktree>`/`cd <worktree> && ...` for shell commands. Do not rely on relative paths until this is confirmed; accidental edits to the main checkout must be treated as a workflow bug and repaired before continuing.
- In an audit-only review, gather repository/worktree evidence before any `memory_view`, `skill_view`, or other managed-artifact lookup when memories or skills are allowed but their drift must be ignored. Memory-first or skill-first orientation invalidates a clean audit verdict; if this ordering boundary is violated, stop and report that a fresh compliant audit is required instead of continuing to inspect or claiming no material findings. If managed artifacts are explicitly excluded, make zero memory or skill tool calls for the entire audit turn.
- If the assigned isolated worktree directory itself has disappeared mid-task (shell commands fail at `chdir`/cwd-not-found even though an earlier read succeeded, or the path is missing from `.worktrees`), do not silently work in the main checkout and do not just recreate an empty bootstrap folder. Inspect the main repository's git metadata (e.g. `git -C <main_repo> worktree list`, `git -C <main_repo> branch --list 'task/<task_id>*'`) to determine whether the task branch still exists, then recreate the isolated worktree at the assigned path on that task branch (`git -C <main_repo> worktree add <assigned_path> <task_branch>`, or from `main` if the branch is also gone) before making any code changes. Re-verify the recreated worktree is a real git checkout (not just a directory) before editing.
- When a user asks for an end-user channel action such as sending an email, Slack, Telegram, or Discord message, only use tools actually exposed in the current turn. `send_message` is an OpenVibely runtime tool available to agents running inside the task runner; an external coding-agent session cannot call it unless it is listed in the active tool surface. Do not imply a runtime send occurred if no callable send tool exists; either use a verified local/server delivery path, ask for permission to check one, or provide the composed content for the user to send.
- When a task is explicitly governed by a source-of-truth spec or runbook path, read that file before edits. If the exact file is missing, first verify the worktree/path and search for the exact filename or a clearly matching spec in the repo; if still absent, stop and report a blocker instead of implementing from assumptions or similarly named docs.
- Avoid validation loops, but do not leave compile or test failures unresolved after code edits. Make all intended code changes first, run the required validation chain once, fix any failures, and rerun only when needed to confirm the fix.
- If code changed, use `-count=1` for tests. Skip it only when re-running without code changes and cached results are acceptable.
- Avoid read-only tool loops. Repeated `read_file`, `list_files`, and `grep_search` calls without progress usually mean missing objective or weak handoff context.
- For `edit_file`, expand stable surrounding context when a replacement fails instead of retrying the same snippet. Use bulk replacement only when intentionally replacing every occurrence.
- When a user asks to intentionally fail a task to trigger OpenVibely failure alerts, output the explicit failure marker required by the current runtime/instructions; merely describing an exit code or claiming failure is insufficient. If the marker is not already in the prompt, inspect the relevant marker-extraction instructions/code and emit the exact marker, not a paraphrase.
- When a user asks to explain something better, says "no word salad", or challenges a prior diagnosis, explain the exact current bug, fix, or evidence in plain causal terms. Re-read the current diff/context if needed; do not paste unrelated instruction templates, cite stale commits as proof of a live fix, or claim a bug is fixed without current validation evidence.
- When a user pushes back mid-task with a short directive observation that clearly implies a design change (e.g. pointing out an existing global setting makes a proposed per-entity setting inconsistent), treat that as direction to implement, not an open question. Do not restate the tradeoff and ask "should I implement that change?" — make the change, then report what changed. Reserve an actual clarifying question for cases where the user's intent is genuinely ambiguous or the change is destructive/irreversible, not for ordinary design agreement.

## Critical Rules

- Never delete, truncate, or overwrite `openvibely.db`; it contains user data.
- Never run `DROP TABLE` on production tables except in goose migrations.
- Never run `DELETE FROM` without a `WHERE` clause on production tables.
- Never change `busy_timeout`, `MaxOpenConns`, or `_loc=UTC` in `internal/database/database.go`.
- Never write tests that hit real LLM APIs or spawn CLI subprocesses. Use `models.ProviderTest` with `SetLLMCaller(testutil.NewMockLLMCaller())`.
- Strip `CLAUDECODE` from the environment when spawning Claude CLI subprocesses.
- Never persist or log GitHub App installation access tokens, GitHub PATs, private-key material, OAuth tokens, API keys, or webhook secrets. Mint operation tokens per operation and keep token use in process.
- Do not print raw prompts, streamed model tokens, provider payloads, OAuth/API-key data, or other content-carrying LLM data at info level. In high-frequency streaming paths, especially `internal/llm/stream.Writer`, do not call logging methods per chunk in normal code; leave raw stream `Debugf` instrumentation commented out and only temporarily uncomment it for a debugging session. For lower-frequency raw stream diagnostics outside hot chunk loops, use `internal/applog.Debugf` gated by `OPENVIBELY_LOG_LEVEL=debug`.
- Server-side git commands that may contact remotes must run non-interactively and use the same GitHub operation-token environment injection as clone/push paths.
- Use `TaskRepo.ClaimTask()` for atomic task claiming. Never set task status to `running` directly.
- Use parameterized SQL with `?` placeholders.
- Use `context.Context` for database and service calls.
- Respect foreign-key and CHECK constraints in test fixtures. Create referenced rows first and use valid enum values.

## Development Workflow

- Confirm where a change belongs before editing. Follow the layered architecture: `models -> repository -> service -> handler -> templates`.
- `models`: plain structs and domain rules.
- `repository`: raw SQL access with context-aware calls.
- `service`: orchestration and business logic.
- `handler`: HTTP parsing/rendering and response shaping.
- Prefer coherent end-to-end slices through the proper layers over scattered one-off edits.
- Keep changes minimal, explicit, and directly tied to the request.
- Do not add features, broad refactors, compatibility shims, fallback migrations, or abstractions unless the task requires them.
- When implementing a runbook/spec with exclusions, treat every non-excluded section as in scope.
- Identify the underlying product concept before coding; do not derive major behavior from incidental implementation shape such as tool lists, default flags, or temporary code structure.
- Put product policy that affects workflow, isolation, data writes, recovery, or review in explicit configuration, state, or data model.
- Keep generic capabilities generic. Model exceptional built-in-agent or workflow behavior through explicit configuration instead of hidden one-off cases.
- Derive environment/path values from authoritative user or system sources instead of hardcoded guessed locations. Project root, isolated worktree root, process working directory, durable repo location, app data root, and tool scope root are distinct concepts.
- Prefer product-correct defaults over mechanically convenient defaults.
- When editing model-facing prompts, preserve only context that helps the model act correctly. Do not add product names or internal category labels such as `OpenVibely`, `built-in system agent`, `system agent configuration`, or `non-system agent` just to make text sound project-specific; prefer direct role and boundary wording like `Skill Curator`, `Memory Curator`, `protected agent`, or `user-managed agent` when that distinction matters.
- Treat runtime tool descriptions, generated-agent/repair prompts, bundled agent root and skill bodies, scheduled task prompt bodies/titles, lifecycle prompt constants/templates, and prompt-safe hook input JSON as model-facing prompt surfaces. Audit them together for low-value product names or internal labels, not only files or constants named `Prompt`.
- Keep long model prompts as readable const templates with dynamic context interpolated, not chains of `WriteString` calls.
- Use logs intentionally. `logs/openvibely.log` is useful for behavior verification and diagnosis.
- When auditing runtime log noise, inspect `logs/openvibely.log` first and classify logs by operational value: keep errors, startup/shutdown, CRUD, task/execution creation, SSE lifecycle, completion metrics, and unusual state transitions at info; demote or comment out high-frequency HTMX poll/request counts, stream/delta/diff tick counters, and any content-carrying payloads or messages. Do not comment out every debug log: keep useful low-frequency diagnostics as active `applog.Debugf(...)` calls, and reserve fully commented-out debug instrumentation for hot loops, frequent polling, or payload dumps where even the method-call overhead or argument construction is wasteful. `start.sh` should default `OPENVIBELY_LOG_LEVEL` to `info` while allowing env or `.env` override.
- For handlers with setter-injected optional dependencies, validate required dependencies at handler entry and return controlled HTTP errors instead of nil-pointer panics.
- When introducing behavior modes, propagate mode through typed request contracts and enforce behavior in provider/tool policy layers, not only in prompt text.
- For task-execution actions, prefer exact entity targeting by `task_id` or `title`; reserve tag/priority filters for explicit group execution requests.
- When a user requests a task or application mutation, use only an authorized runtime tool or direct local API actually exposed for the turn. Model-emitted bracket text is ordinary prose and must never be used as a mutation fallback; do not claim success without a successful tool/API result. Use `category=backlog` when the task must remain non-running.
- If tasks run in isolated worktrees, include explicit worktree orientation in the model prompt while keeping runtime workdir enforcement as the source of truth.
- When implementing, debugging, or explaining custom Automation graph tasks, treat `Task` as one generic public node capability. Derive behavior from the exact topology: an ordinary Task is materialized when the Automation is saved, while `GitHub inbox -> Task -> Open pull request` configures issue-specific tasks that are created later by the inbox and must not create one shared task during Save. Keep legacy published `role=implementation` graphs runnable and normalize that role to `task` when edited/saved. Runtime task-template lookup must require the exact persisted inbox/Task/PR topology; never classify an arbitrary connected Task or ordinary Task-to-Task chain as issue-specific work.
- Any web/API/channel runtime tool handler that accepts `task_id` must resolve a `"current"` sentinel (and any omitted `task_id`/`title` during a task-thread follow-up) through `Handler.resolveTaskIDForTool` (`internal/handler/chat_action_tools.go`), not by passing the raw string into `resolveTaskReference`/`GetByID`. `resolveTaskIDForTool` is the single place that maps `"current"` to `params.TaskID` when `params.IsTaskFollowup`, and rejects it otherwise. A handler that skips this and calls `resolveTaskReference` directly (as `view_task_thread` once did) fails with a literal `task current not found` when the model sends `task_id=current`. When adding or auditing a new task-id-accepting web-surface tool, verify it routes through `resolveTaskIDForTool` and add a regression covering explicit `"current"`, omitted `task_id`/`title` during a follow-up, and rejection outside a follow-up context.

## Project Overview Requests

- When the user asks to explain or summarize this project, inspect the README plus a small set of current entry points or package directories before answering; do not rely only on remembered project context.
- Do not run build or test validation for read-only overview requests unless the user asks for verification.
- Only include setup or run commands in the summary when you have read exact commands from repository files. Quote those actual commands, and omit the commands section entirely rather than leaving empty fenced code blocks, placeholders, or guessed commands.

## Adding Features

- New tables or columns require a new migration in `internal/database/migrations/` using the existing goose numbering pattern.
- New models belong in `internal/models/`, repositories in `internal/repository/`, services in `internal/service/`, and handlers in `internal/handler/`.
- Register new handlers and routes in `internal/handler/handler.go`.
- Update templates under `web/templates/**/*.templ` when UI changes are needed, then run `templ generate`.

## Testing And Validation

- Always create or update tests when fixing bugs or adding features.
- Every bug fix needs a regression test that reproduces the failure scenario.
- For cross-layer production changes, cover the touched wiring/call-site layer as well as lower-level service behavior.
- For consistent UI/API/provider/mode bugs, reproduce the exact reported path and verify the final provider-bound request or tool payload when relevant.
- For task-thread UI follow-up behavior, lifecycle DB rows, intermediate context objects, direct helper tests, and adjacent tool/API paths are not enough by themselves.
- Use `testutil.NewTestDB(t)` for DB-backed tests in the main Go app. It accepts `testing.TB`, so the same helper works for benchmarks (`testing.B`) when writing before/after performance benchmarks, not only regular `*testing.T` tests.
- Never use `t.Parallel()` with shared database connections.
- Production baseline should not assume a default model config. In tests, use `testutil.NewTestDB(t)` or create one explicitly.
- Run `templ generate` after modifying `.templ` files.
- After main Go app code changes, run the required validation chain at the end: `go build ./cmd/server && go test ./internal/... -count=1 -timeout 60s`. Include `templ generate &&` first if `.templ` files changed.

## Running

```bash
./start.sh              # Start server (logs to logs/openvibely.log)
make dev                # Runs air for Go backend rebuild/restart on Go changes
```

`make dev` is not a complete browser hot-reload pipeline. In this repo it invokes `air`; when no `.air.toml` is present, air falls back to default Go-file watching/rebuild behavior. For UI work, `.templ` edits still need `templ generate` (or a separate templ watch) before the generated Go changes are picked up, Tailwind/CSS changes need their own configured watcher or rebuild, and the browser generally needs a manual refresh after the server restarts.

```bash
go test ./internal/... -count=1 -timeout 60s  # Focused/internal tests; use the validation workflow for full-suite commands and timeout guidance
```

## Key Files

| What | Where |
|------|-------|
| Entry point | `cmd/server/main.go` |
| Database setup | `internal/database/database.go` |
| Migrations | `internal/database/migrations/*.sql` |
| Models | `internal/models/*.go` |
| Repositories | `internal/repository/*_repo.go` |
| Services | `internal/service/*_service.go` |
| HTTP Handlers | `internal/handler/*_handler.go` |
| Route registration | `internal/handler/handler.go` |
| Templates | `web/templates/**/*.templ` |
| Test helper | `internal/testutil/testdb.go` |

## Data Access

- Repositories use raw SQL, not an ORM.
- Prefer `QueryRowContext` with `RETURNING` for inserts that need created row data.
- Enforce cross-row invariants inside repository transactions so behavior remains correct when handlers or UI bypass optional workflows.
- When a repository transaction runs on a dedicated connection, do not commit and then call a repository read through `*sql.DB` while that connection is still held. Single-connection tests can deadlock waiting for the same connection; assemble the result with transaction-scoped queries before commit, or release the connection before the follow-up read.
- When adding columns, update every SELECT that scans the struct, not only `GetByID` or list methods.
- Task SELECT mappings must include all current task columns, including follow-up, worktree, merge, lineage, origin, and agent-definition fields.
- Valid CHECK values include agent provider `anthropic`, `openai`, `ollama`, `test`; auth method `cli`, `oauth`, `api_key`; task status `pending`, `queued`, `running`, `completed`, `failed`, `cancelled`, `blocked`; task merge status `''`, `pending`, `merged`, `failed`, `conflict`; schedule repeat type `once`, `seconds`, `minutes`, `hours`, `daily`, `weekly`, `monthly`.
- `models.Agent` and the `agents` table do not include a `color` field. Do not add it back.
- `projects` includes both `repo_path` and `repo_url`; keep model and repository CRUD mappings symmetric.
- SQLite table recreation migrations must use `-- +goose NO TRANSACTION`, disable and reenable foreign keys, recreate all indexes, and preserve CHECK constraints.

## Time And Scheduling

- Parse `datetime-local` form inputs with `time.ParseInLocation("2006-01-02T15:04", value, time.Local)`, not `time.Parse()`.
- Convert times to local with `.Local()` before formatting in templates.
- For daily, weekly, and monthly schedule `ComputeNextRun`, convert to local time, use `AddDate`, then convert back to UTC so DST transitions are safe.
- Do not use `time.Add(N*time.Hour)` for day arithmetic; use `time.AddDate(0, 0, N)`.
- `ComputeNextRun` for one-time schedules returns `nil` after execution.

## Frontend Practices

- Use HTMX forms with explicit `method="post"` and return the appropriate fragment/container.
- Keep client-side behavior deterministic. Avoid duplicate listener registration and brittle swap assumptions.
- Scope page-specific selectors to unique roots such as `#chat-page-root`; avoid generic `[data-project-id]` selectors that appear on multiple pages.
- For inline scripts inside HTMX-swapped fragments, use window-level one-time binding guards.
- Prefer app-scoped `HX-Trigger` events bridged in the base layout for cross-page toast feedback.
- For async actions, show explicit in-progress state, disable conflicting actions, and restore state in `finally`.
- Pair template-level action gating with server-side enforcement.
- Guard async refreshes against out-of-order responses.
- Batch high-frequency streaming UI updates with `requestAnimationFrame` and flush on completion.
- Preserve drafts during polling by keying them to entity identity and clearing only on successful intentional submits.
- Route Chat/task-thread Markdown through the base layout's shared code-range parser: escape raw tag openers outside valid escaped/unmatched/multiline inline and fenced code before Marked, then DOM-sanitize dangerous elements, event/style/srcdoc/srcset attributes, and unsafe URL schemes before assigning `innerHTML`.
- Centralize shared link, badge, loader, chat bubble, and semantic component styling instead of one-off utility strings.
- Chat bubbles and input containers should not use visible borders; use depth/drop shadow.
