---
name: agent_lifecycle_and_skills
type: project
created: 2026-05-24
updated: 2026-08-28
source: consolidation
source_id: memory_consolidation_2026_08_28
confidence: high
title: Agent Lifecycle and Skills
---

OpenVibely lifecycle-agent and skill behavior is guided by the on-disk agent/skill catalog plus lifecycle hooks. Exact implementation details are source-authoritative.

Agent and skill catalog facts:
- Built-in protected system agents include Skill Curator (`skill_curator`), Memory Curator (`memory_curator`), Goal Agent (`goal`), and Loop Agent (`loop`). Fresh-startup initialization must materialize them idempotently from bundled declarations.
- Protected built-in reconciliation for Memory Curator, Skill Curator, and Goal Agent should share common declaration-to-agent mapping, persisted-field repair, and declared lifecycle-hook create/update behavior. `MemoryService` still owns memory migration/consolidation scheduling and Memory Curator legacy alias/hook cleanup; `AgentLibraryMaintenanceService` still owns skill/agent root sync, Skill Curator sanitization, Goal Agent fallback lookup, and skill-library maintenance scheduling.
- Goal Agent ships under `internal/builtinskills/builtin/agents/goal/` with root `SKILLS.md` and `skills/evaluate_task_goal/SKILL.md`.
- Loop Agent is a protected built-in lifecycle agent and runs after-complete only for tasks with dynamic-loop state enabled.
- Agents are global by default and reusable across projects. Project-scoped agents/skills live under `<project_root>/.openvibely/agents/...`; global agents live under the app/config agents root.
- The on-disk per-agent `SKILLS.md` declaration is authoritative for agent skills, lifecycle hooks, task loading, tool permissions, enabled/disabled state, and declarations. Declaration import/sync must preserve `agent.enabled: false`; missing enabled metadata defaults to enabled, and archived generated agents remain disabled.
- Deleting filesystem-backed non-protected agents must remove the database row, `agents/<key>/`, and the `## <key>` section from `agents/AGENTS.md`; otherwise declaration sync can rematerialize the agent. Protected system agents remain non-deletable and should surface disabled delete UI plus backend rejection.
- Project-scoped agent skill list/create/update/archive routes reject explicit foreign `project_id` before filesystem mutation; omitted `project_id` resolves to the agent's owning project root. Global agent skill management is unchanged.
- Agent create/update normalizes visible names by trimming whitespace, rejects blank-looking names, and rejects normalized case-insensitive duplicates among enabled/selectable primary agents. Disabled or non-primary duplicates remain allowed; genuinely ambiguous legacy normalized duplicates remain rejected.
- Standalone skills are filesystem-backed packages. `<root>/skills/SKILLS.md` headings are canonical handles and match `<root>/skills/<handle>/SKILL.md`.
- Indexed standalone skills are unusable unless the matching package body exists in the checkout loaded by the running app. Creating the package only inside an isolated task worktree leaves the main catalog pointing at a dead path.
- Bundled-skill startup sync overwrites embedded `SKILL.md` and merges the bundled index but does not prune extra support files already present in installed global packages. Fresh installs receive only files shipped by the repository.
- Project scope overrides global scope for matching standalone or agent-owned skill keys. Declaration reconciliation may cache parsed declarations by filesystem fingerprint; project-root switches must preserve project precedence and restore displaced globals when project declarations are removed.
- Product direction favors explicit import/index maintenance over automatic disk auto-discovery.
- `skill.enabled: false` disables a skill for execution, lifecycle hooks, routing, `skill_view`, and context injection; management/admin listings still show disabled skills.
- Task-turn skill catalog routing and available-skill rendering should determine `skill.enabled` from bounded `SKILL.md` frontmatter reads rather than full body reads. Missing/malformed/absent metadata defaults to enabled, missing package bodies are skipped for route-visible surfaces, and full bodies remain for explicit detail/editor paths.
- Bundled GitHub/Native autonomous SDLC bootstrap skills are disabled by default so lifecycle routing cannot select setup guidance for maintained Automation tasks. They remain management-visible for deliberate re-enablement, and startup sync should overwrite stale installed enabled copies.
- Standalone top-level `always_use` metadata is catalog control data and does not appear in model-visible `<available_skills>` rendering.
- Generated/native OpenVibely declarations include explicit `kind` frontmatter. Import surfaces should materialize packages through shared normalization into `<root>/skills/<handle>/SKILL.md` and update `<root>/skills/SKILLS.md`.
- Skill import normalization guarantees YAML frontmatter with at least `name`, `description`, `kind: skill`, and `enabled: true`, supporting raw Markdown bodies, common package forms, and existing declarations without clobbering valid fields.
- Browser-dialog request-to-declaration conversion for standalone and agent-owned skill saves is centralized before importer persistence. Standalone saves reject agent-root/`agent.key` declarations; agent-owned saves validate `agent.key` scope.
- Open duplication gap `#806`: agent plugin MCP server resolution is duplicated between `ResolveRuntimeBundle` and `pluginServersForIDs`, covering selected-plugin parsing, auto-install/load, deduplication, and sorting for runtime bundles and persistent MCP process reconciliation.
- Open bug `#846`: the browser plugin-install path accepts an agent ID and persists plugin IDs without applying the protected system-agent read-only/`GeneratedStatus` guard, so a user can mutate a protected agent's plugin configuration through installation even though normal agent editing blocks it.
- Per-agent materialization resolves project-scoped declarations and legacy embedded skills from the Agent's recorded `ProjectID`; refreshes must not create a project-B tree under project A. Project-aware declaration sync ignores mismatched stored project IDs, including warm-cache refreshes, while global/unowned legacy rows retain fallback behavior. Cross-project switching, legacy migration, and warm-refresh regressions cover this boundary.
- `skill_import` is a skill-library write capability alongside `skill_manage`; grant it to write-authorized skill/curation agents rather than ordinary task turns.
- The standalone `git_worktree_discipline` skill is intentionally compact at routing time; detailed recovery and prompt-orientation references live in support files.

Project guidance facts:
- The 2026-06-06 guidance migration removed OpenVibely's own root `AGENTS.md`, `CLAUDE.md`, `PRACTICES.md`, and `guardrails.md` as required app artifacts.
- Static OpenVibely repository guidance belongs in `.openvibely/skills/openvibely_project_guidance/SKILL.md`, indexed by `.openvibely/skills/SKILLS.md`.
- `openvibely_project_guidance` should be selected through top-level `always_use`, not hardcoded routing prompt text or bespoke service paths.
- `.gitignore` selectively unignores committed project skills/memories while leaving local app-managed `.openvibely` state ignored.

Skill Curator facts:
- Skill Curator is a recursive self-improvement loop: `observe_task_for_learning` reviews completed task conversations for reusable learnings and can create or patch skills; `maintain_skill_library` consolidates and prunes the skill library on a schedule.
- `observe_task_for_learning` is a Skill Curator `after_complete` hook, not execution as the task's assigned primary agent.
- Cross-agent improvements belong in standalone skills.
- Skill-library maintenance may create, patch, consolidate, or archive skills, but agents are user-managed configurations: do not create, edit, archive, route, reassign, or mutate agent metadata/tools/hooks/attachments as part of skill maintenance.
- Skill Curator consolidation/archive decisions must inspect the full safe package manifest from `skill_view`, including nested files beyond `SKILL.md`. If `package_manifest` is unavailable, do not archive or consolidate based on inferred contents.
- Assigned-agent updates are reserved for behavior specific to that agent's role, purpose, private workflow, or selected agent-owned skill.
- Skill catalog maintenance must distinguish intentional global-generic/project-specific layering from true duplication; topic/name overlap alone is insufficient reason to consolidate.

Lifecycle facts:
- Lifecycle hooks live around `internal/lifecycle/` and task execution/server setup. Durable concepts include `route_task`, `before_run`, `after_complete`, `scheduled`, task-mode bookkeeping, blocking/non-blocking execution, idempotency/audit rows, recursion prevention, output contracts, and runtime-tool filtering.
- `route_task` runs before `before_run`. Skill Curator returns selected skill handles; Memory Curator returns selected memory handles. Both can occupy the route slot.
- Built-in route hooks default non-blocking, while the runner waits for route-slot completion before the main model turn starts.
- Lifecycle hook skill resolution is scoped to the hook owner.
- Routing/effective-mode logic has one primary agent/effective mode. Multi-agent permission merging is not part of the current design.
- Ordinary tasks may have no assigned primary agent. Explicit assigned primary agents skip standalone skill routing and use that agent's curated/default or manual skill selection.
- Maintenance/system agents are excluded from auto-routing via `selectable_as_primary=false`.
- Lifecycle visibility renders structured selected-skill and selected-memory route decisions as compact prompt-safe badges/pills; text summaries remain useful for non-route hook rows.
- Known lifecycle evidence gaps include richer prompt-safe trace events, persisted skill-mutation outcomes, and richer selected-memory context in Task Detail Lifecycle views. Strict audit-only turns must gather repository/task evidence before lifecycle or managed-memory reads when the audit gate requires it.
- Lifecycle activity APIs and the Task Detail Lifecycle tab are project-scoped; foreign or unknown IDs return controlled not-found responses without exposing selected skills/memories, summaries, errors, or event payloads. Canonical ownership details are also recorded in `openvibely_architecture.md`.
- Lifecycle output contracts constrain final stored/validated results, not the agent's working notes or tool use.
- Agent root `SKILLS.md` lifecycle hook declarations can include `payload:` blocks naming context extras. Missing/empty payload means the hook receives every slot-produced block. Protected reconciliation repairs stale hook rows so built-ins keep declared payload scopes.
- `extras.execution_error` is delivered to after-complete hooks on ordinary failed task runs even when omitted from declared payload; its absence on successful runs remains meaningful.
- Explicit user cancellation/Stop suppresses optional non-required lifecycle hook/model work, especially detached hooks such as Goal Agent, Skill Curator, Memory Curator, and Loop Agent. Cancellation still performs required terminal bookkeeping, publication, capacity cleanup, and audit/logging.
- Ordinary non-cancel failures, including `context.DeadlineExceeded`, still run detached `after_complete` hooks with `extras.execution_error` even if terminal bookkeeping later persists `status=cancelled`.
- Lifecycle hook and task-mode terminal status writes must use a fresh short-timeout finalization context after hook/model work returns so LLM deadlines/cancellations do not leave rows `running`.
- Each lifecycle hook invocation persists a sanitized JSON snapshot in `lifecycle_executions.input_json` and an `input_snapshot` trace event, so full hook inputs can materially contribute to SQLite size.
- Canonical task-thread runtime details live in `chat_thread_system.md`.

Goal and Loop Agent facts:
- Goal Agent evaluation runs as a protected generic `after_complete` lifecycle evaluator, not a deterministic checkpoint.
- Goal Agent after-complete evaluation is detached from the user-visible task response and reloads/publishes current goal state after evaluation.
- Goal Agent must remain a generic model evaluator reconciling transcript evidence with the stored goal. Avoid keyword parsing, deterministic completion logic, Goal-Agent-specific lifecycle fields, transcript patching, raw-output replacement, or audit-specific hardcoding unless redesigned.
- Goal runtime tool IDs such as `get_task_goal`, `send_to_task`, `mark_task_goal_achieved`, and `report_task_goal_blocked` are part of the agent tool catalog/UI so grants survive saves.
- `send_message` is part of the agent tool catalog/UI and agent tool normalization. Runtime availability still depends on runtime-tool support and channel configuration.
- Dynamic task-loop wakeups use the protected Loop Agent after-complete hook. `schedule_task_wakeup` is lifecycle-only and should not be exposed to ordinary task agents by default.
- Runtime agents can mutate schedules but currently lack a project-scoped schedule-ID discovery path; `list_schedules` covers bounded schedule discovery where available.
- Loop Agent wakeups are task-thread continuations enqueued through durable `thread_inputs`, not direct worker submissions or separate tasks.
- Loop Agent wakeup scheduling is server-side blocked when a task goal is achieved, paused, cleared, blocked, or failed.
- Lifecycle-origin `send_to_task` continuations reject stale hook executions, cancelled source/destination tasks, and in-process cancellation markers. Freshness checks compare against the hook source task/run. Legitimate activation, follow-up promotion/admission, scheduled work, and swarm restart/follow-up paths clear stale markers.

Scheduled maintenance and UI facts:
- Scheduled maintenance is modeled as normal scheduled tasks assigned to agents unless a future runbook explicitly requires invisible background hooks.
- Fresh installs must idempotently create visible scheduled tasks for `System: Memory Consolidation` and `System: Skill Library Maintenance`, including when the default project has no repo path.
- Open bug `#694`: maintenance scheduled-task reconciliation identifies managed tasks by reserved title only, so a user task with the same title can be overwritten into the system-owned maintenance task during startup/project setup.
- Scheduled maintenance task titles may remain app/storage identifiers; lifecycle hook input uses prompt-safe titles without low-value internal prefixes such as `System:`.
- Daily memory and skill maintenance schedules must be created idempotently with `clear_context_on_start: true`; startup/project reconciliation repairs stale false values while preserving timing.
- The standalone Skills page uses shared shell/sidebar conventions, searchable cards, scope badges, disabled badges, and create/edit controls for Enabled and Always use.
- The agent create/edit dialog aligns with on-disk agent-owned skills and labels the area “Skills.” Lifecycle editing focuses on real hook slots, not `task_mode`.
- Agent create/update browser handlers share server-side modal payload parsing/normalization. Handlers still own construction/loading, protected checks, repository writes, lifecycle hook persistence, disk materialization, legacy skill migration, logging, and list rendering.
- Protected system agents are read-only in the agent modal and skipped by dialog disk materialization. Non-protected lifecycle hook form saves are ordinary user-agent edits and must be audited separately if they need to preserve custom `payload_json`.
- Agent edit modals must hydrate persisted Advanced-tab values, including unchecked booleans such as `enabled` and `selectable_as_primary`, before saving so hidden defaults do not overwrite backend state.
- Open suggestion `#886`: Agents cards currently hide the persisted routing-critical `enabled`, `selectable_as_primary`, and global/project `scope` values even though `/agents` loads them; users can see those controls only in the Advanced modal. Showing this state on each card would explain why an agent is absent from task or schedule pickers without changing routing or selection rules.
