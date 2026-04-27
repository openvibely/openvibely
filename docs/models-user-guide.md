# Models User Guide

Use `/models` to configure LLM providers and defaults.

## What This Page Is For

Models are the execution engines used by Chat and Tasks.

Why this matters:

- No configured models means task/chat execution is blocked.
- Model choice affects quality, speed, cost, and tool behavior.
- Default model determines what runs when a task/chat turn does not choose one explicitly.

## Add a Model

1. Open `/models`.
2. Click `+ Add Model`.
3. Fill:
   - `Name`
   - `Provider` (`Anthropic`, `OpenAI`, or `Ollama`)
   - `Authentication` / `Connection Method` (provider-dependent)
   - `Model`
   - Optional runtime settings (`Temperature`, worker pool settings, etc.)
4. Click `Create`.

## Supported Models

### Anthropic

| Model | Effort Options | Notes |
|---|---|---|
| Claude Opus 4.7 (`claude-opus-4-7`) | low / medium / high / max | 128k max output, 1M context. |
| Claude Sonnet 4.6 (`claude-sonnet-4-6`) | low / medium / high / max | 64k max output, 1M context. |
| Claude Sonnet 4.5 (`claude-sonnet-4-5-20250929`) | low / medium / high / max | Legacy. |
| Claude Haiku 4.5 (`claude-haiku-4-5-20251001`) | none | Legacy. |
| Claude Opus 4.6 (`claude-opus-4-6`) | low / medium / high / max | Legacy. |

### OpenAI (Codex)

| Model | Reasoning Efforts | Notes |
|---|---|---|
| gpt-5.5 | low / medium / high / xhigh | Codex 5.5 frontier model. |
| gpt-5.5-pro | low / medium / high / xhigh | Pro/Enterprise tier. |
| gpt-5.4 | low / medium / high / xhigh | |
| gpt-5.4-mini | low / medium / high | Smaller, faster variant. |
| gpt-5.3-codex | low / medium / high / xhigh | Legacy. |
| gpt-5.3-codex-spark | low / medium / high | Fast research preview. |
| gpt-5.2-codex | low / medium / high / xhigh | Legacy. |
| gpt-5.1-codex-max | low / medium / high / xhigh | Legacy. |
| gpt-5.1-codex / mini | low / medium / high | Legacy. |
| gpt-5-codex / mini | low / medium / high | Legacy. |

### Output Token Caps

Output token caps are not model configuration. Codex and Claude-style workflows primarily expose model and effort controls, so OpenVibely does not show or accept output-token settings in the model dialog.

Where a low-level provider API still requires an output limit, OpenVibely chooses an internal runtime budget in the provider adapter. Existing saved `max_tokens` values are ignored by runtime code and retained only so older database rows remain readable.

## Provider-Specific Options

### Anthropic

- Supports API key or OAuth-based flows.
- OAuth can use API/web flow or CLI flow, depending on connection method.
- `Claude Effort` matches the Claude Code extension's effort selector (`low`, `medium`, `high`, `max`). Blank/legacy saved configs keep the provider default behavior.
- For Anthropic API/OAuth calls, `Claude Effort` is translated into an extended-thinking budget. For Claude CLI calls, the value is passed through as `--effort`.

### OpenAI

- Auth options include API key and OAuth.
- For supported Codex models, `Codex Reasoning Effort` is available.

### Ollama

- Set `Base URL` (default `http://localhost:11434`).
- Use a listed model or enter `Custom Model Name`.

## Default Model Behavior

- First configured model becomes default automatically when no default exists.
- You can set another model as default from the card menu.
- Deleting a default model prompts reassignment when needed.
- Deleting the last remaining model is allowed.

Why default matters:

- It is the fallback model for most task/chat actions.
- Keeping a sensible default prevents accidental “no model selected” friction.

## OAuth Connection Status

OAuth-based models show connection status on cards:

- `Connected`
- `Token Expired`
- `Not Connected`

Use `Connect with OAuth` / `Re-authorize` from the model card.

### Hosted/VPS OAuth Setup

For remote deployments (for example `https://dubee.org`), set `APP_BASE_URL` in your environment.

- Example: `APP_BASE_URL=https://dubee.org`
- Do not set this to `localhost` on hosted servers.
- If `APP_BASE_URL` is not set, OAuth defaults to localhost callback mode (intended for local development).

Hosted callback paths in normal hosted mode:
- Anthropic: `/callback`
- OpenAI: `/auth/callback`

If provider OAuth apps only accept localhost redirect URIs, use manual localhost mode on VPS:
- Set `OAUTH_REDIRECT_MODE=localhost_manual`
- Start OAuth from `/models`
- After provider redirects to failed localhost URL, copy that full URL
- Paste it into the "OAuth localhost fallback" box on `/models` and click `Complete OAuth`

This mode keeps localhost redirect URIs provider-compatible while still completing token exchange on the VPS.

## Worker Pool Settings Per Model

In the model modal:

- `Max Workers` (0 means use global worker pool)
- `Timeout (seconds)` (0 means no override)

Per-model utilization is visible in `/workers`.

Why use model worker limits:

- Protect expensive/slower models from overuse.
- Prevent one model from consuming all worker capacity.

## Auto-Start Option

`Auto-start created tasks` makes tasks created with that model move directly toward execution.

Use this when you want “create task -> run immediately” behavior by default for that model.

## Important UX Guardrail

If no models are configured, Chat and Task execution actions are blocked and show a toast linking to `/models`.
