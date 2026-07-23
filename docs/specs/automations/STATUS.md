# Automation Implementation Status

## Current State

Automation Graphs is implemented as a project-scoped custom automation builder over existing OpenVibely Tasks, Scheduler rows, workers, Native Alerts, GitHub services, queues, and runtime projection. Template, Describe It, Blank, and Chat use the same surfaced graph contract and safety boundaries.

Each Automation has exactly one current saved graph. There is no Automation draft lifecycle, retained version history, superseded graph, version selection, Definition page, execution/history page, invocation page, work-item page, or node-resource sidebar. Internal graph IDs and Save journals exist only to apply one Save atomically and retry an exact failed application; they are not user-selectable Automations or history.

## Save Contract

- New and edited web graphs exist only in browser memory until `Save changes`. Refresh or navigation discards unsaved changes.
- Save validates the complete submitted graph, applies real resources immediately, and creates or replaces the current graph under the same Automation identity.
- Save may replace the current graph while Automation work is active. Successful replacement deletes the prior graph and its Automation runtime projection in the same transaction. Failure leaves the current graph and behavior unchanged.
- Maintained Native and GitHub registration follows the same replacement contract: changed registration leaves one current graph, deletes prior runtime projection, and removes obsolete exclusively owned Scheduler rows. Registration, a later web Save, and Automation deletion all fail closed while a private staged or failed Save journal exists, preserving its compiler-created Tasks, Scheduler rows, and exact retry identity.
- A failed Save remains recoverable after refresh or navigation once its private publication attempt is durably reserved. The Automations portfolio shows a separate `Save needs attention` item rather than a saved Automation card; reopening restores the exact private candidate, retry plan, and resource steps, and retry reuses compiler-created Tasks and Scheduler rows. Failed first-Save recovery keeps browser history on the refreshable Automations portfolio, while failed replacement recovery keeps the existing saved Automation's valid Live URL. Recovery exposes no version selection/history and hides Automation deletion until Save succeeds.
- If planning or publication-attempt reservation fails before an attempt or resource effect exists, Save atomically discards only that unreserved staging. The still-open browser design remains available to retry, a failed first Save leaves no Automation identity, and a failed replacement leaves the current saved graph and resources unchanged.
- Chat uses `plan_automation_save` followed by later-confirmed `save_automation`. Before Save, the displayed plan and private confirmation state expose no Automation/graph identity, URL, runtime resources, or draft terminology.

## Graph Contract

- Blank is an open custom builder, not a preset assembler. Users can add supported Schedule, Task, Native Alert, GitHub action/gate, and Outcome nodes and connect deterministic supported handoffs.
- A Schedule is a substantive Task plus a Scheduler row targeting that same Task. `Schedule → Task` means scheduled Task A completes before OpenVibely activates distinct follow-up Task B.
- Maintained Native and GitHub recurring loops use one Schedule node per substantive scheduled Task. Vision Driver also uses one Schedule node rather than a schedule relay linked to a duplicate same-purpose Task.
- Graph connections leave the source node's unlabeled right output handle and enter the target node's unlabeled left input handle. Missing, reversed, or same-kind ports are rejected through the public Save path. Legacy saved connector metadata is normalized only when reopening an existing saved graph.
- Unsupported handoffs, unsafe configuration, ambiguous task parents, executable cycles, invalid conditions, foreign-project references, and unavailable capabilities fail closed before resource effects.
- The hidden Workflow subsystem is not exposed as an Automation node.

## Runtime And Lifecycle

- Compiler-created Tasks are staged in non-admitted Backlog state until the graph, resource membership, configured categories, and topology commit. Active root Tasks are submitted through the existing worker only after commit.
- Saving while paused or archived preserves that lifecycle. It does not enable schedules, resume work, or reacquire active ownership.
- Active root Tasks added or changed from Backlog to Active while paused remain non-admitted until explicit Resume. Resume atomically restores their configured Active category and then submits those exact roots through `TaskService`; archived roots remain non-admitted and archived Automations remain non-resumable.
- Ordinary queued roots reload their current persisted Task configuration at dispatch, so replacement prompt/Agent/topology data cannot be mixed with stale queued values. Definition-only root provenance creates and terminalizes an execution-scoped work item on the replacement Live graph; prepared Automation dispatches retain their exact immutable envelope.
- Archived Automations retain disabled exclusive schedule provenance using archived owner rows. Archived edits can therefore remove obsolete Scheduler rows, and deleting an archived Automation removes all exclusively owned Scheduler rows while preserving Tasks.
- Native Alert approval never authorizes merge, release, or deployment. GitHub assignment, review, merge, release, and deployment remain human-controlled.
- Runtime state is shown only in the full-width Live graph, whose canvas grows to fill the remaining viewport height. Only Schedule and Task nodes with exact current task bindings navigate, and they open that existing project-scoped Task; nodes without runtime resources are inert.

## Regression Coverage

Current regressions cover:

- Browser-local new/edit flows, direct Save, Save while running, one-current-graph replacement, and exact failed-Save recovery after browser state loss. Recovery coverage includes failed first Saves omitted from saved cards, project-isolated portfolio discovery, first-Save recovery history and hard refresh through the portfolio, failed replacements reopening the staged design while retaining the saved Automation's refreshable Live URL, hidden deletion on the recovery builder, affected portfolio card, and refreshed Live page while recovery is active, deletion restoration after exact retry, and retry without duplicate Tasks or Scheduler rows. Separate early-failure coverage injects initial planning and publication-attempt reservation failures for first and replacement Saves, proving unreserved staging is removed, no false recovery item or resource appears, the current saved behavior remains active, and a later Save is unblocked.
- Maintained registration replacement for active, paused, and archived Automations, including prior graph/runtime deletion, staged Save-journal conflict protection across registration, later web Save, and deletion, exact retry with pre-created Tasks/Scheduler rows, unchanged-registration cleanup, and obsolete schedule removal.
- No pre-commit root execution, production provenance lookup, stale queued-root refresh after graph replacement, completed root Live projection, paused existing/new root admission on Resume, and deterministic task handoffs/fan-out.
- Archive then delete, archive then schedule removal, paused/archived Save, and configured disabled schedules on Resume.
- Strict public browser Save rejection of missing and reversed ports plus narrowly scoped reopened-graph connector normalization.
- Schedule-owned Task semantics across custom, maintained, Describe It, template, and Chat paths.
- Live-only detail, exact Task links, removed auxiliary routes, and absence of version/history compatibility copy.
- Native Alert and GitHub lifecycle boundaries, project isolation, idempotency, and no inference from existing resources.

## Validation

Current checkpoint evidence:

- `templ generate` completed with zero generated updates.
- `gofmt` and `git diff --check` pass.
- `go build ./...` passes; desktop linking emits only the documented non-failing newer-SDK macOS warnings.
- The focused migration/repository/service/handler/template package suite passes.
- `TMPDIR=/private/tmp go test -p 1 ./... -count=1 -timeout 300s` passes every package.

The pre-reservation recovery finding from the strict audit of checkpoint `689271e7` is repaired and validated in the rebased implementation checkpoint `d23694b8`. Web Save now uses one transactional guard to discard staging only when no publication attempt exists: initial planning and attempt-reservation failures leave no hidden graph, Automation identity, false recovery item, Task, or Scheduler row, while failures after reservation preserve the exact journal and compiler-created resources for retry. First and replacement Save regressions prove later Saves remain unblocked and the current saved behavior is unchanged. The authoritative `automation_graphs` managed memory is reconciled with this implementation state. A new separate strict read-only full contract audit remains the final completion gate; no clean-audit completion is claimed here.
