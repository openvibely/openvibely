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
- `Activate All`
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
- `Schedules`: add/edit/remove task schedules
- `Chaining`: configure child-task creation flow
- `Attachments`: manage attached files

Why task detail matters:

- It is where you inspect execution output and iterate safely.
- `Thread` and `Changes` are the two most important tabs during active debugging/build work.

## No-Model Behavior

Execution paths are blocked when no models are configured. You will get an error toast with a link to `/models`.
