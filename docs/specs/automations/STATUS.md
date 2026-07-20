# Automation Implementation Status

## Current Phase

Phase 5 is active after direct product feedback showed the completed first-release Blank flow was still a preset-topology assembler rather than a custom Automation builder. Blank now uses the registered `custom` capability adapter. User-defined Schedule, Agent task, Native Alert, GitHub issue/assignment/inbox/implementation/PR/review, and Outcome nodes can be freely added, positioned, connected, and configured; supported task, approval, and GitHub lifecycle graphs publish real visible resources through existing services and project exact runtime state onto the user-owned topology. Describe It and Chat now generate and publish the same surfaced custom contract instead of forcing a maintained preset.

## Status

Phases 1-4 and the prior graph/editor, publication, runtime, identity, safety, pagination, accessibility, and observability repairs remain complete through `d2512dd`. Phase 5 now has a validated custom foundation, deterministic multi-task execution, Native human approval, configurable GitHub lifecycle capabilities, Describe It/Chat parity for the surfaced custom schema, and repaired Blank connector and schedule resource ownership. Blank starts with `adapter_key=custom`, the node dialog uses one `Node purpose` field with no runtime/design-only split, no preset nodes or transitions are inserted, and its empty supported-edge palette is serialized as `[]` rather than `null`, so direct node-to-node drags cannot fail in the browser before creating an edge. Visible lines and arrows render fully opaque in a final pointer-transparent foreground above nodes and connector circles, while selection hit areas remain behind nodes and captured pointer releases resolve the real connector under the release coordinates. Applying a custom Schedule → Agent task graph creates a dedicated scheduled task and Scheduler row for the Schedule node plus a separate ordinary Backlog Agent Task. A due occurrence dispatches the Schedule node's own task through the existing Automation machinery; successful completion follows the exact immutable Schedule-to-Agent edge through the existing task-chain path and activates the Agent Task without repointing the Scheduler row. The Schedules calendar therefore shows every applied Schedule node as its own scheduled task using the immutable schedule-node name. Unsupported capability handoffs remain fail-closed draft errors, and publication derives only real task/schedule effects plus explicit Alert/GitHub/human-gate configuration from user-owned nodes and edges. Runtime Alert and GitHub activity uses existing services and exact immutable Automation-version provenance. Per the user's correction, the old JSON-only hidden Workflow subsystem is not an Automation builder capability and will be removed separately rather than exposed here. Remaining Phase 5 work is managed-memory reconciliation, a clean checkpoint, and the fresh edit-free full-objective audit.

## Phase 5 Composable Capability Follow-up

### Completed Requirements

- Custom publication no longer requires a Schedule. A graph containing one standalone ordinary task publishes that task as a normal Automation-owned resource; maintained registration adapters retain their existing trigger requirement.
- Schedule and ordinary task nodes may connect to supported tasks, actions, or Outcomes. One task may fan out to multiple ordinary child tasks, and each child is activated through the existing task transition and worker path using an ordered, bounded lookup of immutable published edges and resource memberships.
- A Schedule may directly drive Create notification or Create GitHub issue because the Schedule node is itself the scheduled task. Multiple valid Schedule/task producers may share one Create notification action.
- Native approval may be terminal or expose either or both approved/rejected Outcomes. If a real human decision has no configured result edge, runtime records terminal completion at the approval gate instead of failing or inventing a handoff.
- Fixed workflow recipes, exact task-source counts, task category rules derived from graph position, mandatory paired approval Outcomes, and inert disconnected-Outcome errors were removed. The remaining publication blockers represent actual unsupported semantics: unknown capabilities/handoffs, more than one persisted task parent, cycles, invalid gate conditions, duplicate same-kind external action/Outcome targets that the role-based runtime cannot distinguish, GitHub assignment/review bypasses, and incomplete executable action paths.
- Describe It and Chat use the same composable custom contract. The model prompt now describes standalone tasks, task fan-out, direct Schedule actions, shared notification actions, and optional approval outcomes instead of instructing the model to generate only linear mini-workflows.

### Regression And Validation Evidence

- `TestCustomAutomationValidatesComposableTaskHandoffsAndRejectsUnsupportedJoinsOrCycles` and the Native Alert topology regression failed first under the recipe validator, then proved standalone tasks, fan-out, Schedule-to-action, shared notification actions, optional outcomes, and explicit duplicate same-kind target rejection while retaining task-parent, cycle, condition, and human-boundary rejection.
- `TestCustomAutomationPublicationSupportsStandaloneTaskFanout` failed first on the global Schedule requirement. It now publishes three ordinary tasks with no Scheduler row, persists both child-parent relationships, and proves one parent completion activates both children and records two exact immutable transitions.
- `TestCustomAutomationPublicationRunsNativeAlertApprovalOnExactUserNodes` failed first with `sql: no rows in result set` for an omitted rejected branch. It now proves the configured approved branch reaches its Outcome and the unconfigured rejected result terminates safely at the approval gate.
- `TestAutomationDescriptionGenerationSupportsExpandedCustomBuilderContract` failed first on the old linear-only prompt and now proves Describe It/Chat receive the expanded composition contract.
- The compiler/adapter contract version is `7`; both publication-plan goldens were deliberately refreshed so stale version-6 confirmations cannot apply the changed semantics.
- The Automation-focused repository/service/handler/Chat suite passes after the regression repairs.
- `templ generate` completed with zero generated updates; `gofmt`, `git diff --check`, and `go build ./...` passed with only the documented non-failing macOS SDK linker warnings.
- `go test ./web/templates/pages -count=1 -timeout 180s` and `go test ./internal/... -count=1 -timeout 180s` passed every package.
- The first `TMPDIR=/private/tmp go test ./... -count=1 -timeout 180s` run had one unrelated Task Thread browser scroll-position timing failure. The exact test passed immediately in isolation, and a fresh complete rerun passed every package, classifying the first failure as transient.

## Phase 5 Task Parity And Edge Controls Follow-up

### Completed Requirements

- A custom Schedule node is the scheduled task. Its settings expose the task prompt, optional primary Agent, fixed `Scheduled task` category, priority, and timing; a standalone Schedule publishes its real task and Scheduler row and appears on the Schedules page without requiring an Agent Task.
- A custom Agent Task node stays in parity with an ordinary OpenVibely task. Its settings expose prompt, optional primary Agent, Backlog/Active category, and priority; they do not expose Scheduled category, Skills, Source files, or schedule timing.
- `Schedule → Agent Task` publishes two distinct tasks. The Agent Task starts blocked in Backlog, a due occurrence dispatches the Schedule task, and successful Schedule-task completion activates the Agent Task through the existing immutable task-chain transaction and Worker submission path.
- Edge rendering uses separate paint layers: transparent selection geometry behind nodes, opaque pointer-transparent lines/arrows above nodes and connector circles, and reconnect/delete controls in the final topmost SVG overlay. Newly drawn and server-rendered edges use the same ordering.

### Regression And Validation Evidence

- The production Chrome regression failed first because no topmost controls overlay existed. It now selects a real Blank connection, proves the midpoint delete control follows the foreground edge in SVG paint order, verifies the control is the hit-tested element at its center, and retains connection creation, reconnect, deletion, node movement, scroll, and zoom coverage.
- Custom draft and handler regressions prove standalone Schedule publication, required Schedule task prompt/category/priority, no custom Agent Task Skills/Source files/Scheduled category, and server rejection of those unsupported custom fields.
- `TestCustomAutomationPublicationCompilesLinearTaskHandoffIntoExistingTaskChain` failed first because the transactional handoff guard accepted only Agent-task sources. It now proves exact Schedule-task completion activates the connected Agent Task as Active/Pending while preserving the complete immutable causal lineage.
- `templ generate` completed with zero pending generated updates; `gofmt`, `git diff --check`, and `go build ./...` pass. Desktop build/test emits only the documented non-failing newer-SDK linker warnings.
- The focused Automation database/repository/service/handler/Chat/observability/page suite, `go test ./web/templates/pages -count=1 -timeout 120s`, `go test ./internal/... -count=1 -timeout 120s`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` all pass.
- Direct managed-memory mutation remains unavailable in this task runtime. The selected `automation_graphs.md` topic reflects the corrected Schedule/task ownership and edge controls, but still says executable task branching is unsupported and records checkpoint `30a7ad4`; it requires reconciliation with the composable capability checkpoint before a qualifying full-objective audit.

## Phase 5 Blank Connector Regression Checkpoint

### Completed Requirements

- Blank custom drafts now serialize a missing supported-edge palette as `[]`; the prior `null` value caused `canonicalEdge(...).find` to throw before the first user-defined connection could be added.
- Connection completion hit-tests the actual release coordinates before falling back to the event target, so touch/pen implicit pointer capture and equivalent captured releases cannot mistake the source connector for the destination.
- Selectable transparent edge hit areas remain behind nodes. Visible edge lines and arrowheads render after all nodes in a pointer-transparent foreground, while reconnect handles and the midpoint delete control render in a final controls overlay above the foreground geometry.
- Dynamic add, reconnect, delete-edge, delete-node, node movement, selection highlighting, arbitrary multi-edge/cycle geometry, ordinary page wheel scrolling, and Ctrl/Meta pinch-style zoom all update both the interaction and foreground edge surfaces.

### Regression And Validation Evidence

- The production real-Chrome fixture now enters through an actually empty Blank Automation, uses the visible Add first node flow, receives a custom multi-node graph, and reproduces a captured source `pointerup` at another node's connector coordinates. It failed first with no saved edge and then exposed the `null.find` exception before passing with exact candidate JSON persistence.
- The browser fixture asserts the destination connector wins coordinate hit-testing, the visible edge follows node elements in SVG paint order, and the foreground edge has `pointer-events: none`. Existing consecutive connection, cycle, selection, reconnection, deletion, node movement, wheel-scroll, and pinch-zoom assertions remain passing.
- `templ generate`, `gofmt`, `git diff --check`, and `go build ./...` pass; desktop emits only the documented non-failing newer-SDK linker warnings.
- `go test ./web/templates/pages -count=1 -timeout 120s` passes, including the production Chrome graph fixture.
- `go test ./internal/... -count=1 -timeout 120s` passes every internal package.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package.

## Phase 5 Foreground And Schedule Projection Follow-up

### Completed Requirements

- The custom builder’s persisted edge line and marker arrow are now fully opaque as well as DOM-foregrounded. They paint visibly over connector circles while retaining `pointer-events: none`; the transparent selectable hit surface remains behind nodes.
- Applying a real Blank `Schedule → Agent task` graph creates two distinct tasks: the Schedule node's task remains `scheduled` and owns the Scheduler row, while the Agent Task node remains an ordinary blocked `backlog` task until successful completion of the scheduled task activates it.
- The Schedules query follows the schedule’s project-scoped `automation_trigger_owners` identity to the exact immutable Automation version/node and exposes that schedule-node name to the calendar.
- Automation-owned calendar cards use the configured Schedule node's name and real scheduled task. The connected Agent Task is never substituted as the Scheduler row's task; ordinary non-Automation schedules continue using their existing task title and behavior.

### Regression And Validation Evidence

- The production Chrome regression failed first because the foreground line and marker computed to translucent colors; it now asserts opaque line/arrow paint, foreground DOM order, and pointer transparency after creating a real Blank connection.
- `TestAutomationBlankAppliedScheduleUsesScheduleNodeNameOnSchedulePage` failed first after a complete visible Blank create/connect/review/apply flow: the real Scheduler row was returned, but `/schedule` titled it with the target Agent task. It now proves the applied schedule is rendered as `Weekday review` and not `Review support queue`.
- `templ generate`, `gofmt`, `git diff --check`, and `go build ./...` pass; desktop emits only the documented non-failing newer-SDK linker warnings.
- `go test ./web/templates/pages -count=1 -timeout 120s` passes, including the production Chrome graph fixture.
- `go test ./internal/... -count=1 -timeout 120s` passes every internal package.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package.

## Phase 5 Schedule Resource Ownership Correction

### Completed Requirements

- Every applied custom Schedule node now compiles to its own visible `scheduled` task and one real Scheduler row linked to that task. A Schedule no longer borrows the connected Agent Task as its backing task.
- Agent Task and GitHub inbox nodes rooted at a Schedule normalize to ordinary `backlog` tasks. The custom builder no longer offers `Scheduled` as an Agent Task category, and Describe It/Chat use the same corrected contract.
- When a custom schedule becomes due, the existing Automation occurrence/dispatch machinery validates persisted project, Automation, immutable version, trigger node, and exact Schedule-task membership, then dispatches the Schedule node's own task. Successful completion resolves the persisted Schedule-to-Agent edge and target task membership through the existing task-chain path and activates the ordinary Agent Task.
- Runtime fails closed with no invocation or dispatch if the Scheduler row is repointed to the Agent Task or the Schedule node's exact task membership is absent. Completion handoff fails closed if the immutable trigger edge and target task do not match exactly.
- Maintained adapters retain their existing schedule-to-task behavior. Review/apply plans and compiler revisions include the additional custom Schedule task effect, and changed legacy-shaped custom schedules are replaced rather than silently reused.

### Regression And Validation Evidence

- `TestCustomAutomationPublicationCreatesUserConfiguredTaskAndSchedule` failed first because planning produced only one task effect. It now proves distinct scheduled/Agent tasks, exact Scheduler linkage, ordinary Agent category, Schedule-task dispatch, and fail-closed tamper handling with zero runtime state.
- `TestAutomationBlankAppliedScheduleUsesScheduleNodeNameOnSchedulePage` now proves the full visible Blank create/connect/review/apply path persists distinct tasks, links the Scheduler row to the Schedule task, leaves the Agent Task in Backlog, and renders the Schedule node on `/schedule`.
- Custom validation, Alert, GitHub, Describe It, canonical Chat, maintained Automation, handler, and production-browser focused suites pass with the corrected resource counts and categories.
- `templ generate` completes with zero pending generated updates; `gofmt`, `git diff --check`, and `go build ./...` pass. Desktop build/test emits only the documented non-failing newer-SDK linker warnings.
- `go test ./internal/... -count=1 -timeout 120s` passes every internal package.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package, including production real-Chrome Automation graph coverage.
- Direct managed-memory mutation remains unavailable in this task runtime. The authoritative `automation_graphs.md` topic correctly reflects Schedule resource ownership but must be reconciled with the later composable task fan-out and optional approval-result contract through an authorized writer before the final qualifying audit.

## Phase 5 Custom Builder Checklist

- [x] Reproduce the preset-only Blank defect in handler, draft-service, planner, and publication tests.
- [x] Register a dynamic `custom` compiler adapter while keeping maintained bootstrap registration restricted to Native/GitHub.
- [x] Make Blank create an empty custom draft instead of silently selecting Vision Driver.
- [x] Replace `Runtime behavior` plus `Design-only type` with one user-facing `Node purpose` control.
- [x] Remove preset required-node/required-connection errors from custom drafts.
- [x] Validate the initial Schedule → Agent task → Outcome capability handoff and fail closed for unsupported capabilities, handoffs, conditions, and invalid targets.
- [x] Plan and compile a user-named, user-configured custom Agent task and Schedule into real existing-domain resources.
- [x] Preserve draft-only geometry, project isolation, explicit Automation identity, confirmation, idempotency, trigger ownership, lifecycle, and no-inference boundaries.
- [x] Compile Agent task → Agent task handoffs through existing task machinery with exact Automation provenance and Live projection.
  - [x] Accept standalone ordinary tasks and deterministic task fan-out; reject only multiple task parents, cycles, and unsupported handoffs.
  - [x] Publish every configured Agent task as a visible OpenVibely task and atomically configure existing task relationships only after the version is ready.
  - [x] Reuse connected child tasks across executions while preserving task status, category, Agent definition, prompt handoff, and project ownership.
  - [x] Persist one idempotent work item/activity/transition for each task handoff and project the child execution on the exact connected node.
  - [x] Transition a completed terminal custom task to its connected Outcome using persisted Automation provenance.
  - [x] Add regression-first validation, publication, runtime projection, idempotency, and analogous unsupported-topology coverage; run the slice validation and fresh audit.
- [x] Add native approval/notification capability nodes through the existing Alert service.
- [x] Add GitHub issue/assignment/PR/review capability nodes through the existing GitHub services and human boundaries.
- [x] Exclude the old JSON-only hidden Workflow subsystem from Automation capabilities per direct user correction; it is being removed separately rather than presented as a surfaced feature.
- [x] Extend Describe It and Chat schemas/capability snapshots to generate the same surfaced custom graph contract.
- [x] Add focused and real-browser coverage for every custom capability and supported/unsupported analogous handoff class.
- [ ] Run generation, build, focused Automation, internal, and full validation; update managed memory; create a clean checkpoint; perform a fresh edit-free Phase 5/full-objective audit.

## Phase 5 Describe It And Chat Parity Checkpoint

### Completed Requirements

- Describe It now treats `custom` as a first-class generated adapter instead of forcing every request into Native SDLC, GitHub SDLC, or Vision Driver. Its model-facing schema enumerates the same surfaced Schedule, Agent task, Native Alert approval, GitHub lifecycle, and Outcome node configuration available in Blank.
- The generation contract documents deterministic capability composition: standalone tasks, task fan-out, Schedule/task actions and Outcomes, optional Native approval results, and the existing human-gated GitHub lifecycle. It rejects multiple task parents, cycles, unsupported conditions, human-gate bypasses, automatic issue assignment, hidden/internal capabilities, and merge/release/deployment authority.
- The bounded project capability snapshot now advertises the custom `task` role alongside all surfaced Alert/GitHub roles. Agent, skill, source-file, GitHub readiness, secret exclusion, deterministic ordering, and collection bounds remain unchanged.
- The one bounded repair prompt preserves valid custom intent and no longer replaces a requested custom graph with an unrelated preset.
- The Automations page and canonical Chat actions use the same capability snapshot, no-tools model generation, decode, normalization, validation, persistence, planning, confirmation, and compiler path. A fixed custom approval candidate now proves identical web/Chat persistence and confirmed Chat publication into the expected visible task and schedule.
- The canonical Chat registry explicitly describes custom/maintained-template parity while retaining only Template, Describe It, and Blank creation identities. It still exposes no arbitrary candidate payload and creates no runtime resource before explicit review and later confirmation.
- The old JSON-only hidden Workflow subsystem is intentionally absent from the generated custom contract per direct user correction and is being removed separately.

### Regression And Validation Evidence

- `TestAutomationDescriptionGenerationSupportsExpandedCustomBuilderContract` failed first because the generator required a maintained preset and then passed with exact surfaced custom roles, handoffs, safety boundaries, and no hidden Workflow role.
- `TestAutomationCapabilitySnapshotIsBoundedDeterministicAndSecretFree` failed first because `task` was absent and now covers every surfaced custom role without exposing prompts, skill bodies, task prompts, or GitHub credentials.
- `TestAutomationChatActionsUseCanonicalPipelineAndDeferConfirmationReceipt` now enters both the public web Describe It route and Chat with the same fixed custom approval candidate, proves identical normalized persistence, no draft-time task/schedule mutation, and deferred confirmation receipt creation.
- `TestAutomationCanonicalChatRuntimeExecutesPreviewDraftPlanAndConfirmedPublish` now runs the complete canonical Chat action sequence for a custom approval graph and proves explicit confirmation before one real task and schedule are published.
- Focused service, handler, and Chat registry regressions pass.
- `templ generate` completed with zero generated updates; `gofmt`, `git diff --check`, and `go build ./...` pass with only the documented non-failing macOS SDK linker warnings.
- The Automation-wide database/repository/service/handler/Chat/observability/page suite passes.
- `go test ./internal/... -count=1 -timeout 120s` passes every internal package.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package, including the production real-Chrome Automation graph coverage.

### Remaining Work

- Reconcile managed memory through an authorized direct writer if one becomes available, checkpoint the clean candidate, and perform the fresh edit-free full-objective audit.

## Phase 5 Custom GitHub Checkpoint

### Completed Requirements

- Blank users can add and configure `Create GitHub issue`, `Human assignment`, `GitHub inbox`, issue-specific `Implementation task`, `Open pull request`, and `Human review` nodes. The supported topology mirrors the real lifecycle: a producer Schedule/task creates an issue, a separate inbox Schedule polls assigned issues, assignment is the only approval edge into the inbox, implementation tasks are created per issue, and linked PRs wait for human review before an observed merge reaches the Outcome.
- Review and apply creates only the configured producer/inbox tasks and their schedules. GitHub issue, assignment, implementation-template, PR, and review effects are shown explicitly but create no external object during publication. Issues, implementation tasks, and PRs arise only through the existing GitHub runtime, task creation, task-PR, queue, and worker services.
- Runtime provenance resolves connected roles through exact project/Automation/immutable-version edges rather than preset node keys. Real issue creation/retry, assignment discovery, implementation creation, PR linkage, review waiting, and merge completion project onto the exact user-named nodes and edges.
- Automation-bound issue actions always use the selected project's repository, override model-provided labels with the immutable node configuration, and reject assignees before calling GitHub. Human GitHub assignment therefore remains authoritative.
- The existing `create_task` action receives the immutable connected implementation template before task creation, including category, priority, configured instructions/source context, optional Agent selection, and PR guidance. The real implementation task remains issue-specific and carries the source issue and Automation work-item provenance.
- The existing `github_open_pull_request` action uses the immutable configured base/draft values for Automation-bound implementation tasks. It opens or reuses a linked PR but cannot approve, merge, release, or deploy. Existing cached PR reconciliation observes open/closed/merged state; only observed merge completion reaches the configured terminal Outcome.
- Custom publication now requires the same usable GitHub authentication, explicit project repository, and enabled project approval inbox as the maintained GitHub adapter. Missing capabilities produce no publication effects.
- No generic edge interpreter, parallel GitHub/task runtime, automatic assignment, PR approval, merge, release, deployment, Register Existing, legacy inference, migration, or backfill path was added.

### Regression And Validation Evidence

- `TestCustomAutomationValidatesGitHubHandoffsAndRejectsHumanBoundaryBypasses` covers the valid two-schedule lifecycle plus assignment/review bypasses, wrong gate conditions, and forbidden issue-assignee configuration.
- `TestCustomAutomationPublicationConfiguresGitHubRuntimeWithoutCrossingHumanGates` covers fail-closed integration readiness, exact publication effects, no shared implementation task, deterministic prompts, immutable issue repository/labels, assignment enforcement, issue retry, assigned discovery, server-applied implementation configuration, PR base/draft enforcement, exact runtime transitions, waiting review, and observed merge outcome.
- `TestAutomationBlankBuildsCustomRunnableTaskAndSchedule` covers every GitHub node purpose/control and the public connect-then-select `Assigned in GitHub` flow. The production Chrome Automation graph test asserts the full Blank node palette through the visible Add first node dialog.
- The maintained GitHub adapter regressions `TestAutomationRuntimeGitHubIssueInboxAndPRProvenance` and `TestAutomationExternalPullRequestRefreshIsExplicitCachedAndReconcilesProjection` pass with the connected-role runtime resolver.
- `templ generate`, `gofmt`, `git diff --check`, and `go build ./...` pass; desktop emits only the documented non-failing newer-SDK linker warnings.
- `go test ./internal/... -count=1 -timeout 120s` passes.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package, including production real-Chrome Automation graph coverage.

### Remaining Work

- Describe It/Chat parity is completed by the later Phase 5 checkpoint above. The old hidden Workflow subsystem is intentionally excluded per direct user correction.
- Reconcile the authoritative `automation_graphs.md` managed topic when a scoped mutation path is available, then perform the fresh full-objective audit.

## Phase 5 Native Alert Approval Checkpoint

### Completed Requirements

- Blank users can add and configure `Create notification` and `Human approval` nodes alongside Schedule, Agent task, and Outcome nodes. Nothing is preassembled; incomplete connections remain editable draft setup items, including a newly connected approval edge before its Approved/Rejected result is selected.
- Publishable Native Alert paths compose with standalone, chained, or fan-out task graphs: `Schedule/task → Create notification → Human approval → optional approved/rejected Outcomes`. Conditions remain restricted to exact gate results; duplicate result edges, cycles, wrong-role connections, and unsafe analogous topologies fail closed.
- Review and apply distinguishes visible task/schedule resource effects from notification-handoff and human-gate configuration. Publication creates no Alert; the compiled Agent task uses the existing `create_notification` runtime capability, and the real pending project Alert is created only when that task runs.
- Alert creation resolves the exact user-owned action and gate through immutable version nodes/edges rather than preset keys. Human approve, reject, and dismiss decisions remain authoritative in the existing Alert service and append exact configured Automation transitions transactionally.
- Runtime state projects the real pending Alert as waiting on the selected gate and the real decision on the selected Outcome. Alert/task links continue to use native drill-down resources rather than graph-only records.
- Immutable task-chain activation rebuilds notification guidance from the causal published version. Mutable task prompt/chain tampering cannot erase or redirect the configured human handoff.
- Idempotent retries from the same immutable source reuse one Alert and one transition set. A same-key Alert previously created outside that Automation is rejected rather than inferred or backfilled into the graph.
- Native approval authorizes only the human decision state. This slice adds no merge, review, release, deployment, generic graph executor, parallel Alert/task runtime, Register Existing, legacy detection, heuristic inference, migration, or backfill path.

### Regression And Validation Evidence

- `TestCustomAutomationValidatesNativeAlertApprovalHandoffsAndRejectsAnalogousUnsafeBranches` covers task-chain composition, direct Schedule-to-notification composition, shared valid producers, optional/terminal result branches, duplicate result states, wrong targets, and unsupported conditions.
- `TestCustomAutomationPublicationRunsNativeAlertApprovalOnExactUserNodes` covers planning, real resource publication, immutable prompt compilation, mutable-state tampering, idempotency collision isolation, pending Live state, a configured approved Outcome, and safe terminal projection at the gate when the rejected Outcome is omitted.
- `TestAutomationBlankBuildsCustomRunnableTaskAndSchedule` covers the visible node purposes, configuration controls, save-before-result selection, and Approved/Rejected edge persistence through the web builder.
- `templ generate`, `gofmt`, `git diff --check`, and `go build ./...` pass; desktop emits only the documented non-failing newer-SDK linker warnings.
- `go test ./internal/... -count=1 -timeout 120s` passes.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package, including the production Chrome Automation graph coverage.

### Remaining Work

- GitHub lifecycle capability nodes and Describe It/Chat parity are completed by the later Phase 5 checkpoints above. The hidden Workflow subsystem is excluded per direct user correction.
- Reconcile the authoritative `automation_graphs.md` managed topic when a scoped mutation path is available, then perform the fresh full-objective audit.

## Phase 5 Agent-Task Handoff Checkpoint

### Completed Requirements

- Custom graphs accept standalone ordinary tasks, deterministic task chains, and task fan-out rooted either at a Schedule or an ordinary task. Categories remain normal task settings; multiple task parents, cycles, unsupported capability edges, and cross-project/version/resource targets fail closed.
- Confirmed publication creates every configured node as a visible normal OpenVibely task. Downstream tasks remain blocked during compilation, and the existing publication transaction atomically installs parent IDs, chain configuration, Agent definitions, prompts, categories, priorities, and statuses with the immutable version switch.
- Runtime handoff uses the existing task-chain and Worker paths. It reactivates the published child task, carries parent output into the configured child prompt, preserves lineage, reloads Automation context through the child task activity, and creates one work item/activity/edge transition at the exact connected node.
- Causal runtime behavior resolves the source version's persisted custom edge and task membership rather than trusting the task's latest mutable chain JSON. Old invocations therefore retain their immutable graph-version semantics after later task configuration changes.
- Handoff retry is idempotent. A crash after the local handoff commit can resubmit the still-pending Active child through the existing deduplicating Worker queue and Scheduler recovery; a busy child records a visible blocked work item/transition rather than silently dropping work.
- A terminal custom task projects completion either to its connected Outcome or, when Outcome is omitted, to the task node itself. Invocation-only terminal activity safely creates the required work item, and empty terminal aggregates use zero rather than SQLite `NULL`.

### Changed Files

- Task-chain identity and completion context: `internal/models/task.go`, `internal/service/llm_service.go`, `internal/service/llm_workflow_chain.go`.
- Custom topology normalization/validation, deterministic planning, compilation, and atomic publication: `internal/service/automation_draft_service.go`, `internal/service/automation_publication_planner.go`, `internal/service/automation_compiler.go`, `internal/repository/automation_publication_repo.go`.
- Atomic task activation and runtime projection: `internal/repository/task_automation_repo.go`, `internal/repository/automation_runtime_repo.go`.
- Builder category presentation and generated output: `web/templates/pages/automations.templ`, `web/templates/pages/automations_templ.go`.
- Regression coverage: `internal/service/automation_draft_service_test.go`, `internal/service/automation_publication_service_test.go`.

### Validation And Audit

- Regression-first focused tests initially failed because chain configuration had no server-owned target task/node identity. The current regressions cover standalone tasks, task chains and fan-out, multiple-parent and cycle rejection, visible task creation, blocked-before-publication safety, atomic topology setup, publication retry, mutable-chain tampering, exact target-node context, idempotent retry/resubmission, busy-child blocking, connected Outcome completion, and the original single-task custom path.
- `templ generate`, `gofmt`, `git diff --check`, and `go build ./...` pass; desktop emits only the documented non-failing newer-SDK linker warnings.
- `go test ./internal/... -count=1 -timeout 120s` passes.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package, including production Chrome Automation graph coverage.
- The fresh slice audit found and repaired commit-before-submit recovery, single-task terminal projection/empty aggregate handling, busy-child provenance, published edge/resource verification, and immutable-version handoff resolution. No task/worker/queue/Workflow/Alert/GitHub parallel runtime, generic edge interpreter, implicit Automation identity, legacy inference, Register Existing, migration/backfill, confidence scoring, or changed human merge/release/deploy authority was introduced.

### Remaining Work

- Native approval, configurable GitHub lifecycle nodes, and described/Chat parity are completed by the later Phase 5 checkpoints above. The hidden Workflow subsystem is excluded per direct user correction. Remaining work is managed-memory reconciliation and the fresh full-objective audit.
- The authoritative `automation_graphs.md` managed view now reflects the surfaced custom GitHub and Schedule/task parity checkpoints, but it still records task branching as unsupported and must be updated with the later composable fan-out and optional approval-result contract. This runtime exposes `memory_view` but no scoped memory mutation action. Per the managed-memory runbook and explicit user direction, do not delegate or edit an untracked copy; reconcile the authoritative topic once a scoped writer is available and before the qualifying final audit.

## Phase 4 Checklist

- [x] Add publication attempts, publication steps, and Chat confirmation receipts with composite Automation/version ownership, idempotent attempt/step keys, transactional receipt consumption, indexes, project cascade, and migration up/down coverage.
- [x] Add a strict versioned structured draft candidate/schema with bounded known fields; reject generated database/project IDs, URLs, executable code, SQL, arbitrary tools, unknown configuration, duplicate keys, dangling/cross-version edges, unsupported cycles, and oversized graphs/configuration.
- [x] Add canonical Native SDLC, GitHub SDLC, and Vision Driver templates accepted by registered adapters; every publishable topology must map to one adapter and no generic Automation edge interpreter may be introduced.
- [x] Build a bounded, deterministic, project-scoped capability snapshot for agents, skills, integrations, source files, and reusable resources without credentials, prompts, worktree contents, private messages, provider identity, or unbounded listings.
- [x] Normalize Template, Describe It, Blank, fixed Chat candidate, and active-version clone inputs through one deterministic draft service with server-generated IDs/layout and shared validation.
- [x] Use the provider-neutral existing no-tools model and usage path for described generation, strict JSON output, and at most one bounded repair attempt; preview must remain ephemeral and draft creation must persist only definition data.
- [x] Persist mutable draft versions/nodes/edges while keeping published topology immutable; clone active versions into new drafts and retain all historical invocation/work-item version references.
- [x] Add constrained schema-driven node/edge editing and validation with explicit affected-node and summary errors; saving or validating a draft must create no task, schedule, Alert, issue, execution, goal, Workflow, or PR.
- [x] Build exact canonical publication plans with create/reuse/update/disable/unchanged effects and SHA-256 revisions over normalized topology, adapter/compiler versions, dependencies, integrations, and ordered effects while excluding volatile runtime state.
- [x] Add golden canonical-plan revision fixtures and reject non-finite/unknown values, changed drafts/dependencies, stale revisions, and unsupported topologies before publication mutation.
- [x] Add a resumable idempotent compiler journal that persists each step before mutation, reconciles pending/running/ambiguous/failed steps by stable target key, uses existing Task/TaskRepo, ScheduleRepo cadence, Alert/goal/Workflow/GitHub boundaries, and never duplicates runtime resources after retries/crashes.
- [x] Publish only after every required resource and membership exists; failed publication must keep the draft unpublished and prior version active while returning exact created/reused resources.
- [x] Add safe pause/resume/archive behavior that disables/enables only exclusively owned trigger schedules, retains paused ownership, never disables shared worker/inbox resources, and preserves all history.
- [x] Add host-issued 30-minute signed Chat confirmation receipts bound to project, automation, version, plan revision, principal, thread, stored plan message, and a later affirmative user input; reject same-turn, expired, stale, cross-scope, ambiguous, negative, and replayed confirmations.
- [x] Register `DomainAutomations` plus `preview_automation_description`, `create_automation_draft`, `plan_automation_publication`, and `publish_automation_draft` in the canonical Chat registry with project-aware surface/mode policies and the existing typed handler map; ordinary task agents receive no definition mutation capability.
- [x] Ensure preview/planning do not persist definitions/runtime resources or invoke ordinary mutation actions; draft creation persists no runtime resources; publish reports success only after resources and published version are durable.
- [x] Add Automations-page `New Automation` with Template, Describe It, and Blank paths only, draft graph/editor, assumptions/warnings/errors, publish preview, explicit web confirmation, failure preservation, and success navigation to Live.
- [x] Add direct-load/HTMX-safe builder behavior, accessible keyboard/status labels, responsive/mobile/Wails layouts, reduced-motion behavior, bounded payloads, and generated templ output.
- [x] Add migration/repository/service/compiler/handler/Chat registry/executor/UI tests for all draft sources, deterministic normalization, no-mutation boundaries, stale/failed/idempotent publication, confirmation attacks, project isolation, adapter ownership, lifecycle safety, and page/Chat parity using one fixed candidate.
- [x] Run Phase 4 and full validation chains, perform a fresh phase and Definition of Done audit against concrete code/tests, repair every material finding, update this file and managed memory, and create the final checkpoint.

## Phase 3 Checklist

- [x] Add project-scoped, bounded invocation history ordered by effective occurrence time `COALESCE(scheduled_for, started_at, created_at), id` with an opaque stable cursor and a maximum initial page of 50.
- [x] Add project-scoped, bounded work-item history ordered by `(created_at, id)`, optional status filtering, and an opaque stable cursor.
- [x] Add project-scoped invocation graph detail that includes only activities and transitions carrying the selected invocation ID while retaining the invocation's immutable version topology.
- [x] Add project-scoped work-item history spanning every related invocation, with bounded activities and stable cursor-paginated transitions ordered by `(occurred_at, id)`.
- [x] Add transition replay frames derived solely from persisted append-only transitions; do not query current Task, Alert, execution, issue, or PR state to reconstruct historical positions.
- [x] Define funnel conversion as persisted `entered` transitions at each version node divided by the first topology node with a persisted entry, and define node duration as the elapsed time from the first persisted transition into a node to the next persisted transition for that work item.
- [x] Add bounded set-based funnel, average-duration, recent-failure, and bottleneck summaries with no per-node or per-work-item query loop.
- [x] Compute and persist Automation health independently from lifecycle: unknown before a completed invocation, healthy after successful trigger/dispatch with no active systemic condition, degraded for isolated recent failures/blocked work, and unhealthy for repeated trigger/dispatch failures.
- [x] Render History navigation, invocation selection, cross-invocation work-item selection, replay controls/timeline, failure and bottleneck summaries, and Chart.js funnel/duration charts with direct-load and HTMX-safe initialization/cleanup.
- [x] Keep all payloads compact and escaped; omit prompts, model output, diffs, Alert bodies, credentials, and provider identity metadata.
- [x] Reject foreign-project automation/invocation/work-item IDs and invalid/tampered cursors according to existing handler conventions.
- [x] Add repository/service/handler/UI tests for stable pagination under ties, occurrence isolation, cross-invocation lifetime, persisted-transition replay, defined metric boundaries, health/lifecycle independence, empty/partial states, and project isolation.
- [x] Run Phase 3 generation/build/tests, perform a fresh contract audit, repair all material findings, update this file with concrete evidence, and create a checkpoint commit before Phase 4.

## Phase 2 Checklist

- [x] Add `executions.dispatch_id` and durable invocation, leased outbox, task-run reservation, work-item, position, thread-input binding, activity, activity-resource, and append-only transition tables in the required dependency order.
- [x] Enforce composite project/automation/version/node/invocation/work-item/activity/edge parents, nullable-reference semantics, canonical key uniqueness, project cascade, query indexes, and migration up/down coverage with foreign keys enabled.
- [x] Add server-derived `AutomationContext`, multi-binding `AutomationBinding`, and prepared `AutomationDispatchEnvelope` models; never accept arbitrary runtime project/automation/work-item identity.
- [x] Add one atomic Automation-owned schedule claim that verifies expected due time, creates or resolves one occurrence, creates outbox/reservation or a terminal skipped invocation, applies existing recurring task eligibility/category rules, and compare-and-swap advances the schedule.
- [x] Preserve ordinary non-Automation schedule behavior; coalesce running/reserved Automation occurrences, clear skipped one-time `next_run`, and advance recurring schedules to the first future cadence boundary.
- [x] Add owner-only leased outbox claim/renew/retry/failure behavior with bounded backoff and stable dispatch identity.
- [x] Extend `TaskRepo` with one transactional Automation dispatch claim that validates reservation/lease, atomically claims the existing task, and creates or resolves exactly one execution by `dispatch_id`.
- [x] Add `WorkerService.SubmitPrepared` using existing global/project/model capacity, cancellation, lifecycle, LLM/tool execution, completion, broadcaster, and cleanup semantics; do not add a second worker pool or executor.
- [x] Finalize invocation/outbox/activity state and release reservations from terminal existing-worker completion paths; recover a crash after DB claim without duplicate execution or activity.
- [x] Extend `ThreadInputRepo.ClaimQueuedForTaskExecution` to load durable bindings and upsert Automation activities while retaining existing FIFO, cancellation, retargeting, stale-turn, and guarded-promotion behavior.
- [x] Add canonical idempotent work-item/activity/transition repository operations with transactional position/status projection and stable bounded queries.
- [x] Instrument Native Alert creation, decision, claim/implementation linkage, processing completion/failure, and implementation execution without changing Alert authority or approval boundaries.
- [x] Instrument GitHub issue creation and task/PR linkage using canonical persisted repository-qualified identities and external-call reconciliation rules; never infer provenance from prompts, titles, or issue URLs.
- [x] Preserve one work item across invocations/versions, bind activities to the work item's immutable origin topology, and support several Automation bindings on one shared inbox execution.
- [x] Add a set-based live graph projection with deterministic display-state precedence, concurrent invocation/work-item counters, waiting-human distinction, recent completion cutoff, bounded node resources, and no implicit GitHub reads.
- [x] Add Automation invalidation event types through the existing broadcaster/SSE path and periodic projection reconciliation that never overwrites authoritative Task, Execution, Alert, Goal, Workflow, thread-input, issue, or PR state.
- [x] Add project-scoped live graph and node-resource handler/UI paths with escaped compact summaries, no prompts/outputs/diffs/secrets, periodic visible-page refresh, and direct-load/HTMX behavior.
- [x] Add focused migration, repository concurrency/idempotency, scheduler, dispatcher/worker, thread-input, Alert, GitHub, projection, handler, UI, restart/reconciliation, no-inference, and foreign-project tests mapped to the Phase 2 runbook.
- [x] Run Phase 2 generation/build/tests, perform a fresh contract audit, repair all material findings, update this file with concrete evidence, and create a checkpoint commit before Phase 3.

## Phase 1 Checklist

- [x] Add project-owned Automation definitions with lifecycle and health metadata.
- [x] Add immutable published versions, nodes, edges, definition-resource memberships, and exclusive trigger ownership with composite parent constraints.
- [x] Add migration up/down coverage, project cascade coverage, and required Phase 1 indexes.
- [x] Implement project-scoped repository list/detail queries and atomic registered publication.
- [x] Reject foreign-project resources, unsupported adapter keys, malformed topology bindings, duplicate memberships, and concurrent trigger ownership.
- [x] Keep published definitions immutable and expose no ordinary delete path.
- [x] Add canonical maintained `native_sdlc` and `github_sdlc` adapter templates.
- [x] Add idempotent registration by stable project-scoped key using actual task/schedule IDs only.
- [x] Update maintained Native/GitHub bootstrap flows to register their newly created resources explicitly.
- [x] Render a bounded project-scoped Automations portfolio and Automation Definition detail view.
- [x] Render current persisted task/schedule resource summaries without title, prompt, lineage, Alert, or GitHub inference.
- [x] Allow worker/inbox task memberships across automations while keeping active trigger schedules exclusive.
- [x] Enforce foreign-project isolation in repository, service, handler, and UI paths.
- [x] Add focused repository, service, handler, UI, bootstrap-capability, and no-inference tests.
- [x] Run Phase 1 generation/build/tests, perform a fresh contract audit, repair all material findings, update this file, and checkpoint commit.

## Completed

- Added migration `113_automation_definitions.sql` with definitions, versions, nodes, edges, definition resources, exclusive trigger owners, composite ownership constraints, indexes, and reversible down migration.
- Added Automation domain models, project-scoped repository reads, bounded resource summaries, and one resumable-safe `BEGIN IMMEDIATE` registered-publication transaction.
- Added maintained Native SDLC and GitHub SDLC adapter templates plus stable-key, same-project, actual-resource registration.
- Added selected-bootstrap-skill-only `register_automation_resources` runtime capability to initial task and task-thread execution paths; ordinary tasks do not receive it.
- Updated the maintained Native/GitHub bootstrap skills with canonical node bindings and explicit registration steps.
- Added project-scoped portfolio and read-only SVG Definition views, native task/schedule links, HTMX navigation, generated templ output, and sidebar navigation.
- Added migration, repository/service, concurrency, runtime capability, no-inference, handler, UI, shared-resource, cascade, and foreign-project isolation tests.
- Added migration `114_automation_runtime.sql` with unique execution dispatch identity, invocation/outbox/reservation state, long-lived work items and positions, queued-input bindings, activities/resources, append-only transitions, composite ownership constraints, indexes, and reversible down behavior.
- Added atomic Automation schedule occurrence claims, coalesced skipped occurrences, owner-only outbox leasing/backoff, transactional task/execution claiming, and prepared dispatch submission through the existing `WorkerService` capacity/lifecycle/cancellation/completion pipeline.
- Added restart reconciliation for precreated and submitted executions, terminal dispatch/invocation/activity finalization, exact execution projection repair, reservation cleanup, and ordinary-worker exclusion for Automation-reserved tasks.
- Added server-derived multi-binding context propagation through scheduled execution, child lineage, `send_to_task`, queued-input promotion, and shared inbox execution without adding a second queue or executor.
- Added transactional Native Alert provenance from creation through approval, claim/release/reclaim, implementation linkage, processing, execution, and completion while retaining Alert authority and approval semantics.
- Added repository-qualified GitHub issue/PR identities, pre-call external activity reservation, ambiguous-mutation refusal, exact discovered-issue-to-task binding, issue-to-implementation-to-review transitions, and no prompt/title/URL inference.
- Added set-based live graph counters/edge activity, deterministic display precedence, compatible prior-version mapping plus visible unmapped work, bounded node resources, compact Automation events, direct/HTMX live UI, event debounce, and visible-page periodic refresh.
- Added bounded project-scoped invocation, work-item, activity, and transition history with collection/filter-bound opaque cursors and stable tie ordering.
- Added immutable-version invocation graphs, set-based touched-node projection, cross-invocation work-item timelines, and deterministic replay frames derived only from append-only persisted transitions.
- Added set-based funnel, first-arrival-to-next-transition duration, recent-failure, and current bottleneck metrics plus persisted health evaluation independent from lifecycle state.
- Added direct-load and HTMX History, invocation, and work-item views with Chart.js lifecycle cleanup, activity/transition paging, replay controls, compact escaped payloads, empty/partial states, and project isolation.
- Added migration `115_automation_publication.sql` with draft metadata, leased publication attempts, ordered idempotent steps, host confirmation receipts/input markers, composite ownership, indexes, project cascade, and reversible down behavior.
- Added one strict deterministic draft schema/service shared by Template, Describe It, Blank, fixed Chat candidates, and published-version cloning, with canonical Native/GitHub/Vision templates, server layout, bounded capability snapshots, no-tools model generation, one repair, and unsafe/oversized/non-finite rejection.
- Added mutable draft persistence and constrained builder editing without runtime mutation, immutable published topology retention, exact canonical publication plans and golden revisions, stale draft/dependency protection, and volatile-state exclusion.
- Added leased resumable compilation through existing TaskService/TaskRepo and ScheduleRepo behavior, disabled-until-published trigger creation, stable target reconciliation, partial-resource evidence, atomic membership/ownership publication, prior-version preservation, and immediate Live topology switching.
- Added safe pause/resume/archive semantics that touch only exclusive trigger owners, preserve configured-disabled triggers, retain paused ownership, release archived ownership, and leave shared worker/inbox resources untouched.
- Added 30-minute host-signed Chat/web confirmation receipts, durable post-plan issuance, exact later-command marking, scope/expiry/replay enforcement, and canonical registry/executor actions for preview, draft, plan, and confirmed publish.
- Added Automations-page Template, Describe It, and Blank paths only, generation progress/errors, draft graph/editor, assumptions/warnings, explicit publication preview/confirmation, partial-failure preservation, HTMX/direct-load behavior, and successful Live navigation.
- Kept maintained setup registration explicitly restricted to Native/GitHub even though Vision Driver is a registered draft-publication adapter; no Register Existing, detection, inference, migration, scoring, or backfill path exists.

## Changed Files

- Phase 1 migration/model/repository: `113_automation_definitions.sql`, Automation definition models/repository, adapter/registration services, and migration tests.
- Phase 2 migration/model/repository: `114_automation_runtime.sql`, Automation runtime models/repositories, execution/task/thread-input integration, Alert projection transactions, and migration/runtime tests.
- Runtime/services/wiring: Scheduler ownership routing, prepared Worker submission, dispatcher/reconciler, execution finalization, server-derived context propagation, Alert and GitHub runtime provenance, compact broadcaster events, and server lifecycle wiring.
- UI/handlers: project-scoped live/definition/node-resource handlers, Automation tests, portfolio/live/definition templates and generated output, sidebar navigation/project switching/SSE forwarding.
- Phase 3 history/replay/metrics: Automation history models and repository, graph-service history methods, reconciler health evaluation, History/invocation/work-item handlers and routes, History/replay/Chart.js templates, generated output, and focused service/handler tests.
- Phase 4 definition/publication: migration 115, draft/publication/confirmation models and repositories, capability/draft/generation/planner/compiler/confirmation/lifecycle services, canonical Chat registry/executor wiring, builder/lifecycle handlers and routes, server wiring, task/schedule/agent bounded repository extensions, Automations templates/generated output, and migration/service/handler/registry regressions.
- Maintained setup guidance and schemas: Native/GitHub bootstrap skill bodies and exact GitHub source-issue fields on the existing `create_task` action.

## Current Decisions

- The feature vision is governed by [VISION.md](./VISION.md).
- The technical contract is governed by [IMPLEMENTATION.md](./IMPLEMENTATION.md).
- Implementation proceeds one phase at a time in the order defined by the runbook.
- Existing OpenVibely domain services and records remain authoritative.
- Backward compatibility is not required: pre-feature resources are never detected or backfilled into Automations.
- Automations appear only through explicit publication or maintained Native/GitHub setup registration.
- The Automations page offers Template, Describe It, and Blank creation paths; it does not offer Register Existing.

## Validation

- Phase 1 `templ generate`, `go build ./cmd/server`, focused Automation/migration tests, and `go test ./internal/... -count=1 -timeout 60s`: passed.
- Phase 2 focused `go test ./internal/database ./internal/repository ./internal/service ./internal/handler -run 'TestMigration114|TestAutomation' -count=1 -timeout 180s`: passed.
- Phase 2 `templ generate` and `go build ./cmd/server`: passed after all audit repairs.
- Phase 2 full `go test ./internal/... -count=1 -timeout 60s`: passed. The first broad attempt found a touched GitHub bootstrap wording assertion; restoring the required sentence while retaining explicit source-issue fields repaired it, and the fresh full rerun passed every package.
- Phase 3 focused `go test ./internal/service -run 'TestAutomationHistory' -count=1 -timeout 120s` and `go test ./internal/handler -run 'TestAutomationPagesRenderRegisteredDefinitionsAndEnforceProject' -count=1 -timeout 120s`: passed after audit repairs.
- Phase 3 `go test ./internal/repository ./internal/service ./internal/handler -run 'TestAutomation' -count=1 -timeout 180s`: passed.
- Phase 3 final `templ generate`, `go build ./cmd/server`, and `go test ./internal/... -count=1 -timeout 60s`: passed every package; `internal/handler` completed in 40.104s and `internal/service` in 42.678s.
- Phase 4 focused `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol -run 'TestMigration115|TestAutomation' -count=1 -timeout 180s`: passed.
- Phase 4 final `templ generate`, `go build ./cmd/server`, and `go test ./internal/... -count=1 -timeout 60s`: passed every package; `internal/handler` completed in 41.289s and `internal/service` in 44.097s.
- Final checkpoint revalidation after interrupted response: `templ generate`, `go build ./cmd/server`, focused `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol -run 'TestMigration115|TestAutomation' -count=1 -timeout 180s`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`: passed every package. The macOS desktop linker emitted only the documented newer-SDK warning and exited successfully.

## Audit Findings And Repairs

- Repaired a dedicated-connection deadlock by loading the published return snapshot on the publication connection before commit/release.
- Added real successful runtime-action execution coverage after the first audit found only capability-scoping coverage.
- Added simultaneous registration coverage proving one claimant wins an exclusive trigger schedule and the losing publication rolls back completely.
- Rejected `shared` relation metadata for exclusive trigger schedules.
- Made idempotent setup reruns refresh stable Automation display metadata without creating a new version.
- Confirmed there is no Register Existing surface, legacy discovery, title/prompt matching, heuristic inference, migration, confidence scoring, or pre-feature graph backfill.
- Repaired parallel-branch completion so one terminal branch removes only its own position and a work item closes only after all positions, nonterminal activities, Alert claims, and queued inputs are gone.
- Excluded Automation-reserved tasks from ordinary Active-task discovery and `ClaimTask`, preventing Scheduler/Worker races that bypass prepared dispatch ownership.
- Closed stale Native Alert claim activities on release and reclaim, leaving exactly one current claim and preserving correct live counters.
- Replaced brittle duplicate-submit string matching with a typed worker sentinel; reclaimed dispatches already present in the worker queue are acknowledged and remain restart-recoverable instead of consuming failure attempts.
- Added concurrent schedule-poller and shared-task reservation coverage, post-ack reconciler resubmission coverage, actual issue-linked implementation PR coverage, thread-input project-cascade evidence, and direct-load plus HTMX live-page evidence.
- Repaired Automations project switching and normalized the touched shared SSE listener while retaining existing task/chat/file event behavior.
- Restored the established GitHub bootstrap instruction required by catalog tests while keeping exact source issue/repository provenance fields.
- Repaired health evaluation so stale historical failures do not keep an automation degraded after newer successful terminal invocations; health now uses the latest three terminal results plus current blocked/failed positions without changing lifecycle.
- Repaired aggregate failure timestamp scanning from SQLite text and added recent-failure plus waiting-bottleneck assertions.
- Repaired duration metrics to start at the first persisted arrival of any state, so waiting/blocked human-gate time is measured until the next work-item transition.
- Reduced replay JSON to state, occurrence time, and positions so transition metadata, event keys, and internal resource identifiers do not enter the browser DOM.
- Added stable tie-pagination and cursor-binding coverage for invocations, work items, activities, and transitions; invalid cursors/status filters now return controlled 400 responses.
- Decoupled invocation graph highlighting from paginated rows through one bounded set-based touched-node query and exposed independent activity pagination for invocation/work-item history.
- Added direct-load and HTMX assertions for all three History surfaces, Chart.js cleanup, replay controls, empty states, compact payloads, and foreign-project 404 isolation.
- Repaired publication concurrency by acquiring the existing durable attempt lease before compiler effects; concurrent compilers now serialize and retries reuse the completed attempt.
- Preserved already-journaled resource IDs on ambiguous status updates and reconciled committed tasks by stable compiler identity, so partial failures report exact visible resources and retry without duplication.
- Repaired lifecycle resume to restore each published trigger's configured enabled state instead of enabling every owned schedule.
- Added failed replacement evidence proving the prior published version and trigger remain active, then verified successful retry creates one new cadence-specific trigger, disables/releases the superseded trigger, and reuses the task.
- Expanded canonical plan dependencies to include GitHub inbox enabled state, added GitHub and Vision golden hashes, and proved layout/messages/next-run/last-run do not change a revision while compilation configuration does.
- Enforced one shared 64 KiB candidate bound for raw and in-memory/page inputs, rejected non-finite JSON plus URL/executable/SQL values, and retained strict unknown-field/topology/config rejection.
- Added independent tampered-token, project, version, principal, thread, same-turn, ambiguous-command, unmarked-input, replay, and expiry confirmation assertions.
- Added end-to-end canonical runtime execution of all four Automation Chat actions through the shared handler/executor, including no-tools preview, draft-only persistence, durably stored plan, host-marked later confirmation, and truthful durable publication.
- Added visible described-generation progress and in-page validation failure rendering, project-scoped draft/plan/publish 404 isolation, and partial publication resource rendering.
- Repaired the maintained registration boundary after Vision Driver joined the shared adapter registry: registration remains Native/GitHub-only, while Vision is available only through explicit draft publication.
- Reconfirmed no generic edge interpreter or parallel task/worker/queue/Workflow/Alert/GitHub system was introduced.

## Definition Of Done Audit

1. Multiple project graphs: `TestAutomationRegistrationExplicitIdentityAndIsolation` and portfolio handler assertions cover multiple independent cards.
2. Explicit Native/GitHub registration: maintained adapter/runtime tests use actual IDs and reject Vision/custom registration; no title-derived identity exists.
3. Concurrent Live states: `TestAutomationLiveDisplayStatePrecedencePreservesMixedCounters` and overlapping-invocation tests cover running, waiting-human, blocked, failed, and recent completion.
4. Resource drill-down: Automation page and node-resource handler tests link compact native/GitHub resources to their authoritative surfaces.
5. Reload/restart durability: atomic dispatch, acknowledged-prepared-execution, and terminal reconciliation tests rebuild exact persisted positions.
6. Immutable historical versions: clone, invocation-history, work-item lifetime, and replay tests retain origin/version topology.
7. Shared provenance: shared-inbox, multi-binding, old-work-item/new-invocation, child, and thread-input tests preserve causal identity.
8. Project isolation: definition/live/history/draft/plan/publish plus composite-constraint and foreign-ID tests reject cross-project access.
9. Human boundaries: Native Alert and GitHub assignment/PR provenance tests preserve existing approval, review, merge, release, and deployment authorization.
10. Existing primitives: Scheduler, TaskRepo, ThreadInputRepo, WorkerService, AlertService, goal/lineage, GitHub linkage, and broadcaster paths are extended; no hidden engine exists.
11. Shared definition model: all templates and builder publication persist the same versions/nodes/edges used by Live and History.
12. Explicit creation paths: builder tests expose only Template, Describe It, and Blank; maintained registration remains Native/GitHub-only.
13. Chat creation/confirmation: canonical runtime and confirmation attack tests cover persisted draft, visible plan, later exact command, and activation.
14. Page/Chat parity: fixed-candidate handler assertions and shared draft/planner/compiler services prove identical normalization/validation/publication behavior.
15. Registered topology ownership: all three templates validate through registered adapters; arbitrary nodes, edges, conditions, cycles/topologies, tools, code, SQL, URLs, and unknown config are rejected.
16. Idempotency: confirmation replay, leased publication, ambiguous task recovery, schedule journaling, trigger ownership, dispatch, work-item/activity/event keys, and concurrent polling tests cover retries/crashes.
17. Validation breadth: migration, repository, service, handler, Chat registry/executor, runtime, SSE/live UI, accessibility/status, restart, generated-template, desktop, and full repository suites pass.

## Post-Completion UI Repairs

### Reopened Phase 4 Visual Builder Contract

- [x] Make Blank creation open a genuinely empty canvas without a topology/template selector or pre-populated nodes and transitions.
- [x] Expose only nodes and transitions supported by the draft's registered adapter; permit add/remove operations while rejecting unknown nodes, arbitrary edges, and transitions whose endpoints are absent.
- [x] Persist incomplete safe drafts with explicit `missing_node`/`missing_edge` validation, while continuing to block publication until the registered topology and required configuration are complete.
- [x] Preserve user-authored node positions through normalization and update, with drag and keyboard-arrow movement plus Reset layout to canonical adapter positions.
- [x] Add canvas pan, wheel/button zoom, Fit, and Reset controls without introducing a generic edge interpreter or independent execution engine.
- [x] Bound node content inside larger cards with wrapped/clamped names, compact semantic type/state labels, nonzero-only work summaries, and accessible node focus/labels.
- [x] Compute padded, negative-coordinate-aware graph view boxes and increase canonical spacing so top nodes, labels, and connected cards are not clipped or overlapped.
- [x] Replace the badge-like state row with a semantic color-key legend explaining that each node shows its highest-priority current state.
- [x] Preserve HTMX/browser history ownership and reinitialize canvas controls after authoritative fragment swaps without duplicate global SSE listeners.
- [x] Add service, handler, rendered-markup, and real-Chrome regressions for empty Blank creation, constrained add/connect actions, persisted layout, absent-form value preservation, text containment, negative-coordinate clipping, pan/zoom/reset controls, and navigation.

### Reopened Phase 4 Changed Files

- Draft contract and persistence: `internal/models/automation_draft.go`, `internal/service/automation_draft_service.go`, and `internal/service/automation_draft_service_test.go`.
- Builder HTTP actions and regressions: `internal/handler/automation_builder_handler.go` and `internal/handler/automation_handler_test.go`.
- Graph/editor UI and browser coverage: `web/templates/pages/automations.templ`, generated `web/templates/pages/automations_templ.go`, and `web/templates/pages/automations_navigation_browser_test.go`.

### Reopened Phase 4 Validation And Audit

- `gofmt`, `templ generate`, and `go build ./cmd/server`: passed.
- Focused `go test ./internal/service ./internal/handler ./web/templates/pages -run 'TestAutomation' -count=1 -timeout 180s`: passed, including the production Chrome graph/navigation fixture.
- Final repository validation passed: `templ generate`, `go build ./cmd/server`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; every package passed and the macOS desktop linker emitted only the documented non-failing newer-SDK warning.
- Fresh audit found and repaired one palette-submit data-loss path: absent transition-label fields no longer overwrite labels already carried by strict `candidate_json`; a handler regression proves the preserved value.
- Fresh audit confirmed generated output is synchronized, graph geometry has no remaining 140-pixel legacy node cards or all-zero count strings, negative coordinates receive view-box padding, and Blank has no topology selector.
- The builder remains deliberately constrained to registered adapter palettes and canonical edges. It does not add arbitrary executable nodes, generic graph execution semantics, Register Existing, legacy detection, heuristic inference, migration, confidence scoring, or graph backfill.
- Remaining work for this repair: none; the candidate is ready for its clean checkpoint commit.

- Replaced unsupported SVG `fill-*`/`stroke-*` theme utilities with Automation graph classes backed directly by DaisyUI theme variables across Live, Definition, invocation, replay, and draft graphs. Dark-mode node surfaces and labels no longer fall back to black-on-black, and status colors remain non-color-labeled and keyboard-focusable.
- Corrected those graph classes from `hsl(var(--…))` to DaisyUI v4's actual `oklch(var(--…))` channel contract, changed the Chrome fixture to real OKLCH channel values so invalid color functions cannot falsely pass, and derived Chart.js legend, axis, and grid colors from the rendered theme.
- Repaired Automation navigation after HTMX history restoration: portfolio cards, `New Automation`, and every cross-surface Automation navigation control now use the shared `openVibelyNavigate` history owner, while draft create/clone responses push the canonical persisted draft URL instead of caching draft DOM under the source page URL.
- Expanded `TestAutomationGraphThemeAndHistoryNavigationInChrome` to verify computed graph contrast, Live-to-History-to-Definition navigation, browser Back restoration at each surface, in-page portfolio return, and repeated card navigation using production HTMX 2.0.4.
- Follow-up focused validation passed: `templ generate`, `go build ./cmd/server`, `go test ./internal/handler -run 'TestAutomationPagesRenderRegisteredDefinitionsAndEnforceProject' -count=1 -timeout 180s`, and `go test ./web/templates/pages -run 'TestAutomationGraphThemeAndHistoryNavigationInChrome' -count=1 -timeout 180s`.
- Follow-up Automation contract validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./web/templates/pages -run 'TestMigration11[345]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`.
- Final repository validation passed: `templ generate`, `go build ./cmd/server`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; the macOS desktop linker emitted only the documented newer-SDK warning and exited successfully.
- Fresh follow-up audit found synchronized generated output, no unsupported registration/inference/backfill path, no cross-surface raw HTMX history owner, and no material Definition of Done regression.

## Post-Completion Navigation, Connection, And Delete Repair

### Repair Checklist

- [x] Render one persistent Live, History, and Definition tab strip on all three published Automation surfaces, with an explicit active tab and shared `openVibelyNavigate` ownership.
- [x] Add real-Chrome coverage for Live to History to Live, Live to Definition to Live, browser Back restoration, and repeated Automation navigation after history restoration.
- [x] Replace the separate transition palette with direct canvas connectors: select a source node's right connector, then a highlighted target node's left connector.
- [x] Expose connector handles only when both endpoints are present and an unmade adapter-supported transition exists; retain keyboard activation and accessible endpoint labels.
- [x] Submit source/target keys to the existing draft update handler and resolve them only through the registered adapter's canonical edge palette; reject missing endpoints and arbitrary pairs without persistence.
- [x] Collapse repeated incomplete-Blank validation bullets into one named missing-node summary and one remaining-connection count.
- [x] Replace the published Automation Archive control and route with Delete plus a native DaisyUI confirmation dialog, explicit irreversible scope, Cancel/backdrop/close controls, and HTMX/native return to the portfolio.
- [x] Delete project-scoped Automation definitions and all Automation metadata/history transactionally while first disabling exclusively owned trigger schedules; preserve existing tasks, schedules, executions, Alerts, issues, and other authoritative domain resources.
- [x] Reject cross-project deletion without changing the Automation or its trigger schedule.
- [x] Preserve explicit Automation identity and all exclusions: no Register Existing, detection, inference, confidence scoring, migration, or graph backfill was added.
- [x] Run focused Automation validation, a fresh contract/accessibility audit, and the full repository validation chain; repair every touched-scope finding.

### Product Contract Decision

- The original Phase 1/4 runbook specified no ordinary delete and archive-with-history retention. The latest explicit product decision replaces the web Archive action with a confirmed permanent Automation delete.
- The compatible safety boundary is explicit: deleting removes only the Automation graph, versions, provenance/history, draft/publication records, and trigger ownership. Existing domain resources remain authoritative and are not deleted; schedules exclusively owned as Automation triggers are disabled before metadata deletion.
- The internal archive lifecycle operation remains available to existing service-level contracts, but there is no Automation archive route or control in the web product.

### Changed Files

- Lifecycle repository/service and web route: `internal/repository/automation_publication_repo.go`, `internal/service/automation_compiler.go`, `internal/handler/automation_builder_handler.go`, and `internal/handler/handler.go`.
- Handler/service regressions: `internal/handler/automation_handler_test.go` and `internal/service/automation_publication_service_test.go`.
- Navigation, confirmed delete dialog, validation summary, direct connectors, canvas interaction, generated output, and Chrome regression: `web/templates/pages/automations.templ`, `web/templates/pages/automations_templ.go`, and `web/templates/pages/automations_navigation_browser_test.go`.

### Validation And Audit

- Regression-first focused tests initially failed because shared tabs, connector actions, and Delete did not exist; the real-Chrome fixture could not find a stable History tab.
- `templ generate` and `go build ./cmd/server`: passed.
- `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./web/templates/pages -run 'TestMigration11[345]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`: passed.
- `go test ./web/templates/pages -run 'TestAutomationGraphThemeAndHistoryNavigationInChrome' -count=1 -timeout 180s`: passed with direct connector highlighting/payload, graph containment/theme, persistent tabs, Back/Forward, and return-navigation coverage.
- The first full `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` found the new delete dialog lacked the repository-standard labeled close control. Adding `ModalCloseButton` repaired the only touched-scope audit failure.
- The fresh full rerun `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passed every package; the desktop linker emitted only the documented non-failing newer-SDK warning.
- Fresh route/UI search found no Automation archive route/control and no new identity inference/backfill path. Transactional deletion tests prove project isolation, Automation-history cascade, preserved tasks/schedules, disabled owned triggers, and released ownership.
- Remaining work for this repair: none; create the clean checkpoint and allow Goal Agent evaluation.

## Post-Completion Free-Form Graph Editor Repair

### Repair Checklist

- [x] Replace the source-click/target-click submission dance with one continuous output-to-input pointer drag and a visible in-canvas preview edge.
- [x] Keep the builder mounted while drawing connections so users can create any number of consecutive directed edges without an HTMX fragment replacement.
- [x] Allow connections between any two distinct draft nodes, including reverse edges and multi-node cycles, while rejecting duplicate directed pairs and self-edges.
- [x] Add direct creation for trigger, agent-task, human-gate, condition, action, and outcome nodes; retain drag positioning, keyboard movement, removal, and explicit draft saving.
- [x] Remove adapter/template node suggestions from Blank drafts; template-created drafts alone retain collapsed runtime-node recovery suggestions.
- [x] Persist structurally safe custom nodes and cyclic edges as inert drafts with an explicit `unsupported_topology` publication blocker; do not add arbitrary edge execution or a generic runtime engine.
- [x] Preserve canonical adapter edge identity when a drawn pair exactly matches a registered adapter so supported Template/Describe/Blank drafts retain their existing publication path.
- [x] Let ordinary unmodified wheel events pass through the graph without changing its view box; retain Ctrl/Meta pinch-style wheel zoom and explicit zoom controls.
- [x] Retain keyboard-accessible source/target handles and clean in-progress pointer listeners when HTMX removes the builder.
- [x] Add service, handler, rendered-page, and real-Chrome regressions covering custom node creation, three-edge cycles, no connection submits/rerenders, Blank template-list exclusion, ordinary wheel pass-through, and pinch zoom.
- [x] Run the Automation-focused validation, full repository validation chain, fresh contract/security audit, and repair all material findings before checkpointing.

### Changed Files

- Draft validation and inert custom-topology persistence: `internal/service/automation_draft_service.go` and `internal/service/automation_draft_service_test.go`.
- Node/edge builder actions and rendered Blank regressions: `internal/handler/automation_builder_handler.go` and `internal/handler/automation_handler_test.go`.
- Direct-manipulation canvas, wheel behavior, generated output, and production Chrome regression: `web/templates/pages/automations.templ`, `web/templates/pages/automations_templ.go`, and `web/templates/pages/automations_navigation_browser_test.go`.

### Validation And Audit

- Regression-first Chrome validation failed before the production repair because a connector for the second edge did not exist, reproducing the reported inability to connect a third node.
- `templ generate`, `go build ./cmd/server`, focused service/handler regressions, and `TestAutomationGraphThemeAndHistoryNavigationInChrome`: passed after implementation.
- Focused Chrome evidence draws `vision_trigger -> vision_driver -> result -> vision_trigger` without submitting the draft form, then proves an ordinary wheel event is not prevented and does not zoom while a Ctrl-modified event is prevented and does zoom.
- Fresh audit confirmed custom topology remains draft-only through `unsupported_topology`; the real publication planner returns validation with no resource effects, canonical edges retain adapter keys, and self-edges, dangling endpoints, duplicate keys, unsafe/unknown configuration, and oversized payloads remain rejected.
- Fresh audit confirmed no new task, worker, queue, Workflow, Alert, GitHub, or generic graph execution path and no Register Existing, detection, inference, migration, confidence scoring, or graph backfill path.
- Automation-focused validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./web/templates/pages -run 'TestMigration11[345]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`.
- Final repository validation passed: `templ generate`, `go build ./cmd/server`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; every package passed and the desktop linker emitted only the documented non-failing newer-SDK warning.
- Final generated-output/diff audit found synchronized `.templ` output, no stale click-to-connect/request-submit controller, ordinary-wheel pass-through on both editable and read-only graph canvases, bounded listener cleanup, and no material Definition of Done regression.
- Remaining work: none; create the clean checkpoint and allow Goal Agent evaluation.

## Post-Completion Architect Workspace Repair

### Repair Checklist

- [x] Replace the page-sized yellow incomplete-graph alert with a compact neutral disclosure that keeps setup details available without dominating the builder.
- [x] Make the graph canvas the primary full-width workspace, with viewport-scale height and no permanent right-side node-creation rail.
- [x] Reposition Add node into the canvas toolbar while retaining all six supported node types and template-only maintained suggestions.
- [x] Make both left and right ports on every node valid drag origins and drop targets; preserve the chosen source and target sides in saved design metadata and across apply/edit cycles.
- [x] Keep edge direction explicit from drag origin to drop target while retaining arbitrary repeated connections, reverse edges, and multi-node loops.
- [x] Make graph connections directly selectable, expose draggable start/end handles for rewiring, and provide an on-canvas Delete connection control plus Delete/Backspace keyboard support.
- [x] Provide an on-canvas Delete node control plus Delete/Backspace keyboard support; node deletion also removes every incident connection from the saved candidate.
- [x] Replace visible `Edit as new draft`, draft badge, and Save draft terminology with Edit automation, Save changes, Review and apply, and Apply changes.
- [x] Reopen and overwrite one existing working design for repeated edits instead of accumulating duplicate draft versions; keep immutable published versions and history internal.
- [x] Preserve ordinary vertical wheel page scrolling, Ctrl/Meta pinch-style zoom, explicit zoom controls, and the unsupported-topology publication block.
- [x] Add service, handler, generated-template, and real-Chrome regressions for layout, terminology, two-sided port persistence, loops, rewiring, edge deletion, node deletion, repeated Edit behavior, wheel pass-through, and pinch zoom.

### Changed Files

- Saved connector geometry and one-working-design edit behavior: `internal/models/automation_draft.go`, `internal/repository/automation_draft_repo.go`, `internal/service/automation_draft_service.go`, and `internal/service/automation_draft_service_test.go`.
- Rendered builder contract regressions: `internal/handler/automation_handler_test.go`.
- Full architect workspace, compact setup disclosure, toolbar node creator, bidirectional ports, direct deletion/rewiring, terminology, generated output, and production browser regression: `web/templates/pages/automations.templ`, `web/templates/pages/automations_templ.go`, and `web/templates/pages/automations_navigation_browser_test.go`.

### Validation And Audit

- Regression-first handler tests failed against the previous page-sized warning, permanent 18rem node rail, 30rem builder canvas, fixed output/input handles, missing canvas deletion/rewiring controls, and old Draft terminology.
- Regression-first service coverage proved repeated Edit requests created multiple draft versions before the working-design reuse repair.
- `templ generate`, `go build ./cmd/server`, focused draft service tests, focused builder handler tests, and `TestAutomationGraphThemeAndHistoryNavigationInChrome`: passed.
- The real-Chrome fixture draws three consecutive edges using left-to-right and right-to-left port combinations, retains the chosen port sides, forms a cycle, rewires an existing endpoint, deletes a connection, deletes a node and its incident edges, preserves keyboard movement, passes ordinary wheel events through, and reserves Ctrl/Meta wheel for zoom.
- Automation-focused validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./web/templates/pages -run 'TestMigration11[345]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`.
- The first final repository rerun found one stale handler assertion expecting the removed `Generating and validating draft` copy; updating that regression to the user-facing design terminology repaired the only failure, and the fresh full rerun passed every package.
- Final repository validation passed: `templ generate`, `go build ./cmd/server`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; every package passed and the desktop linker emitted only the documented non-failing newer-SDK warning.
- Fresh audit found and repaired three material follow-ups: the painted edge stroke could intercept clicks before the transparent hit target; published design metadata was not reused when reopening an Automation, which would lose chosen connector sides; and repeated Edit requests created duplicate working versions instead of reopening the saved design.
- Fresh safety audit confirmed custom loops and arbitrary topology remain inert saved designs with `unsupported_topology`; planning returns no effects, published versions remain immutable, and no generic runtime, task/worker/queue/Workflow/Alert/GitHub replacement, Register Existing, detection, inference, migration, confidence scoring, or graph backfill was introduced.
- Remaining work for this repair: none; create the clean checkpoint and allow Goal Agent evaluation.

## Post-Completion Blank Builder And Connection Repair

### Repair Checklist

- [x] Replace the Blank builder's fragile Add node details dropdown with an obvious toolbar button that opens a native dialog.
- [x] Put a large Add first node action in the center of a truly empty canvas so node creation remains discoverable before any graph content exists.
- [x] Keep node creation on the existing project-scoped draft update handler and preserve the six safe node types, candidate bounds, and inert unsupported-topology behavior.
- [x] Make connection removal explicit with an always-present Disconnect selected toolbar action that becomes enabled when a line is selected.
- [x] Keep selected-line endpoint handles available for direct start/end rewiring and update the canvas guidance with the exact interaction.
- [x] Add rendered-handler and real-Chrome regressions that start from zero nodes, open the dialog, submit a named node through HTMX, select a line, disconnect it, and verify serialized candidate state.
- [x] Run generation, build, focused Automation validation, full repository validation, and a fresh accessibility/identity audit; repair every material finding.

### Changed Files

- Blank canvas, add-node dialog, explicit connection selection/disconnect behavior, and generated output: `web/templates/pages/automations.templ` and `web/templates/pages/automations_templ.go`.
- Empty-builder rendered contract and production browser interaction coverage: `internal/handler/automation_handler_test.go` and `web/templates/pages/automations_navigation_browser_test.go`.

### Validation And Audit

- Regression-first handler coverage failed because the prior markup had only a details dropdown and no empty-canvas first-node action, native dialog, or explicit disconnect control.
- The production Chrome fixture now starts from a zero-node Blank Automation, opens the node dialog from the canvas, submits a named agent-task node through the real HTMX form path, and waits for the node to appear after replacement.
- The same Chrome fixture selects a painted connection, verifies Disconnect selected becomes enabled, removes the edge without replacing the builder, and confirms the edge is absent from `candidate_json`; endpoint-drag rewiring remains covered in the same mounted graph.
- `templ generate`, `go build ./cmd/server`, and Automation-focused validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./web/templates/pages -run 'TestMigration11[345]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`.
- The first full repository run found one dialog accessibility-contract failure because the close helper appeared after nested modal markup. Moving the labeled close control to the top of the modal box repaired the finding; `TestTemplateDialogsHaveConsistentCloseControls`, the Blank handler regression, and the Chrome fixture then passed.
- Final repository validation passed: `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; every package passed and the desktop linker emitted only the documented non-failing newer-SDK warning.
- Fresh audit confirmed Add node submits through the existing Automation draft handler/service, line removal only changes draft candidate geometry, custom topology remains non-executable until accepted by a registered adapter, and no identity inference, Register Existing, backfill, migration, or parallel runtime system was introduced.
- Remaining work for this repair: none; create the clean checkpoint and allow Goal Agent evaluation.

## Post-Completion Draft Selection, Naming, And Connection Deletion Repair

### Repair Checklist

- [x] Make unpublished Automation portfolio cards resolve their latest persisted working version and open its canonical builder URL instead of an empty Live `v0` graph.
- [x] Make stale or copied `/automations/:id` URLs for unpublished Automations resolve to the same builder: render with canonical `HX-Push-Url` for HTMX and redirect full-page requests.
- [x] Keep published Automation cards and detail URLs on Live, and preserve project isolation for both draft detail and canonical builder routes.
- [x] Add an always-visible Automation name field to the builder and persist it through the existing draft update path without losing nodes, edges, positions, or port metadata.
- [x] Synchronize name edits into every candidate payload so node creation and direct graph manipulation cannot overwrite a newly entered name.
- [x] Replace ambiguous connection-removal copy with an always-present `Delete connection` canvas action that enables after selecting a line; retain selected-edge endpoint rewiring, on-edge delete, and Delete/Backspace behavior.
- [x] Add handler and real-Chrome regressions that enter through the portfolio, select an unpublished Automation, mount its persisted nodes and edge, rename it, select and delete a connection, and retain the remaining direct-manipulation behavior.
- [x] Run generation, build, focused Automation validation, full repository validation, and a fresh routing/accessibility/identity audit; repair every material finding and checkpoint the clean phase.

### Changed Files

- Draft-card version resolution and current-working-design lookup: `internal/service/automation_service.go` and `internal/service/automation_draft_service.go`.
- Draft-only detail routing and Automation-name form persistence: `internal/handler/automation_handler.go` and `internal/handler/automation_builder_handler.go`.
- Canonical card links, visible name editing, explicit selected-connection deletion, candidate synchronization, and generated output: `web/templates/pages/automations.templ` and `web/templates/pages/automations_templ.go`.
- Exact-route handler and production-browser regressions: `internal/handler/automation_handler_test.go` and `web/templates/pages/automations_navigation_browser_test.go`.

### Validation And Audit

- The regression-first handler test failed against checkpoint `fba6883` because the unpublished card linked to `/automations/:id`; that route rendered an empty Live graph with no published topology, no builder controls, and no editable name.
- Focused handler coverage now proves the portfolio emits the canonical draft URL, full-page and HTMX stale-detail fallbacks resolve to the same persisted builder, published cards remain on Live, foreign-project access fails closed, renaming persists, and the graph is not lost.
- The production Chrome fixture now returns from an actually empty Blank builder to the portfolio, clicks a real unpublished Automation card, verifies the graph mounts, changes the visible Automation name, and then exercises multi-edge cycles, selection, rewiring, `Delete connection`, node deletion, ordinary wheel pass-through, and pinch zoom in the same mounted design.
- `templ generate`, `git diff --check`, `go build ./cmd/server`, Automation-focused validation, the exact handler regressions, and `TestAutomationGraphThemeAndHistoryNavigationInChrome`: passed.
- Final repository validation passed: `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; every package passed and the desktop linker emitted only the documented non-failing newer-SDK warning.
- Fresh routing audit confirmed only draft-only Automations are redirected; published Automations with or without a separate working design retain immutable published Live, History, and Definition surfaces.
- Fresh safety audit confirmed the change only resolves explicit persisted Automation/draft IDs through project-scoped repositories. It adds no Register Existing, detection, inference, confidence scoring, migration, graph backfill, generic execution engine, or replacement task/worker/queue/Workflow/Alert/GitHub system.
- Remaining work for this repair: none; create the clean checkpoint and allow Goal Agent evaluation.

## Reopened Definition Of Done Repair

### Contract Checklist

- [x] Reject GitHub Automation publication unless the selected PAT/App authentication mode is actually configured, the project has an explicit GitHub repository, and the project approval inbox exists and is enabled; return validation with no effects and cover each unavailable capability plus the valid path.
- [x] Stage replacement task configuration and commit it atomically with successful version publication so any schedule/resource/publication failure leaves the still-active prior version's task behavior unchanged; preserve resumable/idempotent publication.
- [x] Reject Automation deletion while any owned invocation/dispatch is nonterminal, preserving outbox, reservation, task, and execution recovery state; allow deletion after deterministic terminal reconciliation.
- [x] Expose the existing confirmed Automation Delete action from unpublished builders and prove draft-only project-scoped deletion through the visible route.
- [x] Count one failed Live work item once when its failed activity and failed position represent the same provenance; retain failed invocation-only activity visibility.
- [x] Run formatting/generation, build, focused regression suites, Automation-wide validation, and the full repository suite; record exact evidence.
- [ ] Perform a separate fresh read-only audit against all phase contracts, Definition of Done items, explicit identity boundaries, and stated exclusions; repair any material finding in a later implementation pass before another audit.

### Regression Evidence

- Regression-first focused execution passed the build but failed all five new contracts against `00729f7`: GitHub planning returned twelve effects with no credential; a forced late schedule failure left the active task prompt changed; deletion succeeded with a submitted dispatch; the Live node reported two failures for one work item; and the Blank builder omitted the Delete control.
- `TestAutomationGitHubPublicationRequiresExecutableIntegrationAndApprovalCapabilities` now covers missing PAT, absent/disabled inbox, missing project GitHub repository, valid PAT, missing App setup, and valid connected App setup. Invalid plans contain the exact capability validation and no effects.
- `TestAutomationReplacementFailureKeepsPriorTaskBehaviorAndSuccessfulRetrySwitchesTrigger` uses a real SQLite schedule-insert failure after the task step is staged. It proves the active task prompt and trigger remain unchanged, the task journal remains `running`, retry reuses resources, and task update plus version switch commit together with journal completion.
- `TestAutomationDeleteRejectsInFlightDispatchAndPreservesRestartRecovery` creates a leased/submitted dispatch and dispatch-bound execution, proves deletion preserves definition/outbox/reservation/execution state and generic recovery exclusion, then completes through the existing reconciler and proves deletion succeeds only afterward.
- `TestAutomationBlankBuilderIsEmptyInteractiveAndPersistsNodeActions` now proves the visible confirmed Delete control and exact project-scoped route remove an unpublished Automation.
- `TestAutomationLiveDisplayStatePrecedencePreservesMixedCounters` now proves one failed work item represented by both activity and position counts once while a separate invocation-only failed activity remains visible.
- Focused repaired regressions passed: `go test ./internal/service ./internal/handler -run 'TestAutomationGitHubPublicationRequiresExecutableIntegrationAndApprovalCapabilities|TestAutomationReplacementFailureKeepsPriorTaskBehaviorAndSuccessfulRetrySwitchesTrigger|TestAutomationDeleteRejectsInFlightDispatchAndPreservesRestartRecovery|TestAutomationLiveDisplayStatePrecedencePreservesMixedCounters|TestAutomationBlankBuilderIsEmptyInteractiveAndPersistsNodeActions|TestAutomationPublicationPlanGoldenGitHubDependenciesAndConfigurationChanges' -count=1 -timeout 180s`.
- Automation-wide validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./web/templates/pages -run 'TestMigration11[345]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`.
- Full validation passed: `templ generate`, `git diff --check`, `go build ./...`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`. Every package passed; desktop emitted only the documented non-failing newer-SDK linker warning.

### Changed Files

- GitHub readiness validation and production connection-status wiring: `internal/service/automation_publication_planner.go`, `internal/server/server.go`, and `internal/service/automation_publication_service_test.go`.
- Atomic replacement task staging/publication: `internal/service/automation_compiler.go`, `internal/repository/automation_publication_repo.go`, and `internal/service/automation_publication_service_test.go`.
- In-flight deletion guard and restart recovery coverage: `internal/repository/automation_publication_repo.go` and `internal/service/automation_runtime_test.go`.
- Draft-only confirmed deletion UI and generated output: `web/templates/pages/automations.templ`, `web/templates/pages/automations_templ.go`, and `internal/handler/automation_handler_test.go`.
- Provenance-deduplicated Live failure counts: `internal/repository/automation_runtime_repo.go` and `internal/service/automation_runtime_test.go`.

### Implementation Audit

- GitHub capability checks run before effect planning, use the configured production `GitHubService` connection status without persisting credentials into the plan, and fail closed on missing approval inbox/repository.
- Existing-task update steps remain staged as `running`; the repository validates every staged step, applies limited task configuration fields, marks the step complete, switches schedules/ownership/version, and completes the attempt in one `BEGIN IMMEDIATE` transaction.
- Deletion checks nonterminal invocation/outbox state and dispatch-bound running executions in the same transaction before disabling schedules or deleting metadata.
- Live failure counts use a set-based union keyed by work-item provenance, with activity identity only for invocation-only failures; no current task/execution/GitHub read was introduced.
- Source audit found no new generic graph executor, parallel task/worker/queue/Workflow/Alert/GitHub system, Register Existing, legacy detection, heuristic inference, migration of old resources, confidence scoring, or graph backfill.

### Remaining Work

- Safety repair checkpoint `48b7583` is complete; perform the required separate read-only full-objective audit.

## Post-Completion Drag Connector Preview Repair

### Contract Checklist

- [x] Show a connector line immediately after pointer-down and update its endpoint with every matching pointer move until drop or cancellation.
- [x] Make the in-progress connector visually distinct and easy to see with a thick primary-color stroke and matching directional arrowhead.
- [x] Paint the preview above existing edges but below graph nodes and ports so crossings remain visible without obscuring endpoints.
- [x] Keep the preview non-interactive so it cannot intercept or block the destination port during a drop.
- [x] Cover the exact production Blank/visual-builder pointer drag in real Chrome, including visibility, nonzero geometry, computed stroke width/color, non-interception, consecutive connections, and cycles.
- [x] Preserve direct two-sided connections, reconnection/deletion, ordinary wheel pass-through, pinch zoom, inert unsupported topology, explicit Automation identity, and all stated exclusions.

### Changed Files

- Connector preview layering, primary arrowhead, visible stroke, and generated output: `web/templates/pages/automations.templ` and `web/templates/pages/automations_templ.go`.
- Production-browser drag-preview regression: `web/templates/pages/automations_navigation_browser_test.go`.

### Validation And Audit

- Regression-first `go test ./web/templates/pages -run TestAutomationGraphThemeAndHistoryNavigationInChrome -count=1 -timeout 60s` failed against `221b8d9` with `connector preview is too thin to see while dragging`.
- After repair, `templ generate`, `gofmt`, `git diff --check`, `go build ./cmd/server`, and the focused production Chrome regression passed.
- Automation-wide validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./web/templates/pages -run 'TestMigration11[345]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`.
- Full repository validation passed: `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; every package passed and desktop emitted only the documented non-failing newer-SDK linker warning.
- Fresh diff audit confirmed the preview is painted after durable edge groups and before node groups, carries `pointer-events: none`, uses a non-scaling 4px primary stroke and primary arrowhead, and is cleared through the existing drop/cancel/HTMX cleanup path.
- The change is presentation and browser coverage only. It adds no persistence/runtime behavior, generic graph executor, parallel task/worker/queue/Workflow/Alert/GitHub system, Register Existing, legacy detection, heuristic inference, migration of old resources, confidence scoring, or graph backfill.
- Checkpoint `f3d0524` completed the connector-preview repair; its subsequent qualifying full-objective audit produced the four findings tracked in the current repair section.

## Open Findings Or Blockers

- No known code, test, build, identity, exclusion, phase, or Definition of Done blocker remains after the edit-free audit of `4e971f0`.
- The authoritative `automation_graphs.md` managed view still names an older checkpoint for the current repairs. This thread exposes only read-only `memory_view`; a dedicated active reconciliation task also failed with the exact same missing mutation capability and changed no repository code. Completion cannot be claimed until an authorized managed-memory mutation path updates and reload-verifies that topic.

## Reopened Full-Objective Audit Repair

### Contract Checklist

- [x] Let an actually empty Blank draft add the selected registered adapter's supported node semantics one node at a time, connect its canonical transitions, and reach a publishable topology without presenting or preloading a template; keep custom nodes/connections and loops safe, inert, and unpublishable.
- [x] Remove `candidate` from the public canonical Chat schema and action handler so draft creation has only Template, Describe It, and Blank identities; retain fixed-candidate page/Chat parity through mocked structured Describe It output rather than a public candidate input.
- [x] Replace the fixed node-resource list with a project/node/version-scoped, opaque, stable keyset cursor; return at most 50 rows, expose the next page in the HTMX panel, reject malformed/cross-scope cursors, preserve resource sanitization, and add matching indexes/query-plan evidence.
- [x] Add required safe Automation observability and local metrics for lifecycle identifiers/state changes, validation failures, projection/transition failures, dispatch recovery/skips/retries/age, confirmation rejection/replay, ambiguous GitHub mutations, reconciler repairs, graph query duration/payload size, and graph limits without logging content, credentials, or confirmation tokens.
- [x] Run generation/formatting, focused regressions, Automation-wide validation, build, and the uncached full repository suite; repair every failure and record exact evidence.
- [ ] Create a clean checkpoint, then perform a separate strict read-only audit against every phase, all 17 Definition of Done items, explicit identity boundaries, and all exclusions.

### Requirements And Files

- Blank now stays actually empty while its Add node dialog identifies the selected registered runtime adapter and offers each missing canonical runtime behavior one at a time. The server copies only adapter-owned node keys, roles, types, configuration, and positions; custom visual nodes remain design-only. Incremental persistence now treats absent and empty canonical edge conditions equivalently, so an assembled topology survives each save and reaches the existing planner without weakening adapter validation. Implemented in `internal/handler/automation_builder_handler.go`, `internal/service/automation_draft_service.go`, `web/templates/pages/automations.templ`, and generated `web/templates/pages/automations_templ.go`.
- Public Chat draft creation now accepts only `template`, `describe`, and `blank`; the typed handler no longer accepts a candidate payload. Fixed structured parity runs through mocked Describe It output in handler/runtime tests. Implemented in `internal/chatcontrol/registry.go`, `internal/handler/automation_chat_actions.go`, and their tests.
- Node resources now use a bounded 50-row `(started_at DESC, activity_id DESC, resource_link_id DESC)` keyset page with the existing opaque cursor contract scoped to automation/version/node. The HTMX panel exposes Next resources, malformed and cross-node cursors fail closed, and migration `116_automation_node_resource_pagination.sql` adds the activity/resource indexes selected by `EXPLAIN QUERY PLAN`. Implemented across the Automation model, repository, graph service, handler, template, migration, and handler regression.
- `internal/automationobs` now records safe structured Automation events plus process-local counters/aggregates through a fixed non-content field allowlist and bounded values. Registration/publication validation and outcomes, draft/graph limits, lifecycle projections, transition failures, dispatch retry/recovery/age, Chat confirmation rejection/replay, ambiguous GitHub mutations, reconciliation repairs, and graph duration/payload metrics are wired through existing repositories/services without content, credentials, or tokens.

### Regression, Validation, And Audit Evidence

- Regression-first focused tests cover `TestAutomationBlankCanAssembleRegisteredTopologyWithoutTemplate`, `TestAutomationChatDraftCreationRejectsCandidateIdentity`, public registry source enumeration, mocked Describe It page/Chat parity, `TestAutomationNodeResourcesUseStableBoundedPagination`, safe observer fields, publication metrics, reconciliation repair metrics, transition failures, graph duration/payload size, and publication validation failures.
- The Blank regression found and repaired persisted empty canonical conditions decoding as `nil` and being compared as JSON `null` against `{}`; the final assembled graph validates and produces publication effects through the registered Vision Driver adapter.
- Pagination proves 50/1 page boundaries with no overlap, stable newest-first ordering, resource-name sanitization, malformed/cross-node/project rejection, and both migration indexes in the production-shaped SQLite query plan.
- Focused generation/build/regressions passed: `templ generate`, `git diff --check`, `go build ./cmd/server`, and the named handler/service/Chat/observer tests.
- Automation-wide validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./internal/automationobs ./web/templates/pages -run 'TestMigration11[3-6]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`, including the production Chrome graph fixture.
- The first full suite correctly found four stale latest-migration assertions expecting `115`; all were advanced to `116`, and the focused migration tests passed.
- Final validation passed: `templ generate`, `git diff --check`, `go build ./...`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`. Every package passed; desktop emitted only the documented non-failing newer-SDK linker warnings.
- Fresh implementation diff audit found no remaining call-site, project-isolation, cursor-scope, identity, secret-logging, or adapter-ownership defect. It confirmed no Register Existing, legacy detection, heuristic inference, migration/backfill of prior resources, confidence scoring, or parallel graph runtime was added.

### Remaining Work

- Treat the repair commit containing this evidence as the clean checkpoint, then perform the separate qualifying strict read-only full-objective audit from that immutable commit.

## Reopened Strict-Audit Contract Repair

### Contract Checklist

- [x] Preserve caller/model-supplied `schema_version` through normalization and reject missing or unsupported versions during generation, draft create/update/clone, and publication planning rather than coercing them to version 1.
- [x] Add project-scoped `agent_ref`, `skills`, and `source_files` task-node configuration using the bounded capability snapshot; reject unavailable/cross-project references, render editable builder controls, include resolved dependencies in publication planning, and assign the resolved primary Agent through normal Task creation and atomic replacement updates.
- [x] Add bounded set-based Automation portfolio summaries for running, waiting-human, blocked, failed, and recently completed work, with deduplicated provenance and the persisted concise health reason.
- [x] Report GitHub configured in described-draft capability snapshots only when the selected PAT/App mode is configured and connected, reusing the publication connection-status contract without exposing credentials.
- [x] Add a bounded running-node pulse to graph nodes carrying the running state and disable it under `prefers-reduced-motion: reduce`.
- [x] Add regression-first service/repository/handler/template/browser coverage for every repair, reconcile symmetric call sites, run generation/build/focused/full validation, perform a fresh implementation audit, and create a clean checkpoint.
- [ ] Perform a separate strict read-only audit from the clean checkpoint against all four phases, all 17 Definition of Done items, explicit Automation identity, and every stated exclusion.

### Requirements And Files

- `NormalizeCandidate` now preserves the supplied schema version. Missing/future versions fail create/update/load/clone/planning or enter the one-repair described-generation path; constructors alone assign version 1. Implemented in `internal/service/automation_draft_service.go` and `internal/service/automation_draft_generation.go` with create/update/load/clone/repair regressions.
- Task-node schema now accepts bounded `agent_ref`, `skills`, and `source_files`. Current project capabilities are rebuilt for draft persistence, display, clone, and planning; unavailable references remain visible validation errors and cannot produce publication effects. Builder controls round-trip through the existing candidate update path. Planning hashes resolved Agent revision and exposes Agent/skill/source-file reuse; compilation assigns `Task.AgentDefinitionID`, carries selected skill/source guidance into the task prompt, and atomically stages Agent changes with replacement publication. Implemented across Automation models, capability/draft/planner/compiler services, publication repository, handler/template, server wiring, and tests.
- Portfolio cards now receive one set-based project-scoped operational summary with work-item provenance deduplication and render all five counters plus the persisted health reason. Implemented in `internal/repository/automation_runtime_repo.go`, `internal/service/automation_service.go`, Automation models/templates, and mixed-state/handler regressions.
- Capability snapshots now require the selected PAT/App mode to match a configured and connected production `GitHubService` status. PAT/App connected and disconnected matrices are covered without rendering credentials.
- Running graph nodes use a bounded 1.8-second stroke pulse; the concrete `prefers-reduced-motion: reduce` rule disables animation. Generated templ output and direct/HTMX/Chrome Automation coverage remain synchronized.

### Regression, Validation, And Implementation Audit Evidence

- Regression-first focused execution failed against `4c72be8` because capability-aware draft validation, GitHub connection injection, Agent planner/compiler wiring, and portfolio counters did not exist; this captured the pre-repair contract failures before production edits.
- Focused schema/reference/readiness/publication/portfolio/render tests pass, including unsupported schema values `0` and `2`, bounded repair, cross-project Agent rejection, missing skill/source reporting, PAT/App connection matrices, resolved plan reuse effects, compiled task Agent/source/skill behavior, failed replacement Agent preservation, mixed-count deduplication, health reason rendering, animation, and reduced-motion CSS.
- Automation-wide validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./internal/automationobs ./web/templates/pages -run 'TestMigration11[3-6]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`, including the production Chrome graph fixture.
- Full validation passed: `templ generate`, `git diff --check`, `go build ./...`, and `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`. Every package passed; desktop emitted only the documented non-failing newer-SDK linker warnings.
- Fresh implementation audit traced decode/normalize/validate/persist/load/clone/plan, page/Chat shared capability validation, create/replacement task configuration, project isolation, plan dependency/effect hashing, portfolio provenance identity, GitHub readiness, and rendered motion policy. It repaired handler Agent-repository overwrite and unresolved-reference draft behavior before the final validation chain.
- Identity/exclusion search found no Register Existing, legacy detection, heuristic inference, confidence scoring, resource migration/backfill, arbitrary topology execution, or parallel task/worker/Workflow/Alert/GitHub runtime.

### Managed Memory State

- The authoritative `automation_graphs.md` managed view records checkpoint `d023f0e`, status reconciliation `8dad69b`, all five completed strict-audit repairs, passing full validation, explicit Automation identity, and every exclusion.
- No tracked `.openvibely/memories/automation_graphs.md` counterpart exists. The managed view is semantically current and no longer blocks the qualifying read-only audit.

### Identity And Scope Guard

- Supported Automation identity remains only Template, Describe It, Blank, and maintained Native/GitHub setup registration. This pass must not add Register Existing, legacy detection, heuristic inference, migration or graph backfill, confidence scoring, arbitrary graph execution, or a parallel runtime engine.

## Follow-Up Connector Rendering Repair

### Contract Checklist

- [x] Reproduce the production SVG drag path in real Chrome and require the preview to have no `hidden` attribute, non-`none` display, rendered client geometry, visible primary stroke, nonzero endpoints, and non-interactive hit behavior before drop.
- [x] Remove and restore the SVG `hidden` attribute explicitly during create and reconnect drags instead of assigning an unsupported `SVGLineElement.hidden` expando.
- [x] Suppress the selected edge group’s browser bounding-box outline while preserving selected-line emphasis, draggable endpoints, the red midpoint delete control, the visible Delete connection action, and keyboard deletion.
- [x] Preserve connector layering, two-sided geometry, consecutive edges/cycles, reconnection, deletion, pointer cancellation, ordinary-wheel pass-through, pinch zoom, explicit Automation identity, and every exclusion.

### Requirements And Files

- Live preview visibility and selected-edge focus presentation are implemented in `web/templates/pages/automations.templ` with synchronized generated output in `web/templates/pages/automations_templ.go`.
- The production Chrome regression in `web/templates/pages/automations_navigation_browser_test.go` now rejects an SVG preview that still has `hidden`, computes to `display: none`, has no rendered client rect, or gives the selected edge group a browser outline.

### Regression, Validation, And Audit Evidence

- Regression-first `go test ./web/templates/pages -run TestAutomationGraphThemeAndHistoryNavigationInChrome -count=1 -timeout 60s` failed against `8dad69b` with `connector preview remains hidden while dragging`, proving that `preview.hidden = false` did not remove the SVG `hidden` attribute.
- After repair, `templ generate`, `gofmt`, `git diff --check`, `go build ./cmd/server`, and the focused production Chrome regression passed.
- Automation-wide validation passed: `go test ./internal/database ./internal/repository ./internal/service ./internal/handler ./internal/chatcontrol ./internal/automationobs ./web/templates/pages -run 'TestMigration11[3-6]|TestAutomation|TestRegistry_Automation' -count=1 -timeout 180s`.
- Full repository validation passed: `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s`; every package passed and desktop emitted only the documented non-failing newer-SDK linker warnings.
- The implementation changes only SVG visibility and focus presentation. It adds no persistence or runtime semantics, Register Existing, legacy detection, heuristic inference, migration/backfill, confidence scoring, arbitrary graph execution, or parallel runtime engine.

### Remaining Work

- Checkpoint `f3d0524` completed this connector-rendering repair. The later qualifying audit failed on the four separate contracts documented below.

## Reopened `f3d0524` Full-Objective Audit Repair

### Contract Checklist

- [x] Deduplicate Live and portfolio running, waiting-human, blocked, failed, and recently-completed counters by work-item identity across activities, positions, and transitions while retaining invocation-only activity identity; select exactly one state per identity using failed, blocked, waiting, running, then recent precedence.
- [x] Add an explicit bounded GitHub PR refresh service with a persisted one-minute cache, 20-resource limit, no retry on provider/rate-limit errors, local freshness/staleness, merged and closed projection reconciliation, persisted degraded health for stale external state, and no GitHub calls from ordinary graph reads.
- [x] Resolve task, execution, Alert, goal, Workflow execution, GitHub issue, PR, and review resources to actual native detail destinations with project-scoped Alert and goal navigation.
- [x] Emit safe exact-once process-local Automation lifecycle events/counters after successful pause, resume, archive, and delete commits, with no success event on failed mutation.
- [x] Refresh tracked external state through the explicit cached POST endpoint when a hidden Live tab becomes visible; retain the local GET fallback for graphs without tracked external resources.
- [x] Run generation, formatting, focused regressions, build, internal tests, and the uncached full repository suite.
- [ ] Create the clean repair checkpoint, then perform the required separate edit-free audit against all four phases, all 17 Definition of Done items, identity boundaries, and exclusions.

### Resource And Execution Semantics

- Draft creation, editing, and validation remain inert. Confirmed publication plans and compiles only registered adapter effects through existing services.
- Publication creates or reuses the adapter's fixed visible OpenVibely task nodes and trigger schedules. `TestAutomationPublicationPlanIsDeterministicAndCompilerIsIdempotent` proves real task/schedule creation and retry idempotency; stale-plan coverage proves no resources are created before valid confirmation.
- Dynamic Implementation nodes represent ordinary implementation tasks created by the existing Alert/GitHub runtime tools after approval or assignment. They are not a hidden graph task type. Existing Native/GitHub runtime tests prove those tasks, executions, issues, PRs, and transitions retain authoritative domain state and explicit Automation provenance.
- Automation edges remain adapter-owned topology and projection only; no generic edge interpreter, worker pool, task queue, Workflow engine, Alert system, or GitHub subsystem was added.

### Validation Evidence

- Regression-first destination and visible-tab tests failed against the pre-repair implementation on missing project/goal destinations and missing explicit visibility reconciliation, then passed after the focused repairs.
- The first strict audit of repair checkpoint `8601d36` found that same-state UNION deduplication still allowed one work-item identity to appear in multiple state counters. A cross-state regression failed with `running=3` instead of `2`; Live and portfolio now group each identity through one precedence-selected state, and the focused regression, build, and uncached full suite pass.
- The next strict audit of checkpoint `c48f7c8` found that displayed stale GitHub state did not enter the persisted health projection required by the Phase 2 health model. The regression failed with `healthy` instead of `degraded`; `RecomputeAutomationHealth` now consumes the local persisted freshness threshold, explicit refresh recomputes health, stale state persists a concise degraded reason, successful refresh restores healthy state, and lifecycle remains unchanged.
- Focused service and handler regressions pass for exact operational counts, explicit cached/rate-limit refresh, merged-state parsing and projection, every supported resource destination, visible-tab refresh, and exact lifecycle observability.
- `templ generate`, `gofmt`, `git diff --check`, and `go build ./...` pass. Desktop build emits only the known non-failing newer-macOS-SDK linker warnings.
- `go test ./internal/... -count=1 -timeout 120s` passes.
- `TMPDIR=/private/tmp go test ./... -count=1 -timeout 120s` passes every package, including the production Chrome graph regression.
- The authoritative `automation_graphs.md` managed view records the four repaired audit contracts, explicit persisted Automation identity, exclusions, and the separate audit as the only remaining action. No tracked `.openvibely/memories/automation_graphs.md` counterpart exists.

## Final Edit-Free Full-Objective Audit Of `4e971f0`

### Four Phases

- Phase 1 passes: migration `113`, explicit project-scoped Native/GitHub registration, immutable definitions, actual-ID resource binding, exclusive triggers, maintained-bootstrap restrictions, portfolio/detail UI, no-inference tests, project cascade, and isolation are present and mapped to repository/service/handler coverage.
- Phase 2 passes: migration `114`, atomic schedule claims, leased outbox/reservations, prepared execution identity, existing Worker/ThreadInput/Alert/GitHub paths, explicit causal projections, SSE invalidation, restart recovery, one-state-per-work-item counters, bounded native drill-down, cached external reconciliation/freshness, stale external health, and lifecycle observability are present and tested.
- Phase 3 passes: immutable invocation/work-item history, bounded collection-scoped cursors, cross-invocation lifetime, persisted-transition replay, funnel/duration/failure/bottleneck summaries, independently persisted health, Chart.js lifecycle, compact payloads, empty/partial states, and project isolation are present and tested.
- Phase 4 passes: migration `115`, strict schema/capability resolution, inert drafts, Template/Describe It/Blank, constrained visual builder, deterministic plans, idempotent compilation into real tasks/schedules, immutable replacement publication, page/Chat parity, later-turn confirmation, lifecycle/delete safety, direct/HTMX/browser behavior, accessibility, and generated templ output are present and tested.

### Definition Of Done

1. Multiple project graphs are proven by explicit registration and portfolio tests.
2. Native/GitHub setup uses actual IDs and rejects title-derived or unsupported registration.
3. Live/portfolio counters choose one precedence-selected state per work-item identity and retain distinct invocation-only activity.
4. Every supported task, execution, Alert, goal, Workflow execution, issue, PR, and review drill-down resolves its native project-scoped destination.
5. Atomic dispatch and reconciler tests preserve state across reload/restart.
6. History, clone, replay, and lifetime tests retain immutable version identity.
7. Shared inbox, child, queued-input, and multi-binding tests preserve causal provenance.
8. Composite constraints plus definition/live/history/draft/plan/publish/resource tests enforce project isolation.
9. Native approval, GitHub assignment/review, merge, release, and deployment boundaries remain unchanged.
10. Publication and runtime use existing Task, Schedule, Worker, ThreadInput, Alert, Goal, Workflow, GitHub, execution, lineage, and broadcaster boundaries; no hidden engine exists.
11. Templates and builder publication persist the same definition/version/node/edge model used by Live and History.
12. Public creation remains Template, Describe It, and Blank; maintained registration remains Native/GitHub-only.
13. Canonical Chat tests prove persisted draft, visible exact plan, later explicit confirmation, and durable activation.
14. Page and Chat share draft normalization, validation, planning, compilation, and publication services.
15. Every publishable topology is accepted by one registered adapter; the registered custom adapter compiles only allowlisted capability handoffs into existing services, and unsupported edges never execute generically.
16. Confirmation, publication journal, trigger ownership, dispatch, execution, work-item, activity, and transition identities are retry/crash/concurrency-safe.
17. Migration, repository, service, handler, Chat, runtime, SSE, UI, accessibility, browser-navigation, restart, build, internal, and uncached full repository validation pass.

### Identity, Exclusions, And Outcome

- Supported identity remains explicit persisted Automation identity through Template, Describe It, Blank, and maintained Native/GitHub setup registration. No Register Existing, legacy detection, heuristic inference, confidence scoring, migration/backfill of prior resources, or prompt/title/URL identity inference exists.
- No generic edge interpreter, parallel worker/executor/queue, duplicate Workflow/Alert/GitHub subsystem, auto-merge, release, deployment, arbitrary executable node, or silent schema coercion was introduced.
- Confirmed publication creates/reuses fixed visible adapter tasks and schedules through existing services. Dynamic Implementation nodes represent ordinary Alert/GitHub-linked OpenVibely tasks created after existing approval/assignment gates and projected through explicit provenance.
- The prior full-objective audit passed at `4e971f0` and was recorded at `d2512dd`; authoritative managed memory was subsequently reconciled. Direct product feedback then opened Phase 5 because Blank still assembled preset topology instead of creating custom runnable Automations.

## Remaining Phases

- Reconcile managed memory, create a clean checkpoint, and perform a new edit-free full-objective audit. The old hidden Workflow subsystem is not an Automation capability and is being removed separately per direct user correction.

## Exact Next Action

Checkpoint the validated surfaced custom builder candidate, then reconcile the authoritative managed topic through an authorized direct writer and perform the fresh edit-free audit against the revised full objective.

## Update Contract

After every implementation phase, record:

- completed requirements and acceptance items;
- changed files and migrations;
- tests and validation results;
- audit findings and repairs;
- unresolved decisions or blockers;
- the exact next phase or action.
