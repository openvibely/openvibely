# Environment Variables

This is the full in-repo environment-variable reference for self-hosting OpenVibely. For high-level overview and quick start, see the [project README](../README.md). For canonical user and operator docs, see <a href="https://docs.openvibely.ai" target="_blank" rel="noopener noreferrer">docs.openvibely.ai</a>.

Set environment variables directly or place them in `.env` (loaded by `start.sh`). The published docs keep a shorter operator-facing configuration overview; this page keeps the detailed reference close to the source tree.

## Runtime and Project Setup

| Variable | Purpose | Allowed values | Default behavior (from code) | Examples (local / VPS-public) |
|---|---|---|---|---|
| `PORT` | HTTP listen port | Any valid TCP port string | `3001` (`internal/config/config.go`; Docker image overrides to `3001`; desktop uses `0` for ephemeral) | `3001` \| `8080` |
| `OPENVIBELY_APP_DATA_DIR` | Overrides the external app-data root shared by the database, managed repos, global agents, skills, and plugin storage | Filesystem path (absolute recommended) | Server: `~/.openvibely`. Desktop: macOS `~/Library/Application Support/OpenVibely`, Windows `%LOCALAPPDATA%\OpenVibely`, Linux `$XDG_DATA_HOME/openvibely`. Docker: `/data`. Desktop storage must remain outside the replaceable application bundle. | `~/.openvibely` \| `/data` |
| `OPENVIBELY_RUNTIME_DIR` | **Deprecated** `start.sh`-only alias for `OPENVIBELY_APP_DATA_DIR`. If `OPENVIBELY_APP_DATA_DIR` is unset and this is set, `start.sh` copies its value into `OPENVIBELY_APP_DATA_DIR`. Has no effect when the binary is launched directly. | Filesystem path | Unset; prefer `OPENVIBELY_APP_DATA_DIR` in all new configurations | deprecated \| — |
| `DATABASE_PATH` | SQLite database file path | Filesystem path | `<OPENVIBELY_APP_DATA_DIR>/openvibely.db`; desktop uses its platform application-data directory and server uses `~/.openvibely` | `~/.openvibely/openvibely.db` \| `/data/openvibely.db` |
| `DATABASE_URL` | Reserved config field (currently loaded but not used by server startup) | String | Empty (`""`) when unset | unset \| unset |
| `ENVIRONMENT` | Runtime environment label | Free-form string | `development` (`start.sh` also defaults to `development`; Docker image sets `production`) | `development` \| `production` |
| `OPENVIBELY_LOG_LEVEL` | Controls runtime log verbosity. Setting `debug` enables `applog.Debugf` output (LLM adapter traces, worker events, etc.). All other values or unset default to `info`. | `info`, `debug` | `info` (`start.sh` exports `info`; process default when unset is also `info` via `applog` package init) | `info` \| `debug` |
| `PROJECT_REPO_ROOT` | Managed clone root for GitHub URL projects | Filesystem path | `<OPENVIBELY_APP_DATA_DIR>/repos`; desktop uses its platform application-data directory and server uses `~/.openvibely` | `~/.openvibely/repos` \| `/data/repos` |
| `OPENVIBELY_PLUGIN_ROOT` | Plugin storage root override | Filesystem path (absolute or relative) | Desktop defaults to `<platform application-data directory>/.openvibely/plugins`; server defaults to the existing runtime-local plugin root | `./.openvibely/plugins` \| `/var/lib/openvibely/plugins` |
| `OPENVIBELY_DESKTOP_CONFIG_FILE` | Overrides the path to the desktop app's `config.env` file that the packaged Wails binary reads at startup. Useful when the OS-packaged app does not inherit shell environment variables. | Filesystem path | OS-conventional desktop config dir: macOS `~/Library/Application Support/OpenVibely/config.env`; Windows `%LOCALAPPDATA%\OpenVibely\config.env`; Linux `$XDG_DATA_HOME/openvibely/config.env` | unset \| `~/Desktop/openvibely.env` |
| `OPENVIBELY_ENABLE_LOCAL_REPO_PATH` | Enables Local Path source mode in project setup | `1,true,yes,on,0,false,no,off` | Unset/invalid = `false`; `start.sh` exports `true` unless overridden in `.env`; desktop mode defaults to `true` unless explicitly set | `true` \| `false` |
| `OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION` | When set to any non-empty value, skips the startup migration that moves old database and repo paths from source-checkout locations into the app-data directory. Use when you have already migrated or are managing storage manually. Has no effect if `OPENVIBELY_APP_DATA_DIR` is set or if no legacy paths exist. | Any non-empty string (presence is the check) | Unset (migration runs automatically when legacy paths are detected) | unset \| `1` |
| `OPENVIBELY_CODEX_REASONING_EFFORT` | Fallback reasoning effort for Codex requests when model config does not set one | `none`, `low`, `medium`, `high`, `xhigh`, `max` (model support varies) | If unset/invalid, uses the selected model's default: `medium` for GPT-5.6, GPT-5.5, GPT-5.4, and GPT-5.4 mini; `high` for most older Codex models | `medium` \| `none` |
| `CLAUDE_CODE_MAX_OUTPUT_TOKENS` | Overrides the per-call output token budget for Anthropic API/OAuth calls. When set to a positive integer, the value is applied up to the model's hard upper limit. | Positive integer string | Defaults to `64 000` for Claude Opus 5, Sonnet 5, Fable 5, Mythos 5, and Opus 4.6–4.8; `32 000` for Sonnet 4.6 and most Claude 4/3.7 models; `8 192` for Claude 3.5; and `4 096` for older Claude 3 Opus/Haiku models. Model-specific upper limits still apply. | unset \| `32000` |

## Application Updates

Build artifact identity is compiled into official releases and cannot be changed by environment variables. Source builds perform a metric-only daily check and never show or apply updates.

| Variable | Purpose | Allowed values | Default behavior |
|---|---|---|---|
| `OPENVIBELY_UPDATE_SERVICE_URL` | Release-check service origin | HTTPS origin, or loopback HTTP origin for development | `https://openvibely.ai` |
| `OPENVIBELY_UPDATE_CHANNEL` | Release channel sent with checks | Non-empty channel name | `stable` |
| `OPENVIBELY_DISABLE_INSTALL_ID` | Omits the pseudonymous per-install identifier from update checks | Presence of the variable opts out, including an empty value | Unset; update checks send a random identifier that rotates every 90 days |
| `DISABLE_UPDATE_NOTIFICATIONS` | Hides idle update offers and prevents packaged download/staging/apply while retaining release checks | `1,true,yes,on,0,false,no,off` | `false`; set to `true` only when you want packaged installs to check silently without offering updates |
| `OPENVIBELY_UPDATE_PUBLIC_KEY_FILE` | Custom distribution trust roots | Path to a JSON object mapping key IDs to base64 Ed25519 public keys | Official builds use their embedded key |
| `OPENVIBELY_UPDATE_MODE` | Container replacement owner | `hosted`, `docker-agent`, `docker-manual` | Containers use `docker-manual`; non-container builds use `none` and reject container modes |
| `OPENVIBELY_HOSTED_AGENT_TOKEN` | Authenticates Hosted update coordination and health checks | Secret string | Required with `OPENVIBELY_UPDATE_MODE=hosted` |
| `OPENVIBELY_DOCKER_AGENT_URL` | Restricted host updater endpoint | `unix:///...` or HTTPS origin | Required for managed Docker apply |
| `OPENVIBELY_DOCKER_AGENT_TOKEN` | Authenticates the restricted host updater | Secret string | Required for managed Docker apply |

Packaged standalone binary and desktop builds always perform the startup/daily signed release check and show signed update offers by default. Update checks include a random 128-bit pseudonymous install identifier that is stable across days and upgrades, rotates every 90 days, and is stored only in the app-data update state rather than the application database. The hosted service stores only an HMAC of this identifier and never joins it to a user account or session. Set `OPENVIBELY_DISABLE_INSTALL_ID` (to any value, including empty) to omit the identifier and avoid generating or storing one. Set `DISABLE_UPDATE_NOTIFICATIONS=true` to hide idle update offers and prevent packaged download, staging, and installation while retaining release checks. Source checkout metric-only behavior and externally controlled Hosted/Docker behavior keep their existing defaults. An update accepted before notifications were disabled remains visible and completes recovery so changing the setting cannot abandon a drained installation.

Standalone packaged binaries update without service-manager configuration. After approval and drain, OpenVibely copies its signed executable into a detached helper, passes the original arguments and working directory over an inherited pipe, and shuts down only after the helper durably claims the operation. The helper replaces and relaunches the executable, validates the expected version through `/api/system/health`, and stops a failed successor before restoring and relaunching the backup. Durable phases let the next normal launch reconcile an interrupted replacement.

Desktop update boundary enforcement resolves `OPENVIBELY_APP_DATA_DIR`, `DATABASE_PATH`, `PROJECT_REPO_ROOT`, `OPENVIBELY_DESKTOP_CONFIG_FILE`, `OPENVIBELY_PLUGIN_ROOT`, and any custom update trust file through symlinks before staging or replacement. Every protected path must remain outside the live and backup install units. macOS replaces the complete `.app` bundle; Windows and Linux replace the signed desktop executable.

Official standalone artifacts are packaged as notarized `.zip` archives on macOS, Authenticode-signed `.zip` archives on Windows, and `.tar.gz` archives on Linux. The updater verifies the signed catalog and archive SHA-256, securely extracts the single root-level `openvibely` executable, and never publishes archive bytes as the running program.

Official release builds additionally require `OPENVIBELY_RELEASE_KEY_ID` and `OPENVIBELY_RELEASE_PUBLIC_KEY` so every binary, desktop bundle, and image embeds the Ed25519 verification trust root. macOS packaging requires `OPENVIBELY_MACOS_SIGN_IDENTITY` and `OPENVIBELY_MACOS_NOTARY_PROFILE`; desktop bundles are hardened-runtime signed, notarized, stapled, and verified, while raw server binaries are Developer ID signed and distributed in notarized archives. Windows packaging requires executable `OPENVIBELY_WINDOWS_SIGN_COMMAND` and `OPENVIBELY_WINDOWS_VERIFY_COMMAND` hooks; the signing hook must Authenticode sign and timestamp each executable, and the verification hook must fail if either requirement is absent. Credentials are supplied externally and are never generated by the release scripts. OS signing supplements rather than replaces Ed25519 catalog and SHA-256 verification.

Never mount the Docker socket into OpenVibely. Without a host updater agent, use `Prepare for update` on Alerts, wait for `Safe to restart`, then recreate the container externally.

## App Authentication (Built-in Login)

| Variable | Purpose | Required? | Allowed values | Default behavior (from code) | Security notes | Example (safe placeholder) |
|---|---|---|---|---|---|---|
| `AUTH_ENABLED` | Explicitly enable/disable built-in login middleware | Optional (explicit toggle) | `1,true,yes,on,0,false,no,off` | If unset/invalid, inferred from credentials: enabled only when both `AUTH_USERNAME` and `AUTH_PASSWORD` are set | Prefer explicit `true`/`false` in production to avoid accidental enablement from partial env changes | `true` |
| `AUTH_USERNAME` | Login username for built-in auth | Required when auth is enabled | String | Empty by default; if set with `AUTH_PASSWORD` and `AUTH_ENABLED` unset, auth is inferred enabled | Keep non-sensitive but unique; avoid obvious defaults like `admin` in internet-exposed deployments | `openvibely_admin` |
| `AUTH_PASSWORD` | Login password for built-in auth | Required when auth is enabled | String | Empty by default | Treat as a secret; generate a long random password and store in secret manager/env file with restricted permissions | `__REPLACE_WITH_LONG_RANDOM_PASSWORD__` |
| `AUTH_SESSION_SECRET` | HMAC signing secret for `ov_session` cookie tokens | Required when auth is enabled | Local auth accepts the existing string format. Hosted SSO requires exactly 43 canonical unpadded-base64url characters decoding to 32 bytes | Empty by default | Treat as high-sensitivity secret. Hosted mode decodes the 32 bytes once and uses them directly as HMAC key material; rotation invalidates workspace sessions | Generate as documented below |
| `AUTH_SESSION_TTL` | Session lifetime for signed auth cookies | Optional | Go duration string (`24h`, `12h`, `30m`, including subsecond values) | `24h`; invalid/non-positive values fall back to `24h` | Token verification preserves the configured duration precision; keep it long enough for usability but short enough for risk tolerance | `24h` |

Runtime enforcement from code:
- If local auth resolves enabled and `AUTH_USERNAME`/`AUTH_PASSWORD`/`AUTH_SESSION_SECRET` is missing, startup fails with `invalid auth configuration: ...`.
- If `AUTH_ENABLED` is unset, setting both `AUTH_USERNAME` and `AUTH_PASSWORD` implicitly enables local auth.

## Hosted Workspace SSO

| Variable | Purpose | Required? | Exact contract |
|---|---|---|---|
| `OPENVIBELY_HOSTED_SSO_ENABLED` | Selects hosted control-plane SSO | Optional, server mode only | Unset means not requested. After trimming and case folding, only `true` or `false` is accepted. A present empty value or values such as `1`, `yes`, and `on` fail startup. `true` is rejected in desktop mode and takes precedence over local credentials in server mode. |
| `OPENVIBELY_HOSTED_CONTROL_URL` | Hosted identity-provider origin | Required when hosted SSO is enabled | An already-canonical HTTPS origin such as `https://openvibely.ai`; no path, trailing slash, userinfo, query, fragment, or explicit default port. |
| `OPENVIBELY_HOSTED_INSTANCE_ID` | Immutable provider registration/client ID | Required when hosted SSO is enabled | 1-128 valid UTF-8 bytes, no ASCII controls, and no leading or trailing whitespace. |
| `APP_BASE_URL` | Canonical workspace origin and callback source | Required when hosted SSO is enabled | An already-canonical HTTPS origin such as `https://alice.openvibely.ai`. Hosted SSO derives exactly `<APP_BASE_URL>/auth/sso/callback`. The same injected value is used by model/channel OAuth, absolute URL generation, cookie security, and logout-origin checks. |
| `AUTH_SESSION_SECRET` | Workspace session and browser-binding HMAC key | Required when hosted SSO is enabled | Exactly the canonical unpadded-base64url encoding of 32 CSPRNG bytes: 43 ASCII characters. This stronger format applies to hosted SSO in every environment and does not change local-auth compatibility. |

Generate a manual development secret without printing it in application logs:

```sh
openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n'
```

Hosted mode creates a host-only `ov_session` in the exact `<payload_b64>.<signature_b64>` format with a fixed one-hour lifetime. The temporary host-only `ov_sso_browser` uses exact `<nonce_b64>.<signature_b64>` framing, `Path=/auth/sso`, and a ten-minute lifetime. The decoded key is used directly: session HMAC input is `"openvibely/session/v1\x00" || canonical-session-payload`, and browser-binding HMAC input is `"openvibely/sso-browser/v1\x00" || nonce_b64`, where `\x00` denotes one raw NUL byte. The signatures use separate purpose domains; no KDF, subkey derivation, or extra key hashing is applied. `AUTH_SESSION_TTL` remains local-auth-only.

HTTPS is required by default. Plain HTTP is accepted only when `ENVIRONMENT` was explicitly supplied as `development` and each HTTP origin uses exactly `localhost` or a parsed loopback IP. An unset environment that merely defaults to development does not qualify; neither do `0.0.0.0`, `::`, private addresses, or `*.localhost`.

Pending SSO transactions are bounded and process-local. Run exactly one OpenVibely application replica per hosted workspace. A restart safely loses an in-progress login and presents a manual restart link; shared transaction storage is required before enabling multiple replicas. Workspace logout clears only the workspace cookie and does not claim to sign out the hosted control-plane account.

## OAuth and Deployment Variables

| Variable | Purpose | Required? | Allowed values | Default behavior (from code) | Security notes | Examples (local / VPS-public) |
|---|---|---|---|---|---|---|
| `APP_BASE_URL` | External origin used for absolute callback/redirect URLs | Required for hosted OAuth and hosted workspace SSO | Outside hosted SSO: absolute `http://` or `https://` URL with no query/fragment/userinfo. Hosted SSO: exact canonical origin rules above | Outside hosted SSO, unset/invalid values preserve current forwarded/request-host fallback. Hosted SSO rejects missing, invalid, or noncanonical values at startup and never falls back to request headers | Not a secret, but must exactly match provider registration in hosted SSO | unset \| `https://app.example.com` |
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

## Integration and Provider Bootstrap Variables

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

## Git/GitHub Clone SSL Variables

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

**Security note**: The `GIT_SSL_NO_VERIFY=true` fallback is intended to keep self-hosted setups usable when CA bundles are missing. For production with sensitive repositories, install a valid CA bundle and set `GIT_SSL_CAINFO` explicitly.

## OAuth Callback Troubleshooting

If OAuth opens or returns to `localhost` on a remote deployment:

1. Set `APP_BASE_URL` to your public app origin (for example `https://app.example.com`).
2. Leave `OAUTH_REDIRECT_MODE=auto` (recommended) or set `OAUTH_REDIRECT_MODE=hosted`.
3. If you set `hosted`, ensure `APP_BASE_URL` is set and valid, otherwise `/models/:id/oauth/initiate` fails with `OAUTH_REDIRECT_MODE=hosted requires APP_BASE_URL`.
4. For OpenAI hosted callbacks, ensure your OAuth app allows `<APP_BASE_URL>/auth/callback`; for Anthropic hosted callbacks, allow `<APP_BASE_URL>/callback`.
5. If you intentionally want localhost callback URIs on a remote box, set `OAUTH_REDIRECT_MODE=localhost_manual` and finish by pasting the callback URL in the Models UI.
6. If `APP_BASE_URL` includes query, fragment, userinfo, or non-http(s), it is treated as invalid and ignored; fix it to an absolute `http(s)` origin.

## Deployment Examples

```bash
# Local development (default localhost callback behavior)
PORT=3001
# DATABASE_PATH defaults to ~/.openvibely/openvibely.db
# PROJECT_REPO_ROOT defaults to ~/.openvibely/repos
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

## OAuth by Mode

- **Server mode (VPS)**: Set `APP_BASE_URL` to your public origin. OAuth callbacks route to your hostname.
- **Desktop mode**: OAuth defaults to localhost callback flow. `APP_BASE_URL` is typically unset; the backend binds to `127.0.0.1` with an ephemeral port and OAuth providers redirect back to localhost.
- **Troubleshooting**: If a provider rejects localhost callbacks, set `OAUTH_REDIRECT_MODE=localhost_manual` and paste the callback URL manually.
