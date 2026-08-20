---
name: chat_thread_system
type: project
created: 2026-05-09
updated: 2026-08-19
source: after_complete
source_id: e66c64236d6c3b8344566d00dbb75c41:3e665d862de2f852
confidence: high
title: Chat and Task-Thread Behavior
---

Interactive Chat intentionally bypasses worker capacity limits. Task-thread follow-ups use `processStreamingResponse` with `IsTaskFollowup=true` and respect global, project, and model capacity limits.

Queueing, steering, and attachments:
- Queueing, steering, and Stop are distinct behaviors. Normal Send during an active Chat/task-thread run queues next input; explicit steering applies only at OpenVibely-owned provider-call/model-step boundaries; Stop cancels current execution.
- Queueing and steering use durable `thread_inputs`. `executions` represent real model runs, not parked composer state.
- Queued input promotion claims pending input while creating the next execution/task and preserves FIFO order.
- Promoted queued task-thread inputs resolve current task model assignment at promotion time, so later model changes or failed-run edits take effect. Interactive Chat queued inputs keep per-input model selection.
- Queued and steering rows with attachments store `attachment_session_id` on `thread_inputs`. Chat queued rows have no `task_id` and identify active run through `run_execution_id`; task-thread rows use `task_id`.
- Steering attachments are one-shot: the pending session is included in the next outer-model provider step when claimed and committed as `ChatAttachment` after successful execution. Text attachments enter prompt context, image attachments become model attachments, large text files stay attached but not inlined, and text-only provider-loop steering skips attachment rows until an outer step can process them.
- Attachment-bearing steering discovered after the provider call returns or during final/deferred completion is requeued as the next normal queued message; it must not be committed to the completed execution/user bubble.
- Chat and task-thread composers share pending attachment behavior: repeated drops append, duplicate browser filenames are preserved with unique pending filenames, and the three-files-per-message limit applies across repeated drops.
- Failed Chat/task-thread steering preserves drafts and pending attachment sessions for retry; publication/queued-input failures retire/delete unowned sessions through rollback.
- Retryable model stream/transport failures during task-thread follow-ups can recover through shared logical-turn retries. Exhausted or non-retryable failures are terminal: prepared steering is not requeued, pending inputs tied to the failed execution are cancelled, live cancellation events remove stale rows, and queued-turn promotion is suppressed.
- Attachment publication copies to temp destination, applies permissions, flushes, atomically renames, syncs the destination directory, then persists metadata. Batch rollback deletes metadata before files and retains files if metadata deletion fails.
- Browser/API attachment prompt assembly shares a helper from finalized file facts. Callers own file movement, metadata persistence, rollback, pending-session cleanup, and surface-specific responses.
- Known attachment inspection gap `#427` and related suggestions: runtime actions can edit task attachment IDs but cannot list attached-file metadata already shown on Task Detail. Scope should be metadata-only listing, not download/content access.
- Attachment-bearing runtime task requests resolve effective category before creation. Effective Active requests persist as Backlog until conversion succeeds, then activate by request index; conversion failure leaves them Backlog.
- Upload/session lifecycle is rooted in configured uploads directory across local, env-overridden, Docker/hosted, and desktop storage. Broken metadata can be reconciled; missing file contents cannot be recovered.
- Non-default project deletion routes project-owned tasks through `TaskService` cleanup before deleting the project row, retiring owned pending attachment sessions, removing pending directories after commit, preserving late-upload 409 tombstones, and invoking worker cancellation hooks.

Task-thread admission and lifecycle:
- Task-thread sends made while execution is running, including before first assistant output or during initial lifecycle hooks, queue as next-turn input.
- Direct task-thread follow-up admission is serialized with scheduler/worker claims and Automation reservations in the database. It atomically reserves a direct execution or appends durable FIFO input.
- Follow-up executions are persisted as `queued` until a worker slot is admitted; only admitted work is promoted to `running`. UI/tool paths requesting running work must route through worker admission.
- `TaskThreadSend`, review submission, and swarm-child follow-up acceptance should share admission logic while retaining surface-specific parsing, attachments, HTMX/JSON responses, and streaming setup.
- Startup recovery reclaims interrupted follow-ups and drains recoverable pending inputs through stable keyset pages with per-task FIFO and exactly-once promotion.
- `after_complete` hooks receive only the latest normalized user input and assistant response for that input; prior provider history may influence the answer but is not copied into lifecycle transcript.
- Thread history for future provider calls is rebuilt from `executions.PromptSent` and cleaned `executions.Output` as plain turns, not provider-native tool-use messages.
- Task-thread follow-ups use chronological execution history, not reinjection of original task prompt. Replay history is bounded to preserve up to 20 prior turns after filtering current execution.
- Task-thread HTTP send/acceptance stays lightweight; full transcript/model-context setup runs in background after durable acceptance. Deferred setup failures should still run terminal cleanup that can promote queued input.
- Task-thread transcript/pagination renders `executions` history; `tasks.prompt` may not appear as the oldest paged item unless also recorded as execution prompt.
- Lifecycle-agent activity belongs in the Lifecycle tab rather than main Thread. Task-thread follow-ups run normal task lifecycle routing before model call.

Composer shortcuts and steering races:
- Shared composer hint is platform-aware: Apple `⏎ sends or queues · ⌘+⏎ steers`, other platforms `Enter sends or queues · Ctrl+Enter steers`.
- Plain Enter sends/queues; Shift+Enter inserts newline; Meta/Ctrl+Enter steers when active and falls back to one normal send when idle. Meta/Ctrl-click on primary button steers when an active turn is resolvable.
- Accepted steer submissions, active-turn queue submissions, and idle sends clear submitted textarea after acceptance; submitted attachment session clears only when the visible session still matches, preserving explicit user clearing.
- `activeTurnID()` resolves from authoritative running/queued transcript rows first, then composer guard. Guard is cleared when idle so stale turn IDs are not reused.
- OOB primary-action fragments carry the new execution turn ID immediately after send so immediate steering can target it before transcript rows appear.
- If steering fires while normal send is in flight and no turn ID exists yet, draft/session is captured as deferred steering and flushed once execution row appears. Failed/non-accepted sends discard deferred steer.
- If explicit steering loses a race because active model completed and endpoint returns `409`, composer retries the same message/session once through normal send/queue.
- `submitComposer` increments in-flight counters from `htmx:beforeRequest`, not call time, so native HTML validation cannot strand counters.
- This area requires direct browser reproduction for idle, active, immediately-post-send, mid-request, validation-blocked, keyboard, and modifier-click states when changed.

Routes, modes, and runtime actions:
- `/chat` is the global/project orchestrator; Task Detail Thread is task-specific.
- `/chat` supports Orchestrate and Plan. Plan is read-only; Orchestrate mutations require authorized runtime tools/actions.
- Canonical Chat capabilities live in `internal/chatcontrol/registry.go`.
- Orchestrate-only `set_default_model` spans web/API Chat and channels, resolves exact model ID or unambiguous case-insensitive model name, rejects blank/missing/ambiguous references, and delegates to `LLMConfigRepo.Update`. Plan mode must not advertise or execute it.
- `list_channels` is a read-only Chat action available across normal Chat surfaces in Plan and Orchestrate modes. It returns a prompt-safe current-project integration summary for GitHub, Slack, Telegram, Discord, Email, inbound webhooks, and outbound message target counts/status/policy without exposing tokens, passwords, private keys, webhook secrets, or raw target credentials.
- Resolved `#684`: Slack/Discord/Telegram channel-provided `list_channels` handlers take precedence over generic fallback but now receive `EmailStatus`, `EmailAuthRepo`, `WebhookRepo`, and outbound target stores, so normal Chat surfaces summarize GitHub App status plus Email/Webhooks/outbound target counts consistently and prompt-safely.
- Known Chat inspection gaps: no prompt-safe detail view for a specific agent's capabilities/configuration (`#595`), no project skill catalog discovery action (`#621`), no live worker-capacity pressure action (`#604`), no Insights query action (`#624`), no Pulse/upcoming agenda summary action (`#677`), and no read-only model usage/cost analytics action (`#703`).
- Orchestrate-only `create_project` creates GitHub-backed projects from `name` and `repo_url`. It rejects local path/create-directory behavior, uses the normal project service/repository and existing GitHub clone path, updates normalized repo URL/path state, rolls back clearly on clone failure, and returns prompt-safe JSON containing project ID/name, normalized repository URL, repo-path presence, and switch-after-create status. Open `#699` follow-up: Slack, Telegram, and Discord advertise `create_project` but must pass project/GitHub/memory/agent-library dependencies into their channel project handlers; otherwise they return `project service is not configured`. Web/API Chat and Email show the intended wiring pattern.
- `list_tasks` is bounded read-only current-project task discovery across web/API Chat, supported channels, task-thread follow-ups, and generic initial/scheduled runs. It supports title/category/status filters, deterministic pagination, excludes `category=chat`, and returns compact summaries.
- Runtime `edit_task` can set/clear primary Agent assignment through Agent-definition ID/name fields while preserving provider model semantics on `agent_id`/`agent_config_id`.
- Known task-discovery gaps include merge state, assigned Agent, tags, goal badges/state, triage badges, bounded diffs/review comments/lifecycle evidence, attachments, and related Task Detail metadata.
- Saved Automation state inspection is available through read-only Chat actions `list_automations` and `get_automation` backed by compact Automation card shape.
- Orchestrate-only Automation lifecycle tools `run_automation_now`, `pause_automation`, and `resume_automation` must not be advertised/executed in Plan mode and should delegate to `AutomationLifecycleService`.
- `list_schedules` is bounded read-only current-project schedule discovery across supported runtimes; it supports task ID/title/enabled filters, deterministic pagination, and returns IDs accepted by modify/delete actions.
- Legacy bracket markers such as `[CREATE_TASK]`, `[EDIT_TASK]`, `[EXECUTE_TASKS]`, `[SEND_TO_TASK]`, and `[SCHEDULE_TASK]` are inert model prose. Runtime-tool handlers decode typed JSON and invoke services; tool-incapable providers receive no textual fallback.
- Status-marker parsing recognizes only a complete canonical final standalone control: `[STATUS: SUCCESS]`, `[STATUS: FAILED | reason]`, or `[STATUS: NEEDS_FOLLOWUP | reason]`. Malformed/non-final/fenced/inline examples remain visible/inert.
- Chat output cleaning preserves control-looking examples inside Markdown code while stripping/normalizing real provider controls outside code.
- Open/replace PR runtime actions share target-resolution logic for task selectors, project ownership/loading, and Automation repository validation.
- Known task-thread runtime gaps: PR tools reject omitted current task (`#536`), `execute_tasks` mishandles `task_id="current"` or omitted current task (`#423`/duplicates), channel task-thread runtimes do not resolve current task (`#326`), goal tools reject omitted current task (`#338`), and scheduled `list_capabilities` can advertise tools unavailable to executor (`#341`).

Task goals, continuations, and cancellation:
- Plan handoff is driven by completed Plan-mode responses containing `<proposed_plan>`.
- Task goals are durable `task_goals` records managed by `TaskGoalService`; Chat orchestration supports explicit and implicit goal creation.
- Goal continuation uses normal task-thread follow-ups through `thread_inputs`; it does not start work inline.
- Tasks do not support direct peer-to-peer chat. Coordination is through app state and control-plane tools such as `send_to_task`, `view_task_thread`, child tasks, schedules, dynamic wakeups, goals, and `thread_inputs`.
- `view_task_thread` and other task-identity tools are strictly project-scoped. Cross-project task IDs return an explicit different-project error; there is no cross-project switch/view capability.
- A durable “inbox task” pattern is a canonical task thread, not an automatic mailbox.
- Goal status writes use stale `goal_id` plus active-status guards. Goal tools can be granted beyond Goal Agent, but ungranted/default agents do not see or execute them.
- Manual/user follow-ups on achieved goals reactivate the same goal before prompt context/lifecycle evaluation; Goal Agent/system continuations do not reopen achieved goals.
- Manual Stop/cancel implicitly pauses active goal with reason `stopped by user`. Starting again resumes only goals paused with that reason; explicit/manual pauses require explicit resume.
- Global Chat Stop uses active Chat execution's backing task and task cancellation semantics while preserving `category=chat`.
- Task cancellation and Chat stop share handler logic for running-execution cancellation, best-effort repo-error handling, cancellation logging, and terminal SSE publication.
- Open cancellation bug `#477`: stale/duplicate cancel requests can rewrite terminal execution history as cancelled, nonexistent execution IDs can return success, and active cancellation can race with completion/failure. Fix should preserve terminal statuses and require cancellable executions.
- `processStreamingResponse` must re-check persisted task/execution cancellation after registering runtime cancellation and immediately before provider dispatch.
- Provider output after durable user cancellation must not finalize a run as success; persisted cancellation wins.
- Task Thread composer Stop cancels active run/capacity wait without bulk-cancelling queued follow-ups. Broader task cancel controls may cancel pending inputs.
- Known task-control gap `#235`: Task Details replaces Run Now with disabled state for running/queued tasks instead of exposing Stop/Cancel.
- Normal active terminal failed/cancelled rows normalize to Backlog; Chat preserves `category=chat`.
- Direct cancel and Active-to-Backlog/Completed moves cancel queued/running executions, pause active goal, and preserve requested target category. `TaskService.UpdateCategory` must use pre-update category/status for stop semantics.
- Lifecycle-origin `send_to_task` must re-check goal state, execution freshness, and cancellation state before queueing continuations. Timeout/deadline failures are ordinary failures for lifecycle purposes; only explicit Stop/Cancel suppresses optional after-complete work.

Task execution, schedules, capacity, and swarms:
- Task cards should surface source/provenance badges for channel-origin and Automation-created tasks; gap `#306`.
- Browser task create/edit and runtime `create_task` share normalized title/prompt invariant: trim whitespace, reject blank normalized values, and detect duplicate titles after normalization.
- Task edit priority is a strict `1..4` invariant for browser Task Detail edits and runtime `edit_task`. Browser edits reject missing, malformed, or out-of-range priority with controlled validation and preserve the existing row; runtime edits distinguish omitted priority from explicitly supplied invalid `0` and reject out-of-range values without persistence. Legacy persisted out-of-range rows remain display-tolerant.
- Active tasks auto-submit on creation or when explicitly moved to Active through execution/category activation paths.
- Task Detail `PUT /tasks/:id` saves are metadata-only for title, prompt/model/priority/category fields, and goals; they must not create executions, enqueue thread inputs, reset terminal status, or submit stored prompt.
- Scheduled tasks run when `next_run <= now`; one-time schedules clear `next_run`, repeating schedules compute next occurrence. Disabled schedules are excluded from due selection.
- Browser/API schedule toggles share a handler-level transition operation. Runtime schedule mutations share `ScheduleActionService` parsing, recurrence mapping, interval validation, persistence, ownership, and linked-task transitions.
- Known schedule gaps include `list_schedules` missing `LastRun`, due one-time occurrence loss on process exit between enqueue/MarkRan (`#88`), drag-to-past one-time reruns (`#270`), Schedule page missing Goal and attachments on new scheduled task (`#342`, `#353`), calendar card cues (`#354`), and current-task context gaps (`#312`).
- Monthly recurrence uses anchor-aware clamping.
- Schedule rows own timing; linked tasks own execution assignment. Primary Agent is `tasks.agent_definition_id`; model config remains task `agent_id`.
- `Clear context on start` is schedule-owned and non-destructive. New schedules default true; existing schedules retain stored value.
- Promoted queued task-thread follow-ups move task back to Active before exposing execution.
- Reactivating a failed task with no queued input retries latest failed follow-up from `PromptSent` and chronological history, not blindly `tasks.prompt`.
- Zero-model guardrails block execution and Chat send surfaces with an Open Models action.
- Worker capacity uses global/project/model counters. Cleanup after slot acquisition is centralized so completion, errors, cancellation, claim failures, and panics release slots and trigger redispatch.
- Known capacity gap `#44`: task-thread follow-ups reserve global/project capacity before model capacity, so model-blocked follow-ups can occupy global slots.
- Capacity waiting for follow-ups is cancellable and not charged against `worker_timeout`; timeout begins after slots are acquired.
- Tasks legitimately run while category is active or scheduled; stale-running recovery must not treat scheduled-category running executions as inactive.
- Direct task-thread startup on completed/inactive swarm children must reactivate task and insert execution atomically.
- Swarm parent follow-ups and dropdown changes guard before touching parent `Task.AgentID`; parent assignment remains child-creation template.
- New swarms persist resolved model for children; legacy nil-agent rows resolve effective model at render time.
- Runtime `create_swarm_task` accepts goal, priority, tag, explicit model/Primary Agent assignment, reviewer/merger flags, and merge target through web/API/channels.
- Direct Chat/API and channel/scheduled `create_swarm_task` runtimes share one canonical input type and service-layer helper for normalization, Agent/reviewer/merger resolution, `CreateSwarmTaskRequest` mapping, and success summaries. Surface-specific behavior remains outside the helper: channel surfaces use the active project and still run Slack/Discord/Telegram origin/context callbacks, while direct non-channel surfaces may use explicit project override.
- Swarm child cancellation/deletion should cancel running child work. Swarm cancellation marks parent/child in-process cancellation intent before runtime callbacks/status persistence, preventing lifecycle continuations during races.
- Known swarm gap `#502`: `shared` isolation is exposed in UI/runtime creation but planner output application rejects inherited `shared` isolation when planner entries omit per-worker isolation.

Resolved incidents worth retaining:
- Steering/queueing attachment incident: pending steered rows with attachments show indicators consistently, same-tab direct steering suppresses duplicate live rows, accepted submissions clear submitted text/session, stale-steer `409` falls back once to normal send/queue, and late attachment-bearing steering preserves usage row.
- Task-thread model-rerun incident: after failed Anthropic/OAuth Opus run, changing task to OpenAI/Codex now affects failed-follow-up retry and queued task-thread promotion.
- `view_task_thread` resolves `task_id="current"` and defaults omitted task/title to current in task-thread follow-ups.
- Runtime/channel schedule creation/modification reject multi-day weekly `days` input instead of silently storing one weekday.
- Task-thread runtime schedule tools resolve current task for omitted or `task_id="current"` in task-thread follow-ups while rejecting current alias outside persisted follow-ups.
- One-time schedules for completed/failed tasks reset terminal non-running targets to pending at creation so due scheduler submits them.
- Automatic resume of goals paused by user Stop uses a conditional update that only resumes when still paused for that reason.
- Completed-task Task Detail edit no longer reruns stored prompt.
- Scheduled work paths clear stale in-process cancellation markers before legitimate scheduled runs.
