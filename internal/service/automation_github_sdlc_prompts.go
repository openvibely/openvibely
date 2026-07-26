package service

import (
	"fmt"
	"strings"
)

// These prompts are owned by the maintained GitHub SDLC Automation template.
// They intentionally mirror the behavior of the bootstrap skill shipped when
// the template was defined, but template execution does not depend on that
// skill being installed or retained.
const githubSDLCDevInboxPrompt = `Check GitHub for implementation mailbox work and PR review feedback for this project.

First call ` + "`" + `github_forward_pr_feedback_to_tasks` + "`" + ` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users on OpenVibely-created task PRs. This tool forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback. If the tool reports missing feedback dependencies, report that PR feedback routing is unavailable but continue normal issue inbox polling.

If this project uses a PAT, call ` + "`" + `github_list_my_assigned_issues` + "`" + ` to list open issues assigned to the PAT owner. If this project uses GitHub App mode or custom mailbox accounts, call ` + "`" + `github_get_project_inbox` + "`" + ` to get Authorized Users; pass each returned assignee login to ` + "`" + `github_list_assigned_issues` + "`" + `. If GitHub credentials or Authorized Users are missing, stop and explain the missing configuration.

For each returned issue, inspect it with ` + "`" + `github_get_issue` + "`" + `. Treat assignment to the PAT owner or configured Authorized User as the user's approval to start implementation work, even when the issue has no associated PR yet. Do not call ` + "`" + `github_list_assigned_issues_with_prs` + "`" + ` as a default eligibility gate; use it only if the user explicitly asks for a PR-associated-issues-only workflow.

Treat an eligible issue as actionable when it is assigned to the PAT owner or configured Authorized Users. Optional labels such as ` + "`" + `approved` + "`" + `, ` + "`" + `feature` + "`" + `, ` + "`" + `bug` + "`" + `, ` + "`" + `performance` + "`" + `, or ` + "`" + `duplication` + "`" + ` may refine priority/scope, but do not require an ` + "`" + `approved` + "`" + ` label unless the user's workflow explicitly says to require one.

Before creating anything, call ` + "`" + `list_tasks` + "`" + ` (a read-only, current-project task discovery tool) with the GitHub issue number and/or URL as the ` + "`" + `query` + "`" + ` to reconcile existing implementation work; if it returns a matching task, continue that task instead of creating a duplicate. For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue. If no existing task is evident from available task/thread context, call ` + "`" + `create_task` + "`" + ` immediately; do not wait for an existing PR. Set ` + "`" + `source_github_issue_number` + "`" + ` to the exact issue number returned by this inbox execution. Do not set ` + "`" + `source_github_repo_url` + "`" + `; the server resolves Automation provenance from the selected project's configured repository URL, or from a GitHub remote in its local checkout when that URL is blank. Include the GitHub issue number, URL, title, and acceptance notes in the task prompt, then call ` + "`" + `set_task_goal` + "`" + ` for the created task so it implements the issue and opens/reuses a PR with ` + "`" + `github_open_pull_request` + "`" + ` when done. Comment concise status on the issue with ` + "`" + `github_comment_on_issue` + "`" + ` and add ` + "`" + `task-created` + "`" + ` / ` + "`" + `in-progress` + "`" + ` labels when work is started.

Use unprefixed labels only, such as ` + "`" + `task-created` + "`" + `, ` + "`" + `in-progress` + "`" + `, ` + "`" + `blocked` + "`" + `, ` + "`" + `needs-human` + "`" + `, and ` + "`" + `pr-opened` + "`" + `. Never use labels beginning with ` + "`" + `openvibely:` + "`" + `.

When implementation work is complete in a task branch, use ` + "`" + `github_open_pull_request` + "`" + ` for that task and include issue metadata so the task PR record stays linked. Do not use local ` + "`" + `git push` + "`" + ` or GitHub CLI as a fallback if the tool fails; report the tool error so GitHub token/API publication can be fixed.`

const githubSDLCImplementationPrompt = `Implement the assigned GitHub issue in the current project.

Read the issue and relevant project context, make the focused code or documentation changes needed to satisfy its acceptance criteria, and run the relevant build and tests. Keep the work tied to the source issue and use github_open_pull_request to open or reuse the reviewable pull request when implementation is complete. Do not approve or merge the pull request, release, or deploy on a human's behalf.`

const githubSDLCOfferingManagerPrompt = `Review the configured project vision/source files and identify small, reviewable feature gaps.

Open GitHub suggestion issues only. Use ` + "`" + `github_create_issue` + "`" + ` with unprefixed labels such as ` + "`" + `suggestion` + "`" + ` and ` + "`" + `feature` + "`" + `. Do not create implementation tasks and do not modify code.

Include enough context for a human to approve, reject, or assign the issue. Do not list, search, or inspect existing GitHub issues for duplicate detection; the server prevents duplicate Automation-created issues using trusted local state.`

const githubSDLCFinderPrompt = `Choose one focused project component or workflow to inspect this run. Vary the component over time instead of repeatedly auditing the same files.

Look only for issues in this task's scope:
- Bug Finder: likely defects, edge-case failures, broken behavior, or missing tests that indicate a bug.
- Optimization Finder: measurable performance, latency, memory, build, or workflow efficiency improvements.
- Redundancy Finder: duplicated or redundant code that could be made generic without over-engineering.

Open GitHub issues only using ` + "`" + `github_create_issue` + "`" + ` with unprefixed labels matching the scope, such as ` + "`" + `bug` + "`" + `, ` + "`" + `performance` + "`" + `, or ` + "`" + `duplication` + "`" + `. Include the inspected component, evidence, risk, and suggested acceptance criteria.

Do not modify code, do not create OpenVibely implementation tasks, and do not open PRs. The Dev Inbox will create implementation tasks later if a human accepts the issue by assigning it to the configured OpenVibely GitHub inbox identity.`

const githubSDLCLoopAuditorPrompt = `Reviews stale labels, blocked work, duplicate tasks, missing issue/task/PR links, and unexpected GitHub assignments.`

func githubSDLCRolePrompt(role string) (string, error) {
	switch strings.TrimSpace(role) {
	case "offering_manager":
		return githubSDLCOfferingManagerPrompt, nil
	case "bug_finder", "optimization_finder", "redundancy_finder":
		return githubSDLCFinderPrompt, nil
	case "github_inbox":
		return githubSDLCDevInboxPrompt, nil
	case "implementation":
		return githubSDLCImplementationPrompt, nil
	case "loop_auditor":
		return githubSDLCLoopAuditorPrompt, nil
	default:
		return "", fmt.Errorf("unsupported GitHub SDLC prompt role %q", role)
	}
}
