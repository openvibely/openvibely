# GitHub Channels Setup

This guide covers GitHub setup from `/channels`.

## What GitHub Channel Is For

GitHub channel settings give OpenVibely authenticated access to GitHub operations used by project/task workflows (for example private repo access, authenticated clone/push, and pull request flows).

Why this matters:

- Lets OpenVibely use app-managed credentials for private repositories.
- Enables authenticated Git operations and PR-related actions.
- Keeps credentials centralized in one place.
- Provides the generic GitHub runtime settings used by prompt-driven scheduled tasks.

GitHub URL project creation can still clone through your local `git` CLI when no PAT or GitHub App token is configured. That fallback supports public repositories and private repositories your local Git environment can already access. Configure this channel when you want OpenVibely-managed credentials or PR workflows.

## Choose an Auth Mode

OpenVibely supports two GitHub auth modes in `/channels`:

1. `Personal Access Token (Recommended)`
2. `GitHub App (Advanced)`

PAT is the fastest setup for local/self-hosted usage.

## Option A: Personal Access Token (Recommended)

1. Open `/channels`.
2. Click `+ Add Channel` -> `GitHub`.
3. Set auth mode to `Personal Access Token (Recommended)`.
4. Paste your token in `GitHub Personal Access Token`.
5. Click `Save GitHub Settings`.

After save, the GitHub card should show connected/configured.

Why choose PAT:

- Quickest local setup.
- Least moving parts.
- Good default for single-user or self-hosted deployments.

## Option B: GitHub App (Advanced)

1. Open `/channels` and add/open `GitHub`.
2. Set auth mode to `GitHub App (Advanced)`.
3. Fill:
   - `GitHub App ID`
   - `GitHub App Slug`
   - `GitHub App Private Key (PEM)`
4. Click `Save GitHub Settings`.
5. Back on the GitHub card, click `Connect` to complete installation callback flow.

Why choose GitHub App:

- Better for centralized/cloud-style multi-user deployments.
- Uses installation-based auth model instead of user PATs.

## GitHub Runtime Settings

After configuring GitHub credentials, open the GitHub settings modal and use `GitHub Runtime Settings` for prompt-driven task workflows.

- `Authorized Users` works like the Authorized Users list in other channels: GitHub users allowed for this channel.
- Scheduled GitHub tasks can scan issues assigned to Authorized Users.
- With PAT auth, scheduled tasks can also find open issues assigned to the PAT owner directly.
- With GitHub App auth, add the real GitHub user or bot that should receive OpenVibely work to `Authorized Users`, then assign issues to that account.
- User-facing GitHub logins can be entered as `alice` or `@alice`; OpenVibely stores the normalized plain login.
- Ordinary non-Automation GitHub issue tools may accept `repo_url` for an explicitly named repository. Automation-bound GitHub tools ignore model-supplied `repo_url` overrides and use only the selected project's configured GitHub repository URL, falling back to a GitHub remote in that project's local checkout when the URL is blank.
- Pull request tools remain tied to the current OpenVibely task/project because they publish task worktree branches through the configured GitHub token/API and persist task PR records.

For the visible scheduled-task pattern, see the [GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md).

## Card Actions

From the GitHub card menu:

- `Edit`
- `Remove`

In App mode, card actions also include connect/disconnect state handling.

## Status Badges

The card shows one of:

- `Not Configured`
- `Not Connected`
- `Connected`

## How This Relates To Other Pages

- `/channels` configures GitHub integration/auth and GitHub runtime trust/inbox settings.
- Project repository source (`repo_url`/clone behavior) is managed in project settings.
- Task PR creation is done from task `Changes` tab or the generic `github_open_pull_request` runtime tool when available.
- Prompt-driven scheduled GitHub mailbox loops are described in the [GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md).

## Troubleshooting

- Save fails in PAT mode: token is required.
- Save fails in App mode: App ID, slug, and private key are required.
- App mode stays not connected: run `Connect` from the card and complete install callback.
