---
name: realtime_and_frontend_patterns
type: project
created: 2026-05-09
updated: 2026-08-28
source: consolidation
source_id: memory_consolidation_2026_08_28
confidence: high
title: Realtime and Frontend Patterns
---

OpenVibely uses server-rendered HTMX/templ UI with shared SSE-style live updates. Durable UI contract: SSE announces that something changed, while HTMX/server-rendered templ fragments remain authoritative state.

Realtime and diff contracts:
- Shared `/events/live` multiplexes task, chat, mixture-progress, and file-change invalidation events. Sidebar-managed JS dispatches events such as `sse-task-event`, `sse-chat-live-event`, `sse-file-change-event`, and `sse-live-connected`.
- `window._tabVisibility` owns broad realtime visibility behavior and pauses polling while hidden.
- Per-execution `/events/chat/:exec_id` streams are token-style output path. SQLite is durable transcript/reconnect source; hot deltas flow through injected `events.ExecutionStreamHub`, not SQLite polling.
- `ExecutionStreamHub` is keyed by exact execution ID, supports multiple subscribers, enforces subscriber limits, drops slow-subscriber deltas nonblockingly, closes on terminal events, and must be dependency-injected.
- `internal/events` uses one private generic broadcaster core for the three typed broadcasters (`Broadcaster`, `ChatBroadcaster`, and `FileChangeBroadcaster`). The core owns the mutex-protected subscriber registry, capacity checks, idempotent unsubscribe/close, nonblocking publish, and count lifecycle; typed wrappers preserve the public constructors/APIs and buffer capacities (`10`, `10`, and `50`). Handler coverage must verify request-context cancellation returns task/chat/file subscriber counts to zero and that max-subscriber failures return HTTP 503 while cleaning up earlier successful subscriptions.
- `llmstream.Writer.Write` publishes live deltas with cumulative UTF-8 byte offsets after releasing its mutex. It must not publish seeded existing output or `WriteText` content, and must not synchronously flush SQLite on the hot token path.
- Periodic/final stream persistence must serialize snapshot plus SQLite update so older periodic snapshots cannot overwrite newer final flushes.
- Per-execution SSE clamps optional untrusted UTF-8 byte offsets, subscribes before DB catch-up, skips duplicate/partial overlaps, and uses targeted DB catch-up for dropped deltas or terminal fallback.
- Fresh/resume/live-created Chat/task-thread streams track raw UTF-8 byte lengths, not rendered DOM text, normalized display content, message counts, or scroll/window state.
- Terminal execution-stream events publish only after durable terminal writes. Completed/cancelled map to SSE `done`; failures map to SSE `error`; terminal handlers replay missing durable output before terminalizing subscribers.
- Real-time file changes are invalidation/snapshot signals. `diff_snapshot` events are metadata-only; full diffs stay in DB/routes and browser refreshes authoritative Changes fragments.
- Task detail lazily loads Changes unless `tab=changes` is active; direct `?tab=changes` renders must match lazy route behavior.
- Diff viewer uses GitHub-style load envelopes, oversized placeholders, deletion summaries, constrained rename/copy headers, and viewport-safe overflow. Worktree semantics live in `worktree_and_lineage.md`.
- Task Changes diff view mode is app/system UI preference `app_settings.ui.diff_view`; default `inline`, alternate `split`, legacy `unified` normalizes to `inline`. Only user toggles should persist it.

Chat and task-thread rendering:
- Chat and task-thread streaming batch DOM rendering and force final flush on completion.
- Live Chat completion reconciliation targets inner assistant stream node `streaming-message-<exec_id>`, not outer turn wrapper.
- Task-thread live execution starts append authoritative server-rendered execution fragments. Page-level listeners must not force-reload `#thread-content` on start/input-applied events because that can replace fresh streams.
- Terminal completion fallback must reconcile assistant output and terminal/composer state together; it must not leave loading dots as visible assistant bubble.
- Streaming bubble scripts target explicit thinking/loading IDs because mixture-progress slots may appear between elements.
- Queued/steering-pending Chat/task-thread messages render as compact composer/input-box rows, not transcript bubbles. Attachment indicators must appear consistently across HTMX fragments, refresh/reconnect, and live-created rows.
- Chat live `chat_new_message` handling must let queued attachment events reach queued-row path; attachment transcript-refresh branch applies only to non-queued messages.
- Queued-to-steering conversion publishes live steering events so other tabs replace stale queued rows. Same-tab conversions guard local HTMX swap target until response settles.
- Promoted queued task-thread runs are recovered through live events that remove pending rows, append execution fragments, and attach streams. Either execution-started or input-applied event should recover UI if the other is missed.
- Pending containers, queued rows, steering rows, OOB appends, and live DOM mutations are task-scoped with task-qualified selectors.
- Initial Task Run publishes `task_thread_execution_started` after execution row exists. Live reconnect/start/status refreshes discard stale visible-task responses and must not replace composer with active pending uploads.
- Terminal reconciliation uses persisted authoritative execution status/output regardless of whether shared completion or per-execution terminal arrives first.
- Known gap `#226`: hard refresh after cancellation does not show same explicit terminal marker as completed/failed threads.
- Long Chat and Task Thread histories remain complete in DB but are UI-windowed with scroll-top pagination; initial/older windows default to 30 and cap at 100. Browser-memory protection removes old execution DOM nodes, not hides them.
- Completed/error assistant bubbles render compact `data-raw-content` markup and hydrate through the shared scanner/queue once per page or HTMX fragment instead of emitting repeated per-bubble inline scripts. The path preserves Markdown/code hydration, task-link conversion, copy buttons, failed/cancelled partial output, pagination insertion, morph swaps, streaming/running resume, and `hx-preserve`.
- Active streaming uses smart autoscroll: pinned viewers follow growth, upward user movement is read intent.
- Tool result output remains full-fidelity; canonical tool-result markers are not display-truncated and render in responsive scroll containers.

Shared composer and steering UI:
- Chat and task-thread composers share `web/templates/components/chat_shared.templ` and one `ChatInputForm` contract.
- Active primary button becomes Stop with same primary styling and square stop icon. Sends/stops that do not replace full form return OOB primary-action fragment.
- While running, primary action is reconciled client-side from current trimmed text: empty shows Stop, non-empty shows Send for queue/steer. Server-rendered OOB fragments remain authoritative for running/idle transitions.
- Submit logic resolves/delegates to current submit button after OOB swaps rather than caching original node.
- Platform hints are concise and Apple detection prefers `navigator.userAgentData.platform` with fallback.
- Steering, queue, and draft/attachment-session semantics are canonical in `chat_thread_system.md`; this topic owns their DOM/OOB rendering, stale-response handling, and hydration behavior.
- Desktop Chat clear-actions uses a semantic button with `aria-expanded` and explicit `data-chat-actions-open` state rather than a focusable DaisyUI label. CSS must explicitly enforce hidden and open states because WebKit/DaisyUI defaults can make a marker-only open state appear inert. Focus-out, outside pointer activity, history restoration, and fresh fragment initialization reset the state; Escape restores focus to the live trigger, and Clear Chat preserves `hx-confirm="Clear all chat history? This cannot be undone."`. Native WebKit coverage is required for desktop-specific focus and visibility regressions.
- Known race `#48`: overlapping composer action-only refreshes lack stale-response guards and can regress Send to Stop.

Transcript safety and navigation:
- Final Chat/task-thread Markdown rendering uses shared escaped/unmatched/multiline code-range parser. Raw `<` outside protected code is escaped before Marked, and output is sanitized to remove dangerous elements/attributes/URLs.
- Safe external Markdown/autolink anchors open outside the app view. Desktop uses authoritative runtime marker and `/open-external` bridge when Wails globals are unavailable. Internal app links preserve normal navigation.
- Missing/incomplete/configuration-failing/parse-failing Marked returns escaped plain text while preserving Markdown source provenance.
- Go, browser, and generated transcript parsing treat LF, CRLF, and bare CR as equivalent CommonMark line endings without normalizing source bytes. Controls/examples remain visible/inert unless exact grammar matches.
- Browser titles use server-rendered `PageTitle` markers as source of truth for full documents and HTMX navigation.
- Programmatic navigation must use `window.openVibelyNavigate`, not manual `history.pushState()` plus `htmx.ajax()`.
- HTMX history cache-miss requests are full-document requests with app history element and authoritative title; ordinary HTMX requests remain fragments.
- Reconnect/refocus should be revision-gated: identical revisions cancel swaps before scroll/draft capture, EventSource cleanup, or DOM mutation.
- Completed execution pairs should use stable IDs/HTMX preservation so unchanged bubble DOM and expanded tool outputs survive transcript morphs.
- Known review navigation gaps: execution detail pages exist but normal Thread/Reflection review surfaces do not expose visible links (`#365`, `#380`).
- Forced task-thread refreshes must close per-exec EventSources before replacement, but revision no-op checks run before cleanup.
- Older-history pagination captures scroll intent revision and cancels stale restoration if newer send/scroll changes intent.
- Known frontend duplication gaps include task-thread scroll-state helpers, terminal UI/composer reconciliation, live queued-row construction, task-thread post-swap hydration, schedule repeat-interval controllers, Task Templates dashboard refresh, and execution status badge rendering.
- Sidebar collapse toggling, including click and Ctrl/Cmd+B, delegates to one helper that owns class state, localStorage mirroring, `/ui/preferences` persistence, and accessibility/body markers; early server-rendered restore remains separate.
- Browser clipboard writes delegate to the shared base-layout `window.openVibelyCopyText` helper for Clipboard API use, hidden-textarea fallback, cleanup, and feedback restoration. Surface wrappers retain their labels/icons/toasts.
- Kanban backlog priority bulk execution uses canonical priority ordering/labels and only renders eligible priority actions.
- Destructive delete confirmations share one dialog component across projects, tasks, models, agents, skills, schedules, channels, and related settings while entity-specific behavior remains local.
- Task detail HTMX refresh assembly and task-board mutation/sort HTMX refresh assembly should stay centralized through private helpers while preserving route-specific behavior.

Responsive and shared UI contracts:
- Chat/thread/message panes, bubbles, composers, and tool/code output must not create whole-page horizontal scrolling on mobile. Use `min-w-0`, `max-w-full`, wrapping, and inner overflow boundaries.
- Avoid hard `overflow-x-hidden` on immediate chat/thread roots, message scrollports, or rounded composer shells when it clips shadows/bevels.
- Chat/task-thread bubbles and composer align visually with Agents/Models card widths while preserving in-pane scrolling and unclipped shadows/tails.
- Composer bottom controls remain contained on mobile. Selectors shrink/truncate; action clusters do not shrink.
- Chat/task-thread model and mode selectors use custom portal dropdowns appended to `document.body`, with hidden inputs carrying values.
- Preserve native macOS overlay scrollbar behavior; Firefox scrollbar styling is acceptable.
- Task detail Details tab order is Prompt, Goal, Git Worktree. Seven-tab row remains horizontally scrollable/no-wrap on narrow screens.
- Completed task Thread views include next-step CTA after terminal status: Changes when diff exists, merge guidance only when merge state indicates it, and no-diff/no-output guidance otherwise.
- Task Changes/diff surfaces stay viewport-contained on 320px screens. Long filenames, branch labels, and dropdown stacking are recurring risks.
- Tasks page uses server-rendered kanban components: one column on phones, two around tablets, three on desktop, no phantom fourth column, mobile-safe wrapping, independent mobile dropzone scrolling, compact desktop density, and `+ Add Task` colocated with title.
- Known board/card gaps: task cards lack worktree/merge state (`#227`) and linked PR URL/status (`#374`).
- Active kanban pending dropzones render only real active pending/queued/blocked work; terminal failed/cancelled rows must not appear as queued work.
- Tasks page date sorting defaults newest-first for Backlog and Completed. Task Detail edit-form saves are metadata-only.
- Task tag badge labels/classes are centralized through exported task-card component helpers such as `components.TagLabel` and `components.TagBadgeClass`; Pulse `/upcoming` reuses the same mapping instead of rendering raw tag strings or a page-local badge switch.
- Known Pulse actionable-failure gap [#866](https://github.com/openvibely/openvibely/issues/866): the backend summary computes failed-task counts, but browser Pulse drops failed-task identities before rendering, leaving users with a failure signal but no task-level action. Its pending total and priority buckets also include non-completed failed tasks, so the visible pending/empty-state wording can conflict with the failure count; a focused failed-task section should expose affected tasks and keep summary copy consistent without mutating task state.
- Responsive card pages must keep roots/grids/cards/badges shrink-safe. Long badge values truncate within rows.
- Workers settings tables are intentionally non-shrinking within viewport-bound `#main-content`, with horizontal overflow contained inside the wrapper.
- Schedule UI should distinguish disabled schedules; dynamic loop wakeups remain visually distinct. The Schedules page uses the bounded app-shell `#main-content` as its outer scrollport: the page root, timeline, `min-w-[800px]` wrapper, grid body, and hour rows form a nested flex-column chain (`flex-1`/`min-h-0`), so tall viewports fill without `100vh` sizing while narrow viewports preserve timeline scrolling and horizontal width.
- Draggable task-board cards and enabled schedule cards use pointer-driven movement, not native HTML5 `draggable`, to preserve cursor feedback. Do not introduce custom cursor overlays/images/preview clones. Auto-scroll relevant scrollports during drag and refresh drop-zone tracking each scroll frame.
- Destructive task delete flows from schedule-origin detail pages preserve origin with whitelisted `return_to=schedule`, not arbitrary return URLs.
- Global link color token is `--ov-link-color: #7480ff`.
- Sidebar navigation preserves hover-only highlight; avoid DaisyUI visual `.active` class. Sidebar collapsed state and selected project are app-shell UI preferences backed by `app_settings`, with localStorage only as same-origin mirror/early-layout helper.
- Sidebar preference loads must not depend on browser-side GET before rendering. Full-document server render embeds early state; HTMX fragments should not reread UI preferences. Toggle writes update DOM/localStorage immediately and save DB in background.
- If frontend becomes fully separated/static, DB-backed sidebar preference storage can remain authoritative but needs preferences bootstrap/read path to avoid first-paint jump.
- `/models` uses `LLMConfig`/`agent_configs`; `/agents` is plugin-first and has no `color` field.
- `Managed Memory` in UI/tool profiles is scoped memory-file capability, not broad repo read/write access.
- Shared toasts account for native dialog top-layer behavior, reserve right inset, use accessible close buttons, and avoid mobile overflow. Automation-start toasts dedupe by neutral `toast_key` and can click through to Automation Live/detail.
- HTMX toast rendering uses the global `openvibelyToast` event contract through the shared helper path. Worktree success paths should use the canonical helper while preserving unrelated HTMX trigger keys such as `refreshChanges`; task-detail pages must not reintroduce page-local `showToast` DOM rendering, deduplication, status/icon, or auto-dismiss logic.
- For global indicators driven by page-local polling, expose shared layout handler and feed fetched snapshots instead of divergent state paths.
- Destructive deletes for projects, tasks, models, agents, skills, schedules, and channel integrations use shared DaisyUI `<dialog>` confirmation; avoid browser `confirm()` or immediate `hx-confirm` delete wiring.

Models, Channels, cards, and Automation YAML:
- Models initial render uses compact card projections excluding secrets/tokens/client secrets/request JSON/custom auth/full mixture JSON. Edit fetches one authorized full record with request-generation and returned-ID guards.
- Personality settings initial render uses compact card projections and bounded prompt previews; Edit lazy-loads full detail with stale-response guards. Browser saves reject stale, deleted, or fabricated personality keys before mutating settings, and the custom update path rejects missing non-preset keys while allowing intentional built-in overrides; base/default, built-in, existing custom, and runtime/channel `set_personality` paths remain consistent.
- Lazy edit modals for compact cards must not allow Save before detail hydration succeeds. Reused secret modals reset unsaved edits and revealed secrets before reopening.
- Project-scoped settings pages preserve active project through `project_id`; Models paths derive URLs from live selector before fallback.
- Card search pages use shared `data-card-search`; fragment replacement paths call `window.refreshCardSearches` after swapping card containers.
- Standalone Skills cards render compact metadata only; edit bodies/support-file lists are lazily fetched with Save disabled until hydration succeeds. Delete swaps preserve scroll/search/focus without opening DaisyUI dropdowns via focus.
- Channels success paths refresh `#channels-container` in place so search filters reapply; top-level webhook modal state remains redeclarable `var` state because inline script may re-execute.
- Inbound webhook cards keep sensitive fields/prompts/templates/secrets/agent assignments out of card attributes; edit modals lazy-load authorized detail and only open webhook modal form attaches `agent_ids`.
- Independent HTMX child controls inside Channels modals return modal-scoped fragments and should not emit broad refresh unless a safe sibling/card refresh exists.
- Automation YAML editor and Live YAML panel use line-number gutters, highlighting, indentation guides/rails, horizontal scrolling, and no word wrap. Fold controls are disabled. Tab inserts two spaces; Shift+Tab removes one indent level.
- YAML highlight rendering is shared between editable and read-only panels. Live/read-only panels render line numbers client-side into block elements and use read-only textarea for selection.
- Automation YAML editor/read-only preview font and syntax styling follow exact selected theme, including code surface colors, caret, tokens, diagnostics, gutters, dots, and rails.
