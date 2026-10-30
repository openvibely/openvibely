# Transcript persistence map

Scope: current repository implementation, traced from database writers/readers through provider adapters, compaction/recovery, and display delivery. This is a source audit, not a live memory profile or a schema proposal. No production persistence changes are included.

Follow-up: the requested implementation scope now includes display rendering and catch-up in the same project. See [shared-store feasibility](shared-transcript-store-feasibility.md) for the one-new-table design check and planned removal of both old replay and output-chunk tables.

## Findings that change the migration plan

- A replay row is **not necessarily one message**. Its `transcript_json` can contain an entire Responses conversation or an execution-local message array. Multiple replay rows can represent steering segments.
- There is a second full-history container in `chat_compaction_checkpoints.history_json`, plus separate native compaction and provider session state columns.
- Responses history has **replace-prefix semantics**; Anthropic and Chat Completions replay generally has **append-execution semantics**. Flattening everything by execution/sequence would duplicate context.
- Saving is not exclusively through `SaveConversationReplay`: local steering writes replay inside its own transaction. The steering recovery wrapper also marshals the history separately.
- Some existing items change: Anthropic accumulates parallel results in a single user message; skill-installation approval patches an earlier saved tool exchange.
- Display output is a separate representation. It is also used as a fallback for model history and provider-switch recovery, so removing it requires more than a UI change.

## 1. Durable records

| Store / identity | Payload | Write and read owners |
|---|---|---|
| `execution_replay_messages`, primary key `(execution_id, sequence)` | `user_content`, `assistant_content`, `reasoning_content`, `transcript_json` | `SaveConversationReplay`, `ReplaceReasoningReplay`, internal `replaceExecutionReplayMessages`; reader `ReplayMessagesByExecutionIDs` in [execution_repo.go](../internal/persistence/sqlite/execution_repo.go). Also direct transactional writers below. |
| `executions`, execution ID | `prompt_sent`, `output`, `reasoning_content`, status/error and context-boundary metadata | Creation / execution completion; reasoning mirror updated by replay replacement. Read by history queries, fallback converters and display handlers. |
| `chat_compaction_checkpoints`, primary key `(scope_type, scope_id)` | `history_json`, `summary`, `strategy`, `source_execution_id`, model/compatibility identities | `UpsertChatCompactionCheckpoint`, `SaveInTurnCompactionCheckpoint`, `GetChatCompactionCheckpoint` in execution repository. Despite the name, scopes include task threads and project chat. |
| Same checkpoint row | `provider_state_json` | Provider-native replacement context; service native checkpoint persistence and restore. |
| Same checkpoint row | `provider_session_state_json` | Independent provider conversational settings; `UpsertChatProviderSessionState`, read with checkpoint. Not the same as native compaction. |
| `execution_output_chunks`, execution ID + monotonically increasing row ID | Display-text `output` chunks | Stream writer `AppendOutput`; terminal folding/replacement in execution repository; `ReadOutputAfter` and full-output projections read it. No stored byte positions. |
| `openai_async_tool_calls`, execution/call identity | Tool arguments JSON, result, delivery/run status | Separate durable tool delivery ledger, not a conversation snapshot; [repository](../internal/persistence/sqlite/openai_async_tool_call_repo.go) and OpenAI adapter callbacks. |

Schema sources: migrations [119](../internal/persistence/sqlite/database/migrations/119_execution_replay_messages.sql), [120](../internal/persistence/sqlite/database/migrations/120_execution_replay_transcripts.sql), [181](../internal/persistence/sqlite/database/migrations/181_chat_compaction_checkpoints.sql), [182](../internal/persistence/sqlite/database/migrations/182_chat_compaction_provider_state.sql), [188](../internal/persistence/sqlite/database/migrations/188_context_budget_identities.sql), [196](../internal/persistence/sqlite/database/migrations/196_chat_provider_session_state.sql). Model definitions: [Execution](../internal/models/execution.go), [ChatCompactionCheckpoint](../internal/models/chat_compaction_checkpoint.go).

`SaveConversationReplay` marshals its supplied value, converts the bytes to a string, sets `TranscriptJSON`, then calls `ReplaceReasoningReplay`. That transaction updates `executions.reasoning_content`, deletes this execution's old replay rows and inserts the replacement rows. It returns the encoded string after success. The `sequence` today orders replay segments, not individual tool/message objects.

## 2. Provider save/load paths

### OpenAI Responses

- [OpenAI adapter](../internal/llm/openai/adapter.go) supplies `responsesHistoryRecorder` as `OnHistory` for task/chat Responses routes.
- [Agentic client](../pkg/openai_client/agentic.go), `SendAgentic` / `persistHistory`, calls it before model turns, after response items, during tool-result completion, after tools, and at the final loop checkpoint. `InitialInputCommit` / steering commits can run alongside it.
- [Recorder](../internal/llm/openai/conversation_replay.go) writes `{"responses_input":[...]}` through `SaveConversationReplay` and calls `RecordResponsesRecovery` with the returned JSON. Items include multimodal input, tool calls/results, reasoning/encrypted state and possibly native compaction items—not only visible messages.
- `buildClientHistory` in the adapter reads the envelope as a complete replacement of earlier accumulated input. Completions arrays are appended instead; text-only replay continuations can follow a full snapshot. `RetainedUserMessage` is another input path.
- [EffectiveReplayHistory](../internal/llm/openai/replay_budget.go) selects the newest complete Responses replay before conversion/budgeting. It operates **after database hydration**, so it cannot prevent those earlier read allocations.

### OpenAI Chat Completions

- The same OpenAI adapter has Completions routes, with final/error persistence through `saveCompletionsHistory` → `saveConversation` → `SaveConversationReplay` in [conversation_replay.go](../internal/llm/openai/conversation_replay.go).
- Unlike the compatible adapter, these routes do not supply the compatible `OnReplay` checkpoint saver. Do not assume their save schedules are identical.
- The saved payload is the client's `LastCompletionsTranscript()` array. [Client](../pkg/openai_client/completions.go) distinguishes the current execution transcript from the full `completionsHistory` used for requests/error recovery.
- `buildCompletionsHistory` accepts both Completions arrays and Responses envelopes, with prompt/output fallback for records without structured replay.

### Anthropic

- [Adapter](../internal/llm/anthropic/adapter.go), `Call`, installs a replay saver unless this is a raw-direct/lifecycle-hook call. Task, chat and streaming routes pass it as `OnReplaySave`.
- [Client](../pkg/anthropic_client/agentic.go) maintains a current-execution `replay` array separately from active request `messages`. It saves after assistant content and intermediate tool completions. Parallel tool results **update the same replay message's `ContentBlocks`** as results arrive; boundary steering can modify that message again.
- The envelope is `{"anthropic_execution_environment":"...","messages":[...]}`. Message `ContentBlocks` is structured JSON and can contain thinking/signatures, images, tool use/results and compaction blocks.
- Client `OnReplay` returns final replay to the adapter; the deferred save adds final assistant-output metadata and stores the execution environment. Checkpoint and final saves can encode overlapping data.
- `buildRecoveryHistory` appends execution-local structured messages, preserving environment changes. Failed/running execution output can supply a missing partial assistant answer. Empty/legacy environment-only records fall back to prompt/output.

### OpenAI-compatible and Ollama

- [Compatible adapter](../internal/llm/openai_compatible/adapter.go) supplies `replaySaver` through `OnReplay` on task/chat/streaming routes. [Completions client](../pkg/openai_client/completions.go) saves assistant messages and intermediate tool results, including image payloads.
- Final/error `persistReasoningContent` stores `LastCompletionsTranscript()` and accumulated reasoning metadata. A continuation can deliberately save `[]`, including after a failed first request, to supersede pre-compaction replay.
- Payload is a Completions message array: roles, content, reasoning, tool calls/call IDs and image content. Current-execution replay may retain full tool output even where the model request used truncated output.
- `buildClientHistory` / `decodeReplayTranscript` append arrays or use replay text fields; without replay they use execution prompt/output/reasoning.
- [Ollama adapter](../internal/llm/ollama/adapter.go) delegates to the compatible adapter with Chat Completions routing. It does not have a separate transcript store.

### Other registered routes

[Provider registration](../internal/service/provider_adapter.go), `initProviderAdapters`, also includes Mixture and Test. Mixture is virtual and re-enters the concrete provider budget boundary for its reference/aggregator models; it is not a new transcript schema. Test is a synthetic provider. Raw/lifecycle operations can bypass normal replay saving; their display execution records must not be mistaken for corrupt missing snapshots.

## 3. Shared loading, conversion and recovery

The service budget boundary in [provider_adapter.go](../internal/service/provider_adapter.go) performs:

1. `trackSteeringRecovery`.
2. `loadProviderReplayHistory`: loads replay for eligible executions already present in `req.ChatHistory` (`ReplayMessages == nil`, `ContextKind == ""`). Compatible/Ollama also hydrate reasoning. This is **not proof that every historical execution in the DB is loaded**.
3. Optional `completeInstallationTranscript`.
4. Restore the saved compaction boundary.
5. Provider-switch recovery and `normalizeProviderReplay`, then request budgeting / compaction / provider call.

Other consumers and transformations:

| Path | Reads / writes |
|---|---|
| [summary_conversion.go](../internal/service/summary_conversion.go), `normalizeProviderReplay` / `normalizeSummaryReplay` | Reads Responses, Anthropic and Completions payloads; converts copies for destination providers and summaries, repairs tool exchanges, preserves images and partial output. Can marshal new in-memory transcript JSON even without a DB write. |
| [steering_recovery.go](../internal/service/steering_recovery.go) | Ordinary Responses saves retain the successful JSON in `steeringRecovery.transcript`. `trackSteeringRecovery` also wraps steering commits with a **separate full marshal**. `restoreCurrentSteeringForRecovery` constructs replacement request history and clears already-consumed prompt/attachments/native state. |
| [Anthropic recovery](../pkg/anthropic_client/recovery.go), [Completions recovery](../pkg/openai_client/recovery.go) | Errors encode the active failed conversation into `ConversationError.MessagesJSON`; `recordProviderRecovery` transforms this into in-memory recovery executions. No independent recovery JSON database column. |
| [provider_switch_recovery.go](../internal/service/provider_switch_recovery.go) | Handles opaque native compaction, readable history reconstruction and earlier-execution recovery; respects clear-context boundaries. `portableAnthropicCheckpoint` converts native state to portable replay. |
| [provider_switch_window.go](../internal/service/provider_switch_window.go) | Selects bounded recovery input; `persistSuccessfulRecoveryWindow` checks the existing checkpoint and saves a successful `provider_recovery` history through compaction persistence. |
| [compaction_history.go](../internal/service/compaction_history.go) | Restores saved application context, retained user identity/multimodal data and summary records. `ContextKind` prevents synthetic records from being hydrated again as ordinary executions. |

## 4. Writers outside the provider checkpoint callbacks

- **Atomic steering acceptance:** [local_steering.go](../internal/persistence/sqlite/local_steering.go), `ThreadInputRepo.CommitLocalSteering`, marshals a Responses envelope and atomically updates `thread_inputs` acceptance plus replay replacement. Called from task execution steering in [llm_service.go](../internal/service/llm_service.go) and prepared/initial steering in [chat_processing.go](../internal/handler/chat_processing.go). It bypasses `SaveConversationReplay`. Keeping acceptance and transcript persistence atomic is essential.
- **Chat steering segments:** `chat_processing.go` loads current replay while continuing a turn; `persistSteeringReplayHistory` joins earlier steering segments with current replay or a text fallback, then `ReplaceReasoningReplay` replaces all rows and reasoning. Equality checks distinguish an unchanged old checkpoint from a new continuation.
- **Skill approval completion:** [skill_dependency_approval.go](../internal/service/skill_dependency_approval.go), `completeInstallationTranscript`, loads an earlier execution's replay, patches the matching tool exchange via `completeSkillToolTranscript`, and writes it back with `ReplaceReasoningReplay`. An incremental store needs item amendment/replacement semantics here.
- **In-turn summary boundary:** `SaveInTurnCompactionCheckpoint` saves the replacement history and resets source replay to an explicit `[]` in **one transaction**. This sentinel means “empty continuation”, not “missing data; use old output”.
- `UpdateReasoningContent` also exists in the concrete repository; no production call sites were found in this audit. Normal reasoning writes come from replay replacement.

## 5. Compaction and independent session snapshots

### Local summary / portable recovery history

`persistCompactionCheckpoint` → `UpsertChatCompactionCheckpoint` serializes an array of `checkpointExecution` into `history_json`. Each element embeds `Execution` plus otherwise-private `replay_messages` and `retained_user_message`. This can therefore contain transcript JSON strings **nested inside another JSON snapshot**.

Stored identity is thread scope, not just execution ID. `source_execution_id` marks history covered by the replacement. Strategies include `local_summary`, `local_summary_inflight`, `provider_recovery` and native strategies. `summary`, model identity and compatibility key are part of restore decisions.

`GetChatCompactionCheckpoint` parses `history_json` and restores private replay/user fields. `restoreCompactionCheckpoint` validates scope/context boundary and compatibility, merges only subsequent history, and handles the in-flight sentinel. A Responses continuation may already contain the summary, whereas other provider continuations must follow the separate summary. `checkpointWithinCurrentContext` also checks persisted context resets outside a bounded request history window.

### Provider-native compaction

`persistNativeCompactionCheckpoint` → `persistNativeProviderStateCheckpoint` writes `provider_state_json` separately from portable `history_json`:

- **OpenAI:** `recordOpenAINativeCompactionState` serializes `CompactedInputItems` as a JSON array. This can include the subsequent continuation, not merely one opaque summary token. `nativeCompactionInputItems` decodes it for the next call. A complete Responses replay can supersede it. Strategy: `openai_responses`.
- **Anthropic:** client `nativeCompactionState` is `{"version":1,"messages":[...]}` containing active messages and provider compaction blocks, including encrypted content when supplied. Client decode validates/normalizes older representations. Adapter passes it via `NativeCompactionStateJSON`; strategy `anthropic_context_management` permits portable conversion on compatible restore failure/provider changes.
- Compatible/Ollama use shared local summary/recovery paths; no equivalent native snapshot emitter was found in their adapters.

Migration must preserve unknown/opaque provider fields and block roles. Native state cannot safely be flattened into plain summary text.

### Provider session state

`persistProviderSessionState` → `UpsertChatProviderSessionState` updates `provider_session_state_json` independently. Current concrete producer/consumer is OpenAI Astra reasoning state:

```json
{"version":1,"model":"...","request_effort":"...","configured_effort":"...","updates":[{"user_history_index":3,"effort":"..."}]}
```

[responses_websocket.go](../pkg/openai_client/responses_websocket.go) exports/imports it; adapter `restoreOpenAIAstraReasoningState` applies it. Update positions refer to user history and must survive context changes correctly. This is not a saved WebSocket or a full transcript. Transport connection/baseline/last-response fields remain in-memory transport state.

Repository upserts deliberately preserve compatible session state when saving compaction and preserve compatible compaction when updating session state; incompatible native state is cleared. Keep these distinct lifetimes even with one transcript format.

## 6. Display output: writes, full loads, catch-up

- Provider task/chat streams use [stream/writer.go](../internal/llm/stream/writer.go). Construction seeds the output buffer from `GetByID` when retrying an existing execution. `Write` publishes live deltas; `flushPeriodicOnce` appends only pending text through `AppendOutput` and publishes the persisted offset. Explicit `Flush` retries pending data and calls `FinalizeOutput` with the full buffered display string. Terminal `Complete` separately normalizes display markers. Thus the periodic flush is incremental, but final flush and retry seeding still materialize full output.
- Execution repository `FinalizeOutput` / `UpdateOutput` share `replaceRunningOutput`, replacing the base output and clearing chunks transactionally for running executions.
- `Complete` and `CompleteSuccessIfNoPendingSteering` fold chunks into the base when passed empty output, or use supplied full output, then delete chunks. Other terminal/restart status transitions do not necessarily fold chunks; readers must still support base plus chunks. `UpsertSwarmParentResult` can also write an execution display result without producing provider replay.
- Full execution projections concatenate base plus ordered chunks. The chat handler's `loadChatExecutionWindow` and task handler's `loadTaskThreadExecutionWindow` load bounded execution windows for display. These do not require provider replay JSON. See [chat_handler.go](../internal/handler/chat_handler.go), [task_handler.go](../internal/handler/task_handler.go).
- [execution_replay.go](../internal/persistence/sqlite/execution_replay.go), `ReadOutputAfter`, calculates total bytes and cumulative chunk positions, then returns the suffix. It avoids returning the full prefix but still scans chunk lengths. It also implements UTF-8 cursor handling and terminal reset behavior.
- [live_websocket.go](../internal/handler/live_websocket.go), `streamExecution`, subscribes before reading persisted output, sends catch-up frames, and reconciles live offsets. Byte cursors and terminal reset rules are display contracts, not provider item sequences.
- [chat_shared.templ](../web/templates/components/chat_shared.templ) renders saved `data-raw-content` and streamed marker text into message/tool segments. [thread-snapshots.js](../web/assets/thread-snapshots.js) and [thread-navigation.js](../web/assets/thread-navigation.js) hold browser-side snapshots/caches, not another server transcript table.
- Model-history fallback (`ReplayAssistantContent` and provider-switch recovery) can read display output when structured history is absent/incomplete. All old executions cannot be assumed to contain recoverable tool-level JSON.

### Adjacent durable tool records

[OpenAI adapter](../internal/llm/openai/adapter.go), `asyncRuntimeToolCallbacks`, records pending calls, completion/results and delivery/rejection state in `openai_async_tool_calls`; recovery loads undelivered records into the provider conversation. Conversation migration must preserve call identities and delivery ownership. These rows cannot simply be deleted because their result also appears in a transcript.

## 7. Migration requirements established by this audit

1. Classify envelopes, local message arrays, text-only replay rows, explicit empty continuations and nested checkpoint history. Preserve private retained-user/context metadata.
2. Represent replacement boundaries separately from append segments. Preserve thread scope, execution origin, generation and model compatibility.
3. Support changed items as well as appends (parallel results, steering, approval patches). Preserve idempotency and atomic acceptance/checkpoint updates.
4. Preserve native provider payloads losslessly and keep session settings separate from transcript content.
5. Define how output-only historical executions become readable in the single new history path. Some cannot be reconstructed as exact original provider tool exchanges; fail/retain information explicitly rather than silently inventing it.
6. Preserve display strings/cursors until the separate display-reader migration is designed. Do not conflate context compaction with deleting the user-visible conversation.
7. Removing full snapshot allocation also requires changing steering wrappers, provider conversion/recovery and native state packaging—not just the repository method.

## 8. Validation coverage and limits

This map was checked against production call sites of replay repository methods, direct replay-table mutation helpers, checkpoint/session accessors, provider callbacks and display readers. It covers registered provider routing in this checkout; it is not a historical-data integrity scan.

Existing regression suites to carry forward include replay/budget/steering tests, provider-switch and image-conversion tests, skill dependency approval tests, local steering transaction tests, and the compaction server/recompaction/preflight matrices under `internal/service`, `internal/handler`, `internal/persistence/sqlite`, and provider clients. The migration must additionally test interrupted transactions and reconstruct old persisted fixtures of each listed shape.

The [historical memory benchmark](../scripts/replay-memory-bench/EVIDENCE.md) tested the ordinary Responses save method and a prototype incremental store. It did **not** measure real native/local compaction, atomic steering commits, conversion/recovery allocation, display catch-up, or the complete live follow-up. Its result remains evidence for the ordinary checkpoint optimization, not for the memory cost of every path above.
