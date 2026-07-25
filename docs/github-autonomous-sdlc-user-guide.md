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

## Canonical Role Prompts

The bundled bootstrap skill owns the maintained prompt templates for Offering Manager, Bug Finder, Optimization Finder, Redundancy Finder, Dev Inbox, and Loop Auditor. The GitHub SDLC Automation template reads those exact same prompts and uses the same daily finder, hourly inbox, and weekly auditor cadences. Use the skill when you want it to create and register the visible Tasks and Schedules for you; use the Automation template when you want to configure and Save the same maintained loop directly in the visual builder.

Do not replace the maintained prompts with shortened examples. The canonical Dev Inbox prompt includes current task and pull-request reconciliation, one evidenced disposition per assigned issue, exact issue linkage on created Tasks, task goals for implementation work, and `github_open_pull_request` publication. Offering Manager and the finder prompts perform repository-wide duplicate checks and create issues only. Loop Auditor remains read-only except for narrowly useful human-facing issue updates.

In both setup paths, assignment to the PAT owner or configured Authorized User is the default approval signal. Finder Tasks do not modify code or create implementation Tasks; Dev Inbox is the gateway that creates issue-specific work after assignment.

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
