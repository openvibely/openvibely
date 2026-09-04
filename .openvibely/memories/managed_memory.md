---
name: managed_memory
type: project
created: 2026-05-09
updated: 2026-08-28
source: consolidation
source_id: memory_consolidation_2026_08_28
confidence: high
title: Managed Memory
---

OpenVibely managed memory is model-backed, tool-driven, project-scoped, and stored under the selected project's repo-local `.openvibely/memories/` directory. The directory stays flat: `MEMORIES.md` is the compact routing index and focused top-level topic files hold durable context. The old `user/`, `feedback/`, `project/`, and `runs/` subdirectory model is obsolete.

Topic boundaries are intentional: architecture/storage, providers, chat/task threads, lifecycle/skills, worktrees, integrations, alerts, Automation Graphs, realtime/frontend, theming, analytics, testing, product direction, and coding-agent discipline each retain a separate routing handle. Repeated contracts belong in the most specific topic, with broader topics keeping only the cross-cutting invariant or ownership pointer.

Durable storage boundaries:
- Managed memory requires a selected project with a valid local `repo_path` for file operations.
- `MemoryService` owns path resolution, file storage, context building, extraction, consolidation, and DB metadata.
- SQLite task/chat execution history is the transcript source; JSONL transcript storage and app-owned memory roots are not part of the current design.
- Startup/runtime paths may create `.openvibely/memories/` and `MEMORIES.md` when missing. Topic files are created by explicit durable-memory writes.
- Memory schedule seeding is separate from repo-local memory initialization: the default project can receive the visible Memory Consolidation scheduled task even when it has no `repo_path`; actual memory file operations still require a valid local repo path.
- Managed-memory tools are scoped to the memory directory and reject traversal, absolute paths, and symlink escapes.
- Memory stores durable contextual notes: user preferences, product direction, architectural decisions, workflow constraints, current-state facts, recurring pitfalls, incidents, and repeated feedback.
- Memory excludes raw complaints, assistant boilerplate, one-off prompts, transient logs, raw transcripts, secrets, provider-internal terminology, task-by-task summaries, Chat page prompts, mode-control text, and procedure-only runbooks.
- Static repository operating instructions belong in app-managed skills and selected managed memory, not repo-root `AGENTS.md` or `CLAUDE.md`.

Lifecycle and retrieval facts:
- Memory lifecycle work is owned by the built-in Memory Curator through `recall_memory`, `update_memory`, and scheduled `consolidate_memory` skills.
- Ordinary implementation and audit agents must not manually edit managed memory or skill Markdown as part of task work; those files are lifecycle-agent-owned. Authorized lifecycle curator turns should make scoped updates directly.
- If normal implementation/audit work encounters `.openvibely/memories/*` changes or merge conflicts while syncing a worktree, it should preserve the appropriate target/current memory state and leave interpretation to Memory Curator.
- The user explicitly prefers memory updates not be delegated to another task or agent. When the active lifecycle agent has authorized scoped memory mutation tools, it should perform the update directly; if direct mutation is unavailable, report the limitation.
- Recall is a `route_task` handle-selection step with `selected_memories`, parallel to Skill Curator `selected_skills`, and receives only the compact index from `MEMORIES.md`; topic bodies are not loaded during route selection.
- Normal tasks and task-thread follow-ups consume managed memory through route-selected handles. Interactive Chat uses a narrower recall-only lifecycle preparation path.
- Selected-memory prompt context is handle-only for skill-style parity. Memory bodies are loaded only on demand through the authorized `memory_view` tool.
- `memory_view` is read-only and request-scoped. It is authorized only for route-selected indexed handles plus exact indexed handles explicitly requested by the user for that turn, rejects `MEMORIES.md`, paths/traversal/unindexed handles, and is also an explicit agent allowed-tool grant surfaced in the agent create/edit dialog.
- Route-generated memory summaries/snippets/topics are debug metadata, not final task/chat model context.
- Memory consolidation runs as a normal visible scheduled task assigned to Memory Curator with scoped memory-file tools; hidden bespoke scheduler behavior is not intended.
- The product currently lacks a project-level UI for browsing durable memory index/files; a bounded read-only browser backed by `MEMORIES.md` and the existing safe resolver is tracked in `openvibely/openvibely#32`.
