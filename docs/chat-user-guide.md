# Chat User Guide

Use `/chat` to orchestrate work across tasks in a project.

## What This Page Is For

`/chat` is the high-level control surface for project orchestration.

Use it when you want to:

- plan and prioritize work,
- create/update/run tasks through conversation,
- inspect progress without jumping between many pages.

## Chat Input Controls

At the bottom input bar you can set:

- Model selector: `Auto`, `Default`, or a specific configured model
- Mode selector: `Orchestrate` or `Plan`
- Attach files
- Speech-to-text (microphone)

Then send with Enter (Shift+Enter for new line).

Why these controls matter:

- Model selector controls execution engine for that message.
- Mode selector controls whether the session is planning-only or execution-capable.
- Attachments add context (files, screenshots, notes) to improve results.

## Orchestrate vs Plan

### Orchestrate

Execution-capable mode for normal chat-driven task operations.

Use this when you want actions to happen (for example creating/running/updating tasks).

### Plan

Read-only planning mode for exploration and planning.

Use this when you want strategy/design discussion first without triggering task mutations.

When a plan is complete, the UI can prompt you to `Switch to Orchestrate` to execute the next step.

## Chat History

- Messages stream in real time.
- Task markers in responses are converted to clickable task links.
- `Clear Chat` is available from the top-right menu.

## Attachments

- Add files with the paperclip button or drag-and-drop.
- Attachments are included with the message turn.

Use attachments whenever the answer depends on local file content or visual context.

## Project Scope

Chat is project-scoped. The selected sidebar project controls chat context.

If results look unrelated, verify the currently selected project first.

## No-Model Behavior

If no model is configured, send attempts are blocked and a toast links directly to `/models`.
