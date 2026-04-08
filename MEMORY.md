# OpenVibely - Project Memory

Condensed context so any LLM has a sense of the project when starting without context. For pitfall-prevention rules, see `guardrails.md`. For high-level development workflow and conventions, see `PRACTICES.md`.

## What This Is

OpenVibely is an open-source Go application for automated task scheduling and AI-powered execution. Users create tasks, schedule them, and have LLM agents execute them automatically. Runs locally as a web server.

## Architecture

- **Go** backend with **Echo v4** web framework
- **HTMX + Templ + Tailwind CSS (DaisyUI)** frontend (server-rendered, no SPA)
- **SQLite** via `modernc.org/sqlite` (pure Go, no CGO), WAL mode, `busy_timeout=5000`, `MaxOpenConns(1)`
- **goose v3** for embedded SQL migrations
- Background **time.Ticker** (5s) checks for due tasks and active pending tasks
- **Channel-based worker pool** dispatches tasks to the agent service
- ID generation: `lower(hex(randomblob(16)))` in SQL DEFAULT
- Baseline migration seeds the default project (no default agent/model row)

## Project Structure

```
cmd/server/main.go              # Entry point, wires everything
internal/
  config/config.go              # Env-based configuration
  database/database.go          # SQLite connection + goose migrations
  database/migrations/*.sql     # SQL migrations
  models/                       # Plain Go structs
  repository/                   # Raw SQL data access (no ORM)
  service/                      # Business logic orchestration
  handler/                      # Echo HTTP handlers
  testutil/testdb.go            # In-memory SQLite for tests
  llm/                          # LLM infrastructure (retry, normalize, stream, usage, contracts)
web/templates/                  # Templ templates (layout, pages, components)
pkg/anthropic_client/           # Anthropic API client wrapper
pkg/openai_client/              # OpenAI API client wrapper
runbooks/                       # Operational runbooks
```

## Provider Architecture

All provider logic isolated in adapter packages: `internal/llm/openai`, `internal/llm/anthropic`, `internal/llm/ollama`.

- **OpenAI**: 3-tier fallback (Responses API → Completions API → Codex CLI). Responses `SendAgentic` does Codex-style client-side history compaction for API key and OAuth flows only; pre-turn compaction uses transcript-size estimation, and mid-turn compaction uses a session token ledger based on the latest observed turn footprint (`input_tokens + output_tokens`) plus estimated tokens for local items appended after that response. If a turn hits `context_length_exceeded` before threshold-triggered compaction fires, the agentic loop force-compacts and retries that turn once (AutoCompaction path). Compaction requests use a dedicated compact prompt (`openAICompactionInstructions` by default, optional `CompactionPrompt` override) rather than the full task system prompt (Codex-style split `instructions` vs `compact_prompt`). After compaction, OpenVibely now reuses the compacted output as returned by `/responses/compact` instead of re-summarizing/re-trimming it client-side, so post-compaction context is preserved and less likely to restart from bootstrap instructions. When the transcript must be trimmed for the compaction request, keep both the opening task objective and newest context; tail-only trimming can make summaries lose the actual task. Responses stream parsing now handles reasoning summary/content deltas (`response.reasoning_summary_text.delta`, `response.reasoning_text.delta`, `response.reasoning_summary_part.added`) plus fallback reasoning/text extraction from `response.output_item.done` and `response.completed.output`, and the OpenAI adapter emits them as `[Thinking]` blocks in task/chat streams. OpenAI agentic requests set `reasoning.summary="auto"` so summary progress is surfaced similarly to Codex/Anthropic behavior. OpenAI OAuth API calls append an extra embedded `Working with the user` system-prompt section (sourced from `runbooks/codex/prompt-base-gpt-5.4.md`) to task/chat system prompts so intermediary-update guidance is present only for OAuth-backed OpenAI runs. Error classified by `internal/llm/retry/openai_classify.go`
- **Anthropic**: `ProviderAnthropic` is the only provider (no `ProviderClaudeMax`; merged migration 057). Dual path: OAuth/API key via `pkg/anthropicclient` → CLI via subprocess. Helpers: `IsAnthropicAPIKey()`, `IsAnthropicCLI()`, `IsOAuth()` in `models/llm_config.go`
- **Ollama**: `/api/chat` endpoint, `ollama_base_url` column (migration 056), defaults to `http://localhost:11434`
- **Provider adapter**: `internal/service/provider_adapter.go` routes to adapters based on provider/auth method
- **Tool calling**: 6 tools (`read_file`, `write_file`, `edit_file`, `bash`, `list_files`, `grep_search`). `edit_file` now mirrors Codex-style match tolerance: exact match first, then line-sequence fallback that progressively relaxes whitespace/Unicode punctuation matching (`trim_end`, `trim`, normalized punctuation) before declaring `old_string not found`. Agentic tool execution now runs per-turn tool calls in parallel only when every call in that turn is read-only (`read_file`/`list_files`/`grep_search`), while turns containing mutating tools remain serial for safety. OpenAI and Anthropic agentic loops treat `MaxTurns=0` as no limit (explicit caps still supported)
- **Plugin runtime bridging**: selected Claude plugins are resolved via `internal/agentplugins` and merged into agent runtime (skills + MCP servers). Anthropic/OpenAI API paths now expose plugin MCP tools in the agentic loop and route tool calls through `pkg/mcp_client`; Claude CLI path passes plugin dirs with `--plugin-dir`. Runtime plugin usage is scoped to each agent definition’s `plugins` list (per-agent enablement).
- **Persistent plugin MCP runtime**: installed plugin MCP servers are started in shared persistent mode and reconciled against installed plugins (boot/install/uninstall flows). Plugin install state is global, but plugin enablement is per-agent; global plugin disable is not used. Runtime status is exposed in `/agents/plugins/state` (`running`/`failed` + startup error details).
- **Local-only plugin management**: plugin discovery/install/update/remove now run entirely from app-local plugin storage (`./.openvibely/plugins` by default; override with `OPENVIBELY_PLUGIN_ROOT`) using local marketplace manifests and cache metadata, without relying on `claude plugin ...` commands.
- **Default marketplace seeding**: when plugin discovery is empty, OpenVibely auto-seeds the default marketplaces (`claude-plugins-official`, `anthropic-agent-skills`) into the app-local plugin root so the Agents modal plugin tab is populated out of the box.
- **Plugin root runtime fallback**: app-local plugin root resolution no longer trusts process cwd when cwd is filesystem root (`/`). In that case, OpenVibely falls back to executable/app directory so plugin seeding/discovery does not silently target `/.openvibely/plugins`.
- **Naming**: UI shows "Anthropic"/"OpenAI" with Authentication dropdown; DB stores `anthropic`/`openai` with `auth_method` (`api_key`/`oauth`/`cli`)

### Shared Infrastructure
- **Normalization** (`internal/llm/normalize`): tool call ID sanitization, replay cleanup, attachment preprocessing
- **Attachment startup cleanup**: `cmd/server/main.go` runs both task and chat attachment cleanup on boot. Repos must normalize walked filesystem paths to absolute paths before comparing with DB `file_path` values, because handlers store absolute attachment paths
- **Retry logic** (`internal/llm/retry`): error classification (retryable/fallbackable/fatal)
- **Usage accounting** (`internal/llm/usage`): enriched tracking (input/output/cached/reasoning tokens)
- **Streaming** (`internal/llm/stream`): unified event protocol across providers
- **Contracts** (`internal/llm/contracts`): canonical request/response/result types

## Git Worktree System

- **Task isolation**: Each task execution creates a git worktree in `.worktrees/task_{id}` with branch `task/{id_prefix}-{slug}`
- **Auto-merge**: Per-task and global setting. When enabled, merges to target branch on task completion
- **Merge types**: Merge commit, fast-forward only, squash merge
- **Conflict resolution**: AI-assisted via LLM agent, with manual abort/retry
- **Cleanup**: Configurable policy (after_merge, keep, manual). Removes worktree dir and branch
  - Automatic detection of manually merged branches via `IsBranchMerged()`
  - Periodic cleanup scan every 5 minutes via scheduler (`CleanupMergedWorktrees()`)
  - Orphaned worktree cleanup: Detects and removes worktrees with no corresponding task (`CleanupOrphanedWorktrees()`)
  - Orphan cleanup safety: `.worktrees/task_<id>` paths are treated as in-use when that task ID still exists (even if `worktree_path` metadata is temporarily empty), and locked worktrees are skipped instead of manual `os.RemoveAll` cleanup
  - Handles edge cases: deleted branches, force pushes, manual merges outside auto-merge, deleted tasks
- **Service**: `WorktreeService` in `internal/service/worktree_service.go`
- **Migration**: `059_git_worktrees.sql` adds `worktree_path`, `worktree_branch`, `auto_merge`, `merge_target_branch`, `merge_status` to tasks table
- **UI**: Worktree info panel on task detail, merge buttons on changes tab, auto-merge toggle in create/edit forms
- **Integration**: `LLMService.ExecuteTaskWithAgent` creates worktree before execution, runs startup sync from latest `main`/default branch when the worktree is clean, and handles post-execution merge
- **Startup sync safety**: Startup sync uses `git status --porcelain` guard (skip when dirty), logs explicit ran/skipped/failed outcomes, and aborts on merge conflicts (`git merge --abort`) while marking task `merge_status=conflict`
- **Manual merge conflict feedback**: when `/tasks/:id/worktree/merge` returns a conflict result (`merge_status=conflict`), the handler now also emits an HTMX `openvibelyToast` failure message while refreshing the worktree panel so conflicts are visible immediately.
- **Diff view**: Changes tab shows worktree branch diff when available (vs target branch), falls back to execution diff
- **Merged-branch stale-status fallback**: Changes-tab handlers now fall back to preserved execution diff when live worktree diff is empty and the task branch is already merged into target, even if `tasks.merge_status` is still stale (`pending`) before cleanup updates run
- **Scheduler integration**: `SchedulerService` runs cleanup scan every 5 minutes when worktree service is configured

## Git Lineage for Chained Tasks

- **Lineage fields** on tasks: `base_branch`, `base_commit_sha`, `lineage_depth` (migration 068)
- **Child creation captures parent lineage atomically**: when a parent completes and triggers a child chain task, the child's lineage fields are set from the parent's worktree branch + HEAD commit SHA
- **Lineage resolution priority**: parent worktree branch HEAD → parent merge target/default branch HEAD
- **Worktree setup uses lineage**: `SetupWorktree` creates child branch from `BaseCommitSHA` (preferred) > `BaseBranch` > `MergeTargetBranch` > default branch. Chained children inherit parent code changes via Git lineage
- **Worker dependency gating**: `dispatchNext()` skips chained tasks whose parent is non-terminal (pending/queued/running). Re-checked on subsequent dispatch loops
- **Branch cleanup safety**: `CleanupWorktree` checks `HasNonTerminalDescendants()` before deleting a branch; skips deletion if active descendants exist
- **Non-chained tasks unaffected**: standalone tasks dispatch immediately, lineage fields default to empty/zero

## Key Behaviors

- **Active tasks** auto-submit to the worker pool on creation or when moved to Active category
- **Model default invariant**: first created model is auto-defaulted when no models exist; deleting a default model auto-promotes another remaining model to default; deleting the last model is allowed (empty-model state).
- **No-model execution guardrails**: task actions that transition to execution (`POST /tasks`, category moves to Active, batch category moves to Active, `POST /tasks/:id/run`) and chat send (`POST /chat/send`) now block when zero models are configured, emit a single `openvibelyToast`, and include a direct `Open Models` link action.
- **RunTask idempotency guard**: `/tasks/{id}/run` now uses an atomic guarded pending update (`status NOT IN ('running','queued')`) and only submits when that update succeeds, so duplicate run requests cannot downgrade running work back to pending
- **Scheduled tasks** triggered by background scheduler when `next_run <= now`
- **One-time schedules** set `next_run = NULL` after running
- **Repeating schedules** compute `next_run` based on repeat_type (daily/weekly/monthly)
- **Tag-based execution** allows batch execution of tasks via chat commands
- **Task thread interaction** from /chat via `[VIEW_TASK_CHAT]`/`[SEND_TO_TASK]` markers. `view_task_thread` supports `offset`/`limit` pagination params; transcripts are size-budgeted (80KB total, 50KB per message) with explicit continuation hints when truncated
- **Task thread follow-up completion** now inspects streaming text-only output for `[STATUS: FAILED | ...]` / `[STATUS: NEEDS_FOLLOWUP | ...]` markers. Worktree diffs are still captured for visibility, but a missing/new-empty diff no longer turns a successful read-only task or follow-up into a failure
- **Failure transcript continuity**: execution completion preserves already-streamed `executions.output` when a failed completion call provides empty output, so failed turns keep visible output/tool context and thread history is not effectively reset during failures/retries
- **Retry writer continuity (429-safe)**: streaming writer now seeds its in-memory buffer from existing `executions.output` when initialized for an existing `exec_id` (for retryable provider retries on the same execution). This prevents retry attempts from flushing an empty/fresh buffer that would overwrite already-streamed thread history after transient errors such as Anthropic `429 rate_limit_error`
- **Thread history hydration after failures**: task-thread chat bubble cleanup now re-renders `data-raw-content` bubbles when rendered DOM is missing even if `data-cleaned-*` signatures match, preventing prior messages from appearing blank after failure-triggered `morph:outerHTML` refreshes (including provider 429/rate-limit errors)
- **Real-time file changes** stream to changes tab during task execution via SSE (every 2s diff snapshots). Uses `GetWorktreeDiffWithUncommitted` to show both committed branch changes and uncommitted working directory changes without auto-committing
- **Follow-up realtime diff parity**: task-thread follow-up executions now run the same periodic diff snapshot broadcast path (persisting `executions.diff_output` + publishing `diff_snapshot` events) so completed-task reactivation streams live Changes tab updates like initial runs
- **Changes tab scroll preservation**: SSE-triggered diff updates (`_updateDiffViewer`) now save/restore `window.scrollX`/`scrollY` and the active diff view mode (inline/split) across DOM replacements. A content fingerprint (`_computeDiffFingerprint`) skips DOM replacement entirely when the diff hasn't changed, preventing unnecessary viewport jumps. File expand/collapse state is preserved via existing `window._diffFileState` session state

## Sidebar Navigation Cleanup

- **Early mousedown signal**: Sidebar `mousedown` handler sets `_sidebarNavigating` flag immediately on pointer press (before click event processes). This is critical because in-progress `morph:outerHTML` operations block the main thread, delaying click event processing. The mousedown fires before/during the morph, allowing `htmx:beforeSwap` to suppress stale morphs that complete between mousedown and click. A 3s safety timeout clears the flag if navigation doesn't complete (e.g., drag-off or network failure).
- **Polling abort on nav**: Sidebar `htmx:beforeRequest` handler aborts all in-flight HTMX polling requests within `#main-content` (via `htmx:abort`) and disables their `hx-trigger` before sidebar navigation proceeds. Prevents expensive `morph:outerHTML` operations from blocking page transitions.
- **Stale morph suppression**: `_sidebarNavigating` flag + `htmx:beforeSwap` handler suppresses swap for any element inside `#main-content` while sidebar navigation is in progress (allows `main-content` swap itself). Flag cleared + timeout cancelled when `main-content` swap completes.
- **Post-morph work guard**: Thread view's `htmx:afterSwap` handlers check `_sidebarNavigating` and return early to skip expensive DOM work (`cleanAssistantMessages`, `renderStreamingContent`, `_initThreadStreaming`) during navigation. Thread `htmx:beforeRequest` also blocks polling requests when `_sidebarNavigating` is set.
- **Incremental thread clean pass**: `cleanAssistantMessages` now uses content signatures (`cleanedRaw`, `cleanedText`) to skip unchanged assistant bubbles during thread polling morph updates. This avoids repeatedly re-rendering old thread messages on every 3s poll and reduces main-thread stalls that delayed sidebar navigation from `/tasks` Thread tab.
- **Thread state cleanup on nav**: `htmx:beforeSwap` for `main-content` resets `_taskThreadStreamingActive`, destroys `_taskThreadPageTracker`, closes `_threadEventSources` SSE connections, and clears saved input/scroll state.
- **EventSource tracking**: Thread streaming SSE connections stored in `window._threadEventSources` array for cleanup on navigation.
- **Thread draft persistence**: task-thread textarea drafts are keyed by task ID (`window._taskThreadDrafts`) and restored after `morph:outerHTML` swaps so unsent follow-up text does not disappear while polling/refresh updates run.
- **Thread successful-send clear parity**: on successful thread form swaps with non-empty responses, `htmx:beforeSwap` clears `_taskThreadSavedInput` and the keyed draft before restore runs, so submit-button sends clear the input the same way Enter sends do.
- **Thread send path parity (Enter vs button)**: task-thread `htmx:beforeRequest`/`htmx:afterRequest` handlers now treat real thread sends as `(POST + /thread)` in addition to `#task-thread-form` trigger identity, ensuring tracker reset + auto-scroll + draft-clear behavior stays identical regardless of whether submission originated from Enter or clicking the send button.
- **Thread polling scope**: `task-thread-view` polling is now limited to `running` and `queued` task statuses (not `pending`) to avoid idle morph swaps replacing the input while users draft follow-up messages.
- **Thread EventSource lifecycle**: thread stream EventSources are now both tracked and unregistered on close (`done`/`error`/`onerror`) so navigation cleanup can close only active streams and avoid lingering connections.

## Theme Toggle Responsiveness

- Global transition choreography for theme toggles (`html.theme-transition *` + timed class removal) caused noticeable lag on dense pages. Theme switching now applies immediately by updating `data-theme` directly, then syncing icon/localStorage state.

## Tool Call Card Styling

- Chat and task Thread tool-call cards share the same renderer (`renderStreamingContent` in `web/templates/components/chat_shared.templ`) and CSS in `web/templates/layout/base.templ`, so light-mode contrast fixes should be made in shared light-theme selectors rather than page-local styles.
- Light-mode tool-call readability now uses semantic light tokens for header/icon/filename/body text (`--ov-l-text-strong` / `--ov-l-text-muted`) and status icon colors (`--ov-l-success` / `--ov-l-error`) to keep contrast high while preserving dark-mode styles unchanged.
- Light-mode tool-call hierarchy now mirrors dark mode: the outer tool body wrapper/row separators are visually transparent, while distinct card treatment (surface + border + radius) is applied only to inner IN/OUT content containers.

## Task Changes Rendering Safety

- Task detail now lazy-loads the Changes tab content unless `tab=changes` is active, so opening large completed tasks on Thread/Details no longer pre-renders heavy diff DOM in hidden tabs.
- Diff viewer now uses GitHub-style diff load envelopes: max 300 files considered, max total loadable budget 20,000 lines or 1MB raw diff, max single-file loadable budget 20,000 lines or 500KB raw diff, and auto-load threshold 400 lines or 20KB per file.
- Files above auto-load threshold but within loadable limits render with per-file `Load diff` placeholders; files beyond single-file/total loadable limits render non-loadable placeholders with reason text.
- Diff parsing now synthesizes a fallback hunk when diff content lines exist without an explicit `@@` header, so preserved/fallback diffs without standard hunk metadata still display content.
- Changes-tab live diff refreshes are now gated by active-tab checks, and task-detail file-changes listeners/SSE handlers are explicitly re-bound with cleanup so HTMX page swaps do not accumulate stale listeners or leave file-change SSE running after navigation.
- Task detail now lazy-loads Thread tab content via `GET /tasks/:id/thread` (placeholder in initial task-detail response, fetch on chat-tab activation), so heavy execution transcripts are not pre-rendered in hidden tabs.

## Real-Time Updates

- **Shared Live SSE** (`/events/live`) — multiplexed task/chat/file-change events on one stream (`task_status_changed`, `task_category_changed`, `alert_created`, `chat_new_message`, `chat_response_done`, `file_modified`, `file_deleted`, `diff_snapshot`). Sidebar owns this single per-tab stream and dispatches browser custom events (`sse-task-event`, `sse-chat-live-event`, `sse-file-change-event`, `sse-live-connected`) for page-specific consumers.
- **Chat page live updates** now consume shared `sse-chat-live-event` + `sse-live-connected` events (no dedicated `/events/chat/live` connection in the page script). Reconnect refresh remains scoped to `#chat-page-root` with `project_id` preserved.
- Chat page now tracks per-exec `/events/chat/:exec_id` EventSources in a global registry (`_chatEventSourceByExec`, `_chatEventSources`), de-dupes by `exec_id`, and force-closes streams on `#chat-page-root` / `#main-content` swaps to prevent stale stream leaks during reconnect-driven outerHTML refreshes.
- **Task detail Changes tab** now consumes shared `sse-file-change-event` events with task-id filtering in-page (no dedicated `/events/filechanges` EventSource in task-detail script).
- **Chat Stream SSE** (`/events/chat/:exec_id`) remains per-execution for token streaming.
- Legacy dedicated endpoints (`/events/tasks`, `/events/chat/live`, `/events/filechanges`) were removed; shared live updates now route through `/events/live`.
- All SSE broadcasters still enforce `MaxSubscribers` limits.

## GitHub Integration

- GitHub integration is now auth-mode aware: default/recommended mode is Personal Access Token (`github_auth_mode=pat`) for local/self-hosted OSS installs, with GitHub App as explicit Advanced mode (`github_auth_mode=app`) for cloud deployments.
- GitHub operations (clone/push/PR) mint/use operation tokens by mode: PAT directly in PAT mode, installation access tokens in App mode. Installation tokens remain ephemeral and are never persisted.
- Worktree startup sync now applies GitHub auth env for GitHub-backed repos (`git fetch origin ...`) using the same operation-token path, so private-repo startup auto-merge does not hang on credential prompts.
- `/channels` uses an Add Channel chooser (`GitHub`, `Slack`, `Telegram Bot`) and only renders active channel cards after a channel is added/configured.
- Active GitHub card supports mode-aware actions (App connect/callback/disconnect) plus kebab-menu edit/remove actions; PAT mode keeps connected status/details without token-specific inline actions.
- GitHub edit dialog pre-fills stored PAT/private key values and keeps them masked by default; users can explicitly reveal via eye toggles (Telegram-style secret UX parity).
- Projects now support `repo_url` in addition to `repo_path`. Create/Edit source modes are controlled only by `OPENVIBELY_ENABLE_LOCAL_REPO_PATH` for `Local Path` availability; unset/invalid defaults to GitHub-only mode. GitHub URL mode clones into managed storage (`PROJECT_REPO_ROOT`, default `./repos`) and Edit performs re-clone swap behavior.
- Project create/edit GitHub clone failures now return HTMX toast guardrails (`openvibelyToast`) instead of swapping raw error payloads; New Project modal submits with `hx-post` + `hx-swap="none"` and successful HTMX creates navigate via `HX-Redirect`.
- Task Changes tab supports one-click PR creation (`POST /tasks/:taskId/worktree/pull-request`) with one PR per task persisted in `task_pull_requests`; if a task PR record already exists it is reused, otherwise existing remote branch PRs are detected/reused before creating a new ready PR.
- Task Changes tab merge dropdown visibility is feature-flagged by `OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS` (default off). When disabled, merge actions are hidden in Changes tab and Changes-tab-triggered merge posts are blocked server-side (`merge_source=changes_tab`) with `403`.
- Task Changes tab and WorktreeInfoPanel merge dropdowns use section-grouped menus: `Local` section (merge commit, fast-forward only, squash merge) and `GitHub` section (Create PR / View PR #N). This removes ambiguity between local and remote actions. The Changes tab uses a single "Actions" dropdown combining both sections; the WorktreeInfoPanel keeps the "Merge to <branch>" dropdown with a `Local` section header.
- Changes-tab grouped action rows reserve the same leading spinner slot across both sections (`Local` merge actions and `GitHub` PR actions) so actionable labels stay horizontally aligned in light and dark themes.
- Toast messages for merge/PR actions are destination-prefixed: "Merged locally into <branch>", "GitHub PR created (#N)", "GitHub PR already exists (#N)".

## Chat System

- Root route `/` now redirects to `/chat` (preserving `project_id` when provided). Dashboard remains available at `/dashboard`.
- Chat send (`POST /chat/send`) and task run (`POST /tasks/:taskId/run`) now hard-block when zero models are configured. HTMX callers receive `204` + `HX-Trigger` `openvibelyToast` payload so users see an immediate setup toast instead of a silent failure.
- **Chat** = main orchestrator at `/chat` (global project-level conversation)
- **Thread** = task-specific conversation on task detail page ("Thread" tab)
- `/chat` now supports two modes: `orchestrate` (default) and `plan` (read-only planning)
- `plan` mode enables read-only repo exploration tools (`read_file`, `list_files`, `grep_search`) and blocks mutating tools (`write_file`, `edit_file`, `bash`)
- `plan` mode disables chat marker execution (`ProcessMarkers=false`) so no task/settings mutations run from marker blocks
- **Canonical chat capability registry** (`internal/chatcontrol/registry.go`): single source of truth for all 28 chat-controllable actions. Defines each action's name, domain, read/write access, allowed modes (plan/orchestrate), supported surfaces (web/api/telegram/slack), confirmation requirements, and sensitivity classification. All tool definitions, mode gating, and surface availability are derived from the registry
- **Registry-wired action tools**: Web/API chat (`chat_action_tools.go`) and channel services (Telegram/Slack) all generate runtime tool definitions from `chatcontrol.ToolDefsForContext(mode, surface, includeThread)` — no hand-crafted tool lists
- **Mode enforcement**: plan mode only receives read actions (via registry filter); orchestrate gets full read+write set. Write actions in plan mode return structured `ActionError` with code `mode_blocked`
- **Surface enforcement**: each action specifies which surfaces support it; unknown actions return `ActionError` with code `unknown_action`
- **Runtime-tools vs marker processing are mutually exclusive per request**: when runtime tools are injected, `ProcessMarkers=false` prevents duplicate execution
- **New actions added across all surfaces**: `get_chat_mode`, `set_chat_mode`, `list_capabilities`, `get_alert`, `get_model`, `get_personality`, `get_current_project`, `switch_project` (web/API parity with channels)
- Chat entrypoints now execute actions via runtime tool calls (web `/chat`, API chat, Slack, Telegram) and do not post-process assistant output markers
- Legacy marker parser helpers remain in code for compatibility/tests, but chat runtime should not depend on assistant-emitted marker blocks
- `/chat` now shows a post-plan handoff prompt when a completed assistant response contains `<proposed_plan>` while in plan mode; clicking `Switch to Orchestrate` flips mode and auto-submits a single-task handoff message (`create one active task for the first plan step`, do not start other existing tasks)
- Plan-mode system prompt guidance is prose-first (discourages default numbered outlines) while still requiring a single `<proposed_plan>...</proposed_plan>` output block
- `/chat` plan-completion prompt uses a centralized evaluator (`evaluatePlanCompletionPrompt`) called from all completion paths: per-exec stream `done`, `chat_response_done` live SSE fallback, stream error/onerror, and initial hydration/HTMX swap. Prompt visibility requires all three: stream completed (`_chatStreamInProgress` false), mode is `plan`, and latest completed assistant response contains `<proposed_plan>`. A `_chatStreamInProgress` flag tracks active streaming and is set on send/new-message, cleared on done/error. The evaluator checks only the latest completed assistant bubble (newest-first scan, returns on first non-empty match) — older messages with plan markers do not trigger the prompt. `ChatResponseDone` events include `CompletedOutput`, and the handler now also reconciles the active assistant bubble with that completed output before prompt evaluation so final stream tails render immediately without refresh. Reconnect refresh skips full `/chat` outerHTML swaps while an active stream bubble is present to avoid replacing live content with partial persisted state.
- `/chat` mode-selector hydration (hidden input + localStorage restore) now marks selector hydration state (`data-hydrated`) and re-evaluates `evaluatePlanCompletionPrompt` after restoring persisted mode and on mode changes. `currentChatModeValue()` uses persisted localStorage mode during pre-hydration windows, preventing blur/focus reconnect HTMX refreshes from transiently hiding `Switch to Orchestrate` when durable latest assistant state still contains `<proposed_plan>`.
- `/chat` plan mode keeps read-only repo exploration tool cards (`read_file`, `list_files`, `grep_search`) visible in assistant bubble rendering so planning steps remain inspectable during live/streaming turns and on refresh
- Chat streaming render paths now batch per-chunk tool/text rendering with `requestAnimationFrame` (plus a final forced flush on `done`) to keep plan-mode tool-heavy streams responsive and avoid “updates only after tab refocus” behavior
- Runtime `execute_tasks` filtering now excludes completed tasks/statuses by default; re-running completed tasks requires explicit `include_completed=true`
- Runtime `execute_tasks` also supports exact single-task targeting by `task_id` or `title`; use exact targeting for specific-task requests instead of broad tag/priority filters
- Chat uses `callClaudeCLIChat` (clean prompt + history); tasks use `callClaudeCLI` (directives + STATUS markers)
- Legacy action-marker fallback set: `[CREATE_TASK]`, `[EDIT_TASK]`, `[EXECUTE_TASKS]`, `[VIEW_TASK_CHAT]`, `[SEND_TO_TASK]`, `[SCHEDULE_TASK]`, `[DELETE_SCHEDULE]`, `[MODIFY_SCHEDULE]`, `[LIST_PERSONALITIES]`, `[SET_PERSONALITY]`, `[LIST_MODELS]`, `[VIEW_SETTINGS]`, `[PROJECT_INFO]`, `[LIST_ALERTS]`, `[CREATE_ALERT]`, `[DELETE_ALERT]`, `[TOGGLE_ALERT]`
- Interactive chat bypasses worker limits; thread follow-ups respect them
- Chat uploads live under `uploads/chat/...`; task attachments may exist under legacy `uploads/{taskID}` paths and newer chat-copy paths under `uploads/tasks/{taskID}`. Task-attachment cleanup must skip the `uploads/chat` subtree entirely; chat attachments are cleaned separately by `ChatAttachmentRepo`
- Runtime artifact directories (`uploads/`, managed `repos/`, and any package-local test spillover like `internal/service/uploads/`) are treated as non-source data and should remain ignored/untracked in git.
- Chat/thread running placeholders now use shared custom loader markup (`ov-loading-dots` + `ov-loading-dot`) instead of DaisyUI `loading loading-dots`, with bouncing motion and token-driven color animation tied to the same primary theme color used by `btn-primary` send buttons (`oklch(var(--p))` / softened `oklch(var(--p) / 0.45)`) in `web/templates/layout/base.templ`.
- Chat streaming resume dots stay visible for running executions with partial output: `streaming-dots-resume` must use `hidden` only when `partialContent == ""`. Using Tailwind `!hidden` in that slot hides dots with `!important` and breaks the gray in-progress phase after refresh/reconnect.
- `/chat` plan-complete CTA now uses a small page-level latch (`window._chatPlanPromptLatched`) so tab-refocus reconnect/hydration scans that temporarily return empty latest assistant text do not erase a previously earned `Switch to Orchestrate` prompt. The latch is set only when a completed plan response is detected, preserved across DOM scans/refocus, and explicitly cleared on genuine state changes (new send/stream start, mode change away from plan, explicit dismiss, or completed non-plan response).

## OAuth

- Both Anthropic and OpenAI support OAuth with refresh tokens
- OAuth callback servers auto-start on `OAuthInitiate`, auto-shutdown after callback/timeout
- Tokens auto-refresh within 1 hour of expiry, persisted back to DB
- `oauthServers` map tracks running servers by config ID; `shutdownPreviousOAuthServer()` prevents orphans
- OpenAI OAuth payload shape differs by endpoint: `/responses` accepts `store=false`, but `/responses/compact` rejects `store` with `400 unknown_parameter`

## Shared Code Conventions

- Layered architecture convention: models → repository → service → handler → templates
- Circular deps solved via `SetLLMService()` setter, wired in `main.go`
- Adding services to `handler.New()` requires updating ALL test files (add `nil` for new params)
- Dedup convention: check by title + type + project, exclude inactive

### Shared Helpers (Don't Redeclare)

| Function | File |
|---|---|
| `util.Truncate()`, `util.TruncateWithSuffix()` | `util/strings.go` |
| `util.ExtractJSONObject()`, `util.ExtractJSONArray()`, `util.StripMarkdownFences()` | `util/json.go` |
| `cleanChatOutput()`, `cleanChatOutputForDisplay()` | `service/llm_service.go` |
| `buildTaskPromptHeader()`, `buildAttachmentInstructions()`, `buildChatHistoryText()` | `service/llm_service.go` |
| `getCurrentProjectID()`, `isHTMX()`, `parseIntClamped()` | `handler/handler.go` |

## Telegram Bot

- Chat orchestrator proxy: forwards to same chat backend as `/chat`
- Optional; token via `TELEGRAM_BOT_TOKEN` env or Settings page
- Per-user project context: in-memory cache backed by `telegram_user_projects` table (migration 053)
- Authorization: `telegram_authorized_users` table (migration 043), deny-by-default
- All 19 markers handled in both web (`processChatResponse`) and Telegram (`processChatMarkers`) paths
- `[LIST_PROJECTS]` and `[SWITCH_PROJECT]` markers for project listing/switching via chat
- **Natural language project commands**: Telegram bot intercepts "list projects", "switch to project X" etc. before forwarding to LLM, using `handleNaturalLanguageProjectCommand()` with `isProjectListRequest()` and `extractProjectSwitchTarget()` helpers
- `processChatMarkers` signature includes `userID int64` param (6th arg) for project switching
- **Task origin tracking**: `created_via` and `telegram_chat_id` columns on tasks (migration 061). Tasks created via Telegram get `created_via='telegram'` and the originating chat ID
- **Send task responses**: Configurable toggle (`telegram_send_responses` setting, default: enabled). When enabled, sends completion/failure notifications back to the Telegram user who created the task. Only tasks created via Telegram bot receive notifications; web-created tasks never do. Chat-category tasks are excluded (they already get direct responses)

## Slack Integration

- Slack integration is first-class in `/channels` with configure/connect/callback/disconnect/remove/test endpoints.
- Runtime uses Slack OAuth + Socket Mode. Accepted incoming events are app mentions and DMs; bot/self events are ignored.
- Slack bot token source is mode-based: OAuth callback token (`slack_bot_token`) or manual override token (`slack_bot_token_override`) selected by `slack_bot_token_source`.
- Local setup and HTTPS redirect troubleshooting are documented in `runbooks/slack-channels-setup.md`.
- Socket Mode + Event Subscriptions must both be enabled in the Slack app (`app_mention` and `message.im`) or inbound messages will not reach OpenVibely even with valid tokens.
- Slack chat processing uses the same core chat-context + marker flow as Telegram/web for core markers (create/edit/execute tasks and list/switch project).
- Slack user project selection persists in `slack_user_projects` and task reply context persists in `slack_task_context` (migration 066).
- Slack authorized access now mirrors Telegram-style project-scoped controls via `slack_authorized_users` (migration 067). `/channels` Slack configuration includes an `Authorized Users` section (list/add/remove + helper copy). Enforcement is allow-by-default when the list is empty and restricted to listed `slack_user_id` values when non-empty.
- Tasks created from Slack marker actions are tagged with `created_via='slack'`.
- Task completion/failure notifications can be sent back to the originating Slack thread when the task origin is Slack and `slack_send_responses` is enabled; chat-category tasks are excluded.

## Pages

- `/chat` — Chat orchestrator. Task-creation result rows rendered inside assistant tool-output cards now use shared semantic UI primitives: `ov-task-result-link` (lighter 1px underline + tuned offset/focus ring) and `ov-task-result-start-btn` (balanced `btn-xs` typography/padding with explicit hover/focus/active/disabled states) for consistent light/dark rendering.
- `/dashboard-mockup` — Isolated onboarding-runway dashboard mockup (non-production preview route that does not replace `/`) focused on first-time user activation with a single goal input, suggested use-case prompts, and a Power Moment state that naturally routes users into Chat/Pulse/Reflection/Tasks
- `/personality` — Personality page (card-list UX with create/edit modal). Template: `app_settings.templ`. Cards for Base (no personality) + built-in presets + custom personalities. Base card pinned first, labeled "Base" with "Default" badge when selected, never highlighted with active ring styling. Base card kebab hidden when base is selected; shown with "Set as Default" when another personality is active. Built-in overrides shown with Override badge, custom with Custom badge. Active personality marked with Active badge and ring highlight on non-base cards. Edit dialog includes "Set as Default" button for non-default personalities (uses `data-selected-personality` on section root for JS detection). `handlePersonalitySave` returns re-rendered personality section for HTMX requests.
- `/channels` — Channel integrations (GitHub, Slack, Telegram). Template: `settings.templ`
- `/channels` now hosts GitHub PAT (recommended) and GitHub App (Advanced) controls for repository auth
- Project Create/Edit `Choose Folder` uses a local server endpoint (`POST /projects/pick-folder`) with OS-native desktop dialogs (`osascript` on macOS, `zenity`/`kdialog` on Linux when installed, `powershell` FolderBrowserDialog on Windows), returning normalized absolute paths when local-path mode is enabled; when unavailable, endpoint/UI return actionable manual-path fallback messaging instead of hard failure
- In GitHub-only mode, new project creation only exposes `GitHub URL`. Existing legacy local-path projects remain editable without forced migration: local repo path settings are preserved on save
- Project Repository Path selection applies picker-derived paths only when they are absolute filesystem paths; non-absolute picker output falls back to manual absolute path entry guidance
- Browser chooser output may return home-relative paths (for example `~/go/src/...`). Project create/update now normalize `~`/`~\` to the OS user home directory before persisting so runtime workdir remains absolute
- `/workers` — Worker pool settings and utilization (single `Worker Capacity & Utilization` table-style card combining global + per-project rows; global row is pinned first with a `Global` scope badge and inline global limit edit)
- `/workers` worker-limit inputs use dirty-state highlighting (`input-warning`) only while editing; successful `Set` updates suppress dirty-state restore during immediate HTMX/poll swaps so warning/focus-like rings do not appear stuck after submit. Worker-page event handlers are window-guarded to avoid duplicate listener registration across `#worker-settings-content` HTMX outerHTML swaps
- `/models` — LLM model configuration (code: `LLMConfig`, DB: `agent_configs`). The `Default` badge now uses the shared `ov-badge-default` class (same canonical purple/indigo style as `/personality`) for consistent background/text/border states across themes.
- `/agents` — Agent definition management with plugin-first modal (marketplaces, installed/available plugins, per-agent plugin selection) plus modal "Generate" flow via `POST /agents/generate` and plugin endpoints under `/agents/plugins/*` (state, install, disable, uninstall, marketplace controls). Agent definitions no longer include a `color` field or color UI controls.
- Task kanban cards now render agent-definition badges by resolving `task.agent_definition_id` against the agent definition list passed into `components.KanbanBoard`
- Tasks board column card width is now consistent across Backlog/Active/Completed by keeping Active sub-dropzone wrappers class-aligned with other columns (no extra inner padding that narrows Active cards)
- Task thread/send-to-task/review follow-up flows resolve and forward `task.agent_definition_id` into streaming calls so agent plugin skills/MCP tools are active for API provider paths (not just full task runs)
- Tasks page no longer supports task import/export. Removed surfaces include top-level Tasks header actions, backlog menu import/export entries, task card/detail export actions, Tasks import modal + file handlers, and `/tasks/import` + `/tasks/export` routes/handlers.
- Agent generation now pins to the default configured model, uses a 90s request timeout with one transient-timeout retry for default-model generation, and returns `generation_mode`/`generation_error` so the UI can signal when local template fallback was used (including timeout/cancellation-specific messaging)
- Agent generation JSON handling now tolerates wrapper/prefix text and nested payloads, validates generated-agent payload shape, and runs a same-model JSON-repair reprompt before falling back so malformed model output is less likely to surface as hard fallback.
- Agent generation now also treats provider-side malformed-JSON errors (for example `invalid JSON from model ... invalid character ...`) as recoverable: it routes those errors through the same repair pipeline and a strict-JSON retry prompt before falling back.
- Agent generation fallback toast copy is now non-alarming (`Generated using local template draft`) and uses warning-style status instead of failure styling when a usable fallback prompt is produced.
- Agent generation now explicitly instructs the model not to execute tools/plugins/MCP during JSON drafting, and generation parsing strips leading tool-wrapper transcript lines (for example `[Using tool: ...]`) before JSON extraction so tool chatter cannot trigger malformed JSON fallback.
- GenerateAgent now runs in strict no-tools/no-plugin-runtime mode end-to-end: it calls `CallAgentDirectNoTools` on the first attempt, does not resolve plugin runtime bundles, and does not include plugin IDs/MCP server hints/tool catalogs in the generation prompt. Plugin selection still persists in the response payload, but plugin tools remain task-execution-only runtime context.
- OpenAI direct no-tools generation now propagates `DisableTools` through provider-adapter wiring into the direct OpenAI client request, and the client sets `tool_choice: "none"` for `Send` calls when `DisableTools` is true.
- OpenAI direct no-tools JSON generation also bypasses stream pseudo-tool marker rewriting (`{...}`/tool-snippet sanitizer) and suppresses output-item tool markers, preventing valid JSON deltas from being rewritten into `[Using tool: ...]` text (for example `[Using tool: Unnamed OpenVibely Agent]`) in GenerateAgent/repair flows.
- Agent generation seeds fallback drafts with discovered local MCP servers (`~/.claude/settings.json`, `.claude/settings.json`) and local skill names (`~/.claude/skills`, `.claude/skills`) for better default prompt quality when LLM generation is unavailable
- Agents plugin modal now shows a visible loading spinner while `/agents/plugins/state` is fetching, per-plugin Install buttons switch to disabled `Installing...` state, marketplace Add/Restore Defaults controls show disabled in-progress text states (`Adding...`, `Restoring...`) with spinners, and per-marketplace Sync/Remove controls are compact icon-only actions (sync + trash icons) with disabled/`aria-busy` handling and accessible `aria-label`/tooltip text. Plugin Uninstall shows disabled `Removing...` state. Marketplace/plugin actions also surface inline status/error text in the modal so failures remain visible and actionable beyond transient toasts.
- Marketplace source cards in the Agents modal now preserve full URL visibility for manually added entries by preferring URL/source fields over repo shorthand in UI rendering, and marketplace add requests now attempt the original user-entered source string before normalized GitHub shorthand.
- Plugins tab Available rows intentionally avoid redundant status chips next to plugin names; install is shown as a clear button-style CTA (not plain text) while keeping the same install/loading/disabled behavior and right-aligned row action layout.
- Agent create/edit modal now uses three top-level tabs (`Agent Details`, `Plugins`, `Marketplace`) with a single tab-state model. The modal keeps fixed-height content, per-panel internal scrolling, and sticky bottom actions while preserving plugin-selection and marketplace-management workflows.
- Global toast rendering is top-layer aware: when any `dialog.modal` is open, the shared `#toast-container` is re-hosted into the top-most open dialog so toasts stay above modal panels/backdrops. When dialogs close, toasts re-host back to `document.body`.
- Agent Details now has a single description input flow: generation helper copy and the `Generate` action sit with the `Description` field above `System Prompt` (no separate top prompt block), while generation still posts `description` to `POST /agents/generate` and populates the same form fields.
- Agent create/edit `Model` dropdown is now populated from configured `/models` entries (multi-provider, including OpenAI + Anthropic) with labels from config names and values from config model IDs. Legacy hardcoded `Sonnet/Haiku/Opus` options are removed.
- Agent model normalization now accepts only `inherit` or currently configured model IDs (from `agent_configs.model`), so configured OpenAI overrides persist on create/edit and generation output normalization no longer collapses valid model IDs back to `inherit`.
- Plugins tab installed rows are intentionally compact single-line entries: no description text, per-agent enablement via toggle switch, and icon-only trash uninstall action. Toggle/remove controls disable while plugin loading/install/uninstall is in flight to prevent duplicate clicks.
- Agent modal plugin-state fetches are sequence-guarded (`pluginStateRequestToken`) so stale `/agents/plugins/state` responses from older in-flight requests cannot overwrite newer install/uninstall results. Install/uninstall now apply an immediate local state mutation before refresh so rows update instantly after success.
- Agent modal uninstall now tracks pending uninstall plugin IDs during refresh and filters those IDs out of refreshed installed state, preventing stale fetch/reconcile responses from briefly reinserting removed rows (the second-click symptom).
- Plugin uninstall removes app-local cache/install paths only; uninstall no longer depends on external CLI uninstall state.
- Successful uninstall no longer emits reconcile/discovery warning payloads as global error toasts; unrelated bad plugin runtime/reconcile errors remain visible via runtime row status and alert surfaces instead of being attached to the uninstall success path.
- Install-triggered plugin-state refreshes in the Agent modal now suppress global discovery-warning toasts, so successful install actions show only install-coupled feedback. Unrelated persistent runtime/reconcile failures remain visible through installed-row runtime status and alert surfaces.
- Plugin installs initiated from the Agent modal now support contextual one-click behavior: install remains global, and when modal is editing an existing agent the same request auto-enables the plugin for that agent. In create flow (no persisted agent ID yet), install auto-selects the plugin in modal state so it is saved enabled on create.
- Agent modal install API responses now expose `enabled_for_agent` and `enable_error` (when `agent_id` is provided) so UI can reflect partial success: install succeeded globally but per-agent enable failed and should be retryable.
- Plugin runtime health is now surfaced inline in installed plugin rows (same status dot): green dot for running/healthy runtime, red dot for failed runtime with hover tooltip showing the runtime error. The standalone `Plugin MCP Runtime` panel and helper copy `Installed and available plugins across marketplaces.` are removed from the modal.
- Plugin runtime status entries now include `plugin_id` to correctly match MCP server names (from `.mcp.json`) to plugin IDs (e.g. MCP server `adspirer` → plugin `adspirer-ads-agent@claude-plugins-official`). Frontend lookup tries full plugin ID first, then falls back to name prefix.

## Swagger / OpenAPI

- Swagger generation now covers all registered `/api/*` routes, including analytics, capacity, workflows metrics/votes, autonomous/trends, collisions, chat, and projects.
- `internal/handler/swagger_test.go` now enforces route/spec parity by comparing registered Echo `/api/*` routes against `docs.SwaggerInfo.ReadDoc()` and failing on either missing or stale endpoints.
- Swagger endpoint checks now validate both UI (`/swagger/index.html`) and raw spec (`/swagger/doc.json`) responses.

## Key Dependencies

- `github.com/labstack/echo/v4` - Web framework
- `github.com/a-h/templ` - Type-safe HTML templates
- `github.com/anthropics/anthropic-sdk-go` - Anthropic API client
- `github.com/pressly/goose/v3` - Database migrations
- `modernc.org/sqlite` - Pure Go SQLite driver
