# Shared raw replay, budgeting and recovery results

## Verdict

All ten requested areas are implemented using the shared JSON, replay-row, history-transfer and token-estimation helpers. Provider formats and mutation rules remain adapter-specific.

The principal result is compatible tool-history retention: ten parallel tasks peaked at **239.81 → 191.11 MiB**, with worst sampled heap **169.05 → 112.06 MiB**. Every final candidate RAM sample was below every baseline sample. This narrows, but does not close, the gap with Responses (144.22 MiB) and Anthropic (146.86 MiB) in this workload.

This is **not a universal speed or allocation improvement**. Compatible tool CPU rose from 5.944 to 6.041 s at ten tasks and 0.938 to 0.991 s at one task. Responses tool allocations rose 727.90 → 748.10 MiB at ten tasks, with effectively unchanged RAM; its encoder allocates more despite cheaper history preparation. Anthropic's ten-task CPU and allocation results improved modestly, with effectively unchanged median RAM. Several text-only CPU/RAM changes are small and mixed; their ranges are shown below.

The recovery/budget microbenchmarks improved time and allocated bytes, but they do not represent full interrupted or compacted conversations. No migration or stored-data rewrite was added. Public decoded-history access remains available, so consumers that need mutable text still pay decoding costs.

All 72 final timing runs (2,376 turns), six diagnostics, and six final smoke scenarios passed. The final batch had **zero detected test/build overlap exclusions**. Earlier abandoned candidates and excluded samples are retained separately in the archive and never enter these tables. The final allocation-counter correction is included in every table here; there is no post-timing production patch.

Baseline: previous task branch `5d3ffb2ed2d5761138e0f24110dc5ccda8b0218e`, **not main**. All providers use the same before/after source revisions and harness. Raw result labels `main` and `branch` mean baseline and candidate respectively.

Candidate patch SHA-256: `0d05269a023c329c11221c653d494465cf07f2ffb54e6ad36f0ef37d725583dc`. Source hashes and binary hashes are in the evidence archive. Production source remained frozen during accepted measurements.

## Implemented scope

1. Compatible native tool results remain borrowed JSON during preparation, budgeting and sending. They decode only for truncation/conversion; public decoded-history APIs are unchanged. Unsupported, image, duplicate-key and foreign rows retain the normal path. Responses tool outputs and Anthropic native structured content also use borrowed projections; provider rules still decode the fields they need.
2. Shared JSON counting and image adjustment avoid whole-message serialization in compatible budgets, Anthropic native compaction and summary estimation. Escaped strings preserve canonical encoded size; malformed input retains the decoder fallback.
3. OpenAI compaction reuses prepared tool schemas. Default provider tool-size budgets are initialized once from static defaults; dynamic permissions are still checked at request time.
4. Prepared Responses input transfers directly into task recovery via a request-scoped single-use ownership slot, instead of a serialize/decode round trip.
5. Anthropic request preparation and recovery snapshots use the same raw-block-aware message encoder.
6. Provider-switch windows, steering recovery, summary construction and foreign-history conversion retain individual replay rows. New/changed rows still need encoding.
7. Skill approval edits preserve original bytes for untouched rows and encode changed rows only. The edit still decodes rows for exact call-ID, arguments and sibling-state validation; it is not zero-copy inspection.
8. Shared borrowed JSON inspection covers tool IDs, Anthropic tool input, portable checkpoints and compaction context. Relevant user content and changed blocks still decode.
9. Shared request metadata and serialized transcript splitting borrow payloads. Export/legacy consumers can still request a whole transcript explicitly.
10. Responses-Lite generic tools preserve nested schemas as raw fields; generic estimator paths count row sizes and scan image content without building a complete transcript.

No schema migration. No behavior change is intended for permissions, compaction thresholds, tool results, output, images or cancellation.

## Method and limits

72 accepted timing runs: three providers × text/tool × 1/10 tasks × before/after × three repetitions; 2,376 completed turns. Six additional ten-task tool diagnostics collect CPU/allocation/near-peak heaps and are excluded from timing medians. Six turns per task; ten tasks overlap within one app process and SQLite database. Real persistence, replay and 512 KiB file reads; mocked model inference; 48 KiB streamed answers. The second repetition reverses version order. Same explicit context budget avoids compaction in ordinary runs.

The machine was also being used by another task. The runner waits for other Go test/build/vet jobs to finish and excludes/retries a run if such a job appears during it. Excluded samples are preserved separately and never enter tables. This detects those jobs, not every possible source of host noise. CPU counts application-process time; RSS sampling is approximately 100 ms and includes setup/final verification. Heap profiling is separate, near-peak rather than instantaneous, and can alter GC behavior.

The first candidate was paused after an OpenAI allocation regression. Profiling identified nested raw fields being reformatted and copied. The corrected shared encoder handles those fields, reserves the complete request capacity, and recovery snapshots share only explicitly immutable values. A subsequent targeted benchmark caught temporary string allocation per unescaped span. The shared text counter now accepts strings or byte spans directly; its regression test fails before the fix. Both earlier candidates and their measurements are kept separately and excluded from the final comparison.

Ordinary measurements do not exercise compaction, provider switching, approvals or failure recovery. Their validation comes from regression suites and separately labeled targeted microbenchmarks, not the ordinary workload timings.

## Six-turn medians

| Provider | Workload | Tasks | CPU seconds before → after | Allocated MiB | Peak process RAM MiB |
|---|---|---:|---:|---:|---:|
| openai | text-only | 1 | 0.794 → 0.771 | 27.30 → 27.07 | 57.67 → 57.67 |
| openai | text-only | 10 | 4.766 → 4.810 | 253.11 → 249.12 | 93.17 → 93.95 |
| openai | tool-use | 1 | 0.936 → 0.941 | 75.86 → 78.24 | 74.44 → 74.88 |
| openai | tool-use | 10 | 5.691 → 5.727 | 727.90 → 748.10 | 144.12 → 144.22 |
| anthropic | text-only | 1 | 0.788 → 0.771 | 30.80 → 29.52 | 55.11 → 55.00 |
| anthropic | text-only | 10 | 4.689 → 4.708 | 283.83 → 270.57 | 91.53 → 90.92 |
| anthropic | tool-use | 1 | 0.898 → 0.915 | 86.96 → 83.07 | 73.61 → 73.67 |
| anthropic | tool-use | 10 | 5.802 → 5.677 | 810.07 → 776.01 | 147.56 → 146.86 |
| openai-compatible | text-only | 1 | 0.711 → 0.714 | 23.74 → 22.51 | 55.19 → 55.78 |
| openai-compatible | text-only | 10 | 4.534 → 4.539 | 211.14 → 200.03 | 88.11 → 88.44 |
| openai-compatible | tool-use | 1 | 0.938 → 0.991 | 116.68 → 110.34 | 80.92 → 77.70 |
| openai-compatible | tool-use | 10 | 5.944 → 6.041 | 1094.82 → 1041.26 | 239.81 → 191.11 |

## Storage, elapsed time and delivery gaps

| Provider | Workload | Tasks | Elapsed seconds | DB MiB | Longest live gap ms (median) | Longest persistence gap ms (median) |
|---|---|---:|---:|---:|---:|---:|
| openai | text-only | 1 | 27.14 → 27.10 | 3.54 → 3.54 | 6.05 → 7.03 | 501.22 → 501.35 |
| openai | text-only | 10 | 27.33 → 27.31 | 15.99 → 16.03 | 6.56 → 6.68 | 506.93 → 508.57 |
| openai | tool-use | 1 | 27.24 → 28.11 | 8.16 → 8.16 | 7.32 → 6.70 | 501.31 → 501.40 |
| openai | tool-use | 10 | 27.66 → 27.65 | 57.57 → 57.61 | 22.36 → 22.16 | 510.56 → 511.16 |
| anthropic | text-only | 1 | 29.98 → 29.93 | 3.00 → 3.00 | 6.83 → 6.87 | 501.31 → 501.44 |
| anthropic | text-only | 10 | 30.05 → 30.12 | 11.19 → 11.19 | 7.42 → 7.61 | 507.10 → 507.58 |
| anthropic | tool-use | 1 | 29.99 → 30.02 | 7.00 → 7.00 | 12.18 → 11.72 | 501.56 → 501.75 |
| anthropic | tool-use | 10 | 30.50 → 30.49 | 46.34 → 46.39 | 22.60 → 19.67 | 517.14 → 515.00 |
| openai-compatible | text-only | 1 | 29.97 → 29.89 | 2.76 → 2.76 | 8.31 → 6.70 | 501.40 → 501.93 |
| openai-compatible | text-only | 10 | 30.09 → 30.02 | 8.38 → 8.43 | 7.42 → 7.52 | 505.69 → 508.67 |
| openai-compatible | tool-use | 1 | 30.04 → 30.16 | 9.72 → 9.72 | 10.70 → 10.87 | 502.66 → 502.95 |
| openai-compatible | tool-use | 10 | 30.39 → 30.48 | 73.08 → 73.04 | 29.11 → 27.27 | 509.40 → 510.75 |

## Variability and sampled heap

RAM samples are repetition order. CPU range is min–max across repetitions. Sampled heap is the worst observed across turns/runs, not RSS.

| Provider | Workload | Tasks | Before RAM samples MiB | After RAM samples MiB | CPU range seconds before / after | Worst sampled heap MiB before → after |
|---|---|---:|---|---|---|---|
| openai | text-only | 1 | 57.67, 57.42, 57.80 | 57.67, 58.44, 56.89 | 0.779–0.797 / 0.690–0.794 | 8.41 → 9.54 |
| openai | text-only | 10 | 93.17, 92.09, 95.30 | 92.56, 93.95, 95.59 | 4.611–4.776 / 4.776–4.862 | 34.78 → 33.06 |
| openai | tool-use | 1 | 73.30, 74.44, 77.36 | 73.47, 75.12, 74.88 | 0.921–0.942 / 0.931–0.944 | 11.71 → 12.02 |
| openai | tool-use | 10 | 142.31, 148.86, 144.12 | 136.45, 144.22, 145.97 | 5.648–5.733 / 5.726–5.737 | 54.43 → 54.19 |
| anthropic | text-only | 1 | 55.11, 55.94, 55.03 | 55.00, 55.89, 54.75 | 0.742–0.791 / 0.739–0.786 | 8.62 → 8.06 |
| anthropic | text-only | 10 | 91.53, 92.61, 90.16 | 89.86, 91.52, 90.92 | 4.585–4.738 / 4.676–4.821 | 33.24 → 31.28 |
| anthropic | tool-use | 1 | 73.08, 75.14, 73.61 | 73.11, 73.67, 76.08 | 0.898–0.966 / 0.902–0.980 | 11.57 → 12.58 |
| anthropic | tool-use | 10 | 146.08, 147.56, 151.62 | 146.86, 156.31, 142.11 | 5.780–5.813 / 5.619–5.728 | 66.04 → 66.06 |
| openai-compatible | text-only | 1 | 54.39, 55.84, 55.19 | 55.34, 55.78, 56.31 | 0.696–0.765 / 0.678–0.737 | 7.45 → 9.14 |
| openai-compatible | text-only | 10 | 91.00, 87.92, 88.11 | 87.06, 89.70, 88.44 | 4.487–4.578 / 4.357–4.563 | 28.64 → 27.45 |
| openai-compatible | tool-use | 1 | 80.31, 80.92, 82.84 | 77.70, 77.42, 78.91 | 0.865–0.967 / 0.841–1.009 | 21.23 → 16.39 |
| openai-compatible | tool-use | 10 | 254.03, 239.81, 232.91 | 189.42, 191.11, 197.30 | 5.910–5.993 / 5.962–6.085 | 169.05 → 112.06 |

## Validation

Full models/transcript/persistence/LLM/client/service suites passed, along with build, vet, helper race checks and six final smoke scenarios. Borrowed-string counter fuzzing passed 492,391 executions on the final implementation. Regression coverage includes escaped strings and malformed UTF-8, image budget equivalence, tool-output truncation/edit fallback, single-use transfer, approval row retention, provider switching, compaction and recovery.

## Targeted measurements

These are isolated microbenchmarks, three repetitions per case. They measure the named helper paths, not complete HTTP recovery/compaction operations. Ordinary timing runs were finished before these measurements.

### Existing recovery/budget scenarios: previous branch → final candidate

| Scenario | Time µs | Bytes allocated | Objects allocated |
|---|---:|---:|---:|
| pending_externalization | 401.01 → 346.58 | 738,986 → 643,594 | 325 → 203 |
| native_compaction | 225.64 → 202.26 | 115,573 → 65,029 | 183 → 123 |
| local_summary_retry | 96.05 → 43.78 | 140,441 → 71,622 | 441 → 316 |
| summary_failure | 302.18 → 157.85 | 542,583 → 216,824 | 1,285 → 894 |

### Final-code comparisons

The next two compare decoded and borrowed APIs in the same final build; they are not baseline-commit comparisons. Prepared history only qualifies supported unchanged native tool rows. Decoded APIs remain available for callers that need mutable content.

| Path: decoded → borrowed | Time µs | Bytes allocated | Objects allocated |
|---|---:|---:|---:|
| Native tool history preparation | 52,688.65 → 47,455.83 | 18,457,059 → 19,192 | 206 → 282 |
| Large raw request encoding | 5,543.43 → 5,448.98 | 10,771,252 → 10,500,423 | 10 → 166 |

The shared string counter has a dedicated allocation regression test: 1,024 escaped spans allocated 1,024 temporary strings before the final correction; the final test allows at most two allocations for the same scan. That correction is included in all final timing and diagnostic runs.


## Profile findings

Six separate ten-task tool runs, one before and one after per provider. These are sampled diagnostic runs, not extra repetitions for the timing tables. CPU/allocation profiles cover the full diagnostic process, including startup and verification. Near-peak profiles were requested at external RSS highs without forcing GC; they reflect Go's sampled, GC-accounted allocations, not exact simultaneous RSS. Cumulative stack figures overlap and must not be added.

### Compatible retained content

Turn-5 in-use profile:

| Sampled allocation owner | Before MiB | After MiB |
|---|---:|---:|
| SQLite `columnText` (stored replay and other text) | 31.30 | 30.44 |
| JSON decoder `makeString` | 22.55 | 3.17 |
| Total sampled in-use heap in this snapshot | 65.81 | 67.54 |

The decoded-string reduction supports the intended ownership change: native tool history remains encoded rather than retaining decoded content alongside the source rows. The stored replay itself remains present. The total profile is not lower because these snapshots captured different transient work and GC accounting; the candidate also includes tool execution, quoting, builders and recorder buffers. It would be incorrect to present this one sampled heap total as proof of lower peak memory.

The stronger peak-memory evidence is the independent timing batch: all three candidate ten-task peaks (189.42, 191.11, 197.30 MiB) are below all three baseline peaks (254.03, 239.81, 232.91 MiB). Worst sampled Go heap across those timing runs fell 169.05 → 112.06 MiB.

Both final `memory-turn-5.pprof` files were captured during turn 5. Capture started 10.294 ms after the baseline request and 23.096 ms after the candidate request. Their GC counts were 51 and 60 respectively. Capture metadata is retained in each run's `memory-captures.json` and `memory-diagnostic.json`.

### Allocation stacks: the gains and remaining costs

| Diagnostic cumulative stack | Before MiB | After MiB | Interpretation |
|---|---:|---:|---|
| Compatible history preparation | 169.91 | 20.48 | `buildClientHistory` replaced by `buildPreparedClientHistory`; large native tool strings are borrowed. |
| Compatible `requestjson.MarshalItems` | 106.76 | 52.94 | Less decoding/re-encoding while assembling history. |
| Compatible recorder save | 142.95 | 161.67 | Current-turn persistence still allocates; raw history is not a zero-copy recorder. |
| Anthropic `requestjson.MarshalItems` | 71.85 | 28.66 | Shared encoder handles borrowed message fields with fewer intermediate payloads. |
| Responses history preparation | 32.99 | 17.23 | `buildClientHistory` replaced by `buildPreparedHistory`. |
| Responses WebSocket request encoding | 16.92 | 43.41 | Extra nested-raw assembly offsets the smaller history-preparation cost. |

The Responses encoding stack is consistent with the ordinary tool-workload allocation regression (727.90 → 748.10 MiB at ten tasks). The implementation is not a demonstrated ordinary-workload improvement for Responses; its recovery/ownership changes and shared machinery do not erase that cost.

Compatible's cumulative JSON string allocation is not eliminated: `makeString` totals 82.57 → 92.27 MiB in the diagnostic process even though retained decoded strings shrink sharply. Explicit decoding, truncation, current tool results, verification and serialization still create temporary strings. The final request-time memory improvement should not be described as zero-copy processing.

### CPU limits

Diagnostic CPU sample totals are OpenAI 5.61 → 5.62 s, Anthropic 5.69 → 5.64 s, compatible 5.98 → 6.17 s. These are explanatory captures, not benchmark medians. SQLite, runtime scheduling, I/O and provider work remain substantial. The repeated timing results show a small compatible ten-task CPU regression, 5.944 → 6.041 s. A single CPU profile does not isolate a precise causal share for every small change.

### Reopening the evidence

The archive includes flat and cumulative summaries, unbounded allocation summaries, raw CPU/heap profiles, phase timestamps and source/binary hashes. Inspect `profile-summaries/*--mem--alloc_space--all.txt` for the stack values above and `*--memory-turn-5--inuse_space.txt` for the retention comparison. No rerun is needed to inspect these profiles; a new code change still needs new measurements.


## Reuse the evidence

Example (inside the evidence archive):

```sh
go tool pprof -top diagnostic/openai-compatible/tool-use-large-10-branch-1/cpu.pprof
go tool pprof -sample_index=alloc_space -top diagnostic/openai-compatible/tool-use-large-10-branch-1/mem.pprof
go tool pprof -sample_index=inuse_space -top diagnostic/openai-compatible/tool-use-large-10-branch-1/memory-turn-5.pprof
```

Raw directory names use `main` for the pinned previous branch, not local main. The baseline, candidate patch, per-file hashes, harness and run metadata are included. Generated databases, fixture repositories and executable binaries are omitted; the symbolic profiles and readable summaries are retained.

## Per-turn tables

### openai: text-only, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 104.7 → 102.0 | 3.55 → 3.51 | 8266 → 8257 | 5.25 → 5.24 | 4.372 → 4.368 |
| 1 | 143.7 → 144.9 | 4.15 → 4.10 | 11559 → 11575 | 6.80 → 6.98 | 4.568 → 4.567 |
| 2 | 130.0 → 121.8 | 4.36 → 4.30 | 12057 → 12080 | 6.87 → 7.26 | 4.553 → 4.533 |
| 3 | 139.3 → 134.0 | 4.69 → 4.62 | 12316 → 12372 | 6.32 → 7.48 | 4.560 → 4.547 |
| 4 | 140.0 → 128.1 | 5.04 → 5.06 | 12666 → 12691 | 7.96 → 9.30 | 4.569 → 4.541 |
| 5 | 135.7 → 135.2 | 5.50 → 5.49 | 13016 → 13089 | 8.19 → 7.34 | 4.538 → 4.544 |

### openai: text-only, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 601.0 → 621.1 | 29.22 → 28.73 | 72028 → 71489 | 13.44 → 12.28 | 4.392 → 4.390 |
| 1 | 812.8 → 833.5 | 37.75 → 37.10 | 113738 → 113533 | 15.98 → 18.31 | 4.590 → 4.591 |
| 2 | 806.8 → 830.1 | 40.64 → 40.03 | 120212 → 120190 | 19.78 → 20.65 | 4.590 → 4.588 |
| 3 | 835.2 → 828.5 | 44.23 → 43.56 | 122716 → 123213 | 22.34 → 24.15 | 4.600 → 4.588 |
| 4 | 846.8 → 851.0 | 47.88 → 47.72 | 125809 → 126490 | 25.62 → 25.90 | 4.598 → 4.597 |
| 5 | 821.5 → 872.9 | 52.83 → 51.86 | 129047 → 130016 | 31.10 → 30.74 | 4.592 → 4.588 |

### openai: tool-use, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 117.1 → 112.7 | 9.97 → 10.03 | 11609 → 11685 | 8.55 → 8.46 | 4.386 → 4.378 |
| 1 | 162.3 → 164.6 | 12.26 → 12.08 | 14727 → 14789 | 7.81 → 7.63 | 4.578 → 4.576 |
| 2 | 164.2 → 162.5 | 12.37 → 12.78 | 15437 → 15793 | 11.00 → 9.58 | 4.582 → 4.837 |
| 3 | 159.0 → 165.0 | 13.01 → 13.52 | 16107 → 16430 | 9.91 → 10.05 | 4.583 → 5.027 |
| 4 | 168.4 → 164.5 | 13.74 → 14.39 | 16734 → 17218 | 9.17 → 9.49 | 4.571 → 4.746 |
| 5 | 166.9 → 171.1 | 14.47 → 15.38 | 17456 → 18052 | 11.58 → 10.90 | 4.584 → 4.564 |

### openai: tool-use, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 773.4 → 748.4 | 89.66 → 86.28 | 97224 → 96544 | 30.91 → 32.01 | 4.451 → 4.440 |
| 1 | 957.4 → 925.7 | 116.89 → 116.90 | 143617 → 144912 | 46.25 → 36.78 | 4.631 → 4.610 |
| 2 | 967.3 → 1002.2 | 121.00 → 124.10 | 153814 → 155599 | 40.55 → 40.65 | 4.640 → 4.652 |
| 3 | 1000.0 → 1013.4 | 126.87 → 131.24 | 158773 → 162559 | 44.11 → 48.95 | 4.653 → 4.659 |
| 4 | 993.4 → 1042.6 | 133.76 → 139.54 | 165843 → 170348 | 51.94 → 51.26 | 4.653 → 4.667 |
| 5 | 994.9 → 1048.3 | 140.91 → 149.78 | 173289 → 179134 | 52.37 → 50.19 | 4.675 → 4.659 |

### anthropic: text-only, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 92.1 → 92.8 | 3.49 → 3.44 | 15633 → 15648 | 5.32 → 5.29 | 4.854 → 4.856 |
| 1 | 137.6 → 135.3 | 4.46 → 4.34 | 19062 → 18972 | 6.41 → 6.20 | 5.025 → 5.014 |
| 2 | 132.6 → 133.6 | 4.94 → 4.80 | 19293 → 19215 | 6.79 → 6.80 | 5.026 → 5.019 |
| 3 | 140.2 → 132.0 | 5.49 → 5.21 | 19495 → 19411 | 7.13 → 7.40 | 5.009 → 5.025 |
| 4 | 134.1 → 132.1 | 5.99 → 5.68 | 19723 → 19708 | 7.48 → 7.51 | 5.023 → 5.014 |
| 5 | 144.1 → 140.3 | 6.48 → 6.14 | 19860 → 19876 | 7.70 → 7.86 | 5.028 → 5.027 |

### anthropic: text-only, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 620.4 → 598.6 | 29.99 → 29.39 | 143113 → 142594 | 11.48 → 11.63 | 4.860 → 4.867 |
| 1 | 748.4 → 817.0 | 40.61 → 39.03 | 187496 → 186851 | 15.64 → 15.08 | 5.016 → 5.050 |
| 2 | 844.1 → 801.5 | 45.96 → 44.27 | 192177 → 191324 | 18.06 → 19.87 | 5.052 → 5.035 |
| 3 | 777.0 → 821.0 | 51.06 → 48.30 | 194118 → 193466 | 23.01 → 22.85 | 5.019 → 5.041 |
| 4 | 830.4 → 836.9 | 55.63 → 52.73 | 196347 → 195529 | 25.02 → 25.64 | 5.050 → 5.057 |
| 5 | 855.4 → 874.0 | 60.68 → 56.98 | 198078 → 197405 | 32.63 → 31.07 | 5.045 → 5.073 |

### anthropic: tool-use, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 106.3 → 111.2 | 10.32 → 10.28 | 18798 → 18786 | 8.78 → 8.28 | 4.839 → 4.863 |
| 1 | 173.9 → 166.8 | 13.10 → 12.66 | 22092 → 21924 | 9.10 → 9.56 | 5.052 → 5.026 |
| 2 | 148.0 → 164.5 | 14.15 → 13.66 | 22620 → 22566 | 8.44 → 10.28 | 5.021 → 5.045 |
| 3 | 176.0 → 161.1 | 15.32 → 14.56 | 23134 → 22977 | 11.20 → 10.61 | 5.038 → 5.025 |
| 4 | 164.5 → 163.3 | 16.43 → 15.47 | 23539 → 23458 | 10.55 → 11.14 | 5.037 → 5.033 |
| 5 | 175.7 → 161.1 | 17.59 → 16.41 | 24101 → 23950 | 11.47 → 12.22 | 5.042 → 5.024 |

### anthropic: tool-use, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 759.2 → 730.9 | 90.91 → 90.90 | 165456 → 164733 | 33.21 → 36.07 | 4.917 → 4.925 |
| 1 | 936.6 → 962.3 | 122.51 → 119.44 | 216948 → 216112 | 39.72 → 41.29 | 5.098 → 5.121 |
| 2 | 1009.0 → 997.1 | 133.24 → 128.35 | 224821 → 223821 | 50.41 → 46.79 | 5.115 → 5.117 |
| 3 | 1016.8 → 1005.8 | 142.04 → 136.90 | 229602 → 228631 | 56.07 → 52.65 | 5.129 → 5.140 |
| 4 | 1004.9 → 1009.9 | 154.52 → 147.53 | 234528 → 233640 | 61.61 → 57.49 | 5.102 → 5.103 |
| 5 | 1064.5 → 991.7 | 166.86 → 155.48 | 239355 → 238483 | 61.81 → 62.95 | 5.143 → 5.100 |

### openai-compatible: text-only, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 91.3 → 81.7 | 2.70 → 2.72 | 10406 → 10384 | 4.60 → 4.81 | 4.853 → 4.834 |
| 1 | 137.3 → 125.1 | 3.49 → 3.39 | 13926 → 13797 | 5.89 → 5.53 | 5.024 → 5.004 |
| 2 | 127.5 → 121.1 | 3.88 → 3.71 | 14301 → 14215 | 6.09 → 6.26 | 5.032 → 4.992 |
| 3 | 124.4 → 129.6 | 4.20 → 4.03 | 14406 → 14444 | 6.32 → 6.76 | 5.011 → 5.012 |
| 4 | 116.7 → 118.6 | 4.51 → 4.21 | 14540 → 14560 | 6.92 → 7.43 | 5.008 → 5.002 |
| 5 | 131.6 → 128.6 | 4.84 → 4.43 | 14761 → 14681 | 6.84 → 7.05 | 5.025 → 5.023 |

### openai-compatible: text-only, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 570.2 → 572.9 | 22.13 → 21.78 | 92168 → 91537 | 12.01 → 12.70 | 4.861 → 4.871 |
| 1 | 757.8 → 773.0 | 30.88 → 29.95 | 136962 → 136839 | 16.09 → 14.56 | 5.037 → 5.031 |
| 2 | 796.1 → 819.1 | 34.48 → 32.81 | 141766 → 141471 | 18.62 → 19.60 | 5.039 → 5.041 |
| 3 | 775.2 → 732.3 | 37.97 → 35.86 | 143564 → 143481 | 22.11 → 20.33 | 5.021 → 5.018 |
| 4 | 827.8 → 789.2 | 41.10 → 38.59 | 144857 → 145116 | 23.15 → 23.76 | 5.061 → 5.054 |
| 5 | 804.4 → 809.5 | 44.59 → 41.04 | 146639 → 146658 | 26.93 → 27.12 | 5.056 → 5.029 |

### openai-compatible: tool-use, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 107.1 → 105.3 | 11.61 → 11.64 | 13178 → 13195 | 9.01 → 9.65 | 4.858 → 4.850 |
| 1 | 161.6 → 155.9 | 15.58 → 15.27 | 16386 → 16291 | 11.61 → 10.71 | 5.030 → 5.052 |
| 2 | 159.4 → 179.4 | 18.34 → 17.55 | 16843 → 16916 | 13.13 → 12.56 | 5.036 → 5.041 |
| 3 | 164.3 → 170.7 | 21.32 → 19.73 | 17117 → 17197 | 16.11 → 13.71 | 5.033 → 5.052 |
| 4 | 172.0 → 185.6 | 23.65 → 22.10 | 17481 → 17603 | 19.17 → 15.44 | 5.048 → 5.056 |
| 5 | 178.7 → 178.7 | 26.22 → 24.31 | 17829 → 17933 | 20.38 → 15.61 | 5.047 → 5.070 |

### openai-compatible: tool-use, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects | Sampled heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 714.1 → 723.1 | 102.50 → 103.50 | 110243 → 109430 | 40.18 → 38.13 | 4.899 → 4.910 |
| 1 | 972.1 → 941.4 | 145.69 → 142.92 | 160501 → 160177 | 72.34 → 54.48 | 5.085 → 5.102 |
| 2 | 1002.4 → 1024.0 | 174.28 → 166.14 | 166810 → 167296 | 97.15 → 55.28 | 5.102 → 5.110 |
| 3 | 1015.3 → 1074.8 | 200.83 → 188.42 | 170052 → 170648 | 116.36 → 80.34 | 5.086 → 5.125 |
| 4 | 1078.0 → 1132.3 | 223.00 → 209.22 | 172970 → 173850 | 129.45 → 97.00 | 5.099 → 5.118 |
| 5 | 1154.3 → 1152.7 | 252.72 → 232.92 | 175912 → 177765 | 154.57 → 104.17 | 5.117 → 5.122 |

