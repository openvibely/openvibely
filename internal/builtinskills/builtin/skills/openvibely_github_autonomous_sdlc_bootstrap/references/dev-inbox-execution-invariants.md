# Dev Inbox Execution Invariants

Load this checklist at the start of every GitHub Dev Inbox execution turn, before inspecting issues. Apply it even when an older stored task prompt omits these rules.

## Preconditions

- Verify the requested repository and GitHub mailbox configuration.
- Verify PR-feedback forwarding, issue inbox, task discovery/creation, task-goal, and issue update tools before making side effects.
- Base every reported result on current runtime output. Do not substitute local Git, GitHub CLI, labels, memory, or earlier summaries for unavailable runtime evidence.
- Keep Dev Inbox orchestration-only. Builds, tests, code changes, and PR publication belong in implementation tasks.
- When a required action is not exposed, report the exact capability blocker. Do not claim that a task was continued, a PR was verified, or integration completed without a tool result that proves it.

## Per-Issue Reconciliation

Give every inspected assigned issue exactly one disposition:

- `started`: this run created one implementation task, set its goal, and completed the required issue updates.
- `continued`: this run successfully invoked an exposed action that resumed existing work.
- `already-active`: current task or open-PR evidence shows suitable implementation work is running, queued, or under review and no new action is needed.
- `already-implemented`: current authoritative GitHub or default-branch evidence proves the requested work is integrated.
- `skipped`: the issue is excluded for a concrete reason such as closed, duplicate, unexpectedly assigned, or blocked without actionable acceptance criteria.

Raw task states are observations, not automatically dispositions. A label, persisted goal, local branch, remembered run, or old comment alone does not prove active or integrated implementation.

Before creating work, call `list_tasks` with the issue number and distinctive title substrings across every relevant category exposed by the tool, normally `active`, `backlog`, `scheduled`, and `completed`. `list_tasks` searches task titles only, so do not query an issue URL unless it was deliberately embedded in the title; a URL present only in the task prompt is not discoverable there. The compact task summary does not itself provide PR metadata. Do not claim linked-PR or integration evidence from `list_tasks` unless another current tool result supplies it.

Also retrieve current authoritative issue-to-PR evidence for every candidate before any task creation, goal, comment, or label mutation. It is valid to use `github_list_assigned_issues_with_prs` or another available GitHub PR lookup as this second reconciliation pass after the normal assigned-issue mailbox has been fetched. Do not use PR association as the initial eligibility gate: an assigned issue with no PR may still be actionable. If an assigned issue already has an open associated PR, classify it as `already-active` and reconcile or continue the linked implementation task rather than creating a new workstream. Do not infer absence of a PR from issue details, labels, or compact task listings that omit PR metadata.

Complete both task and PR reconciliation for all assigned issues before the first mutation. Do not create tasks incrementally while authoritative PR evidence for other candidates or the current candidate is still pending. If the runtime cannot retrieve required PR evidence, report that limitation rather than treating missing evidence as proof that no PR exists.

Continue a suitable matching task only when the current runtime exposes an action that actually resumes it. If no continuation action is exposed, do not create a duplicate merely to simulate continuation. Report the blocker unless the existing task is demonstrably active, or create one clearly identified replacement only when the old task is failed, cancelled, completed without integration, or otherwise unsuitable and the issue remains actionable.

When creating a task, use only fields exposed by `create_task`. Pass `source_github_issue_number` and `source_github_repo_url` so the task has authoritative issue linkage. Put the issue number, URL, title, repository URL, acceptance criteria, expected validation, and `github_open_pull_request` instructions in the task title or prompt. Call `set_task_goal` after creation. Do not claim `started` unless both calls succeed.

## Side Effects

- For `started`, comment and add `task-created` or `in-progress` only after task creation and goal setup succeed.
- For `continued`, comment or change labels only after a real resume action succeeds.
- For `already-active`, `already-implemented`, and `skipped`, avoid routine comments or label churn.
- Add or report `pr-opened` only from current authoritative PR evidence.
- Never claim a comment, label, task, goal, or PR transition without a successful tool result for that exact side effect.

## Completion Check

1. List every inspected issue with exactly one disposition and the current evidence supporting it.
2. Reconcile the actual counts with:

```text
assigned inspected = started + continued + already-active + already-implemented + skipped
```

3. Ensure category headings, issue entries, and totals agree.
4. If task creation, goal setup, continuation, comments, or labels failed, report the exact failure and do not claim the transition succeeded.
5. If assigned-issue retrieval failed, omit the disposition equation and state that no issue-level conclusion was possible.
6. If the stored task prompt has drifted and no task-editing capability is exposed, report the prompt drift for human correction rather than claiming it was updated.
