# Automation Implementation Status

## Current State

Automation Graphs is implemented as a project-scoped custom automation builder over existing OpenVibely Tasks, Scheduler rows, workers, Native Alerts, GitHub services, queues, and runtime projection. Template, Describe It, Blank, and Chat use the same surfaced graph contract and safety boundaries.

Each Automation has exactly one current saved graph. There is no Automation draft lifecycle, retained version history, superseded graph, version selection, Definition page, execution/history page, invocation page, work-item page, node-resource sidebar, Save journal, failed-Save recovery item, plan revision, or compiler contract version. Internal graph IDs exist only for current runtime identity.

## Save Contract

- New and edited web graphs exist only in browser memory until `Save changes`. Refresh or navigation discards unsaved changes.
- Save validates the complete submitted graph, applies real resources immediately, and creates or replaces the current graph under the same Automation identity.
- Save may replace the current graph while Automation work is active. Successful replacement deletes the prior graph and its Automation runtime projection in the same transaction. Failure leaves the current graph and behavior unchanged.
- Maintained templates use direct creation: `Use template` validates and atomically creates the Automation, Tasks, and Scheduler rows, then redirects to Live. Validation failure creates nothing.
- Maintained Native and GitHub registration follows the same one-current-graph replacement contract: changed registration leaves one current graph, deletes prior runtime projection, and removes obsolete exclusively owned Scheduler rows.
- Save writes Automation identity, the complete graph, Task changes, Scheduler rows, bindings, lifecycle-compatible admission state, and replaced-graph cleanup in one SQLite transaction. Any error rolls back all Save writes, so a first Save creates nothing and an edit leaves its previous graph active.
- Chat uses `plan_automation_save` followed by later-confirmed `save_automation`. Before Save, the displayed plan and private confirmation state expose no Automation/graph identity, URL, runtime resources, or draft terminology. Confirmation consumption and Save commit atomically.

## Graph Contract

- Blank is an open custom builder, not a preset assembler. Users can add supported Schedule, Task, Native Alert, GitHub action/gate, and Outcome nodes and connect deterministic supported handoffs.
- A Schedule is a substantive Task plus a Scheduler row targeting that same Task. `Schedule → Task` means scheduled Task A completes before OpenVibely activates distinct follow-up Task B.
- Maintained Native and GitHub recurring loops use one Schedule node per substantive scheduled Task. Vision Driver also uses one Schedule node rather than a schedule relay linked to a duplicate same-purpose Task.
- The maintained GitHub SDLC Automation owns complete Offering Manager, finder, Dev Inbox, and Loop Auditor prompts plus daily discovery, hourly inbox, and weekly auditor cadences. It does not read or require the bootstrap skill at runtime. Registration hydrates the exact bound Task/Schedule configuration into the graph and replaces older `{}` graph configuration without mutating the bound Task prompt.
- Graph connections leave the source node's unlabeled right output handle and enter the target node's unlabeled left input handle. Missing, reversed, or same-kind ports are rejected through the public Save path. Legacy saved connector metadata is normalized only when reopening an existing saved graph.
- Unsupported handoffs, unsafe configuration, ambiguous task parents, executable cycles, invalid conditions, foreign-project references, and unavailable capabilities fail closed before resource effects.
- The hidden Workflow subsystem is not exposed as an Automation node.

## Runtime And Lifecycle

- Newly created runnable Tasks remain non-admitted Backlog rows inside the Save transaction until the graph, resource membership, configured categories, and topology are ready. Active root Tasks are submitted through the existing worker only after commit.
- Saving while paused or archived preserves that lifecycle. It does not enable schedules, resume work, or reacquire active ownership.
- Active root Tasks added or changed from Backlog to Active while paused remain non-admitted until explicit Resume. Resume atomically restores their configured Active category and then submits those exact roots through `TaskService`; archived roots remain non-admitted and archived Automations remain non-resumable.
- Ordinary queued roots reload their current persisted Task configuration at dispatch, so replacement prompt/Agent/topology data cannot be mixed with stale queued values. Definition-only root provenance creates and terminalizes an execution-scoped work item on the replacement Live graph; prepared Automation dispatches retain their exact immutable envelope.
- Archived Automations retain disabled exclusive schedule provenance using archived owner rows. Archived edits can therefore remove obsolete Scheduler rows, and deleting an archived Automation removes all exclusively owned Scheduler rows while preserving Tasks.
- Native Alert approval never authorizes merge, release, or deployment. GitHub assignment, review, merge, release, and deployment remain human-controlled.
- Runtime state is shown only in the full-width Live graph, whose canvas grows to fill the remaining viewport height. Only Schedule and Task nodes with exact current task bindings navigate, and they open that existing project-scoped Task; nodes without runtime resources are inert.

## Regression Coverage

Current regressions cover:

- Browser-local new/edit flows, direct template creation, direct Save while running, one-current-graph replacement, and injected first-Save/replacement failures that prove complete rollback with no partial Tasks, Scheduler rows, Automation identity, or graph replacement.
- Maintained registration replacement for active, paused, and archived Automations, including prior graph/runtime deletion, unchanged-registration cleanup, obsolete schedule removal, and exact Task/Schedule configuration hydration.
- Automation-owned prompt coverage for Offering Manager, all three finders, Dev Inbox, and Loop Auditor, plus cadence coverage and a source-level regression that forbids runtime dependencies on `builtinskills`, `SKILL.md`, or global skill support files. Existing saved Automations continue from their current persisted graph.
- No pre-commit root execution, production provenance lookup, stale queued-root refresh after graph replacement, completed root Live projection, paused existing/new root admission on Resume, and deterministic task handoffs/fan-out.
- Archive then delete, archive then schedule removal, paused/archived Save, and configured disabled schedules on Resume.
- Strict public browser Save rejection of missing and reversed ports plus narrowly scoped reopened-graph connector normalization.
- Schedule-owned Task semantics across custom, maintained, Describe It, template, and Chat paths.
- Live-only detail, exact Task links, removed auxiliary routes, and absence of version/history compatibility copy.
- Native Alert and GitHub lifecycle boundaries, project isolation, idempotency, and no inference from existing resources.

## Checkpoint Lineage

The exact pre-collapse implementation tree remains at `backup/e109ce89-before-main-rebase-20260723194351`. Reflog and unreachable-object recovery preserved the replayed checkpoint objects under:

- `backup/e109ce89-recovered-43-checkpoints-20260724`, whose 43 direct commits begin at local-main base `600ca2b3` and end at `623649dd`;
- `backup/e109ce89-recovered-rebase-tip-20260724`, which additionally preserves the next two successfully replayed checkpoints through `0db6521d`;
- `backup/e109ce89-collapsed-published-tip-20260724`, which preserves the collapsed tree at `1a3347be`.

The assigned task branch contains the 43 recovered direct checkpoints, followed by an explicit tree-reconciliation checkpoint with the exact pre-collapse implementation tree and documentation checkpoints. Base `600ca2b3` and recovered tip `623649dd` remain direct ancestors of the task branch. Rebased integration checkpoint `f7f54bcf` brings in current local `main` checkpoint `feaf1d09` without rewriting that recovered history, retains the Chrome browser teardown fix across Automation, Chat, reconnect, and Task Thread coverage, imports the authorized managed-memory artifact update, and includes the subsequent steering-recovery test stabilization.

Shared `main` was not rewritten. Both `origin/main` and `upstream/main` pointed to collapsed checkpoint `1a3347be` at reconciliation time, so replacing that published ancestry requires explicitly approved shared-history rewriting. Task-branch checkpoint recovery is complete; remote-main history cleanup remains a separate destructive integration decision.

## Validation

Current checkpoint evidence:

- `templ generate` completed with zero generated updates.
- `gofmt` and `git diff --check` pass.
- `go build ./...` passes; desktop linking emits only the documented non-failing newer-SDK macOS warnings.
- The focused repository/service/handler/template package suite passes.
- `TMPDIR=/private/tmp go test -p 1 ./... -count=1 -timeout 300s` passes every package.

The atomic Save correction removes publication-attempt and publication-step persistence, candidate hashing, plan revisions, compiler contract versions, and browser recovery state. Repository regressions inject Task/Schedule write failures and prove SQLite rollback leaves either no new Automation or the previous graph and resources unchanged.

The implementation and tracked documentation describe self-contained GitHub SDLC Automation prompt defaults, matching recurring cadences, registration hydration, and atomic direct Save; the Automation has no runtime dependency on the bootstrap skill package. The selected managed `automation_graphs` memory contains the target atomic-Save contract, but its final implementation-status note predates this refactor. No authorized managed-memory writer is available in this task runtime, so that note cannot be updated here and synchronization is not claimed.

A fresh final contract audit passed after the final full suite. It found no production references to compiler contract versions, plan revisions, candidate hashes, publication-attempt/step models or tables, staged-Save guards, failed-Save recovery state, or recovery UI copy. The audit also verified direct template creation, exact Chat candidate matching inside the Save transaction, and rollback coverage for first Save and replacement failures.
