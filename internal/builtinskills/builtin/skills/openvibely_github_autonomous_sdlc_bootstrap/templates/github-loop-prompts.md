# GitHub Loop Prompt Templates

Use these templates when creating visible OpenVibely scheduled tasks for a GitHub-backed autonomous SDLC loop. Keep labels unprefixed, scanners read-only, and state visible through issues, tasks, schedules, and linked PRs.

## Loop Auditor

```text
Audit the GitHub autonomous SDLC loop for this project.

Confirm the current worktree matches the intended repository and verify GitHub credentials, the PAT owner or Authorized Users, visible task context, scheduling/task tools, and required GitHub tools. Stop and name every missing prerequisite before auditing; do not infer unavailable state.

Use `github_list_my_assigned_issues` for PAT setups or `github_get_project_inbox` plus `github_list_assigned_issues` for configured Authorized Users. Inspect relevant issues with `github_get_issue`, and retrieve current task and authoritative PR evidence with the available task/PR lookup tools before classifying issue/task/PR linkage or lifecycle state.

Check separately for stale tasks, blocked work, duplicate GitHub issues, repeated or superseded task attempts, missing issue/task/PR links, unexpected assignments, inconsistent labels, and labels beginning with `openvibely:`. Do not report retry tasks as duplicate GitHub issues. Treat failed and cancelled task records as visible history unless there is evidence they are still being mistaken for current work; recommend reconciliation or supersession rather than deletion.

Base every finding on the state actually returned in this run. An open issue carrying `merged` is not by itself proof of a conflict: identify the linked PR and verify whether it merged, then report the precise mismatch, such as a merged PR with an open tracking issue or a stale label with no merged PR. Likewise, do not attribute failed tasks to PR publication, credentials, branch publishing, or a systemic incident unless task failure details or authoritative tool results establish that cause; otherwise describe the timing correlation and the missing evidence.

For each material finding, include the affected issue/task/PR identifiers, observed state, evidence timestamp when relevant, and the narrow next action or human decision needed. State explicitly when duplicate-issue, blocked-work, or linkage checks found nothing, and separate verified findings from hypotheses or tool limitations.

Keep the workspace read-only. Do not modify code, create implementation tasks, open PRs, or create background workers. Report eligible assigned issues lacking a visible implementation task so Dev Inbox can dispatch them. Comment or change labels only when useful to unblock humans or record a material transition, and use unprefixed labels.
```

## Dev Inbox

```text
Check GitHub for implementation mailbox work and PR review feedback for this project.

At the start of the turn, load `references/dev-inbox-execution-invariants.md` from the selected `openvibely_github_autonomous_sdlc_bootstrap` skill with `skill_view`. Apply its per-issue dispositions and completion check even if this stored prompt is older or incomplete.

Confirm the current worktree matches the intended repository and verify GitHub credentials, the PAT owner or Authorized Users, PR-feedback forwarding, issue inbox tools, task discovery/creation, task-goal support, and issue update tools. Stop and name any missing prerequisite rather than substituting local Git, GitHub CLI, labels, memory, or prose.

First call `github_forward_pr_feedback_to_tasks`. Inspect its current result before saying no feedback exists. Report unauthorized feedback that was found but not routed, and ask for the reviewer handle to be authorized when appropriate. Treat self or bot feedback as intentionally ignored; mentions do not grant authorization.

For PAT mode, call `github_list_my_assigned_issues`. For GitHub App or custom inbox mode, call `github_get_project_inbox` and pass each returned login to `github_list_assigned_issues`. Inspect every returned issue with `github_get_issue`. Assignment to the PAT owner or configured Authorized User is approval to start implementation even without an existing PR. Do not require an `approved` label or use `github_list_assigned_issues_with_prs` as the initial eligibility gate unless the user explicitly requests that stricter workflow.

Before the first mutation, reconcile every assigned issue against both task and authoritative PR evidence. Call `list_tasks` with each issue number and distinctive title substrings once for every relevant category, normally `active`, `backlog`, `scheduled`, and `completed`. `list_tasks` searches titles only, so do not query a URL that exists only in task prompt text. Also retrieve current issue-to-PR evidence for every candidate with `github_list_assigned_issues_with_prs` or another available authoritative GitHub PR lookup. The compact task summaries do not prove PR state, and absence of PR evidence from issue details or labels does not prove no PR exists. If required PR evidence cannot be retrieved, report the limitation rather than dispatching from an unverified assumption.

Continue existing work only when an exposed runtime action actually resumes it; otherwise report the capability blocker or classify demonstrably running, queued, or open-PR work as `already-active`. If no suitable task or associated PR exists, call `create_task` using only fields exposed by the current runtime. Pass `source_github_issue_number` and `source_github_repo_url`, and put the issue number, URL, title, repository, acceptance criteria, expected validation, and `github_open_pull_request` instructions in the task title or prompt, then call `set_task_goal`. Comment and apply plain status labels only after this run successfully starts or materially transitions work.

Implementation tasks use `github_open_pull_request` with issue metadata. Do not substitute `git push` or GitHub CLI if publication fails.

Give every inspected assigned issue exactly one disposition: `started`, `continued`, `already-active`, `already-implemented`, or `skipped`. Do not use raw task states as dispositions or claim PR/integration evidence without a current authoritative tool result.

Before sending the summary, include this literal reconciliation line with actual counts:

assigned inspected = started + continued + already-active + already-implemented + skipped

If any issue lacks one evidenced disposition, or a required transition failed, report the exact blocker instead of claiming successful processing.
```

## Offering Manager

```text
Review the configured project vision and source-of-truth files for small, reviewable feature gaps.

Confirm the worktree matches the intended repository and verify managed GitHub credentials, repository access, authenticated repository-wide current issue listing or search, and issue creation are available. Stop and name any missing prerequisite; do not use assigned-user inboxes, guessed issue reads, visible tasks, memory, or prior summaries as a substitute for the repository-wide duplicate check.

Read the primary vision, README, deployment, product, and local project metadata that describes goals or active work. Before publication, search current repository-wide issues for the same user need, component, and proposed outcome. If that search is unavailable or a matching issue exists, do not create a new issue.

Open suggestion issues only with `github_create_issue` and unprefixed labels such as `suggestion` and `feature`. Include the evidence source, vision alignment, observed gap, and acceptance criteria. Do not modify code, create implementation tasks, set goals, or open PRs.
```

## Bug Finder / Optimization Finder / Redundancy Finder

```text
Confirm the worktree matches the intended repository and verify managed GitHub credentials, repository access, authenticated repository-wide current issue listing or search, and issue creation are available. Stop and name any missing prerequisite; do not use assigned-user inboxes, visible tasks, memory, or prior summaries as a substitute for the repository-wide duplicate check.

Choose one focused component, workflow, or recent-change area. Vary the area over time, but do not expand after finding an actionable item.

Look only for this task's scope:
- Bug Finder: likely defects, edge-case failures, broken behavior, or missing tests indicating a bug.
- Optimization Finder: measurable performance, latency, memory, build, deployment, or workflow improvements.
- Redundancy Finder: duplicated code or workflow that supports a narrow generic abstraction without hiding meaningful differences.

Before publication, search current repository-wide issues for the same component and failure mode or proposed outcome. If that search is unavailable or a matching issue exists, do not create a new issue. Otherwise create at most one high-confidence issue with `github_create_issue`, an unprefixed scope label such as `bug`, `performance`, or `duplication`, the inspected component, evidence, likely affected files/functions, risk, and acceptance or validation criteria.

Keep the workspace read-only. Do not modify code, generate artifacts, install dependencies, create implementation tasks, set goals, or open PRs. Run validation only when the task explicitly requires it and it preserves read-only scope. Dev Inbox dispatches implementation after a human assigns the issue to a configured inbox identity.
```
