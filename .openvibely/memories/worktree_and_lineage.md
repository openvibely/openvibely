---
name: worktree_and_lineage
type: project
created: 2026-05-09
updated: 2026-09-06
source: consolidation
source_id: memory_consolidation_2026-09-06
confidence: high
title: Worktree and Lineage
---

Coding tasks execute in isolated Git worktrees under `.worktrees/task_<id>` on task-scoped branches such as `task/<id_prefix>-<slug>`. Assigned work belongs in that worktree, not the main checkout, unless the user explicitly asks otherwise. Runtime workdir enforcement is authoritative; prompts only orient the model.

Worktree path and lifecycle:
- When `tasks.worktree_path` exists it is the effective repository root for relative shell/file operations. `LLMService.ExecuteTaskWithAgent` creates/synchronizes the worktree before execution and handles post-execution merge. Startup sync is a real `git merge --no-edit <target>`; `MergeTargetBranch` wins, followed by local `main`, detected default branch, or `main` fallback. Remote-tracking branches are not fetched implicitly.
- Setup fails closed. Local commits need no remote, but an unborn repository has no tree for `git worktree add`; never dispatch a coding model in the main checkout when setup fails.
- Auto-merge and Task Changes support merge, fast-forward-only, and squash. Rebase is available only with two-sided unique commits and no active conflict. Cleanup respects after-merge/keep/manual policy and skips locked, dirty, or unmerged worktrees.
- Follow-up lineage records `base_branch`, `base_commit_sha`, and `lineage_depth`. Follow-ups to merged, stale, conflict-aborted, or squash-accepted tasks start fresh branches from the current target; historical branches are read-only lineage, while an active dirty/local follow-up may be reused.
- Active Changes resolve target/task merge base across committed, staged, unstaged, and untracked state. Full diffs, summaries, lazy cards, live fragments, snapshots, follow-up persistence, and direct file/tab requests must share that resolution.

Sandbox and safety:
- A confirmed incident edited the main checkout because prompt/tool orientation pointed there despite an assigned worktree. The durable fix is one `executionRoot` derived from `tasks.worktree_path`, shared by shell and file resolution for initial and follow-up runs.
- Writes outside the sandbox require explicit outside-workspace permission/bypass. Absolute paths and shell `cd` cannot be trusted to remain within cwd. Intentional project-root writers, including `.openvibely` system agents, retain explicit scope configuration; ordinary scoped roots resolve against `executionRoot`.
- Automation prompts must not present the main checkout as the operative root. Do not add hard containment or deterministic prompt rewriting unless requested; intentional absolute paths are a separate policy decision.
- Open security bug `#30`: untracked-file diff synthesis can follow a symlink outside the repo. Skip symlinks and enforce resolved-worktree containment before reading.

Git mutation and recovery:
- A process-wide repository mutation lease is keyed by Git's canonical common directory after absolute-path and symlink resolution, so aliases, linked worktrees, and separate service instances coordinate. It covers setup/startup sync, commits, cleanup, publication/branch replacement, merge, rebase, and conflict recovery.
- Merge, Rebase, Resolve, and Abort revalidate live task, branch, target, terminal status, ancestry, conflict ownership, and operation-specific eligibility while holding the lease. Stale requests return controlled errors rather than mutating unexpected state.
- `MERGE_HEAD` and unmerged files identify the task that owns recovery. Squash conflicts without `MERGE_HEAD` restore only paths introduced by the squash/conflict, preserving unrelated staged work. NUL-delimited Git output preserves unusual filenames.
- A task remains non-terminal until managed post-execution diff capture/commit finishes. Conflict recovery and status persistence occur under the same lease. Recoverable failures return authoritative refreshed fragments; genuine conflicts show Resolve/Abort and hide ordinary merge actions.
- Task-card merge/PR actions reload ownership and repository scope and rerun live eligibility inside the mutation lease. The batched relationship snapshot skips stale refs per card rather than disabling valid cards. Fast-forward operation failures return a swap-safe authoritative board refresh and failure toast; missing and foreign PR requests are indistinguishable.
- Merge and Rebase share operation-parameterized preflight in `internal/handler/worktree_handler.go`; operation-specific validation, result types, wording, and conflict behavior remain separate.

Commits, lineage, and publication:
- Auto-commit and GitHub publication subjects come from actual diff facts, not task title/prompt/output. Use concise capitalized imperative language without provider/tool/status boilerplate or generated file lists. Never follow untracked symlinks while collecting snippets.
- Manual merge conflicts are handled outcomes. Fast-forward skips needless rebase when ancestry permits. Rebase-only preparation changes the task branch onto the current local target and leaves `main` untouched; verify ancestry, clean status, and absence of a task-side merge commit.
- A local commit, task record, clean worktree, or matching filenames does not prove publication. Verify remote configuration, task tip, live PR head/base, exact tree/blob set, file list, checks, issue linkage, review state, and `task_pull_requests.published_head_sha`.
- Startup synchronization can pollute an already-published task branch when local `main` advances. Preserve polluted/pre-rewrite tips in clearly named backup refs, restore the exact published candidate, and recheck the target after long validation because concurrent lifecycle work can advance it again.
- A strict read-only audit is a separate post-repair turn. It inspects exact worktree/lineage, implementation scope, live refs, PR body/files/checks, issue linkage, and review state; it performs no mutating validation and discloses skipped checks.

Known gaps:
- Task-detail Worktree rendering repeats repository/recovery and Git ancestry work before file stats (`#915`). Task Detail and Changes independently load optional repository guards and review comments (`#945`). Consolidate only shared loading while retaining route-specific behavior.
- New Scheduled Task does not expose the persisted worktree auto-merge option (`#982`), although backend and post-execution handling already support it.
- `task_commit_stats` is not shown as per-task evidence across Task Detail (`#723`). Changes parsing must preserve rename/copy paths, unusual filenames, deterministic order, live untracked files, and state fallback.
