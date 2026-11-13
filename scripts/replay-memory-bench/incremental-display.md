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

## Direct-event follow-up

The harness now includes a third mode, `direct-events`: canonical text/tool events
produce DisplayItem objects without ParseDisplay. DisplayRecords encodes those
objects for the existing structured database API. The live-format compatibility
test uses the actual WriteEvent output and checks exact byte equivalence, including
reasoning, incomplete reasoning, escaped tool output, Unicode, invalid UTF-8 and
non-display events. Unsupported raw events explicitly fail in the prototype.

Fresh three-run medians, with all three modes rerun together:

| Metric | Current OV | Text-parsing trial | Direct-event trial |
| --- | ---: | ---: | ---: |
| Retained heap increase before completion | 40.24 MiB | Approximately zero | Approximately zero |
| Allocations through completion | 725.86 MiB | 384.68 MiB | 389.57 MiB |
| Preparation outside persistence API | 25.13 ms | 120.69 ms | 41.69 ms |
| Persistence API calls during execution | 380.99 ms | 479.96 ms | 481.14 ms |
| Combined preparation and saving | 406.12 ms | 600.65 ms | 522.83 ms |
| Finalization | 426.99 ms | 0.36 ms | 0.35 ms |
| Full experiment including checks | 914.91 ms | 691.59 ms | 611.57 ms |

Persistence API duration includes Go transaction/index-maintenance code as well
as SQLite work; it is not a measurement of SQLite execution time alone.
Preparation excludes shared fixture/live-text generation, which is included in
allocation/elapsed measurements. All writes remain synchronous in this trial.
The direct-event variant removes parsing costs, not JSON encoding or index work.
The modest allocation difference between the two incremental variants varied
across repetitions and should not be treated as an established regression.

This is still test-only. It does not implement a background queue, incremental
partial-message persistence, compaction mapping, or the adapter/service API
changes required to stop returning a whole display transcript. Passing these
checks is not proof that production output can yet drop its buffer.
