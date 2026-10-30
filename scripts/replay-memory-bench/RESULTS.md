# Latest measured results

These are the final request/recovery comparison, not new measurements made during repository cleanup. Full results and historical evidence are [archived separately](EVIDENCE.md).

## Versions and workload

- Main: `bdb2ea48fd508a491c0284063938d5cb833a2898` (pinned local main, not today's moving branch).
- Previous: `af10dd39605be6a4a84b78fab7a9cc55df94e43a`, before direct WebSocket events and string forwarding.
- Current measured source: `169bac2ecf0e47348ce86315f377ed86e427b414` plus the accepted patch in the archived `request-recovery/evidence.tar.gz`. Its resulting application code is also in pre-cleanup squash `0d7d5157c`. Cleanup changes no application code.

Three fresh-process repetitions per version/scenario. One or ten tasks share one real backend and SQLite database. Each starts empty, completes a streamed answer, then completes five sequential follow-ups. Ten tasks overlap across tasks, never within one task. Text-only executes no tools. The separate tool-use workload adds a 512 KiB file result per turn. Both stream a 48 KiB answer in 64-byte deltas every 5 ms from a separate simulated OpenAI Responses server. Inference and live browser rendering are excluded.

## Fifth follow-up

CPU, allocated bytes, and objects are totals for the whole response wave, not per second. Ten-task rows cover all ten tasks. Medians of three repetitions; reclaimed objects may belong to earlier work.

| Workload | Metric | Main | Previous | Current |
|---|---|---:|---:|---:|
| One text task | CPU ms | 152.69 | 88.34 | 108.30 |
| | Allocated MiB | 18.93 | 8.36 | 7.89 |
| | Objects created | 56,233 | 17,752 | 14,047 |
| | Objects reclaimed | 56,661 | 17,849 | 15,148 |
| Ten text tasks | CPU ms | 963.29 | 769.67 | 796.66 |
| | Allocated MiB | 181.96 | 79.02 | 74.84 |
| | Objects created | 562,343 | 176,348 | 138,592 |
| | Objects reclaimed | 558,798 | 172,617 | 127,264 |
| One tool-use task | CPU ms | 158.06 | 169.58 | 155.53 |
| | Allocated MiB | 57.31 | 25.57 | 24.50 |
| | Objects created | 75,623 | 32,774 | 28,581 |
| | Objects reclaimed | 79,695 | 33,610 | 29,018 |
| Ten tool-use tasks | CPU ms | 1,269.33 | 1,154.61 | 1,122.71 |
| | Allocated MiB | 558.91 | 250.66 | 239.90 |
| | Objects created | 754,779 | 325,183 | 282,959 |
| | Objects reclaimed | 759,354 | 308,592 | 293,933 |

## Memory, storage, and remaining costs

Peak process RAM includes startup and final integrity reads, not only streaming. DB sizes follow six turns and final WAL checkpoint. Allocated bytes above are cumulative churn, not resident RAM.

| Workload | Peak process RAM MiB: main / previous / current | DB MiB: main → current |
|---|---:|---:|
| One text task | 62.58 / 57.33 / 58.41 | 3.37 → 3.46 |
| Ten text tasks | 123.00 / 101.64 / 101.39 | 15.29 → 15.95 |
| One tool-use task | 77.70 / 86.81 / 86.22 | 7.86 → 8.08 |
| Ten tool-use tasks | 238.27 / 228.19 / 222.14 | 55.44 → 57.49 |

Text follow-up CPU regresses versus previous despite fewer objects; single-task tool RAM and storage regress versus main. Worst observed ten-task tool persistence gap rises 511.65 → 522.34 ms, and single-tool live-output gap 8.03 → 15.38 ms. Those are worst observations, not medians or browser latency. Some individual waves also regress in heap or elapsed time. See the archive for every wave and worst peak, not only this fifth-turn summary.

Kept in the last round: fresh permissions-only reads and shared parser/failure text capture. Removed schema reuse after its repeated CPU/heap tradeoff; rejected a redundant history scan. Differences versus main and previous include earlier improvements, not just this final round.

## Compaction and opening conversations

Separate sequential and browser measurements, three repetitions. Native compaction and saved-checkpoint continuation passed in every version. Summary fallback did not occur. Compaction rows include the whole response that triggered it, not summarization in isolation.

| Scenario | Metric | Main | Previous | Current |
|---|---|---:|---:|---:|
| Text compaction, follow-up 3 | CPU ms | 138.52 | 131.11 | 129.87 |
| Tool compaction, follow-up 1 | CPU ms | 137.63 | 149.19 | 108.31 |
| Tool compaction, follow-up 3 | CPU ms | 113.29 | 146.26 | 116.52 |
| Tool compaction, follow-up 4 | CPU ms | 118.22 | 144.82 | 98.95 |
| Large text conversation | Cold open ms | 151.1 | 152.8 | 159.5 |
| | Cached reopen ms | 149.8 | 145.9 | 154.3 |
| Large tool conversation | Cold open ms | 677.5 | 250.4 | 263.3 |
| | Cached reopen ms | 200.1 | 163.4 | 161.4 |

The large-tool loading benefit predates the final round. Cold means a fresh browser navigation cache, not a flushed OS disk cache. Small/medium text opening is broadly similar; all sizes and JS-memory results are archived.

## Validation and limits

The final production source passed 73 Go test packages and targeted ownership, cancellation/reuse, live-permission, and malformed-schema race checks. This round measured 1,188 concurrent three-version turns and 432 sequential turns, plus 72 cold and 72 verified cached opens. Schema ON/OFF and candidate stages added 1,440 ordinary turns; 132 full-allocation diagnostic turns were excluded from timing comparisons.

Counters reconcile created minus reclaimed to live-object changes; GC reclamation does not identify when a specific object became unused. Profiles use full allocation sampling and forced collections, unlike ordinary timings. Go heap is sampled about every 50 ms, process RSS about every 100 ms; shorter spikes can be missed. GC pause is not total GC CPU. Three runs on a shared host do not establish small timing differences as reliable effects.

An initial schema attempt used a serial peer and was discarded. Missing app-flow test injection in the initial main/previous binaries was detected; those empty runs were discarded and rebuilt/rerun, and harness guards now reject absent tests/artifacts. Valid current app-flow runs were retained, so browser/compaction baselines were collected in separate batches. This limits small timing claims. Concurrent runs had verified overlap and were unaffected. The earlier transient Chrome startup failure passed three isolated retries and the final full suite unchanged.

This is bounded six-turn backend testing with one provider path and a deterministic accelerated peer, not an endurance test, universal provider comparison, remote-model latency measurement, or total desktop-memory measurement.
