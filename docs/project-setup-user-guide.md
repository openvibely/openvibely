# Project Setup User Guide

Use this guide to create and configure projects in OpenVibely.

## What A Project Is

A project is the workspace boundary for your tasks, chat context, schedules, worker limits, and repository settings.

Why this matters:

- Keeps unrelated work separated.
- Lets you tune execution limits per project.
- Defines where code operations happen (local path or managed GitHub clone).

## Before You Create A Project

Recommended first steps:

1. Add at least one model in `/models`.
2. If you plan to use a GitHub repository URL, configure GitHub in `/channels` first.

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

- OpenVibely clones the repository into managed storage (default root is `./repos` unless `PROJECT_REPO_ROOT` is set).
- The project stores both the normalized GitHub URL and managed clone path.

If GitHub credentials are missing (for example PAT not configured), creation fails and you get a toast with a link to `/channels`.

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

## Default Model vs Global Default

`Default Model` in the project form is a per-project override.

- `Use global default`: uses whatever is default on `/models`.
- Specific model selected: tasks in this project use that model unless task-level model is set.

## Max Concurrent Workers

`Max Concurrent Workers` limits parallel task execution for this project only.

- Empty (`No limit`): project uses global worker capacity rules.
- Set value (for example `2`): this project can run up to that many tasks concurrently.

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

## Troubleshooting

- `failed to clone GitHub repository ... PAT is not configured`: open `/channels` and configure GitHub auth.
- `Repository path must be an absolute path`: use a full absolute path or native folder picker.
- Local path option missing: `OPENVIBELY_ENABLE_LOCAL_REPO_PATH` is disabled in this environment.
