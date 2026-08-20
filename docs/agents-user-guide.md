# Agents User Guide

Use `/agents` (System → Agents in the sidebar) to define reusable AI worker profiles.

## What This Page Is For

An Agent is a reusable execution profile you can attach to tasks. It defines the system prompt/persona, allowed tools, routing hints, permissions, skills, and optional model override.

Why this matters:

- Keeps behavior consistent across many tasks.
- Lets you separate "how to work" (agent) from "what to do" (task prompt).
- Lets agent-owned skills improve over time for that specific role.

## Create an Agent

1. Open `Agents` from the System section of the sidebar.
2. Click `+ Add Agent`.
3. Fill the `Details` tab:
   - `Name`, `Description`, `Key`, `Scope`
   - `System Prompt` (or use `Generate` from a description)
   - Optional model override
4. Choose allowed tools (file, shell, web, notebook, scoped-file, management).
5. Set routing hints and permissions.
6. Click `Save`.

## Save From Chat

In Orchestrate-mode Chat, you can explicitly ask Chat to save a reusable Agent profile with a name, description, system prompt, optional model override, allowed tools, scoped-file grants, enabled state, and primary-task selectability. Chat-created Agents appear on the Agents page and can be assigned to future tasks like Agents created in the browser.

Use the Agents page for advanced edits such as plugin configuration, Agent-owned skills, lifecycle hooks, and protected system-agent review; Chat does not accept credentials, MCP env/header secrets, OAuth tokens, API keys, plugin marketplace changes, deletion, or protected-agent edits.

## Generate From Description

In `Agent Details`, enter a description and click `Generate`. This drafts configuration from your description including a starter system prompt.

Use this as a faster starting point for complex prompting rather than writing a full system prompt by hand.

## Agent Detail Tabs

The agent detail page has three tabs: **Details**, **Skills**, and **Lifecycle Hooks**.

**Details** covers identity, instructions, tools, routing, and permissions — the core agent configuration.

**Skills** shows skills owned by this agent. From this tab you can create a new skill, edit an existing one, change skill state (enable, disable, always-use, archive), or preview which skills the router would currently select for a hypothetical task. System agents show a protected banner instead of edit controls.

**Lifecycle Hooks** lists the hooks configured for this agent. Each row shows the hook type (`route_task`, `before_run`, `after_complete`), the assigned skill, run policy, and tool-scope permissions. The tool-scope section controls what that hook's skill is allowed to do — read or write skills, read or write repository files, use shell, and so on. An explainer callout on the tab summarizes what tool-scope means for that hook type.

## Scopes

| Scope | When To Use It |
|---|---|
| Global | The agent should be reusable across projects. |
| Project | The agent is specific to one repository or workspace. |

## How Agents Fit Tasks

When creating a task, choose an agent or leave selection on auto-routing. An agent can also define model behavior, so task execution inherits a consistent combination of instructions, tools, and provider settings.

When a task is assigned to an agent, Skill Curator works within that agent's own skill library and can improve only that assigned agent's skills after completion.

## System Agents

OpenVibely includes protected system agents for lifecycle and learning work users should not have to manage manually.

| System Agent | What It Does |
|---|---|
| `System: Memory Curator` | Autonomously creates and updates durable project memory from completed work, recalls relevant memory before tasks, and runs scheduled consolidation. |
| `System: Goal Agent` | Evaluates active task goals after each task turn via the `evaluate_task_goal` lifecycle hook, queuing continuation follow-ups until the goal is achieved or blocked. |
| `Skill Curator` | Routes reusable skills into tasks and improves standalone or agent-owned skill libraries from completed work. |

`System: Memory Curator` is a protected on-disk system agent. Its skills live under `.openvibely/agents/memory_curator/` in the project repository and can be reviewed there, but the agent is not user-editable or selectable as a primary task agent. It uses scoped memory tools rooted at `.openvibely/memories`, skips normal repository-editing tools, and does not get a runtime git worktree.

`System: Goal Agent` is similarly protected — it is not user-selectable as a primary task agent and acts only through goal tools and `send_to_task`. It does not edit repository files or run shell commands.

## Skills And Lifecycle Hooks

Agents can own skills on disk, and those skills can evolve from completed work.

| Capability | User Impact |
|---|---|
| Agent-owned skills | Keep role-specific instructions and reusable habits attached to one agent. |
| Lifecycle hooks | Run supporting steps before or after task execution. |
| Skill routing | Select relevant agent skills for assigned-agent tasks. |
| Agent skill learning | Improve the assigned agent's skills without writing into unrelated agents. |

Use agent-owned skills when the knowledge should stay with that agent. Use standalone skills when the knowledge should help many agents or no-agent tasks.

## Edit and Delete

From any agent card menu:

- `Edit`
- `Delete`

Deleting an agent unlinks tasks that referenced it. System agents cannot be deleted or edited.

## Best Practices

- Name agents by the work they perform, not by an implementation detail.
- Keep instructions focused enough that you can predict the agent's behavior.
- Use permissions and scoped file settings to make consequences explicit.
- Prefer routing hints when multiple agents could plausibly handle similar work.
- Use project-scoped agents for repository-specific conventions.
- Let agent-owned skills capture role-specific learning that should improve future assigned tasks.
