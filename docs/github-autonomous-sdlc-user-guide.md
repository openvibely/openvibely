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

1. Create or select the OpenVibely project for the repository.
2. Configure GitHub in `/channels` using a PAT or GitHub App.
3. Add the GitHub user or bot accounts OpenVibely should trust under `Authorized Users` in GitHub Runtime Settings.
4. For PAT setups, assign GitHub issues to the PAT owner when you want OpenVibely scheduled tasks to notice them.
5. For GitHub App setups, assign issues to one of the configured Authorized Users; do not assign issues to an organization installation account.
6. Ensure the scheduled task's model/provider supports runtime tool calls.

A PAT identifies a real GitHub user, so scheduled tasks can call `github_list_my_assigned_issues` to find issues assigned to that user. A GitHub App installation may be installed on an organization, which is not an issue assignee; use `github_get_project_inbox` to read the Authorized Users and pass those logins to `github_list_assigned_issues`.

GitHub issue API tools default to the current project repository, but prompts may pass `repo_url` when they name a specific GitHub repository URL. This applies to issue create/read/list/comment/label tools. Pull request tools remain tied to the current OpenVibely task/project because they publish task worktree branches through the configured GitHub token/API and persist task PR records.

## Bootstrap Skill

OpenVibely bundles `openvibely_github_autonomous_sdlc_bootstrap` as a reusable global skill. In any project with GitHub configured, create or run a visible bootstrap task/task-thread turn so lifecycle routing can select the skill, then ask:

```text
Use the OpenVibely GitHub Autonomous SDLC Bootstrap skill to set up the GitHub SDLC loop for this project. Create the visible scheduled loop tasks and schedules needed. Do not set persisted goals on recurring loop tasks; schedules drive the loop. Use the current project GitHub channel configuration. Report anything missing.
```

The skill creates or updates normal visible tasks and schedules; it does not start a hidden daemon. Setup should create one visible task per loop role and schedule that same task, not create separate standalone runner tasks plus scheduled duplicates. The first setup action should create/run `GitHub Offering Manager: Vision Suggestions` immediately so it can open initial suggestion issues, then attach its daily schedule and create the Dev Inbox, Bug Finder, Optimization Finder, Redundancy Finder, and Loop Auditor schedules afterward.

## Minimum Visible Loop

Start with two scheduled tasks before adding more scanner/finder loops.

| Task | Cadence | Purpose |
|---|---:|---|
| `GitHub Offering Manager: Vision Suggestions` | Daily | Reads project vision/source files and opens suggestion issues only. |
| `GitHub Dev Inbox` | Hourly | Forwards authorized PR comments/reviews to linked implementation tasks, then checks open issues assigned to the PAT user or configured GitHub Authorized Users and links/updates eligible work. |

You can later add Bug Finder, Optimization Finder, Redundancy Finder, and Loop Auditor tasks using the same pattern. These finder tasks open GitHub issues only; Dev Inbox remains the path that turns assigned issues into implementation tasks.

Initial setup order:

1. Create `GitHub Offering Manager: Vision Suggestions` first and run that same task immediately. If no explicit run-existing-task action is available, create it as `active` for the first run, then attach the daily schedule to that same task after the creation/start action is accepted.
2. Create `GitHub Dev Inbox`, `GitHub Bug Finder`, `GitHub Optimization Finder`, `GitHub Redundancy Finder`, and optional Loop Auditor tasks as their own scheduled tasks and attach their recurring schedules without setting persisted task goals.
3. Do not create separate standalone one-off runner tasks in addition to the scheduled loop tasks. Do not immediately start Dev Inbox or scanner/finder tasks during bootstrap unless the user explicitly asks for an immediate poll/scan pass.
4. Use `set_task_goal` only for implementation tasks that Dev Inbox creates from assigned GitHub issues, or when the user explicitly asks for goal-driven continuation.

## Dev Inbox Prompt Pattern

Create a visible scheduled task with a prompt like:

```text
Check GitHub for implementation mailbox work and PR review feedback for this project.

First call `github_forward_pr_feedback_to_tasks` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users on OpenVibely-created task PRs. This tool forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback. If the tool reports missing feedback dependencies, report that PR feedback routing is unavailable but continue normal issue inbox polling.

If this project uses a PAT, call `github_list_my_assigned_issues` to list open issues assigned to the PAT owner. If this project uses GitHub App mode or custom mailbox accounts, call `github_get_project_inbox` to get Authorized Users; pass each returned assignee login to `github_list_assigned_issues`. If GitHub credentials or Authorized Users are missing, stop and explain the missing configuration.

For each returned issue, inspect it with `github_get_issue`. Treat assignment to the PAT owner or one of the configured Authorized Users as the user's approval to start implementation work, even when the issue has no associated PR yet. Do not call `github_list_assigned_issues_with_prs` as a default eligibility gate; use it only if you explicitly want a PR-associated-issues-only workflow.

Treat an eligible issue as actionable when it is assigned to the PAT owner or one of the configured Authorized Users. Optional labels such as `approved`, `feature`, `bug`, `performance`, or `duplication` may refine priority/scope, but do not require an `approved` label unless your workflow explicitly says to require one.

Before creating anything, call `list_tasks` (a read-only, current-project task discovery tool) with the GitHub issue number and/or URL as the `query` to reconcile existing implementation work; if it returns a matching task, continue that task instead of creating a duplicate. For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue. If no existing task is evident from available task/thread context, call `create_task` immediately; do not wait for an existing PR. Include the GitHub issue number, URL, title, and acceptance notes in the task prompt, then call `set_task_goal` for the created task so it implements the issue and opens/reuses a PR with `github_open_pull_request` when done. Comment concise status on the issue with `github_comment_on_issue` and add `task-created` / `in-progress` labels when work is started.

Use unprefixed labels only, such as `task-created`, `in-progress`, `blocked`, `needs-human`, and `pr-opened`. Never use labels beginning with `openvibely:`.

When implementation work is complete in a task branch, use `github_open_pull_request` for that task and include issue metadata so the task PR record stays linked.
```

This prompt intentionally uses generic tools. Do not replace it with a hidden background service unless a future product requirement explicitly changes the loop engine.

## Offering Manager Prompt Pattern

Create a visible scheduled task with a prompt like:

```text
Review the configured project vision/source files and identify small, reviewable feature gaps.

Open GitHub suggestion issues only. Use `github_create_issue` with unprefixed labels such as `suggestion` and `feature`. Do not create implementation tasks and do not modify code.

Include enough context for a human to approve, reject, or assign the issue. Avoid duplicates by searching or inspecting existing visible work when the available tools allow it.
```

Offering, Bug Finder, Optimization Finder, and Redundancy Finder tasks should open issues only. They should not modify code, create OpenVibely implementation tasks, or open PRs. Dev Inbox acts on issues assigned to the PAT owner or configured Authorized Users and creates the implementation tasks that later open PRs. Add labels such as `approved`, `feature`, `bug`, `performance`, or `duplication` when useful for human organization, but assignment is the default approval signal for entering the implementation mailbox.

## Finder Prompt Pattern

Create separate visible scheduled tasks for bug, optimization, and redundancy discovery with prompts like:

```text
Choose one focused project component or workflow to inspect this run. Vary the component over time instead of repeatedly auditing the same files.

Look only for issues in this task's scope:
- Bug Finder: likely defects, edge-case failures, broken behavior, or missing tests that indicate a bug.
- Optimization Finder: measurable performance, latency, memory, build, or workflow efficiency improvements.
- Redundancy Finder: duplicated or redundant code that could be made generic without over-engineering.

Open GitHub issues only using `github_create_issue` with unprefixed labels matching the scope, such as `bug`, `performance`, or `duplication`. Include the inspected component, evidence, risk, and suggested acceptance criteria.

Do not modify code, do not create OpenVibely implementation tasks, and do not open PRs. The Dev Inbox will create implementation tasks later if a human accepts the issue by assigning it to the configured OpenVibely GitHub inbox identity.
```

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
