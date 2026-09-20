---
name: agent_lifecycle_and_skills
type: project
created: 2026-05-24
updated: 2026-09-19
source: after_complete
source_id: d06503a3e9b632d05f3d4e2e79c070f9
confidence: high
title: Agent Lifecycle and Skills
---

OpenVibely agent and skill behavior is governed by the on-disk agent/skill catalog and lifecycle hooks; source code is authoritative for exact implementation details.

Agents and catalog:
- Protected built-in agents are declaration-defined and reconciled idempotently at startup. Skill Curator (`skill_curator`), Memory Curator (`memory_curator`), Goal Agent (`goal`), and Loop Agent (`loop`) exist only where their declarations/runtime support exists. MemoryService owns memory scheduling/legacy cleanup; AgentLibraryMaintenanceService owns skill/agent roots and library maintenance.
- Agents are global/reusable by default. Project-scoped agents/skills live under `<project_root>/.openvibely/agents` or `skills`; global ones use app/config roots. Per-agent `SKILLS.md` is authoritative for skills, hooks, task loading, tools, enabled state, and declarations.
- Sync preserves user `enabled: false`, defaults absent enabled metadata to true, keeps archived generated agents disabled, and repairs protected identity/tools/hooks without overwriting user-managed model/enabled choices. Goal Agent is required and cannot be disabled; Skill Curator and Memory Curator may be disabled and may select a configured model.
- Protected updates accept only declaration-allowed model/enabled changes; prompt, tools, identity, and other declaration fields remain protected. Maintenance tasks mirror enabled state and selected model configuration.
- Project-scoped routes reject foreign `project_id` before filesystem mutation; omitted scope uses the owning project root. Names are trimmed, blank names rejected, and enabled/selectable primary Agents cannot have case-insensitive duplicates. Failed materialization compensates both DB and package/index artifacts so retries do not reserve stale names.

Skills and imports:
- Standalone skills are indexed by `<root>/skills/SKILLS.md` headings with matching package bodies. Project scope overrides global scope, and declaration caches restore globals when project declarations disappear. Explicit import/index maintenance is preferred over disk auto-discovery.
- `skill.enabled=false` disables routing, hooks, execution, `skill_view`, and context injection but remains visible to management. Missing/malformed frontmatter defaults enabled; missing bodies are skipped on route-visible surfaces. Top-level `always_use` is catalog control data, not model-visible text.
- `skill_import` is a write capability for skill/curation agents, not ordinary turns. Import normalization adds required YAML fields without clobbering valid values. Reject absolute/traversal/NUL/disallowed/symlink paths and validate support files before mutation. Inline package persistence snapshots package/index, atomically replaces local files, rolls back failed body/index/support writes, removes a newly empty parent, and breaks external hard links.
- Browser standalone skill deletion and importer/archive deletion share `agentlibrary.RemoveSkillIndexEntry`, which owns standalone `skills/SKILLS.md` reading, exact-handle heading removal, no-op behavior, frontmatter/body preservation, `always_use` cleanup for the removed handle, and conditional writes. Handler/importer retain scope resolution, directory/archive behavior, HTTP or ImportResult/error context, and response/result handling.
- Deleting or archiving a standalone skill removes that handle from the same scope's top-level `always_use`, so re-importing the same handle does not silently restore Always use (`#1192`). Agent index cleanup remains section-only; preserve project/global scope isolation, custom frontmatter, and importer archive cleanup behavior.
- Deleting a filesystem-backed non-protected Agent removes its DB row, package, and `agents/AGENTS.md` entry. Agent deletion currently lacks an impact preview for affected tasks (`#1024`). Model deletion atomically resets affected reusable Agent model overrides to inherit across single, bulk, and default-transfer paths, including protected agents; lifecycle resolution fails closed for stale persisted IDs with a valid default or actionable missing-model error while preserving legacy provider-model slugs.

Lifecycle contracts:
- Durable concepts include `route_task`, `before_run`, `after_complete`, `scheduled`, task-mode bookkeeping, blocking/non-blocking hooks, idempotency/audit rows, recursion prevention, output contracts, and runtime-tool filtering. `route_task` precedes `before_run`; Skill Curator returns skill handles and Memory Curator returns memory handles.
- Built-in route hooks default non-blocking, but the runner waits for route-slot completion before the main turn. Hook resolution is owner-scoped with one primary Agent; ordinary tasks may have none and maintenance agents are excluded from primary auto-routing.
- `payload:` extras select slot-produced context blocks; `extras.execution_error` reaches ordinary failed after-complete hooks and is absent on success. Hook input is sanitized and persisted with an `input_snapshot` event.
- Explicit Stop/cancel suppresses optional detached hook/model work but still performs terminal bookkeeping, publication, capacity cleanup, and audit/logging. Non-cancel failures, including deadlines, still run detached after-complete hooks. Final status writes use a fresh short-timeout context so cancellation cannot leave rows running.
- Lifecycle outputs constrain stored/validated final results, not working notes or tool use. Selected handles use catalog-owned ordering, trimming, first-valid deduplication, and unknown-handle skipping.
- Lifecycle hook repository listing uses one private `LifecycleRepo.listHooks` path for query acquisition, row closing/scanning, append construction, and iteration-error propagation; public adapters retain distinct projections, predicates, ordering, arguments, and query-error context.

Goals, loops, and maintenance:
- Goal Agent is a generic detached after-complete evaluator: it reconciles transcript evidence with the stored goal and publishes current goal state. It must not parse keywords, patch transcripts, replace raw output, or gain Goal-specific lifecycle fields without redesign.
- Goal tools and `send_message` are catalog entries so grants survive saves; runtime availability still depends on support/configuration. Loop wakeups are durable `thread_inputs`, not direct worker submissions, and are blocked after goal achievement, pause/clear/block/failure. `send_to_task` rejects stale hook executions, cancelled tasks, and in-process cancellation markers.
- Fresh installs create visible normal scheduled tasks for `System: Memory Consolidation` and `System: Skill Library Maintenance`, including without a default-project repo path. Reconciliation is idempotent, preserves timing, repairs `clear_context_on_start=true`, and must identify ownership beyond title alone; title-only matching remains `#694`.
- Agent dialogs hydrate persisted advanced values before saving. Protected-agent locked controls omitted by the browser are treated as unchanged. Agent edit-modal async JSON and lifecycle-hook hydration is fenced by selected-agent identity plus modal generation; Save is blocked until matching loads complete, and close/reopen invalidates stale callbacks (`#1160`).
- Turn-scoped Agent definition reuse is implemented for lifecycle preparation and hooks: task-thread handler-to-worker handoff seeds a fresh per-turn cache with the assigned Agent, setup helpers and hook resolution/invocation reuse hydrated definitions by Agent ID, distinct IDs remain isolated, and later turns load fresh definitions. It is not a global or cross-task cache.
- Per-hook model overrides are not persisted (`#942`), and routing-critical enabled/selectable/scope state is not yet exposed on Agent cards (`#886`).
- Skills management currently shows editable catalog metadata but does not show per-skill usage context such as selected/loaded activity, last use, recent task, or links into Learning analytics; GitHub issue `#1273` tracks reusing existing Skill Analytics data on individual Skills cards.
