---
name: worktree_and_lineage
type: project
created: 2026-05-09
updated: 2026-09-19
source: mixed
source_id: memory_update_2026-09-19_fd37f46
confidence: high
title: Worktree and Lineage
---

Coding tasks execute in isolated Git worktrees under `.worktrees/task_<id>` on task-scoped branches such as `task/<id_prefix>-<slug>`. Assigned work belongs there, not in the main checkout, unless the user explicitly asks otherwise. Runtime workdir enforcement is authoritative; prompts only orient the model.

Worktree lifecycle:
- When `tasks.worktree_path` exists it is the effective repository root for relative shell/file operations. `LLMService.ExecuteTaskWithAgent` creates/synchronizes the worktree and handles post-execution merge.
- Startup sync is a real `git merge --no-edit <target>`; `MergeTargetBranch` wins, followed by local `main`, detected default branch, or `main` fallback. Remote-tracking branches are not fetched implicitly.
- Setup fails closed. Local commits need no remote, but an unborn repository has no tree for `git worktree add`; never dispatch a coding model in the main checkout when setup fails.
- Auto-merge has two independent explicit default-off settings and an optional target: `auto_merge` after successful execution and `auto_merge_on_goal_achieved` after an exact durable goal transition to `achieved`. Completion merging is allowed during managed finalization; goal merging requires a completed task. Both use one lease-held, live-revalidated, idempotent path.
- Goal-triggered merging is durable rather than callback-only. Achievement is reconciled after terminalization and maintenance and verifies task/goal identity, status, project, source branch, target, worktree cleanliness, and ancestry before recording `merged` or cleanup.
- Follow-up lineage records `base_branch`, `base_commit_sha`, and `lineage_depth`. Follow-ups to merged, stale, conflict-aborted, or squash-accepted tasks start from the current target; active dirty/local follow-ups may reuse their branch. Changes consistently includes committed, staged, unstaged, and untracked state.

Sandbox and safety:
- A confirmed incident edited the main checkout because prompt/tool orientation pointed there despite an assigned worktree. The durable fix is one `executionRoot` derived from `tasks.worktree_path`, shared by shell and file resolution for initial/follow-up runs. Prompts must not present the main checkout as operative root.
- Writes outside the sandbox require explicit outside-workspace permission/bypass. Absolute paths and shell `cd` cannot be trusted to remain within cwd. Intentional project-root writers, including `.openvibely` system agents, retain explicit scope configuration. Do not add hard containment or deterministic prompt rewriting unless requested.
- Untracked-file diff synthesis skips symlinks and enforces resolved-worktree containment (`#30`).

Git mutation and recovery:
- A process-wide repository mutation lease is keyed by Git's canonical common directory after absolute-path and symlink resolution. It coordinates setup/sync, commits, cleanup, publication/branch replacement, merge, rebase, and conflict recovery across aliases, linked worktrees, and service instances.
- Merge, rebase, resolve, abort, cleanup, and publication revalidate live task/project/repository/branch/target/terminal/ancestry/conflict ownership and operation-specific eligibility while holding the lease. Stale requests return controlled errors.
- Automatic conflict recovery carries trigger identity across lease reacquisition. The resolver receives only exact Git-reported conflict paths and safe scoped file tools; it cannot use Git, staging, commits, absolute/traversal paths, `.git`, unlisted paths, or symlinks. The application stages only marker-free known files and creates the commit.
- If eligibility changes during conflict recovery, no resolution commit or target integration occurs; only ownership-verified abort may unwind the task-owned conflict. `MERGE_HEAD` and unmerged files identify recovery ownership. Squash conflicts without `MERGE_HEAD` restore only paths introduced by the squash/conflict, preserving unrelated staged work.
- NUL-delimited Git output preserves unusual filenames. Recoverable failures return authoritative refreshed fragments; genuine conflicts show Resolve/Abort and hide ordinary merge actions.

Commits and publication:
- A task remains non-terminal until managed diff capture/commit finishes. Auto-commit and GitHub subjects come from actual diff facts, not title, prompt, output, provider, tool, status, or generated-file lists. Use concise capitalized imperative language and never follow untracked symlinks while collecting snippets.
- A local commit, clean worktree, matching filenames, or green local tests does not prove publication. Verify remote configuration, task tip, live PR head/base, exact tree/blob set, file list, checks, issue linkage, review state, and `task_pull_requests.published_head_sha`.
- GitHub PR branch replacement requires current Automation authorization and `--force-with-lease`. If an explicitly authorized repair must occur without the guarded runtime, preserve the exact remote head in a named backup ref, use one exact lease, and persist the verified replacement SHA only after success. Validation/push failure leaves the prior publication snapshot unchanged.
- Worktree handoff drift and merge-forward contamination are recurring risks. Before metadata reconciliation, recheck target/live refs and preserve unexpected heads instead of blindly retrying. A changed HEAD invalidates predecessor validation; unrelated contamination, local-main fast-forwards through a task, or missing exact-head evidence blocks completion until separate non-audit reconciliation and a fresh strict audit pass. If a task was explicitly constrained to rebase-only history, a clean merge commit still blocks a clean audit verdict even when the task branch was not merged into `main`.
- Multiple 2026-09 task branches repeatedly repolluted after repairs by merging unrelated local `main`/task heads while PR/source refs stayed at issue-scoped candidates. Treat this as a durable workflow incident: exact issue-scoped parity must be revalidated immediately before audit/completion, and prior validation cannot be carried across a merge tip with unrelated files even if the worktree is clean.
- When a branch has drifted after a clean repair or validation, use a non-audit reconciliation turn to restore the intended issue-scoped head, preserve unexpected heads before replacement, and then run a fresh strict read-only audit on the exact restored state. Do not store or rely on task-specific commit hashes in durable memory; use live Git/PR state for current publication details.
- Generated templ-output conflicts may be resolved by regenerating from marker-free sources, but that fact does not by itself prove publication or exact-head parity. A local clean worktree, local validation, or unchanged target-area blobs cannot substitute for branch/source/PR parity checks.
- Change statistics prefer app-produced `task_commit_stats` and use Git only for the true pre-stats range. Shared bounded NUL-safe numstat parsing preserves rename/copy paths, unusual names, deterministic ordering, untracked files, and state fallback. The per-task stats projection remains unavailable (`#723`); the earlier numstat corruption issue is resolved.
- Known redundancy: `ExecuteTaskWithAgent` commits managed-worktree changes during diff capture, then calls `HandlePostExecution`, which rebuilds commit context and attempts the same task-output commit path again; tracked as duplication issue `#1274`.
