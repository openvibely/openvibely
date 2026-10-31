# Shared provider follow-up: correctness-only changes

Baseline: `ee7920c8ed0d95ad8331c4a97ef431f235888d76`.

Implemented directly in the Find UI Leaks worktree. No benchmark, load-test, profiling, or fuzz campaign was run for this change. No CPU, RAM, allocation, or latency improvement is claimed.

## Implemented

1. **Prepared Responses schemas.** Prepare ordinary tools once per turn for admission and sending. Recheck permissions on every attempt, select the corresponding encoded rows, and append permitted hosted tools through the shared selector. Refresh preparation on the next turn. Responses property encoding and shared metadata assembly preserve encoded fields.
2. **Borrowed history.** Anthropic ordinary replay and summary readers borrow immutable content blocks through one client helper. Compatible serialized native arrays use the prepared row path; summary construction/sending use prepared history. Public decoded history clears private raw references. Actual format-changing conversions retain their decoded APIs.
3. **Single preparation.** Request-scoped ReadRows/TakeRows prepares Responses summary rows for budgeting and transfers ownership to sending. Replaced summary windows invalidate the entry. Failed preparation is not cached. Retained-user extraction uses one prepared projection instead of reconstructing history twice.
4. **Snapshots and checkpoints.** Shared raw-aware encoding now covers compatible failure/summary rows, OpenAI native checkpoints, Anthropic native checkpoints, Anthropic summary snapshots, steering recovery and standalone compaction wrappers. Merging converted Anthropic replay preserves validated rows. Compatibility snapshot JSON uses a pre-sized builder. Mutable messages still require independent encoded snapshots.
5. **Selective inspection.** Checkpoint-presence checks use raw item rows; compaction context checks the role before decoding content. A common tool-display field reader skips unrelated argument payloads while preserving provider-specific label formatting. Two first-turn existence checks use CountByTask rather than loading output histories.
6. **Size accounting.** Field.Size validates metadata and counts encoded keys/field lengths without constructing the complete envelope. Generic row-backed message envelopes use the image scanner and TranscriptBytes without full materialization; ambiguous case-variant envelopes retain fallback behavior.
7. **No-op cleanup.** Common output replacement and tool-output removal return unchanged input when no match exists. Thinking cleanup and summary deduplication avoid irrelevant Markdown/line parsing. Workflow, channel display, UI summary rendering, and recovery reuse these common helpers.

## Boundaries and remaining scope

These changes do not make every provider representation raw. Foreign-provider format conversions that produce public decoded DTOs still decode their content. Public mutable APIs, actual image/role/tool-pair conversion, canonical recorder hashes, malformed/custom JSON and uncommon estimator forms retain their existing compatibility paths. The unused legacy client entry points were not redesigned. The broader inventory's conditional metadata-only database projections and minor string-builder suggestions are not all implemented here.

This document records the implemented call sites, not a claim that the previous exhaustive inventory has no remaining opportunities.

## Correctness checks

Regression coverage includes metadata-size/assembly equivalence and errors, summary-row invalidation and ownership transfer, borrowed Anthropic decoder equivalence, Responses prepared-schema admission equivalence, marker-free Markdown preservation, thinking-control handling, and row-envelope image-budget equivalence. Existing provider tests cover replay edits, recovery, permissions, streaming, native compaction, and wire payloads. The public decoded-history regression exposed private raw references; the implementation was corrected and the original assertion retained.

Validation commands (no `-bench` or profiling flags):

- `go test ./internal/llm/... ./pkg/openai_client ./pkg/anthropic_client`
- `go test ./internal/service`
- Focused service summary/compaction/steering/recovery/conversion/approval/image tests after follow-up edits.
- Focused handler starting-first-turn queue test.
- `go build ./...`
- `go vet ./internal/llm/... ./pkg/openai_client ./pkg/anthropic_client ./internal/service ./internal/handler`

The macOS build emits deployment-target linker warnings. They are not compilation errors.
