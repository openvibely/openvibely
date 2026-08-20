---
name: worktree_and_lineage
type: project
created: 2026-05-09
updated: 2026-08-19
source: consolidation
source_id: memory_consolidation_2026_08_19
confidence: high
title: Worktree and Lineage
---

Task execution uses isolated git worktrees in `.worktrees/task_<id>` with task-scoped branches `task/<id_prefix>-<slug>`. LLM task prompts include explicit worktree path orientation when a workdir is present, while runtime workdir enforcement remains the source of truth. Coding changes for assigned tasks must be made in the assigned task worktree, not the main checkout, unless the user explicitly asks for main-checkout changes.

Worktree path discipline is mandatory when a task provides a worktree path: relative tool paths resolve against the agent's working directory, not automatically against the task worktree.

Durable worktree model:
- Auto-merge supports merge commit, fast-forward only, and squash merge.
- Task Changes local actions include merge commit, fast-forward only, and squash merge. Rebase onto target/default branch is shown only when both target and task branches have unique commits and no active merge/conflict state is present.
- Changes-tab rebase runs against the task worktree, refreshes Changes on success/already-up-to-date/conflict, and treats already-up-to-date as informational success.
- `LLMService.ExecuteTaskWithAgent` creates the worktree before execution, runs startup sync from latest target/default branch when worktree is clean, and handles post-execution merge.
- Startup sync is a real `git merge --no-edit <target>` inside the task worktree for initial runs and follow-ups. On content conflict it detects conflicted paths, aborts, and persists `MergeStatusConflict`. Initial execution fails before model dispatch; follow-up execution may continue in preserved clean worktree with recovery context.
- Startup sync uses stored `MergeTargetBranch` when set. Without target it prefers local `main`, then `GetDefaultBranch`; `GetDefaultBranch` uses `origin/HEAD`, then local `main`, local `master`, then hardcoded `main`; `upstream/HEAD` is not consulted.
- Startup sync treats selected local branch as source of truth by default. Having `origin/<branch>` must not cause fetch/merge/rebase from remote-tracking branch unless an explicit opt-in policy exists.
- Local default branches ahead of remotes can contaminate task branches because startup sync merges local default. Recovery should preserve backup refs, compare local default, live source branch, and PR refs, reset only with evidence live branch contains intended diff, and neutralize polluted local default before future syncs.
- Repos with local `master` and no local `main` should sync from local `master` when default-branch detection resolves to it; if `origin/HEAD` points to absent `main`, worktree creation/startup merge may fail.
- Worktree setup fails closed. A repository with a local commit needs no remote, but an unborn repository lacks a tree for `git worktree add`, so execution/follow-ups must provide initial-commit guidance and never dispatch the coding model in main checkout. Channel-origin/review setup failures promote next queued follow-up instead of parking it.
- Task Changes review semantic for managed active worktrees is target/task merge base to current worktree state, not only pending changes since worktree `HEAD`.
- Full Changes, file summaries, lazy file cards, live fragments, periodic streaming snapshots, follow-up completion persistence, and worktree-specific fragments must use the same handler-level state resolution for live worktree vs preserved execution diff fallback.
- Diff selection should route through a shared handler resolver, with endpoint-equivalence coverage for active live worktrees, missing-worktree fallback, and merged-task preserved-diff/action-state equivalence. Publication state is separate and must be verified through GitHub when completion depends on it.
- Committed managed-worktree diffs use `git diff <target>...<taskBranch>`. Active managed-worktree diffs resolve `git merge-base <target> HEAD`, then diff that base against the current working tree so committed, staged, and unstaged changes collapse into one result while target-only commits are excluded. Untracked files are appended from `git ls-files --others --exclude-standard`.
- Open security bug `#30`: untracked-file diff synthesis follows Git paths with `os.Stat`/`os.ReadFile`, so an untracked symlink can expose target contents outside the repo. Diff capture must inspect without following symlinks, skip symlinks, and enforce resolved-worktree containment before reading.
- `git diff HEAD` and stored execution diffs are restricted to non-worktree execution views or fallback when no live managed worktree exists.
- Streaming snapshot, final diff capture, post-execution commit/merge/status handling, and direct-checkout execution must respect an explicit managed-worktree distinction established only after successful setup. `DisableRuntimeWorktree` direct-checkout executions must not capture/commit/merge/update stale worktree lineage.
- `GetWorktreeDiffWithUncommitted` prefers a live merge-base-relative diff against actual working tree gated on valid git worktree/ref existence. Stale/mismatched persisted branch/path metadata should recover from conventional task lineage before full-tab/lazy-file/streaming/completion diff resolution.
- Cleanup policy supports after-merge, keep, and manual.
- Periodic cleanup removes merged worktrees and detects orphaned worktrees with no task using compact `TaskRepo.ListWithWorktrees` projection. Preserve merged-branch detection, worktree removal, status update, target fallback, descendant-guarded branch deletion, and orphan cleanup.
- Chained tasks carry git lineage through `base_branch`, `base_commit_sha`, and `lineage_depth`.
- Known task-chaining gaps `#276`: child creation/activation does not consume persisted `ChildModel`; later `ChildAgentID` edits do not update already blocked child; chain handoff failures may leave pre-created child blocked without durable user-facing failure evidence.
- Task Changes supports inline comments and `Submit Review`, but submission queues feedback back to the agent, clears comments, and does not persist explicit human approval/changes-requested outcome; gap `#221`.
- Known task-detail lineage gap `#255`: ordinary chained tasks persist parent relationship and inherited git lineage, but Task Details lacks navigable parent/child context outside swarms.
- Known review-comment scoping gap `#271`: inline review comment update/delete endpoints do not verify the comment belongs to requesting task/project.
- Known one-way sync gap `#284`: Task Review UI comments are not posted back to linked GitHub PRs, while GitHub PR feedback can already be forwarded to tasks.
- Known diff-viewer Cancel Review gap `#286`: inline code-review UI's cancel function is dead/unwired and would use wrong HTTP method if invoked.

Worktree sandbox escape incident and direction:
- Confirmed 2026 incident: an assigned task with a worktree edited the main checkout because the model was oriented toward the main repo path. The assigned worktree stayed clean and edits had to be manually copied and committed to the worktree.
- Root cause: prompt/tool orientation pointed at main checkout. Setting cwd/workDir defaults alone is insufficient because absolute-path writes and shell `cd` can bypass cwd.
- Fix direction: compute a single `executionRoot` per task run derived from `tasks.worktree_path` when set, and make shell/file tool path resolution honor it for both initial execution and follow-up/chat handler execution through a shared contract.
- Outside-sandbox writes require explicit outside-workspace permission/bypass mode, not just prompt instruction, because default file tools allow absolute paths and shell can escape cwd.
- Agents with `DisableRuntimeWorktree` or explicit scope configs that intentionally write project root, such as `.openvibely` system agents, should keep explicit config paths rather than hidden exceptions. Scoped-dir roots for normal task agents should resolve against execution root.
- Default relative file/shell operations for task execution should use assigned worktree as effective repository root whenever `tasks.worktree_path` exists. Model-facing prompt should orient around that worktree path.
- Intentional absolute paths and `cd /other/project` commands are separate policy choices from default orientation. Do not add hard containment, confirmation prompts, or deterministic prompt rewriting unless explicitly requested.
- Automation-generated implementation prompts must not present the main checkout path as operative repository root when runner executes in a worktree; prefer prompt-level guidance for assigning/inbox agents before deterministic server-side rewriting.

Commit-message direction:
- Task execution auto-commits use generated descriptive commit messages driven by actual worktree diff. Generation happens while changes are still in worktree for initial execution diff capture, follow-up completion, post-execution safety capture, and merge-prep dirty-worktree commits.
- GitHub PR branch publication uses API-backed synthesized branch commits rather than local git push, but synthesized commit message is still generated from task worktree diff.
- Commit-message generation collects compact diff facts/hunks from `git status`, staged/unstaged diffs, and snippets for untracked text files, then asks LLM for one plain subject.
- Task title, prompt, and execution output are supporting context only and must be ignored when they conflict with diff. Stored execution text must not become the subject by itself.
- If no usable LLM summary exists, fall back deterministically from diff/path/status facts with plain subject-only summaries such as `Add <label>`, `Update <label>`, `Remove <label>`, `Update <area> files`, `Update <n> files`, `Update changes`, `Refine changes`, or `Prepare changes for merge`.
- Commit-summary diff context must not follow untracked symlinks or read snippets outside worktree.
- Task-execution commit subjects should be concise, subject-only, plain language, capitalized imperative mood, and strip provider/status/tool boilerplate, conventional prefixes, and body/file-list headings.
- Do not add or accept `Changed files:` bodies, task scopes, task/worktree machinery mentions, or generic `Task completed:`/`Followup:` subjects.
- Historical commits keep original subjects. Changes-tab integration commits remain static (`Merge task:`, `Squash merge task:`), and fast-forward creates no merge commit.
- Recovery incident: an Automation task branch collapsed a granular checkpoint chain; reflog/unreachable recovery restored it non-destructively while backup refs preserved recovered and collapsed states. Preserve recovery refs, verify tree equality/ancestry, integrate later target changes normally, and keep recovery separate from force rewriting shared refs.

Follow-up lineage direction:
- Task-thread follow-ups to terminal merged/stale tasks are guarded against blindly merging current target into old historical branch/worktree.
- Historical original task branches are read-only lineage when their work has been merged, conflict-aborted, or made stale by squash/duplicate acceptance.
- Follow-up execution continues from current merge target on fresh `task/<id>-followup-*` lineage when old branch is stale.
- Active follow-up worktrees remain the task's current lineage; dirty/local follow-up work is reused.
- Startup-conflict recovery for follow-ups uses typed `StartupSyncConflictError` with target branch, task branch, worktree path, and conflicted files. Reactivated running tasks can continue in preserved clean worktree with instructions to merge, resolve, build, test, and commit. Failed merge aborts, dirty worktrees, missing branches, setup failures, and non-conflict Git errors remain fatal.

Merge, metadata, and publication direction:
- Manual merge conflicts from `/tasks/:id/worktree/merge` are handled results, not ordinary request failures.
- Changes-tab rebase conflicts are handled by aborting rebase and surfacing guidance; because no rebase remains in progress, task should not stay in `MergeStatusConflict` solely from that aborted rebase.
- Fast-forward-only task merges skip unnecessary auto-rebase when ancestry already permits `--ff-only`, preventing replay of old commits and false conflicts. If target is not ancestor, existing auto-rebase applies.
- Local merges do not use blanket dirty-target guard; dirty-but-non-overlapping target checkout changes are allowed.
- Git overwrite/refusal cases without unmerged files surface as merge failures rather than conflict-resolution states.
- Changes-tab and local merge handlers revalidate stale `merge_status` and recover conventional worktree metadata before hiding/rejecting merge actions.
- A conflict-resolution commit made directly in a task worktree does not itself clear persisted `MergeStatusConflict`; task must rerun/resume in owning project or pass explicit merge-status reconciliation before merge option reappears.
- Task controls are project-scoped; an agent under another project cannot rerun/reconcile a task in a different project.
- Local worktree commits are not remote publication. Verify configured remote and task-branch tip before claiming a fix is available outside local app/worktree.
- Live GitHub PR state and head are authoritative. Successful local commit, branch replacement, or local task-record response does not prove linked PR is open or complete.
- For task PR publication, durable evidence is recorded `task_pull_requests.published_head_sha` from successful publish matched against live GitHub PR `head.sha`. Compare PR diffs against live target branch ref from GitHub, not assumed local `origin/main`.
- Automation authorization failures are explicit publication blockers and must not be bypassed.
- A task branch is already merged only when fully reachable from target.
- Direct task-detail `?tab=changes` and `/tasks/:id/changes/file` lazy requests hit same recovery/diff resolution paths as lazy tab loads.
- A local merge error saying worktree is not on expected task branch indicates branch/metadata drift. Diagnose exact assigned worktree path with status/ref evidence and identify which lineage branch owns diff.
- Multi-turn follow-ups can surface newer empty follow-up worktree while implementation commits remain on earlier follow-up branch. Missing merge options or apparently lost changes may be lineage/metadata drift; use branches, worktree registrations, and persisted metadata as evidence.
- Worktree status `exit status 128` can be transient. A genuinely missing assigned directory absent from `git worktree list` is a metadata recovery case, not permission to edit main checkout.

Cleanup and descendant direction:
- Cleanup/recovery preserves conventional task worktree metadata when original `.worktrees/task_<id>` worktree/branch still exists and contains task-side commits beyond target.
- Orphan cleanup treats `.worktrees/task_<id>` and `.worktrees/task_<id>_followup_<timestamp>` paths as in use when that task ID still exists, even if persisted worktree path is temporarily empty.
- Follow-up worktree branches use `task/<id_prefix>-followup-*`; cleanup must preserve active follow-up lineage and not make follow-up commits unreachable.
- Locked, dirty, unmerged, or task/follow-up-lineage-referenced worktrees are skipped rather than removed manually.
- Cleanup does not delete branches with non-terminal descendants or branches not conclusively merged into target.
