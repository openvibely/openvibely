# Remaining provider reuse implementation status

Baseline: b7c169384. Implemented locally; no delegated tasks, benchmarks, load tests or profiles.

Completed in this patch:
- Compatible prepared tool history decodes metadata fields directly, avoiding an intermediate encoded metadata object. Duplicate/unknown-field cases still use the existing compatibility decoder.
- Anthropic native compaction presence inspects borrowed fields, validating string/null types without retaining content/encrypted strings.
- Anthropic request/recovery encoding handles repaired `[]json.RawMessage` content through the shared prepared-array encoder. Public mutable DTO encoding remains unchanged.
- Metadata-only execution reads now use the shared narrow projection for cancellation/terminal status, async tool ownership, attachment owners, installation approval/progress, file events, commit timestamps, and automation ownership/order checks.

Not implemented in this patch:
- Raw tool-argument conversion. Unlike unchanged tool-result strings, the old conversion normalizes numbers, escaping, duplicates and malformed arguments. Reusing raw bytes must preserve those semantics; no blanket substitution was made.
- Row-backed native checkpoint restoration across repository/service/request boundaries. The current public string field participates in invalidation, provider switching, checkpoint equality and retry recovery. This needs an internal native-state representation and end-to-end invalidation tests; no sentinel string or partial bypass was introduced.
- Narrow payload projections for callers that need reasoning, prompt or error text. The metadata-only projection removes those fields and must not be used there.

Validation: provider/shared-helper tests and build/vet passed for the initial patch; dedicated Anthropic regression tests cover repaired raw blocks and compaction-presence type validation. Broader backend correctness results are tracked separately. Performance remains unmeasured. This file is a progress record, not a claim that all requested work is complete.
