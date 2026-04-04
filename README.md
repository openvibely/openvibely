# OpenVibely

OpenVibely is an open-source AI task execution platform for software teams.

Plan work, run tasks with your preferred models, and review every code diff before merge or PR.

If you want AI that produces real code changes without losing visibility or control, OpenVibely gives you that workflow out of the box.

Self-hosted and fully under your control.

## Features

- Task workspace with `Backlog`, `Active`, `Scheduled`, and `Completed` lanes.
- Plan + run from chat (`/chat`) with orchestrate and read-only plan modes.
- Live execution visibility: streaming output, status updates, and file changes.
- Task-level diff review flow before merge or pull request.
- Git worktree isolation per task to keep parallel AI work safe.
- Model/provider support for Anthropic, OpenAI, and Ollama.
- Global + per-project worker limits and queue controls.
- Channel integrations for GitHub, Slack, and Telegram in `/channels`.
- REST API + Swagger UI for external automation and integrations.
- Configurable agents with optional plugin/MCP runtime support.

## Quick Start (Recommended)

### Prerequisite

- Go `1.24.4+`

### Fresh Clone

For most users, this is all you need:

```bash
git clone https://github.com/openvibely/openvibely.git
cd openvibely
./start.sh
```

If needed, make it executable once:

```bash
chmod +x start.sh
```

What `./start.sh` does automatically:

- Installs `templ` if missing
- Runs `templ generate`
- Builds `bin/openvibely`
- Starts the server and tails `logs/openvibely.log`

Default URL with `./start.sh`: `http://localhost:3001`

You do not need to run `go mod download` first for this flow.
You do not need `make install-tools` just to run with `./start.sh`.

## Optional Developer Workflow

Install extra tooling only if you want advanced dev workflows:

```bash
make install-tools
make dev
```

`make install-tools` gives you:

1. `air` for `make dev` live reload
2. `swag` for Swagger generation
3. `goose` CLI (optional; normal app migrate flow does not require it)

Default URL with `make dev` (or direct server run without `PORT` override): `http://localhost:3001`

## First-Time In-App Setup

After startup:

1. Add at least one model in `/models`.
2. (Optional) Configure agents in `/agents`.
3. Create a project (local path or GitHub URL).
4. Create tasks in `/tasks` or orchestrate from `/chat`.
5. Configure `/workers` if you need tighter capacity control.

## Configuration

Set environment variables directly or place them in `.env` (loaded by `start.sh`).

### Core Runtime

| Variable | Default | Description |
|---|---|---|
| `PORT` | `3001` | HTTP port |
| `DATABASE_PATH` | `./openvibely.db` | SQLite file path |
| `ENVIRONMENT` | `development` | Runtime environment |
| `PROJECT_REPO_ROOT` | `./repos` | Managed clone root for GitHub URL projects |
| `OPENVIBELY_PLUGIN_ROOT` | app-local default | Plugin root override |

### Feature Flags

| Variable | Default | Description |
|---|---|---|
| `OPENVIBELY_ENABLE_LOCAL_REPO_PATH` | `true` via `start.sh`; otherwise `false` | Enables local-path project source mode |
| `OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS` | `true` via `start.sh`; otherwise `false` | Shows merge options in task `Changes` tab |

### Integration/Provider Variables

| Variable | Description |
|---|---|
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `GITHUB_APP_ID`, `GITHUB_APP_SLUG`, `GITHUB_APP_PRIVATE_KEY` | GitHub App mode settings |
| `SLACK_CLIENT_ID`, `SLACK_CLIENT_SECRET`, `SLACK_APP_TOKEN`, `SLACK_BOT_TOKEN` | Slack OAuth/Socket/manual token settings |

## UI User Guides

User-facing guides live in [`docs/user-guides.md`](./docs/user-guides.md), including:

- Channels: Slack, Telegram, GitHub
- Pages: Project Setup, Models, Agents, Workers, Tasks, Chat, Schedule

## API and Swagger

Swagger UI:

- `http://localhost:3001/swagger/index.html` (when using `./start.sh`)

Example:

```bash
curl -X POST http://localhost:3001/api/chat/message \
  -F "message=Summarize the current task board" \
  -F "project_id=default"
```

## Project Structure

```text
cmd/
  server/
docs/
internal/
  config/
  database/
  handler/
  llm/
  models/
  repository/
  service/
  testutil/
pkg/
start.sh
web/templates/
```

## Development

```bash
go test ./... -count=1 -timeout 60s
make build
```

Common targets:

- `make dev`
- `make build`
- `make templ`
- `make swagger`
- `make run`
- `make clean`

## For AI Agents

If you are working on this repository as an AI coding agent, read in this order:

1. `AGENTS.md`
2. `MEMORY.md`
3. `guardrails.md`
4. `PRACTICES.md`

## License

MIT
