# Models User Guide

Use `/models` to configure LLM providers and defaults.

## What This Page Is For

Models are the execution engines used by Chat and Tasks.

Why this matters:

- No configured models means task/chat execution is blocked.
- Model choice affects quality, speed, cost, and tool behavior.
- Default model determines what runs when a task/chat turn does not choose one explicitly.

## Recommended Setup

**Start with Codex `gpt-5.6-sol` at `medium` reasoning effort.** This is the default model and the setup OpenVibely is tuned against. It is the flagship GPT-5.6 tier for complex coding and professional work, while `medium` is the model's balanced default. For most teams this is the only model config they need.

For lower cost, use `gpt-5.6-terra`; for efficient high-volume work, use `gpt-5.6-luna`. Increase effort to `high`, `xhigh`, or `max` only when representative tasks show a useful quality gain. Use `none` or `low` for latency-sensitive work.

**Secondary: Claude Opus** for teams that prefer Anthropic. Opus at `medium` effort performs comparably for most coding tasks at a lower latency and cost than higher effort levels.

**Avoid `low` effort for task execution.** `low` is fast but the model skips reasoning steps that matter for correctness. It is fine for lightweight utility tasks but not recommended as a default.

## Add a Model

1. Open `/models`.
2. Click `+ Add Model`.
3. Fill:
   - `Name`
   - `Provider` (`Anthropic`, `OpenAI`, `Ollama`, `Mixture of Models`, or an OpenAI-compatible preset such as `OpenRouter`, `Groq`, `LM Studio`, or `Custom OpenAI-Compatible`)
   - `Authentication` / `Connection Method` (provider-dependent)
   - `Model`
   - Optional runtime settings (`Temperature`, worker pool settings, etc.)
4. Click `Create`.

## Supported Models

### Anthropic

| Model | Effort Options | Notes |
|---|---|---|
| Claude Opus 5 (`claude-opus-5`) | low / medium / high / xhigh / max | Latest Opus generation. 128k max output, 1M context. |
| Claude Sonnet 5 (`claude-sonnet-5`) | low / medium / high / xhigh / max | Latest Sonnet generation. 128k max output, 1M context. |
| Claude Fable 5.1 (`claude-fable-5-1`) | low / medium / high / xhigh / max | Latest Fable generation. 128k max output, 1M context; adaptive thinking is always on. |
| Claude Mythos 5.1 (`claude-mythos-5-1`) | low / medium / high / xhigh / max | Limited-availability Project Glasswing model. 128k max output, 1M context; adaptive thinking is always on. Forced `tool_choice` values `any` and `tool` are unsupported. Requires 30-day data retention unless Anthropic authorizes otherwise. |
| Claude Fable 5 (`claude-fable-5`) | low / medium / high / xhigh / max | 128k max output, 1M context; adaptive thinking is always on. |
| Claude Mythos 5 (`claude-mythos-5`) | low / medium / high / xhigh / max | Limited-availability model. 128k max output, 1M context; adaptive thinking is always on. |
| Claude Opus 4.8 (`claude-opus-4-8`) | low / medium / high / xhigh / max | 128k max output, 1M context. |
| Claude Opus 4.7 (`claude-opus-4-7`) | low / medium / high / xhigh / max | 128k max output, 1M context. |
| Claude Sonnet 4.6 (`claude-sonnet-4-6`) | low / medium / high / max | 64k max output, 1M context. |
| Claude Sonnet 4.5 (`claude-sonnet-4-5-20250929`) | none | Supports manual extended thinking, but not the newer effort parameter. Legacy. |
| Claude Haiku 4.5 (`claude-haiku-4-5-20251001`) | none | No reasoning effort support. Legacy. |
| Claude Opus 4.6 (`claude-opus-4-6`) | low / medium / high / max | Legacy. |

### OpenAI (Codex)

| Model | Reasoning Efforts | Notes |
|---|---|---|
| gpt-5.6-sol | none / low / medium / high / xhigh / max | Default flagship tier. 1.05M context, 128k max output. |
| gpt-5.6-terra | none / low / medium / high / xhigh / max | Balances intelligence and cost. 1.05M context, 128k max output. |
| gpt-5.6-luna | none / low / medium / high / xhigh / max | Efficient high-volume tier. 1.05M context, 128k max output. |
| gpt-5.5 | low / medium / high / xhigh | Codex 5.5 frontier model. |
| gpt-5.5-pro | low / medium / high / xhigh | Pro/Enterprise tier. |
| gpt-5.4 | low / medium / high / xhigh | |
| gpt-5.4-mini | low / medium / high / xhigh | Smaller, faster variant. |
| gpt-5.3-codex | low / medium / high / xhigh | Legacy. |
| gpt-5.3-codex-spark | low / medium / high / xhigh | Fast research preview. |
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
- For supported models, `Claude Effort` (`low`, `medium`, `high`, `xhigh`, `max`; availability varies by model) is sent to API/OAuth calls as `output_config.effort`. The selected value is validated against the model before it is saved and again before a request is sent.
- Effort and thinking mode are separate controls. Claude 5 models use adaptive thinking; older models may use manual extended thinking budgets. Blank or unsupported effort values are omitted so the provider default applies.

### OpenAI

- Auth options include API key and OAuth.
- For supported Codex models, `Codex Reasoning Effort` is available. GPT-5.6 supports `none`, `low`, `medium`, `high`, `xhigh`, and `max`; its default is `medium`.
- First-party OpenAI configs use OpenVibely's OpenAI/Codex provider path, not the generic OpenAI-compatible Chat Completions adapter.

### OpenAI-Compatible Chat Completions

- Provider choices in the UI include OpenRouter, NVIDIA NIM, Local vLLM, LM Studio, SGLang, LiteLLM, DeepInfra, Fireworks, Groq, Mistral, Cerebras, Together, Hugging Face Router, DeepSeek, Moonshot, DashScope, DashScope Intl, Alibaba Coding Plan, Z.AI / GLM, NovitaAI, Venice, Qianfan, Kilo Code, Arcee AI, StepFun, StepFun Step Plan, GMI Cloud, Chutes, Tencent TokenHub, Tencent TokenHub Intl, Xiaomi MiMo, Inferrs Local, ds4 Local, and Custom OpenAI-Compatible.
- Each non-Custom OpenAI-compatible preset sets its default base URL and auto-loads available models when selected. If you enter an API key, discovery retries with the key in a request header.
- Local/self-hosted presets such as `Local vLLM`, `LM Studio`, `SGLang`, `LiteLLM`, `Inferrs Local`, and `ds4 Local` allow blank API keys. `Custom OpenAI-Compatible` is manual-entry oriented; enter the base URL and exact model ID expected by your server or gateway.
- API keys are sent in headers, not URL parameters. Local/self-hosted servers may allow the API key field to remain blank.
- OpenAI-compatible configs call `base_url + /chat/completions` and store the exact model ID after trimming whitespace.

### Ollama

- Set `Base URL` (default `http://localhost:11434`).
- Use a listed model or enter `Custom Model Name`.

### Mixture of Models

- A Mixture of Models is a virtual model config composed from existing non-mixture model configs.
- Choose one `Aggregator Model` and one or more `Reference Models` in the model dialog. The same model can be both the aggregator and one reference if you want it to provide advisory output before its final answer.
- On each turn, reference models run first as private advisory calls with tools disabled. Their outputs are not shown as separate chat/task messages.
- The aggregator then runs as the acting model with normal OpenVibely behavior, including tools, streaming, attachments, task updates, and channel replies.
- The dialog shows the cost warning `This mixture calls N reference models plus 1 aggregator model per turn.`
- You cannot delete a model config while a mixture uses it as an aggregator or reference; remove it from the mixture first.

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
- `Inactivity timeout (seconds)` (resets on model and tool activity; 0 uses the 30-minute default)

Per-model utilization is visible in `/workers`.

Why use model worker limits:

- Protect expensive/slower models from overuse.
- Prevent one model from consuming all worker capacity.

## Auto-Start Option

`Auto-start created tasks` makes tasks created with that model move directly toward execution.

Use this when you want “create task -> run immediately” behavior by default for that model.

## Important UX Guardrail

If no models are configured, Chat and Task execution actions are blocked and show a toast linking to `/models`.
