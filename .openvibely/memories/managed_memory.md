---
name: managed_memory
type: project
created: 2026-05-09
updated: 2026-09-03
source: consolidation
source_id: memory_consolidation_2026-09-03
confidence: high
title: Managed Memory
---

OpenVibely managed memory is model-backed, tool-driven, project-scoped, and stored under the selected project's repo-local `.openvibely/memories/` directory. The directory is flat: `MEMORIES.md` is the compact routing index and focused top-level topic files hold durable context. The former `user/`, `feedback/`, `project/`, and `runs/` layout is obsolete.

Topic boundaries are intentional: architecture/storage, providers, chat/task threads, lifecycle/skills, worktrees, integrations, alerts, Automation Graphs, realtime/frontend, theming, analytics, testing, product direction, and coding-agent discipline each retain a separate routing handle. Repeated contracts belong in the most specific topic; broader topics retain only cross-cutting invariants or ownership pointers.

Storage and safety:
- A selected project with a valid local `repo_path` is required for memory file operations.
- `MemoryService` owns path resolution, storage, context building, extraction, consolidation, and database metadata. SQLite task/chat execution history is the transcript source; JSONL transcript storage and app-owned memory roots are not current design.
- Startup/runtime paths may create `.openvibely/memories/` and `MEMORIES.md` when absent. Topic files are created only by explicit durable-memory writes.
- Memory schedule seeding is separate from repo-local initialization: the default project may receive the visible Memory Consolidation scheduled task without a repo path, but file operations still require one.
- Memory tools are scoped to the memory directory and reject absolute paths, traversal, and symlink escapes.
- Memory stores durable preferences, product direction, architecture decisions, workflow constraints, current-state facts, recurring pitfalls, incidents, and repeated feedback. It excludes transient logs, raw transcripts, secrets, boilerplate, one-off prompts, provider-internal terminology, task-by-task summaries, Chat prompts, mode-control text, and procedure-only runbooks.
- Static repository guidance belongs in app-managed skills and selected memory, not repo-root `AGENTS.md` or `CLAUDE.md`.

Lifecycle and retrieval:
- The built-in Memory Curator owns `recall_memory`, `update_memory`, and scheduled `consolidate_memory` lifecycle work. Ordinary implementation/audit agents must not edit managed memory or skill Markdown.
- If implementation work encounters memory changes or conflicts during worktree sync, preserve the appropriate target/current state and leave interpretation to Memory Curator.
- The user prefers memory updates to be performed directly by the active lifecycle agent, not delegated. If authorized scoped mutation tools are unavailable, report that limitation.
- Recall is a route-time handle-selection step parallel to Skill Curator selection. Route selection receives only `MEMORIES.md`; topic bodies are loaded on demand through authorized `memory_view`.
- Normal tasks and follow-ups consume route-selected memory; Interactive Chat uses a narrower recall-only preparation path. Selected-memory prompt context is handle-only, and generated summaries/snippets/topics are debug metadata rather than final model context.
- `memory_view` is read-only and request-scoped. It allows route-selected handles and exact indexed handles explicitly requested for the turn, rejects `MEMORIES.md`, paths/traversal, and unindexed handles, and is also an explicit allowed-tool grant in agent configuration.
- Consolidation runs as a normal visible scheduled task assigned to Memory Curator with scoped memory-file tools; hidden bespoke scheduler behavior is not intended.
- The product has no project-level durable-memory browser. A bounded read-only browser backed by the index and safe resolver is tracked in `openvibely/openvibely#32`.
