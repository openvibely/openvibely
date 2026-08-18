# Chat User Guide

Use `/chat` to orchestrate work across tasks in a project.

## What This Page Is For

Chat is your single control center for everything happening in a project. Describe what you want — a bug fix, a new feature, a refactor across multiple files — and Chat creates the tasks, runs them in parallel, and reports back. No terminal, no separate AI windows, no manual task wiring. One conversation drives the whole project.

## Chat As Your Single Control Center

The fundamental idea behind Chat is that you should never need more than one window open. Instead of keeping a terminal running, a separate AI session for research, another for writing code, and a third for tests, you stay on the Chat page and describe what you want done. OpenVibely creates the tasks, runs them, and reports back — all from that one conversation.

This matters most when a goal has multiple parts. Say you want to add a new feature with backend changes, frontend wiring, and tests. Rather than creating each task manually, switching between them, and tracking what finished, you describe the goal once in Chat and ask it to break the work apart. Chat creates the task cards, starts the independent ones in parallel, and remains the place you return to ask what is done, what is blocked, and what should happen next.

Chat embeds clickable task links inline as it creates work, so you can jump to a task detail, review the diff, send a follow-up, and come back to Chat without losing your place in the conversation.

**What Chat can orchestrate from a single conversation:**

- Create one task or many tasks from a single natural-language request
- Set a persistent goal on a task so the Goal Agent drives it to completion automatically
- Execute tasks and report their status back into the conversation
- Send follow-up instructions into individual task threads
- Inspect task state, changes, agent output, and lifecycle events
- Schedule work, manage alerts, save repeatable Automation workflows, and coordinate across the whole project
- Send outbound messages through saved Slack, Telegram, Discord, or Email channel targets
- Accept steering or queue new prompts while a response is already in progress

## How Chat Writes Task Prompts

When Chat creates a task, it does not paste your words verbatim. It rewrites your request into a detailed, actionable instruction the executing agent can work from immediately.

If you say "fix the login bug," Chat creates a prompt along the lines of "Debug and fix the login bug that prevents users from signing in. Investigate the authentication flow, identify the root cause, and implement a fix with tests." The agent receives something specific enough to act on — you never had to spell all that out.

This matters because the coding agent that runs the task has no memory of your conversation. The task prompt is everything it knows. A thin prompt produces thin results. Chat's rewrite closes that gap automatically.

**What gets embedded in the rewritten prompt:**

- The intent and scope of your request, expanded into a clear instruction
- Any file paths, code snippets, or configuration you pasted into Chat — included verbatim so the agent acts on the exact code you showed it
- Screenshots or attachments referenced alongside the relevant instruction
- Concrete acceptance criteria or constraints you mentioned in passing

You can always open the task and read the full prompt Chat wrote. If it missed something, edit it before the task runs.

## Chat Input Controls

At the bottom input bar you can set:

- Model selector: `Auto`, `Default`, or a specific configured model
- Mode selector: `Orchestrate` or `Plan`
- Attach files
- Speech-to-text (microphone)

Then send with Enter (Shift+Enter for new line).

## Plan And Orchestrate Modes

The chat input includes a mode selector. OpenVibely defaults to `Orchestrate` when no mode is selected, and the selected mode is remembered per project in the browser.

| Mode | Use It When | Behavior |
|---|---|---|
| `Orchestrate` | You want Chat to help create, inspect, or coordinate real project work. | Enables action-oriented chat tools such as creating and managing tasks, reading project state, and coordinating workflow actions. |
| `Plan` | You want to think through an approach before anything is created or changed. | Keeps the conversation planning-oriented and limits action tools so the assistant can analyze, propose steps, and refine the plan first. |

A good default workflow is to start in `Plan` for vague or risky work, then switch to `Orchestrate` when the next task is clear. When a `Plan` turn finishes, Chat surfaces a prompt to continue in `Orchestrate` mode so you can move from analysis to action without manually switching.

## Parallel Task Example

When a goal has multiple independent parts, ask Chat to split them explicitly:

```
Plan the changes needed for OAuth login, then create separate tasks for backend routes, UI wiring, tests, and docs. Run the independent tasks in parallel where possible and keep this chat updated as they finish.
```

In `Orchestrate` mode, Chat creates multiple task cards and can execute them immediately. Each task keeps its own status, thread, lifecycle events, diff review, and worktree actions — but the original Chat page stays the place to ask what is done, what is blocked, and what comes next.

## What Stays Centralized

- The selected project context stays fixed for the whole conversation.
- Chat history keeps the original goal, plan, and follow-up decisions together in one place.
- Task links produced by Chat open the relevant task detail without leaving the project workflow.
- Running chat turns can accept steering or queue follow-up prompts instead of forcing a new session.

## Sending Messages Through Channels

In `Orchestrate` mode, Chat can save repeatable Automation workflows from maintained templates, descriptions, or reviewed Automation YAML. Channel-origin Chat from Slack, Telegram, Discord, and Email uses the same project-scoped Automation save path as web/API Chat; `Plan` mode can preview Automation designs but cannot save them.

In `Orchestrate` mode, Chat can use the `send_message` tool to send text through configured Slack, Telegram, Discord, or Email channels. Configure allowed destinations in `Channels` under `Outbound Message Targets` before asking Chat to send.

Examples:

```text
Email alice@example.com saying the deploy finished cleanly.
Send "standup moved to 10" to slack:#ops.
Message telegram:#alerts that the backup completed.
```

By default, Chat may send only to saved outbound targets or each platform's saved home target. If you enable `Allow explicit unsaved targets`, Chat may send to direct platform IDs such as `slack:C123`, `telegram:-100123`, or `email:person@example.com` when the channel itself is configured. If the destination is ambiguous, Chat should list available targets first.

## Chat History

- Messages stream in real time.
- Task markers in responses are converted to clickable task links.
- `Clear Chat` is available from the top-right menu.

## Attachments

- Add files with the paperclip button or drag-and-drop.
- Attachments are included with the message turn.

Use attachments whenever the answer depends on local file content or visual context.

## Queueing And Steering

If Chat is already responding, a new prompt can become queued input for the next turn. When the UI offers active-turn steering, a prompt can instead redirect the current response. This keeps the project conversation responsive without forcing users to open another thread just to add context or correct scope.

## Project Scope

Chat is project-scoped. The selected sidebar project controls chat context.

If results look unrelated, verify the currently selected project first.

## No-Model Behavior

If no model is configured, send attempts are blocked and a toast links directly to `/models`.

## Typical Flow

1. Select the project in the sidebar.
2. Open Chat.
3. Pick `Plan` or `Orchestrate` from the chat input controls.
4. Ask a question or describe the goal.
5. Attach files if they help explain the request.
6. Use the response to continue planning, create tasks, or inspect existing project work.

## When To Use Tasks Instead

Use Tasks instead when you already know the exact unit of work and want board status, scheduling, thread review, diff review, or worktree actions without a conversational layer around it.
