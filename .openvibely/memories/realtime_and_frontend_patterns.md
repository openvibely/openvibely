---
name: realtime_and_frontend_patterns
type: project
created: 2026-05-09
updated: 2026-08-10
source: update_memory
source_id: bc683d35728f3c6cf834e99bb84fb839:bbe64855f04606b4
confidence: high
title: Realtime and Frontend Patterns
---

OpenVibely uses server-rendered HTMX/templ UI with shared SSE-style live updates. The durable UI contract is: SSE announces that something changed, while HTMX/server-rendered templ fragments remain the authoritative state.

Realtime and diff contracts:
- The shared `/events/live` stream multiplexes task, chat, mixture-progress, and file-change invalidation events. Sidebar-managed client code dispatches browser `CustomEvent`s such as `sse-task-event`, `sse-chat-live-event`, `sse-file-change-event`, and `sse-live-connected`.
- `window._tabVisibility` owns broad realtime visibility behavior and pauses polling while hidden.
- Per-execution `/events/chat/:exec_id` streams are the token-style output path. SQLite remains the durable transcript and reconnect/recovery source of truth, but hot-path deltas flow through the injected `events.ExecutionStreamHub` rather than SQLite polling.
- `ExecutionStreamHub` is keyed by exact execution ID, supports multiple subscribers, enforces subscriber limits, drops slow-subscriber deltas nonblockingly, closes on terminal events, and must be dependency-injected rather than global.
- `llmstream.Writer.Write` publishes live deltas with cumulative UTF-8 byte offsets after releasing its mutex. It must not publish seeded existing output or `WriteText` content, and it must not synchronously flush SQLite on the hot token path.
- Periodic/final stream persistence must serialize snapshot plus SQLite update so an older periodic snapshot cannot overwrite a newer final flush.
- Per-execution SSE accepts optional untrusted `offset` in UTF-8 bytes. The handler clamps to UTF-8 boundaries, subscribes before DB catch-up, skips duplicate/partial overlaps, and uses targeted DB catch-up for dropped deltas or terminal fallback.
- Fresh, resume, and live-created Chat/task-thread streams track offsets from raw UTF-8 byte lengths, not rendered DOM text, normalized display content, message counts, or scroll/window state.
- Terminal execution-stream events should publish only after durable terminal writes. `completed` and `cancelled` map to SSE `done`; failures map to SSE `error`. Terminal handlers must replay missing durable output before terminalizing subscribers.
- Real-time file changes are invalidation/snapshot signals. `diff_snapshot` events are metadata-only; full diff payloads stay in DB/routes. Browser code refreshes authoritative Changes fragments rather than appending diff DOM directly.
- Task detail lazily loads Changes content unless `tab=changes` is active; direct `?tab=changes` renders must match lazy route behavior.
- Diff viewer uses GitHub-style load envelopes and oversized-file placeholders, deletion summaries, constrained rename/copy headers, and viewport-safe overflow boundaries. Worktree diff semantics live in `worktree_and_lineage.md`.

Chat and task-thread rendering:
- Chat and task-thread streaming batch DOM rendering and force a final flush on completion.
- Live Chat completion reconciliation targets the inner assistant stream node `streaming-message-<exec_id>`, not the outer turn wrapper, so completed output stays inside the assistant bubble.
- Task-thread live execution starts append authoritative server-rendered execution fragments. Page-level task-detail listeners must not force-reload `#thread-content` on start/input-applied events because that can close or replace the fresh per-exec stream.
- Task-thread terminal completion fallback must reconcile assistant output plus terminal/composer state together. A completion/toast/status path must not leave loading dots as the visible assistant bubble.
- Streaming bubble scripts must target explicit thinking/loading IDs such as `streaming-thinking-<exec_id>` rather than relying on sibling position, because mixture-progress slots may appear between elements.
- Queued and steering-pending Chat/task-thread messages render as compact composer/input-box rows rather than transcript bubbles. Queued rows with `attachment_session_id` show `Attachments queued`; steering rows show `Attachments included`. Both indicators must appear consistently across HTMX fragments, pending-input refresh/reconnect, and live-created rows.
- Chat live `chat_new_message` handling must let queued attachment events reach the queued-row path; the attachment transcript-refresh branch applies only to non-queued messages so live queued rows retain the attachment indicator.
- Queued-to-steering conversion must publish live steering events (`chat_turn_steered` for Chat and `task_thread_input_steered` for task threads) so other tabs replace stale queued rows with steering-pending rows and do not leave functional-looking `Steer` actions on already-steered inputs. Same-tab conversions guard the local HTMX swap target from the live steering event until the conversion response settles.
- Promoted queued task-thread runs are recovered through live events that remove stale pending rows, append promoted execution fragments, and attach streams. Either `task_thread_execution_started` or `task_thread_input_applied` should recover UI if the other is missed.
- Pending containers, queued rows, steering rows, queued-message OOB appends, and live-event DOM mutations are task-scoped with task-qualified selectors so one task cannot mutate another task's thread.
- Initial Task Run publishes `task_thread_execution_started` after the execution row exists. Live reconnect/start/status refreshes must discard stale visible-task responses and must not replace a composer with active pending uploads.
- Chat/task-thread terminal reconciliation uses persisted authoritative execution status/output regardless of whether shared completion or per-execution terminal arrives first. Failed/cancelled states update terminal DOM and transcript revision; missing-status compatibility preserves an already-known terminal state.
- Server-rendered task-thread terminal states must stay aligned with live states, including cancelled runs. Known gap `openvibely/openvibely#226`: hard refresh after cancellation does not show the same explicit terminal marker as completed/failed threads.
- Long Chat and Task Thread histories remain complete in DB but are server-windowed in UI with scroll-top pagination; initial and older windows default to 30 rows and cap requested limit at 100. Browser-memory protection requires removing old execution DOM nodes, not hiding them.
- Active streaming uses smart autoscroll: pinned viewers follow growth, while upward user movement is read intent.
- Chat/task-thread tool result output remains full-fidelity. Canonical `tool_result` markers are not display-truncated; rendering uses responsive scroll containers that avoid trapping page/thread scrolling.

Shared composer and steering UI:
- Chat and task-thread composers share `web/templates/components/chat_shared.templ` and one `ChatInputForm` submission contract. Plain Enter sends when idle and queues when active; Shift+Enter inserts a newline; Meta/Ctrl+Enter steers when active and safely falls back to one normal send when idle.
- The active primary send button becomes the Stop control with the same primary styling and a square stop icon. Sends/stops that do not replace the full form should return an OOB primary-action fragment so the button flips immediately while preserving drafts, attachments, and speech controls.
- Shared submit logic must resolve/delegate to the current submit button after OOB swaps instead of caching the original node.
- Platform hints are concise: Apple shows `⏎ sends or queues · ⌘+⏎ steers`; other platforms show `Enter sends or queues · Ctrl+Enter steers`. Apple detection prefers `navigator.userAgentData.platform` with fallback to `navigator.platform`/UA.
- Modifier-click on Send/Stop can steer through guarded endpoints when an active turn ID is resolvable. Steering responses target `#pending-thread-inputs`, not the transcript.
- Accepted steer submissions, accepted active-turn queue submissions, and ordinary idle sends clear the submitted composer textarea after acceptance; submitted attachment sessions clear only when the visible session still matches the submitted one, and explicit user clearing remains honored.
- Drafts and pending attachment sessions must survive failed/non-accepted sends, stale/unavailable steering, OOB swaps, page visibility changes, and live reconnects. Detailed queueing/steering semantics live in `chat_thread_system.md`.
- Known race `openvibely/openvibely#48`: overlapping composer action-only GET refreshes lack stale-response guards, so an older running-state response can overwrite a newer terminal response and regress Send to Stop.

Transcript parsing and rendering safety:
- Final Chat/task-thread Markdown rendering uses the shared escaped/unmatched/multiline code-range parser. Raw `<` outside protected code is escaped before Marked, and Marked output is sanitized to remove dangerous elements, event/style/srcdoc/srcset attributes, and unsafe URLs.
- Missing, incomplete, configuration-failing, or parse-failing Marked returns escaped plain-text markup while preserving Markdown source provenance.
- Go, browser, and generated transcript parsing treat LF, CRLF, and bare CR as equivalent CommonMark line endings without normalizing source bytes. Coded aliases, controls, task metadata, and summaries remain visible and inert; real controls after valid matching fence closers behave normally.

HTMX navigation, refresh, and scroll contracts:
- Browser titles use server-rendered `PageTitle` markers as the single source of truth for full documents and HTMX navigation. Markers stay nested inside the destination root so `outerHTML` swaps preserve a single-root contract.
- Programmatic navigation must use `window.openVibelyNavigate` rather than manual `history.pushState()` plus `htmx.ajax()`, so HTMX history snapshots and Back/Forward restoration remain coherent.
- HTMX history cache-miss requests are treated as full-document requests with the application history element and authoritative title; ordinary HTMX requests remain fragments.
- Reconnect/refocus should be revision-gated: identical revisions cancel swaps before scroll/draft capture, EventSource cleanup, or DOM mutation. Active per-execution stream catch-up should not be disrupted by broad transcript replacement.
- Completed execution pairs should use stable IDs/HTMX preservation so changed authoritative transcript morphs can retain unchanged completed bubble DOM and expanded tool-output state.
- Known review navigation gaps: `openvibely/openvibely#365` notes that dedicated execution detail pages exist but Task Thread and Reflection review surfaces do not expose visible links to them, so users cannot easily inspect per-run metadata from normal review flows; `#380` proposes making execution-detail pages clearer audit/review landing pages, especially when users arrive from history or Automation resource links.
- Forced task-thread fragment refreshes must close tracked per-exec EventSources before replacement, but revision no-op checks must run before cleanup.
- Older-history pagination captures scroll intent revision and rechecks after hydration. Stale anchor restoration is cancelled if a send or newer user scroll changed intent.
- Known duplication gaps: task-thread scroll-state helpers (`#38`), terminal UI/composer reconciliation (`#55`), live-event queued row construction (`#59`), task-thread post-swap hydration (`#65`), and schedule repeat-interval controllers (`#47`).
- Task detail HTMX refresh assembly is centralized as of PR `#403`: `GetTask` fragments, task edit saves, chain-config saves, and schedule create/update/toggle refreshes share one task-detail content loader/renderer with canonical chronological execution ordering; full-page `GetTask` still wraps `TaskDetailPage` and route-specific selected tabs are preserved.
- Task-board mutation/sort HTMX refresh assembly is centralized in a private handler helper as of PR `#399`: preserve route-specific validation, mutations, logging, non-HTMX redirects, and `CancelTask?composer_stop=1`, while reusing shared sort-cookie handling, board reload, model-config loading, kanban rendering, and reload-error handling for board refreshes.

Responsive and shared UI contracts:
- Chat/thread/message panes, bubbles, composers, and tool/code output must not create whole-page horizontal scrolling on mobile. Use `min-w-0`, `max-w-full`, wrapping, and inner overflow boundaries.
- Avoid hard `overflow-x-hidden` on immediate chat/thread roots, message scrollports, or rounded composer shells when it clips shadows or bevels; use gutters and inner containment.
- Chat/task-thread bubbles and composer align visually with Agents/Models card widths while preserving in-pane scrolling and unclipped shadows/tails.
- Composer bottom controls must remain contained on mobile. Selectors are shrinkable/truncated; action clusters do not shrink.
- Chat/task-thread model and mode selectors intentionally use custom portal-style dropdowns appended to `document.body`, with hidden inputs carrying values.
- Preserve native macOS overlay scrollbar behavior for chat/task-thread panes; Firefox `scrollbar-width`/`scrollbar-color` is acceptable.
- Task detail Details tab order is Prompt, Goal, Git Worktree. The seven-tab row remains horizontally scrollable/no-wrap on narrow screens.
- Completed task Thread views include a server-rendered next-step CTA after terminal status: Changes when stored diff exists, merge guidance only when merge state indicates it, and no-diff/no-output guidance otherwise.
- Task Changes/diff surfaces must stay viewport-contained on 320px-class screens; long filenames, branch labels, and Changes dropdowns are recurring containment risks. Changes Actions dropdowns need high stacking context above sticky diff headers.
- Tasks page uses server-rendered kanban components. Responsive contract: one column on phones, two around tablets, three on desktop, no phantom fourth column, mobile-safe wrapping, independent mobile dropzone scrolling, compact desktop density, and `+ Add Task` colocated with the title.
- Known board review-state gap `openvibely/openvibely#227`: task cards do not surface worktree/merge state, so reviewers must open Task Detail to triage review state.
- Known task-card PR visibility gap `openvibely/openvibely#374`: task cards do not surface linked pull request URL/status even though Task Changes can render an existing `TaskPullRequest` as a `View PR` action, so users scanning the board cannot tell which tasks already have external review artifacts without opening each task.
- Active kanban queued/pending dropzones render only real active pending/queued/blocked work; terminal failed/cancelled rows must not appear as queued work.
- Tasks page date sorting defaults newest-first: Backlog by creation time, Completed by `completed_at` with `updated_at` fallback. Board/drag category edits update `completed_at` through `TaskService.UpdateCategory`; Task Detail edit-form saves are metadata-only and must not activate completed tasks or rerun stored prompts.
- Responsive card pages such as Models, Agents, Alerts, Channels, and Personality must keep roots/grids/cards/badges shrink-safe. Long badge values truncate within rows.
- Schedule UI should distinguish disabled schedules; dynamic loop wakeups should remain visually distinct from fixed schedules.
- Destructive task delete flows from schedule-origin detail pages preserve origin with a whitelisted `return_to=schedule`, not arbitrary return URLs.
- Global link color token is `--ov-link-color: #7480ff`.
- Left sidebar navigation preserves hover-only highlight unless intentionally redesigned. Mobile sidebar uses DaisyUI drawer checkbox `#sidebar-toggle`; accepted nav requests close the drawer only after HTMX sends/accepts.
- `/models` uses `LLMConfig`/`agent_configs`; `/agents` is plugin-first and has no `color` field.
- `Managed Memory` in UI/tool profiles is a scoped memory-file capability, not broad repo read/write access.
- Shared toasts account for native dialog top-layer behavior, reserve right-side inset, use accessible close buttons, and avoid mobile overflow. Native DaisyUI dialogs become full-screen on phone-sized viewports using dimension-based shared CSS.
- Destructive deletes for projects, tasks, models, agents, skills, schedules, and channel integrations use the shared DaisyUI `<dialog>` confirmation pattern; avoid browser `confirm()` or immediate `hx-confirm` delete wiring.

Models, Channels, and card-search UI:
- Models initial render uses compact `agent_configs` card projections excluding secrets/tokens/client secrets/request JSON/custom auth/full mixture JSON. Edit fetches one authorized full record; modal hydration uses request-generation and returned-ID guards.
- Personality settings initial render uses compact custom-personality card projections and bounded prompt previews; list card/search attributes must not contain full custom prompt bodies. Opening Edit lazy-loads one full custom personality or preset/override detail through the JSON detail endpoint, with request-generation/stale-response guards while preserving card search and HTMX section refresh behavior.
- Lazy edit modals for compact cards must not allow Save to submit placeholder or blank edit-only fields before detail hydration succeeds. Use loading/disabled/error states with stale-response guards so failed or slow detail fetches cannot overwrite persisted secrets, prompts, templates, instructions, or assignments with defaults.
- Reused secret modals must reset unsaved edits and revealed secrets before reopening across Models, GitHub, Slack, Telegram, Discord, Email, and inbound webhooks.
- Project-scoped settings pages preserve active project through `project_id`; Models create/edit, set-default, delete, delete-with-reassign, and OAuth paths derive URLs from the live project selector before fallback to URL query. OAuth pending state persists ProjectID.
- Models create/edit reuses one modal over `agent_configs`; edit must carry `model_config_id` and update in place rather than inserting duplicates.
- Card search pages use the shared `data-card-search` helper. Direct fragment replacement paths, including Skills and Agents mutations, must call `window.refreshCardSearches` after swapping card containers.
- Skills card refreshes keep server-rendered `#skills-container` fragments authoritative while preserving the user's reading position with stable `data-skill-scroll-anchor` cards. Delete-triggered swaps prioritize surviving neighbors of the deleted card, reapply card search before measuring anchors, and avoid HTMX/browser focus scroll jumps from the shared DaisyUI dialog path.
- Channels create/update/delete/rotate-secret success paths refresh `#channels-container` in place so search filters reapply. Because the Channels inline script may re-execute, top-level webhook modal state must remain redeclarable `var` state.
- Inbound webhook Settings compact cards keep sensitive/detail-only fields, prompt/templates, secrets, and agent assignments out of card attributes; edit modals lazy-load one authorized full webhook detail and must disable Save until detail hydration succeeds. Webhook HTMX request hooks must not assume non-existent selector helpers or elements; only the open webhook modal form should attach `agent_ids` from current modal state, so create/edit/delete/test/rotate-secret card actions remain independent.
- Independent HTMX child controls inside Channels modals should return modal-scoped fragments and should not emit broad `channels-refresh` unless a safe sibling/card refresh path exists.

Automation YAML editor current contracts:
- The Automation YAML editor and read-only Live YAML panel use line-number gutters, syntax highlighting, indentation guides/rails, horizontal scrolling, and no word wrap.
- Fold/collapse controls are currently disabled and hidden. Do not reintroduce fold buttons or word wrap without deliberate product approval and real-browser regression coverage for hidden-content preservation, caret geometry, wheel/scroll behavior, and Live read-only parity.
- Tab inserts two spaces; Shift+Tab removes one two-space indent level from the current line when possible. Active indentation rail highlighting should be theme-aware neutral gray and should not change rail width or flash on typing.
- YAML highlight rendering should be shared between editable and read-only panels to avoid indentation drift. Live/read-only panels render line numbers client-side into block elements and use a readonly, non-submittable textarea for text selection.
