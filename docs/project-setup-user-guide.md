# Project Setup User Guide

## Skills and Memory Permissions

In project settings, **Skills & Memory** controls skill and memory reads and writes independently. New projects allow skill reads and memory reads/writes, while skill writes are off. **Allow global skill writes** is separate and requires skill writes. Existing projects retain their previous permissions.

For a managed GitHub project whose local repository is missing, use **Re-clone** in project settings to recover it. Local-path projects require an existing valid repository directory.

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

## Project Computers

Project computers provide Linux desktops and Chromium for browser and desktop work. They are optional and disabled by default. Existing projects without a saved computer setting also remain disabled.

1. Make the `docker` command available to OpenVibely and start the Docker daemon.
2. Enable **Project computer** in project settings. This permits computer use; it does not enable every task.
3. Open a task's **Computer** tab and enable **Use computer for this task**. See [task computer controls](tasks-user-guide.md#using-a-task-computer).

The local provisioner creates one container per project, with separate task desktops, files, and browser profiles. The first use builds a missing image from source bundled with the app; this can take several minutes and requires network access. After an app update, the next computer start checks the expected image and replaces an outdated container while preserving its volume. Set `OPENVIBELY_COMPUTER_IMAGE` to use a prebuilt image instead; see [configuration](environment.md#project-computers).

The current local implementation invokes Docker directly. Podman and rootless engines have not been validated. The computer container runs its controller as root with `SYS_ADMIN`, `NET_ADMIN`, `/dev/net/tun`, and AppArmor confinement disabled; task processes use restricted users. It is not launched with `--privileged`. Hosting OpenVibely inside a container does not automatically give it access to a computer provisioner; do not mount the Docker socket to work around this.

Project computer controls also configure desktop time zone and idle shutdown. Idle shutdown keeps stored files and browser profiles but closes applications and loses unsaved work. Deleting a provisioned project removes its container and volume before deleting project records. If Docker cleanup fails, start Docker or resolve the reported problem and retry deletion.

### Container Setup by Operating System

Choose one local engine below and keep it running while using project computers. These are engine setup options, not a claim that every OS/engine combination has passed OpenVibely integration tests.

| OS | Docker setup | Rancher Desktop setup |
|---|---|---|
| macOS | Install [Docker Desktop for Mac](https://docs.docker.com/desktop/setup/install/mac-install/) for your processor and start it. | Install [Rancher Desktop for macOS](https://docs.rancherdesktop.io/getting-started/installation/#macos), start it, and select **dockerd (Moby)**. |
| Windows | Install [Docker Desktop for Windows](https://docs.docker.com/desktop/setup/install/windows-install/), enable its WSL 2 backend, and use **Linux containers**, not Windows containers. | Install [Rancher Desktop for Windows](https://docs.rancherdesktop.io/getting-started/installation/#windows), complete its WSL 2 requirements, and select **dockerd (Moby)**. |
| Linux | Install [Docker Engine](https://docs.docker.com/engine/install/) for your distribution and start its service, or install and start [Docker Desktop for Linux](https://docs.docker.com/desktop/setup/install/linux/), which requires KVM. | Install [Rancher Desktop for Linux](https://docs.rancherdesktop.io/getting-started/installation/#linux), meet its virtualization requirements, and select **dockerd (Moby)**. |

In Rancher Desktop, choose the engine under **Preferences → Container Engine → General**. [dockerd (Moby) provides the Docker CLI/API](https://docs.rancherdesktop.io/ui/preferences/container-engine/general/); **containerd/nerdctl mode is not supported by OpenVibely's current local provisioner**. Kubernetes is not required for project computers.

Before enabling a project computer, verify these commands as the same OS user that runs OpenVibely:

```sh
docker context show
docker version
docker info
```

`docker version` must include a responding server, not just an installed client. The selected context must point to your intended local Docker or Rancher engine. OpenVibely connects to published loopback ports, so a remote Docker context is not a substitute. If several engines are installed, use `docker context ls` and select the intended context before launching OpenVibely; their images and volumes are separate.

The OpenVibely process must find `docker` on its `PATH`. After installing CLI tools or changing permissions, restart OpenVibely; a GUI-launched app may have a different environment from your terminal. On Linux, configure [Docker access for the OpenVibely user](https://docs.docker.com/engine/install/linux-postinstall/) so commands work without an interactive `sudo` prompt. Docker-group membership grants root-level engine access; it is not rootless mode.

**Validation status:** development checks have exercised Docker-backed computers on macOS and Linux container runtime tests. Rancher Desktop on all three OSes, Windows provisioning, Linux Desktop engine variants, and rootless engines have not been validated end to end. Meeting the upstream installation requirements alone does not establish compatibility with OpenVibely's nested isolation and networking setup.

## Before You Create A Project

Recommended first steps:

1. Add at least one model in `/models`.
2. If you plan to create pull requests or need app-managed GitHub credentials, configure GitHub in `/channels`.

If no model is configured, chat/task execution is blocked until a model exists.

## Open The New Project Dialog

Use the project menu:

1. Open the plus menu beside the project tabs, or the project selector on a narrow layout, and choose `Create New Project`.
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

**Linux web app:** `Choose Folder` requires either `zenity` or `kdialog` (OpenVibely tries `zenity` first) and an accessible graphical desktop session on the machine running the server. These are optional dependencies; manual path entry works without them. The picker opens on the server's desktop, not on a remote browser's machine. For headless servers, enter the repository's absolute path on the server manually.

**Desktop app:** `Choose Folder` uses Wails' native picker on Linux, macOS, and Windows. It does not require `zenity` or `kdialog`.

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

1. Select the project's tab, or use the project selector on a narrow layout.
2. Open the tab's context menu and choose `Project Settings`, or use the selector's settings control.
3. Update fields and save.

## Switch Projects

Project tabs can be dragged to reorder them. Open tabs, their order, and each project's last page are restored across restarts. Use Command/Ctrl+Shift+Left or Right to switch tabs and Command/Ctrl+Shift+K to open the project menu.

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

## All Projects

Select **All projects** to work across projects from one workspace. Tasks, automations, Analytics, and Insights combine project records and show their ownership. Filter to a project when you need a narrower view. The All projects tab can be reordered or closed like other project tabs.

All projects has its own saved Chat conversation. Select a destination project when creating project-owned work; tasks and automations keep their own project context. A conversation opened within a specific project stays scoped to that project.

## Share a project

Open Project Settings → Sharing to grant organization members viewer, editor, or maintainer access. Joining the organization does not grant project access. Project owners and organization admins/owners manage grants and ownership; maintainers manage project work but cannot change sharing. Save applies the staged sharing changes together.

Project lists and live updates show only projects the user can access. Revoking access blocks new work and subscriptions, but does not automatically cancel work already running. These are application permissions, not process or filesystem isolation; private-project creation remains unavailable.

