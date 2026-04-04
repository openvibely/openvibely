# GitHub Channels Setup

This guide covers GitHub setup from `/channels`.

## What GitHub Channel Is For

GitHub channel settings give OpenVibely authenticated access to GitHub operations used by project/task workflows (for example repo access and pull request flows).

Why this matters:

- Lets OpenVibely work with private repositories.
- Enables authenticated Git operations and PR-related actions.
- Keeps credentials centralized in one place.

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

- `/channels` configures GitHub integration/auth.
- Project repository source (`repo_url`/clone behavior) is managed in project settings.
- Task PR creation is done from task `Changes` tab.

## Troubleshooting

- Save fails in PAT mode: token is required.
- Save fails in App mode: App ID, slug, and private key are required.
- App mode stays not connected: run `Connect` from the card and complete install callback.
