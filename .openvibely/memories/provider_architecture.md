---
name: provider_architecture
type: project
created: 2026-05-09
updated: 2026-08-19
source: after_complete
source_id: b726c8898cb11f43eb8cc9dc6ba1b069:4b9fa91e0a7a63c5
confidence: high
title: Provider Architecture
---

Provider logic is isolated in adapter packages under `internal/llm`: OpenAI, Anthropic, Ollama, and OpenAI-compatible. Provider routing goes through `internal/service/provider_adapter.go` and shared contracts/normalization/streaming packages.

Normalized provider direction:
- OpenVibely is converging model-call behavior around a normalized provider request assembly pipeline.
- Lifecycle preparation precedes provider request construction for turns that need it.
- Selected memories, selected skills, task metadata, goals, follow-up metadata, attachments, and runtime tools are intended to become one normalized `AgentRequest`.
- Provider adapters consume that normalized contract instead of reinterpreting task/chat/follow-up state in ways that can drop context or tools.
- `AgentRequest.Followup=true` is authoritative even when chat history is nil/empty. OpenAI, OpenAI-compatible, Anthropic, Ollama, and mixtures must route zero-history follow-ups through Chat streaming, preserving follow-up prompt and `ChatSystemContext`.
- Initial worker tasks run lifecycle and typically carry selected-memory handle context through `ProjectInstructions`; Chat/API chat/task-thread follow-ups share `processStreamingResponse` and carry follow-up selected-memory context through `ChatSystemContext`.
- Direct utility services such as architect, backlog, collision, insights, trend, and upcoming generally use direct-style model calls and do not run task/chat lifecycle memory routing unless deliberately redesigned.

Provider and model selection:
- Provider/model selection is based on selected `models.LLMConfig`, especially `Provider`, `Model`, and `AuthMethod`; model string alone does not choose the provider.
- Normal task runs and task-thread execution starts select model config in this order: current `Task.AgentID`, project `DefaultAgentConfigID`, global default `agent_configs.is_default = 1`. Stored per-run/queued model IDs are history/accounting evidence, not immutable rerun assignment.
- Interactive Chat differs: explicit `agent_id` uses that model config, `agent_id=default` uses global default, and empty/`auto` triggers complexity/vision-based model selection.
- API Chat immediate and queued execution paths should use compact model-selection/context rows before auto-selection or prompt-context rendering, then hydrate only the selected full `LLMConfig` at provider execution.
- `Task.AgentDefinitionID` selects persona/system prompt/skills, not provider/model.
- The actual per-run model identity is stored but not shown in task-thread execution history; issue `#128` tracks exposing it.
- Model Settings normalize names and runnable model slugs. Trimmed blank names or model slugs are rejected for concrete providers; case-insensitive duplicate trimmed names are rejected. Mixture remains a virtual provider and validates/defaults its virtual identifier separately.
- OpenAI/Anthropic model CLI transports are retired. Models UI must not expose CLI auth/options; migrations normalize legacy rows and adapters reject `auth_method='cli'` at runtime.

OpenAI and Anthropic facts:
- OpenAI supports Responses API and Completions API. OpenAI Responses `SendAgentic` does Codex-style client-side history compaction for API key and OAuth flows.
- First-party OpenAI GPT-5.6 Sol/Terra/Luna are supported. `gpt-5.6-sol` is the default for unknown nonblank first-party OpenAI model selections, while blank submitted model values remain invalid for runnable configs. These models use Codex Responses Lite request shape for API-key and ChatGPT OAuth configs.
- OpenAI GPT-5.6 Fast mode is an opt-in performance-tier gap tracked as `#295`; OpenVibely currently lacks persisted service-tier setting/request plumbing.
- `gpt-5.3-codex` predates the current model-audit window and is a candidate for a future bounded support audit.
- Issues `#602` and `#609` track GPT-5.6 Cyber/Daybreak Red and Daybreak Blue alias support gaps.
- Responses Lite WebSocket state is conversation-scoped and credential/account-aware, not globally shared by model config. It serializes turns per conversation, supports compatible incremental turns with `previous_response_id`, reconnects stale sockets once, and falls back to HTTP after unrecoverable transport failure.
- Anthropic uses `ProviderAnthropic`; OAuth/API-key requests use `pkg/anthropicclient`. Anthropic/OpenAI token refresh is reactive, not background-scheduled.
- Claude Opus 5, Sonnet 5, Haiku 4.5, Fable 5, and Mythos 5 are supported Anthropic model IDs. Fable/Mythos should remain selectable but not preselected/recommended defaults; they require adaptive thinking and can return HTTP 200 refusal responses that should surface as unsuccessful/refusal results.
- Anthropic Opus 4.1 is retired; stale adapter pattern-matching for it is a future cleanup, not a new-model-support gap.
- Anthropic OAuth invalid_grant refresh failures are permanent reauthorization failures: mark `oauth_needs_reauth`, surface reauth required, and clear the flag after successful refresh.

OpenAI-compatible and Ollama facts:
- `ProviderOpenAICompatible` is a separate generic Chat Completions path for inference servers/gateways exposing `/chat/completions`; provider/model selection still comes from `LLMConfig.Provider`.
- OpenAI-compatible supports API-key or optional missing auth for local servers, base URL/transport/preset config, streaming text, provider tool calls/tool-result replay, usage normalization, and advanced extra headers/body JSON. Extra-body fields must not override protected request ownership such as `model`, `messages`, `stream`, or tool fields.
- Setup includes presets and best-effort `/models` discovery via `/models/openai-compatible/available`; discovery credentials are accepted via header, not query parameters.
- Models setup intentionally has no manual “Discover Models” button; presets are shown in the Provider dropdown and submissions normalize to backend provider `openai_compatible` with hidden `preset_slug`.
- Preset catalog includes named providers such as OpenRouter, NVIDIA NIM, Local vLLM, LM Studio, SGLang, LiteLLM, DeepInfra, Fireworks, Groq, Mistral, Cerebras, Together, Hugging Face Router, DeepSeek, Moonshot, DashScope variants, Alibaba Coding Plan, Z.AI/GLM, NovitaAI, Venice, Qianfan, Kilo Code, Arcee AI, StepFun variants, Tencent TokenHub variants, Xiaomi MiMo, Inferrs, ds4 Local, GMI Cloud, Chutes, plus Custom OpenAI-Compatible.
- Local/self-hosted OpenAI-compatible presets include Local vLLM, LM Studio, SGLang, LiteLLM, Inferrs Local, and ds4 Local. Ollama remains separate.
- Excluded/unverified candidates such as xAI, GitHub Copilot, native Bedrock/Gemini, and MiniMax should not be surfaced as generic presets or auto-normalized without a new explicit decision.
- Ollama uses `/api/chat`, defaults to `http://localhost:11434`, and currently cannot use runtime tools during task execution; issue `#264` tracks tool support for Ollama-backed agents.

Mixture of Models facts:
- `ProviderMixture` is a virtual model config stored in `agent_configs` with `provider='mixture'` and `mixture_config_json`; routing goes through `internal/service/provider_adapter.go`.
- Mixture configs select one aggregator model config plus ordered non-mixture reference model configs. Reference calls fan out first; private outputs are appended as aggregator-only context and should not stream into the user-facing transcript.
- Reference requests run without tools, normal OpenVibely system/task prompts, skill/memory/task mutation context, or public streaming output. Aggregator requests keep the normal user-visible model response path and may use runtime tools according to the concrete aggregator.
- Mixture runtime emits compact `mixture_progress` events on the existing live channel; Chat/task-thread listeners show transient progress and hide it once aggregator output starts or stream terminates.
- Slot eligibility is centralized in `LLMConfig.IsCallableMixtureSlot`: reject recursive, duplicate, missing, retired CLI-auth, hidden/internal/unknown, and non-callable rows.
- Mixture reference requests call `llmcontracts.WithoutRuntimeTools(ctx)`. Aggregator requests keep authorized runtime tools; mixture capability checks resolve the concrete aggregator.
- Runtime-incapable aggregators such as Ollama receive no inherited/default native runtime definitions and no textual action fallback.
- Model CRUD validation should reject invalid mixture slots, and deleting a model used by a mixture should be blocked with affected mixture names.
- The Models UI treats Mixture as a no-credential virtual provider with aggregator/reference pickers, ordered references, enabled toggle, numeric controls, edit hydration, cost-warning copy, stable virtual model value, and inline validation errors. The selected aggregator may also be one reference for self-review/second-pass use; duplicate references remain invalid.

OAuth account facts:
- OAuth access/refresh tokens are stored per `agent_configs` row, so two Anthropic model configs can have different OAuth freshness/reauth states even for the same account.
- Editing a model config should update the existing row in place and preserve per-row OAuth token/reauth state unless auth/provider changes require clearing provider-specific credentials.
- Durable direction is a provider-account token table with model configs referencing shared provider-account credentials; Anthropic needs a reliable account identity source before this can be keyed.
- Provider 401 recovery reloads the model config from DB and may refresh/persist rotated tokens; it does not reread OAuth token material from disk, keychain, or environment.
- Chat model discovery currently omits the connected/expired/not-connected OAuth status shown on model cards; issue `#695` tracks compact status exposure.

Provider-native and runtime tools:
- Provider-native web search/fetch is executed by providers, not local web tooling. OpenAI sends `web_search`; Anthropic sends versioned raw-tool types such as `web_search_20250305` and `web_fetch_20250910`.
- Runtime tools are request-scoped, provider-generic, and carried through the LLM service/provider adapter path. Tool definitions carry read/write access classification.
- Runtime-tool-capable providers currently include OpenAI API/OAuth, Anthropic API/OAuth, and OpenAI-compatible API-key Chat Completions. Unsupported providers/transports receive no unusable native tool definitions and no legacy marker fallback.
- Provider-local built-in tool execution for file/shell tools is duplicated across OpenAI and Anthropic clients; issue `#687` tracks consolidation.
- Runtime-tool prompt guidance names are extracted through shared `RuntimeTools.DefinitionNames()` in `internal/llm/contracts`; OpenAI, Anthropic, and OpenAI-compatible adapters should use this provider-neutral helper rather than adapter-local name-list conversions.
- Anthropic `execBash` default timeout is 10 minutes only when no positive timeout is provided; any positive explicit timeout is preserved.
- Memory tool exposure is a request/tool-profile decision, not a global provider-adapter default.
- Anthropic has a provider-side name-combination collision for `skill_view`, `skills_list`, and `skill_manage`; the adapter aliases canonical internal `skills_list` to wire name `skill_list` and translates back locally.
- `read_file` runtime executors emit `decimalLineNumber\t<source bytes>`. Keep this compact format so indentation after the tab is preserved and rendered/copied/model-facing content does not diverge.
- Scoped file runtime tools select the longest configured scope directory prefix, including nested scopes, then strip that matched scope before joining to the selected scope root. Preserve unprefixed fallback, scope permissions, traversal checks, and symlink containment.
- External HTTP retry policy is centralized in `internal/httpretry`; streaming logical-turn retries reconstruct provider requests from canonical turn state, honor cancellation/backoff/retry-after behavior, and use append-only visible output unless a caller adds rollback/reset behavior.
- Default request deadlines for primary HTTP model clients are 10 minutes for Anthropic, OpenAI, and Ollama; lifecycle `after_complete` hooks also use a 10-minute deadline.
- Known streaming-timeout defects are tracked in `#54` and `#68`; hidden-thinking/tool-argument deltas should reset inactivity without requiring rendered output.
- Lifecycle `OperationDirect` calls are hook steps, not coding turns. Direct providers must preserve the hook agent's own prompt through `ProjectInstructions` while omitting shared coding-agent framing.
