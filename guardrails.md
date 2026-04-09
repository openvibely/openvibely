# OpenVibely - Guardrails

Problems encountered during development and how to prevent them. For project context and architecture, see `MEMORY.md`.

## TOKEN-WASTING RULES — READ FIRST, VIOLATING THESE WASTES MONEY

### 1. NEVER Create Documentation Files
Creating markdown files to summarize/document/explain your work is BANNED. This includes but is not limited to:
- `*_FIX.md`, `*_SUMMARY.md`, `*_VERIFICATION.md`, `TECHNICAL_*.md`
- `README_*.md`, `ACTION_PLAN_*.md`, `FINDINGS_*.md`, `INVESTIGATION_*.md`
- `COMMIT_MESSAGE.txt`, QA checklists, overview files, any `Write` call to a `.md` file that isn't in `runbooks/` or the project instruction files
- If you are about to think "let me create a summary/doc/overview" — STOP. Do not do it. The code and commit messages ARE the documentation.

### 2. NEVER Run Build/Test More Than Once
- Make ALL code changes FIRST, then run `templ generate && go build ./cmd/server && go test ./internal/... -timeout 60s` ONCE
- If it fails, fix and run ONCE more. Maximum 2 runs total per task
- Do NOT: run tests after each file, run subsets then full suite, run "verification" builds after passing, run `go build` separately from tests
- `-count=1`: USE when you changed code (bypasses stale cache). SKIP when re-running without code changes — cached results are fine

### 3. Avoid Agentic Read-Only Tool Loops
- Repeated `read_file`/`list_files`/`grep_search` turns can silently burn tokens without progress
- Treat this as a prompt/context quality issue first (missing objective, weak handoff after compaction), not a reason to add synthetic guard messages that diverge from Codex behavior

### 4. Avoid Brittle `edit_file` Replacements
- `edit_file` now has Codex-style fallback matching (exact → trim-end → trim → unicode-normalized line matching), so whitespace-only drift should not force repeated retries
- If `old_string` still fails, expand context (include nearby stable lines) rather than retrying the same snippet
- If fallback finds multiple candidate blocks, use `replace_all=true` only when intentionally bulk-editing; otherwise make `old_string` more specific

### 5. Parallel Tool Execution Safety
- Agentic turn-level parallel tool execution is only for read-only tools (`read_file`, `list_files`, `grep_search`)
- If any mutating tool appears in the same turn (`write_file`, `edit_file`, `bash`, or unknown custom tools), execute that turn serially to avoid filesystem races and nondeterministic write ordering

---

## Critical Rules — NEVER Violate

- **NEVER delete, truncate, or overwrite `openvibely.db`** — it contains all user data
- **NEVER run DROP TABLE** on production tables (only in goose migrations)
- **NEVER run DELETE FROM without a WHERE clause** on production tables
- **NEVER change `busy_timeout` or `MaxOpenConns`** in `database.go`
- **NEVER remove `_loc=UTC`** from the connection string
- **NEVER write tests that hit real LLM APIs or spawn CLI subprocesses** — use `models.ProviderTest` + `SetLLMCaller(testutil.NewMockLLMCaller())`
- Always strip `CLAUDECODE` env var when spawning Claude CLI subprocess
- Never persist or log GitHub App installation access tokens; mint them per operation (clone/push/PR API) and keep token use in-process only
- Never log stored GitHub PAT/private-key material. If the Channels edit dialog pre-fills secrets for reveal UX, keep them masked by default and only reveal on explicit user toggle.
- Any server-side git commands that may contact remotes (for example worktree startup `fetch origin`) must run non-interactively and use the same GitHub operation-token env injection as clone/push paths for GitHub-backed repos.

## OpenAI OAuth Endpoint Shape

- ChatGPT OAuth `/responses/compact` does **not** accept `store`; only regular `/responses` requests should send `store=false`
- If logs show `unknown_parameter: 'store'` on compaction, remove `store` from the compaction payload builder (`pkg/openai_client/agentic.go`)
- Keep compaction instructions separate from normal turn system instructions. Do not reuse full task/system prompt for `/responses/compact`; use dedicated compaction prompt text (default compaction prompt or explicit `CompactionPrompt` override) to avoid re-triggering bootstrap file-read directives after compaction.
- After `/responses/compact`, preserve the compacted output returned by the API; avoid extra client-side re-summarization/re-trimming that can drop task state and cause bootstrap restarts.

## SQLite

- When adding columns, update ALL SELECT queries that scan the struct — not just `GetByID`/`ListByProject`. Methods like `ListActivePending`, `ListByCategory`, etc. each have their own SELECT. Also check `backlog_repo.go`, `insights_repo.go`, and the `ListWithSchedulesByProject` query which uses `t.` prefix aliases. The task SELECT pattern is: `id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at` (25 columns)
- Keep the model-default invariant in repository transactions, not just handlers/UI: if any `agent_configs` rows remain, at least one must have `is_default=1`; only zero-model state may have no default.
- CHECK constraints: `agent_configs.provider` (`anthropic`, `openai`, `ollama`, `test`), `auth_method` (`cli`, `oauth`, `api_key`), `schedules.repeat_type` (`once`, `seconds`, `minutes`, `hours`, `daily`, `weekly`, `monthly`), `tasks.status` (`pending`, `queued`, `running`, `completed`, `failed`, `cancelled`), `tasks.merge_status` (`''`, `pending`, `merged`, `failed`, `conflict`)
- Agent definitions (`agents` table / `models.Agent`) no longer include a `color` field; do not add `color` back to repo SELECT/INSERT/UPDATE mappings or `/agents` form payloads.
- Table recreation in migrations: use `-- +goose NO TRANSACTION`, `PRAGMA foreign_keys=OFF/ON`, recreate ALL indexes, preserve ALL CHECK constraints
- `projects` includes `repo_url` (migration 065). When scanning `models.Project`, include both `repo_path` and `repo_url` in every project SELECT/INSERT/UPDATE mapping

## Timezone

- Use `time.ParseInLocation("2006-01-02T15:04", str, time.Local)` for form inputs (never `time.Parse()`)
- Use `.Local()` before `.Format()` in templates
- `ComputeNextRun` for daily/weekly/monthly: convert to local → `AddDate` → back to UTC (DST-safe)
- Never use `time.Add(N*time.Hour)` for day arithmetic — use `time.AddDate(0, 0, N)`
- `ComputeNextRun` for `RepeatOnce` always returns `nil`

## Schedule NextRun — Handler vs Scheduler

- **Handler always sets `NextRun = RunAt`** (both Create and Update). Never pre-advance NextRun in the handler — causes same-day schedules to be skipped
- **Scheduler handles immediate execution**: picks up `NextRun <= now` within 5s, advances via `ComputeNextRun`
- Worker re-queue must allow `CategoryScheduled` (not just `CategoryActive`) — otherwise scheduled tasks silently dropped when project slots full
- `/workers` unified capacity table: keep the global worker row inside `project-stats-tbody` (pinned first) and clearly scope-marked (`Global` badge/row styling). Do not split global controls back into a separate top card.
- **Project limit changes must trigger dispatch**: `UpdateProjectWorkerLimit` handler must call `workerSvc.DispatchNext()` after updating the DB — otherwise queued tasks won't start until an unrelated event (task completion, scheduler tick). Global limit changes already do this via `Resize()` → `dispatchNext()`.
- **Drag-and-drop reschedule**: don't reset task status; auto-adjust past times to next future occurrence via `schedule.ComputeNextRun(now)`
- **Scheduler must skip completed/failed `RepeatOnce` schedules** — prevents unintended re-execution on drag/drop reorg

## Chat vs Task Worker Separation (Critical)

- **Interactive chat bypasses all worker limits** — no capacity checks on `/chat`, `/api/chat/message`, Telegram
- **Thread follow-ups respect worker limits** — `processStreamingResponse` gates slot acquisition on `params.IsTaskFollowup`
- **Never add capacity checks to interactive chat** — caused a bug where chat was unresponsive at capacity
- Thread follow-ups are never rejected — queued in background goroutine with blocking slot acquisition
- **All chat sources (web, API, Telegram) must use `processStreamingResponse`** — never create a separate code path for API/Telegram chat processing. The old `processAPIChatResponse` duplicated logic with fewer marker handlers, different completion ordering, and no `ChatResponseDone` on failure, causing intermittent UI update failures

## Chat Plan Mode (Read-Only)

- `/chat` plan mode must stay transport-enforced read-only, not prompt-only
- Plan mode tool policy: allow only `read_file`, `list_files`, `grep_search`; block `write_file`, `edit_file`, `bash`
- Plan mode must disable marker processing (`ProcessMarkers=false`) so marker text cannot mutate tasks/settings
- Claude CLI plan mode must run with permission mode `plan` (not dangerous skip-permissions), and should not load plugin dirs in plan mode
- Plan-to-orchestrate UX prompt should trigger only for completed plan responses (detect `<proposed_plan>`) and only when current chat mode is `plan` to avoid false switch prompts during normal orchestration chat
- Plan-to-orchestrate switch action should be one-click: switch mode and auto-submit a handoff message to start implementation without requiring a second manual send
- Plan-to-orchestrate handoff message should request a single active task for the first plan step and explicitly avoid bulk starting other existing tasks
- Plan-complete CTA visibility must be resilient to refresh/poll: recover by scanning latest assistant history in chat mode `plan`, not only from live stream completion callbacks
- Plan-completion prompt must use the centralized `evaluatePlanCompletionPrompt` evaluator from all completion paths (per-exec done, chat_response_done fallback, stream error/onerror, page hydration). Never show mid-stream — check `_chatStreamInProgress` flag. Latest-message semantics: history scan must return the newest completed assistant bubble text and NOT continue to older bubbles. Stream error/onerror handlers must clear `_chatStreamInProgress` and re-evaluate (for non-thread chat context only) so the flag doesn't stay stuck true after failures. `chat_response_done` must reconcile the live assistant bubble from `completed_output` before evaluating prompt visibility; otherwise final streamed tails can be persisted but missing in live UI until refresh.
- `currentChatModeValue` must prefer visible selector state (select element) over hidden input to avoid stale mode mismatches after programmatic mode changes
- `/chat` mode-selector hydration must trigger `evaluatePlanCompletionPrompt()` after persisted mode restore (localStorage) and on selector change. Mark the selector as hydration-gated (`data-hydrated`) and have `currentChatModeValue()` fallback to persisted mode before hydration completes; otherwise focus-return HTMX refreshes can briefly evaluate in default `orchestrate` mode, hide `Switch to Orchestrate`, and never re-show until manual refresh.
- `evaluatePlanCompletionPrompt()` must treat DOM-scan empty text on reconnect/hydration as transient and preserve previously-earned CTA visibility (latch) unless a real completion event or explicit state change says otherwise; otherwise tab blur/focus can clear `Switch to Orchestrate` even though no new response/mode change occurred.
- In `/chat` plan mode, keep read-only exploration tool cards (`read_file`, `list_files`, `grep_search`) visible in rendered assistant bubbles (live/streaming and refreshed history) so planning traces remain transparent
- For OpenAI/Anthropic API orchestrate chat, inject request-scoped action tools and disable marker post-processing for that execution; do not execute both paths in the same response
- Runtime action tools must stay mode-gated: orchestrate allows action tools, plan allows read-only repo tools only, task follow-ups should not receive chat action tools
- `execute_tasks` must exclude completed tasks by default to prevent accidental mass re-runs; only include completed tasks when explicit opt-in (`include_completed=true`) is present
- For specific-task execution requests, `execute_tasks` must target by `task_id` or `title` instead of broad tag/priority search filters

## RunTask Idempotency (Queued vs Running Race)

- Never unconditionally set `status='pending'` inside `TaskService.RunTask`; that can overwrite a concurrent transition to `running` and make active work appear queued in UI
- Use the guarded repo update (`SetPendingIfNotRunningOrQueued`) and submit only when that update actually changed a row
- If the guarded update returns false, treat `/tasks/{id}/run` as a no-op (already `running` or `queued`)

## Task Thread Follow-Up

- **Thread completion must call `DispatchNext()`** — deferred FIRST (runs LAST in LIFO) after slot release, otherwise queued tasks stay blocked
- **Stale queued task recovery**: scheduler resets `StatusQueued` >10 min to `StatusPending` and resubmits
- Never re-inject `task.Prompt` on follow-ups — use `buildThreadSystemContext()`
- Auto-reactivation: always set status=queued + category=active on follow-up (any previous state)
- Task thread MUST use `ListByTaskChronological` (ASC), not `ListByTask` (DESC)
- Task-thread follow-up calls must propagate the task's `agent_definition_id` into streaming requests (or resolve it by `taskID` in shared processing), or agent plugin/MCP tools will be missing on API paths
- **Plugin scoping is by agent definition**: `resolveAgentRuntime(agentDef)` resolves only `agentDef.Plugins`. `nil` agent def → zero plugins (no skills, no MCP, no dirs). Empty `Plugins` list → no resolver call. Never inject globally installed plugins when no agent definition is assigned to a task
- `is_followup` column (migration 034) and `diff_output` column (migration 035): ALL SELECT queries must include them
- Thread follow-ups must not treat “stream returned nil error” as success by itself — parse `[STATUS: FAILED | ...]` / `[STATUS: NEEDS_FOLLOWUP | ...]` from text-only output before marking completion
- Thread follow-ups must start realtime diff snapshot broadcasting when `workDir` is available (persist `executions.diff_output` during the run + publish `diff_snapshot` events). Do not rely only on final completion diff capture, or the Changes tab will stall for completed-task reactivations
- Never clear an execution's existing streamed `output` during failure completion when the new `output` argument is empty. Preserve existing output so failed thread turns keep context/history visible instead of appearing reset
- Streaming retries that reuse the same `exec_id` must seed any new streaming writer buffer from the execution's existing `output` before writing/flushing. Otherwise a retry attempt can flush an empty/fresh buffer and overwrite already-streamed history (observed with transient Anthropic 429/rate-limit retries)
- For thread/chat assistant bubbles rendered from `data-raw-content`, DOM-cleaning skips must verify both content signature and rendered DOM presence; on morph swaps after failures (including provider 429/rate-limit), `data-cleaned-*` flags can survive while inner DOM is blank, which makes prior history look wiped unless re-render is forced when rendered content is missing
- Do not fail a task or follow-up solely because no git diff was produced. Some valid tasks are read-only (analysis, summaries, screenshot inspection, reporting) and complete successfully without repository writes
- In task-thread HTMX lifecycle handlers, never classify all `/thread` requests as sends. Polling is `GET /tasks/:id/thread`; draft-clear logic must only run for real send submissions (`POST` + `/thread` and/or `#task-thread-form` trigger), otherwise drafts can be wiped during blur/focus reconnect cycles.

## Testing

- **CLI safeguards**: `isTestMode()` checks `GO_TESTING` env var AND `os.Args[0]` suffix `.test`. **CRITICAL**: test helper files MUST use `_test.go` suffix — a non-test file importing `testutil` will compile `testutil.init()` into the production binary, setting `GO_TESTING=1` at server startup and blocking all CLI calls
- **Test provider pattern**: Create agents with real providers for DB CHECK constraints, then set `agent.Provider = models.ProviderTest` on in-memory copy. `ensureDefaultAgent` and `createAgent` helpers do this automatically
- `ProviderTest` excluded from vision override so tests with image attachments don't get switched to real agents
- `testutil.NewTestDB(t)` — in-memory SQLite, cached schema, one DB per test. Never `t.Parallel()` with shared DB
- FK constraints: create referenced records first, never fake IDs
- Baseline migration seeds a default project (expect N+1 in count assertions) and built-in templates/patterns/workflows, but no default agent row. `testutil.NewTestDB` backfills a default test agent when absent for compatibility with existing tests.
- `NewWorkerService(svc, 0, nil)` in tests. Never `CategoryActive` with uninitialized WorkerService (deadlock)
- Override base URLs for mocking: `OpenAIAPIBaseURL`, `OpenAIChatGPTAPIBaseURL`, `AnthropicAPIHost`, `OllamaHTTPClient`
- Telegram service tests that call `linkAttachmentsToExecution` must isolate `telegramUploadsDir` with `t.TempDir()`; otherwise relative `uploads/...` writes can spill into `internal/service/uploads/...` when tests run from package cwd
- Handler tests that exercise chat/task attachment file writes must override `uploadsDir` with `t.TempDir()`; otherwise relative `uploads/...` paths spill into `internal/handler/uploads/...` when tests run from package cwd

## Chat Markers

- **Convert task links BEFORE cleaning markers** in SSE done handlers
- **Task link regex must use optional dash**: `(?:-\s*)?` — `marked.parse()` consumes `- ` into `<li>`, so `[TASK_ID:]` loses leading dash
- **No chat marker post-processing**: do not re-enable assistant-output marker parsing in chat entrypoints (`/chat`, API chat, Slack, Telegram). Chat actions must run through request-scoped runtime tools.
- **Action parity when adding tools**: when adding/removing runtime action tools, update `internal/chatcontrol/registry.go` (single source of truth). All surfaces (web/api/telegram/slack) derive tool definitions from the registry — never hand-craft tool lists.
- **Runtime-tools vs markers are mutually exclusive per request**: when `supportsChatActionTools` is true and tools are injected, `ProcessMarkers` must be `false`. Never enable both paths simultaneously.
- **Plan mode must never contain write actions**: the registry enforces this (test `TestRegistry_PlanModeOnlyAllowsReadActions`). If adding a new action, set AllowedModes correctly in the registry.
- **Runtime tool executor must not intercept provider-native tools**: `BuildRuntimeToolExecutor` must return `handled=false` for tools not in the chatcontrol registry (`unknown_action` code) so provider base executors can handle `grep_search`/`read_file`/`list_files` etc. Only `mode_blocked`/`surface_blocked` errors should return `handled=true`.
- `summaryRe` in `cleanChatOutput` uses greedy `.*` — ALL `\n---\n` prefixes must be in the alternation
- `cleanChatOutputForDisplay` preserves summary/result sections — use for Telegram display (never `cleanChatOutput` which strips them)

## Stream JSON Parsing (`parseJSONStream`)

- CLI wraps events in `{"type":"stream_event","event":{...}}` — parser unwraps before dispatching
- When adding content types, update ALL extraction paths: streaming deltas, block start/stop, `extractMessageText`, `collectMessageText`
- Tool detail in `[Using tool: X | detail]` must NOT contain `]` (breaks marker regex) — replace with `)`
- Thinking block auto-closure: `parseJSONStream` auto-closes unclosed thinking on new `content_block_start`, `message_stop`, and end-of-stream

## Templ / HTMX / JavaScript

- **Bare `{` code blocks are NOT valid templ syntax** — use helper functions
- For repo-path inputs, treat `~/...` and `~\...` values as home-relative absolute-intent paths; normalize to concrete absolute paths server-side before save
- Run `go run github.com/a-h/templ/cmd/templ@latest generate` if `templ` not on PATH
- Cache middleware: `Vary: HX-Request` on all responses + `Cache-Control: no-store` on HTMX partials — never remove
- `--animation-btn: 0` in `:root` CSS — never remove
- DaisyUI v4 colors use **oklch** format, not RGB
- Chat/thread loading indicators now use custom `ov-loading-dots` markup. If you change loader classes/markup, update HTML-string assertions in handler/template tests that verify running placeholders.
- For streaming-resume loaders, never use Tailwind `!hidden` as an "unhide" helper. `!hidden` forces `display:none !important` and can make gray in-progress dots disappear after HTMX refresh/reconnect.
- For canonical `Default` badges on major settings/config pages (for example `/models`, `/personality`), use shared `ov-badge-default` instead of page-local variants (`badge-neutral`, `badge-primary`) to prevent light/dark theme drift
- Forms with HTMX: always specify `method="post"`
- `hx-on::after-request`: always check `event.detail.successful` before clearing inputs
- Project `Repository Path` folder pickers must not use upload-oriented action labels (`Upload`). Keep folder-selection wording (`Choose Folder`/`Select Folder`) and use native picker intent messaging (selection, not upload).
- Channels integration UX should not render provider cards by default when not added; keep provider discovery in the Add Channel chooser and show kebab edit/remove on active cards.
- Channels integration toggles (for example `*_send_responses`) must render from persisted settings state; do not hardcode checked defaults in templates or saves can silently re-enable disabled notifications.
- In grouped dropdown menus that mix section headers (`menu-title`) and actions, keep every actionable row on a shared leading-slot pattern (for example a spinner/placeholder span) so labels remain consistently indented across sections/themes.
- Slack bot-token source is explicit (`oauth` vs `manual`). Do not overload one setting key for both modes; keep OAuth token and manual override token separate so switching modes does not accidentally wipe a working OAuth connection.
- Slack OAuth redirect URLs can be HTTPS-enforced by Slack app policy. Do not assume `http://localhost` callback will work in all workspaces; keep fallback guidance visible (HTTPS tunnel or manual bot-token mode).
- Slack bot auth success alone does not prove inbound connectivity. Ensure Slack app has Socket Mode enabled plus Event Subscriptions with `app_mention` and `message.im`, otherwise DMs/mentions will appear as no-ops.
- Slack authorized-user enforcement is project-scoped and **allow-by-default when no authorized users are configured**. Only enforce explicit `slack_user_id` checks when at least one entry exists for the active project; otherwise keep chat access open.
- For local desktop runs, repository-folder selection must use the local endpoint (`POST /projects/pick-folder`) backed by OS-native dialogs (macOS `osascript`, Linux `zenity`/`kdialog` when available, Windows PowerShell FolderBrowserDialog); do not rely on browser file APIs for absolute paths
- Repo path picker apply logic must gate on absolute paths only; ignore non-absolute picker-derived values instead of writing them into `repo_path`
- When local repo paths are disabled (`OPENVIBELY_ENABLE_LOCAL_REPO_PATH` unset/invalid/false), new project/create flows must remain GitHub URL only, while existing legacy local projects may still save non-repo settings without forced migration
- Never `dialog.close()` on submit button onclick with HTMX — close via `htmx:afterRequest`
- Never use templ expressions in `<script>` tags — use `data-*` attributes
- In `.templ` `<script>` blocks, avoid nested JS template literals that embed HTML tags (for example ``${cond ? `<p>...</p>` : ''}``) — templ can misparse and report missing `</script>`. Build those fragments with string concatenation instead.
- `htmx:load` instead of `htmx:afterSwap` for outerHTML swaps (element gets detached)
- Guard `setInterval` with window-level ID + `clearInterval`
- No `DOMContentLoaded` — use `setTimeout(fn, 0)` + `window._flag` guards
- Delete handlers: return re-rendered list, not NoContent
- `htmx.ajax()`: add `.catch()` (HTMX 2.0 rejects on non-2xx)
- When removing concrete HTTP routes, do not assume both methods will return 404 in tests. Dynamic path matches can return 405 (method not allowed); assert route inaccessibility (`!= 200`) unless status code behavior is explicitly guaranteed.
- Keep the default landing behavior explicit: `/` should redirect to `/chat` (not render dashboard directly). If default landing changes, update route tests and preserve old pages on explicit routes (e.g. `/dashboard`) instead of silently dropping them.
- **Sidebar nav + polling morph race**: Pages with `hx-trigger="every Ns"` + `hx-swap="morph:outerHTML"` (e.g. task thread view) block the main JS thread during morph, making concurrent sidebar clicks slow or unresponsive. Three-layer defense:
  1. **Capture-phase pointerdown early signal**: Set `_sidebarNavigating` on `pointerdown` with capture (`addEventListener(..., true)`) so it fires before bubble handlers and before click, even under heavy morph work. Include a 3s safety timeout to clear the flag. Clear timeout when navigation swap completes.
  2. **beforeRequest abort**: Sidebar `htmx:beforeRequest` aborts in-flight polling XHRs and disables `hx-trigger`. Thread `htmx:beforeRequest` also blocks polls when `_sidebarNavigating` is set.
  3. **beforeSwap/afterSwap guards + incremental cleaning**: `htmx:beforeSwap` suppresses stale morph swaps (`shouldSwap=false`). Thread `htmx:afterSwap` checks `_sidebarNavigating` and returns early to skip expensive post-morph work (`cleanAssistantMessages`, `renderStreamingContent`, `_initThreadStreaming`). `cleanAssistantMessages` must be incremental (`cleanedRaw` / `cleanedText` signatures) and must not force-clear clean-state every poll; reprocessing unchanged thread bubbles each 3s poll reintroduces click lag.
  Thread state (`_taskThreadStreamingActive`, `_taskThreadPageTracker`, `_threadEventSources`) must be cleaned up in `htmx:beforeSwap` for `main-content` swaps.
- Task-thread polling must be status-scoped to active execution states only (`running`/`queued`). Do not enable `morph:outerHTML` polling for idle `pending` tasks, or unsent textarea drafts can be lost during periodic swaps.
- For task-thread draft safety, persist textarea drafts by task ID across swaps (not just transient per-swap variables) and restore after `htmx:afterSwap` before user interaction resumes.
- In task-thread `htmx:beforeSwap`, successful thread-form swaps (non-empty response body) must clear `_taskThreadSavedInput` and the task draft key before any restore path runs; otherwise submit-button sends can repopulate stale text even when Enter-submit clears correctly.
- In task-thread `htmx:beforeRequest`/`afterRequest`, do not key send-path logic only on `event.detail.elt` identity (`#task-thread-form`). Also detect by request path (`.../thread`) so Enter-submit, send-button submit, and programmatic `requestSubmit()` all trigger identical tracker reset / draft-clear / auto-scroll behavior.
- Any thread EventSource created by streaming bubbles must be both tracked and untracked (`_threadEventSources` add/remove). Tracking without unregister-on-close leaves stale handles and can mask real open-connection counts.
- Keep one shared sidebar-managed SSE stream (`/events/live`) per tab for task/chat/file-change broadcast events. Chat and task-detail pages should consume dispatched browser events (`sse-chat-live-event`, `sse-file-change-event`, `sse-live-connected`) instead of opening additional long-lived `/events/chat/live` or `/events/filechanges` EventSources.
- Streaming chat/tool-card rendering must be frame-batched (`requestAnimationFrame`) in per-exec `onmessage` handlers and force-flushed on `done`; direct re-render on every chunk can stall plan-mode live updates until tab refocus.
- Theme-toggle responsiveness: do not apply light/dark switch transition choreography to broad selectors (for example `html.theme-transition *`) or maintain transition-class add/remove timers in `toggleTheme()`. On large pages this causes multi-second repaints. Keep theme switching immediate (`data-theme` update + icon/localStorage sync only).
- Tool-call card light-theme contrast: never rely on low-alpha `hsl(var(--bc)/...)` for `.stream-tool-summary`, `.tool-name-secondary`, `.stream-tool-body-content`, or status/spinner icons in light mode. Use semantic light tokens (`--ov-l-text-strong`, `--ov-l-text-muted`, `--ov-l-success`, `--ov-l-error`) so filenames, labels, and icons remain readable on light surfaces.
- Tool-call card light-theme hierarchy must match dark mode: keep `.stream-tool-body`/`.stream-tool-body-row` visually transparent (no emphasized outer border/divider treatment), and keep bordered/surfaced card styling on `.stream-tool-body-content` IN/OUT containers only.
- Task detail Changes tab must not eagerly render heavy diff content when another tab is active; keep Changes content lazy-loaded on tab open to avoid hidden-tab browser stalls on large task diffs.
- Task detail Thread tab must not eagerly render full transcript bubbles in the initial task-detail response; keep Thread content lazy-loaded via `/tasks/:taskId/thread` when the chat tab is active/opened.
- Diff viewer must not embed full raw diff payloads in `data-*` attributes (especially multi-MB diffs). Enforce GitHub-style diff envelopes: auto-load only up to 400 lines/20KB per file, allow on-demand load up to 20,000 lines/500KB per file, cap total loadable diff at 20,000 lines/1MB, and cap considered files at 300.
- Changes-tab diff refreshes (`/tasks/:id/changes`) must be skipped when the Changes tab is not active (including event-triggered refreshes like `refreshChanges`) to avoid hidden-tab large-diff fetches.
- Changes-tab worktree diff selection must not rely only on `tasks.merge_status`. If live diff is empty, verify actual git ancestry (`IsBranchMerged`) and fall back to preserved `executions.diff_output` so manual/local merges don’t appear as “diff disappeared” before cleanup updates metadata.
- Changes-tab live diff updates (`_updateDiffViewer`) must use `fetch()` + offscreen DOM fingerprint comparison, NOT `htmx.ajax()`. The `htmx.ajax()` path swaps innerHTML as a side effect before the `.then()` callback can compare fingerprints, causing full DOM remounts and viewport jumps every 2 seconds. The correct pattern: fetch HTML via `fetch()`, parse in a detached `div`, compute fingerprint, compare with `_lastDiffFingerprint`, and only swap `innerHTML` + restore scroll/view-mode via `requestAnimationFrame` when content actually changed.
- Task-detail file-changes SSE/listener wiring must be swap-safe: before re-binding global listeners on HTMX re-render, remove previous handlers, and always stop file-changes SSE on `main-content`/`task-detail-content` swaps.
- DnD adjacent-slot bug: use `_schedDragActive` flag (150ms timeout in dragend) to prevent click-after-drop navigation
- Schedule page layout: never use viewport-relative heights (`h-[calc(100vh-...)]`) on the timeline container — use flex layout (`flex-1 min-h-0`) inside a `flex flex-col h-full` parent to fill available space without causing outer page scrollbar
- On HTMX pages with polling + dirty-input preservation (for example `/workers`), dirty-value restore must be submission-aware: suppress restoration briefly around form submit/request-success so server-accepted values are not immediately re-marked dirty (`input-warning`) and rendered with a stuck warning/focus ring look
- Inline page scripts inside HTMX-swapped containers must use a window-level registration guard (`if (!window._...Bound)`) so listeners are not duplicated on each swap and stale handlers do not re-apply obsolete UI state
- **Toast stacking prevention**: Event listeners for custom events (like `showToast`) must have registration guards (`if (!window._flag)`) and deduplication logic (timestamp-based Map) to prevent multiple toasts from rapid clicks or HTMX page updates. Dedup key: `message|type|taskId`, time window: 1 second
- For HTMX-only guardrails/errors that should notify without swapping content, emit an app-scoped `HX-Trigger` event (for example `openvibelyToast`) and bridge it once in `layout/base.templ` to `window.showToast(...)` so pages outside task detail still show notifications.
- `openvibelyToast` payloads for model-setup blockers should include structured action fields (`link_url`, `link_text`) instead of embedding raw HTML in `message`.
- Modal forms that can fail server-side validations/integrations (for example project GitHub clone/reclone) must submit via HTMX no-swap (`hx-swap="none"`), and handlers should return `openvibelyToast` + `204` for HTMX failures to avoid raw JSON/error-page swaps.
- **Native dialog top-layer pitfall**: A fixed/high `z-index` toast container under `body` can still render behind `<dialog>.showModal()` content because dialogs use the browser top layer. Keep the shared toast container re-hosted in the top-most open `dialog.modal`, then move it back to `body` when dialogs close.
- If task cards need lookup-backed badges (for example agent-definition names), thread that lookup data through **every** `components.KanbanBoard(...)` render path in `task_handler.go` (full page + all HTMX refresh handlers), or badges disappear after actions like move/delete/sort.
- Keep Kanban dropzone sizing constraints class-consistent across columns. Active sub-sections (`In Progress`/`Queued`) must not add extra horizontal padding classes (for example `p-2`) that make only Active cards render narrower than Backlog/Completed.
- For AI-assisted form generation flows, do not silently degrade quality: include explicit response metadata (e.g., `generation_mode`, `generation_error`) so the UI can tell users when fallback templates were used.
- Agent generation JSON parsing must not assume model output starts as raw JSON. Always strip wrappers/prefix text, extract balanced JSON candidates, validate payload shape, and attempt one strict-JSON repair reprompt before hard fallback.
- Agent generation malformed-output recovery must include provider-level malformed JSON errors too (for example `invalid JSON from model ... invalid character ...`), not only `err == nil` responses with bad payload text. Route those errors through repair + strict retry before fallback.
- Agent system-prompt generation must never rely on runtime tool/plugin/MCP execution. Keep an explicit prompt guardrail forbidding tool execution, and strip leading `[Using tool: ...]` wrapper lines before JSON parsing so tool transcript noise cannot break generation.
- GenerateAgent must not resolve plugin runtime bundles or include plugin/MCP/tool-catalog hints in the model prompt. Treat generation as pure text-in/JSON-out with `disable_tools=true` from the first attempt; plugin tools are task-execution runtime only.
- For OpenAI `OperationDirect` no-tools flows, propagate `DisableTools` through provider adapter into the direct client payload and set `tool_choice: "none"`; logging `disable_tools=true` at service level is insufficient if provider/client wiring drops the flag.
- In OpenAI direct JSON-generation no-tools mode, do **not** run stream pseudo-tool marker rewriting on output deltas. The sanitizer can misinterpret plain JSON (`{...}`) as pseudo-tool syntax and rewrite valid JSON into `[Using tool: ...]` marker text, breaking downstream parsing.
- If Agent generation fallback still yields a usable draft, do not surface it as a hard failure toast. Use warning/info-level feedback with actionable reason text.
- In Agent Details, keep a single description input flow: do not reintroduce a separate top "Describe what this agent should do" prompt block. The generate action should stay adjacent to the persisted `description` field above `System Prompt` so creation/edit UX remains unambiguous.
- Agent modal model options must come from configured `agent_configs` records (rendered by config name with model-id values). Do not hardcode provider-specific labels like `Sonnet/Haiku/Opus`; that drops OpenAI and other configured provider models from create/edit flows.
- Agent model normalization/validation must allow configured model IDs from `agent_configs.model` (plus `inherit`) for create/update/generation normalization. Restricting to legacy aliases (`sonnet`/`haiku`/`opus`) silently rewrites valid OpenAI overrides to `inherit`.
- GenerateAgent default-model calls are longer-running than chatty CRUD handlers; avoid short per-request deadlines (35s caused `context deadline exceeded`). Keep a dedicated generation timeout and classify timeout/cancellation failures clearly in both logs and `generation_error`.
- Plugin marketplace/plugin visibility must come from app-local plugin-root discovery (`$OPENVIBELY_PLUGIN_ROOT/marketplaces/*/.claude-plugin/marketplace.json`, default `./.openvibely/plugins/...`), not external CLI discovery responses.
- App-local plugin-root behavior must keep plugin discovery non-empty on fresh roots: if discovery returns empty, auto-seed default marketplaces into the app root so the Agents plugin tab remains populated.
- Do not derive app-local plugin root from cwd when cwd can be `/` (service/daemon launches). Guard default root resolution with an executable/app-dir fallback, otherwise plugin seeding targets `/.openvibely/plugins` and appears empty in UI.
- Marketplace add/display flows must preserve user-entered URL fidelity: when adding a marketplace, try the original source string before normalized shorthand, and when rendering marketplace cards prefer full URL/source fields before `repo` shorthand.
- Plugin MCP servers selected through installed plugins should be started via the persistent shared MCP runtime and reconciled against installed plugins after install/uninstall actions. Plugin enablement is per-agent (`agents.plugins`), so do not implement global disable state that affects all agents. Uninstalled plugins must not remain active in persistent runtime state, and startup/reconcile failures (for example `npx` missing) must remain visible in `/agents/plugins/state` + UI warnings.
- Marketplace/plugin action UX in the agents modal must expose in-progress state and duplicate-click guards: Add/Restore Defaults/Uninstall controls should disable during requests and show spinner + temporary labels (`Adding...`, `Restoring...`, `Removing...`), while per-marketplace Sync/Remove controls stay compact icon-only buttons (not text buttons) with spinner-only in-progress state and `aria-busy`. Do not reintroduce a standalone marketplace `Refresh` button. Use a shared guard for conflicting marketplace actions so rapid repeated clicks cannot submit overlapping requests. Keep errors visible inline (status text container), not toast-only.
- Agent-modal plugin state refreshes must ignore stale async responses. If multiple `/agents/plugins/state` loads overlap (for example install then immediate uninstall), only the newest response may mutate UI state; older responses should be dropped so uninstall does not appear to require a second click.
- Uninstall-triggered plugin-state refreshes must suppress global discovery-warning toasts (`loadPluginState(false, ...)` in uninstall flow). Do not show unrelated persistent MCP reconcile/runtime errors as uninstall-failure toasts when uninstall itself succeeded.
- Install-triggered plugin-state refreshes must suppress global discovery-warning toasts (for example `loadPluginState(..., { suppressDiscoveryWarningToast: true })` in install flow). Do not surface unrelated persistent MCP reconcile/runtime errors as install-failure toasts when install itself succeeded.
- During uninstall refresh, keep a pending-uninstalled ID set and filter those IDs out of refreshed installed state so stale discovery/reconcile responses cannot reinsert just-removed rows.
- Plugin uninstall must be deterministic app-local cache removal. Do not reintroduce external CLI uninstall dependencies that can diverge from app-local installed state.
- Installed plugin rows in the agents modal should remain compact: do not re-introduce installed-row description text or full-width button-style uninstall controls. Keep per-agent enablement as toggle switch + icon trash action, and disable both while plugin loading/install/uninstall is active.
- Available plugin rows should not render a redundant `available` badge next to the plugin name. Keep `Install` as a real button-style CTA (not ghost/plain text) with the existing disabled + `Installing...` in-progress behavior preserved.
- Agent-modal plugin install flow is contextual: when editing an existing agent, include `agent_id` in `/agents/plugins/install` so successful install auto-enables that plugin for the same agent. Keep install global and enablement per-agent.
- Install failure must remain a hard failure (`400`/error response, no enable attempt). If install succeeds but per-agent enable fails, return `200` with `enabled_for_agent=false` + `enable_error` so UI can show clear retry guidance without pretending enablement succeeded.
- Do not reintroduce a standalone `Plugin MCP Runtime` panel in the agent modal. Runtime health must be shown inline on installed plugin rows using the existing status dot (`running`→green, `failed`→red) and failed runtimes must expose the runtime error on hover via tooltip/popover.
- Plugin MCP server names (from `.mcp.json`) can differ from plugin ID prefixes (e.g. MCP server `adspirer` vs plugin `adspirer-ads-agent@marketplace`). Runtime status lookup must match by `plugin_id` field (set by backend `enrichRuntimePluginIDs`), not by splitting the plugin ID at `@` and hoping the prefix matches the MCP server name.


## Thread Transcript Pagination

- `view_task_thread` tool definition must include `offset`/`limit` params so the model can paginate large threads
- Both handler and Telegram `formatThreadTranscript` must accept `offset, limit int` — all call sites must pass these args
- Per-message truncation limit is 50KB (`maxPerMessageBytes`); total transcript budget is 80KB (`maxThreadTranscriptBytes`). When budget is exceeded, the transcript must include a continuation hint with the next `offset` value
- Never truncate individual messages to tiny sizes (e.g. 2000 bytes) — that hides execution details and prevents accurate summarization

## Stream Tool Markers

- Task-thread `[Using tool: ... | detail]` markers must preserve enough Bash/Grep context to show the relevant path/flags/pattern. Do not reintroduce tiny 40-60 char truncation in provider adapter `toolSecondaryInfo()` helpers

## Chat SSE Reconnect Must Preserve Project Context

- All `htmx.ajax('GET', '/chat...')` calls in reconnect/refresh handlers MUST include `?project_id=` with the current project ID read from the chat root (`#chat-page-root[data-project-id]`)
- Never use bare `/chat` in HTMX AJAX calls — `getCurrentProjectID()` falls back to the first (default) project when no `project_id` param is provided
- Never target chat refresh/swap logic at generic `[data-project-id]` selectors. Non-chat pages (`/tasks`, `/workers`, analytics) also use `data-project-id`; generic selectors can swap chat HTML into unrelated pages after tab refocus
- The tab visibility manager closes and recreates SSE on hide/show; the `onopen` handler that refreshes chat content must pass the current project and target `#chat-page-root` only. On reconnect, skip forced `/chat` outerHTML refresh while a chat stream bubble is actively in progress; refreshing mid-stream can replace the live bubble with partial persisted output and hide plan-complete CTA state until later fallback events.
- When leaving chat via `#main-content` swap, explicitly unregister `chat-live` SSE to prevent stale reconnect side effects
- Per-execution chat stream EventSources (`/events/chat/:exec_id`) must be globally tracked (by exec ID) and force-closed on `#chat-page-root` / `#main-content` swaps. Without this, reconnect outerHTML swaps can leak old streams and exhaust the browser’s per-host connection slots.

## Chat Bubble Styling

- **NEVER use visible borders** on chat bubbles or input containers — use `border: none`
- Depth via drop shadow only. Arrow pointers: set border vars to `transparent`
- Task-create result links/buttons rendered from `convertTaskLinksInMessage` must use shared semantic classes (`ov-task-result-link`, `ov-task-result-start-btn`) rather than ad-hoc utility strings. Keep link underline subtle (`1px` thickness with tuned offset) and preserve explicit hover/focus/active/disabled styles across light/dark themes.
- All links rendered in chat/thread markdown and tool-call card content must route through the shared link styling token (`--ov-link-color`, currently `#7480ff`) in `layout/base.templ`; do not introduce one-off `text-success`/`text-primary` link classes in task marker conversion helpers (`[TASK_ID]`/`[TASK_EDITED]`).

## Git Lineage for Chained Tasks

- **Worker dependency gating**: `dispatchNext()` must skip chained tasks (with `parent_task_id`) whose parent is non-terminal. Do not dispatch child before parent completes or the child will run without parent edits
- **Lineage resolution order**: `BaseCommitSHA` (preferred, SHA-based so parent branch cleanup doesn't break) > `BaseBranch` (metadata/fallback) > `MergeTargetBranch` > global/default branch. Never default chained children to main if valid parent lineage exists
- **Branch cleanup must check descendants**: before deleting a parent branch, call `HasNonTerminalDescendants()` — skip deletion if active children/grandchildren exist
- **Lineage capture is atomic with child creation**: set `base_branch`, `base_commit_sha`, `lineage_depth` in the same `TriggerTaskChain` call path, before returning from create. Never create a chained child without capturing parent lineage first

## Git Worktree Merge Status

- **Reset merge status when follow-ups create new changes** — After a task is merged (merge_status="merged"), if a follow-up creates new changes, reset merge_status to "pending" so the merge button re-appears
- **Task Changes merge UI is flag-gated**: `OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS` controls merge dropdowns in the Changes tab (default off). Keep handler enforcement aligned: Changes-tab merge requests carry `merge_source=changes_tab`, and `MergeTaskBranch` must reject those with `403` when flag is off so hidden UI cannot be bypassed.
- **Check implemented in `completeWithSuccess`** — After capturing diff output, if diffOutput is non-empty AND task has a worktree AND merge_status is "merged", reset to "pending"
- **Only reset when changes exist** — Read-only follow-ups (no diff) should NOT reset merge_status
- **Template condition** — `task.MergeStatus != models.MergeStatusMerged` in `worktree.templ` controls merge button visibility
- **Fixed bug** — Previously, merge button stayed disabled after first merge even when follow-ups created new changes to merge
- **Startup auto-merge safety** — At task start, only merge latest `main`/default branch into worktree branch when `git status --porcelain` is clean. Dirty worktrees must skip startup auto-merge to avoid overwriting in-progress task edits
- **Startup conflict recovery** — If startup auto-merge conflicts, detect conflict files, run `git merge --abort`, set task `merge_status=conflict`, and return actionable error text (do not leave repo in in-progress merge state)
- **Manual merge conflict UX** — In `MergeTaskBranch`, conflict outcomes (`result.Success=false` with `ConflictFiles`) must emit an HTMX `openvibelyToast` failure trigger in addition to re-rendering the worktree panel; otherwise users see conflict status changes with no toast feedback.
- **Worktree diff during execution** — Never construct worktree paths from `filepath.Join(repoDir, "worktrees", worktreeBranch)` — worktrees live at `.worktrees/task_{id}`, use the `workDir`/`task.WorktreePath` variable. Use `GetWorktreeDiffWithUncommitted` for running tasks to capture both committed branch changes and uncommitted working directory changes
- **Orphan cleanup race safety** — `CleanupOrphanedWorktrees` must not delete `.worktrees/task_<id>` when task `<id>` still exists but DB worktree fields are temporarily empty (startup/dispatch race). Also, if `git worktree remove --force` returns `cannot remove a locked working tree`, skip manual filesystem deletion and retry on a later cleanup cycle.

## Task PR Workflow

- Enforce one PR per task via `task_pull_requests.task_id` uniqueness and always check persisted task PR first before calling remote PR create APIs
- PR creation must support legacy projects with empty `repo_url`: fall back to parsing `git remote get-url origin` from local repo path before failing

## Telegram Project Commands

- **Regex pattern ordering matters**: When using multiple regex patterns for the same command (e.g., "switch project to X" vs "switch to project X"), put more specific patterns (with explicit "to" after "project") BEFORE less specific patterns (with optional "to" before "project"). Otherwise the less specific pattern greedily captures "to" as part of the project name.
- **`processChatMarkers` has 6 params**: `(ctx, execID, projectID, output string, chatID int64, userID int64)`. The `userID` is needed for `[SWITCH_PROJECT]` marker processing. All test calls must include the 6th arg.

## Swagger / OpenAPI Guardrails

- Every registered `/api/*` route in `internal/handler/handler.go` must have matching Swagger annotations (`@Summary`, `@Tags`, `@Router`, and response metadata) in handler methods.
- Keep Echo path params (`:id`) and Swagger router params (`{id}`) aligned exactly; mismatches silently create stale/missing OpenAPI paths.
- Preserve `internal/handler/swagger_test.go` route/spec parity test coverage so stale endpoints or missing annotations are caught before merge.
- When updating loader markup used by chat/thread running placeholders, keep tests aligned with shared `ov-loading-dots` classes; stale `loading loading-dots` markup can fail unrelated handler suites.

## Repo Workflow

- For simple docs-only file creation, create directly — no build/test needed
