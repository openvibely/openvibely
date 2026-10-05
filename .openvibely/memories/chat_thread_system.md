---
name: chat_thread_system
type: project
created: 2026-05-09
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Chat and Task-Thread Behavior
---

Interactive Chat bypasses worker capacity. Task-thread follow-ups are real executions and respect global, project, and model capacity.

- Normal Send during an active turn queues the next input; explicit steering applies only at OpenVibely-owned provider/model-step boundaries; Stop cancels the current execution. Durable `thread_inputs` represent queued/steering input and `executions` represent model runs.
- Promotion preserves FIFO and claims pending input while creating the next execution/task. Task-thread follow-ups resolve current model assignment at promotion; queued Chat inputs retain per-input model selection. Follow-up execution remains queued until worker admission.
- Attachments belong to queued/steering rows, support repeated drops with unique names, and enforce three files per message across drops. Text enters prompt context, images become model attachments, and large text remains attached without inlining. Late steering attachments are requeued as a normal next message. Persistence is atomic; metadata cleanup must not delete bytes still referenced elsewhere.
- HTTP acceptance stays lightweight; provider context setup happens after durable acceptance. Setup failure performs terminal cleanup and may promote queued input. History renders executions rather than an unrecorded task prompt. Provider history comes from persisted sent prompts/cleaned outputs with bounded prior turns and context-boundary handling.
- Follow-up admission is serialized with scheduler/worker claims and Automation reservations. Startup/update-drain recovery is bounded, FIFO, and exactly-once. Browser/API/channel Chat and task-thread surfaces share behavior while preserving distinct projections and ownership checks.
- Shared Chat composer contract: Enter sends/queues, Ctrl/Meta+Enter steers while active and sends once while idle, Shift+Enter inserts a newline. Accepted sends clear text/attachments only after acceptance; a lost steer conflict retries once as normal send. Server OOB state and execution IDs are authoritative. Reject normalized-empty messages before lookup/model/attachment/queue side effects.
- Task Detail keeps the thread as its workspace and loads full execution content lazily for the exact execution under task-plus-project ownership checks. Non-poll previews are bounded and UTF-8-safe; the full `tasks.prompt` must not be reloaded for a non-poll thread window. Keep chronological history page-bounded.
- `/chat` is global/project Orchestrate; Task Detail Thread is task-specific. Plan is read-only; Orchestrate mutations require authorized runtime tools. Providers without tools receive no bracket-marker fallback; legacy markers are inert prose.
- Web Orchestrate Chat alone supports provider-agnostic `request_user_input` (1-3 questions, 2-3 options), project-scoped SSE answers, one-time in-memory responses, and a ten-minute TTL. It must precede ambiguous writes. Requests are not durable across restart, so reload/recovery UX remains a gap.
- Runtime actions resolve exact IDs or unambiguous names, enforce current-project ownership, validate before side effects, and preserve protected records. Shared create-task/swarm-task input normalization, compact model loading, validation, and summaries apply across web/API/channels. `list_tasks` is bounded, current-project, read-only, and excludes Chat category.
- Goals are durable `task_goals`; Plan handoff uses completed responses containing `<proposed_plan>`, and continuation uses queued task-thread follow-ups. There is no direct peer-to-peer task chat. Manual follow-up may reactivate an achieved goal; system continuation does not. Persisted cancellation wins over provider output, pauses active goals, preserves terminal status/history, and must perform capacity cleanup.
- Scheduling due selection excludes disabled rows, clears one-time dates, advances recurring rows, and clamps monthly recurrence to its anchor. A one-time schedule attached during new-task creation can be skipped after the immediate first execution completes; issue #1377 tracks this timing case, distinct from #390 for adding a schedule to an already-completed task. Schedule action logic is shared while operation-specific semantics remain distinct. Calendar still lacks some local pause/resume/run-now and historical context affordances.
- Swarm parents own child templates and guarded assignments; coordination uses durable state/runtime tools. Auto-merge triggers are independent and default off. Shared isolation inheritance and richer parent context on child detail remain gaps.

SSE, composer rendering, scrolling, and navigation details are in `realtime_and_frontend_patterns.md`; capacity and schedule storage boundaries are in `openvibely_architecture.md`.
