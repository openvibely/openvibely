---
name: agent_lifecycle_and_skills
type: project
created: 2026-05-24
updated: 2026-09-28
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Agent Lifecycle and Skills
---

Agent and skill behavior is governed by on-disk catalogs and lifecycle hooks; source code remains authoritative for implementation details.

- Protected built-in Agents are declaration-defined and reconciled idempotently. Goal Agent is required; Skill Curator and Memory Curator may be disabled. Protected identity, prompts, tools, and hooks cannot be overridden; permitted model/enabled choices remain user-managed. Sync preserves explicit `enabled: false` and archived generated Agents stay disabled.
- Agents are global/reusable by default; project Agents and skills live under the project `.openvibely` library. Per-Agent `SKILLS.md` is authoritative. Project scope overrides global skills; explicit import/index maintenance is preferred over disk auto-discovery.
- Skill disablement excludes routing, hooks, execution, viewing, and context injection but does not hide management entries. Imports reject traversal, absolute/NUL/disallowed/symlink paths and validate support files before mutation. Package/index writes are atomic and roll back together. Removing a standalone skill also removes its `always_use` entry in that scope.
- Project-scoped routes reject foreign project IDs before filesystem mutation. Names are normalized and enabled/selectable primary Agents cannot collide case-insensitively. Failed materialization must compensate database and package/index artifacts.
- Lifecycle order is `route_task` before `before_run`, followed by `after_complete`; Skill Curator returns skill handles and Memory Curator returns memory handles. Route hooks are non-blocking by default but complete before the main turn. Outputs constrain stored results, not working notes/tool use; selected handles are catalog-validated and deduplicated.
- Explicit Stop suppresses optional detached hook/model work but not terminal bookkeeping, capacity cleanup, publication, or audit. Other failures, including deadlines, still run detached after-complete hooks. Final status writes use a fresh bounded context.
- Goal Agent is a generic detached evaluator of transcript evidence against persisted goal state; it must not parse keywords, patch transcripts, replace raw output, or acquire goal-specific lifecycle fields without redesign. Loop wakeups use durable `thread_inputs`, not direct worker submissions.
- Fresh installs create visible scheduled tasks for Memory Consolidation and Skill Library Maintenance, including without a default project repo path. Reconciliation is idempotent, preserves schedule timing, and identifies ownership beyond title alone.
- Agent deletion has no impact preview for affected tasks; per-hook model overrides are not persisted. Exact memory lifecycle details live in `managed_memory.md`.

## Skill Library Maintenance Status

**Latest Review (2026-09-27):** The project skill library (~90+ skills) is well-maintained with no consolidation opportunities or obsolete skills identified. Skills are appropriately specialized by OpenVibely subsystem with distinct routing triggers, purpose-built guidance, and clear separation of concerns (e.g., finder workflows for bugs vs. redundancy vs. optimization vs. features; lifecycle workflow vs. import workflow for distinct infrastructure concerns). Support files are properly organized. Library structure is sound and functioning as designed.
