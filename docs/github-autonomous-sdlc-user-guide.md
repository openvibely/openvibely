# GitHub Autonomous SDLC User Guide

Use GitHub issues and pull requests as the visible mailbox for recurring OpenVibely work. The loop is prompt-driven: OpenVibely scheduled tasks run normal task prompts that call generic GitHub tools. There is no hidden GitHub poller daemon.

## What This Loop Is For

The GitHub autonomous SDLC loop helps a project turn suggestions, approvals, implementation tasks, and pull requests into a reviewable GitHub-centered workflow.

Why this matters:

- GitHub issues stay the durable inbox for suggestions, approved work, blockers, and status.
- GitHub PRs stay the review and merge boundary.
- OpenVibely tasks, schedules, threads, and worktrees remain visible and inspectable; persisted goals are for implementation tasks or explicit goal-driven work, not the default recurrence mechanism for scheduled loops.
- Humans keep control of approval, prioritization, review, merge, credentials, and release decisions.

## Prerequisites

Before creating the loop:

1. Create or select the OpenVibely project for the repository. Configure its GitHub repository URL, or ensure the project's local checkout has a GitHub remote; the explicit project URL takes precedence when both exist.
2. Configure GitHub in `/channels` using a PAT or GitHub App.
3. Add every GitHub user or bot account OpenVibely should scan or trust under `Authorized Users` in GitHub Runtime Settings.
4. Assign GitHub issues to the PAT owner or any configured Authorized User when you want OpenVibely scheduled tasks to notice them.
5. For GitHub App setups, do not assign issues to an organization installation account; assign them to a configured real user or bot.
6. Ensure the scheduled task's model/provider supports runtime tool calls.

A PAT identifies a real GitHub user, so scheduled tasks call `github_list_my_assigned_issues` to find issues assigned to that user. They also scan every configured Authorized User through `github_get_project_inbox` and `github_list_assigned_issues`, regardless of authentication mode. A GitHub App installation may be installed on an organization, which is not an issue assignee, so App setups rely on configured real users or bots.

For this Automation loop, do not pass `repo_url` overrides. Automation-bound GitHub tools use only the selected project's configured GitHub repository URL and fall back to a GitHub remote in that project's local checkout when the URL is blank. Pull request tools use that same project boundary because they publish task worktree branches through the configured GitHub token/API and persist task PR records.

## Bootstrap Skill

OpenVibely bundles `openvibely_github_autonomous_sdlc_bootstrap` as a reusable global skill. In any project with GitHub configured, create or run a visible bootstrap task/task-thread turn so lifecycle routing can select the skill, then ask:

```text
Use the OpenVibely GitHub Autonomous SDLC Bootstrap skill to set up the GitHub SDLC loop for this project. Create the visible scheduled loop tasks and schedules needed. Do not set persisted goals on recurring loop tasks; schedules drive the loop. Use the current project GitHub channel configuration. Report anything missing.
```

The skill creates or updates normal visible tasks and schedules; it does not start a hidden daemon. Setup should create one visible task per loop role and schedule that same task, not create separate standalone runner tasks plus scheduled duplicates. The first setup action should create/run `GitHub Offering Manager: Vision Suggestions` immediately so it can open initial suggestion issues, then attach its daily schedule and create the Dev Inbox, Bug Finder, Optimization Finder, and Redundancy Finder schedules afterward.

## Maintained Visible Loop

Create all five scheduled roles as part of the maintained setup.

| Task | Cadence | Purpose |
|---|---:|---|
| `GitHub Offering Manager: Vision Suggestions` | Daily | Reads project vision/source files and opens suggestion issues only. |
| `GitHub Dev Inbox` | Hourly | Forwards authorized PR comments/reviews to linked implementation tasks, then checks open issues assigned to the PAT user or configured GitHub Authorized Users and links/updates eligible work. |
| `GitHub Bug Finder` | Daily | Inspects one focused area for likely defects and opens bug issues only. |
| `GitHub Optimization Finder` | Daily | Inspects one focused area for measurable performance or efficiency improvements and opens performance issues only. |
| `GitHub Redundancy Finder` | Daily | Inspects one focused area for duplicated or redundant code and opens duplication issues only. |

These finder tasks open GitHub issues only; Dev Inbox remains the path that turns assigned issues into implementation tasks.

Initial setup order:

1. Create `GitHub Offering Manager: Vision Suggestions` first and run that same task immediately. If no explicit run-existing-task action is available, create it as `active` for the first run, then attach the daily schedule to that same task after the creation/start action is accepted.
2. Create `GitHub Dev Inbox`, `GitHub Bug Finder`, `GitHub Optimization Finder`, and `GitHub Redundancy Finder` as their own scheduled tasks and attach their recurring schedules without setting persisted task goals.
3. Do not create separate standalone one-off runner tasks in addition to the scheduled loop tasks. Do not immediately start Dev Inbox or scanner/finder tasks during bootstrap unless the user explicitly asks for an immediate poll/scan pass.
4. Use `set_task_goal` only for implementation tasks that Dev Inbox creates from assigned GitHub issues, or when the user explicitly asks for goal-driven continuation.
5. After all five tasks and schedules exist, call `register_automation_resources` once with adapter key `github_sdlc`, stable key `github-sdlc/default`, and the actual task and schedule IDs. Bind each task and its schedule to the same canonical node key: `vision_suggestions`, `bug_finder`, `optimization_finder`, `redundancy_finder`, and `dev_inbox`. Do not infer or register pre-feature resources.

## Dev Inbox Prompt Pattern

Create a visible scheduled task with a prompt like:

```text
Check GitHub for implementation mailbox work and PR review feedback for this project.

First call `github_forward_pr_feedback_to_tasks` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users on OpenVibely-created task PRs. This tool forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback. If the tool reports missing feedback dependencies, report that PR feedback routing is unavailable but continue normal issue inbox polling.

Always call `github_get_project_inbox`, then call `github_list_assigned_issues` for every returned Authorized User. When PAT authentication is available, also call `github_list_my_assigned_issues` so issues assigned only to the PAT owner are included. Deduplicate issues by repository plus issue number before processing them. If GitHub credentials are missing, or neither a PAT owner nor an Authorized User can be scanned, stop and explain the missing configuration.

Use the issue details returned by `github_list_assigned_issues` and `github_list_my_assigned_issues` directly when they include the issue number, URL, title, body, labels, assignees, and state needed for task creation. Do not call `github_get_issue` for every listed issue as a default step; call it only for an explicit issue read or when a listed issue is missing the fields needed to create an accurate implementation task. Treat assignment to the PAT owner or one of the configured Authorized Users as the user's approval to start implementation work, even when the issue has no associated PR yet. Do not call `github_list_assigned_issues_with_prs` as a default eligibility gate; use it only if you explicitly want a PR-associated-issues-only workflow.

Treat an eligible issue as actionable when it is assigned to the PAT owner or one of the configured Authorized Users. Optional labels such as `approved`, `feature`, `bug`, `performance`, or `duplication` may refine priority/scope, but do not require an `approved` label unless your workflow explicitly says to require one.

Before creating anything, call `list_tasks` (a read-only, current-project task discovery tool) with the GitHub issue number and/or URL as the `query` to reconcile existing implementation work. For GitHub issue reconciliation, omit `category` and `status` for a broad search across all visible task categories and statuses. Supplying `category` or `status` restricts results to only that lifecycle state and can exclude otherwise matching tasks; do not add them when checking whether any matching task exists. Do not enumerate lifecycle state combinations after an empty result with `total=0` and `has_more=false` for that issue query; use a different query such as the exact proposed task title only when issue-number/URL matching is inconclusive. If `list_tasks` returns a matching task, continue that task instead of creating a duplicate. For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue. If no existing task is evident from available task/thread context, call `create_task` immediately with `category=active`; do not wait for an existing PR. A newly created Active task is submitted automatically. Do not call `execute_tasks` for a newly created Active task, because that can submit the same task twice. Set `source_github_issue_number` to the exact issue number returned by this inbox execution. Do not set `source_github_repo_url`; the server resolves Automation provenance from the selected project's configured repository URL, or from a GitHub remote in its local checkout when that URL is blank. Include the GitHub issue number, URL, title, body or acceptance notes, relevant labels, and assignment context in the task prompt, then call `set_task_goal` for the created or reconciled task so it implements the issue and opens/reuses a PR with `github_open_pull_request` when done. For a reconciled existing task, call `execute_tasks` only when `list_tasks` shows category Backlog or status failed/cancelled, and pass that exact existing task ID so approved implementation resumes. Never call `execute_tasks` for an Active pending, queued, running, or completed task. Do not leave approved implementation work in Backlog or merely reconcile a task without starting it when it still needs execution. Do not post status comments on GitHub issues. Add `task-created` / `in-progress` labels only after the task is confirmed started.

Use unprefixed labels only, such as `task-created`, `in-progress`, `blocked`, `needs-human`, and `pr-opened`. Never use labels beginning with `openvibely:`.

When implementation work is complete in a task branch, use `github_open_pull_request` for that task and include issue metadata so the task PR record stays linked.
```

This prompt intentionally uses generic tools. Do not replace it with a hidden background service unless a future product requirement explicitly changes the loop engine.

## Offering Manager Prompt Pattern

Create a visible scheduled task with a prompt like:

```text
Review the configured project vision/source files and identify small, reviewable feature gaps.

Open GitHub suggestion issues only. Use `github_create_issue` with unprefixed labels such as `suggestion` and `feature`. Do not create implementation tasks and do not modify code.

Include enough context for a human to approve, reject, or assign the issue. Before creating any issue, call `github_list_existing_automation_issues`. Use the returned issue numbers, titles, labels, and states to avoid reporting work already covered by this Automation account. Follow next_offset until it is zero before deciding no existing issue matches. If a candidate might match an existing issue, call `github_get_issue` for that issue and read the body. If it is covered, skip that candidate and keep searching for a different new finding. Try to create at most one new GitHub issue this run. Only call `github_create_issue` after you believe the finding is not already represented. If no new finding remains, report that no new issue was found.
```

Offering, Bug Finder, Optimization Finder, and Redundancy Finder tasks should open issues only. They should not modify code, create OpenVibely implementation tasks, or open PRs. Dev Inbox acts on issues assigned to the PAT owner or configured Authorized Users and creates the implementation tasks that later open PRs. Finder categories are always visible as labels: Vision Suggestions uses `suggestion` and `feature`, Bug Finder uses `bug`, Optimization Finder uses `performance`, and Redundancy Finder uses `duplication`. Assignment is the default approval signal for entering the implementation mailbox.

## Role-Specific Finder Prompts

Create separate visible scheduled tasks for bug, optimization, and redundancy discovery. Never give one finder a menu of all three roles and expect it to infer its identity from the task title.

- Bug Finder starts with `You are the Bug Finder.`, requires a concrete correctness failure path plus expected versus actual behavior and regression coverage, and creates issues only with the `bug` label.
- Optimization Finder starts with `You are the Optimization Finder.`, requires measurable evidence or a measurement plan plus before-and-after criteria, and creates issues only with the `performance` label.
- Redundancy Finder starts with `You are the Redundancy Finder.`, identifies the repeated locations and smallest safe consolidation without over-engineering, and creates issues only with the `duplication` label.

Every finder chooses one focused component or workflow and varies it over time. Each issue body starts with `## Summary`: 2-4 plain-language sentences explaining the finding, one concrete example a user would notice, and why it matters. The full technical analysis remains below it for the implementation agent, including inspected components, evidence and failure paths, expected versus actual behavior, risk, suggested implementation direction, acceptance criteria, file/symbol references, and regression cases. Before creating any issue, call `github_list_existing_automation_issues` and follow next_offset until it is zero. If a candidate might match an existing issue, call `github_get_issue` for that issue and read the body. If it is covered, skip that candidate and keep searching for a different new finding. Try to create at most one new GitHub issue this run. Only call `github_create_issue` after you believe the finding is not already represented. If no new finding remains, report that no new issue was found.

Finders do not modify code, create OpenVibely implementation tasks, or open PRs. The Dev Inbox creates implementation tasks later only after a human assigns the issue to the configured OpenVibely GitHub inbox identity.

## Labels

Use plain, unprefixed labels such as:

- `suggestion`
- `approved`
- `in-progress`
- `task-created`
- `pr-opened`
- `blocked`
- `needs-human`
- `done`
- `duplicate`
- `bug`
- `feature`
- `performance`
- `duplication`
- `security-review`

Never use labels beginning with `openvibely:`. OpenVibely rejects that prefix in GitHub issue creation and label-add paths.

## Safety Rules

- Scheduled tasks are the loop engine; do not create hidden GitHub poller daemons.
- GitHub tools are generic reusable capabilities, not SDLC-only APIs.
- Assignment to the PAT owner or configured Authorized User is the default approval signal for OpenVibely to create implementation work; assigned issues do not need an existing PR first.
- Use `github_list_assigned_issues_with_prs` only for explicit PR-associated-issues-only workflows, not for the normal issue-to-task-to-PR flow.
- Do not treat GitHub API credentials alone as human authorization; the issue must be assigned to the configured inbox identity.
- Keep each implementation task tied to visible issue/task/PR state so humans can review and merge in GitHub.

## Troubleshooting

- Dev Inbox stops with missing GitHub account: configure a PAT, or add the GitHub App mailbox user/bot to Authorized Users.
- Dev Inbox refuses work: make sure the issue is assigned to the PAT owner or configured Authorized Users, and remove any stale prompt text that also requires an existing PR or `approved` label.
- Assigned issue is skipped: verify the Dev Inbox scheduled task prompt treats assignment as approval for the normal issue-to-task-to-PR flow and creates a distinct implementation task with an appropriate task-specific goal. Do not set a persisted goal on the Dev Inbox scheduled task itself.
- PR creation fails: check that the task has a worktree branch and that GitHub credentials allow API-backed branch publication and PR creation.
- Labels are rejected: remove any `openvibely:` prefix and use the plain label vocabulary above.

## Related Guides

- [GitHub Channels Setup](./github-channels-setup.md)
- [Schedule User Guide](./schedule-user-guide.md)
- [Tasks User Guide](./tasks-user-guide.md)
