---
name: agent_lifecycle_and_skills
type: project
created: 2026-05-24
updated: 2026-08-03
source: consolidation
source_id: memory_consolidation_2026_07_17
confidence: high
title: Agent Lifecycle and Skills
---

OpenVibely lifecycle-agent and skill behavior is guided by the on-disk agent/skill catalog plus lifecycle hooks. Exact implementation details are source-authoritative.

Agent and catalog facts:
- Built-in protected system agents include Skill Curator (`skill_curator`), Memory Curator (`memory_curator`), Goal Agent (`goal`), and Loop Agent (`loop`). Fresh-startup initialization must materialize them idempotently from bundled declarations.
- Goal Agent ships under `internal/builtinskills/builtin/agents/goal/` with root `SKILLS.md` and `skills/evaluate_task_goal/SKILL.md`.
- Loop Agent is a protected built-in lifecycle agent and runs after-complete only for tasks with dynamic-loop state enabled.
- Agents are global by default and reusable across projects. Project-scoped agents/skills live under `<project_root>/.openvibely/agents/...`; global agents live under the app/config agents root.
- The on-disk per-agent `SKILLS.md` declaration is authoritative for agent skills, lifecycle hooks, task loading, tool permissions, enabled/disabled state, and declarations. Declaration import/sync must preserve `agent.enabled: false`; missing enabled metadata defaults to enabled, and archived generated agents remain disabled.
- Deleting filesystem-backed non-protected agents must remove the database row, the corresponding `agents/<key>/` directory, and the `## <key>` section from `agents/AGENTS.md`; otherwise declaration sync can rematerialize the agent from `SKILLS.md`, stale metadata can be restored, and deleted agents can remain in catalog/LLM context. Protected system agents remain non-deletable and should surface disabled delete UI plus backend rejection.
- Project-scoped agent create/update/delete and related agent-specific UI/API requests must preserve or recover project context. Backend cleanup/materialization should prefer the agent's persisted `ProjectID` when resolving the project skill root, and frontend agent-specific URLs should carry the active `project_id` query.
- Standalone skills are filesystem-backed packages. `<root>/skills/SKILLS.md` headings are canonical handles and match `<root>/skills/<handle>/SKILL.md`.
- An indexed standalone skill is unusable unless the matching package body exists in the checkout the running app loads; creating the package only inside an isolated task worktree leaves the main catalog pointing at a dead path.
- Bundled-skill startup sync overwrites the embedded `SKILL.md` and merges the bundled index, but does not prune extra support files already present in the installed global package. A global skill may therefore retain `references/` or `templates/` added by an earlier import, update, or Skill Curator operation even when the current repository built-in package ships only `SKILL.md`; a fresh installation from that repository will not receive those absent support files.
- Project scope overrides global scope for matching standalone or agent-owned skill keys. Agent declaration reconciliation caches parsed declarations by filesystem fingerprint so unchanged warm Agents-page requests may scan metadata without rereading/reparsing content or rewriting agent/hook rows. Project-root switches reapply cached declarations to preserve project precedence, while removed or re-keyed project declarations restore any displaced cached global declaration.
- Product direction favors explicit import/index maintenance over automatic disk auto-discovery.
- `skill.enabled: false` disables a skill for task execution, lifecycle hooks, routing, `skill_view`, and context injection; management/admin listings still show disabled skills.
- The bundled `openvibely_github_autonomous_sdlc_bootstrap` and `openvibely_native_autonomous_sdlc_bootstrap` standalone skills are disabled by default so lifecycle routing cannot select setup guidance for maintained Automation tasks. They remain shipped and management-visible for deliberate administrator re-enablement, and bundled startup sync overwrites stale installed enabled copies. Maintained Native/GitHub Automations execute their stored Automation-owned prompts and do not depend on these bootstrap packages at runtime.
- Standalone top-level `always_use` metadata is catalog control data and does not appear in model-visible `<available_skills>` rendering.
- Generated/native OpenVibely declarations include explicit `kind` frontmatter. Explicit skill import surfaces, including `/skills/import` and `skill_import`, materialize packages through shared normalization into `<root>/skills/<handle>/SKILL.md` and update `<root>/skills/SKILLS.md`.
- Skill import normalization guarantees YAML frontmatter with at least `name`, `description`, `kind: skill`, and `enabled: true`; it supports raw Markdown bodies, common top-level `name`/`description` packages, and existing OpenVibely declarations without wholesale clobbering valid fields.
- `skill_import` is treated as a skill-library write capability alongside `skill_manage`; grant it to write-authorized skill/curation agents rather than ordinary task turns.

Project guidance facts:
- The 2026-06-06 guidance migration removed OpenVibely's own root `AGENTS.md`, `CLAUDE.md`, `PRACTICES.md`, and `guardrails.md` files as required app artifacts.
- Static OpenVibely repository guidance belongs in `.openvibely/skills/openvibely_project_guidance/SKILL.md`, indexed by `.openvibely/skills/SKILLS.md`.
- `openvibely_project_guidance` should be selected for applicable ordinary tasks through top-level `always_use`, not hardcoded routing prompt text or a bespoke service path.
- `.gitignore` selectively unignores committed project skills/memories while leaving local app-managed `.openvibely` state ignored.

Skill Curator facts:
- Skill Curator is a recursive self-improvement loop: `observe_task_for_learning` reviews completed task conversations for reusable learnings and can create or patch skills; `maintain_skill_library` consolidates and prunes the skill library on a schedule.
- `observe_task_for_learning` is a Skill Curator `after_complete` hook, not execution as the task's assigned primary agent.
- Cross-agent improvements belong in standalone skills.
- Skill-library maintenance may create, patch, consolidate, or archive skills, but agents are user-managed configurations: do not create, edit, archive, route, reassign, or mutate agent metadata/tools/hooks/attachments as part of skill maintenance.
- Skill Curator consolidation/archive decisions must inspect the full safe package manifest from `skill_view`, including nested files and directories beyond `SKILL.md`; package file actions must explicitly account for safe non-`SKILL.md` contents. If `skill_view` exposes only `SKILL.md` and linked files without `package_manifest`, the incomplete inventory is a hard blocker: do not archive or consolidate the skill based on inferred package contents.
- Assigned-agent updates are reserved for behavior specific to that agent's role, purpose, private workflow, or selected agent-owned skill. Agent-owned skill mutation uses the server-scoped `agent_skill_manage` path.
- Skill catalog maintenance must distinguish intentional global-generic/project-specific layering from true duplication; topic or name overlap alone is not sufficient reason to consolidate packages.

Lifecycle facts:
- Lifecycle hooks live around `internal/lifecycle/` and task execution/server setup. Durable concepts include `route_task`, `before_run`, `after_complete`, `scheduled`, task-mode bookkeeping, blocking/non-blocking execution, idempotency/audit rows, recursion prevention, output contracts, and runtime-tool filtering.
- `route_task` runs before `before_run`. Skill Curator returns selected skill handles; Memory Curator returns selected memory handles. Both can occupy the route slot.
- Built-in route hooks default non-blocking, while the runner waits for route-slot completion before the main model turn starts.
- Lifecycle hook skill resolution is scoped to the hook owner.
- Routing/effective-mode logic has one primary agent/effective mode. Multi-agent permission merging is not part of the current design.
- Ordinary tasks may intentionally have no assigned primary agent. Explicit assigned primary agents skip standalone skill routing and use that agent's curated/default or manual skill selection.
- Maintenance/system agents are excluded from auto-routing via `selectable_as_primary=false`.
- Lifecycle visibility renders structured selected-skill and selected-memory route decisions as compact prompt-safe badges/pills; text summaries remain useful for non-route hook rows.
- Known lifecycle-evidence gaps include prompt-safe lifecycle trace events and durable applied/blocked skill-mutation audit rows being persisted by the backend but not exposed from the task-detail Lifecycle tab, which currently renders execution summary cards. Selected-memory activity is also reduced to filenames even though richer prompt-safe context is available. Issues `#161`, `#168`, `#175`, `#177`, `#201`, `#205`, `#209`, `#219`, and `#220` propose bounded, on-demand prompt-safe lifecycle evidence views without changing lifecycle behavior. Issues `#201`, `#209`, and `#219` specifically propose exposing existing durable lifecycle trace events from task execution cards, while `#205` proposes a prompt-safe task-detail read surface for persisted lifecycle skill-mutation outcomes and `#220` proposes showing prompt-safe memory context; these suggestions were opened with `suggestion` and `feature` labels.
- Lifecycle output contracts constrain final stored/validated results, not the agent's working notes or tool use.
- Lifecycle hook and task-mode terminal execution status writes must use a fresh short-timeout finalization context after hook/model work returns so LLM deadlines or cancellations do not leave rows `running`.
- Resolved incident (fixed 2026-08-07, task `6e8273579fba756d1f103ce266177be1`, commit `317cbbef`): a coding task's required audit-only review turn (task `77da04e6fd8381fdc82f90e01d086a8f`) reported `view_task_thread` failing with `task current not found` and claimed no read-only filesystem/repository inspection tool was available. Root cause was narrower than the model's claim: unlike `resolveTaskIDForTool` (used by goal tools and `send_to_task`), the `view_task_thread` handler (`executeViewTaskThreadRequest` in `internal/handler/chat_processing.go`, wired from `internal/handler/chat_action_tools.go`) never special-cased `task_id="current"` and passed it straight to `resolveTaskReference`/`GetByID`, which fails literally. Fix: `view_task_thread` now defaults `task_id` to `"current"` when both `task_id` and `title` are omitted during a task-thread follow-up, and `executeViewTaskThreadRequest` routes through `resolveTaskIDForTool` so `"current"` resolves to `params.TaskID` in a follow-up and is rejected with a clear error outside one. Regression test `TestViewTaskThreadResolvesCurrentTaskID` added in `internal/handler/chat_action_tools_test.go`. The "no read-only filesystem tool available" half of the original claim was a model misstatement, not a real gap: `read_file`/`list_files`/`grep_search`/`bash` are provisioned by default for coding task turns (see `agentAllowsBuiltInTool` in `internal/llm/anthropic/adapter.go`), and `openvibely_audit_review_workflow` already instructs audit turns to actually attempt those tools before claiming they're unavailable.

Goal and Loop Agent facts:
- Goal Agent evaluation runs as a protected generic `after_complete` lifecycle evaluator, not a deterministic checkpoint. Its authority comes from protected `system_kind=goal` identity and explicit runtime tool grants.
- Goal Agent after-complete evaluation is detached from the user-visible task response and reloads/publishes current goal state after evaluation.
- Goal Agent must remain a generic model evaluator that reconciles concrete transcript evidence with the stored goal, including task-agent claims about actions, files changed, commands run, validation, and remaining issues. Avoid goal-objective keyword parsing, deterministic completion logic, Goal-Agent-specific lifecycle fields, transcript patching, raw-output replacement, or audit-specific hardcoding unless explicitly redesigned.
- Goal runtime tool IDs such as `get_task_goal`, `send_to_task`, `mark_task_goal_achieved`, and `report_task_goal_blocked` are part of the agent tool catalog/UI so grants survive saves.
- `send_message` is part of the agent tool catalog/UI and agent tool normalization, so users can select and persist it in the agent create/edit dialog. Ordinary task execution and task-thread follow-ups may still expose their own narrow default outbound-message runtime independent of that persisted grant; task-send availability is governed by runtime-tool support and channel configuration, not by the catalog checkbox alone.
- Dynamic task-loop wakeups use the protected Loop Agent after-complete hook. Its `schedule_task_wakeup` runtime tool is lifecycle-only and should not be exposed to ordinary task agents by default.
- Runtime agents can mutate schedules but currently lack a project-scoped way to discover schedule identifiers; a bounded schedule-discovery tool surface is proposed in `openvibely/openvibely#84`.
- Loop Agent wakeups are task-thread continuations enqueued through durable `thread_inputs`, not direct worker submissions or separate worker tasks.
- Loop Agent wakeup scheduling is server-side blocked when a task goal is achieved, paused, cleared, blocked, or failed.
- Lifecycle-origin `send_to_task` continuations are rejected when the hook evaluated an older execution and a newer execution exists for the same source task. Freshness checks compare against the hook source task/run and use each logical task run's head/first lifecycle row rather than detached hook-row timestamps.

Scheduled maintenance and UI facts:
- Scheduled maintenance is modeled as normal scheduled tasks assigned to agents unless a future runbook explicitly requires invisible background hooks.
- Fresh installs must idempotently create visible scheduled tasks for `System: Memory Consolidation` and `System: Skill Library Maintenance` during startup, including when the default project exists but has no repo path.
- Scheduled maintenance task titles may remain app/storage identifiers; lifecycle hook input uses prompt-safe titles without low-value internal prefixes such as `System:`.
- The standalone Skills page uses shared shell/sidebar conventions, searchable cards, scope badges, disabled badges, and create/edit controls for Enabled and Always use.
- The agent create/edit dialog aligns with on-disk agent-owned skills and labels the area “Skills.” Lifecycle editing focuses on real hook slots, not `task_mode`.
- The Agents edit modal must hydrate persisted Advanced-tab values from authoritative card/server state, including unchecked booleans such as `enabled` and `selectable_as_primary`, before saving; otherwise default hidden fields can overwrite backend state when a modal is reopened or saved before async detail refresh completes.

