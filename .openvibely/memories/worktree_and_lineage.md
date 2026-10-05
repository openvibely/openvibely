---
name: worktree_and_lineage
type: project
created: 2026-05-09
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Worktree and Lineage
---

Coding tasks execute in isolated worktrees under `.worktrees/task_<id>` on task-scoped branches. Assigned work belongs there, not the main checkout, unless the user explicitly asks otherwise. Runtime workdir enforcement is authoritative; prompts only orient the model.

- `tasks.worktree_path` is the effective repository root for relative shell/file operations. Shell and file tools must derive from one execution root. Setup fails closed; never dispatch a coding model in the main checkout when worktree setup fails. Local commits do not require a remote.
- A confirmed incident edited the main checkout because tool/prompt orientation pointed there despite an assigned worktree. The durable correction is shared `executionRoot` resolution for shell and file operations. Absolute paths and shell `cd` cannot be assumed to remain contained; writes outside the sandbox require explicit permission. Do not add hard containment or deterministic prompt rewriting unless requested.
- Auto-merge is explicitly default-off with separate completion and goal-achieved triggers. Both require lease-held live revalidation and idempotent state. Follow-up lineage stores base branch/commit and depth; merged, stale, conflict-aborted, or squash-accepted tasks start from current target, while eligible active dirty follow-ups may reuse their branch.
- A process-wide repository mutation lease is keyed by Git's canonical common directory after path/symlink resolution. Setup/sync, commits, cleanup, publication/branch replacement, merge/rebase, and conflict recovery coordinate through it. Mutations revalidate task, project, repository, branch, target, terminal state, ancestry, conflict ownership, and eligibility while holding the lease.
- Automatic conflict recovery receives only Git-reported conflict paths and safe scoped file tools; it cannot run Git, stage/commit, or access unlisted/traversal/symlink paths. The application stages only marker-free known files and commits. If eligibility changes, only ownership-verified abort may unwind task-owned conflict state.
- Managed diff capture/commit finishes before a task becomes terminal. Commit/PR subjects come from actual diff facts, not title/prompt/output/provider/tool. Untracked symlinks must not be followed. Changes include committed, staged, unstaged, and untracked state.
- Local commit, clean worktree, matching filenames, or green local tests do not prove publication. Verify remote configuration, task tip, live PR head/base/tree/files/checks, issue linkage, review state, and persisted publication SHA. PR branch replacement requires current authorization and `--force-with-lease`; preserve exact prior head before any explicitly authorized recovery.
- Repeated branch drift/merge-forward contamination is a known incident class: unrelated local branches can repollute a repaired task branch while PR/source refs still point elsewhere. Revalidate exact issue-scoped parity immediately before audit/completion. Changed HEAD invalidates prior validation; preserve unexpected heads and use a separate reconciliation followed by fresh strict audit. Never rely on task-specific hashes in durable memory; read live refs.
- Exact-head hosted checks pending or failed block a clean publication verdict. If logs cannot establish cause, leave it unresolved. Strict read-only audits do not mutate files or run build/test/format/generation/fetch/commit/push; see `coding_agent_product_discipline.md` and `testing_coverage_and_performance.md`.
- Change statistics prefer app-produced `task_commit_stats`; Git is only fallback for the pre-stats range. Shared parsing must be bounded and NUL-safe. A per-task statistics projection remains a product gap.
