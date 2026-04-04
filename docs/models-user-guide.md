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
   - Optional runtime settings (`Max Tokens`, `Temperature`, etc.)
4. Click `Create`.

## Provider-Specific Options

### Anthropic

- Supports API key or OAuth-based flows.
- OAuth can use API/web flow or CLI flow, depending on connection method.

### OpenAI

- Auth options include API key and OAuth.
- For supported models, `Reasoning Effort` is available.

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
