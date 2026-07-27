# OpenVibely Automation Graphs Implementation

## Product Contract

Automation Graphs is a project-scoped custom automation builder over existing OpenVibely Tasks, Scheduler rows, workers, queues, Native Alerts, GitHub services, and runtime projection. It does not introduce a second graph executor, queue, worker, arbitrary-code runtime, or hidden Workflow integration.

Each Automation has one stable identity and exactly one current saved graph. There is no Automation draft lifecycle, persisted editable draft, retained graph history, superseded graph, version selector, Definition page, execution/history page, invocation page, work-item page, or restorable publication.

New and edited web graphs exist only in browser memory until `Save changes`. Refresh or navigation discards unsaved changes. Save validates the complete submitted graph and immediately creates or replaces the current graph under the same Automation identity. Save is allowed while Automation work is running. Successful replacement deletes the prior graph and its Automation runtime projection in the same transaction; failure leaves the current graph and behavior unchanged.

Internal graph IDs support current-graph runtime identity only. Save does not create a journal, staged graph, compiler contract version, candidate hash, or retry record.

## Creation Surfaces

The supported creation paths are:

- `Template`, using a canonical maintained candidate.
- `Describe It`, using the bounded model generation and repair pipeline.
- `Blank`, using the open custom builder.
- Chat, using `plan_automation_save` followed by later-confirmed `save_automation`.

Template, Describe It, Blank, and Edit all open browser-local graphs and apply them only on `Save changes`. `Use template` loads the canonical maintained candidate into the same builder without creating durable rows. All surfaces use the same candidate schema, capability validation, atomic Save service, and safety boundaries. New template creation and maintained setup registration are restricted to Native and GitHub adapters; both SDLC templates include scheduled Vision Suggestions. The internal Vision Driver adapter remains available only to preserve editing and runtime support for already-saved graphs, without migration or backfill.

Opening a web builder creates no Automation, graph row, Task, Scheduler row, Alert, GitHub issue, execution, or other runtime resource. An invalid Save returns the submitted browser-local candidate with a prominent `Save did not apply` error, expands the visible setup issues, and creates no resources. An unready GitHub graph names the required connected authentication, at least one GitHub Authorized User, and a project GitHub repository resolvable from either the configured repository URL or a GitHub remote in the project's local checkout instead of appearing to ignore the Save action.

## Custom Graph

The builder lets users add, place, configure, connect, reconnect, and remove surfaced nodes. Supported public capabilities include:

- `Schedule`, a substantive scheduled Task plus one Scheduler row targeting that same Task.
- `Task`, an ordinary OpenVibely Task with prompt, category, priority, and optional surfaced Agent assignment.
- Native Alert `Create notification` and `Human approval` nodes.
- Supported GitHub issue, assignment, inbox, pull-request, and human-review nodes.
- `Outcome`, a terminal visual result with no runtime side effect.

The existing hidden Workflow subsystem is not an Automation node.

A Schedule can perform recurring work without a second Task. `Schedule → Task` is an explicit downstream handoff: scheduled Task A completes, then existing Task/worker machinery activates or reuses distinct Task B. Task-to-Task handoffs use the same normal parent/chain machinery. One task may fan out to multiple children, but a child may have at most one task parent because persisted Tasks have one parent.

Only the exact `GitHub inbox → Task → Open pull request` shape treats the Task as issue-specific configuration for Tasks created later by the inbox. Other Task nodes materialize as stable Tasks on Save.

## Connections

Edges persist execution direction with `from` and `to`. Every browser-authored edge must use the source right output port and target left input port:

```json
{
  "from_port": "right",
  "to_port": "left"
}
```

Explicit reversed, same-kind, or unsupported ports fail strict public Save validation. Missing or older trusted connector metadata may be canonicalized only while reopening an existing saved graph. New manual submissions are never silently repaired before validation.

The canvas renders distinct unlabeled left input and right output handles. Assistive labels and typed port metadata remain explicit. The absence of visible `IN` and `OUT` text is intentional.

Unsupported handoffs, dangling endpoints, duplicate keys, self-edges, executable cycles, ambiguous task parents, invalid human-gate conditions, unsafe configuration, unavailable capabilities, and foreign-project references fail closed before effects.

## Save And Application

Save follows one shared sequence:

1. Strictly decode and normalize non-semantic formatting without overwriting explicit invalid values.
2. Validate schema, limits, node configuration, ports, topology, project scope, integration readiness, and human boundaries.
3. Build exact Task, Schedule, binding, and graph writes from the complete candidate.
4. Begin one SQLite transaction and create new runnable Tasks in non-admitted Backlog state.
5. Apply Task configuration, Scheduler rows, resource membership, lifecycle-compatible admission state, and current-graph identity.
6. Delete the replaced graph and runtime projection tied to it, then commit.
7. After commit, submit newly admitted Active root Tasks through the existing `TaskService` and worker queue.

Any error before commit rolls back every Save write. A failed first Save creates no Automation or resources; a failed edit leaves the prior graph active. No failed-application journal or retry path exists.

Removing or reconfiguring a Schedule deletes its obsolete exclusively owned Scheduler row. Domain Tasks remain ordinary OpenVibely resources unless their normal lifecycle deletes them independently.

Maintained Native/GitHub setup registration is a point-in-time creation path, not an upgrade or migration path. The first registration for a project-scoped stable setup key creates the saved graph from that release's bundled template, including complete valid defaults for every maintained Task, Schedule, action, and human-gate node before bound resource configuration is overlaid. No bundled-template version, upgrade state, or migration target is persisted. Any later registration for the same published Automation returns the existing graph, identity, lifecycle, resource bindings, and runtime projection unchanged, even when a newer release changes template nodes, edges, configuration, names, or supplied resources. Registration may remove obsolete noncurrent rows left by pre-contract failed/draft attempts, but it never replaces the current graph or its owned Schedules. Trusted Edit reconstruction fills only absent canonical configuration keys in older maintained snapshots so they remain valid and editable; it preserves every explicitly stored value, performs no durable write, and does not apply to public Save input. A user changes an existing Automation only through explicit Edit and `Save changes`; adopting a newer bundled template requires deleting the old Automation and creating it again.

## Lifecycle

Lifecycle state controls admission of new work and is independent of graph replacement:

- `Active` Save remains active and applies configured schedule enablement.
- `Paused` Save remains paused, leaves schedules disabled, and keeps ownership marked paused.
- `Archived` Save remains archived, leaves schedules disabled, and keeps ownership marked archived.
- Save or maintained registration never resumes paused work or bypasses the archived non-resumable boundary.

Exclusive schedule provenance survives pause and archive. This allows archived edits, Schedule removal/replacement, and Automation deletion to locate and delete owned Scheduler rows without orphaning them. Deletion remains blocked while a surviving scheduler dispatch or a running execution owns in-flight work. Running ordinary Task executions without a scheduler dispatch retain Automation ownership through their exact Automation activity `execution` resource and block deletion until the execution becomes terminal. A stale nonterminal invocation whose dispatch and execution were already removed by ordinary Task deletion does not represent recoverable in-flight work and cannot permanently prevent Automation deletion.

A root Task configured Active while the Automation is paused is stored non-admitted in Backlog and is not submitted during Save. A downstream handoff completed while paused also persists its causal entered transition while leaving the child pending in Backlog. Explicit Resume atomically re-enables configured schedules, admits matching current-graph pending roots and deferred entered children, commits lifecycle state, and then submits those exact Tasks once. Existing Active roots are not resubmitted merely because the Automation resumes. Pause and Archive demote pending Active current-graph roots and children; a final persisted admission check between handoff commit and worker submission prevents a child that loses a concurrent lifecycle race from being submitted.

## Runtime Projection

Automation runtime uses existing domain machinery. Runtime rows bind exact project, Automation, current graph, node, edge, invocation, work item, Task/execution, and external resource identities as applicable.

A newly admitted standalone root resolves Automation context from exact current-graph Task resource membership before execution. Causal activity-derived bindings take precedence for downstream and shared Task work.

Live displays only the current graph's projection. Replacement deletes prior invocation, dispatch, reservation, work-item, activity, transition, and position rows through graph ownership cascades. No older-graph positions are mapped into current nodes, and there is no “Earlier activity” compatibility block. Existing domain Task executions may continue independently after their old Automation projection is removed, but cannot continue through a deleted graph.

Only Schedule and Task nodes with exact current `task` resource bindings are links. A Schedule opens its Schedule-owned Task. Action, gate, Outcome, and unbound nodes remain inert.

## Native Alert Boundary

Saving a graph containing `Create notification` creates no Alert. A bound Task later invokes the existing notification runtime. Every supported notification action hands off through `Human approval`; approval may terminate or branch to configured approved/rejected Outcomes.

Approval authorizes only the configured downstream handoff. It never authorizes merge, release, deployment, or arbitrary execution. Idempotent retries may reuse only an Alert already owned by the exact Automation source.

## GitHub Boundary

GitHub actions remain constrained by configured project/integration readiness and the supported lifecycle topology. Publication creates only configured producer/inbox Tasks and Scheduler rows. The exact `GitHub inbox → Task → Open pull request` Task is configuration-only during Save, but its maintained node must still carry a non-empty canonical prompt, category exactly `active`, and priority `1..4` because runtime copies those values into each later issue-specific Task. Assignment is already the human approval signal, so the maintained builder renders that category as fixed Active and runtime submits the created issue-specific Task immediately; Schedule nodes retain their scheduled category behavior. Missing or malformed values, including any non-Active Implementation category, fail Save and first registration before effects. Issues, issue-specific Tasks, and pull requests arise later through existing GitHub and Task services.

Issue assignment is human approval to begin implementation. Pull requests are opened or reused but never automatically approved or merged. Review, merge, release, and deployment remain human-controlled. Runtime idempotency cannot adopt a GitHub object created outside the exact Automation source.

Automation issue-creation duplicate protection never lists, searches, fetches, or compares existing GitHub issues. It uses a durable local record keyed by project, repository, and a normalized-title fingerprint. A short-lived `reserved` state serializes concurrent equivalent runs before provider dispatch. Immediately before calling GitHub, the record becomes `dispatched`; that state is never expiry-reclaimable, so an ambiguous provider outcome remains durably fail-closed without reading GitHub. A successful provider response stores only the returned issue number and marks the record `completed` using a bounded persistence context independent of caller cancellation. The claim also stores only trusted OpenVibely project, Automation, graph, invocation, Task, and execution identifiers needed to finish the exact local activity, work item, issue transition, and assignment transition. It never stores GitHub issue titles, bodies, comments, or instructions. A completed claim is reusable only when the caller has the same project, Task, and complete Automation/graph/source-node binding set; invocation and execution IDs may differ for a legitimate later run. A different Automation, replacement graph, source node, or Task fails closed without another provider mutation or GitHub lookup. Projection uses a bounded cancellation-independent context, and a same-source same- or later-execution retry idempotently repairs any missing projection before returning the locally recorded canonical issue. A resource already recorded for one causal binding is reconciliation evidence, not independent reuse authority: the retry must still load the completed claim and repair its full stored binding set. Repair succeeds only after every stored source binding records both the issue transition and waiting assignment transition; a deleted or incomplete source graph remains a reconciliation error rather than a successful no-op. Historical claims without an exact trusted projection source remain fail-closed rather than inferring provenance. Only a record that is still definitively pre-dispatch may expire or be released. Assigned-issue inbox reads remain a separate human-gated path and do not participate in issue-creation deduplication.

## Chat Contract

`plan_automation_save` generates or accepts a candidate, validates it, and returns a user-readable plan plus the exact later confirmation command. Before Save it exposes no Automation ID, graph identity, URL, runtime resource identity, or draft terminology, and leaves zero Automation, Task, or Scheduler rows.

Private signed confirmation state durably retains the exact candidate, project, principal, thread, plan message, Automation name, and expiry. It contains no Automation ID, graph ID, plan revision, hash, or resource identity. Validation does not consume it; the later exact `save <automation-name>` confirmation consumes it inside the same transaction that saves the graph. Successful Save creates one Automation and returns its real Live URL.

The removed `create_automation_draft`, `plan_automation_publication`, and `publish_automation_draft` actions and `/automations/drafts` route are not compatibility surfaces.

## Describe It

Describe It receives a purpose-built, project-scoped, secret-free view of supported node types, roles, surfaced Agents/tool capabilities, integration readiness, and safety boundaries. It receives no mutation tools.

The model must return the strict candidate schema. One bounded repair may receive the original schema, capability snapshot, user description, validation failure, and prior output. Unsupported schema versions and unknown fields are rejected rather than downgraded.

Generation-only normalization may add the unambiguous Native notification approval boundary or clamp numeric Task priorities into `1..4`. These narrow repairs do not apply to browser-authored Save input. If requested work lacks a surfaced executable capability, generation must warn instead of claiming it is runnable.

## Project And Safety Boundaries

All reads and writes are project-scoped. Automations are created only by explicit Save or maintained registration; existing Tasks, schedules, Alerts, GitHub objects, names, prompts, or lineage are never inferred into an Automation.

Schedule ownership is exclusive. Worker/inbox resources may be shared only where an adapter explicitly permits it. Resource validation rejects foreign-project IDs and maintained Schedule/Task bindings whose Scheduler row targets a different Task.

Ordinary reads, portfolio rendering, and Live rendering do not make external GitHub calls. Explicit reconciliation remains bounded and persisted through existing services.

## User Interface

The Automations portfolio uses one card per saved Automation. Card click opens Live, `Edit` opens a browser-local reconstruction of the current graph, and `Delete` confirms removal of the saved graph, Automation record, and exclusively owned Scheduler rows while preserving Tasks.

Automation detail is one full-width Live graph with no tabs or auxiliary Automation pages. Removed Definition, History, invocation, work-item, and node-resource URLs return `404`.

## Required Validation

Changes to Automation Graphs require:

- regression-first repository/service coverage for persistence, replacement, lifecycle, runtime, and project isolation;
- public handler coverage for browser Save validation and removed routes;
- production-template/browser coverage for canvas interactions and visible copy;
- stable `templ generate` after `.templ` changes;
- `gofmt`, `git diff --check`, and `go build ./...`;
- focused touched-package tests and a fresh full repository suite;
- `STATUS.md` and authoritative tracked specification reconciliation, with managed memory excluded when the task contract requires it;
- a clean checkpoint followed by a separate edit-free full contract audit.
