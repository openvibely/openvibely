---
name: managed_memory
type: project
created: 2026-05-09
updated: 2026-09-15
source: consolidation
source_id: memory_consolidation_2026-09-15
confidence: high
title: Managed Memory
---

OpenVibely managed memory is model-backed, tool-driven, and project-scoped under the selected repository's `.openvibely/memories/` directory. The directory is flat: `MEMORIES.md` is the compact routing index and focused top-level topic files hold durable context. The former `user/`, `feedback/`, `project/`, and `runs/` layout is obsolete.

Storage and safety:
- A selected project with a valid local `repo_path` is required for memory file operations. Scoped memory tools reject absolute paths, traversal, and symlink escapes.
- `MemoryService` owns path resolution, context building, extraction, consolidation, and database metadata. SQLite task/chat history is the transcript source; JSONL transcripts and app-owned memory roots are not current design.
- Runtime initialization may create the memory directory and index when absent; topic files are created only by explicit durable-memory writes. Memory schedule seeding is separate from repo-local initialization.
- Durable memory includes preferences, product direction, architecture decisions, workflow constraints, current-state facts, recurring pitfalls/incidents, and repeated feedback. Exclude transient logs, raw transcripts, secrets, boilerplate, one-off prompts, provider-internal terminology, task summaries, Chat prompts, mode-control text, and procedure-only runbooks.
- Static repository guidance belongs in app-managed skills and selected memory, not root `AGENTS.md` or `CLAUDE.md` files.

Lifecycle and retrieval:
- Memory Curator owns `recall_memory`, `update_memory`, and scheduled `consolidate_memory`; ordinary implementation/audit agents must not edit managed memory or skill Markdown.
- The user prefers authorized memory updates to be performed directly by the active lifecycle agent, not delegated. If scoped mutation tools are unavailable, report that limitation.
- Recall selects handles at route time from `MEMORIES.md`, parallel to Skill Curator selection. Topic bodies are loaded on demand through authorized, read-only `memory_view`; selected prompt context is handle-oriented rather than a dump of all topics.
- `memory_view` is request-scoped and read-only. It permits route-selected or explicitly indexed handles, rejects the index itself, traversal, and unindexed handles, and is an explicit allowed-tool grant in agent configuration.
- Consolidation is a normal visible scheduled task assigned to Memory Curator, not hidden scheduler behavior. A project-level durable-memory browser is not implemented; bounded read-only browsing is tracked by `openvibely/openvibely#32`.
