---
name: worktree_and_lineage
type: project
created: 2026-05-09
updated: 2026-08-23
source: consolidation
source_id: memory_consolidation_2026_08_23
confidence: high
title: Worktree and Lineage
---

Task execution uses isolated git worktrees in `.worktrees/task_<id>` with task-scoped branches `task/<id_prefix>-<slug>`. LLM task prompts include explicit worktree path orientation when a workdir is present, while runtime workdir enforcement remains the source of truth. Coding changes for assigned tasks must be made in the assigned task worktree, not the main checkout, unless the user explicitly asks for main-checkout changes.

Worktree path discipline is mandatory when a task provides a worktree path: relative tool paths resolve against the agent's working directory, not automatically against the task worktree.

Durable worktree model:
- Auto-merge and Task Changes local actions support merge commit, fast-forward only, and squash merge. Rebase is shown only when target and task branches both have unique commits and no active merge/conflict state is present.
- `LLMService.ExecuteTaskWithAgent` creates the worktree before execution, runs startup sync from the latest target/default branch when the worktree is clean, and handles post-execution merge.
- Startup sync is a real `git merge --no-edit <target>` inside the task worktree. Initial conflicts fail before model dispatch; follow-up conflicts may continue in the preserved clean worktree with recovery context.
- Startup sync uses stored `MergeTargetBranch` when set. Without target it prefers local `main`, then `GetDefaultBranch`; `GetDefaultBranch` uses `origin/HEAD`, local `main`, local `master`, then hardcoded `main`. `upstream/HEAD` is not consulted.
- Startup sync treats the selected local branch as source of truth by default. Having `origin/<branch>` must not cause fetch/merge/rebase from remote-tracking branch unless an explicit opt-in policy exists.
- Repos with local `master` and no local `main` should sync from local `master` when default-branch detection resolves to it; if `origin/HEAD` points to absent `main`, worktree creation/startup merge may fail.
- Worktree setup fails closed. A local commit needs no remote, but an unborn repository lacks a tree for `git worktree add`, so execution/follow-ups must provide initial-commit guidance and never dispatch the coding model in main checkout.
- Active managed-worktree diffs resolve target/task merge base to current working tree state, including committed, staged, unstaged, and untracked files. `git diff HEAD` and stored execution diffs are restricted to non-worktree execution views or fallback when no live managed worktree exists.
- Full Changes, file summaries, lazy file cards, live fragments, periodic streaming snapshots, follow-up completion persistence, and worktree-specific fragments must use the same state resolution for live worktree vs preserved execution diff fallback.
- Direct task-detail `?tab=changes` and `/tasks/:id/changes/file` lazy requests hit the same recovery/diff resolution paths as lazy tab loads.
- Direct task attachment/worktree changes remain project-scoped; an agent under another project cannot rerun/reconcile a task in a different project.
- Cleanup policy supports after-merge, keep, and manual. Periodic cleanup removes merged worktrees and detects orphaned worktrees with compact task projections while preserving merged-branch detection, target fallback, descendant-guarded branch deletion, active follow-up lineage, and skipped locked/dirty/unmerged worktrees.
- Chained tasks carry git lineage through `base_branch`, `base_commit_sha`, and `lineage_depth`.

Known worktree, diff, and lineage gaps:
- Open security bug `#30`: untracked-file diff synthesis follows Git paths with `os.Stat`/`os.ReadFile`, so an untracked symlink can expose target contents outside the repo. Diff capture must inspect without following symlinks, skip symlinks, and enforce resolved-worktree containment before reading.
- Known task-chaining gaps `#276`: child creation/activation does not consume persisted `ChildModel`; later `ChildAgentID` edits do not update already blocked child; chain handoff failures may leave pre-created child blocked without durable user-facing failure evidence.
- Known task-chaining UI gap `#773`: Task Detail's Chaining tab cannot name or prompt-shape follow-up child tasks even though persisted chain fields `ChildTitle` and `ChildPromptPrefix` affect child creation and are exposed through Chat/API schema.
- Task Changes supports inline comments and `Submit Review`, but submission queues feedback back to the agent, clears comments, and does not persist explicit human approval/changes-requested outcome; gap `#221`.
- Known task-detail lineage gap `#255`: ordinary chained tasks persist parent relationship and inherited git lineage, but Task Details lacks navigable parent/child context outside swarms.
- Known task-detail commit-summary gap `#723`: `task_commit_stats` stores app-produced task commit summaries, but Task Detail does not expose this per-task evidence in Details/Thread/Changes/Schedules/Chaining/Attachments/Lifecycle surfaces.
- Known review-comment scoping gap `#271`: inline review comment update/delete endpoints do not verify the comment belongs to requesting task/project.
- Known one-way sync gap `#284`: Task Review UI comments are not posted back to linked GitHub PRs, while GitHub PR feedback can already be forwarded to tasks.
- Known diff-viewer Cancel Review gap `#286`: inline code-review UI's cancel function is dead/unwired and would use wrong HTTP method if invoked.

Worktree sandbox escape incident and direction:
- Confirmed 2026 incident: an assigned task with a worktree edited the main checkout because prompt/tool orientation pointed at the main repo path. The assigned worktree stayed clean and edits had to be manually copied and committed to the worktree.
- Fix direction: compute a single `executionRoot` per task run derived from `tasks.worktree_path` when set, and make shell/file tool path resolution honor it for both initial execution and follow-up/chat handler execution through a shared contract.
- Outside-sandbox writes require explicit outside-workspace permission/bypass mode, not just prompt instruction, because default file tools allow absolute paths and shell can escape cwd.
- Agents with `DisableRuntimeWorktree` or explicit scope configs that intentionally write project root, such as `.openvibely` system agents, should keep explicit config paths rather than hidden exceptions. Scoped-dir roots for normal task agents should resolve against execution root.
- Default relative file/shell operations for task execution should use assigned worktree as effective repository root whenever `tasks.worktree_path` exists. Intentional absolute paths and `cd /other/project` commands are separate policy choices; do not add hard containment, confirmation prompts, or deterministic prompt rewriting unless explicitly requested.
- Automation-generated implementation prompts must not present the main checkout path as operative repository root when runner executes in a worktree.

Commit-message direction:
- Task execution auto-commits and GitHub API-backed PR publication commits use generated descriptive commit subjects driven by actual worktree diff.
- Commit-message generation collects compact diff facts/hunks from `git status`, staged/unstaged diffs, and snippets for untracked text files, then asks the LLM for one plain subject.
- Task title, prompt, and execution output are supporting context only and must be ignored when they conflict with the diff. Stored execution text must not become the subject by itself.
- If no usable LLM summary exists, fall back deterministically from diff/path/status facts with plain subject-only summaries such as `Add <label>`, `Update <label>`, `Remove <label>`, `Update <area> files`, `Update <n> files`, `Update changes`, `Refine changes`, or `Prepare changes for merge`.
- Commit-summary diff context must not follow untracked symlinks or read snippets outside worktree.
- Task-execution commit subjects should be concise, subject-only, plain language, capitalized imperative mood, and strip provider/status/tool boilerplate, conventional prefixes, and body/file-list headings. Do not add `Changed files:` bodies, task scopes, task/worktree machinery mentions, or generic `Task completed:`/`Followup:` subjects.

Follow-up lineage direction:
- Task-thread follow-ups to terminal merged/stale tasks are guarded against blindly merging current target into old historical branch/worktree.
- Historical original task branches are read-only lineage when their work has been merged, conflict-aborted, or made stale by squash/duplicate acceptance.
- Follow-up execution continues from current merge target on fresh `task/<id>-followup-*` lineage when old branch is stale. Active follow-up worktrees remain the task's current lineage; dirty/local follow-up work is reused.
- Startup-conflict recovery for follow-ups uses typed `StartupSyncConflictError` with target branch, task branch, worktree path, and conflicted files. Reactivated running tasks can continue in preserved clean worktree with instructions to merge, resolve, build, test, and commit.
- Failed merge aborts, dirty worktrees, missing branches, setup failures, and non-conflict Git errors remain fatal.

Merge, metadata, and publication direction:
- Manual merge conflicts from `/tasks/:id/worktree/merge` are handled results, not ordinary request failures. Changes-tab rebase conflicts abort rebase and surface guidance; an aborted rebase alone should not leave `MergeStatusConflict`.
- Fast-forward-only task merges skip unnecessary auto-rebase when ancestry already permits `--ff-only`; if target is not ancestor, existing auto-rebase applies.
- Local merges do not use blanket dirty-target guard; dirty-but-non-overlapping target checkout changes are allowed. Git overwrite/refusal cases without unmerged files surface as merge failures rather than conflict-resolution states.
- Changes-tab and local merge handlers revalidate stale `merge_status` and recover conventional worktree metadata before hiding/rejecting merge actions. A conflict-resolution commit made directly in a task worktree does not itself clear persisted `MergeStatusConflict`.
- A task branch is already merged only when fully reachable from target.
- Local worktree commits are not remote publication. Verify configured remote and task-branch tip before claiming a fix is available outside local app/worktree.
- Live GitHub PR state and head are authoritative. Successful local commit, branch replacement, or local task-record response does not prove a linked PR is open, current, scoped, or complete.
- For task PR publication, durable evidence is recorded `task_pull_requests.published_head_sha` from successful publish matched against live GitHub PR `head.sha`. Compare PR diffs against the live target branch ref from GitHub, not assumed local `origin/main`.
- Repeated 2026 PR handoff incidents showed stale/polluted remote PR heads despite clean local task candidates. Durable response is to verify local candidate scope, live source/PR heads, live PR file list, target branch, validation evidence, and issue closure before claiming publication is current.
- Automation authorization failures are explicit publication blockers and must not be bypassed by ordinary agents. Manual publication repair is exceptional: preserve stale remote heads in clearly named backup refs, verify issue-scoped diffs and live refs, use guarded `--force-with-lease`, then reconcile/reopen the PR and run a fresh audit.

Diagnostics:
- A local merge error saying the worktree is not on the expected task branch indicates branch/metadata drift. Diagnose the assigned worktree path with status/ref evidence and identify which lineage branch owns the diff.
- Multi-turn follow-ups can surface a newer empty follow-up worktree while implementation commits remain on an earlier follow-up branch. Missing merge options or apparently lost changes may be lineage/metadata drift; use branches, worktree registrations, and persisted metadata as evidence.
- If the browser does not show expected merge actions while server-rendered Task Changes HTML does, suspect stale HTMX/page state or dropdown visibility before lineage/ancestry.
- Worktree status `exit status 128` can be transient. A genuinely missing assigned directory absent from `git worktree list` is a metadata recovery case, not permission to edit main checkout.
