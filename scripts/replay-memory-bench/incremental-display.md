# Incremental display storage experiment

Run explicitly:

```sh
OV_INCREMENTAL_DISPLAY_EXPERIMENT=1 go test ./internal/llm/stream -run '^TestIncrementalDisplayExperiment$' -count=3 -v
```

Production code is unchanged. The opt-in test compares the existing stream writer with direct, incremental writes through the existing `AppendConversationChanges` API. Each run uses a fresh file-backed SQLite database with production reader/writer routing.

Fixture: 128 complete groups containing assistant text, a tool invocation, and a roughly 288 KiB tool result; 37,756,946 bytes total (36 MiB). The incremental variant parses only the just-completed group, writes its structured records, and releases it. The baseline retains the full output in the actual stream Writer, periodically appends raw chunks, and calls Flush before completion. Both use identical output and finish with Complete. Assertions verify reconnect byte offsets and exact final transcript hashes.

## Initial results

Three sequential repetitions, medians:

| Metric | Existing writer | Incremental structured groups |
| --- | ---: | ---: |
| Retained Go heap increase before completion | 40.24 MiB | Approximately zero (-0.31 MiB measured) |
| Cumulative allocations through completion | 725.81 MiB | 396.88 MiB |
| Time writing all groups | 458.03 ms | 625.40 ms |
| Completion including final flush | 470.63 ms | 0.36 ms |

Negative heap deltas reflect collection of setup allocations, not negative memory usage. Allocation totals include reconnect checks and fixture/group preparation, but exclude the final validation read. There is no peak-RSS or CPU-time measurement. Times are wall-clock durations. The structured write timing varied (616–873 ms).

## Limits and remaining integration

This isolates storage of completed display groups; it is not an end-to-end task implementation or a provider-memory benchmark. The current providers' conversation state and text-only response buffers are not modeled. Streaming partial text still needs durable storage and retry-safe promotion into structured rows. Compaction inspects the full display text, and adapters return full output to service/handler callers; those interfaces must be addressed to prevent recreating the same full buffer elsewhere. Tests must cover plans, pagination, compaction, disconnects, cancellation, crashes, and retry duplication before replacing the production writer.

The result supports pursuing incremental storage, but does not establish bounded memory for unfinished/oversized messages or total application memory savings.
