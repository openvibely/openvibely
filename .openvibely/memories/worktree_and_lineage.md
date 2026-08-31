---
name: worktree_and_lineage
type: project
created: 2026-05-09
updated: 2026-08-28
source: consolidation
source_id: memory_consolidation_2026_08_28
confidence: high
title: Worktree and Lineage
---

Task execution uses isolated git worktrees in `.worktrees/task_<id>` with task-scoped branches `task/<id_prefix>-<slug>`. Coding changes for assigned tasks belong in the assigned worktree, not the main checkout, unless the user explicitly requests main-checkout changes. Prompts should orient to the worktree, but runtime workdir enforcement is the source of truth.

Worktree path and lifecycle:
- When a task provides a worktree path, relative tool paths resolve against the agent working directory, not automatically against that task worktree. Default relative file/shell operations should use the assigned worktree as effective repository root whenever `tasks.worktree_path` exists.
- `LLMService.ExecuteTaskWithAgent` creates the worktree before execution, synchronizes from the selected target/default branch when clean, and handles post-execution merge. Startup sync is a real `git merge --no-edit <target>`; initial conflicts fail before model dispatch, while follow-up conflicts can continue in a preserved clean worktree with recovery context.
- Startup sync uses `MergeTargetBranch` when set, otherwise local `main`, detected default branch, or `main` fallback. Local branches are the source of truth by default; remote-tracking branches must not be fetched/merged implicitly. A local `master` without `main` is supported when default detection resolves it.
- Worktree setup fails closed. Local commits need no remote, but an unborn repository has no tree for `git worktree add`; provide initial-commit guidance and never dispatch the coding model in the main checkout.
- Auto-merge and Task Changes support merge commit, fast-forward-only, and squash. Rebase is shown only when both branches have unique commits and no active conflict. Cleanup supports after-merge, keep, and manual policies, preserves active follow-up lineage, and skips locked, dirty, or unmerged worktrees. Chained tasks carry `base_branch`, `base_commit_sha`, and `lineage_depth`.
- Active managed-worktree diffs resolve the target/task merge base against current working-tree state, including committed, staged, unstaged, and untracked files. Full Changes, file summaries, lazy cards, live fragments, periodic snapshots, follow-up persistence, and direct `?tab=changes`/file requests must share live-worktree versus preserved-diff resolution.

Known worktree, diff, and review gaps:
- Open security bug `#30`: untracked-file diff synthesis follows Git paths with `os.Stat`/`os.ReadFile`, so an untracked symlink can expose contents outside the repo. Skip symlinks and enforce resolved-worktree containment before reading.
- Task chaining still has gaps around persisted `ChildModel`, already-created blocked children after `ChildAgentID` edits, durable handoff failure evidence, Chaining-tab child title/prompt editing (`#276`, `#773`), and navigable ordinary parent/child context (`#255`).
- Task Changes review supports inline comments and feedback follow-ups but does not persist an explicit human approval/changes-requested outcome (`#221`). Comment update/delete ownership checks (`#271`), GitHub PR comment synchronization (`#284`), and the dead/wrong-method Cancel Review path (`#286`) remain open.
- `task_commit_stats` is not exposed as per-task evidence across Task Detail surfaces (`#723`). Changes-file parsing uses one `parseWorktreeNameStatus` decoder for tracked name-status records while preserving rename/copy pathspecs, malformed-row skips, deterministic ordering, live untracked enumeration, and state fallback.

Sandbox escape direction:
- A confirmed incident edited the main checkout when prompt/tool orientation pointed there despite an assigned worktree. The durable fix direction is one `executionRoot` derived from `tasks.worktree_path`, shared by shell/file resolution for initial runs and follow-ups.
- Writes outside the sandbox require explicit outside-workspace permission/bypass, not only prompt instructions, because absolute file paths and shell `cd` can escape cwd. Intentional project-root writers such as `.openvibely` system agents retain explicit scope configurations; normal scoped roots resolve against `executionRoot`.
- Automation-generated implementation prompts must not present the main checkout as the operative repository root. Do not add hard containment, confirmation prompts, or deterministic prompt rewriting unless explicitly requested; intentional absolute paths remain a separate policy choice.

Commit and follow-up lineage:
- Auto-commit and GitHub publication subjects are generated from actual worktree diff facts, not task title/prompt/output when they conflict. Subjects are concise, capitalized imperative plain language, with no provider/tool/status boilerplate, task scopes, machinery, or `Changed files:` body. Deterministic fallbacks use diff/path/status facts. Untracked symlinks must not be followed while collecting snippets.
- Follow-ups to merged, stale, conflict-aborted, or squash-accepted tasks must not merge current target into an old historical branch blindly. Historical branches become read-only lineage; fresh `task/<id>-followup-*` lineage starts from the current target, while an active dirty/local follow-up worktree is reused.
- Startup-conflict recovery uses typed `StartupSyncConflictError` with target branch, task branch, worktree, and conflicted files. Failed merge aborts, dirty worktrees, missing branches, setup failures, and non-conflict Git errors remain fatal.

Merge, publication, and recovery evidence:
- Manual merge conflicts are handled results. Changes-tab rebase conflicts abort rebase and surface guidance; an aborted rebase alone must not persist `MergeStatusConflict`. Fast-forward-only merges skip unnecessary rebase when ancestry already permits it. Dirty but non-overlapping target changes are allowed, while Git overwrite/refusal without unmerged files remains a merge failure.
- Revalidate stale `merge_status` and recover conventional worktree metadata before hiding/rejecting merge actions. A conflict-resolution commit in a task worktree does not itself clear persisted conflict status. A branch is merged only when fully reachable from target.
- Local commits, task records, or a clean worktree do not prove remote publication. Verify configured remote, task-branch tip, live PR head, live target ref, current-base file list, checks, issue closure, and review state. Durable publication evidence is `task_pull_requests.published_head_sha` matched to live GitHub `head.sha`; compare against the live target branch, not assumed local `origin/main`.
- Repeated handoff contamination occurred when a task branch merged local-only `main` after publication or when PR-body reconciliation republished branch state. Before repair, preserve advanced/polluted tips on named backup refs; then align the task branch to the verified live PR head and validate scope against the live base. Never use a body update as permission to publish unrelated branch commits.
- Some supported PR reuse paths return success while ignoring a supplied `pr_body`. Re-read the authoritative body after reconciliation; stale broad-suite claims or obsolete timings are a handoff blocker. If no authorized body-only action exists, report it rather than using unsafe branch replacement or unauthorized API mutation. Require a fresh strict read-only audit after repair. Current #880 handoff evidence is concrete: PR #893's source branch and `refs/pull/893/head` point to live SHA `38deecc`, whose tree matches local implementation commit `edb8d68`; the local worktree is clean and both local/live diffs contain the same six issue-scoped paths. The fresh 2026-08-28 audit still found the PR body stale because it reports only the 50-node direct benchmark and omits the required 10-node and end-to-end/contention measurements. No authorized body-only update capability is available, no unauthorized fallback is permitted, and the non-experimental macOS Intel desktop packaged-update check is failing.

Diagnostics:
- Branch/ref/worktree registrations plus persisted task metadata are evidence for lineage drift. A missing assigned directory is a recovery case, never permission to edit the main checkout. An unexpected branch in a local merge error, a newer empty follow-up alongside older implementation commits, or missing merge options may indicate metadata/lineage drift rather than lost code.
- A server-rendered Changes page containing merge actions while the browser does not usually indicates stale HTMX/page state or dropdown visibility before ancestry. Worktree status `exit status 128` may be transient; absence from both the filesystem and `git worktree list` requires metadata recovery.
