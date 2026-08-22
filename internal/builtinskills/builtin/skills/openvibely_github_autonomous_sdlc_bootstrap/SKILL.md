---
kind: openvibely.agent_skill
version: 1
skill:
    key: openvibely_github_autonomous_sdlc_bootstrap
    name: OpenVibely GitHub Autonomous SDLC Bootstrap
    scope: global
    enabled: false
    description: Bootstrap a GitHub-backed, prompt-driven autonomous SDLC loop using generic GitHub tools and visible OpenVibely tasks and schedules.
---

# OpenVibely GitHub Autonomous SDLC Bootstrap

Use this skill when the user asks to automate a project with GitHub, use GitHub issues as a mailbox, or set up reviewable GitHub issue-to-task-to-PR development.

Keep the setup generic. Use existing OpenVibely tasks, schedules, task threads, and generic GitHub runtime tools. Do not create hidden daemon or poller services, workflow-specific database state, or bespoke SDLC-only backends when a visible scheduled task prompt and reusable tools are enough. Recurring loop tasks should be driven by their schedules and prompt text, not persisted task goals, unless the user explicitly asks for goal-driven continuation.

## Required Direction

- Scheduled tasks are the loop engine. Create or update visible scheduled OpenVibely tasks with prompts that say exactly what GitHub mailbox work to perform. Do not set persisted task goals on recurring loop tasks by default; their recurrence comes from `schedule_task`, not Goal Agent continuation.
- Bootstrap setup should run from a visible task or task-thread follow-up so lifecycle routing can select this skill and expose `skill_view`; ordinary Chat may have Orchestrate actions but does not run standalone skill routing today. If a user starts in Chat, create a small bootstrap task and continue setup there rather than claiming the skill was applied in Chat.
- GitHub tools are reusable capabilities, not SDLC-only APIs. Always read assignee candidates with `github_get_project_inbox` and call `github_list_assigned_issues` for every returned Authorized User. When PAT authentication is available, also use `github_list_my_assigned_issues` to include issues assigned only to the authenticated PAT user. Deduplicate issues by repository plus issue number. For GitHub App setups, do not treat the installation owner or organization as an issue assignee; add the real GitHub user or bot that should receive work to Authorized Users. For this Automation bootstrap, do not pass `repo_url` overrides. Automation-bound GitHub tools resolve only the selected project's configured GitHub repository URL, falling back to a GitHub remote in that project's local checkout when the URL is blank. Use `github_is_actor_authorized`, `github_create_issue`, `github_get_issue`, `github_list_assigned_issues_with_prs`, `github_add_issue_labels`, and `github_open_pull_request` when available.
- GitHub PR publication for implementation tasks should use `github_open_pull_request`, not ad hoc `git push` or GitHub CLI fallback. The tool is current-project/task scoped, publishes the task worktree branch through the configured GitHub token/API, then opens or reuses the PR and persists task PR metadata. The Changes tab Create PR button uses the same backend path with UI defaults; the runtime tool can additionally pass PR title/body/base/draft and issue metadata.
- `github_replace_pull_request_branch` is a separate destructive remediation tool, not normal publication. Use it only after explicit approval when an existing linked PR needs its stale history replaced with the task worktree's clean local `HEAD`. Pass the exact current remote PR branch SHA as `expected_head_sha` and explicitly set `confirm_history_rewrite: true`; the operation uses atomic `git push --force-with-lease`, refuses dirty worktrees, and fails rather than overwriting a concurrently changed remote head.
- GitHub labels must be unprefixed. Never use labels beginning with `openvibely:`. Use labels such as `suggestion`, `approved`, `in-progress`, `task-created`, `pr-opened`, `blocked`, `needs-human`, `done`, `duplicate`, `bug`, `feature`, `performance`, and `duplication`.
- Assignment to the configured OpenVibely GitHub inbox identity is the default human approval signal to start work. Assigned issues do not need an existing PR before automation may create OpenVibely implementation tasks; creating the task and later opening a PR is the normal issue-to-task-to-PR flow. Use `github_list_assigned_issues_with_prs` only for special workflows that explicitly require PR-associated issues.
- Human trust is separate from API credentials. PAT credentials identify a real GitHub user for default assignment polling. GitHub App credentials identify an installation and may be installed on an organization, so App setups use Authorized Users as the real issue assignee accounts to poll; assigning an issue to one of those configured identities is the user's approval to enter the implementation mailbox.
- Offering/finder/scanner tasks open GitHub issues only. They do not modify code, create implementation tasks, or open PRs. The Dev Inbox is the default implementation gateway: it acts on issues assigned to the PAT owner or configured Authorized Users, creates distinct OpenVibely implementation tasks, and lets those implementation tasks open/reuse PRs. Optional labels such as `approved` may help humans audit the mailbox, but the default bootstrap workflow must not require an `approved` label in addition to assignment unless the user explicitly asks for that stricter gate.

## Bootstrap Steps

1. Confirm the current project, repository, and GitHub credentials. Ask only for missing inputs.
2. Create one visible OpenVibely task per loop role; do not create separate one-off setup/runner tasks in addition to the scheduled loop tasks. The task you schedule is the task that owns the loop prompt. Do not call `set_task_goal` for recurring loop tasks during bootstrap.
3. Create `GitHub Offering Manager: Vision Suggestions` first and make it run immediately before creating downstream implementation schedules. If the current runtime cannot explicitly execute an existing task, create this task as `active` for its first run, wait for that setup action to be accepted, then attach its daily recurring schedule to the same task. Do not create a second standalone Vision Suggestions task for the immediate run, and do not set a persisted goal on this recurring loop task.
4. After the Vision Suggestions task is created for its immediate first run, create the Dev Inbox, Bug Finder, Optimization Finder, and Redundancy Finder as the scheduled loop tasks themselves and attach their recurring schedules without setting persisted task goals. Do not start Dev Inbox or scanner/finder tasks as extra one-off setup work unless the user explicitly asks for an immediate poll/scan pass.
5. Create recurring schedules with `schedule_task`; usually daily for suggestion/finder/scanner tasks and hourly for Dev Inbox. Reuse the same task IDs/titles for schedules rather than duplicating tasks.
6. Put the behavior in each task prompt. Prompts should tell the agent which generic GitHub tools to call, which labels/auth checks to apply, and how to keep visible task and PR state current without posting issue status comments.
7. Use `set_task_goal` only for implementation tasks that Dev Inbox creates from assigned GitHub issues, or when the user explicitly asks for a goal-driven task. Do not use persisted goals to make scheduled loop tasks recur.
8. After all maintained setup tasks and schedules exist, call `register_automation_resources` once with `adapter_key: github_sdlc`, stable key `github-sdlc/default`, and their actual IDs. Bind both the task and its schedule to the same node key: `vision_suggestions`, `bug_finder`, `optimization_finder`, `redundancy_finder`, and `dev_inbox`. Do not use separate trigger node keys. Mark intentionally reused worker tasks as `shared`. Do not pass topology JSON or infer pre-feature resources. A setup rerun reuses the same Automation identity.
9. Report exactly which tasks, schedules, labels, and GitHub credential/settings dependencies were created or still need user action, plus the returned Automation URL.

## Suggested Visible Tasks

- `GitHub Offering Manager: Vision Suggestions`, daily. Reads the project vision/source-of-truth files and opens suggestion issues only.
- `GitHub Dev Inbox`, hourly. Calls `github_forward_pr_feedback_to_tasks` so authorized PR review feedback reaches linked implementation tasks, then scans every configured Authorized User with `github_get_project_inbox` plus `github_list_assigned_issues` and additionally scans the PAT owner with `github_list_my_assigned_issues` when available, deduplicates results, treats assignment as approval, and creates or continues one visible implementation task per actionable issue without posting issue status comments.
- `GitHub Bug Finder`, daily. Chooses a focused project component, audits it for likely defects, and opens GitHub bug issues only.
- `GitHub Optimization Finder`, daily. Chooses a focused project component or workflow, looks for measurable performance/efficiency opportunities, and opens GitHub performance issues only.
- `GitHub Redundancy Finder`, daily. Chooses a focused project component, looks for duplicated/redundant code that could become a generic abstraction, and opens GitHub duplication issues only.

## Prompt Pattern For Dev Inbox

Use a prompt like this when creating the Dev Inbox scheduled task:

```text
Check GitHub for implementation mailbox work and PR review feedback for this project.

First call `github_forward_pr_feedback_to_tasks` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users on OpenVibely-created task PRs. This tool forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback. If the tool reports missing feedback dependencies, report that PR feedback routing is unavailable but continue normal issue inbox polling.

Always call `github_get_project_inbox`, then call `github_list_assigned_issues` for every returned Authorized User. When PAT authentication is available, also call `github_list_my_assigned_issues` so issues assigned only to the PAT owner are included. Deduplicate issues by repository plus issue number before processing them. If GitHub credentials are missing, or neither a PAT owner nor an Authorized User can be scanned, stop and explain the missing configuration.

Use `github_list_assigned_issues` and `github_list_my_assigned_issues` as compact body-free discovery lists containing issue numbers, URLs, titles, labels, assignees, and state. Do not call `github_get_issue` for every listed issue as a default scan step; after repository/issue deduplication and existing-task reconciliation, call it only for the specific listed issue that needs body or acceptance-note details for accurate task creation. Treat assignment to the PAT owner or configured Authorized User as the user's approval to start implementation work, even when the issue has no associated PR yet. Do not call `github_list_assigned_issues_with_prs` as a default eligibility gate; use it only if the user explicitly asks for a PR-associated-issues-only workflow. Use the provided GitHub runtime tools as the only source for inbox discovery; do not use local shell commands, `gh`, Python scripts, `curl`, or direct GitHub API calls to list or reconstruct assigned issues. If a runtime GitHub tool response is incomplete or too large to inspect safely, use `github_get_issue` only for the specific issue numbers already returned by that runtime tool, or report the limitation.

Treat an eligible issue as actionable when it is assigned to the PAT owner or configured Authorized Users. Optional labels such as `approved`, `feature`, `bug`, `performance`, or `duplication` may refine priority/scope, but do not require an `approved` label unless the user's workflow explicitly says to require one.

Before creating anything, call `list_tasks` (a read-only, current-project task discovery tool) with the GitHub issue number and/or URL as the `query` to reconcile existing implementation work. For each GitHub issue, perform one existing-task lookup with `list_tasks` using the issue number or URL as the `query`. Treat that single lookup result as the reconciliation result for that issue. Do not retry the same issue lookup, do not vary task lifecycle filters to search again, and do not run title or fragment searches for the same issue. If `list_tasks` returns a matching task, continue that task instead of creating a duplicate. For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue. If no existing task is evident from available task/thread context, call `create_task` immediately with `category=active`; do not wait for an existing PR. A newly created Active task is submitted automatically. Do not call `execute_tasks` for a newly created Active task, because that can submit the same task twice. After `create_task` succeeds for a newly created Active task, do not call `list_tasks` again for that issue in the same inbox turn just to verify start, labels, or existence; the successful `create_task` response is the confirmation to proceed with the goal refresh and summary/labels. Set `source_github_issue_number` to the exact issue number returned by this inbox execution. Do not set `source_github_repo_url`; the server resolves Automation provenance from the selected project's configured repository URL, or from a GitHub remote in its local checkout when that URL is blank. Include the GitHub issue number, URL, title, body or acceptance notes, relevant labels, and assignment context in the task prompt, then call `set_task_goal` for the created or reconciled task so it implements the issue and opens/reuses a PR with `github_open_pull_request` when done. For a reconciled existing task, call `execute_tasks` only when `list_tasks` shows category Backlog or status failed/cancelled, and pass that exact existing task ID so approved implementation resumes. Never call `execute_tasks` for an Active pending, queued, running, or completed task. Do not leave approved implementation work in Backlog or merely reconcile a task without starting it when it still needs execution. Do not post status comments on GitHub issues. Add `task-created` / `in-progress` labels only after the task is confirmed started.

Use unprefixed labels only, such as `task-created`, `in-progress`, `blocked`, `needs-human`, and `pr-opened`. Never use labels beginning with `openvibely:`.

When implementation work is complete in a task branch, use `github_open_pull_request` for that task and include issue metadata so the task PR record stays linked. Do not use local `git push` or GitHub CLI as a fallback if the tool fails; report the tool error so GitHub token/API publication can be fixed.
```

## Prompt Pattern For Offering Manager

```text
Review the configured project vision/source files and identify small, reviewable feature gaps.

Open GitHub suggestion issues only. Use `github_create_issue` with unprefixed labels such as `suggestion` and `feature`. Do not create implementation tasks and do not modify code.

Include enough context for a human to approve, reject, or assign the issue. Before creating any issue, call `github_list_existing_automation_issues`. Use the returned issue numbers, titles, labels, and states to avoid reporting work already covered by this Automation account. Follow next_offset until it is zero before deciding no existing issue matches. If a candidate might match an existing issue, call `github_get_issue` for that issue and read the body. If it is covered, skip that candidate and keep searching for a different new finding. Try to create at most one new GitHub issue this run. Only call `github_create_issue` after you believe the finding is not already represented. If no new finding remains, report that no new issue was found.
```

## Role-Specific Finder Prompts

Give each finder its own prompt. Never give one finder a menu of all three roles and expect it to infer its identity from the task title.

- Bug Finder: start with `You are the Bug Finder.` Inspect only likely correctness defects, edge-case failures, broken behavior, or missing tests that indicate a bug. Require a concrete failure path, expected versus actual behavior, and needed regression coverage. Create issues only with the `bug` label.
- Optimization Finder: start with `You are the Optimization Finder.` Inspect only measurable performance, latency, throughput, memory, build, or workflow efficiency bottlenecks. Require evidence or a measurement plan and before-and-after criteria. Create issues only with the `performance` label.
- Redundancy Finder: start with `You are the Redundancy Finder.` Inspect only demonstrated duplicated or redundant code, configuration, or workflow logic. Name the repeated locations and propose the smallest safe consolidation without over-engineering. Create issues only with the `duplication` label.

Every finder prompt must choose one focused project component or workflow and vary it over time. Require every body to start with `## Summary`: 2-4 plain-language sentences explaining the finding, one concrete example a user would notice, and why it matters. After that summary, preserve all details useful to an implementation agent, including the inspected component, evidence and failure paths, expected versus actual behavior, risk, suggested implementation direction, acceptance criteria, file/symbol references, and regression cases. Pass the role's required category label in the tool call rather than relying on body text. Before creating any issue, call `github_list_existing_automation_issues` and follow next_offset until it is zero. If a candidate might match an existing issue, call `github_get_issue` for that issue and read the body. If it is covered, skip that candidate and keep searching for a different new finding. Try to create at most one new GitHub issue this run. Only call `github_create_issue` after you believe the finding is not already represented. If no new finding remains, report that no new issue was found.

Do not modify code, do not create OpenVibely implementation tasks, and do not open PRs. The Dev Inbox creates implementation tasks later only after a human assigns the issue to the configured OpenVibely GitHub inbox identity.

## Common Pitfalls

- Do not create hidden services or background workers for the GitHub loop.
- Do not make GitHub runtime tools explicit-agent-grant-only as part of this setup; scheduled tasks need the generic GitHub tools when GitHub is configured and the provider supports runtime tool calls.
- Do not create or mutate agents unless the user explicitly asks and the available tool surface supports it. Prefer visible tasks, schedules, prompts, and configured GitHub identities; do not add persisted goals to recurring loop tasks.
- Do not treat GitHub API credentials as authorization for human-triggered auto-fix work.
- Do not rely on prompt memory alone for duplicate prevention. Finder prompts must call `github_list_existing_automation_issues` before creating new reports, follow next_offset until it is zero, inspect similar issues with `github_get_issue` when needed, skip covered candidates, and keep searching.
- Do not claim the bootstrap is complete if required GitHub tools, channel credentials, or schedules are missing.
