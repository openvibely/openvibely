---
name: automation_graphs
type: project
created: 2026-07-18
updated: 2026-08-19
source: consolidation
source_id: memory_consolidation_2026_08_19
confidence: high
title: Automation Graphs
---

Automation Graphs is OpenVibely's project-scoped, reviewable orchestration surface. It compiles visible Tasks, Schedules, Alerts/GitHub handoffs, workers, queues, and current-graph provenance; it is not a parallel graph executor, queue, worker, or arbitrary-code runtime.

Canonical authoring and persistence:
- Automation YAML is the sole configuration authoring surface for Template, Describe, Custom, direct Chat Save, and saved Edit flows. Graph mode is browser-local topology/layout editing only; node and edge configuration stays in YAML.
- YAML uses schema version 1 and the existing candidate model. Strict decoding rejects malformed/multi-document YAML, aliases/anchors, duplicate keys, non-string mapping keys, unsupported fields/tags, unsafe configuration, invalid topology, unsupported capabilities, and foreign-project references before resource effects.
- Chat `save_automation` accepts exact canonical Automation YAML through `source: "yaml"` and `automation_yaml`, uses the same PreviewSave/Save pipeline as browser builder, and rejects stale/raw candidate fields before YAML decode/save.
- Chat Automation lifecycle actions `run_automation_now`, `pause_automation`, and `resume_automation` are Orchestrate-only writes. Plan mode stays read-only. Runtime execution resolves exact current-project Automation IDs or exact case-insensitive names and delegates to `AutomationLifecycleService`.
- Draft schedule config validation is shared by custom trigger schedule nodes and maintained adapter schedule resources for timing, recurrence, enabled, and `clear_context_on_start`; maintained adapters keep separate `schedule_target` validation.
- Existing graph-backed Automations serialize to YAML on read for compatibility. Opening/previewing/saving does not silently migrate, rewrite prompts, replace point-in-time configuration, recreate resources, or backfill pre-graph resources.
- Each Automation has exactly one current saved graph. Internal graph revision IDs isolate runtime state but are not durable Automation or mailbox identities.
- In origins/ownership keys, `automation_id` means the saved Automation row's internal ID, not adapter/template key or display name.
- Save atomically persists Automation identity/current graph, materialized Tasks/Schedules, topology, lifecycle-compatible admission, resource membership, and old runtime-projection removal in one transaction. After commit, eligible Active roots are submitted.
- PreviewSave and Save share validation/normalization/project/capability/agent-reference checks. Invalid Save fails before persisting Automations, tasks, schedules, or resources.
- Save preserves Active, Paused, and Archived lifecycle state. Active enables owned Schedules; Pause/Archive disable them and demote pending current-graph work; Resume restores eligible lifecycle-demoted work.
- New canonical Schedules use `clear_context_on_start=true`; trusted Edit/Save preserves older bound values when graph metadata omits the key.
- Schedule and ordinary Task nodes can carry a bounded per-node task goal and optional `model_config_id`. Blank/missing/`default` use normal runtime fallback; explicit IDs materialize into `tasks.agent_id`.
- Native SDLC implementation nodes expose only `goal` plus `model_config_id` in Preview/Edit because prompt is supplied by Approved Inbox. GitHub SDLC implementation nodes expose static prompt/category/priority plus optional `model_config_id`.
- New maintained SDLC template creation fills blank/missing task-node `model_config_id` with `default`, preserving dynamic runtime default lookup. Existing Automations are point-in-time prompt/model snapshots.
- Draft-node default configuration is centralized in `DefaultAutomationDraftNodeConfig`, covering common category, priority, goal, model sentinel, schedule cadence, approval, labels, notification guidance, and PR options.
- Maintained-template update availability is gated by `CurrentAutomationTemplateRevision(adapterKey)`. Existing lower-revision Automations need explicit maintained-template update/edit/save/recreation to adopt new embedded template YAML.

Builder and UI contracts:
- Supported web creation paths are Template, Describe, and Custom; Custom is the runnable blank builder. Native SDLC and GitHub SDLC are maintained starting templates. Vision Driver remains internal for already-saved graph reconstruction.
- Template selection loads embedded canonical YAML through the strict decoder without creating resources.
- Saved Edit, maintained Template selection, Custom blank creation, and Describe-generated builders use `Automations / [editable automation name]`; name editing and primary save actions live in the breadcrumb/header.
- Live primary actions live in the breadcrumb header; kebab menu is for lifecycle/template/delete actions.
- Builder/Edit YAML is browser-local until Save. Selecting Graph validates through non-persisting PreviewSave before applying topology/layout mutations. Live renders saved YAML and graph read-only.
- Live background refreshes may swap the detail fragment, but must preserve selected Graph/Details/YAML view per visible Automation and reset that memory when Automation ID changes.
- Live/read-only YAML panel should visually match Edit YAML: line numbers, highlighting, indentation guides, no word wrap, horizontal scrolling, read-only selection.
- Automation YAML highlighting keeps string values neutral whether quoted or plain; booleans/numbers/null keep scalar coloring.
- Fold/collapse controls and word wrap are disabled until deliberately re-enabled with regression coverage.
- The graph editor can add/delete nodes, connect/reconnect/delete edges, and move nodes. Every canvas mutation updates YAML. Add-node modal edits must not reserialize or discard unsaved YAML until submitted.
- Canvas-generated YAML should use readable block-style YAML for nested maps/arrays, not inline JSON blobs.
- Details destructive node/edge controls use icon-only trash buttons with descriptive `aria-label`s.
- Template preview should not show a separate browser-local `Suggested nodes` section.

Maintained templates and custom topology:
- Maintained Native and GitHub SDLC templates are starting graphs, not fixed schemas. Every template node may be deleted, and valid reduced graphs remain saveable.
- Maintained Native and GitHub SDLC templates have five scheduled roles and no Loop Auditor: Vision Suggestions, Bug Finder, Optimization Finder, Redundancy Finder, and the Native/GitHub inbox. Loop Auditor is absent from canonical restore-node metadata for new maintained templates.
- The retained graph is authoritative: action/workflow stages determine capability readiness and runtime authorization, not prompt wording or separate required-capabilities metadata.
- A producer with no action edge is a valid standalone schedule. Removing a node removes its owned Schedule/resource membership and incident edges while preserving valid shared targets and durable backing Tasks required by ownership semantics.
- Re-adding a maintained node may reclaim only the unique, unbound, unscheduled same-project Task whose durable `automation:<automation-id>:<node-key>` origin exactly matches that Automation and node; ambiguous/cross-Automation/still-bound candidates fail closed.
- Add Node offers Custom Automation capabilities plus `Restore:` choices for deleted canonical nodes. User-added nodes use Custom validation/materialization; canonical nodes retain maintained behavior.
- Custom Native mailbox topology is `Schedule/Task -> Create notification -> Human approval -> Approved inbox -> Implementation projection -> Outcome`.
- Custom GitHub mailbox topology is `Schedule/Task -> Create GitHub issue -> Human assignment -> GitHub inbox -> Task projection -> Open pull request -> Human review -> Outcome`.
- Native and GitHub mailbox stages cannot mix. Unsupported handoffs, executable cycles, ambiguous duplicate targets, self-edges, dangling endpoints, unsafe configuration, and arbitrary executable behavior fail closed.
- Describe/repair generation treats issues assigned to the PAT owner or configured GitHub Authorized Users as eligible even when manually created in GitHub. Generated GitHub inbox prompts should identify upstream producers as issue sources, not eligibility limits.

Runtime and approval contracts:
- Runtime projects state onto the current graph while using normal Task, Scheduler, Alert, GitHub, worker, and queue paths.
- Trigger schedules have exclusive ownership. Worker/inbox resources may be shared only when an adapter explicitly permits it.
- Runtime persistence covers current-graph invocations, leased dispatch, reservations, work items, activities, transitions, live projection, metrics, and health.
- Durable authorization for long-lived external handoffs must be keyed independently of replaceable graph-version rows when artifact/task relationships survive graph replacement.
- Scheduled Automation execution snapshots schedule owner graph version and physical trigger node when the occurrence is claimed. In-flight execution may carry old binding after a save; stale-binding repair authorizes only compatible same-logical actions.
- Already-started issue creation may retain authority after graph save when current graph still contains same logical producer connected to issue creation. Destructive mutations such as branch replacement stay stricter.
- Initial Automation-dispatched GitHub implementation runs mark Automation-created tasks as `OriginTask`, so `github_open_pull_request` can use durable issue-task provenance on first runs and follow-ups after graph replacement.
- Native notification durable ownership is `project_id + automation_id + alert_id`; a current active same-project Native inbox proves live processing authority.
- GitHub issue mailbox-owner mappings were retired on 2026-08-08; assigned GitHub issues are discovered/reconciled by current inbox scans and execution provenance.
- Duplicate prevention for GitHub/Native SDLC is existing-work-first, not model-authored idempotency keys. Finders must list existing Automation-owned artifacts, inspect compact metadata, hydrate likely matches only, skip covered findings, continue searching, create at most one new issue/notification per run, and report no finding rather than create weak duplicates.
- Human approval authorizes configured implementation handoff only, never merge/release/deploy/destructive remediation/credential changes/arbitrary execution.
- Native notification handoffs use explicit approval, atomic lease claim, atomic Backlog implementation-task creation/linkage, exact linked-task execution, and processing completion only after execution starts. Inbox scans collect all pages before mutation.
- Scheduled Native inboxes derive project scope from persisted caller task and scan both read states. Automation-origin metadata is routing evidence; durable authorization is keyed separately.
- Native Approved Inbox Live can legitimately show `Running` from hidden/internal Automation state even when no ordinary Active task card is visible. If no scheduled inbox task, inbox activity, active work-item position, or pending queued input exists while node still shows `Running`, suspect stale-running projection.
- GitHub assignment to PAT owner or configured Authorized User is approval to implement. GitHub issue implementation Tasks are Active and auto-submitted; `execute_tasks` is for reconciled Backlog or failed/cancelled work.
- External mutations reload persisted Task and current Automation context. Visible GitHub tools do not authorize mutation by themselves, and local work does not prove PR publication.
- GitHub SDLC implementation tasks with issue-task provenance require live verified open/reviewable PR evidence before final completion. Missing/stale PR rows or live head SHA mismatch block completion.
- Loop Auditors are inspect/report-only for direct task/swarm/execution creation; those grants are removed based on durable `automation:<id>:auditor` origin.
- `Run now` manually dispatches each schedule-owned entry task using persisted prompt, Agent, model, and tool configuration. It does not mutate timing, skips queued/running/reserved entries, and rejects Paused/Archived Automations.
- Automation scheduled occurrence idempotency is scoped to owned schedule ID plus scheduled time. Crash recovery should reuse committed occurrences rather than start duplicates.
- Enabled repeating Automation schedules whose `next_run` points at a terminal past invocation should advance instead of returning old invocation forever.
- Automation Live no longer surfaces manual GitHub refresh state; `AutomationReconciler` refreshes stale tracked PR state in the background only while Live is recently open/polling.

GitHub and notification content standards:
- Automations use the selected project's repository identity: explicit `repo_url` first, otherwise a GitHub remote from the local checkout. Automation prompts/tools cannot override it.
- GitHub publication readiness requires usable provider authentication, at least one visible GitHub Authorized User, and a resolvable project repository before creating Automation resources.
- Existing-work listing tools return compact metadata and support pagination. GitHub issue lists omit body excerpts; Native notification lists omit message/body/metadata/idempotency key. Finders hydrate only likely matches via detail tools.
- Native notifications and GitHub issues should start with a short nontechnical `Summary`, then include technical evidence, risk, implementation guidance, acceptance criteria, references, and regression cases. Bug findings require concrete failure paths; optimization findings require measurable evidence; redundancy findings require repeated locations and minimal consolidation.
- GitHub Dev Inbox scans configured Authorized Users and PAT identity when available, deduplicates by repository/issue, and creates work after assignment approval and task/PR reconciliation. Assigned open issues are eligible even when manually created.
- Automation-bound GitHub SDLC turns cannot post issue-status comments. `github_open_pull_request` requires `## Summary`, `## Validation`, and exact `Closes #<source issue>` matching trusted provenance.
- For stale-version implementation tasks, PR publication is authorized only through durable same-project issue-task provenance plus current active graph's retained implementation-to-PR policy. Spoofed tasks and other stale-origin writes fail closed.

Current gaps and incidents:
- Resolved 2026-08-15 duplicate-finder incident: repeated SDLC finder runs could create duplicates when identity depended on unstable model-supplied keys/title hashes. Durable fix is existing-work-first duplicate prevention; generic/direct-caller idempotency support remains separate.
- Automation graph repository persistence should use shared graph-writing code accepting neutral node/edge specs, returning node-key-to-ID map for resource binding, and preserving graph metadata, trigger ownership, maintained registrations, and fail-closed unknown-node handling.
- Open Automation UI scanability gaps: Automation cards should surface next/last run, active work, counts, and resource summaries; Automation Live Details should expose linked resources, resource URLs/statuses, status counts, and task links equivalent to canvas drill-downs (`#474`, `#480`).
- Automation Live counts should rank activities by current projection identity (`node + work_item`), apply only latest activity before state priority, and ignore nonterminal activity states for completed work items while preserving real waiting/recent-completion signals.
- Resolved 2026-08-16 save-during-run incident: saving an Automation deletes old graph-version runtime projection rows; current-graph task-backed nodes should fallback to active retained task executions when no current-graph activity represents that execution.
- Gap `#490`: Automation Live detail should use compact `automation_live_activity_states` instead of ranking full activity history during page render.
- Node display state prioritizes `failed > blocked > running > waiting_human > recently_completed`, so active work is labeled Running even when waiting items remain visible.
- Automation authoring should not rely on weak per-Automation `skills` or `source_files` hints; use prompt text for source reading and let Skill Curator route skills.
- Vision Suggestions should explicitly check for root `VISION.md` and read it before choosing a finding. Existing saved Automations require update/edit/save/recreation to adopt corrected prompts.
- Stale-origin GitHub SDLC implementation tasks can publish PRs after graph replacement only when durable graph-independent issue-task provenance proves task/source-issue relationship.
- Describe-generated and custom Automation snapshots require explicit Edit/Save or recreation to receive corrected prompts. Already-created Backlog tasks are not retroactively started.
- Stale-version repair coverage should include queued input bindings and dependent child work items so they are removed before deleting stale parent work items.
- Native alert ownership is stable by project/Automation/alert. Deleting and recreating a Native inbox inside the same Automation can allow old approved notifications to be processed by the new/current inbox.
- Human Approval `waiting_human` can be backed by real pending actionable-notification state rather than stale projection; diagnose alert decision/processing state before treating it as projection bug.
- Automation-owned scheduled tasks must remain generic OpenVibely tasks with the shared tool surface. Missing tools belong in shared allow-lists; excessive authority belongs in service-layer authorization checks.
- Gap `#705`: Custom Automation task handoff activation rebuilds the child prompt from a target-only mini-candidate, so downstream action edges such as `open_pull_request` can be dropped from implementation-task instructions.
