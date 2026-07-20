# OpenVibely Automation Graphs Implementation

## Goal

Add project-scoped, dynamic graphs for OpenVibely autonomous SDLC automation.

The feature must support:

- multiple automations in one project;
- an automatically rendered graph for each explicitly published automation;
- a live view that shows where work is currently located;
- concurrent work at several nodes rather than one misleading global cursor;
- historical automation invocations and work-item traversal;
- reusable automation templates and, later, a visual automation builder;
- native Alerts-based and GitHub-based SDLC automation;
- human approval, PR review, merge, release, and deployment boundaries;
- project isolation and durable provenance.

The graph is a control-plane and observability surface over existing OpenVibely primitives. It must not become a second hidden workflow engine. Published automations must compile through a registered, deterministic automation adapter; graph edges are not interpreted by a new generic runtime.

## Product Model

Use this hierarchy:

```text
Project
└── Automation definitions
    └── Published automation versions
        ├── Trigger invocations
        └── Long-lived work items and transitions
                └── Tasks, executions, alerts, issues, goals, PRs, and reviews
```

Keep these concepts separate:

| Concept | Meaning |
| --- | --- |
| Automation definition | Stable user-facing identity, name, status, and ownership. |
| Automation version | Immutable published topology and node configuration. |
| Automation node | A trigger, agent task, human gate, action, condition, or outcome. |
| Automation edge | An allowed transition between two nodes. |
| Automation invocation | One occurrence of a fixed schedule, manual trigger, or other registered trigger. It is short-lived and records what caused processing. |
| Work item | One suggestion, issue, implementation stream, or other unit moving through the automation across any number of invocations. |
| Resource link | Provenance between a graph node/work item and a real OpenVibely or external object. |
| Transition | Durable evidence that a work item entered or left a node. |

An automation does not have one universal current position. One invocation may produce multiple suggestions, later invocations may process them, and work items may remain active across many invocations. The live graph therefore displays active work counts and states at every node.

## Product Surfaces

### All Automations

Add a project-scoped `Automations` page. It lists every automation as a card or compact mini-graph.

Each card shows:

- automation name and type;
- draft, active, paused, or archived lifecycle state;
- unknown, healthy, degraded, or unhealthy health state with a concise reason;
- current published version;
- last invocation and next scheduled invocation;
- running work count;
- pending human approval/review count;
- blocked/failed count;
- recently completed count;
- open action to inspect the full graph.

Do not render every task and execution from every automation on this page. The portfolio view should remain bounded and scannable.

### Single Automation

The automation detail page has three modes:

| Mode | Purpose |
| --- | --- |
| Live | Shows all current work plus currently executing invocations. |
| Invocation | Shows one selected trigger occurrence and the work it created or processed. |
| History | Lists/replays invocations and work-item transitions over time. |

Default to `Live` for an active automation.

Example:

```text
[Daily trigger] -> [Bug Finder] -> [Suggestion] -> [Human approval]
                                            approved |
                                                     v
                    [PR review] <- [Open PR] <- [Implementation]
```

Node overlays:

- blue/pulsing: currently executing;
- yellow: waiting for a human decision or review;
- red: failed or blocked;
- green: recently completed;
- gray: idle or not reached;
- numeric badges: active, waiting, blocked, failed, and completed work counts.

When a node contains mixed states, do not collapse it to one color and hide the mix. Render the highest-severity border plus explicit state counters.

Clicking a node opens a side panel with the actual resources at that node:

```text
Implementation · 2 active · 1 blocked

- Task abc: Add retry handling · running
- Task def: Improve task lineage · running
- Task ghi: Update provider adapter · blocked
```

Every resource should link to its native task, alert, execution, goal, issue, or PR view.

### Combined View

An optional project overview may collapse each automation into one node and show shared resources such as a central Dev Inbox:

```text
[Vision automation] ------\
[Bug finder automation] ---+--> [Shared Dev Inbox] --> [Implementation] --> [PR review]
[Security automation] ----/
```

Expand at most one or a small bounded number of automations at a time. A fully expanded cross-project graph will become unreadable and expensive.

### Definition And Builder Views

Keep definition authoring separate from runtime state:

- `Definition`: the published topology and configuration;
- `Edit`: browser-local changes to the published definition until Save;
- `Live`: runtime overlays for the published version;
- `History`: immutable invocations and transitions.

The initial release may render only registered definitions. Later releases can let users create an automation from a template or visual builder.

## Creating An Automation

The Automations page and Chat must use the same graph-generation, validation, publication-plan, and compilation services. They are two entry points into one automation-definition system, not separate implementations.

Creating an automation uses one shared candidate, plan, and compiler pipeline with surface-appropriate publication intent:

```text
Describe/select template
  -> generate graph candidate
  -> inspect/edit graph
  -> web: keep changes in browser memory until Save changes
     Chat: persist the displayed candidate for later confirmation
  -> validate and compute the concrete resource plan
  -> web: publish immediately from Save changes
     Chat: show the plan, then require a later explicit confirmation
  -> create/reuse resources and activate
  -> observe in Live view
```

Generating a candidate and changing canvas geometry must not create definitions, versions, tasks, schedules, alerts, issues, executions, or PRs on the Automations page. Refresh or navigation discards unsaved page edits. The visible `Save changes` action submits the complete candidate once and is the explicit request to validate and publish immediately. A short-lived unpublished version may be used internally during atomic publication, but it is not editable product state. In Chat, the displayed candidate remains persisted so a later user-authored input can confirm its exact publication plan; runtime resources change only after that confirmation.

### Automations Page Entry Point

Place a `New Automation` action on the project-scoped All Automations page.

It offers three paths:

| Path | Behavior |
| --- | --- |
| Use a template | Starts from a maintained Native SDLC, GitHub SDLC, Vision Driver, finder, audit, or documentation template. |
| Describe it | Accepts natural language and generates a constrained browser-local graph candidate. |
| Start blank | Opens an empty browser-local graph with the visual node palette. |

Recommend `Use a template` for common flows and make `Describe it` the primary flexible option. Starting blank is an advanced path, not a prerequisite for using automation.

### Template Creation Flow

After template selection, ask only for values required by that template. Example GitHub SDLC fields:

```text
Name: Product Improvement
Source files: VISION.md
Suggestion cadence: Daily
Inbox cadence: Hourly
Approval method: GitHub assignment
Implementation agent: Coding Agent
Open PR when complete: Yes
```

Generate a graph candidate from the template and project capabilities. Missing integrations remain validation errors or clearly marked unresolved configuration; do not silently substitute a different approval mechanism.

### Describe It Flow

The page presents a natural-language field such as:

```text
Every morning inspect the project for likely bugs. Create GitHub issues for
findings. Only start implementation after I assign an issue to the configured
inbox account. Open a pull request when implementation finishes.
```

Send the selected project, description, and available server-derived capabilities to `AutomationDraftService.GenerateFromDescription`. The service returns a structured draft using only supported node types and configuration fields.

#### Draft Generation Pipeline

`GenerateFromDescription` owns the complete generation pipeline for both the Automations page and Chat:

```text
Natural-language description
  -> build safe project capability snapshot
  -> provider-neutral internal model call with a strict graph schema
  -> decode structured graph candidate
  -> normalize IDs, roles, configuration, and layout
  -> deterministically validate topology and project references
  -> page: return an ephemeral browser-local candidate
     Chat: persist automation + unpublished version + nodes + edges
  -> return summary, assumptions, warnings, errors, and the surface-appropriate graph destination
```

The internal model call proposes a definition only. It receives no mutation tools and cannot create tasks, schedules, alerts, issues, executions, goals, or PRs.

Use the existing provider/model selection and usage-recording path for the internal generation call. Keep generation provider-neutral. Require schema-constrained structured output when the selected provider supports it; otherwise decode JSON and apply the same strict server validation. A malformed candidate may receive one bounded repair attempt containing validation errors, but generation must not enter an unbounded retry cycle.

#### Project Capability Snapshot

Build the snapshot server-side for the selected project. Include only information needed to produce a valid definition:

```json
{
  "project": {
    "id": "project-id",
    "name": "OpenVibely"
  },
  "supported_node_types": [
    "trigger",
    "agent_task",
    "human_gate",
    "action",
    "condition",
    "outcome"
  ],
  "supported_roles": [
    "bug_finder",
    "create_notification",
    "create_github_issue",
    "native_approval",
    "github_assignment",
    "implementation",
    "open_pull_request",
    "pull_request_review"
  ],
  "agents": [
    {"id": "agent-id", "name": "Coding Agent", "capabilities": ["tools", "workspace"]}
  ],
  "skills": [
    {"handle": "project-guidance", "name": "Project Guidance"}
  ],
  "integrations": {
    "github": {"configured": true, "approval_modes": ["assignment", "review"]},
    "native_alerts": {"configured": true, "approval_modes": ["alert_decision"]}
  },
  "source_files": ["VISION.md"],
  "reusable_resources": [
    {"type": "task", "id": "task-id", "name": "GitHub Dev Inbox"}
  ],
  "safety_boundaries": {
    "manual_merge": true,
    "manual_release": true,
    "manual_deploy": true
  }
}
```

This is a conceptual contract, not permission to expose raw configuration. Exclude credentials, tokens, prompts containing secrets, worktree contents, provider identity metadata, private message contents, and unbounded task/source listings. Bound and sort every collection deterministically.

The snapshot may state that a capability exists without exposing its secret configuration. The model can select a capability by stable ID/handle, but only the server can validate and use it.

#### Structured Draft Contract

The model returns a graph candidate matching a versioned schema. Example:

```json
{
  "schema_version": 1,
  "name": "Daily Bug Discovery",
  "description": "Find likely bugs, request approval, implement accepted work, and open PRs.",
  "automation_type": "github_sdlc",
  "nodes": [
    {
      "key": "daily_trigger",
      "name": "Daily Schedule",
      "type": "trigger",
      "role": "fixed_schedule",
      "config": {"repeat_type": "daily", "repeat_interval": 1}
    },
    {
      "key": "bug_finder",
      "name": "Bug Finder",
      "type": "agent_task",
      "role": "bug_finder",
      "config": {"agent_ref": "agent-id", "source_files": ["VISION.md"]}
    },
    {
      "key": "create_issue",
      "name": "Create GitHub Issue",
      "type": "action",
      "role": "create_github_issue",
      "config": {}
    },
    {
      "key": "assignment_gate",
      "name": "Human Assignment",
      "type": "human_gate",
      "role": "github_assignment",
      "config": {}
    },
    {
      "key": "implementation",
      "name": "Implementation Task",
      "type": "agent_task",
      "role": "implementation",
      "config": {"agent_ref": "agent-id"}
    },
    {
      "key": "open_pr",
      "name": "Open Pull Request",
      "type": "action",
      "role": "open_pull_request",
      "config": {}
    },
    {
      "key": "review_gate",
      "name": "Human Review",
      "type": "human_gate",
      "role": "pull_request_review",
      "config": {}
    }
  ],
  "edges": [
    {"key": "trigger_to_finder", "from": "daily_trigger", "to": "bug_finder"},
    {"key": "finder_to_issue", "from": "bug_finder", "to": "create_issue"},
    {"key": "issue_to_assignment", "from": "create_issue", "to": "assignment_gate"},
    {"key": "approved_to_implementation", "from": "assignment_gate", "to": "implementation", "condition": {"state": "approved"}},
    {"key": "implementation_to_pr", "from": "implementation", "to": "open_pr"},
    {"key": "pr_to_review", "from": "open_pr", "to": "review_gate"}
  ],
  "assumptions": ["GitHub assignment is the implementation approval signal."],
  "warnings": []
}
```

Do not accept model-generated database IDs, project IDs, resource URLs, executable code, SQL, arbitrary tool names, or unknown configuration keys. The server generates persistent IDs and resolves stable project-scoped references.

#### Normalization And Validation

After decoding, the server must:

1. verify the schema version and size limits;
2. map node types, roles, and configuration through an allowlist registry;
3. generate persistent IDs while preserving unique human-readable node keys;
4. resolve agent, skill, integration, source-file, and reusable-resource references inside the selected project;
5. calculate deterministic default positions when the candidate omits layout;
6. reject duplicate keys, dangling edges, unsupported cycles, unreachable required nodes, invalid conditions, and unknown fields;
7. verify that human approval/review requirements have explicit gate nodes;
8. record unresolved references as validation errors instead of silently changing behavior;
9. produce a canonical normalized definition used by page, Chat, planning, and compilation;
10. persist only after the candidate is structurally safe to store as a draft.

Draft persistence does not imply that the definition is publishable. Missing integrations or configuration may remain visible validation errors for the user to resolve in the builder.

Draft generation must:

- remain inside the selected project;
- resolve obvious existing agents, skills, channels, and source files where exact matches exist;
- mark ambiguous or missing dependencies instead of guessing;
- add an explicit human gate when the description requires approval/review;
- preserve manual merge, release, and deployment boundaries unless the user separately authorizes and configures them;
- reject requests that cannot be represented with supported nodes;
- return warnings and assumptions alongside the graph;
- never publish or execute the generated graph.

Example generated draft:

```text
[Daily Schedule]
       -> [Bug Finder]
       -> [Create GitHub Issue]
       -> [Human Assignment]
       -> [Dev Inbox]
       -> [Implementation Task]
       -> [Open Pull Request]
       -> [Human Review]
```

### Blank Builder Flow

`Start blank` opens an empty browser-local candidate using the registered `custom` compiler adapter. The user adds configurable OpenVibely capability nodes, connects them, and configures each selected node in a side panel. Preset adapters remain optional starting points, not required node lists for a blank graph. The custom adapter publishes only explicitly supported capability handoffs through existing OpenVibely services; unsupported node types, edges, cycles, and conditions may remain visible as unsaved validation errors but never execute or persist.

Example Agent Task panel:

```text
Name: Bug Finder
Agent: Project Architect
Instructions: Inspect one focused component for likely defects.
Skills: Project guidance
Task behavior: Create/reuse one visible task
```

Example Human Gate panel:

```text
Approval method:
- Native Alert approval
- GitHub issue assignment
- Pull request review
```

Node configuration controls must be schema-driven so template, described, blank, and Chat-created drafts share the same validation rules.

The builder must continuously show the selected compiler adapter and reject topology or handoff semantics that adapter cannot compile. The registered `custom` adapter accepts user-defined nodes and edges only when each node maps to an allowlisted OpenVibely capability and each edge maps to a supported handoff implemented through existing services. A graph with individually valid nodes is not publishable merely because its edges form a DAG.

### Validation And Publication

`Validate` checks at least:

- every required node is configured;
- every non-terminal node has a valid outgoing path;
- referenced agents, skills, source files, and integrations exist;
- recurrence nodes have valid schedule configuration;
- required human gates are present;
- every referenced resource belongs to the selected project;
- unsupported cycles and unreachable nodes are absent;
- one registered automation adapter accepts the complete topology and configuration;
- shared resources will not be unintentionally disabled or mutated;
- compilation targets existing OpenVibely capabilities.

Show validation failures on the affected nodes and in a summary panel.

After validation, the publication planner computes the concrete create/reuse/update plan before any resource mutation. The Automations page does not require a separate review/apply screen: `Save changes` submits the complete candidate, computes that plan, and immediately invokes the compiler when validation succeeds. Chat displays the same plan before accepting its later explicit confirmation.

On a successful web Save, navigate to the automation's `Live` view. If validation fails, return the submitted browser-local candidate with setup errors and create no Automation, version, or runtime resources. On partial compilation failure, retain the short-lived unpublished version and publication journal only as internal retry/reconciliation state, retain the prior published version, and report created/reused resources so an idempotent retry can reconcile them. Web Save and confirmed Chat publication both preserve stale-plan checks, immutable versions, and the same compiler journal.

### Editing An Active Automation

Editing an active automation reconstructs the published immutable version into browser-local state without cloning or writing a version. Existing invocations, activities, and work items retain their original immutable version references. Save validates the complete edited candidate, stages one internal version, and switches newly triggered invocations to it only after publication succeeds.

Do not mutate an active topology in place.

## Creating An Automation From Chat

The Chat page must support the same `Describe it` process with natural language:

```text
Create an automation that checks for likely bugs every morning, opens GitHub
issues, waits for my assignment approval, creates implementation tasks, and
opens PRs for review.
```

Chat uses a registered control-plane action, not ad hoc phrase parsing in the HTTP handler.

Recommended action:

```text
create_automation_draft
```

Input:

```json
{
  "name": "Daily Bug Discovery",
  "description": "Every morning inspect for likely bugs, open GitHub issues, wait for assignment approval, implement approved issues, and open PRs.",
  "creation_mode": "described"
}
```

The action derives `project_id` from the current Chat project and calls the same `AutomationDraftService.GenerateFromDescription` used by the Automations page. Do not accept a caller-supplied project ID that switches context.

The action returns a compact result:

```json
{
  "automation_id": "automation-id",
  "version_id": "draft-version-id",
  "status": "draft",
  "name": "Daily Bug Discovery",
  "summary": "Daily trigger -> Bug Finder -> GitHub Issue -> Assignment Gate -> Implementation -> PR Review",
  "warnings": [],
  "validation_errors": [],
  "url": "/automations/automation-id?view=definition&version=draft-version-id"
}
```

Chat responds with the draft summary, assumptions/warnings, and a link to inspect the graph. It must state that nothing is active yet.

Example:

```text
Created a draft automation: Daily Bug Discovery.

Daily trigger -> Bug Finder -> GitHub Issue -> Assignment Gate -> Implementation -> PR Review

Nothing has been scheduled or started. Review the graph and publish it when ready.
```

### Publishing From Chat

Users may complete the process without leaving Chat, but publication remains two-step:

1. `plan_automation_publication` validates the draft and returns the exact create/reuse/update plan.
2. Chat durably displays that plan and requests a host-recognized confirmation such as a `Publish automation` button or the exact reply `publish <automation-name>`.
3. After the assistant plan message is durably stored, the Chat host creates a confirmation receipt and short-lived signed `confirmation_token` bound to the project, automation, version, canonical plan revision, authenticated principal, Chat thread, and stored assistant plan-message ID. The model cannot issue this token itself.
4. Only a later user-authored input in that same thread that the Chat host deterministically records as the pending plan's affirmative confirmation may call `publish_automation_draft`. The action supplies both the signed token and that user-input ID; the backend verifies the input follows the plan message, belongs to the same authenticated principal, and carries the host's affirmative-confirmation marker.

Do not treat the initial description or `create an automation` request as approval of an unseen publication plan. A user may explicitly ask to generate a draft without publishing.

Recommended publication action inputs:

```json
{
  "automation_id": "automation-id",
  "version_id": "draft-version-id",
  "plan_revision": "opaque-plan-revision",
  "confirmation_token": "signed-short-lived-token",
  "confirming_user_input_id": "later-user-input-id"
}
```

The Chat host keeps the opaque token in pending-confirmation thread state and exposes it to the next-turn action context; it need not print the token to the user. Tokens expire after 30 minutes, are never logged, and require a fresh plan after expiry. Confirmation is a host control-plane decision, not model interpretation: buttons submit a structured confirmation, while text surfaces accept only the documented normalized command tied to the pending automation. Negative, unrelated, or ambiguous messages leave the receipt pending and cannot authorize the tool call. The Chat action executor verifies the token, affirmative marker, and input ordering. Reject a token used from another project, user, thread, automation, version, or plan; a confirmation from the same turn as planning; an expired token; and a stale plan. Consume the token and bind the confirming input transactionally when the publication attempt is created. A retry with the same consumed token may return the existing attempt/result but must never start a second attempt. The web page uses its authenticated confirmation POST plus CSRF protection and is not required to manufacture a Chat input ID.

The publication service must reject stale confirmations if the draft, dependencies, or plan changed after preview. On success, Chat returns the automation URL, created/reused tasks and schedules, and active status. It must not claim success unless those resources and the published version are persisted.

Chat action availability:

- draft creation and publication planning are available only on project-aware surfaces;
- publication is available only in mutation-capable/orchestration mode;
- read-only/plan mode may call `preview_automation_description` but must not persist a draft or publish it;
- ordinary task execution agents do not automatically receive automation-definition mutation actions;
- scheduled automation tasks cannot rewrite their own definition unless a separate explicit capability is granted.

Both page and Chat creation paths must use the same schema, capability snapshot builder, normalization, validation, and compilation services. Normalizing the same structured graph candidate must be deterministic after IDs and timestamps are removed. Do not test semantic equivalence between separate nondeterministic model generations; test both entry points with the same fixed structured candidate and assert identical normalized output and validation results.

### Chat Action Boundaries

The Chat actions have deliberately narrow responsibilities:

| Action | May persist definition data? | May create runtime resources? | Responsibility |
| --- | ---: | ---: | --- |
| `preview_automation_description` | No | No | Generate and validate an ephemeral graph candidate for read-only/plan mode. |
| `create_automation_draft` | Yes | No | Generate, normalize, validate, and persist an unpublished definition/version. |
| `plan_automation_publication` | No | No | Revalidate dependencies and return an exact create/reuse/update plan plus revision. |
| `publish_automation_draft` | Yes | Yes | After explicit confirmation, compile the confirmed plan and publish the version. |

During preview, draft creation, and publication planning, Chat must not call `create_task`, `edit_task`, `execute_tasks`, `schedule_task`, `modify_schedule`, `create_notification`, `github_create_issue`, `github_open_pull_request`, or equivalent mutation actions to assemble the automation incrementally.

The Chat model calls one automation control-plane action for each phase. The backend publisher then reuses the underlying task, schedule, alert, goal, and GitHub services directly and deterministically. It must not ask another model to perform a loose sequence of runtime tool calls.

Register a new `DomainAutomations` and these actions in the canonical Chat control registry (`internal/chatcontrol/registry.go`) with explicit mode/surface policies and JSON schemas. `preview_automation_description` and `plan_automation_publication` are read actions allowed in both modes on project-aware surfaces. Preview may consume model tokens and records usage through the existing usage path, but it does not mutate automation definitions or runtime resources. `create_automation_draft` and `publish_automation_draft` are write actions allowed only in orchestrate mode; publish also requires the signed confirmation-token and later-user-input contract. Wire handlers through the existing Chat action executor/handler map rather than introducing a second Chat tool dispatcher. Add the same capability filtering and truthful-persistence checks used by existing task actions.

Publication compilation order:

```text
Revalidate draft and plan revision
  -> reserve idempotency keys
  -> create/reuse visible tasks
  -> create/reuse schedules and approval configuration
  -> persist automation resource memberships
  -> publish immutable version
  -> emit automation invalidation event
  -> return persisted resource summary
```

If compilation crosses an external API boundary, use the external idempotency/reconciliation rules in this runbook. A failed publication must not be reported as active merely because some resources were created.

## Existing OpenVibely Primitives To Reuse

Reuse current persisted relationships:

- `schedules.task_id` connects a fixed schedule to a visible task;
- `tasks.parent_task_id` and lineage fields connect chained and swarm work;
- `tasks.swarm_role`, `swarm_status`, and `swarm_config` describe swarm topology;
- `executions.task_id` connects executions to tasks;
- `alerts.source_task_id` connects a native suggestion to its producer;
- `alerts.implementation_task_id` connects an approved suggestion to implementation;
- `alerts.decision_state`, `processing_state`, and timestamps describe human and automation gates;
- `task_goals.task_id` connects goal state to implementation work;
- `task_pull_requests.task_id`, issue fields, and PR state connect GitHub work;
- task events and the existing live-event SSE path provide invalidation signals;
- Chart.js on Analytics remains appropriate for time series, funnels, and aggregate metrics.

Never derive Automation identity from task titles, prompts, schedules, lineage, Alerts, GitHub relationships, or other resource heuristics. An Automation exists only after an explicit draft publication or maintained bootstrap registration persists its definition and memberships. Pre-feature resources receive no automatic detection, migration, or graph backfill.

## Boundary With Existing Workflows

OpenVibely already has `workflows`, `workflow_steps`, `workflow_executions`, and `step_executions` plus a bounded multi-agent Workflow engine. Keep that subsystem and Automations distinct:

| Existing Workflow | Automation |
| --- | --- |
| Bounded execution for one triggering task. | Long-lived coordination across schedules, tasks, approvals, issues, and PRs. |
| Engine interprets step dependencies, gates, votes, merges, and handoffs. | Registered adapters configure existing primitives; no generic edge interpreter. |
| Usually completes in one execution window. | Work items may remain active across days and many invocations. |
| `workflow_executions` and `step_executions` are authoritative runtime state. | Automation activities may link to a Workflow execution as one resource. |

An automation node may invoke or observe an existing Workflow as a bounded action when a registered adapter supports it. In that case, do not copy Workflow steps into automation nodes or duplicate Workflow execution state. Link the `workflow_execution` as an activity resource and project its aggregate status onto the automation node.

The automation builder supports both maintained preset adapters and a registered `custom` capability adapter:

```text
custom
native_sdlc
github_sdlc
vision_driver
scheduled_finder
```

Adding a new capability or handoff requires schema, validation, deterministic publication planning, compilation into an existing OpenVibely service, provenance hooks, and tests. The `custom` adapter does not interpret arbitrary edges at runtime: each accepted edge compiles to a specific existing task, Scheduler, Workflow, Alert, or GitHub mechanism. Unsupported graph semantics fail closed rather than creating a second Workflow engine, worker pool, or queue.

## Data Model

Use automation-specific tables for cross-time coordination while preserving existing Workflow tables for bounded Workflow execution. Names below are recommended and may be adjusted to migration conventions.

Every table that repeats project, automation, version, node, invocation, or work-item identity must enforce same-parent relationships with composite unique keys/foreign keys where SQLite supports them and repeat those checks transactionally in repository methods. Polymorphic external resource IDs still require repository validation.

### Definitions And Health

```sql
CREATE TABLE automations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    stable_key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    automation_type TEXT NOT NULL DEFAULT 'custom',
    lifecycle_state TEXT NOT NULL DEFAULT 'draft'
      CHECK (lifecycle_state IN ('draft', 'active', 'paused', 'archived')),
    health_state TEXT NOT NULL DEFAULT 'unknown'
      CHECK (health_state IN ('unknown', 'healthy', 'degraded', 'unhealthy')),
    health_reason TEXT NOT NULL DEFAULT '',
    health_evaluated_at DATETIME,
    published_version_id TEXT,
    created_via TEXT NOT NULL DEFAULT 'web',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at DATETIME,
    UNIQUE(project_id, stable_key),
    UNIQUE(id, project_id)
);
```

Lifecycle controls whether new triggers may start. Health describes observed operation and never implicitly pauses an active automation. `published_version_id` is verified as a same-automation published version in the publication transaction.

### Immutable Versions, Nodes, And Edges

```sql
CREATE TABLE automation_versions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'draft'
      CHECK (state IN ('draft', 'published', 'superseded')),
    source TEXT NOT NULL DEFAULT 'manual'
      CHECK (source IN ('manual', 'template', 'bootstrap')),
    adapter_key TEXT NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at DATETIME,
    FOREIGN KEY (automation_id, project_id)
      REFERENCES automations(id, project_id) ON DELETE CASCADE,
    UNIQUE(automation_id, version),
    UNIQUE(id, automation_id, project_id)
);

CREATE TABLE automation_nodes (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_key TEXT NOT NULL,
    name TEXT NOT NULL,
    node_type TEXT NOT NULL
      CHECK (node_type IN ('trigger','agent_task','human_gate','action','condition','outcome')),
    role TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}',
    position_x REAL NOT NULL DEFAULT 0,
    position_y REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, node_key),
    UNIQUE(id, version_id, automation_id, project_id)
);

CREATE TABLE automation_edges (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    source_node_id TEXT NOT NULL,
    target_node_id TEXT NOT NULL,
    edge_key TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    condition_json TEXT NOT NULL DEFAULT '{}',
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (source_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (target_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, edge_key),
    UNIQUE(id, version_id, automation_id, project_id),
    CHECK (source_node_id <> target_node_id)
);
```

Published versions are immutable by service policy. The application exposes no ordinary delete operation for a published version or its nodes. Internal foreign keys use cascading deletion so deleting a project still succeeds; ordinary history preservation is enforced by archive/no-delete service behavior and tests rather than conflicting `RESTRICT` actions.

Work items remain bound to their `origin_version_id` for their lifetime. Publishing a new version does not migrate in-flight work. A later invocation of version N+1 may discover/process a version N work item, but its work-item activities, positions, and transitions use version N nodes and adapter semantics. The invocation retains its own version N+1 identity. Stable `node_key` values allow compatible projection across versions; adapters must declare node-key mappings when keys change.

### Definition Resource Membership

Definition resources use a dedicated table with no nullable identity columns:

```sql
CREATE TABLE automation_definition_resources (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    resource_type TEXT NOT NULL
      CHECK (resource_type IN ('schedule','task','workflow','agent','skill','channel','source_file')),
    resource_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT 'owned',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, node_id, resource_type, resource_id, relation)
);

CREATE TABLE automation_trigger_owners (
    schedule_id TEXT PRIMARY KEY REFERENCES schedules(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    ownership_state TEXT NOT NULL DEFAULT 'active'
      CHECK (ownership_state IN ('active','paused')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE
);
```

Repository methods validate that local resources belong to the project. External identities are canonical and repository-qualified. `automation_trigger_owners.schedule_id` is the database-enforced exclusive current owner; the definition-resource table remains versioned history. Publishing claims the owner row in the same `BEGIN IMMEDIATE` transaction that activates the version. A primary-key conflict rejects concurrent ownership. Pausing retains the row with `ownership_state='paused'`; superseding or archiving disables the schedule before releasing or replacing its owner. Shared worker/inbox tasks remain allowed.

Do not mutate an old version's trigger schedule in place when cadence/topology changes. Publication creates a new exclusive schedule for the new version, atomically switches `published_version_id`, and disables the superseded owned trigger after the new resources are ready. Reusable worker/inbox tasks may remain linked to several versions. Publication preview must show every trigger created, enabled, or disabled.

### Publication Journal

Publication is a durable, resumable operation:

```sql
CREATE TABLE automation_publication_attempts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    plan_revision TEXT NOT NULL,
    status TEXT NOT NULL
      CHECK (status IN ('publishing','completed','failed')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    FOREIGN KEY (version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, plan_revision)
);

CREATE TABLE automation_publication_steps (
    id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL REFERENCES automation_publication_attempts(id) ON DELETE CASCADE,
    step_key TEXT NOT NULL,
    operation TEXT NOT NULL,
    target_key TEXT NOT NULL,
    status TEXT NOT NULL
      CHECK (status IN ('pending','running','completed','ambiguous','failed')),
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(attempt_id, step_key),
    UNIQUE(attempt_id, operation, target_key)
);

CREATE TABLE automation_chat_confirmation_receipts (
    token_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    plan_revision TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    plan_message_id TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    consumed_attempt_id TEXT REFERENCES automation_publication_attempts(id),
    confirming_user_input_id TEXT UNIQUE,
    confirmation_method TEXT NOT NULL DEFAULT ''
      CHECK (confirmation_method IN ('','button','command')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    consumed_at DATETIME,
    FOREIGN KEY (version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    CHECK ((consumed_at IS NULL AND consumed_attempt_id IS NULL AND confirming_user_input_id IS NULL AND confirmation_method = '') OR
           (consumed_at IS NOT NULL AND consumed_attempt_id IS NOT NULL AND confirming_user_input_id IS NOT NULL AND confirmation_method <> ''))
);
```

`plan_automation_publication` remains non-mutating with respect to definition and runtime resources. Chat delivery stores the confirmation receipt while issuing its signed token; this is security/audit state, not an automation draft or publication attempt. The token contains only the receipt ID plus an HMAC/signature and expiry; authoritative claims come from the receipt row.

Compute `plan_revision` with SHA-256 over canonical JSON containing exactly:

- the normalized draft schema version, adapter key, adapter implementation version, nodes, edges, and supported configuration fields, with maps sorted and IDs/timestamps/layout-only fields omitted;
- every referenced local dependency's type, ID, project ID, immutable/configuration revision, enabled/archived state, and ownership identity used by compilation;
- every referenced external integration's stable installation/account/repository identity and capability/configuration revision, never credentials;
- the compiler contract version and the exact ordered create/reuse/update/disable plan including stable target keys.

Exclude volatile operational fields such as `next_run`, `last_run`, health, activity status, counters, observation timestamps, and updated-at values that do not represent configuration. Encode timestamps and numbers canonically, reject non-finite numbers, and add golden hash fixtures so page and Chat compute identical revisions. Publishing rebuilds the canonical input under a transaction, rejects a stale confirmation before creating an attempt, and serializes publication of a version. The compiler persists each step before its mutation and records the resulting resource ID before advancing. Retries reconcile `running` or `ambiguous` steps by stable target key before creating anything new.

### Trigger Invocations And Dispatch

An invocation represents one trigger occurrence, not the lifetime of its work:

```sql
CREATE TABLE automation_invocations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    trigger_node_id TEXT NOT NULL,
    trigger_resource_type TEXT NOT NULL,
    trigger_resource_id TEXT NOT NULL,
    occurrence_key TEXT NOT NULL,
    scheduled_for DATETIME,
    status TEXT NOT NULL DEFAULT 'claimed'
      CHECK (status IN ('claimed','dispatched','running','completed','failed','cancelled','skipped')),
    skipped_reason TEXT NOT NULL DEFAULT '',
    started_at DATETIME,
    completed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    error_message TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (trigger_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    CHECK ((status = 'skipped' AND skipped_reason <> '') OR
           (status <> 'skipped' AND skipped_reason = '')),
    UNIQUE(automation_id, trigger_resource_type, trigger_resource_id, occurrence_key),
    UNIQUE(id, automation_id, project_id),
    UNIQUE(id, automation_id, version_id, project_id)
);

CREATE TABLE automation_dispatch_outbox (
    id TEXT PRIMARY KEY,
    invocation_id TEXT NOT NULL UNIQUE REFERENCES automation_invocations(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    execution_id TEXT UNIQUE REFERENCES executions(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending'
      CHECK (status IN ('pending','processing','submitted','completed','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    claimed_by TEXT NOT NULL DEFAULT '',
    claim_expires_at DATETIME,
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((status = 'processing' AND claimed_by <> '' AND claim_expires_at IS NOT NULL) OR
           (status <> 'processing' AND claimed_by = '' AND claim_expires_at IS NULL))
);

CREATE TABLE automation_task_run_reservations (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    dispatch_id TEXT NOT NULL UNIQUE
      REFERENCES automation_dispatch_outbox(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'reserved'
      CHECK (state IN ('reserved','claimed')),
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((state = 'reserved' AND lease_owner = '' AND lease_expires_at IS NULL) OR
           (state = 'claimed' AND lease_owner <> '' AND lease_expires_at IS NOT NULL))
);
```

For fixed schedules, `occurrence_key` is derived from the schedule ID and the exact due timestamp, not the scheduler's wall-clock processing time.

Reuse the same claimant/expiry semantics already used by `AlertRepo.ClaimApproved`: one compare-and-swap changes an eligible `pending` row, or an expired `processing` row, to `processing`, records `claimed_by`, increments `attempts`, and sets `claim_expires_at`. A claimant renews its lease while handing work to the existing worker pipeline. Failures use bounded exponential backoff through `next_attempt_at`; after the configured maximum attempts, mark the row failed, release its task reservation, and mark the invocation failed. A process may update or release only a lease it owns. Never select pending rows and claim them in separate transactions.

`automation_task_run_reservations` is a small coordination extension around the existing singleton task-status model, not another task queue. Its primary key prevents two schedules or automations from reserving the same shared task concurrently. Reservation creation uses the same transaction as invocation/outbox creation and schedule advancement. Terminal dispatch handling deletes the reservation. Expired reservations are recovered only after inspecting the outbox, execution, and existing task status.

Add a nullable `dispatch_id TEXT UNIQUE` column to the existing `executions` table and corresponding `Execution` model. The outbox row ID is the dispatch ID. Extend `TaskRepo` with one transactional automation-specific claim method that verifies the reservation and outbox lease, applies the existing pending-to-running task transition, inserts or resolves the execution by `dispatch_id`, and records `execution_id` before returning. `INSERT ... ON CONFLICT(dispatch_id) DO NOTHING` followed by a lookup must never reset an existing execution. Ordinary executions keep `dispatch_id` null.

### Long-Lived Work Items, Positions, Activities, And Transitions

Work items belong to an automation and originating version, with only an optional originating invocation:

```sql
CREATE TABLE automation_work_items (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    origin_version_id TEXT NOT NULL,
    origin_invocation_id TEXT,
    parent_work_item_id TEXT,
    work_item_key TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'work',
    title TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active'
      CHECK (status IN ('active','waiting','blocked','failed','completed','cancelled')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    FOREIGN KEY (origin_version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (origin_invocation_id, automation_id, origin_version_id, project_id)
      REFERENCES automation_invocations(id, automation_id, version_id, project_id),
    FOREIGN KEY (parent_work_item_id, automation_id, project_id)
      REFERENCES automation_work_items(id, automation_id, project_id),
    UNIQUE(automation_id, work_item_key),
    UNIQUE(id, automation_id, project_id),
    UNIQUE(id, automation_id, origin_version_id, project_id)
);

CREATE TABLE automation_work_item_positions (
    work_item_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    state TEXT NOT NULL
      CHECK (state IN ('active','waiting','blocked','failed')),
    entered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    PRIMARY KEY (work_item_id, node_id)
);

CREATE TABLE automation_thread_input_bindings (
    id TEXT PRIMARY KEY,
    thread_input_id TEXT NOT NULL REFERENCES thread_inputs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    invocation_id TEXT,
    work_item_id TEXT,
    binding_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (invocation_id, automation_id, project_id)
      REFERENCES automation_invocations(id, automation_id, project_id),
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id),
    CHECK (invocation_id IS NOT NULL OR work_item_id IS NOT NULL),
    UNIQUE(thread_input_id, binding_key)
);
```

Multiple active positions represent parallel branches. Registered adapters define fork/join rules and whether branching creates positions on one work item or explicit child work items. The generic graph service only projects persisted positions; it never invents join semantics.

`thread_inputs` remains the only durable queue for Chat and shared inbox follow-ups. Automation code must not introduce a parallel inbox queue. Persist only causal graph metadata in `automation_thread_input_bindings`. Extend the existing `ThreadInputRepo.ClaimQueuedForTaskExecution` transaction so it loads these bindings, creates/resolves the execution through its existing guarded-promotion path, and upserts the corresponding automation activities. Cancellation, retargeting, stale-turn recovery, and remaining-input guard updates continue to use existing `ThreadInputRepo` behavior.

Every processing action creates an activity. One invocation may process many existing work items, and one work item may be processed by many invocations:

```sql
CREATE TABLE automation_activities (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    invocation_id TEXT,
    work_item_id TEXT,
    activity_key TEXT NOT NULL,
    activity_type TEXT NOT NULL,
    status TEXT NOT NULL
      CHECK (status IN ('pending','running','waiting','completed','failed','cancelled')),
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    error_message TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (invocation_id, automation_id, project_id)
      REFERENCES automation_invocations(id, automation_id, project_id),
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id),
    CHECK (invocation_id IS NOT NULL OR work_item_id IS NOT NULL),
    UNIQUE(automation_id, version_id, activity_key),
    UNIQUE(id, automation_id, project_id),
    UNIQUE(id, version_id, automation_id, project_id)
);

CREATE TABLE automation_activity_resources (
    id TEXT PRIMARY KEY,
    activity_id TEXT NOT NULL REFERENCES automation_activities(id) ON DELETE CASCADE,
    resource_type TEXT NOT NULL
      CHECK (resource_type IN ('task','execution','alert','goal','github_issue','pull_request','review','workflow_execution')),
    resource_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT 'subject',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(activity_id, resource_type, resource_id, relation)
);

CREATE TABLE automation_transitions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    work_item_id TEXT NOT NULL,
    invocation_id TEXT,
    activity_id TEXT,
    from_node_id TEXT,
    to_node_id TEXT NOT NULL,
    edge_id TEXT,
    event_key TEXT NOT NULL,
    state TEXT NOT NULL
      CHECK (state IN ('entered','waiting','completed','failed','blocked','cancelled')),
    metadata_json TEXT NOT NULL DEFAULT '{}',
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (invocation_id, automation_id, project_id)
      REFERENCES automation_invocations(id, automation_id, project_id),
    FOREIGN KEY (activity_id, version_id, automation_id, project_id)
      REFERENCES automation_activities(id, version_id, automation_id, project_id),
    FOREIGN KEY (from_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id),
    FOREIGN KEY (to_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (edge_id, version_id, automation_id, project_id)
      REFERENCES automation_edges(id, version_id, automation_id, project_id),
    UNIQUE(automation_id, version_id, event_key)
);
```

The SQL above is the required ownership model, including nullable composite foreign keys. Create or rebuild objects in this dependency order: add `executions.dispatch_id`; then create automations, versions, nodes, edges, definition resources, trigger owners, publication attempts, publication steps, Chat confirmation receipts, invocations, dispatch outbox, task-run reservations, work items, positions, thread-input bindings, activities, activity resources, and transitions; then add indexes. Rebuild existing tables during migration if SQLite cannot add a required constraint in place. Keep foreign keys enabled in migration tests. For nullable references, the nullable ID (`origin_invocation_id`, `parent_work_item_id`, `invocation_id`, `activity_id`, `from_node_id`, or `edge_id`) alone determines absence while the shared project/automation/version columns remain populated; repositories reject malformed or mismatched referenced IDs.

Each registered adapter defines canonical, project-scoped keys before it may emit runtime state:

- `work_item_key`: immutable source identity such as `alert:<alert-id>`, `github:<installation>:<repo-id>:issue:<number>`, or `invocation:<id>:finding:<stable-finding-key>`;
- `activity_key`: causal logical-operation identity such as `dispatch:<dispatch-id>:execute`, `alert:<alert-id>:approve`, or `work-item:<id>:open-pr:<intent-revision>`; transport retry numbers are excluded;
- `event_key`: immutable domain-event identity plus destination/state, such as `alert:<alert-id>:decision:<decision-revision>:approved`.

Adapters reserve/upsert these keys in the same transaction as local effects. Retries return the existing row and may advance its status but cannot append a duplicate transition. Random UUIDs, timestamps, titles, prompts, and response-write attempts are never idempotency keys.

Transitions are append-only. Work-item status and active positions are Automation projections updated in the same transaction as each transition. They never become the source of truth for a task, execution, Alert, goal, Workflow execution, queued input, issue, PR, or review. Those existing domain records remain authoritative; adapters translate their committed changes into idempotent automation activities/transitions, and the reconciler repairs only missing or stale Automation projections. Archive and version supersession delete no history. Project deletion cascades all automation metadata and is covered by an integration test.

Required query indexes include:

```sql
CREATE INDEX idx_automations_project_lifecycle
  ON automations(project_id, lifecycle_state, updated_at DESC);
CREATE INDEX idx_automation_versions_parent
  ON automation_versions(automation_id, state, version DESC);
CREATE INDEX idx_automation_definition_resources_reverse
  ON automation_definition_resources(project_id, resource_type, resource_id);
CREATE INDEX idx_automation_trigger_owners_parent
  ON automation_trigger_owners(automation_id, ownership_state, version_id);
CREATE INDEX idx_automation_invocations_history
  ON automation_invocations(automation_id,
    COALESCE(scheduled_for, started_at, created_at) DESC, id);
CREATE INDEX idx_automation_invocations_status
  ON automation_invocations(project_id, status, updated_at);
CREATE INDEX idx_automation_dispatch_pending
  ON automation_dispatch_outbox(status, next_attempt_at, claim_expires_at);
CREATE INDEX idx_automation_task_reservation_lease
  ON automation_task_run_reservations(state, lease_expires_at);
CREATE INDEX idx_automation_work_items_live
  ON automation_work_items(automation_id, status, updated_at DESC, id);
CREATE INDEX idx_automation_positions_node
  ON automation_work_item_positions(automation_id, version_id, node_id, state);
CREATE INDEX idx_automation_input_bindings_input
  ON automation_thread_input_bindings(thread_input_id, automation_id);
CREATE INDEX idx_automation_activities_invocation
  ON automation_activities(invocation_id, started_at, id);
CREATE INDEX idx_automation_activities_work_item
  ON automation_activities(work_item_id, started_at, id);
CREATE INDEX idx_automation_activity_resources_reverse
  ON automation_activity_resources(resource_type, resource_id);
CREATE INDEX idx_automation_transitions_work_item
  ON automation_transitions(work_item_id, occurred_at, id);
CREATE INDEX idx_automation_transitions_node
  ON automation_transitions(automation_id, version_id, to_node_id, occurred_at DESC);
```

## Runtime Provenance

Introduce an internal context capable of carrying more than one causal binding:

```go
type AutomationContext struct {
    ProjectID string
    Bindings  []AutomationBinding
}

type AutomationBinding struct {
    AutomationID string
    VersionID    string // topology version for this binding; work-item version once WorkItemID is set
    InvocationID string // optional for callbacks not caused by a trigger invocation
    NodeID       string
    WorkItemID   string // optional until an inbox/finder resolves a specific item
}

type AutomationDispatchEnvelope struct {
    DispatchID string
    Task       models.Task
    Context    AutomationContext
}
```

This context is server-derived. Runtime tools and prompts must not be allowed to switch projects, automations, invocations, or work items by supplying arbitrary IDs. Persist bindings on the automation outbox, `automation_thread_input_bindings`, and activities; never rely on in-memory context alone and never use an Automation-specific replacement for `thread_inputs`.

Propagation rules:

1. A due, exclusive automation trigger is claimed by inserting its unique invocation and dispatch-outbox row while advancing the schedule occurrence in one transaction.
2. The outbox dispatcher submits the task with the invocation binding; the execution is linked through an activity.
3. A suggestion-producing action creates an automation-level work item whose `origin_invocation_id` is that finder invocation.
4. A later inbox invocation may resolve and process that existing work item without changing its origin.
5. Human approval/assignment appends a transition and updates active positions transactionally.
6. Implementation task creation links the new task and execution activities to the existing work item.
7. Child/chained/swarm work inherits all applicable bindings; a registered adapter creates child work items or parallel positions according to its declared fork semantics.
8. PR creation links the PR activity to the work item and transitions it to review.
9. Completion, rejection, failure, or cancellation records terminal positions/transitions and recomputes overall work-item health.

Shared worker/inbox tasks are definition resources, not causal identity. Use the invocation, queued input, alert claim, canonical GitHub issue, or parent activity that caused work. One inbox execution may produce one activity per processed work item and may carry bindings from several automations. Active trigger schedules are not shared in the first release.

Before a work item is resolved, a binding uses the invocation version. After resolution, each work-item activity binding uses that work item's immutable topology version even if the processing invocation was created from a newer published version.

`AutomationDispatcher` is a durable adapter into the existing `WorkerService`; it owns no goroutine pool, task executor, lifecycle hook runner, model invocation path, or completion semantics of its own. After leasing an outbox row, it asks the new transactional `TaskRepo.ClaimAutomationDispatch` method to consume the matching reservation, claim the existing task, and precreate/resolve the dispatch execution. It then calls a narrow `WorkerService.SubmitPrepared` entry point carrying `AutomationDispatchEnvelope` plus that execution ID. `SubmitPrepared` uses the existing worker limits, cancellation, lifecycle hooks, LLM/tool execution, event publication, and completion path while skipping only the already-completed task-claim and execution-create steps.

If the process crashes after the database claim but before in-memory submission, the outbox/reservation lease expires and reconciliation resubmits the same prepared execution. If a different task path already owns the task, the automation claim fails without resetting it; the invocation is coalesced/skipped according to the overlap policy and its reservation is released. Occurrence identity comes from the unique invocation row, execution identity from the unique dispatch ID, and task concurrency remains owned by the existing task status transition.

### Atomic Trigger Claim And Dispatch

The current scheduler lists due schedules, submits a task, and marks the schedule afterward. Automation triggers require a new transactional claim path; wrapping only invocation creation is insufficient.

For an automation-owned fixed trigger:

```text
BEGIN IMMEDIATE
  verify schedule is enabled and still due at expected_due_at
  read the target task status and task-run reservation while holding the write transaction
  if task is already running or has a nonexpired reservation:
    insert one terminal skipped invocation for expected_due_at with reason
      task_running or task_reserved; set started_at and completed_at to transaction time
      ON CONFLICT return existing invocation
    advance schedule from expected_due_at to its first recurrence strictly after now
      using expected_due_at compare-and-swap
    create no outbox row
    COMMIT and stop
  insert automation_invocation using schedule_id + expected_due_at occurrence key
    ON CONFLICT return existing invocation
  insert automation_dispatch_outbox for that invocation
    ON CONFLICT return existing dispatch
  insert automation_task_run_reservation keyed by task_id and dispatch_id
    ON CONFLICT treat occurrence as skipped and roll back its outbox
  apply the existing recurring-schedule task eligibility/category reset rules
  advance schedule last_run/next_run with expected_due_at compare-and-swap
COMMIT

outbox dispatcher atomically leases eligible pending/expired-processing dispatch
  -> TaskRepo.ClaimAutomationDispatch atomically validates reservation,
     claims existing task, and upserts execution by unique dispatch_id
  -> WorkerService.SubmitPrepared uses existing worker/lifecycle/LLM pipeline
  -> worker upserts execution activity by dispatch-derived activity_key
  -> mark dispatch submitted/completed
```

If schedule advancement loses the compare-and-swap, roll back the invocation, outbox, and reservation. If the process crashes after task claim or submission, retrying the same leased outbox row resolves `executions.dispatch_id` and the dispatch-derived activity key rather than creating duplicate work. Heartbeats extend only the claimant's lease; terminal execution completion updates the invocation/outbox and deletes the reservation in one transaction before publishing SSE invalidation.

The first release uses coalescing when an automation schedule's task is already running or reserved. It records exactly one terminal `skipped` invocation for the due timestamp, with `skipped_reason='task_running'` or `task_reserved` and `started_at=completed_at=transaction_time`, creates no dispatch, and advances recurring schedules to the first cadence boundary strictly after transaction time. It does not backfill every missed cadence. For a one-time schedule, `next_run` becomes null after recording the skipped occurrence. Invocation history orders by `COALESCE(scheduled_for, started_at, created_at), id`, so skipped and nonscheduled invocations have stable cursors. The skipped row is visible in history and metrics but contributes no active work or node position. This policy applies only to automation-owned schedules; changing it later requires an explicit per-automation overlap policy and migration.

Non-automation schedules continue using current behavior until intentionally migrated. The new claim method must not change unrelated schedule semantics.

## Supported Explicit Automation Registrations

### Native Alerts SDLC

Register a definition with nodes similar to:

```text
Schedule -> Suggestion Producer -> Pending Notification -> Human Approval
                                                rejected -> Rejected Outcome
                                                approved -> Approved Inbox
                                                         -> Implementation Task
                                                         -> Execution/Goal
                                                         -> PR/Review (optional)
                                                         -> Completed Outcome
```

Use exact `source_task_id`, decision state, processing state, claimant, implementation task ID, execution ID, and timestamps. This path already has strong local provenance and should be the first complete implementation.

### GitHub SDLC

Register Offering Manager, Bug Finder, Optimization Finder, Redundancy Finder, Dev Inbox, implementation, PR review, and Auditor nodes as applicable.

The current producer-task-to-created-issue relationship may not be durable enough for exact graph reconstruction. Update successful GitHub issue creation to record:

- repository identity;
- issue number and URL;
- creating task and execution;
- automation/invocation/node/work-item context.

Do not rely on searching task prompts for issue URLs as the canonical link.

GitHub assignment by the configured inbox identity remains the default human approval signal. PR review and merge remain human-controlled unless a separately authorized workflow says otherwise.

### No Legacy Discovery Or Backfill

Do not scan or infer Automations from resources created before this feature. Existing tasks, schedules, Alerts, task lineage, issues, and pull requests remain usable through their native OpenVibely surfaces but do not appear as an Automation unless the user creates and publishes a new Automation. There is no compatibility migration, confidence-scored preview, prompt/title matching, or automatic membership backfill.

## Automation Registration In Bootstrap Flows

Update native and GitHub autonomous SDLC bootstrap implementations so successful setup creates or updates a graph definition in the same project.

Registration must be idempotent through a stable project-scoped key, for example:

```text
native-sdlc/default
github-sdlc/default
```

The bootstrap should:

1. create or reconcile visible tasks and schedules using existing behavior;
2. create a draft automation version;
3. create nodes and edges from a maintained server-side/template definition;
4. link the actual task and schedule IDs to nodes;
5. validate the graph and project scope;
6. publish the version only after all required resources exist;
7. leave the previous published version active if reconciliation fails;
8. report the automation URL and any unlinked optional nodes.

Do not embed the only copy of graph topology in an LLM prompt. Maintain canonical templates in code or structured built-in-skill support assets.

Current prompt-driven bootstrap skills need one bounded registration action after they create/reconcile their visible resources:

```text
register_automation_resources
```

Input contains only a registered `adapter_key`, stable automation key, and actual project resource bindings by ID/node key. It does not accept arbitrary topology JSON. The server loads the canonical adapter template, validates every resource in the executing task's project, creates/publishes the definition idempotently, and returns the automation URL. Expose this action only to the maintained native/GitHub bootstrap capability path, not ordinary scheduled tasks. Phase 4 publication calls the same registration service internally after compilation rather than invoking this runtime action.

## Live State Projection

Build a `AutomationGraphService` that returns topology and a bounded runtime projection.

For every node return:

```json
{
  "id": "node-id",
  "key": "implementation",
  "name": "Implementation",
  "type": "agent_task",
  "role": "implementation",
  "position": {"x": 620, "y": 240},
  "counts": {
    "running": 2,
    "waiting": 0,
    "blocked": 1,
    "failed": 0,
    "completed_recently": 3
  },
  "display_state": "blocked"
}
```

Recommended display-state precedence:

```text
failed > blocked > waiting_human > running > recently_completed > idle
```

Counters remain authoritative; display state only chooses the primary border/animation.

For every edge return current and date-range transition counts. Highlight an edge when a work item traversed it recently or is waiting immediately after it.

The Live view uses the current published topology as its primary graph. Older-version positions are aggregated onto nodes only when the adapter declares a compatible stable `node_key` mapping. Unmapped positions appear in a visible `Legacy work (vN)` group and remain drillable; never silently drop them or attach them to a guessed node.

Define `recently_completed` using a server-provided cutoff so all clients agree. Do not make persisted status depend on a browser clock.

Health is a separately computed projection. Initial semantics:

```text
unknown   no completed invocation yet
healthy   triggers/dispatch succeed and no systemic adapter/integration error is active
degraded  isolated recent failures, blocked work, or stale external state
unhealthy repeated trigger/dispatch/compiler failure or required integration unavailable
```

Health never changes lifecycle state automatically. Show the reason and last evaluation time.

### Invocation And Work-Item Completion

An invocation completes when its dispatched execution and invocation-owned activities are terminal. It does not remain open merely because a suggestion it created is waiting for human approval.

A work item completes only when its registered adapter reports a terminal outcome and it has no nonterminal active positions, pending activities, claims, or queued inputs. Human-gated work remains `waiting` even after the invocation that created it has completed.

Use reconciliation to repair invocation, activity, position, and work-item projections after crashes. Never treat invocation completion as work-item completion.

## API

Recommended project-scoped endpoints:

```text
GET    /api/projects/:project_id/automations
POST   /api/projects/:project_id/automations
POST   /api/projects/:project_id/automations/preview
POST   /api/projects/:project_id/automations/drafts:generate
GET    /api/projects/:project_id/automations/:automation_id
PATCH  /api/projects/:project_id/automations/:automation_id
POST   /api/projects/:project_id/automations/:automation_id/pause
POST   /api/projects/:project_id/automations/:automation_id/resume
POST   /api/projects/:project_id/automations/:automation_id/archive

POST   /api/projects/:project_id/automations/:automation_id/drafts
PUT    /api/projects/:project_id/automations/:automation_id/drafts/:version_id
POST   /api/projects/:project_id/automations/:automation_id/drafts/:version_id/validate
POST   /api/projects/:project_id/automations/:automation_id/drafts/:version_id/publication-plan
POST   /api/projects/:project_id/automations/:automation_id/drafts/:version_id/publish

GET    /api/projects/:project_id/automations/:automation_id/graph?mode=live
GET    /api/projects/:project_id/automations/:automation_id/invocations?limit=50&cursor=...
GET    /api/projects/:project_id/automations/:automation_id/invocations/:invocation_id/graph
GET    /api/projects/:project_id/automations/:automation_id/work-items?status=active&limit=50&cursor=...
GET    /api/projects/:project_id/automations/:automation_id/work-items/:work_item_id/transitions
GET    /api/projects/:project_id/automations/:automation_id/nodes/:node_id/resources
```

All automation ID lookups must include the request's project ID in SQL or validate ownership before returning data. A known foreign-project automation, node, invocation, work-item, activity, or resource-link ID must return not found/forbidden according to existing API conventions.

Bound node-resource and transition results. Do not return full prompts, execution output, diffs, or alert bodies in the graph payload. Fetch details from existing resource endpoints after the user opens an item.

Use stable cursor pagination for invocations, work items, activities, and transitions.

`preview` performs ephemeral generation for read-only/plan mode. `drafts:generate` persists a draft for the Automations-page and orchestrating Chat `Describe it` paths through the same service. The publication-plan response includes an opaque plan revision that `publish` must verify before mutating resources. Chat additionally requires its signed confirmation token and a later confirming input ID; direct web publication requires the authenticated confirmation request and CSRF protection.

## Live Updates

Reuse the existing task broadcaster and live-event SSE connection for invalidation. Add automation-aware event types such as:

```text
automation_definition_updated
automation_invocation_started
automation_invocation_updated
automation_work_item_updated
automation_transition_created
automation_resource_linked
```

Event payloads should contain only identifiers and compact state needed to invalidate/refetch:

```json
{
  "type": "automation_work_item_updated",
  "project_id": "project-id",
  "automation_id": "automation-id",
  "invocation_id": "invocation-id",
  "work_item_id": "work-item-id",
  "node_id": "node-id",
  "status": "waiting"
}
```

The page should debounce events and refetch the compact graph projection rather than attempting to reconstruct authoritative state from possibly dropped SSE messages.

Also refetch the local projection periodically while the page is visible, for example every 15-30 seconds, and refresh after returning from a hidden tab. A graph read must not call GitHub. External state enters the local projection only through existing visible scheduled tasks, persisted generic webhooks when configured, or an explicit rate-limited user refresh. Show the last persisted external-update time and stale state; cache explicit refresh results and honor provider rate limits.

Do not stream model token deltas into the graph. Execution start/status/terminal events are sufficient.

## Graph Rendering

Chart.js remains for aggregate charts but is not the right abstraction for an interactive directed topology.

Use a directed-graph renderer such as Cytoscape.js, vendored or bundled with the application rather than loaded only from a public CDN. Desktop/Wails and offline use must continue to work. A small SVG implementation is acceptable only if it supports pan, zoom, selection, accessible node controls, stable positioning, and bounded performance without duplicating layout logic.

Rendering requirements:

- deterministic layout for template graphs;
- persisted user positions per version;
- fit-to-view and reset controls;
- pan and zoom;
- keyboard-focusable nodes;
- non-color status labels/icons;
- responsive narrow-screen fallback;
- node side panel instead of huge labels;
- collapsed groups for repeated implementation work;
- no full task/execution transcript in graph DOM;
- bounded animation that respects reduced-motion preferences;
- graph instance cleanup on HTMX navigation.

Use the `.templ` source files as authoritative and regenerate corresponding `*_templ.go` files. Do not hand-edit generated template output.

## Visual Automation Builder

The builder is a later phase built on the same definition/version tables.

Supported node types should be constrained and validated:

| Node type | Example compilation target |
| --- | --- |
| Fixed trigger | `schedules` row attached to a visible task. |
| Agent task | Visible OpenVibely task plus prompt, agent, and skills. |
| Human gate | Native Alert approval or GitHub assignment/review policy. |
| Create suggestion | `create_notification` or `github_create_issue`. |
| Create implementation | Alert-linked or issue-linked task creation. |
| Goal continuation | Persisted task goal where explicitly appropriate. |
| Child work | Task chaining or swarm configuration. |
| Open PR | Existing task PR operation. |
| Outcome | Graph-only terminal classification backed by resource state. |

The Automations-page builder keeps canvas/node/edge mutations in browser memory until the user selects `Save changes`; refresh or navigation discards them. That one action submits the complete graph, computes the deterministic publication plan, and immediately creates or updates resources and publishes one immutable version when validation succeeds. There is no persisted editable web draft, separate web confirmation plan, or Apply step. Chat continues to persist its displayed candidate and confirmation plan before compilation.

If resource creation partially fails, keep the draft unpublished and report exact created/reused resources; use idempotency keys to make retry safe.

For the custom builder, support only capability nodes and handoffs with deterministic compilers into existing OpenVibely services. Do not promise arbitrary workflow expressions, automations within automations, automatic rollback, or generic code execution. Recurrence remains owned by fixed schedules. Dynamic wakeups may be offered only when separately persisted through the capability registry; the custom adapter must not invent them. Work remains owned by tasks, existing Workflow/Alert/GitHub services, and existing runtime tools.

## Service And Repository Shape

Recommended packages/files:

```text
internal/models/automation.go
internal/repository/automation_repo.go
internal/service/automation_service.go
internal/service/automation_draft_service.go
internal/service/automation_adapter_registry.go
internal/service/automation_compiler.go
internal/service/automation_dispatcher.go
internal/service/automation_graph_service.go
internal/service/automation_registration.go
internal/service/automation_reconciler.go
internal/handler/automation_handler.go
web/templates/pages/automations.templ
web/templates/pages/automation_detail.templ
web/templates/components/automation_graph.templ
```

Responsibilities:

| Component | Responsibility |
| --- | --- |
| Repository | Project-scoped CRUD, immutable transition append, transactional projections, bounded queries. |
| Automation service | Definition validation, versioning, publish/pause/archive, resource membership. |
| Draft service | Normalize template, described, blank, and Chat inputs into one validated graph schema. |
| Adapter registry | Own supported topology/config schemas, validation, publication planning, compilation, and fork/join semantics. |
| Compiler | Produce publication plans and call existing task, schedule, Alert, goal, Workflow, and GitHub service/repository APIs to create or reuse resources after confirmation. |
| Dispatcher | Lease durable automation occurrences and adapt them into the existing `WorkerService`; it is not a second worker pool or execution engine. |
| Graph service | Assemble topology, live counters, invocation view, work-item view, and resource summaries. |
| Registration | Idempotently register bootstrap-created automations and actual resources. |
| Reconciler | Read existing domain repositories and repair only automation invocation/activity/position/work-item projections; it does not overwrite task, Alert, goal, Workflow, execution, or PR state. |
| Handler | Authorization, request parsing, HTTP/SSE responses, page rendering. |

Do not let handlers construct graph state with many per-node repository calls. Use bounded set-based queries and assemble the projection in the service.

The compiler may write automation definition, publication-journal, membership, and provenance tables directly through `AutomationRepo`. For domain resources it must use the current behavior-owning boundary:

- `TaskService`/`TaskRepo` for visible task creation, category/status rules, lineage, chain, and swarm configuration;
- `ScheduleRepo` plus the existing `Schedule.ComputeNextRun` cadence rules for schedules until a dedicated schedule service exists;
- `AlertService` for actionable Alerts, decisions, claims, implementation-task linkage, and Alert events;
- existing goal service/repository methods for task goals;
- `WorkflowService` for bounded Workflow execution and status;
- `GitHubService` and existing task-PR/feedback repositories for issue, PR, review, and merge state;
- the existing `events.Broadcaster` and SSE handler for post-commit invalidation.

Do not duplicate validation, task activation, alert claiming, queued-input promotion, GitHub authorization, Workflow execution, event publication, or task completion logic inside an adapter or compiler. When an existing operation lacks the transaction boundary Automation needs, extract or extend that operation behind its current service/repository rather than inserting the domain row from `AutomationCompiler`.

## State Mutation And Transactions

Operations that create a resource and advance a work item must be atomic where both resources are local.

Example native approval flow:

```text
BEGIN
  claim approved alert
  create/reuse implementation task
  create implementation activity for existing work item
  link alert and task as activity resources
  append transition
  update work-item positions/status projection
COMMIT
publish compact automation event
```

External GitHub calls cannot share a database transaction. Use an idempotency key and this sequence:

1. create/reserve the local work item and intended action;
2. call GitHub with stable identifying metadata where supported;
3. persist the returned canonical issue/PR identity and transition;
4. on ambiguous failure, reconcile before retrying;
5. never create a duplicate merely because the local response write failed.

Publish SSE events only after commit.

## Multiple-Automation Semantics

- A project may contain any bounded number of active, paused, draft, and archived automations.
- A worker/inbox task may be definition-linked to several automations. An active trigger schedule is exclusive to one automation in the first release.
- A runtime execution may have several persisted automation bindings when a shared inbox intentionally processes work items from multiple automations.
- Shared resource state may appear in multiple automation graphs, but each graph's transition history remains separate.
- Pausing one automation disables only its exclusively owned trigger schedules. It does not disable shared worker/inbox tasks or unrelated schedules.
- Archiving an automation removes no tasks, schedules, alerts, issues, PRs, executions, or history.
- Deleting a project cascades automation metadata with the rest of the project.

Set a sensible per-project graph limit and payload/node limit. Report overflow with filters/collapsed groups rather than trying to render an unbounded graph.

## Security And Safety

- Enforce project scope in every definition, version, node, edge, invocation, activity, work-item, transition, and resource-link query.
- Derive automation context server-side from persisted triggers and resources.
- Treat graph editing as configuration authority, not merge/release/deploy authority.
- Preserve existing human approval and GitHub assignment/review boundaries.
- Escape node names, resource summaries, external titles, and error messages.
- Do not render raw prompt/output HTML in graph labels or tooltips.
- Do not expose credentials, tokens, local worktree secrets, or provider identity metadata.
- Reject cross-project node edges and resource membership.
- Validate JSON configuration sizes and known fields.
- Keep audit transitions append-only.

## Performance

Target the live graph payload rather than full resource records.

Initial practical limits:

- at most 100 definition nodes per automation version;
- at most 200 definition edges;
- aggregate repeated work into node counters;
- return at most 50 recent resources per selected node with pagination;
- return at most 50 invocations on the initial history page;
- paginate transition history;
- debounce SSE-driven refetches;
- cancel or ignore stale graph fetches after automation/project navigation;
- destroy renderer instances on HTMX swaps.

Use set-based SQL grouped by node/status. Avoid one query per node or work item.

## Implementation Phases

### Phase 1: Read-Only Registered Automation Graphs

1. Add definition, immutable-version, node, edge, definition-resource, and exclusive trigger-owner models/tables.
2. Implement project-scoped repositories, adapter registry, validation, and atomic publication for explicitly registered resources created by updated supported setup flows.
3. Add idempotent native and GitHub bootstrap registration using canonical adapters/templates.
4. Render the All Automations page and individual Automation Definition views.
5. Build resource summaries from current persisted state.
6. Do not add user draft editing or resource compilation yet.

Acceptance:

- one project can show multiple independent automation graphs;
- native and GitHub automations appear as separate cards;
- shared resources can appear in more than one definition;
- no title, prompt, schedule, or relationship inference exists;
- bootstrap registration accepts only maintained adapter keys and same-project resource IDs;
- bootstrap publication claims exclusive trigger ownership before reporting the registered automation active;
- foreign-project IDs cannot reveal graph data.

### Phase 2: Invocations, Work Items, And Live Position

1. Add invocations, leased outbox, task-run reservations, execution `dispatch_id`, long-lived work items, positions, thread-input bindings, activities, transitions, canonical idempotency keys, and activity-resource links.
2. Extend the existing Scheduler, `TaskRepo`, `ThreadInputRepo`, and `WorkerService` paths to propagate durable `AutomationContext` and prepared `AutomationDispatchEnvelope` values.
3. Instrument native Alerts end to end.
4. Instrument GitHub issue/implementation/PR creation paths.
5. Add live graph projection and node-resource side panels.
6. Publish automation invalidation events and add periodic reconciliation.

Acceptance:

- overlapping invocations display concurrently;
- one invocation can create or process several work items at different nodes without owning their entire lifetime;
- waiting-human work is distinct from running and completed work;
- reload/restart preserves exact live positions;
- native suggestions can be followed from producer through approval and implementation;
- GitHub work can be followed from issue creation through PR state when instrumented.

### Phase 3: History, Replay, And Metrics

1. Add invocation list, invocation graph, and work-item history views.
2. Add transition timeline/replay controls.
3. Add funnel and duration metrics using Chart.js.
4. Add automation health computation and failure/bottleneck summaries.

Acceptance:

- selecting an invocation shows only activity caused by that occurrence, while selecting a work item shows its cross-invocation lifetime;
- replay uses persisted transitions, not current resource state;
- conversion and duration calculations have defined start/end events.

### Phase 4: Templates And Visual Builder

1. Add draft cloning, publication planning/journal tables, and the idempotent resource compiler on top of Phase 1 immutable publication.
2. Add Native SDLC, GitHub SDLC, and Vision Driver templates.
3. Add the Automations-page Template, Describe It, and Blank entry paths.
4. Implement constrained node/edge editing and validation.
5. Add shared described-draft generation for the page and Chat.
6. Register `preview_automation_description`, `create_automation_draft`, `plan_automation_publication`, and confirmed `publish_automation_draft` Chat actions.
7. Add direct Automations-page Save publication, Chat publication preview, stale-plan protection, and idempotent resource compilation.
8. Add safe pause/resume behavior for shared resources.

Acceptance:

- users can create multiple automations from templates;
- users can describe an automation on the Automations page or in Chat through the same normalized schema and generation service;
- page canvas editing and generation persist no Automation/version and cause no runtime mutation;
- refresh or navigation discards unsaved page edits, while Save submits the complete candidate once;
- the Automations-page `Save changes` action validates and publishes immediately without a second Review/Apply step;
- Chat does not publish an unseen plan and requires explicit confirmation after preview;
- publishing creates/reuses visible tasks and schedules;
- failed publication leaves the prior version active;
- the published definition immediately becomes the Live graph topology.

## Tests

### Repository And Migration

- migration up/down behavior follows repository conventions;
- project-scoped list/detail/update/delete isolation;
- immutable published versions;
- composite constraints reject nodes, edges, invocations, activities, work items, positions, and transitions with mismatched project/automation/version parents;
- definition/activity resource tables reject foreign-project resources and duplicate links without relying on nullable unique columns;
- thread-input bindings reject foreign-project/mismatched topology parents and are deleted with their existing queued input;
- shared resource membership across automations;
- active trigger schedules cannot be shared while worker/inbox tasks can;
- concurrent publication cannot claim the same trigger schedule, and pause/supersede/archive retain or release ownership according to policy;
- archive/no-delete rules preserve ordinary history;
- project deletion succeeds with foreign keys enabled and cascades every automation table;
- publication attempts/steps resume safely from pending, running, ambiguous, and failed states;
- nullable composite foreign keys reject partial or mismatched origin, parent, invocation, activity, from-node, and edge identities;
- canonical work-item, activity, and event keys reject duplicate adapter effects;
- one canonical work-item key resolves the same Alert/issue across automation versions;
- append-only transitions and transactional work-item projection;
- stable pagination ordering for invocations, work items, activities, resources, and transitions.

### Runtime Provenance

- schedule claim, invocation creation, outbox insertion, and next-run advancement are atomic;
- scheduled firing creates exactly one invocation/dispatch row for an occurrence under retry, restart, and concurrent scheduler polling;
- concurrent outbox pollers produce one leased claimant, lease renewal is owner-only, and expired claims recover with bounded backoff;
- two automations cannot reserve the same shared task; the losing occurrence is recorded as skipped without a dispatch;
- a crash after task submission resolves the existing invocation/execution activity rather than dispatching duplicate work;
- execution creation is unique by dispatch ID even if the process crashes before the outbox stores `execution_id`;
- a crash after task claim but before in-memory worker submission is recovered through the same prepared execution;
- a due occurrence whose task is running creates one terminal skipped invocation, no outbox, and advances to the first future cadence boundary;
- a skipped one-time occurrence clears `next_run`, and skipped invocations contribute no active graph position;
- losing the expected-due compare-and-swap creates no orphan invocation;
- task execution links to the correct invocation/activity/node;
- concurrent schedule firings do not share invocation IDs;
- native alert inherits producer context;
- approval and atomic implementation-task creation preserve work-item identity;
- child/chained/swarm tasks inherit context;
- a shared Dev Inbox execution uses its causal issue/input context, not arbitrary membership;
- one shared inbox execution can create distinct activities for work items from multiple automations;
- a new-version invocation can process an old-version work item while activities/transitions retain the work item's topology version;
- retry does not duplicate issue, task, PR, work item, or transition;
- restart between queueing and execution preserves context;
- queued inbox automation work uses `ThreadInputRepo.ClaimQueuedForTaskExecution`, including its cancellation, retargeting, and stale-turn behavior;
- automation dispatch uses the existing WorkerService limits, cancellation, lifecycle hooks, LLM/tools, completion, and broadcaster rather than a second executor;
- compiler tests assert domain resources are created through existing Task, Alert, goal, Workflow, schedule, and GitHub boundaries;
- reconciliation never overwrites authoritative task, execution, Alert, goal, Workflow, queued-input, issue, or PR state.

### Draft Generation And Chat

- Template, Describe It, Blank, and Chat inputs normalize through the same graph schema and service pipeline;
- preview generation persists no definition, publication plan, or runtime resource;
- the safe capability snapshot is bounded, deterministically ordered, project-scoped, and excludes credentials/secrets;
- the internal generation model receives a strict versioned graph schema and no mutation tools;
- malformed structured output receives at most one bounded repair attempt;
- model-generated database IDs, arbitrary tools, executable code, SQL, URLs, and unknown configuration fields are rejected;
- described generation uses only supported node/configuration types;
- missing or ambiguous agents, skills, integrations, and source files are reported rather than guessed;
- page graph generation and canvas mutation create no persisted Automation/version or runtime task, schedule, alert, issue, execution, or PR;
- Chat draft creation creates no runtime task, schedule, alert, issue, execution, or PR;
- Chat draft creation and publication planning never invoke ordinary task, schedule, notification, issue, or PR mutation actions;
- Chat derives the project from current context and rejects cross-project IDs;
- Chat draft results include persisted automation/version IDs and a working graph URL;
- publication planning is read-only and reports exact create/reuse/update effects;
- publication rejects a stale plan revision after the draft or dependency state changes;
- publication-plan golden fixtures cover canonical serialization, all enumerated dependency revisions, compiler/adapter versions, and exclusion of volatile operational fields;
- an initial natural-language Chat request cannot publish before the plan is shown and explicitly confirmed;
- Chat rejects missing, expired, cross-user, cross-thread, cross-project, same-turn, replayed, and stale confirmation tokens;
- negative, unrelated, and ambiguous later messages cannot satisfy confirmation; only a structured button or documented normalized publish command can;
- retrying a consumed confirmation token returns the existing publication attempt/result without creating another attempt;
- publication actions are unavailable in read-only/plan mode and to ordinary scheduled tasks by default;
- all four actions are registered in the canonical Chat action registry and execute through the existing action handler path;
- bootstrap-only `register_automation_resources` is capability-filtered, idempotent, and unavailable to ordinary scheduled tasks;
- page and Chat entry points given the same fixed structured candidate produce identical normalized definitions and validation results after IDs/timestamps are removed;
- tests do not compare separate live model generations for semantic equivalence;
- every publishable graph is accepted by one registered adapter; arbitrary DAGs are rejected;
- Workflow execution remains owned by the existing Workflow engine and appears only as a linked activity resource;
- failed or retried publication does not duplicate tasks, schedules, gates, issues, or memberships.
### State Projection

- mixed node states preserve counters;
- display-state precedence is deterministic;
- waiting-human work items remain open after their originating invocation completes;
- invocation completion and work-item completion are computed independently;
- stale/failed claims reconcile correctly;
- external PR state updates change the projection;
- periodic graph refresh reads local persisted state and never calls GitHub implicitly;
- explicit external refresh is cached, rate-limit aware, and records freshness;
- compatible old-version positions map by declared stable node key and incompatible positions appear as legacy work;
- recent-completion cutoff uses server time;
- bounded queries do not exhibit per-node N+1 behavior.

### Handler And Security

- all endpoints require/derive a project;
- foreign-project automation/version/node/invocation/activity/work-item IDs are rejected;
- graph payload omits full prompts, outputs, diffs, and secrets;
- invalid node config, oversized JSON, invalid cycles, and cross-version edges fail clearly;
- publish is idempotent and leaves prior version active on failure.

### UI

- multiple automation cards render and filter correctly;
- New Automation exposes Template, Describe It, and Blank paths;
- described generation displays progress, assumptions, warnings, and validation failures;
- node configuration uses the same schema and validation as template/Chat candidates;
- page edits remain browser-local and are discarded by refresh/navigation until Save;
- opening Edit automation does not clone or create a persisted version;
- the Automations-page `Save changes` action validates and immediately publishes through the shared planner/compiler, with no separate Review/Apply controls;
- successful web Save or confirmed Chat publication navigates to Live view while failed web validation returns the submitted browser-local candidate;
- graph initializes after direct load and HTMX navigation;
- renderer is destroyed when navigating away;
- live events debounce and update node state;
- dropped events recover through periodic reconciliation;
- node panel paginates resources;
- invocation selection isolates the selected trigger occurrence and work-item selection spans all related invocations;
- keyboard navigation and screen-reader labels work;
- status is understandable without color;
- reduced-motion preference disables pulsing;
- narrow/mobile and Wails desktop layouts remain usable;
- empty, loading, partial-data, unhealthy, and archived states render clearly;
- lifecycle and health render independently, so an active degraded/unhealthy automation remains visibly active.

## Observability

Add structured logs and local metrics for:

- automation ID, version ID, invocation ID, activity ID, node ID, and work-item ID on state-changing operations;
- registration/publish validation failures;
- invocation/activity/work-item creation and completion;
- transition append failures;
- dispatch ID recovery, skipped occurrences, and outbox retry/age;
- rejected or replayed Chat confirmation receipts without logging tokens;
- ambiguous external GitHub mutations;
- reconciliation repairs;
- live graph query duration and payload size;
- graph/node limits reached.

Do not log prompts, model output, alert bodies, GitHub credentials, or other secrets merely to support graph debugging.

## Definition Of Done

The feature is complete when:

1. A project can own and display multiple automation graphs.
2. Native and GitHub automation setup registers durable definitions without relying on task titles.
3. The Live view shows concurrent running, waiting-human, blocked, failed, and recently completed work at the correct nodes.
4. Users can drill from graph nodes into real OpenVibely and GitHub resources.
5. Reloads and process restarts preserve automation/invocation/activity/work-item position.
6. Historical invocations and work items retain their immutable graph-version references.
7. Shared automation resources do not collapse causal runtime provenance.
8. Project isolation is enforced for every graph resource.
9. Human approval, PR review, merge, release, and deployment boundaries remain unchanged.
10. The implementation reuses tasks, schedules, alerts, goals, executions, task lineage, PR linkage, and live events rather than creating a hidden workflow engine.
11. Templates and the later builder publish into the same model used by observed live graphs.
12. Users can create an automation through Template, Describe It, or Blank on the Automations page, while maintained Native/GitHub setup flows explicitly register their newly created automation resources.
13. Users can describe an automation in Chat, inspect the resulting persisted draft, preview publication, and explicitly confirm activation.
14. Page and Chat creation share normalization, validation, planning, and compilation services.
15. Every published topology is owned by a registered adapter; Automation edges are never executed by a second generic Workflow engine.
16. Confirmation receipts, publication steps, trigger ownership, dispatch executions, work items, activities, and transitions are idempotent across retries, crashes, and concurrent polling.
17. Relevant repository, service, handler, Chat action, runtime, SSE, UI, accessibility, and restart tests pass.
