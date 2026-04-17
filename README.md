
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

### Runtime and Project Setup

| Variable | Purpose | Allowed values | Default behavior (from code) | Examples (local / VPS-public) |
|---|---|---|---|---|
| `PORT` | HTTP listen port | Any valid TCP port string | `3001` (`internal/config/config.go`; Docker image overrides to `3001`) | `3001` \| `8080` |
| `DATABASE_PATH` | SQLite database file path | Filesystem path | `./openvibely.db` | `./openvibely.db` \| `/var/lib/openvibely/openvibely.db` |
| `DATABASE_URL` | Reserved config field (currently loaded but not used by server startup) | String | Empty (`""`) when unset | unset \| unset |
| `ENVIRONMENT` | Runtime environment label | Free-form string | `development` (`start.sh` also defaults to `development`; Docker image sets `production`) | `development` \| `production` |
| `PROJECT_REPO_ROOT` | Managed clone root for GitHub URL projects | Filesystem path | `./repos` | `./repos` \| `/var/lib/openvibely/repos` |
| `OPENVIBELY_PLUGIN_ROOT` | App-local plugin root override | Filesystem path (absolute or relative) | Unset = app-local plugin root (`.openvibely/plugins` under runtime base) | `./.openvibely/plugins` \| `/var/lib/openvibely/plugins` |
| `OPENVIBELY_ENABLE_LOCAL_REPO_PATH` | Enables Local Path source mode in project setup | `1,true,yes,on,0,false,no,off` | Unset/invalid = `false`; `start.sh` exports `true` unless overridden in `.env` | `true` \| `false` |
| `OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS` | Shows merge options in task `Changes` tab | `1,true,yes,on,0,false,no,off` | Unset/invalid = `false`; `start.sh` exports `true` unless overridden in `.env` | `true` \| `false` |
| `OPENVIBELY_CODEX_REASONING_EFFORT` | Fallback reasoning effort for Codex requests when model config does not set one | `low`, `medium`, `high`, `xhigh` | If unset/invalid, defaults to `high` | `high` \| `medium` |

### App Authentication (Built-in Login)

| Variable | Purpose | Required? | Allowed values | Default behavior (from code) | Security notes | Example (safe placeholder) |
|---|---|---|---|---|---|---|
| `AUTH_ENABLED` | Explicitly enable/disable built-in login middleware | Optional (explicit toggle) | `1,true,yes,on,0,false,no,off` | If unset/invalid, inferred from credentials: enabled only when both `AUTH_USERNAME` and `AUTH_PASSWORD` are set | Prefer explicit `true`/`false` in production to avoid accidental enablement from partial env changes | `true` |
| `AUTH_USERNAME` | Login username for built-in auth | Required when auth is enabled | String | Empty by default; if set with `AUTH_PASSWORD` and `AUTH_ENABLED` unset, auth is inferred enabled | Keep non-sensitive but unique; avoid obvious defaults like `admin` in internet-exposed deployments | `openvibely_admin` |
| `AUTH_PASSWORD` | Login password for built-in auth | Required when auth is enabled | String | Empty by default | Treat as a secret; generate a long random password and store in secret manager/env file with restricted permissions | `__REPLACE_WITH_LONG_RANDOM_PASSWORD__` |
| `AUTH_SESSION_SECRET` | HMAC signing secret for `ov_session` cookie tokens | Required when auth is enabled | String | Empty by default | Treat as high-sensitivity secret; use at least 32 random bytes. Rotating this invalidates existing login sessions | `__REPLACE_WITH_32+_BYTE_RANDOM_SECRET__` |
| `AUTH_SESSION_TTL` | Session lifetime for signed auth cookies | Optional | Go duration string (`24h`, `12h`, `30m`) | `24h`; invalid/non-positive values fall back to `24h` | Keep long enough for usability but short enough for risk tolerance; avoid very short TTLs that cause frequent logouts | `24h` |

Runtime enforcement from code:
- If auth resolves enabled and `AUTH_USERNAME`/`AUTH_PASSWORD`/`AUTH_SESSION_SECRET` is missing, startup fails with `invalid auth configuration: ...`.
- If `AUTH_ENABLED` is unset, setting both `AUTH_USERNAME` and `AUTH_PASSWORD` implicitly enables auth.

### OAuth and Deployment Variables

| Variable | Purpose | Required? | Allowed values | Default behavior (from code) | Security notes | Examples (local / VPS-public) |
|---|---|---|---|---|---|---|
| `APP_BASE_URL` | External origin used for absolute callback/redirect URLs | Required for hosted OAuth mode | Absolute `http://` or `https://` URL, no query/fragment/userinfo | Unset/invalid -> ignored; app falls back to forwarded/request host. OAuth `auto` then uses localhost mode when no valid base URL is available | Not a secret, but must be accurate for OAuth redirects and reverse-proxy setups | unset \| `https://app.example.com` |
| `OAUTH_REDIRECT_MODE` | OAuth callback strategy | Optional | `auto`, `hosted`, `localhost_manual` | Unset/invalid -> `auto`; invalid values are logged and treated as `auto`; `hosted` without `APP_BASE_URL` returns `400` on OAuth initiate | `hosted` is safest for public deployments with proper callback registration; `localhost_manual` is fallback for providers that only accept localhost redirect URIs | `localhost_manual` or `auto` \| `auto` or `hosted` |
| `ANTHROPIC_OAUTH_CLIENT_ID` | Hosted Anthropic OAuth client ID override | Optional (hosted mode strongly recommended) | String | Used only for hosted callback mode; fallback is built-in Anthropic client ID | Client ID is not secret, but should match your registered OAuth app | unset \| `your_anthropic_client_id` |
| `ANTHROPIC_OAUTH_CLIENT_SECRET` | Hosted Anthropic OAuth client secret override | Optional | String | Used only for hosted callback mode; default empty | Secret: store in env/secret manager, never in git | unset \| `__REPLACE_WITH_ANTHROPIC_OAUTH_CLIENT_SECRET__` |
| `ANTHROPIC_OAUTH_AUTHORIZE_URL` | Anthropic authorize endpoint override | Optional | URL string | `https://claude.ai/oauth/authorize` | Not secret; override only for provider-compatible custom endpoints | unset \| `https://claude.ai/oauth/authorize` |
| `ANTHROPIC_OAUTH_TOKEN_URL` | Anthropic token endpoint override | Optional | URL string | `https://platform.claude.com/v1/oauth/token` | Not secret; keep provider-accurate | unset \| `https://platform.claude.com/v1/oauth/token` |
| `ANTHROPIC_OAUTH_SCOPES` | Anthropic scopes override | Optional | Space-delimited scope string | `user:profile user:inference user:sessions:claude_code user:mcp_servers` | Not secret; grant least privilege needed | unset \| provider-specific scopes |
| `OPENAI_OAUTH_CLIENT_ID` | Hosted OpenAI OAuth client ID override | Optional (hosted mode strongly recommended) | String | Used only for hosted callback mode; fallback is built-in Codex client ID | Client ID is not secret, but should match your OAuth app | unset \| `your_openai_client_id` |
| `OPENAI_OAUTH_CLIENT_SECRET` | Hosted OpenAI OAuth client secret override | Optional | String | Used only for hosted callback mode; default empty | Secret: store in env/secret manager, never in git | unset \| `__REPLACE_WITH_OPENAI_OAUTH_CLIENT_SECRET__` |
| `OPENAI_OAUTH_AUTHORIZE_URL` | OpenAI authorize endpoint override | Optional | URL string | `https://auth.openai.com/oauth/authorize` | Not secret; override only when required | unset \| `https://auth.openai.com/oauth/authorize` |
| `OPENAI_OAUTH_TOKEN_URL` | OpenAI token endpoint override | Optional | URL string | `https://auth.openai.com/oauth/token` | Not secret; keep provider-accurate | unset \| `https://auth.openai.com/oauth/token` |
| `OPENAI_OAUTH_SCOPES` | OpenAI scopes override | Optional | Space-delimited scope string | `openid profile email offline_access api.connectors.read api.connectors.invoke` | Not secret; grant least privilege needed | unset \| provider-specific scopes |

Auth-secret values should always be supplied as environment variables (or secret manager injection), not hardcoded in source, Compose YAML, or committed `.env` files.

Keep `.env`/`*.env` files containing secrets out of git (for example via `.gitignore`) and restrict file permissions (for example `chmod 600`).

Security recommendations for internet-facing deployments:
- Always terminate TLS (HTTPS) at your reverse proxy/load balancer.
- Use long random values for `AUTH_PASSWORD`, `AUTH_SESSION_SECRET`, and OAuth client secrets.
- Rotate secrets periodically; expect active sessions to be invalidated after `AUTH_SESSION_SECRET` rotation.
- Do not print secret values in logs, screenshots, CI output, or issue tickets.

OAuth callback mode behavior summary:
- `auto`: uses hosted callbacks only when `APP_BASE_URL` resolves to a valid absolute URL; otherwise uses localhost callback flow.
- `hosted`: always uses hosted callbacks and requires `APP_BASE_URL`.
- `localhost_manual`: always uses localhost redirect URIs and expects manual callback URL paste in UI.

### Integration and Provider Bootstrap Variables

| Variable | Purpose | Allowed values | Default behavior (from code) | Examples (local / VPS-public) |
|---|---|---|---|---|
| `ANTHROPIC_API_KEY` | Anthropic API key convenience input (also used as vision fallback when no vision-capable model is configured) | String | Empty by default | `sk-ant-...` \| secret via env/vault |
| `TELEGRAM_BOT_TOKEN` | Bootstraps Telegram bot token | String | Empty by default; if empty, app can use token saved in Settings DB | unset \| `123456:ABC-DEF...` |
| `GITHUB_APP_ID` | GitHub App auth bootstrap | String | Empty by default | unset \| `123456` |
| `GITHUB_APP_SLUG` | GitHub App slug bootstrap | String | Empty by default | unset \| `your-github-app` |
| `GITHUB_APP_PRIVATE_KEY` | GitHub App private key bootstrap | PEM string | Empty by default | unset \| `-----BEGIN RSA PRIVATE KEY-----...` |
| `SLACK_CLIENT_ID` | Slack OAuth client ID bootstrap | String | Empty by default | unset \| `1234567890.1234567890` |
| `SLACK_CLIENT_SECRET` | Slack OAuth client secret bootstrap | String | Empty by default | unset \| `your_slack_client_secret` |
| `SLACK_APP_TOKEN` | Slack Socket Mode app token bootstrap | String | Empty by default | unset \| `xapp-...` |
| `SLACK_BOT_TOKEN` | Slack manual bot token override bootstrap | String | Empty by default; when set, startup stores override token and marks token source as manual | unset \| `xoxb-...` |

### Git/GitHub Clone SSL Variables

Git operations (especially GitHub clone/fetch paths) auto-detect system CA bundles. If no valid CA bundle is found, OpenVibely falls back to `GIT_SSL_NO_VERIFY=true` and logs a warning.

| Variable | Purpose | Allowed values | Default behavior (from code) | Examples (local / VPS-public) |
|---|---|---|---|---|
| `GIT_SSL_CAINFO` | Explicit CA bundle path for git TLS verification | Filesystem path | If set in environment, it is respected and auto-detection is skipped | `/etc/ssl/certs/ca-certificates.crt` \| `/etc/ssl/certs/ca-certificates.crt` |
| `SSL_CERT_FILE` | Alternate certificate file path recognized by git/TLS tooling | Filesystem path | If present, auto-detection is skipped | unset \| `/etc/ssl/certs/ca-bundle.crt` |
| `GIT_SSL_NO_VERIFY` | Explicit TLS verification override for git | `true`/`false` (string) | If unset and no CA bundle is found, app appends `GIT_SSL_NO_VERIFY=true` as last resort | unset \| `false` (preferred) |

Auto-detected CA bundle locations by OS:
- Debian/Ubuntu/Alpine: `/etc/ssl/certs/ca-certificates.crt`
- RHEL/CentOS: `/etc/pki/tls/certs/ca-bundle.crt`
- OpenSUSE: `/etc/ssl/ca-bundle.pem`
- OpenBSD (if present): `/etc/ssl/cert.pem`
- FreeBSD: `/usr/local/share/certs/ca-root-nss.crt`

### OAuth Callback Troubleshooting

If OAuth opens or returns to `localhost` on a remote deployment:

1. Set `APP_BASE_URL` to your public app origin (for example `https://app.example.com`).
2. Leave `OAUTH_REDIRECT_MODE=auto` (recommended) or set `OAUTH_REDIRECT_MODE=hosted`.
3. If you set `hosted`, ensure `APP_BASE_URL` is set and valid, otherwise `/models/:id/oauth/initiate` fails with `OAUTH_REDIRECT_MODE=hosted requires APP_BASE_URL`.
4. For OpenAI hosted callbacks, ensure your OAuth app allows `<APP_BASE_URL>/auth/callback`; for Anthropic hosted callbacks, allow `<APP_BASE_URL>/callback`.
5. If you intentionally want localhost callback URIs on a remote box, set `OAUTH_REDIRECT_MODE=localhost_manual` and finish by pasting the callback URL in the Models UI.
6. If `APP_BASE_URL` includes query, fragment, userinfo, or non-http(s), it is treated as invalid and ignored; fix it to an absolute `http(s)` origin.

Minimal deployment examples:

```bash
# Local development (default localhost callback behavior)
PORT=3001
DATABASE_PATH=./openvibely.db
# APP_BASE_URL unset
OAUTH_REDIRECT_MODE=auto

# VPS/public hostname (hosted OAuth callbacks)
PORT=8080
DATABASE_PATH=/var/lib/openvibely/openvibely.db
APP_BASE_URL=https://app.example.com
OAUTH_REDIRECT_MODE=auto
# Optional hosted OAuth client overrides:
# OPENAI_OAUTH_CLIENT_ID=...
# ANTHROPIC_OAUTH_CLIENT_ID=...
```

**Security note**: The `GIT_SSL_NO_VERIFY=true` fallback is intended to keep self-hosted setups usable when CA bundles are missing. For production with sensitive repositories, install a valid CA bundle and set `GIT_SSL_CAINFO` explicitly.

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

