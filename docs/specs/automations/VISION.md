# OpenVibely Automation Graphs Vision

## Purpose

Automation Graphs gives users one visual, project-scoped place to design recurring and event-driven work from capabilities OpenVibely already provides. Users can configure supported nodes, connect deterministic handoffs, save the graph once, and watch resulting runtime state on that same graph.

Automation Graphs is not a diagram-only preset assembler and does not introduce a second task system, scheduler, queue, worker pool, GitHub client, Alert processor, Workflow engine, or arbitrary graph runtime.

## Product Promise

A user can start from a maintained template, a natural-language description, or a blank canvas; build an open-ended supported graph; and save it directly into real OpenVibely resources.

Every saved Automation has one stable project-owned identity and exactly one current saved graph. The product does not retain an Automation draft lifecycle, editable server-side drafts, version history, superseded graphs, version selection, a Definition page, an execution/history page, or restorable publications.

## User Experience

### Portfolio

The project-level Automations page presents one compact searchable card for each saved Automation. A card opens its Live graph; its actions allow browser-local editing or confirmed deletion when lifecycle boundaries permit it.

A failed Save creates no separate portfolio item or persistent recovery state. A failed first Save creates nothing, and a failed edit leaves the previously saved Automation active.

Existing tasks, schedules, Alerts, GitHub objects, prompts, names, or lineage never cause an Automation to appear implicitly. An Automation exists only after an explicit successful Save or maintained Native/GitHub registration.

### Builder

Users can add, place, configure, connect, reconnect, and remove surfaced nodes. The public node set includes:

- `Schedule`, which owns a substantive scheduled Task and its Scheduler row;
- `Task`, which uses ordinary OpenVibely Task and Agent capabilities;
- Native Alert notification and human-approval nodes;
- supported GitHub issue, inbox, assignment, pull-request, and human-review nodes;
- `Outcome`, which represents a terminal result without creating a parallel runtime effect.

The hidden Workflow subsystem is not exposed as an Automation node.

Connections are directional and deterministic. A browser-authored edge leaves the source node's right output and enters the target node's left input. Unsupported handoffs, unsafe configuration, ambiguous task parents, executable cycles, invalid approval conditions, unavailable capabilities, and foreign-project references fail closed.

New and edited web graphs live only in browser memory until `Save changes`. Refreshing or navigating away discards unsaved edits. Opening a builder creates no Automation identity, Task, Scheduler row, Alert, GitHub object, execution, or runtime state.

### Save

Save submits the complete browser graph through shared schema validation, capability checks, and atomic application. A successful first Save creates the Automation and its real resources. A successful edit immediately replaces the current graph under the same Automation identity, including while Automation work is running.

Replacement removes runtime projection tied to the replaced graph. Existing domain resources continue according to their own lifecycle, but an old graph cannot remain selectable, restorable, or visible as Automation history. If Save fails, the current saved graph and behavior remain active.

Save writes the Automation identity, complete current graph, Task changes, Scheduler rows, resource bindings, and replaced-graph cleanup in one SQLite transaction. Any error rolls back the entire transaction, so no journal, staged graph, retry state, or partially created Save resources remain.

### Live Graph

Automation detail is one full-width Live graph. It fills the available page space and overlays current state on the nodes that own or project real work.

There are no Automation-facing Definition, History, invocation, work-item, or node-resource pages. Runtime provenance may persist internally to support current-graph projection and reliable handoffs, but it is not a user-selectable execution history.

Schedule and Task nodes link to their exact current project-scoped Task when one is bound. A Schedule opens its Schedule-owned Task. Action, gate, Outcome, and unbound nodes remain inert.

## Creation Surfaces

The supported creation paths are:

- `Template`, using a canonical maintained graph;
- `Describe It`, using bounded model generation and one bounded repair path;
- `Blank`, using the full supported custom builder;
- Chat, using one direct `save_automation` tool call when the user asks to create or save an Automation.

Template, Describe It, and Blank all load a graph into browser memory and create nothing until `Save changes`. `Use template` opens the canonical maintained graph in the same builder so the user can review or customize it before the atomic Save.

Describe It receives only surfaced, project-scoped, secret-free capabilities and no mutation tools. Invalid or unsupported generated graphs remain visible as errors and create no resources.

Chat creation is available only in Orchestrate mode. The user's request to create or save an Automation authorizes one `save_automation` tool call, which generates or loads the candidate, validates it, and applies it through the same atomic Save path as the web builder. Chat does not stop at a confirmation-only plan or require a second exact command. A successful call creates the Automation and returns its Live URL; a generation, validation, or Save failure creates no partial resources.

## Runtime Model

Automation Graphs reuses existing OpenVibely machinery:

- Tasks own visible work, status, Agent assignment, lineage, chaining, and swarm configuration;
- Scheduler rows own recurrence;
- workers and queues own execution admission and dispatch;
- Alerts own Native approval and processing claims;
- GitHub services own issues, assignment, pull requests, review observation, and merge observation;
- existing repositories and runtime projection preserve exact project, Automation, graph, node, edge, invocation, work-item, activity, and resource provenance where required.

A Schedule node is itself a substantive Task plus a Scheduler row targeting that Task. `Schedule → Task` means scheduled Task A performs its work and then activates or reuses distinct Task B through normal Task and worker machinery. It must not silently schedule Task B directly.

Ordinary Task nodes materialize as stable Tasks on Save. Only the exact `GitHub inbox → Task → Open pull request` topology treats the Task node as configuration for issue-specific Tasks created later by the inbox.

When an existing service lacks a required transaction or provenance boundary, extend that service or repository. Do not duplicate its behavior in an Automation-specific executor.

## Human And Integration Boundaries

Native notification actions create no Alert during Save. Alerts arise later from configured runtime work and pass through the explicit human-approval boundary.

GitHub publication creates only configured producer or inbox Tasks and Scheduler rows. Issues, issue-specific Tasks, and pull requests arise later through existing GitHub and Task services.

Approval authorizes only the configured downstream handoff. GitHub assignment, pull-request review, merge, release, and deployment remain human-controlled. Automation configuration never grants automatic merge, release, deployment, rollback, or arbitrary execution authority.

Every read, write, resource reference, and idempotency decision remains project-scoped. Runtime idempotency cannot adopt an Alert, Task, issue, or pull request created outside the exact Automation source.

## Lifecycle And Replacement

Automation lifecycle is independent from graph replacement:

- Saving an active Automation leaves it active.
- Saving a paused Automation leaves schedules and new root work paused.
- Saving an archived Automation leaves it archived and non-resumable.
- Save never bypasses explicit Resume or human approval.

Compiler-created Tasks remain non-admitted until the graph and resource bindings commit. Eligible active roots enter the existing worker queue only after commit. Removing or replacing Schedule nodes deletes obsolete exclusively owned Scheduler rows while preserving ordinary domain Tasks.

Maintained Native and GitHub setup registration copies the bundled template only when it first creates an Automation. It is not an upgrade path and stores no bundled-template version or migration state: later software releases and registration reruns leave every existing saved graph, resource binding, lifecycle state, and runtime projection unchanged. A user may explicitly Edit and Save their existing Automation, or delete it and create a new snapshot from the newer bundled template.

## Safety And Trust

The product earns trust through explicit intent and real resources:

- unsupported graphs fail before resource effects;
- successful Save creates or replaces one current graph atomically;
- failed replacement leaves current behavior active;
- failed Save rolls back without partial resources or persistent recovery state;
- project isolation applies to definitions, runtime bindings, and external objects;
- graph payloads omit secrets and private execution content;
- shared resources are allowed only where an adapter explicitly permits them;
- live reads do not call external providers on every refresh;
- no Automation node exposes the hidden Workflow subsystem.

## Representative Journeys

### Build From Blank

1. The user opens `New Automation` and selects `Blank`.
2. The browser shows an empty custom canvas without creating server resources.
3. The user adds a Schedule, Task, human gate, GitHub or Native action, and Outcome as needed.
4. The user connects only supported right-to-left handoffs and configures each node.
5. `Save changes` validates and immediately creates one Automation plus its required Tasks and Scheduler rows.
6. The user lands on the full-width Live graph and watches current state appear on its nodes.

### Edit While Work Is Running

1. The user opens Edit from a saved Automation.
2. The current graph is copied into browser memory.
3. Work continues against the current saved graph while the user edits.
4. Save atomically installs the replacement graph, removes the old Automation runtime projection, and preserves the Automation identity and lifecycle.
5. A failed Save leaves the prior graph active and no partial Save resources.

### Describe From Chat

1. The user describes supported automation behavior in project-aware Chat.
2. Chat displays a validated plan without an Automation identity, graph URL, or runtime resources.
3. The user later sends the exact Save confirmation.
4. OpenVibely applies the graph through the same deterministic services as the web builder.
5. Only successful Save returns the new Automation's Live URL.

## Non-Goals

Automation Graphs does not provide:

- arbitrary executable directed graphs or user-authored code execution;
- persisted editable Automation drafts;
- Automation graph versions, superseded graphs, version selection, or restoration;
- separate Definition, History, invocation, or work-item product pages;
- a replacement for Tasks, Scheduler, workers, queues, Alerts, GitHub services, or Workflows;
- Workflow nodes or hidden Workflow integration;
- heuristic discovery, migration, or graph backfill for existing resources;
- automatic merge, release, deployment, rollback, or approval authority;
- live provider calls on every graph refresh.

## Product Success

The vision is realized when users can:

1. Build supported custom graphs rather than choosing only preset topologies.
2. Configure and connect surfaced Schedule, Task/Agent, Native Alert, GitHub, and Outcome capabilities.
3. Save once and receive real resources or a clear no-effects validation error.
4. Replace the one current graph without creating public drafts or retained versions.
5. See current runtime state on a full-width Live graph and navigate to exact bound Tasks.
6. Trust that failed Saves roll back completely without duplicate or partial resources.
7. Trust project isolation and human review, merge, release, and deployment boundaries.
8. Use Template, Describe It, Blank, Edit, and Chat through one deterministic contract.
