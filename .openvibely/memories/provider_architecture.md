---
name: provider_architecture
type: project
created: 2026-05-09
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Provider Architecture
---

Provider implementations live under `internal/llm`; routing and normalization are shared through the service provider adapter. Supported transports are OpenAI Responses/Completions, Anthropic, generic OpenAI-compatible Chat Completions, Ollama `/api/chat`, and Mixture. CLI auth for OpenAI/Anthropic is retired. Realtime voice and Chat/Task image generation remain outside the current text/task contract.

- Provider selection uses `LLMConfig.Provider`, `Model`, and `AuthMethod`, not model string alone. Normal tasks resolve current task assignment, then project default, then global default; persisted run/queue model IDs are history/accounting evidence, not immutable rerun assignment. `Task.AgentDefinitionID` selects persona; task `agent_id` selects provider/model.
- A normalized `AgentRequest` carries lifecycle preparation, selected memory/skills, task metadata, goals, follow-up context, attachments, and runtime tools. `Followup=true` is authoritative even with empty history. Direct utility calls (Insights, architect/backlog/collision and similar) do not silently run task/chat memory lifecycle or coding-agent framing.
- OpenAI-compatible transport is distinct from Ollama and supports configurable endpoints, API-key or optional local-server auth, streaming, tools, usage normalization, and extra headers/body JSON with protected fields. Ollama accepts exact custom model names, supports configurable context length, and does not support runtime tools. Discovery is read-only and best-effort; browser requests must be bounded, cancellable, and fenced from stale responses.
- Mixture fans out ordered non-mixture reference calls and gives private outputs only to the aggregator. Only the aggregator response is user-visible and tool-capable. References receive no tools, coding prompt, task mutation context, or public output. Invalid, recursive, duplicate, hidden, or non-callable slots are rejected.
- Standard OpenAI/Anthropic OAuth credentials belong to explicit `oauth_connections`, separate from model settings. Models link to a connection; new models default private and sharing requires explicit linking. API-key models and custom compatible OAuth remain independent. Deleting a model preserves referenced connections; linked connections cannot be deleted. Account disconnect affects all linked models.
- Migration may merge same-provider connections only on exact matching non-empty refresh-token evidence. Strong same-user/account UUID evidence can authorize adoption after live profile resolution; display names, organization identity, emails, model names, access tokens, and provider account IDs alone cannot. Ambiguous historical rows require at most one account-level choice, not repetitive per-model edits.
- Refresh uses connection-level singleflight and durable leases. Callback, refresh, profile/account identity, reauthentication, and Analytics writes are revision-fenced and verify model linkage where applicable. Linked callers reload their own model settings. Safe provider display names may be shown, but credentials, emails, account IDs, profile payloads, and principal hashes never reach user-facing UI.
- Runtime tools are request-scoped and provider-generic, with shared read/write authorization at both advertisement and execution. Unsupported transports receive neither unusable schemas nor prose-marker fallbacks. Provider-native web search/fetch is provider-executed. `request_user_input` is Web Orchestrate Chat-only.
- Retry policy is centralized; streaming retries honor cancellation/backoff/Retry-After and preserve visible output unless reset. Context budgeting accounts for provider-visible instructions, tools, attachments, history, native state, pending input, reserved output, and safety margin. Preflights are transport-specific; compaction preserves provider/transport affinity and opaque checkpoints are restored only for compatible configurations.
- Context observability must be content-free and report budget/strategy and typed failure context. Recompute request budgets only after request-visible mutations; fit checks and observability must correspond to the same refreshed request.
- Models and Automation selectors should expose only safe provider, endpoint, and OAuth account context needed to distinguish configurations. Healthy OAuth cards remain status-only; reconnect actions target the persisted connection and repair all models linked to it.

Usage snapshot and privacy behavior is covered in `usage_analytics.md`; task/chat lifecycle details belong in `chat_thread_system.md`.
