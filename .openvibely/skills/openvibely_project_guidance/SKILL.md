---
kind: openvibely.agent_skill
version: 19
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
- In an audit-only review, if managed artifacts are explicitly excluded, make zero memory or skill tool calls for the entire audit turn. If the audit is strict, final, or otherwise requires a separate read-only verdict, prefer inspecting repository/worktree/PR/task evidence before optional memory or skill reads even when those reads are allowed; selected memory names or skill handles are not themselves a requirement to load managed artifacts first. Do not include `memory_view`, `skill_view`, or other managed-artifact lookups in the same initial parallel tool batch as repository or PR inspection as the default sequencing rule. If the prompt explicitly says memory or skill reads are allowed and do not by themselves invalidate the audit, an early managed-artifact read is not by itself a blocker; pivot to repository/workspace/PR/task-thread/requirement evidence and base the verdict only on that evidence, not memory/skill drift. Treat ordering as compromised only when the user prompt or higher-priority instructions explicitly prohibit managed-artifact reads or explicitly require repository/workspace/PR evidence before any managed-artifact read and that order was violated.
- If the assigned isolated worktree directory itself has disappeared mid-task (shell commands fail at `chdir`/cwd-not-found even though an earlier read succeeded, or the path is missing from `.worktrees`), do not silently work in the main checkout and do not just recreate an empty bootstrap folder. Inspect the main repository's git metadata (e.g. `git -C <main_repo> worktree list`, `git -C <main_repo> branch --list 'task/<task_id>*'`) to determine whether the task branch still exists, then recreate the isolated worktree at the assigned path on that task branch (`git -C <main_repo> worktree add <assigned_path> <task_branch>`, or from `main` if the branch is also gone) before making any code changes. Re-verify the recreated worktree is a real git checkout (not just a directory) before editing.
- When a user asks for an end-user channel action such as sending an email, Slack, Telegram, or Discord message, only use tools actually exposed in the current turn. `send_message` is an OpenVibely runtime tool available to agents running inside the task runner; an external coding-agent session cannot call it unless it is listed in the active tool surface. Do not imply a runtime send occurred if no callable send tool exists; either use a verified local/server delivery path, ask for permission to check one, or provide the composed content for the user to send.
- When a task is explicitly governed by a source-of-truth spec or runbook path, read that file before edits. If the exact file is missing, first verify the worktree/path and search for the exact filename or a clearly matching spec in the repo; if still absent, stop and report a blocker instead of implementing from assumptions or similarly named docs.
- Avoid validation loops, but do not leave compile or test failures unresolved after code edits. Make all intended code changes first, run the required validation chain once, fix any failures, and rerun only when needed to confirm the fix.
- If code changed, use `-count=1` for tests. Skip it only when re-running without code changes and cached results are acceptable.
- Avoid read-only tool loops. Repeated `read_file`, `list_files`, and `grep_search` calls without progress usually mean missing objective or weak handoff context.
- For `edit_file`, expand stable surrounding context when a replacement fails instead of retrying the same snippet. Use bulk replacement only when intentionally replacing every occurrence.
- When a user asks to intentionally fail a task to trigger OpenVibely failure alerts, output the explicit failure marker required by the current runtime/instructions; merely describing an exit code or claiming failure is insufficient. If the marker is not already in the prompt, inspect the relevant marker-extraction instructions/code and emit the exact marker, not a paraphrase.
- When a user asks to explain something better, says "no word salad", or challenges a prior diagnosis, explain the exact current bug, fix, or evidence in plain causal terms. If the concern is that a small request produced many changed files, separate the real feature path from required generated files/tests and from any unrelated cleanup or audit fixes. Re-read the current diff/context if needed; do not paste unrelated instruction templates, cite stale commits as proof of a live fix, or claim a bug is fixed without current validation evidence.
- For a "plain, no word salad" explain-the-changes request on a task branch: first run `git status --short && git diff --stat` to check for uncommitted work. If the worktree is clean (changes already committed), run `git log --oneline --decorate -n 20 && git diff --stat main...HEAD` (or the correct base branch) to enumerate the actual committed commits/files before writing the summary. Group the explanation by problem fixed (one short heading and 2-4 plain bullets per fix), not by file or by chronological commit order, and end with the exact validation command(s) that were run, not a restated command from memory/instructions.
- When a user pushes back mid-task with a short directive observation that clearly implies a design change (e.g. pointing out an existing global setting makes a proposed per-entity setting inconsistent), treat that as direction to implement, not an open question. Do not restate the tradeoff and ask "should I implement that change?" — make the change, then report what changed. Reserve an actual clarifying question for cases where the user's intent is genuinely ambiguous or the change is destructive/irreversible, not for ordinary design agreement.
- When a user asks to investigate or check a specific task ID that belongs to a different project than the current task's context, `view_task_thread`, `list_tasks`, `list_alerts`, and other task/alert tools are strictly project-scoped and return an error such as `task <id> belongs to a different project` rather than following the user across projects. There is no runtime project-switch tool. Do not loop retrying the same tool or guessing project IDs. In one turn: attempt the direct lookup, and if it is rejected as cross-project, stop and clearly report the exact rejection, name the target project from the user's message, and ask the user to either run/paste the equivalent lookup from a task/chat in that project or restate the request from within that project's context.

## Critical Rules

- Never delete, truncate, or overwrite `openvibely.db`; it contains user data.
- Never run `DROP TABLE` on production tables except in goose migrations.
- Never run `DELETE FROM` without a `WHERE` clause on production tables.
- Never change `busy_timeout`, `MaxOpenConns`, or `_loc=UTC` in `internal/database/database.go`.
- Never write tests that hit real LLM APIs. Use `models.ProviderTest` with `SetLLMCaller(testutil.NewMockLLMCaller())`; retired OpenAI/Anthropic model CLI auth should be covered as unsupported legacy state without spawning subprocesses.
- Do not reintroduce OpenAI/Anthropic model CLI subprocess transports; `AuthMethodCLI` is legacy data compatibility only.
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
- **Task runtime cwd:** When a task has an assigned worktree (`tasks.worktree_path` is set), the task prompt/tool runtime must use that path as the default working directory. Do not use the project root as the default cwd. Absolute paths and `cd` outside the worktree are always permitted — the worktree default is a convenience, not a sandbox. Do not add warnings when a model operates outside the worktree; just make the default correct and stay out of the way. The fix for accidental main-branch edits caused by wrong default root is `cwd = tasks.worktree_path`, not path blocking or escape warnings.
- **Generated task prompt repository path:** When a task has an assigned worktree (`tasks.worktree_path` is set), any generated or automation-built model-facing prompt that includes a `## Repository` section or equivalent path orientation must reference `tasks.worktree_path` as the repository root, **not** the project main-checkout path (`projects.repo_path`). The sandbox-escape incident (task `cf8347672f964254592f8b5b1f1db0bd`) was caused by the model being explicitly told the main-checkout path in the generated prompt — the system-prompt orientation fix alone is insufficient if the generated task prompt still names the main checkout. Audit automation-generated and linked-implementation-task prompt builders for this pattern.
- **Inbox/assigning-agent prompt — no local repo path:** The inbox agent prompt (`NativeSDLCNotificationInboxPrompt`) and any other assigning-agent prompt that calls `create_alert_implementation_task` (or an equivalent task-creation tool) must **not** include a `## Repository` section, a `Repository:` field, or any reference to `project.RepoPath` in the generated implementation task prompt. The implementation task runner supplies the correct worktree path at execution time; baking the main-checkout path into the assigning prompt is the upstream cause of sandbox-escape incidents. If you see a `## Repository` block being constructed from `project.RepoPath` inside an inbox/assigning prompt builder, remove it.
- **Task-produced commit recorder wrapper:** When a code path commits task-turn output changes to a worktree, it must call `LLMService.CommitTaskWorktreeChanges` (defined in `internal/service/task_commit_stat_service.go`), **not** the bare `CommitWorktreeChanges` helper. `CommitTaskWorktreeChanges` is a thin wrapper that calls `CommitWorktreeChanges` and then records a `task_commit_stats` row for the new commit. The only call sites that should use bare `CommitWorktreeChanges` are: merge/squash pre-commit cleanup in `WorktreeService.MergeBranch`, rebase/conflict resolution paths, and any commit that is explicitly not a task-turn output (e.g. integration merge commits). When auditing or adding a task-execution commit call site, grep for `CommitWorktreeChanges(` and verify every task-turn output call site is through the `LLMService` method. Known gap as of migration 169: `WorktreeService.HandlePostExecution` (`internal/service/worktree_service.go` ~line 2105) still calls bare `CommitWorktreeChanges`; commits that reach only that path are not recorded in `task_commit_stats`.
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
- Valid CHECK values include agent provider `anthropic`, `openai`, `ollama`, `test`; auth method `cli` (legacy only), `oauth`, `api_key`; task status `pending`, `queued`, `running`, `completed`, `failed`, `cancelled`, `blocked`; task merge status `''`, `pending`, `merged`, `failed`, `conflict`; schedule repeat type `once`, `seconds`, `minutes`, `hours`, `daily`, `weekly`, `monthly`.
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
