# OpenVibely Automation Vision

## Purpose

OpenVibely Automations give users a clear way to define, activate, and observe recurring autonomous software-development work.

The product should answer four questions without requiring users to inspect prompts, schedules, or task internals:

1. What automations exist in this project?
2. What is each automation intended to accomplish?
3. What work is active, waiting, blocked, or complete right now?
4. What real task, alert, issue, execution, or pull request produced that state?

This document defines the product direction and durable design principles. Detailed schemas, transactions, APIs, failure recovery, tests, and delivery phases belong in [IMPLEMENTATION.md](./IMPLEMENTATION.md).

## Product Promise

A user can create several automations in one project, understand each one visually, and watch work move through them in near real time.

Automations remain grounded in visible OpenVibely resources. A graph is not a fictional representation generated from prompts. Its nodes and status link to persisted tasks, executions, schedules, alerts, goals, issues, pull requests, reviews, and bounded Workflows.

## The User Experience

### Portfolio

The project-level Automations page presents all automations as a bounded, scannable portfolio.

Each automation shows:

- its name and purpose;
- whether it is a draft, active, paused, or archived;
- whether it is healthy, degraded, or unhealthy;
- recent and next scheduled activity;
- counts of running, waiting, blocked, failed, and recently completed work;
- a direct path to its graph and underlying resources.

Users may have several active automations that share workers or inbox tasks. Shared resources must not merge the automations' identities or histories.

### Live Graph

The Live view shows the published automation topology with current work overlaid on every relevant node.

An automation does not have one universal current position. One scheduled invocation can create several suggestions, later invocations can process them, and each suggestion can remain active for days. The graph therefore shows simultaneous counts and states instead of one moving cursor.

For example:

```text
Daily Scan       1 running
Approval         2 waiting
Implementation   1 running · 1 blocked
PR Review        1 waiting
Done             14 completed
```

Selecting a node reveals the real resources responsible for those counts. Selecting an invocation isolates one trigger occurrence. Selecting a work item shows its full lifetime across invocations.

### Definition And History

The product keeps four concerns visually distinct:

- `Definition`: the immutable published topology and configuration;
- `Edit draft`: unpublished changes that have no runtime effect;
- `Live`: current activity projected onto the published topology;
- `History`: immutable invocations and work-item transitions.

Users should never mistake draft configuration for active behavior or current graph state for historical truth.

## Creating An Automation

Users can begin from the Automations page through:

- a maintained template;
- a natural-language `Describe it` flow;
- a blank constrained draft.

Users can also describe an automation from Chat. Page and Chat creation are two surfaces over the same generation, normalization, validation, planning, and publication services.

Every creation path follows the same safety boundary:

```text
Describe or select
  -> create draft
  -> inspect and edit
  -> validate
  -> preview exact resource changes
  -> explicitly confirm
  -> publish
  -> observe in Live view
```

Creating or editing a draft never starts work and never silently creates tasks, schedules, alerts, issues, executions, or pull requests.

Natural-language generation is constrained to supported automation adapters and configuration. It may propose a valid draft; it does not invent tools, executable code, arbitrary graph runtime behavior, or missing integrations.

## Core Concepts

| Concept | Product meaning |
| --- | --- |
| Automation | Stable project-owned identity for an autonomous process. |
| Version | Immutable published definition used by new trigger occurrences. |
| Node | A visible trigger, task, human gate, action, condition, or outcome. |
| Invocation | One occurrence of a schedule, manual trigger, or registered event. |
| Work item | One durable suggestion, issue, or implementation stream that may span many invocations. |
| Position | A node currently occupied by a work item; one work item may have several positions during parallel work. |
| Activity | A processing attempt linked to a real resource or execution. |
| Transition | Durable evidence that a work item entered, left, completed, or failed at a node. |

These concepts must remain separate. In particular, an invocation is not the lifetime of every work item it discovers.

## Relationship To Existing OpenVibely Functionality

Automations are a control-plane, provenance, and visualization layer over existing OpenVibely behavior. They are not a second task system, worker pool, inbox queue, GitHub client, or Workflow engine.

Existing domain records remain authoritative:

- tasks own visible work, status, lineage, chaining, and swarm configuration;
- executions own agent-run status and output;
- schedules own fixed recurrence;
- Alerts own native approval and processing claims;
- `thread_inputs` owns durable queued inbox and Chat follow-up work;
- goals own continuation state;
- Workflow executions own bounded multi-agent Workflow state;
- GitHub-linked records own issue, pull-request, review, and merge state;
- the existing broadcaster and SSE connection own live invalidation delivery.

Automation-specific state records identity across time, causal bindings, graph positions, and transition history. It may project existing resource state, but it must not overwrite that state based only on a graph interpretation.

When a required transaction boundary is missing, extend the existing behavior-owning service or repository. Do not reproduce its logic inside an automation adapter.

## Existing Workflows Versus Automations

OpenVibely Workflows and Automations solve different problems.

| Workflow | Automation |
| --- | --- |
| Bounded execution for one triggering task. | Long-lived coordination across schedules, tasks, human gates, issues, and pull requests. |
| Interprets step dependencies, votes, gates, and handoffs. | Uses registered adapters to configure and observe existing functionality. |
| Usually completes in one execution window. | Work items may remain active across days and many invocations. |
| Workflow execution tables are authoritative. | Links a Workflow execution as one activity when appropriate. |

Automation graph edges are not interpreted by a new generic execution engine. A supported topology exists only when a registered adapter defines how it maps to current OpenVibely capabilities.

## Safety And Trust

The product earns trust through visible intent and durable evidence.

- Drafts are inert.
- Publication shows exact resources that will be created, reused, updated, enabled, or disabled.
- Chat publication requires a server-recognized explicit confirmation, not model interpretation of an ambiguous message.
- Human approval, pull-request review, merge, release, and deployment boundaries remain human-controlled unless separately configured and authorized.
- Project isolation applies to every definition and runtime relationship.
- Graph payloads omit secrets, full prompts, execution output, diffs, and private message bodies.
- Retries and restarts must not duplicate work or silently lose provenance.
- Pausing one automation must not disable shared workers or unrelated schedules.

## Representative Journeys

### Create From A Template

1. The user opens Automations and selects `New Automation`.
2. The user chooses a maintained GitHub SDLC template.
3. OpenVibely asks only for required project-specific configuration.
4. The user reviews the draft graph and validation results.
5. Publication preview lists concrete task, schedule, and approval changes.
6. The user confirms publication and enters the Live view.

### Describe From Chat

1. The user describes the desired automation in project-aware Chat.
2. OpenVibely generates and persists an inert draft using supported capabilities.
3. Chat returns a concise summary and graph link.
4. The user requests a publication plan.
5. Chat displays the exact plan and requests the documented explicit confirmation.
6. After confirmation, OpenVibely publishes through deterministic services and returns the active graph link.

### Observe Concurrent Work

1. A scheduled finder creates three suggestions.
2. The invocation finishes, but the work items remain open.
3. One suggestion waits for approval, one is implemented, and one reaches PR review.
4. A later invocation discovers more work while the earlier items remain active.
5. The graph displays all current positions and lets the user inspect each native resource.

### Explicit Registration By Supported Setup Flows

1. A maintained Native or GitHub SDLC setup creates or reconciles its visible tasks, schedules, and approval configuration.
2. The same setup explicitly publishes an Automation definition using its registered adapter and actual project resource IDs.
3. Publication returns the Automation URL only after the definition and resource memberships are durable.
4. Rerunning setup reuses the same stable project-scoped Automation key instead of creating a duplicate.

OpenVibely never infers an Automation from task titles, prompts, schedules, lineage, Alerts, or GitHub relationships. Pre-feature tasks and schedules remain ordinary resources and do not appear on the Automations page. A user who wants an old setup represented creates a new Automation through a supported creation path.

## Delivery Strategy

Build the product incrementally and audit each phase before starting the next.

1. Explicitly register and render definitions created by updated supported setup flows.
2. Add durable invocations, work items, positions, and live projection by extending existing Scheduler, Worker, Alert, queued-input, and GitHub paths.
3. Add history, replay, health, and metrics.
4. Add template, Describe it, Chat, and constrained visual-builder publication.

Each phase must leave existing non-automation behavior unchanged and independently tested.

## Non-Goals

The initial product does not provide:

- arbitrary executable directed graphs;
- a replacement for the existing Workflow engine;
- hidden background tasks that do not appear in OpenVibely;
- automations that rewrite their own definitions by default;
- automatic merge, release, deployment, or rollback authority;
- implicit publication from a natural-language description;
- heuristic discovery, migration, or graph backfill for pre-feature task and schedule arrangements;
- a fully expanded unbounded graph of every project resource;
- live graph reads that call external providers on every refresh.

## Product Success

The vision is realized when users can:

1. See several independent automations in one project.
2. Create an automation safely from a template, description, blank draft, supported setup flow, or Chat.
3. Understand exactly what publication will change before it becomes active.
4. See simultaneous running, waiting, blocked, failed, and completed work at the correct graph nodes.
5. Navigate from graph state to the authoritative OpenVibely or GitHub resource.
6. Understand the history of one trigger occurrence or one work item across many occurrences.
7. Reload or restart OpenVibely without losing position or provenance.
8. Trust that shared resources, retries, and failures do not merge histories or duplicate work.
9. Continue using existing tasks, schedules, Alerts, Workflows, goals, GitHub integrations, queued inputs, and live updates without behavioral regressions.

## Guidance For Implementing Agents

An implementing agent should use this vision to preserve product intent and the implementation runbook for technical contracts.

For each phase:

1. Inspect the current repository before proposing changes.
2. Extract only the relevant runbook requirements into a bounded implementation plan.
3. Identify the existing service or repository that owns each behavior.
4. Extend that boundary instead of creating a parallel subsystem.
5. Implement and test one phase completely.
6. Audit correctness, reuse, security, migrations, and regressions before continuing.

If current code conflicts with an implementation detail but not with this vision, update the phase plan and document the reason. If a proposed change conflicts with this vision's safety, authority, or product boundaries, stop and resolve the design conflict before implementation.
