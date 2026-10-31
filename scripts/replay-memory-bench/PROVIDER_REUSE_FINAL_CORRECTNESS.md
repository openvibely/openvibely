# Provider reuse follow-up: correctness-only implementation

Implements the six active groups from the inventory reviewed at `8f3c39549`, directly in the task worktree. No delegation, benchmark, load-test, fuzzing, or profiling runs were performed for this implementation. No performance improvement is claimed.

## Coverage

| Group | Implementation |
| --- | --- |
| Borrow unchanged fields through provider switching | `jsonview.Value.CanonicalTextValue`, `PreparedCompletionsHistory.Visit`, `PreparedToolMessage`, and `ReadOnlyContent` preserve immutable textual tool results across Completions/Responses/Anthropic conversions. Service conversion/recovery writes use prepared snapshot rows. Public decoded history still returns isolated, editable values. Retained-user replacement preserves untouched JSON fields. |
| Inspect metadata without serializing unrelated history | `ExecutionReplayMessage.HasResponsesInput` uses native row metadata, with the existing decoder for serialized/foreign representations. Shared user-only summary projection skips encoding assistant/tool messages. Recovery steering inspects roles before decoding user content. |
| Prepared metadata and Responses-Lite reuse | `requestjson.Field.Size` counts supported map metadata and prepared fields without building a header. Both SSE preflight callers use that path. Responses-Lite processes prepared schemas through borrowed fields and raw-aware encoding, preserving unknown schema fields while removing async/hosted tools as before. |
| Narrow metadata-only database reads | Explicit `persistence.WithExecutionMetadata` context selects the existing scan shape with empty payload columns. Context-boundary paging and task-owner discovery opt in. Normal UI and model-history reads remain unchanged; filtering, ownership, ordering and context boundaries retain existing queries. |
| Direct Responses event dispatch | The direct/summary parser skips valid, ignored reasoning deltas before generic event decoding. Visible output and required terminal-event handling remain unchanged. |
| Repeated text assembly | Builders cover Anthropic turn text and consecutive plain recovery messages, shared compaction/steering text, and OpenAI direct attachment lists. Anthropic path labels use standard string splitting, preserving separator behavior and UTF-8. |

## Deliberate compatibility boundaries

- Borrowed values are immutable. Public decoded histories still decode/copy, and changed content/images/tool formats still undergo necessary conversion.
- Noncanonical escaped strings can require decoding to preserve historical token budgets; raw byte length is not automatically equivalent to the old decoded-and-encoded length.
- Metadata size counting follows the wire encoder's direct-raw-field policy. Nested ordinary objects use normal JSON rules. Unsupported/custom marshalers and invalid values retain validation/fallback behavior.
- Null and unusual foreign replay blocks retain the prior decoded conversion behavior instead of discarding valid following blocks.
- Anthropic preflight and sending retain their distinct system representations; the plain-text preflight is counted without a temporary encoding, not substituted for the block envelope.
- User-only projection avoids encoding unrelated messages, but still performs provider conversion/checkpoint selection needed to identify the effective user history.
- One-off concatenations, actual tool argument interpretation, mutable API boundaries, unknown-provider formats, and inactive compatibility APIs were not indiscriminately rewritten.

## Required main synchronization

Startup required resolving the merge with main; merge commit `dd24018cc` records that resolution. Main now owns migration 219 (`drop_account_usage_snapshots`), so the branch's single transcript migration is registered as 220. Compatibility handling adopts completed older branch schemas without rerunning transcript conversion and applies main's actual account-snapshot migration where needed. Migration coverage includes fresh upgrades, downgrade/reopen behavior, and adoption from the former branch 219. There is still only one transcript migration file.

## Correctness validation

Passed:

- `go test ./internal/llm/... ./internal/models ./pkg/openai_client ./pkg/anthropic_client ./internal/persistence/sqlite/...`
- Full `go test ./internal/service`; focused summary, recovery, compaction and provider-switch suites after corrections; final foreign/null-block and user-projection tests.
- Focused handler output-window and steering/context tests; `TestTaskPagedCachedNavigationInChrome` passed after the merge resolution.
- Direct streaming regression: ignored reasoning does not become visible output, and EOF without terminal completion still errors.
- `go build ./...`
- `go vet ./internal/llm/... ./internal/models ./internal/persistence/... ./internal/service ./internal/handler ./pkg/openai_client ./pkg/anthropic_client`
- `git diff --check`

Added tests cover preserved large foreign tool results, editable decoded exports, escaped-string budgets, null rows/blocks, user-only projection equivalence, raw nested metadata sizing, prepared Lite schema fields, native checkpoint presence, and narrow database identity/paging reads.

Existing macOS linker-version warnings were emitted during builds; the build completed successfully. Benchmark sources elsewhere in this directory were not executed.
