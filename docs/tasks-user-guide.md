# Tasks User Guide

Use `/tasks` for Kanban-style task management and execution.

## What This Page Is For

`/tasks` is the operational board for work intake, execution, and completion.

Why this matters:

- Gives a clear queue of what should run now vs later.
- Makes execution state visible (`In Progress`, `Queued`, `Completed`, `Failed`).
- Supports both manual and bulk task operations.

## Board Layout

Columns:

- `Backlog`
- `Active` (split into `In Progress` and `Queued`)
- `Completed`

How to use these columns:

- `Backlog`: planned work not running yet.
- `Active`: work selected for execution.
- `Completed`: finished tasks and historical output.

You can drag cards between categories and active sub-lanes to change where work sits in the flow.

## Create a Task

1. Click `+ Add Task`.
2. Fill:
   - `Title`
   - `Prompt`
   - Optional `Model`
   - Optional `Agent`
   - `Category`, `Priority`, `Tag`
   - Optional attachments
3. Click `Create Task`.

Optional: enable `Auto-merge to target branch on completion` for git worktree flow.

## Swarm Tasks

Enable `Swarm mode` in the task dialog when a job should be split across planner, worker, reviewer, and merger child tasks. OpenVibely creates a swarm parent card plus real child tasks, so each worker keeps its own thread, diff, retry, follow-up, and worktree context.

Workers created by the planner all start in parallel as soon as the plan is applied; there is no dependency ordering between workers, so a worker cannot wait for another worker to finish first. Validation of the combined worker output happens only in the reviewer and merger phases, which run after all required workers complete.

Defaults:

- `Max workers`: `3`, configurable from `1` to `8`.
- `Worker isolation`: `worktree`, so code-writing workers do not share a checkout by default.
- `Reviewer enabled`: on.
- `Merger enabled`: on.

The kanban board shows the swarm parent as one grouped card by default. Child rows inside the card link to their full task pages and diffs. Open the parent task for a swarm overview, or open a child task to inspect or continue that role directly.

Follow-up behavior:

- Parent follow-ups coordinate the whole swarm and mark the result for renewed review/merge.
- Worker follow-ups continue only that worker slice, then require reviewer/merger reruns.
- Reviewer follow-ups rerun review without rerunning workers.
- Merger follow-ups update integration without rerunning workers or reviewer.

## Card Actions

From each task card menu:

- `Run` (when applicable)
- `Cancel` (running tasks)
- `Edit`

You can also delete directly from the card.

When to use:

- `Run`: execute immediately.
- `Cancel`: stop a long/incorrect run.
- `Edit`: adjust prompt/model/priority before re-running.

## Backlog and Completed Bulk Actions

Backlog menu includes:

- Sorting options
- `Execute All` (and priority-specific execute actions)
- `Delete All`

Completed menu includes:

- Sorting options
- `Delete All`

Use bulk actions when you want to process many tasks quickly (for example, execute only urgent backlog tasks first).

## Task Detail Page

Open a task card title to access tabs:

- `Details`: run now, edit fields, delete
- `Thread`: task-specific conversation/execution follow-ups
- `Changes`: git diff and review comments
- `Lifecycle`: hook invocations, skill pills, and recalled memory pills for every hook run
- `Schedules`: add/edit/remove task schedules
- `Chaining`: configure child-task creation flow
- `Attachments`: manage attached files

Why task detail matters:

- It is where you inspect execution output and iterate safely.
- `Thread` and `Changes` are the two most important tabs during active debugging/build work.
- `Lifecycle` shows which skills and memories were involved in each hook invocation.

## Thread Follow-Ups And Steering

Send follow-up messages from the `Thread` tab to continue a task after its initial run. Follow-ups queue when task capacity is full and dispatch when a slot frees.

When a follow-up is queued and the task thread shows a pending input row, a **Steer** button appears on that row. Steering from a task thread redirects the active turn rather than queuing behind it — use this when you want to correct or narrow a follow-up before the worker picks it up. You can also cancel a queued follow-up before it is applied. If the turn has already started, the steer may be rejected and you should send a new follow-up instead.

## Task Goals

A Task Goal is a persistent objective that the built-in Goal Agent evaluates after each execution turn. When a goal is set, the agent automatically queues continuation follow-ups until the objective is achieved, blocked, or cleared — without manual follow-up messages.

Set a goal in the task edit dialog (Goal section). The goal panel on the task detail page shows the objective, current status (`active`, `paused`, `achieved`, `blocked`, `cleared`, `failed`), the agent's last evaluation reason, and when it was last checked. Clear the objective text and save to remove the goal.

In Chat Orchestrate mode, you can create a task and set a goal on it in the same turn using `set_task_goal`.

For the full reference see <a href="https://docs.openvibely.ai/task-goals" target="_blank" rel="noopener noreferrer">Task Goals</a>.

## Diff Review

Open the `Changes` tab on any task detail page to review generated code before merging.

| Surface | Use It For |
|---|---|
| File cards | Inspect each changed file with status, hunks, line numbers, and rename or binary metadata. |
| Inline / split view | Toggle how textual diffs are displayed per file. |
| Review comments | Attach line-level feedback and submit it back to the agent to trigger a revision follow-up. |
| Worktree actions | Rebase stale task branches, merge locally, resolve conflicts, clean up the worktree, or open a pull request when supported. |

For worktree-backed tasks, the Changes tab shows the live worktree diff while work is running, then falls back to the preserved execution diff after merge or cleanup. If the task branch is stale and the action is available, rebase onto the merge target before final review or merge. Large files can be loaded on demand to keep the view responsive.

Use review comments when you want the agent to revise specific lines. Use a task follow-up when the feedback is broader or not tied to a particular file.

For the full reference see <a href="https://docs.openvibely.ai/task-diffs-review" target="_blank" rel="noopener noreferrer">Task Diffs & Review</a>.

## No-Model Behavior

Execution paths are blocked when no models are configured. You will get an error toast with a link to `/models`.
