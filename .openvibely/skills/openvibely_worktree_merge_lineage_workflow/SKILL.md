---
kind: openvibely.agent_skill
version: 1
skill:
    key: openvibely_worktree_merge_lineage_workflow
    name: OpenVibely Worktree Merge And Lineage Workflow
    scope: project
    description: Implement and audit OpenVibely task worktrees, merge actions, Changes tab recovery, cleanup, and chained-task lineage.
---

# OpenVibely Worktree Merge And Lineage Workflow

Use this skill when changing task worktree creation, startup sync, local merge/squash/fast-forward behavior, Changes tab merge controls, stale worktree metadata recovery, cleanup, or chained-task lineage.

## How To Invoke

Example prompts:

- `Fix stale merge_status hiding Task Changes merge actions`
- `Audit worktree cleanup and lineage behavior`
- `Implement dirty-target-safe local merge handling`
- `Debug a task follow-up that appears to undo merged work`
- `Fix Changes tab direct ?tab=changes recovery`
- `where are the changes / no merge option here` (user sees no diff or no merge action even though implementation work was done)

For manual rebase-only work, also use `openvibely_git_worktree_rebase_workflow`.

## MANDATORY: Worktree Path Discipline

**This section must be followed before touching any file when a worktree path is given in the system prompt.**

Tool calls (`read_file`, `edit_file`, `write_file`, `bash`) resolve **relative paths against the agent's working directory, which is always the main repo root — never the worktree.** This means silently editing the wrong tree is possible and has happened. Prevent it:

1. **First action on every worktree task:** run `pwd` and `git branch --show-current` inside the worktree to confirm location before reading or writing anything:
   ```bash
   cd /path/to/worktree && pwd && git branch --show-current && git status --short
   ```
2. **All `bash` commands must be prefixed** with `cd <absolute-worktree-path> &&` for the duration of the task, or the shell must be explicitly changed to the worktree directory first and confirmed.
3. **All `read_file`, `edit_file`, `write_file` calls must use absolute paths** rooted at the worktree, not relative paths from the repo root.
4. **Before committing**, run `git -C <worktree-path> status` and `git -C <worktree-path> diff --stat HEAD` to verify changes are in the worktree branch, not main.
5. If accidental edits land in the main checkout, stop immediately: inspect both trees, move changes to the worktree, restore main before continuing.
6. After recovering wrong-checkout edits or before claiming a task branch is ready to merge, verify the main checkout is clean of task-owned paths with path-limited status/diff checks such as `git -C <main-repo> status --short -- <paths>` and `git -C <main-repo> diff --stat HEAD -- <paths>`. Only restore/delete paths after confirming the same intended changes are committed or preserved in the task worktree; leave unrelated user/system dirty files untouched and explicitly report them as unrelated.
7. **Do not trust a worktree path reported by a prior turn/message.** For tasks with follow-up lineage, the platform can assign a *new* worktree checkout path (a different `_followup_<epoch>` suffix) on each turn, even when the underlying task-branch state carries over. Every turn — especially every audit turn — must re-run `pwd` first and treat that as the authoritative current path; do not assume the path named in the previous turn's report, the task prompt's stale context, or your own prior-turn evidence still matches. It is possible for a brand-new worktree path to already be checked out on the correct implementation branch with correct ancestry (because branches are shared git refs, not tied to one worktree mount), so a changed path alone is not proof of drift — verify branch/ancestry independently of the path.

## Core Model

- Task execution uses isolated git worktrees under `.worktrees/task_<id>` with task-scoped branches such as `task/<id_prefix>-<slug>`.
- The worktree absolute path is provided in the system prompt; treat it as the working root for all file operations.
- LLM task prompts should include explicit worktree path orientation when a workdir is present, but runtime workdir enforcement remains authoritative.
- `LLMService.ExecuteTaskWithAgent` creates the worktree before execution, runs startup sync when the worktree is clean, and handles post-execution merge.
- Startup sync should use the task's `MergeTargetBranch` when set, falling back to the default branch only when no target is stored.
- Startup sync treats the selected local branch as the source of truth by default. Do not fetch, merge, or rebase `origin/<branch>` merely because an `origin` remote or remote-tracking ref exists; remote refs may help discover a branch name, but content sync requires a deliberate opt-in policy.
- When local `main` is absent, do not let `origin/main` alone force startup sync to choose or merge `main`. Prefer an existing local `main` when present, otherwise the detected local default branch, and keep the merge source a local ref.
- Default-branch discovery currently checks `refs/remotes/origin/HEAD` for the branch name, then local `main`, then local `master`, then hardcoded `main`; it does not inspect `upstream/HEAD`. When changing startup sync or answering branch-selection questions, verify this exact fallback order and add tests before changing remote-HEAD behavior.

## Task Execution Commit Messages

- Task execution auto-commits are created through `WorktreeService.CommitWorktreeChanges`, but the descriptive message should be generated before that call from the current worktree diff and task/execution context rather than by changing merge mechanics.
- Initial execution message context flows from `LLMService` after the agent run; follow-up message context flows through `handler.completeWithSuccess` using the execution row, including `Execution.PromptSent`, `IsFollowup`, and output/summary text when available.
- Generate messages from the unstaged worktree state before staging/committing. Use `git status` plus diff summaries such as `diff --numstat`/file paths to determine touched files and deterministic fallback subjects.
- If the requirement says messages must be based on the actual diff or LLM-generated from what changed, the subject must be driven by compact diff content, not merely by task title/prompt/output and not merely by file paths/statuses. Pass bounded diff facts such as `git diff --stat`, `git diff --numstat`, and selected/capped hunks to the summarizer/model; instruct it to describe what changed rather than which files were edited. Treat task title, turn intent, and prior LLM output only as supporting context, and never let unrelated context override the diff. Do not claim LLM-generated diff summaries unless the implementation actually sends compact diff content to a model at commit-message time. Add tests that catch a misleading context summary winning over an unrelated diff and tests that fail for path-only messages like `Update worktree service` when hunk content proves a more specific change.
- When adding a commit-message model call from a task worktree, audit usage attribution as part of the feature: `.worktrees/task_<id>` paths often do not equal `projects.repo_path`, so direct-call usage may be recorded with an empty or wrong `project_id` and disappear from the project-filtered Analytics page. Resolve the owning project explicitly or teach workdir-to-project mapping about task worktrees, and add a regression that the summary call's `llm_usage_events.project_id` is the task project.
- Deterministic fallback subjects are backup-only for missing, failed, or junk LLM summaries and should still come from the actual diff/status, never from stale task text. Keep them plain and subject-only: single-file changes use the status verb plus a cleaned label such as `Add analytics template`, `Update README.md`, or `Remove app`; related multi-file changes use concise area summaries such as `Update internal service files` or `Update worktree service tests`; unrelated multi-file changes use `Update <n> files`; no usable diff label falls back to `Update changes`, `Refine changes`, or `Prepare changes for merge` depending on the execution context.
- Commit-subject casing should follow the requested/project standard across both LLM-derived subjects and deterministic fallbacks. Tim Pope's "A Note About Git Commit Messages" convention expects a capitalized imperative subject such as `Fix bug`, not `fix bug`; when that link or convention is in scope, do not force the first character lowercase. If the generator currently lowercases LLM output or emits lowercase fallback verbs, update cleanup, prompt wording, fallback verbs, and tests together so capitalization is consistent.
- For commit-message collectors that summarize untracked files, prefer `git status --porcelain --untracked-files=all` so nested untracked files are listed at file level instead of only as their parent directory; keep generic status helpers unchanged unless they specifically need this behavior.
- Never let untracked-file snippet collectors follow symlinks or read outside the worktree when building prompt/model input. Use `os.Lstat` or equivalent before reading, skip symlinks and non-regular files, keep path traversal protections, and verify any fully resolved candidate path remains inside the resolved worktree before `ReadFile` so symlinked parent directories cannot escape either. Add regressions with an untracked symlink pointing outside the worktree and, when practical, a symlinked-parent path to prove commit-summary prompts cannot leak external file contents.
- When deriving subjects from LLM output or tool results, filter runtime/status markers before accepting a line as the summary source. Skip bracketed markers such as `[STATUS: SUCCESS]`, `[STATUS: FAILED | ...]`, `[Thinking]`, `[Using tool: ...]`, and other lifecycle/tool transcript lines so generated commit subjects describe code changes rather than agent protocol noise.
- Keep task execution commit messages concise and change-focused. Emit a plain subject-only description, not a conventional-commit type/scope prefix and not a body. Strip LLM-provided prefixes such as `chore:`, `docs:`, or `fix(worktree):` before using a summary. Skip LLM-provided body/file-list headings such as `Changed files:`, `Files changed:`, or `Modified files:` before selecting an acceptable summary line. Do not add a `Changed files:` body because the diff already shows files, and do not invent a `task` scope or say `task worktree` in user-facing commit messages unless the changed code's real feature is task functionality.
- Do not include task lifecycle or turn labels in user-facing commit subjects or bodies. Avoid `Task Complete`, `Task completed:`, `Follow-up`, `Followup:`, `Execution phase:`, `Task turn: first task turn`, `Task turn: later task turn`, and similar process-focused wording; distinguish later executions through the diff-derived description and execution context used to choose the subject, not by printing lifecycle metadata in the commit message.
- Manual PR/conflict-resolution commit paths are separate from task execution finalization, but they are still user-visible when they create task-branch prep commits. If a task targets commit-message quality broadly or the user calls out Create PR/PR prep, route dirty-worktree PR-prep commits through the same diff-summary message builder and deterministic fallback instead of static labels like `Task updates: <title>`, and add a handler regression such as `TestCreateTaskPullRequest_CommitsDirtyWorktreeWithDiffSummaryMessage`.
- When explaining or auditing Changes tab commit-message behavior, distinguish the merge options: Merge commit creates a target-branch integration commit with static `Merge task: <title>`, fast-forward creates no new commit and reuses the task branch commits, squash creates a target-branch squash commit with static `Squash merge task: <title>`, and Create PR may create a dirty-worktree task-branch prep commit that should use the diff-summary/fallback message path when this feature is in scope.
- When auditing this feature, do not treat the already-created task branch commit subject as proof of failure if that commit was produced before the new generator ran. Verify future behavior through call sites, tests, or a later auto-commit, and call out the limitation when relevant.
- Cover message generation with focused service tests for diff-derived subjects, later-execution context, conventional-prefix stripping, marker filtering, body/file-list heading filtering, capitalized fallback/LLM cleanup, and empty/no-summary fallback behavior.

## Follow-Up Lineage

- Task-thread follow-ups to terminal merged/stale tasks should not blindly merge the current target into an old historical task branch/worktree.
- Historical original task branches are read-only lineage when already merged, conflict-aborted, or stale after squash/duplicate acceptance. Follow-up execution should continue from the current merge target on fresh `task/<id>-followup-*` lineage and skip startup sync for that first current-target setup.
- Follow-up worktree paths use `.worktrees/task_<fullID>_followup_<timestamp>`; any task ID extraction or cleanup logic must map that path back to the original full task ID, not `<id>_followup_<timestamp>`.
- Preserve active follow-up worktrees as current lineage. Dirty/local follow-up work must be reused. Clean read-only follow-up branches may be reused when they have no commits beyond the target.
- If a user asks to commit missing work from a task-thread follow-up and the active follow-up worktree is clean, inspect the original task worktree/branch before concluding there is no diff. When the intended uncommitted diff exists only on stale original lineage, recreate or port that diff into the active follow-up lineage and commit there; do not commit the stale original branch unless it is still the current task worktree, and do not reset the stale worktree without explicit user approval.
- If startup auto-merge is aborted in a clean preserved worktree that still has real unmerged task commits, keep conflict metadata and allow the follow-up run to start there with explicit context that the worktree may be behind/diverged.
- **Multiple sibling follow-up branches can accumulate across turns.** A long-running task with many continuation turns can end up with several `task/<id_prefix>-followup-<epoch>` branches (one per follow-up worktree setup), not just one. It is possible for the currently checked-out follow-up branch/worktree to be empty (identical to the merge target, `main...HEAD = 0 0`) while an older sibling follow-up branch still holds the real implementation commits and is itself fast-forward eligible. This is why a user can report "no merge option" even after a prior turn confirmed a clean fast-forward state: a *later* turn's fresh follow-up setup created a new, empty branch on top of `main` without carrying forward the older branch's commits.
- **The assigned worktree PATH itself (not just the branch) can also change between turns for the same task**, even without any sibling-branch confusion: successive turns can be assigned different `.worktrees/task_<id>_followup_<epoch>` mount paths while ultimately resolving to the same shared local branch refs. A worktree path differing from what a prior turn reported is not by itself evidence of drift or of a platform bug — always re-verify branch/ancestry from the current `pwd` independently before concluding anything drifted. Conversely, do not assume a new path is automatically broken/empty; check `main...HEAD` on the actual current path before acting.
- **If this "empty new follow-up branch orphans the real implementation" cycle repeats across three or more consecutive turns** even though each turn correctly re-verified `pwd`/branch/ancestry and correctly switched to the branch holding the real commits, treat repeated branch-switching as a reactive fix that the platform can keep undoing on the next turn's fresh follow-up setup — not a stable resolution. In that situation, prefer making the diff durable on whichever branch the platform is currently assigning by default, instead of only switching back to the older implementation branch again:
  - Create a backup ref of the branch holding the real commits first.
  - Cherry-pick (or `git diff <target>...<implementation-branch> | git apply`) the implementation commits onto the currently checked-out/assigned branch so that branch itself carries the diff, rather than relying on the UI/task metadata to keep pointing at the older sibling branch.
  - Rerun focused validation after the cherry-pick/apply.
  - Explicitly tell the user this cycle has repeated multiple times and that you consolidated the implementation onto the currently active branch as a more durable fix than repeated switching; suggest they confirm the Changes tab now shows the diff and consider it a possible platform/task-metadata issue worth reporting if a brand-new empty follow-up branch appears again afterward.

## Merge Handling

- Manual merge conflicts from `/tasks/:id/worktree/merge` are handled results, not ordinary request failures. The merge service should set conflict status, return conflict file details, refresh the panel, and emit an HTMX toast.
- Local merges should not use a blanket dirty-target guard. Allow dirty-but-non-overlapping target checkout changes; block only for active conflict/merge state or when Git reports local changes would be overwritten/conflict.
- Git overwrite/refusal cases without unmerged files should be surfaced as merge failures, not conflict-resolution states. Leave user files untouched, set failed status, and return Git's message.
- Merge/squash cleanup needs pre-merge status snapshots and ownership-aware handling so OpenVibely does not reset, unstage, delete, or commit pre-existing user work.
- Sequential local fast-forward merges for existing task worktrees should stay in the task worktree for validation/rebase, verify the expected branch, require a clean task worktree, rebase onto the current target, and use the post-rebase task worktree `HEAD`.
- After a local fast-forward rebase, update the target branch conditionally. If the target branch is checked out in a worktree, merge `refs/heads/<task-branch>` inside that target worktree. If not checked out, use protected `git update-ref refs/heads/<target> <rebased-task-HEAD> <old-target>`.
- Avoid `reset --hard` as default cleanup because it can destroy real user work.
- Squash merge failure handling must clean or abort squash state and leave the target checkout clean without hard-resetting the main/default target branch.

## Changes Tab And Metadata Recovery

- Changes tab should show changes introduced by the worktree branch since its merge base with the target branch, falling back to execution diff. Use `git diff <targetBranch>...<taskBranch>` for committed branch state so newer target-only commits do not appear as reverse changes.
- Active/running worktree Changes should resolve `git merge-base <targetBranch> HEAD` inside the worktree and compute one tracked-file diff from that base to the current working tree. This preserves committed, staged, and unstaged task changes while excluding target-only commits; append synthetic diffs only for genuinely untracked files. Do not concatenate the committed branch diff with `git diff HEAD`; a follow-up that re-edits a file already committed by the previous turn will otherwise render duplicate `diff --git` blocks for the same path while the follow-up is still running.
- Treat an empty but successful merge-base-to-worktree tracked diff as a valid empty diff, not as a Git failure or a reason to fall back to stale committed branch diff. This matters when an in-progress follow-up reverts a tracked file back to the selected merge base; stale committed file blocks must not reappear, though untracked-file synthetic diffs should still be appended.
- Show `Rebase onto <target>` only when `git rev-list --left-right --count <target>...<taskBranch>` reports at least one unique commit on both sides. A branch that is only behind the target has no task commits to replay, so it should not get the rebase action; Git/ref/count failures should also hide the action.
- Do not hide or reject merge actions solely because `tasks.merge_status=merged` or worktree metadata is blank. First revalidate against Git and recover conventional `.worktrees/task_<id>` / expected `task/<id_prefix>-<slug>` metadata when present.
- Clear stale merged metadata whenever Git shows the task branch still has commits beyond the target, including diverged branches where the target also has newer commits.
- Only treat a task as already merged when the task branch is fully reachable from the target.
- Apply stale metadata recovery consistently across `/tasks/:id/changes`, merge POST, older `/tasks/:id/changes/worktree`, worktree info panel, and direct task-detail renders with `?tab=changes`.
- Direct `?tab=changes` render should lazy-load `/tasks/:id/changes` on page load instead of inlining stale `TaskChangesContent(...)`.
- Direct `/tasks/:id/changes/file` lazy-file requests currently need care because normal UI flow runs `/tasks/:id/changes` first and persists recovery before lazy file loads.
- Worktree merge HTMX flows should close the dropdown, disable/show busy state for the clicked action, and refresh the initiating Changes surface or show a toast.

## Diagnostics

- When a follow-up appears to undo already-merged work, verify the real target/default branch and task branch ancestry instead of trusting a linked worktree's local `main` ref.
- When a user asks whether task changes are already in `main`, verify committed and uncommitted worktree state against both the local target and remote-tracking target before answering. Do not rely on `git branch --contains HEAD` or branch reachability alone, because the task branch `HEAD` can be reachable from `main` while the actual implementation remains as uncommitted modifications in the isolated worktree. Use path-scoped `git status --short --branch`, `git diff --name-status main -- <paths>`, and when relevant `git diff --name-status origin/main -- <paths>`; report local-main, origin-main, uncommitted, and publication/authorization evidence separately.
- When a user worries a commit merged into `main` is missing from a task worktree, use explicit reachability/range checks:

```bash
git merge-base --is-ancestor main HEAD
git merge-base --is-ancestor HEAD main
git log --oneline HEAD..main
git log --oneline main..HEAD
```

- If accidental edits land in the main checkout while a task has an assigned worktree, inspect both statuses/diffs, move or recreate the relevant changes in the task worktree, then restore main only after confirming nothing will be lost.
- For conflict triage, check actual merge direction. `main -> task` may report already up to date while `task -> main` or cherry-picking a follow-up commit can still conflict.
- Because `internal/database/migrations/068_baseline.sql` seeds `worktree_merge_target` as `main`, tests that create temporary Git repos for worktree setup/cleanup paths should ensure those repos have a `main` branch.
- **"Where are the changes" / "no merge option" triage.** Do not assume the currently checked-out branch in the assigned worktree is the one holding the implementation. First check whether it is empty relative to the target:
  ```bash
  git status --short --branch
  git rev-list --left-right --count main...HEAD
  ```
  If `main...HEAD` is `0 0`, the current branch has no diff to merge — that alone explains a missing merge option, and there is no bug to fix in the merge UI. Next, enumerate every task-lineage branch (original plus every follow-up) and diff each against the target to find which one actually carries the implementation:
  ```bash
  git branch --list 'task/<id_prefix>*' -vv
  git for-each-ref --format='%(refname:short) %(objectname:short)' 'refs/heads/task/<id_prefix>*'
  git diff --stat main...task/<id_prefix>-followup-<epoch>
  git rev-list --left-right --count main...task/<id_prefix>-followup-<epoch>
  ```
  Switch the assigned worktree to whichever branch has both a non-zero diff and clean fast-forward ancestry (`main` is an ancestor), then tell the user to refresh the Changes/Merge view. If more than one candidate branch has real commits, treat that as a signal the task metadata/UI may be pointed at the wrong branch and say so explicitly, rather than guessing which one is authoritative.
- Branch-switching alone to diagnose/fix a "no merge option" report is a workspace-modifying action (it changes checked-out files), even without editing source. Do not follow it in the same turn with a full audit-only review claim; if the active task goal requires a strict modifying/audit split, treat a branch switch as needing its own dedicated follow-up audit turn.
- If a long-running task keeps cycling fix → audit-finds-drift → fix → audit-finds-drift across many turns even though each modifying turn correctly re-verifies `pwd`/branch/ancestry before acting and each audit turn correctly re-verifies from scratch, that per-turn discipline is usually still sufficient to eventually reach a stable, mergeable state — it does not by itself prove a platform bug. Keep applying strict re-verification each turn rather than assuming a fix is durable; only escalate as a suspected platform/runtime issue if the same worktree path (not just branch) repeatedly reverts to stale content after being correctly fixed within the same turn boundary.
- If a user explicitly asks "why does this keep happening" after several "no merge option" cycles, answer in terms of task/worktree lineage rather than the underlying code fix: name the specific empty branch(es) the UI landed on, name the branch that actually holds the implementation commits, and state plainly that each new follow-up setup can create a fresh branch from the target without carrying forward older follow-up commits. Avoid implying the Skills/Agents/Channels code fix itself is unstable when the recurring cause is lineage/branch assignment, not the implementation.

## Cleanup And Chained Tasks

- Cleanup policy supports after-merge, keep, and manual. Periodic cleanup removes merged worktrees and detects orphaned worktrees with no corresponding task.
- Cleanup/recovery must not blank conventional task worktree metadata when an original `.worktrees/task_<id>` worktree/branch still exists and contains task-side commits beyond the target.
- Treat `.worktrees/task_<id>` paths as in-use when that task ID still exists, even if `worktree_path` metadata is temporarily empty.
- Treat `.worktrees/task_<id>_followup_<timestamp>` paths and `task/<id_prefix>-followup-*` branches as in-use lineage for the original task when that task ID still exists, even if current task worktree metadata is stale or blank.
- Orphan cleanup must preflight candidates before filesystem removal: skip locked worktrees, dirty worktrees, worktrees whose HEAD is not merged into the merge target, branches referenced by any existing task metadata, follow-up branches for existing tasks, and branches with valid/non-terminal descendant lineage.
- Delete a branch only after it is conclusively safe: the associated worktree was safe to remove, the branch is merged/reachable from the target, and it is not the only reference to valid task or follow-up commits. On ambiguity, preserve the worktree and branch.
- Skip locked worktrees rather than removing them manually.
- Chained tasks carry lineage through `base_branch`, `base_commit_sha`, and `lineage_depth`. Child tasks should inherit parent changes from the parent worktree branch HEAD or merge target/default branch HEAD as appropriate.
- Cleanup should not delete branches with non-terminal descendants.

## Testing

- Add regressions for startup sync proving local branch authority: divergent or ahead `origin/main` must not be fetched/merged by default, mere `origin/main` existence must not change the merge source, broken `origin` should not matter, and upstream-only remotes should still use the selected local branch.
- Add regressions for stale `merge_status=merged` with blank/recovered metadata, target moved, task branch still has commits, and Changes tab showing local actions after status resets.
- Cover active follow-up Changes diff regressions where a previous execution committed a file and the running follow-up edits the same file; assert service diff generation and `/tasks/:id/changes` render exactly one diff file/card with the latest content. Also cover target-only commits being excluded, the revert-to-merge-base edge case, and rebase availability requiring unique commits on both sides.
- Cover direct `?tab=changes`, lazy `/tasks/:id/changes`, merge POST, worktree panel, and legacy fragments when stale metadata recovery changes.
- Cover dirty-but-non-overlapping target changes, Git overwrite refusals, true conflicts, squash failure cleanup, checked-out target fast-forward merge, and ref-only target updates.
- Cover follow-up lineage for terminal merged/stale tasks, dirty follow-up reuse, clean follow-up staleness, startup sync conflict fallback, cleanup preserving conventional worktrees, and chained-task descendants.
- For orphan cleanup changes, add focused service regressions for base path extraction, `.worktrees/task_<id>_followup_<timestamp>` extraction, actual `SetupFollowupWorktree` naming, stale metadata with an existing task preserving the follow-up worktree and branch, dirty candidate preservation, unmerged candidate preservation, and reachability of follow-up commits after cleanup.
- If this pattern is proven to happen repeatedly for a given task (a new empty follow-up branch created each turn while an older sibling holds the real commits), it is worth adding a regression that follow-up worktree setup logic branches from the correct existing lineage HEAD (the latest branch/worktree with real task commits) rather than always branching fresh from the current merge target.
