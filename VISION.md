# OpenVibely Vision

OpenVibely exists to make AI software development feel like running a capable,
auditable team instead of juggling disconnected chat sessions.

The long-term goal is a self-hosted command center where one project-aware
conversation can plan work, split it into tasks, run specialized agents in
parallel, connect those tasks into automations, review their changes, learn
from the results, and keep unfinished goals moving without losing human
control.

Agents do the work. Humans stay in command.

## The Product We Are Building

OpenVibely should become the recursive self-improvement control plane for
software teams.

That means:

- A user can describe an outcome once, then watch the system turn it into
  reviewable, executable work.
- Work can be modeled at the right level: one task, a swarm of tasks, a
  scheduled follow-up, or an automation graph that ties triggers, agents,
  tasks, approvals, GitHub actions, and outcomes together.
- Each task and automation has a visible lifecycle: prompt, model, agent, logs,
  thread, attachments, changed files, review comments, schedules, goals,
  memory, skill usage, graph state, validation, provenance, and approvals.
- Work happens in isolated, inspectable branches or worktrees so AI output is
  never a hidden mutation.
- Chat remains the coordination surface for the whole project, while task
  detail pages remain the operational surface for individual units of work.
- The system improves through durable project memory and reusable skills, not
  through opaque personalization or unreviewable background behavior.
- Teams can run it themselves, keep their data, choose their model providers,
  and understand what happened after every run.

## Why It Matters

The first wave of coding agents made individual developers faster, but often by
moving work into scattered, disposable sessions. Context gets repeated. Diffs
appear without a clear chain of intent. Follow-up work depends on whoever
remembers to ask for it. Lessons learned in one run rarely make the next run
better.

OpenVibely is built around a different premise: AI development work should be
orchestrated, inspectable, repeatable, and improvable.

The product should help a team answer:

- What are we trying to accomplish?
- Which workflow is responsible for moving it forward?
- Which agents are working on it?
- What changed?
- Why did it change?
- What still needs attention?
- What did the system learn that should help next time?

## Principles

### Chat Is The Control Plane

The project chat should be the place where fuzzy intent becomes coordinated
execution. Users should be able to plan, create tasks, steer active work,
inspect status, schedule follow-ups, and ask what comes next without opening a
new AI session for every subproblem.

### Tasks Are The Unit Of Accountability

Every meaningful piece of work should become a task with durable state,
execution history, reviewable output, and clear status. A task should be small
enough to inspect and steer, but rich enough to carry the context an agent needs
to make real progress.

### Automations Are The Unit Of Repeatable Orchestration

Automations should let teams turn recurring or multi-step development workflows
into explicit, project-scoped graphs. A graph can connect schedules, tasks,
agents, approvals, GitHub handoffs, alerts, and outcomes, but it should compile
into the same inspectable resources the rest of OpenVibely uses. Automations
should be declarative enough to review, diff, template, and regenerate from a
description without silently changing running resources. They are not a second
executor or a shortcut around review; they are a higher-level way to make the
existing lifecycle repeatable, visible, and easier to improve.

### Autonomy Must Stay Reviewable

OpenVibely should automate the boring and repetitive parts of software work, but
not hide them. Automated execution, automation graphs, scheduled work, task
chaining, goal loops, memory updates, and skill improvements should leave
evidence that users can inspect, question, undo, or refine.

### Local Ownership Is A Feature

Self-hosting, SQLite defaults, a single Go binary, desktop mode, and explicit
runtime paths are not incidental implementation details. They are part of the
promise: teams should be able to run the system close to their code and keep
control of operational state, credentials, repositories, and model choices.

### Model Choice Should Be Practical

OpenVibely should support multiple providers and auth paths without turning
provider setup into the product. The user should configure models once, assign
defaults or agents where useful, and then focus on work. Provider differences
should be normalized where possible and surfaced clearly where they matter.

### Memory Should Reduce Repetition

Project memory should preserve stable facts, decisions, conventions, pitfalls,
and implementation context that would otherwise be retyped into every prompt.
It should stay project-scoped, compact, inspectable, and curated rather than
becoming an unbounded transcript dump.

### Skills Should Make Agents Better

Skills are how completed work becomes future leverage. The system should learn
reusable workflows, domain habits, review checklists, and operational runbooks
from successful tasks while keeping those skills visible and editable.

### Insights Should Turn Activity Into Judgment

As projects accumulate tasks, schedules, automations, agents, model usage,
failures, costs, memories, and skills, OpenVibely should help teams see the
shape of that activity. Insights should expose trends, health signals, upcoming
work, reflections, and learning quality so teams can decide what to trust, tune,
pause, prune, or invest in next.

### Integrations Should Bring Work To The System

Slack, Telegram, GitHub, webhooks, the REST API, and future channels should make
OpenVibely easier to use from where teams already operate. Integrations should
create and coordinate real project work, including automation triggers and
handoffs, rather than becoming separate products with separate sources of truth.

### The UI Should Favor Operational Clarity

OpenVibely is an operations surface, not a marketing page. Screens should make
status, actions, diffs, logs, schedules, and review state easy to scan. The
interface should feel calm, dense, and dependable when a team has many agents
and tasks in motion.

## What We Will Not Optimize For

OpenVibely should not become:

- A black-box autonomous developer that changes code without review.
- A thin chat wrapper with no durable task, diff, memory, or scheduling model.
- A generic no-code automation builder disconnected from code, agents, review,
  provenance, and project memory.
- A hosted-only SaaS that requires teams to give up ownership of their runtime
  state.
- A single-provider product where model choice is locked into one vendor.
- A project-management clone where AI execution is secondary.
- A pile of prompt templates with no lifecycle, learning, or audit trail.

## Direction

The product should keep deepening the loop that already defines it:

1. A user describes a goal in Chat.
2. OpenVibely turns the goal into concrete tasks.
3. When the work should repeat or coordinate multiple handoffs, OpenVibely turns
   the plan into an automation graph.
4. Agents execute the graph's tasks with the right model, skills, memory, and
   tools.
5. The user reviews diffs, threads, logs, graph state, approvals, and lifecycle
   evidence.
6. Goal loops, schedules, automations, and integrations keep useful work moving.
7. Insights reveal trends, costs, failures, upcoming work, and learning quality.
8. Memory Curator and Skill Curator preserve what should improve the next run.

Near-term product decisions should strengthen that loop. Features are most
valuable when they make work easier to orchestrate, safer to review, clearer to
operate, or more reusable over time.

The north star is not simply "more automation." It is compounding capability
under human command.
