# Remaining provider reuse implementation status

Initial implementation baseline: `b7c169384`. Completion baseline: `cf29baf12`.
Implemented locally. No delegation, benchmarks, load tests, or profiling.

## Previously completed

- Compatible prepared tool history decodes metadata directly, avoiding the encoded-metadata round trip. Duplicate/unknown-field cases retain the compatibility decoder.
- Anthropic compaction-presence checks inspect borrowed fields.
- Anthropic repaired raw-block arrays use the shared encoder.
- Metadata-only status and ownership reads use the narrow execution projection.

## Completed in this follow-up

1. **Tool-argument conversion.** Shared `jsonview.DecodeNormalized` borrows unchanged canonical strings within argument objects/arrays. Anthropic-to-Completions and reverse conversion use it. Numbers, duplicate keys, noncanonical escaping, invalid UTF-8, and malformed arguments retain the previous normalization/fallback behavior. Structural containers and values that need normalization still decode; this is not a claim of completely zero-copy conversion.
2. **Native checkpoint rows.** Internal repository reads opt into `RestoreCheckpointRows`; native items stay in immutable `ReplayRows` through the service, budgeting, context propagation, and OpenAI/Anthropic sending. Both providers use the same request-scoped `ReadReplayState`/`TakeReplayState` cache. Clearing, replacement, retry, local summary, and provider switching handle both representations. Serialized repository reads and writes remain compatibility boundaries; no storage schema or migration changed. Rare legacy formats, export/string APIs, and exact serialized checkpoint comparison still materialize JSON when required.
3. **Selective execution payloads.** `WithExecutionPayload` extends the common metadata projection with explicit prompt/reasoning/error/session flags. Steering reads reasoning, installation continuation reads its prompt and reasoning/session where needed, automation confirmation reads its prompt, worker completion reads its error, and commit-message preparation reads its prompt. Full UI/model-history readers remain unchanged. The remaining shared callers now opt in too: automation reconciliation loads only error plus metadata, and panic cleanup requests metadata through `ListByTask`, which now honors the common projection helper.
4. **Anthropic compaction response encoding.** Select the complete response blocks before encoding replay, so a compaction response no longer encodes and discards the non-compaction block array first.

## Correctness coverage

- Argument normalization equivalence, escaped/numeric/duplicate/malformed values, and borrowed large strings.
- Repository row/string compatibility, row-backed save and clear, and selective payload projections.
- Native cache ownership, replacement and invalidation; OpenAI budgeting-to-send transfer.
- Anthropic compaction continuation over its real client with serialized and row-backed checkpoints.
- Service restore/budget/provider-switch behavior and older checkpoint formats.
- Existing content assertions use the replay compatibility decoder instead of assuming that every internal replay contains a transcript string.

No performance improvement is claimed without measurements.
