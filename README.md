
# OpenVibely

OpenVibely is open-source infrastructure for teams that want AI to ship real code, with full visibility and control.

From one chat prompt, you can create and run tasks, monitor execution in real time, and review diffs before anything gets merged.

Built for teams that want speed without giving up quality, auditability, or ownership.

Self-hosted, single binary, and built for high performance with low overhead.

User-friendly by design, simple to operate, and fast to set up.

<a href="https://github.com/user-attachments/assets/377521fa-b117-476c-a52a-cfc10befb981">
  <img src="docs/screenshots/openvibely-ui-demo-poster.png" alt="Watch the OpenVibely UI demo" width="100%" />
</a>

## Features

- Agent task board for clear status tracking, visibility, and control.
- Chat-first flow: create, plan, delegate, and run work from chat.
- Agent delegation with chained tasks for multi-step execution.
- Custom agents with reusable skills and MCP-enabled plugins.
- Personalities to tune behavior and communication style.
- Insights and analytics to spot trends, bottlenecks, and quality issues.
- Real-time execution visibility: streaming output, status updates, and live file changes.
- Reviewable diffs before merge or pull request, with per-task git worktree isolation.
- Auditability by default through execution logs, thread history, and code diffs.
- Model providers: Anthropic, OpenAI, and Ollama.
- Messaging channels: GitHub, Slack, and Telegram.
- Task scheduling for one-time and recurring execution.
- Minimal operations footprint: self-hosted single binary + SQLite by default.
- High-performance runtime for fast startup, responsive execution, and low overhead.
- REST API + Swagger UI for automation and external integrations.

## Quick Start (Recommended)

### Prerequisite

- Go `1.24.4+`

### Fresh Clone

For most users, setup is this fast:

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
| `APP_BASE_URL` | unset | Public base URL for hosted deployments (for OAuth callback URL generation), for example `https://dubee.org` |
| `OAUTH_REDIRECT_MODE` | `auto` | OAuth callback strategy: `auto` (uses hosted callbacks when `APP_BASE_URL` is set), `hosted` (force hosted callbacks), `localhost_manual` (force localhost redirect URIs and complete by pasting callback URL in UI) |
| `ANTHROPIC_OAUTH_CLIENT_ID` | unset | Optional Anthropic hosted OAuth client ID override (in `auto/hosted` mode only); falls back to built-in client |
| `ANTHROPIC_OAUTH_CLIENT_SECRET` | unset | Optional Anthropic hosted OAuth client secret |
| `ANTHROPIC_OAUTH_AUTHORIZE_URL` | `https://claude.ai/oauth/authorize` | Optional Anthropic hosted OAuth authorize endpoint override |
| `ANTHROPIC_OAUTH_TOKEN_URL` | `https://platform.claude.com/v1/oauth/token` | Optional Anthropic hosted OAuth token endpoint override |
| `ANTHROPIC_OAUTH_SCOPES` | built-in default | Optional Anthropic hosted OAuth scope override |
| `OPENAI_OAUTH_CLIENT_ID` | unset | Optional OpenAI hosted OAuth client ID override (in `auto/hosted` mode only); falls back to built-in Codex client |
| `OPENAI_OAUTH_CLIENT_SECRET` | unset | Optional OpenAI hosted OAuth client secret |
| `OPENAI_OAUTH_AUTHORIZE_URL` | `https://auth.openai.com/oauth/authorize` | Optional OpenAI hosted OAuth authorize endpoint override |
| `OPENAI_OAUTH_TOKEN_URL` | `https://auth.openai.com/oauth/token` | Optional OpenAI hosted OAuth token endpoint override |
| `OPENAI_OAUTH_SCOPES` | built-in default | Optional OpenAI hosted OAuth scope override |

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

