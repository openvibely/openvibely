# Project Setup User Guide

Use this guide to create and configure projects in OpenVibely.

## What A Project Is

A project is the workspace boundary for your tasks, chat context, schedules, worker limits, and repository settings. The selected project controls what Chat, Tasks, Schedule, Workers, Alerts, Memory, Insights, Channels, and many settings operate on.

Why this matters:

- Keeps unrelated work separated.
- Lets you tune execution limits per project.
- Defines where code operations happen (local path or managed GitHub clone).

## Project-Scoped Features

| Feature | How The Project Matters |
|---|---|
| Chat | Conversations run with the selected project context. |
| Tasks | Board categories, task execution, review, and schedules belong to the project. |
| Memory | Repository-local managed memory is stored under `.openvibely/memories` when memory is enabled. |
| Skills | Project-scoped skills and agent-owned skills can override global behavior for this repository. |
| Workers | Project worker limits prevent one workspace from consuming all execution capacity. |
| Channels | Slack, Telegram, GitHub, and webhooks can be connected to project work. |
| Insights | Grades, Pulse, Reflection, and Analytics summarize project activity. |

## Before You Create A Project

Recommended first steps:

1. Add at least one model in `/models`.
2. If you plan to create pull requests or need app-managed GitHub credentials, configure GitHub in `/channels`.

If no model is configured, chat/task execution is blocked until a model exists.

## Open The New Project Dialog

In the left sidebar, use the `Project` section:

1. Click the `+` icon (tooltip: `New Project`).
2. Fill the modal fields.
3. Click `Create`.

## Required Fields

- `Name` (required)

Optional fields:

- `Description`
- `Default Model` (project-level override)
- `Max Concurrent Workers` (project-level capacity limit)

## Choose Repository Source

`Repository Source` controls where this project’s code lives.

### Option A: GitHub URL

Use this when OpenVibely should clone and manage the repo for you.

1. Select `GitHub URL`.
2. Enter `https://github.com/<owner>/<repo>`.
3. Click `Create`.

Behavior:

- OpenVibely clones the repository into managed storage (default root is `~/.openvibely/repos` unless `PROJECT_REPO_ROOT` or `OPENVIBELY_APP_DATA_DIR` is set).
- The project stores both the normalized GitHub URL and managed clone path.
- If GitHub PAT/App auth is configured in `/channels`, OpenVibely uses it for the clone.
- If no GitHub token is configured, OpenVibely falls back to the local `git` CLI. This works for public repositories and private repositories when your local Git credential helper, SSH setup, or CLI environment already has access.

If both configured GitHub auth and the local git fallback are unavailable or fail, creation fails with the underlying clone error.

### Option B: Local Path

Use this when you already have a local repository folder.

1. Select `Local Path`.
2. Enter an absolute path in `Repository Path`, or click `Choose Folder`.
3. Optional: enable `Create directory if it doesn't exist`.
4. Click `Create`.

Notes:

- Path must be absolute (for example `/Users/name/code/repo`).
- Home-relative values like `~/code/repo` are accepted and normalized.
- If local-path mode is disabled in your environment, this option is hidden and only `GitHub URL` is available.

## Project Guidance

OpenVibely loads reusable project guidance from app-managed memory and skills, such as `.openvibely/memories/` and `.openvibely/skills/`. Use managed memory for durable project facts and skills for reusable task guidance.

Projects may still provide root instruction files such as `AGENTS.md` or `CLAUDE.md` for compatibility with other tools and local workflows, but OpenVibely's own repository guidance is stored in managed project skills/memory rather than requiring those root files to exist.

## Default Model vs Global Default

`Default Model` in the project form is a per-project override.

- `Use global default`: uses whatever is default on `/models`.
- Specific model selected: tasks in this project use that model unless task-level model is set.

## Max Concurrent Workers

`Max Concurrent Workers` limits parallel task execution for this project only.

- Empty or `0` (`No limit`): the project inherits the global worker capacity rules.
- A positive value: this project can run up to that many tasks concurrently, provided the value does not exceed the current finite global limit.
- With an unlimited global limit (`0`), any positive project value is valid; there is no product-level maximum.

Project limits are independent caps, not a sum that must fit inside the global limit. The runtime enforces the global limit using actual concurrent reservations, so multiple projects may each have a cap equal to the global limit without reducing one another's configured values.

Lowering the global limit does not cancel running work. Existing project caps that are now above the global limit are retained for safe runtime behavior and marked `Exceeds global` in Workers; lower those caps before saving a new finite project limit. New admissions remain blocked whenever actual global usage has reached the lowered ceiling.

Use this to keep one project from consuming all worker slots.

## Edit Existing Project

To update an existing project:

1. In the sidebar `Project` section, select the project from the dropdown.
2. Click the gear icon (tooltip: `Project Settings`).
3. Update fields and save.

## Delete Project

From `Project Settings`, you can delete non-default projects.

Important:

- The default project cannot be deleted.
- Deletion removes project records from OpenVibely.

## First Project Checklist

- Give the project a clear name that matches the repository or product area.
- Add a repository path or URL before expecting worktree diffs and review flows.
- Choose a default model if most tasks should use the same provider.
- Let project-scoped memory and skills capture repository conventions as work completes.
- Set a worker limit if the project should not consume unlimited execution slots.
- Add channels only after the project is stable enough for team use.

## Troubleshooting

- `failed to clone GitHub repository ... local git clone failed`: confirm `git` is installed and available on `PATH`, then verify your Git credentials can clone the repository from a terminal. Configure GitHub auth in `/channels` if you want OpenVibely-managed credentials.
- `Repository path must be an absolute path`: use a full absolute path or native folder picker.
- Local path option missing: `OPENVIBELY_ENABLE_LOCAL_REPO_PATH` is disabled in this environment.
