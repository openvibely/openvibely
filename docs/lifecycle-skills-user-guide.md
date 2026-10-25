# Lifecycle Hooks and Skills User Guide

The main task model selects relevant skills and memories from the available indexes during execution. Lifecycle hooks are optional supporting steps for context, goal evaluation, and learning.

They do not replace the main task. The normal task still runs as the selected/default agent.

## Task Flow

A task run can include enabled hooks in this order:

```text
route_task
-> before_run
-> normal task execution
-> after_complete
```

What each step does:

- `route_task` is an optional custom routing hook; built-in Skill Curator routing has been removed.
- `before_run` can add extra context before the main task runs.
- Normal task execution runs the task prompt with the selected/default agent.
- `after_complete` observes the result and can improve skills afterward.

Lifecycle hook implementation skills come from the lifecycle agent that owns the hook. Task skills are separate and come from the current task's skill scope. The main model chooses skills without a separate built-in routing call.

## Lifecycle View (Task Details)

Open a task's Details panel and select **Lifecycle** to see lifecycle hook invocations for that task, newest first. Each entry displays the hook type, status badge (`queued`, `running`, `completed`, `failed`, `skipped`), and pills for what the hook involved:

| Pill Type | What It Represents |
|---|---|
| Skill pill | A skill that was routed or selected for this hook invocation. |
| Memory pill | A memory file that was recalled during a `before_run` hook. |

Use the Refresh button to pull updated hook state while a task is still running.

## Skill Curator

Skill Curator maintains the skill library. Its after-complete learning hook is disabled by default; it can be enabled in the agent's lifecycle settings when automatic learning is wanted.

Common Skill Curator lifecycle skills:

- `observe_task_for_learning`: reviews completed tasks and decides whether skills should be improved.
- `maintain_skill_library`: runs scheduled skill-library maintenance.

When enabled and permitted, after-complete learning can update skills through scoped write tools. Scheduled library maintenance remains available independently.

## Project Permissions and Dependencies

Project settings → **Skills & Memory** controls reading and writing skills and memories separately. New projects allow reading skills and reading/writing memories, but disable skill writes. Global skill writes require a separate switch and enabled skill writes. Existing projects retain their previous permissions.

Built-in document skills support Word documents, PDFs, spreadsheets, and presentations. When a selected skill needs a missing dependency, OpenVibely asks for approval before installing it and shows installation progress. Once installation succeeds, the pending skill request resumes.

## Goal Agent

`System: Goal Agent` is a protected built-in system agent that drives persistent task objectives. It is not user-selectable as a primary task agent.

The Goal Agent owns one `after_complete` lifecycle hook: `evaluate_task_goal`. After every task turn, this hook reads the stored goal, inspects the latest task transcript and tool evidence, and takes one of three actions:

| Decision | What Happens |
|---|---|
| Not yet achieved | Queues a concrete continuation follow-up so the task keeps making progress. |
| Achieved | Marks the goal `achieved` with an evidence-based reason and stops queuing. |
| Blocked | Records a blocker key and reason; after the same blocker repeats, transitions the goal to `blocked` and stops queuing. |

Goal continuation is injected as a normal queued follow-up, so it respects worker capacity, lifecycle hooks, worktree state, and all other existing task behaviors. The Goal Agent acts only through goal tools (`get_task_goal`, `mark_task_goal_achieved`, `report_task_goal_blocked`) and `send_to_task` — it does not edit repository files or run shell commands.

See the [Tasks User Guide § Task Goals](./tasks-user-guide.md#task-goals) section for how to set a goal from the task edit dialog or from Chat Orchestrate mode.

## The Skills Page

Open `Skills` from the sidebar. The Skills page lists all skills in scope for the selected project: standalone and agent-owned, global and project-scoped.

Each skill card shows the skill name, key, scope, and current state badges. Use the per-card dropdown to create, edit, enable or disable, toggle always-use, or archive a skill. The search bar filters by name or key.

To add a skill manually, click `+ Create Skill`. To import a pre-written standalone skill package, use `Import Skill Package`.

## Importing Skill Packages

A standalone skill package can be either a `SKILL.md` file or a folder containing `SKILL.md`. The package body describes the reusable behavior, and optional support files can live under `references/`, `templates/`, `scripts/`, or `assets/`.

When importing, choose whether the skill should be global or project-scoped. Project-scoped imports stay tied to the selected project and can override a global skill with the same key.

Standalone imports must not declare `agent.key`. Agent-owned skills belong to a specific agent and are managed through agent skill surfaces and lifecycle curation, not the standalone package import flow.

## Skill States

| State | What It Means |
|---|---|
| Enabled | Default. The skill is available for routing and appears in the routing context. |
| Disabled | Hidden from routing. The skill does not appear as a routing candidate and is excluded from the `skills_list` tool output. Use this to suppress a skill without deleting it. |
| Always Use | Forces the skill into every task run for its scope, regardless of routing selection. Stored in the `SKILLS.md` index. Use sparingly — always-use skills add context to every task even when they may not be relevant. |
| Archived | Soft-deleted. Archived skills are removed from normal views and routing but remain on disk. |

The always-use toggle can be set from the skill card dropdown or from the create/edit modal. A skill can be both always-use and enabled; a disabled skill's always-use flag has no effect while it is disabled.

## Standalone Skills

Standalone skills are shared, reusable skills. They are used when a task does not have a specific assigned agent.

Paths:

```text
<root>/skills/SKILLS.md
<root>/skills/<skill_key>/SKILL.md
```

Flow for a task with no assigned agent:

```text
main model sees the standalone skill index
-> main model selects and reads relevant skills with skill_view
-> normal task uses the skill instructions
-> permitted skill writes can improve standalone skills with skill_manage
```

Use standalone skills for knowledge that should help many agents, many tasks, or future no-agent tasks.

## Agent-Owned Skills

Agent-owned skills belong to one specific agent. They are used when a task is assigned to that agent.

Paths:

```text
<root>/agents/<agent_key>/SKILLS.md
<root>/agents/<agent_key>/skills/<skill_key>/SKILL.md
```

Flow for a task assigned to an agent:

```text
normal task runs as the assigned agent
-> main model sees that agent's skill index
-> main model selects and reads relevant agent-owned skills
-> permitted writes can improve that agent's skills with agent_skill_manage
```

Assigned-agent selection uses the assigned agent's skill scope; no-agent tasks use standalone skills.

Use agent-owned skills for behavior that is specific to that agent's role, workflow, or private responsibilities.

## Global and Project Scopes

Skills and agents can exist in two scopes:

```text
global
project
```

Global scope is shared across projects. Project scope is specific to the current project.

For standalone skills:

```text
global skills/SKILLS.md
+ project skills/SKILLS.md
-> project wins when the same skill key exists in both
```

For agent-owned skills:

```text
global agents/<agent>/SKILLS.md
+ project agents/<agent>/SKILLS.md
-> project wins when the same agent skill key exists in both
```

Examples:

- If both global and project define `debug_go_tests`, the project standalone skill wins.
- If both global and project define `code_reviewer/review_migrations`, the project agent-owned skill wins.

## Learning After Completion

When enabled, `observe_task_for_learning` runs after the normal task finishes. It is disabled by default.

It receives learning context that includes:

- assigned agent profile, when the task had one,
- selected agent-owned skills,
- selected standalone skills,
- task transcript,
- write policy.

Write tools are separated by scope:

```text
skill_manage
-> writes standalone skills only

agent_skill_manage
-> writes only the assigned agent's skills
-> server-scoped to the completed task's assigned agent
-> cannot choose another agent
```

Use `skill_manage` when the learning is reusable across agents or future no-agent tasks.

Use `agent_skill_manage` when the learning is specific to the assigned agent's role or one of its selected agent-owned skills.

If the correct scope is unclear, prefer no automatic write or a standalone skill update. Do not copy standalone skills into an agent unless the task clearly proves that agent needs a specialized version.

## Scheduled Skill Library Maintenance

`System: Skill Library Maintenance` is a scheduled task assigned to Skill Curator.

Flow:

```text
scheduled task fires
-> assigned agent is Skill Curator
-> main model selects maintain_skill_library from Skill Curator's skills
-> normal task runs as Skill Curator
-> maintain_skill_library manages standalone skills and non-system agent-owned skills
```

This scheduled task maintains standalone skills and non-system agent-owned skill packages when the guidance is agent-specific. It must not create, edit, archive, route, or reassign agents, and it must not modify protected system agent skills such as `skill_curator/*` or `memory_curator/*`.

## System Memory Consolidation

`System: Memory Curator` is the built-in on-disk agent under `internal/builtinskills/builtin/agents/memory_curator/`. It maintains scheduled consolidation. Main task models select and update memories themselves, subject to project permissions.

- `memory_curator/consolidate_memory` runs through the visible `System: Memory Consolidation` scheduled task assigned to Memory Curator.

Scheduled consolidation flow:

```text
scheduled memory task fires
-> assigned agent is System: Memory Curator
-> main model selects consolidate_memory from the Memory Curator agent's indexed skills
-> normal task runs as Memory Curator with scoped memory file tools
```

The Memory Curator agent is not user-selectable as a primary agent and writes only through its scoped memory file tools.

## Guardrails

Important separation rules:

- Lifecycle hook skills come from the lifecycle agent that owns the hook.
- Task skills come from the current task's routing scope.
- No-agent tasks route standalone skills.
- Assigned-agent tasks route only that assigned agent's skills.
- `skill_manage` cannot write agent-owned skills.
- `agent_skill_manage` cannot write standalone skills; assigned-agent learning is scoped to that assigned agent, while explicit scheduled library maintenance may target non-system, non-protected agents only.
- Project scope overrides global scope for matching skill keys.
