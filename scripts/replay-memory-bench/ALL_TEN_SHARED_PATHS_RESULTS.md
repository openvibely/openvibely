# All ten shared provider optimization areas

**Verdict:** All ten areas are implemented through common helpers. The measured candidate lowers allocations in all twelve ordinary scenarios, but is not a consistent CPU or RAM win. Ten-task tool RAM rises for all providers. OpenAI single-task tool CPU is worse in the median; observed ranges overlap. Do not describe this as a universal speed or RAM improvement.

**Baseline:** previous task branch `b9a23eb5afb9317a4c4c3f12bda3660bff7653bc`, not main. Same before/after revisions and harness across all providers. `main` in raw directory names means this baseline, not a newly checked-out main branch.

**Final-source qualification:** The 72 timings and six diagnostic profiles measure the frozen candidate patch. A small shared schema-selection correction was subsequently tested and microbenchmarked, and final smoke workloads use that corrected build. Both patches and binary hashes are included; the large tables are not a repeated final-correction performance claim.

72 timing runs (2,376 turns) and six diagnostic runs passed. Relevant LLM, persistence, service and client suites; focused handler/channel/approval tests; build; vet; helper race checks; and roughly 1.7 million size-counter fuzz cases passed. The final selection helper also passed client tests and race checks. No migration was added.

## Implemented scope

Baseline b9a23eb5afb9317a4c4c3f12bda3660bff7653bc (previous task branch, NOT main).
Candidate is exact candidate.patch, including untracked helper packages and tests.

1. `requestjson.MarshalArrayField` extends raw items to OpenAI HTTP, fallback and legacy compaction. Anthropic's prepared message envelopes preserve raw blocks.
2. `jsonview` borrows object/array fields for Anthropic stream envelopes/content inspection, compatible content decoding and failure recovery. Retained nested data is decoded/owned before scanner reuse.
3. `requestjson.StringSize`/`Size`/scalar counters share exact size counting. Compatible tool arguments and additional decoded Responses shapes avoid full serialization. Unsupported/custom types keep the encoder fallback.
4. `tokenestimate.FromLegacyTranscript` and shared message scanning cover recovery image normalization and Anthropic native-state budgeting. Preserves image_content conversion, envelope length and image costs.
5. `preparedhistory.ReadState`/`TakeState` reuse decoded native state with ownership transfer, changed-state invalidation, clearing and request scope.
6. `recovery.Snapshot` stores immutable per-message rows. Both clients and service recovery consume rows instead of encoding and decoding an entire conversation array. Legacy externally supplied MessagesJSON is still accepted. This is not zero serialization: modified/new rows must be frozen, and later compaction may serialize its result.
7. Responses reasoning and tool-argument deltas use typed decoding; malformed cases retain the previous fallback. Wire structs remain provider-specific.
8. `requestjson.Field.Select` reuses schemas for dynamically filtered/native tool sets. Permission checks run per attempt, including trimmed Anthropic names. Native-only tools are supported.
9. `prompt.profileCache` shares once-initialized bounded profile snapshots for Anthropic and Codex. Request/personality context remains outside the cache.
10. `persistence.WithModelHistory` shares selective display projection across task, chat, API, review, email, retry, channel and skill-approval continuation reads; Ollama uses compatible eligibility. UI reads remain full. Failed/empty/foreign/legacy histories preserve fallback output. Shared channel ingress applies one rule across integrations.

Legacy Anthropic simple Send now uses shared SSE framing. Retained first-party Completions adapter uses shared prepared history and native-row decoding. No new migration.

## Validation scope
Normal harness: text and tool requests, 1 and 10 parallel tasks, six turns, all three providers, same revisions, three repeats. Large answers 48 KiB, file reads 512 KiB. Real backend/SQLite/tools/replay, mocked inference.
Targeted equivalence and microbenchmarks: JSON size escaping/numbers, legacy image recovery, native state reuse, failed-conversation capture. HTTP fallback, native/filtered tools, compaction, cancellation and recovery also have existing provider/service tests. The normal timing harness does NOT measure real-vendor latency, image conversations, recovery/compaction frequency or every channel UI route. Targeted microbenchmarks are not end-to-end performance claims for those paths.
One initial partial timing batch was excluded because the channel/approval loader routes were not yet wired; accepted final timings use the rebuilt final candidate. Final smoke directories are prefixed final-smoke.

## Six-turn results

All values are medians of three fresh processes. CPU/allocation totals include all tasks; ten tasks run concurrently. Peak RAM includes setup and final verification. Second repetition reverses version order. Profiling is separate from timing.

| Provider | Workload | Tasks | CPU seconds before → after | Allocated MiB | Peak RAM MiB |
|---|---|---:|---:|---:|---:|
| openai | text-only | 1 | 0.768 → 0.753 | 28.17 → 27.24 | 57.88 → 57.53 |
| openai | text-only | 10 | 4.540 → 4.609 | 258.47 → 252.90 | 92.53 → 93.41 |
| openai | tool-use | 1 | 0.857 → 0.930 | 77.78 → 75.82 | 74.86 → 75.25 |
| openai | tool-use | 10 | 5.546 → 5.539 | 738.66 → 728.37 | 144.70 → 151.98 |
| anthropic | text-only | 1 | 0.755 → 0.734 | 34.22 → 31.32 | 55.88 → 55.84 |
| anthropic | text-only | 10 | 4.637 → 4.602 | 316.40 → 288.01 | 90.14 → 91.62 |
| anthropic | tool-use | 1 | 0.909 → 0.887 | 99.40 → 87.67 | 73.61 → 73.52 |
| anthropic | tool-use | 10 | 5.701 → 5.639 | 931.90 → 827.85 | 145.88 → 148.09 |
| openai-compatible | text-only | 1 | 0.681 → 0.703 | 24.86 → 23.65 | 55.88 → 55.42 |
| openai-compatible | text-only | 10 | 4.250 → 4.185 | 227.92 → 211.46 | 87.86 → 86.86 |
| openai-compatible | tool-use | 1 | 0.907 → 0.908 | 126.11 → 117.18 | 81.58 → 81.05 |
| openai-compatible | tool-use | 10 | 5.848 → 5.785 | 1193.07 → 1098.52 | 233.42 → 241.59 |

## Elapsed time, storage and delivery gaps

These are observer gaps, not browser latency. Database size is measured after final WAL checkpoint.

| Provider | Workload | Tasks | Elapsed seconds | DB MiB | Median longest live gap ms | Median longest persistence gap ms |
|---|---|---:|---:|---:|---:|---:|
| openai | text-only | 1 | 29.58 → 29.64 | 3.54 → 3.54 | 6.65 → 6.68 | 501.23 → 501.18 |
| openai | text-only | 10 | 29.92 → 29.84 | 16.10 → 16.11 | 7.44 → 7.07 | 507.72 → 506.33 |
| openai | tool-use | 1 | 29.91 → 29.93 | 8.17 → 8.17 | 6.96 → 6.96 | 501.52 → 501.31 |
| openai | tool-use | 10 | 30.30 → 30.26 | 57.61 → 57.57 | 20.95 → 22.83 | 510.30 → 511.28 |
| anthropic | text-only | 1 | 29.87 → 29.78 | 3.00 → 3.00 | 7.72 → 7.03 | 501.03 → 501.16 |
| anthropic | text-only | 10 | 30.05 → 29.99 | 11.20 → 11.18 | 7.31 → 6.96 | 505.08 → 505.29 |
| anthropic | tool-use | 1 | 30.02 → 29.93 | 7.00 → 7.00 | 11.12 → 10.53 | 501.17 → 501.36 |
| anthropic | tool-use | 10 | 30.44 → 30.44 | 46.40 → 46.38 | 22.76 → 20.77 | 509.04 → 513.57 |
| openai-compatible | text-only | 1 | 29.91 → 29.88 | 2.76 → 2.76 | 6.70 → 7.02 | 501.32 → 501.31 |
| openai-compatible | text-only | 10 | 30.02 → 30.02 | 8.43 → 8.40 | 7.62 → 7.13 | 504.81 → 506.45 |
| openai-compatible | tool-use | 1 | 30.01 → 29.96 | 9.72 → 9.72 | 11.29 → 10.92 | 505.22 → 505.09 |
| openai-compatible | tool-use | 10 | 30.41 → 30.42 | 73.08 → 73.04 | 29.46 → 27.03 | 509.23 → 507.63 |

## Peak RAM samples and CPU ranges

All three RSS samples are listed in repetition order; CPU range is minimum–maximum across the three runs.

| Provider | Workload | Tasks | Before RAM samples MiB | After RAM samples MiB | Before CPU range s | After CPU range s |
|---|---|---:|---|---|---|---|
| openai | text-only | 1 | 58.11, 57.78, 57.88 | 57.53, 57.84, 57.53 | 0.745–0.862 | 0.746–0.772 |
| openai | text-only | 10 | 91.39, 92.78, 92.53 | 93.72, 93.41, 92.25 | 4.402–4.756 | 4.454–4.610 |
| openai | tool-use | 1 | 74.86, 74.94, 74.20 | 75.25, 74.58, 76.14 | 0.849–0.962 | 0.898–0.934 |
| openai | tool-use | 10 | 143.02, 144.70, 152.14 | 150.58, 151.98, 153.39 | 5.315–5.610 | 5.352–5.603 |
| anthropic | text-only | 1 | 55.88, 56.27, 55.81 | 56.42, 55.53, 55.84 | 0.673–0.775 | 0.724–0.737 |
| anthropic | text-only | 10 | 90.17, 90.14, 89.53 | 90.19, 91.62, 92.45 | 4.603–4.684 | 4.490–4.630 |
| anthropic | tool-use | 1 | 73.61, 73.47, 74.98 | 72.78, 74.36, 73.52 | 0.800–0.944 | 0.859–0.897 |
| anthropic | tool-use | 10 | 148.03, 145.88, 139.83 | 150.61, 148.09, 142.45 | 5.576–5.841 | 5.473–5.971 |
| openai-compatible | text-only | 1 | 55.88, 55.44, 55.89 | 55.41, 56.58, 55.42 | 0.675–0.719 | 0.697–0.712 |
| openai-compatible | text-only | 10 | 85.91, 88.36, 87.86 | 86.06, 88.31, 86.86 | 4.142–4.308 | 4.149–4.275 |
| openai-compatible | tool-use | 1 | 81.52, 82.02, 81.58 | 80.86, 81.05, 81.22 | 0.882–0.920 | 0.864–0.909 |
| openai-compatible | tool-use | 10 | 233.42, 231.59, 239.83 | 251.41, 241.59, 228.44 | 5.775–5.903 | 5.772–5.937 |

## Reusable evidence

The archive includes both exact patches, harness, pinned baseline and binary hashes, test/build logs, raw accepted runs, all profiles and pre-generated profile summaries. Databases and generated fixture repositories are excluded. The delivered source is the measured candidate plus the separately tested selection correction; both patches and all binary hashes are retained. Development failure logs are excluded from accepted results.

Profile inspection does not require another workload run:

```sh
go tool pprof -top diagnostic-anthropic/tool-use-large-10-branch-1/cpu.pprof
go tool pprof -sample_index=alloc_space -top diagnostic-openai-compatible/tool-use-large-10-branch-1/mem.pprof
go tool pprof -sample_index=inuse_space -top diagnostic-openai/tool-use-large-10-branch-1/memory-turn-5.pprof
```

Heap captures are near-peak observations with timestamps, not exact accounting for every RSS byte. Sampling can miss brief peaks, profiling changes runtime behavior, and in-use profiles can lag GC. New code requires new measurements to validate an improvement.

## Targeted measurements

Median of three microbenchmark runs on the final implementations. These isolate stages, not full fallback/recovery/compaction requests. Prior approaches are implemented alongside the new helpers in each benchmark.

| Stage | Time ms before → after | Allocated bytes/op | Objects/op |
|---|---:|---:|---:|
| HTTP/fallback raw array encoding | 2.850 → 1.630 | 2,991,706 → 2,962,490 | 31 → 35 |
| Filtered/native schemas (final correction) | 0.114 → 0.015 | 217,642 → 213,666 | 20 → 6 |
| Large tool argument size | 0.680 → 0.403 | 1,192,440 → 0 | 9 → 0 |
| Legacy image recovery estimate | 2.485 → 3.288 | 2,577,078 → 2,669 | 74 → 43 |
| Native state: budget + send | 17.474 → 8.908 | 17,396,350 → 8,698,783 | 103 → 57 |
| Failed-conversation capture + decode | 8.298 → 1.725 | 15,049,055 → 4,105,618 | 168 → 166 |

Fixtures: HTTP encoding has 20 raw messages, each with 57,344 bytes of escaped text; schema selection chooses 16 of 32 schemas plus one native tool. Its reusable field is prepared outside the repeated selection. Native-state/recovery fixtures have 20 messages with 32 KiB NUL strings (large escaping expansion); they are stress inputs, not representative natural-language text. Tool-argument counting uses 458,752 bytes including HTML/newlines. Image recovery uses the legacy test fixture with inline image data. See the benchmark source for exact construction.

Legacy image recovery trades speed for memory: about 2.485 → 3.288 ms, while allocated bytes fall from 2,577,078 to 2,669. This regression remains; the operation is not used in the ordinary workload. HTTP encoding is faster but allocates approximately the same total bytes because the final body still must exist.

### Schema-selection correction after timing

The initial new helper reparsed every prepared schema on each selection. The targeted benchmark exposed a regression: 0.116 → 0.489 ms and 218,138 → 479,677 bytes. The final helper indexes once, retains numeric offsets (not old buffers), and copies only the chosen bytes. It checks permissions through the unchanged caller on every attempt. A regression test exercises repeated indices, native extras, out-of-range indices, and moving the field into a new request buffer.

This correction was made AFTER the 72-run comparison and six diagnostic captures. Those tables measure candidate.patch; the delivered source additionally includes selection-correction.patch. The correction has separate client/helper tests, race validation, the targeted measurements above, and six final smoke workloads. The full timing matrix was not repeated after this correction.

## Profile findings

Separate ten-task tool diagnostics, not part of timing medians:

- Anthropic `SendAgentic` cumulative allocation falls from 660.3 to 562.6 MiB. `json.Marshal` cumulative allocation falls from 365.6 to 177.0 MiB. Raw envelope assembly avoids some reformatting; it still allocates the outgoing body.
- Compatible history-building cumulative allocation falls from 253.7 to 179.4 MiB. Borrowed JSON field inspection reduces intermediate decode copies. The candidate's turn-5 heap still shows about 33.0 MiB of SQLite text and 31.2 MiB of decoded strings; this work does not remove all simultaneously retained history representations.
- Near-peak snapshots differ in timing and GC state. They explain allocation owners, not every RSS change. The cause of the small concurrent RAM regressions is not proven by these samples.
- Recovery/native compaction do not run in the ordinary timing fixture; their improvements are supported by isolated benchmarks and correctness tests, not these end-to-end profiles.


## Per-turn medians

Turn 0 is the initial request; turns 1–5 are follow-ups. Totals cover all concurrent tasks. Heap is sampled Go heap, not process RAM.

### openai: text-only, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 97.2 → 92.6 | 3.36 → 3.33 | 8364 → 8380 | 5.18 → 5.29 | 4.788 → 4.800 |
| 1 | 136.9 → 127.7 | 4.40 → 4.12 | 11810 → 11586 | 6.17 → 6.23 | 4.965 → 4.961 |
| 2 | 138.4 → 139.0 | 4.56 → 4.34 | 12366 → 12227 | 6.57 → 6.97 | 4.945 → 4.961 |
| 3 | 131.9 → 136.1 | 4.87 → 4.71 | 12605 → 12485 | 6.92 → 6.67 | 4.962 → 4.968 |
| 4 | 130.4 → 132.1 | 5.25 → 5.07 | 12933 → 12807 | 7.90 → 8.05 | 4.958 → 4.968 |
| 5 | 136.4 → 135.5 | 5.81 → 5.60 | 13375 → 13071 | 7.76 → 8.28 | 4.978 → 4.967 |

### openai: text-only, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 577.9 → 588.4 | 30.06 → 29.79 | 73362 → 73129 | 12.32 → 11.54 | 4.829 → 4.830 |
| 1 | 780.7 → 793.2 | 38.66 → 37.45 | 115429 → 114636 | 17.26 → 18.01 | 5.016 → 4.994 |
| 2 | 751.0 → 766.1 | 41.87 → 40.60 | 122221 → 121017 | 20.55 → 18.80 | 5.034 → 5.002 |
| 3 | 820.0 → 824.5 | 45.26 → 43.96 | 125142 → 123684 | 23.71 → 23.89 | 4.998 → 5.018 |
| 4 | 819.1 → 788.0 | 49.40 → 47.70 | 128834 → 126580 | 26.38 → 26.32 | 5.007 → 4.982 |
| 5 | 817.5 → 822.1 | 53.80 → 52.75 | 132326 → 130328 | 30.68 → 29.76 | 5.035 → 5.018 |

### openai: tool-use, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 117.2 → 114.9 | 10.15 → 9.95 | 11824 → 11681 | 8.40 → 8.69 | 4.856 → 4.842 |
| 1 | 147.7 → 161.4 | 12.56 → 12.19 | 14913 → 14775 | 9.03 → 9.87 | 5.008 → 5.002 |
| 2 | 152.4 → 157.2 | 12.78 → 12.44 | 15812 → 15579 | 10.92 → 9.64 | 5.024 → 4.996 |
| 3 | 165.6 → 163.2 | 13.40 → 12.94 | 16463 → 16093 | 10.14 → 10.44 | 5.016 → 5.019 |
| 4 | 154.4 → 163.2 | 14.08 → 13.82 | 17146 → 16869 | 11.35 → 9.93 | 5.013 → 5.013 |
| 5 | 171.9 → 162.0 | 14.86 → 14.54 | 17946 → 17610 | 11.22 → 10.73 | 5.006 → 5.032 |

### openai: tool-use, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 733.5 → 724.1 | 88.09 → 90.75 | 98562 → 98160 | 32.48 → 33.32 | 4.893 → 4.891 |
| 1 | 913.5 → 917.7 | 119.20 → 116.45 | 146156 → 144623 | 35.18 → 35.72 | 5.063 → 5.053 |
| 2 | 950.4 → 929.7 | 123.00 → 120.63 | 156429 → 154386 | 42.60 → 45.34 | 5.080 → 5.062 |
| 3 | 950.9 → 976.8 | 128.86 → 127.02 | 163033 → 159884 | 44.79 → 50.44 | 5.072 → 5.089 |
| 4 | 984.2 → 982.4 | 135.77 → 133.89 | 169698 → 166646 | 46.93 → 50.07 | 5.098 → 5.064 |
| 5 | 977.7 → 991.0 | 144.12 → 141.56 | 177926 → 173819 | 48.26 → 55.44 | 5.084 → 5.085 |

### anthropic: text-only, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 91.4 → 87.5 | 3.94 → 3.57 | 10863 → 15746 | 5.61 → 5.13 | 4.855 → 4.847 |
| 1 | 127.2 → 125.5 | 4.68 → 4.55 | 14168 → 19110 | 6.80 → 6.61 | 4.996 → 4.990 |
| 2 | 132.5 → 126.5 | 5.34 → 5.08 | 14417 → 19399 | 7.10 → 6.83 | 4.998 → 4.987 |
| 3 | 130.6 → 119.3 | 6.04 → 5.51 | 14606 → 19564 | 7.27 → 7.39 | 5.010 → 4.973 |
| 4 | 132.1 → 137.8 | 6.72 → 6.08 | 14769 → 19765 | 7.89 → 7.42 | 5.002 → 4.994 |
| 5 | 135.5 → 131.6 | 7.52 → 6.56 | 14942 → 19961 | 8.28 → 7.87 | 5.011 → 4.997 |

### anthropic: text-only, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 601.7 → 599.1 | 34.12 → 30.82 | 95121 → 144061 | 11.38 → 11.33 | 4.868 → 4.844 |
| 1 | 776.4 → 764.9 | 43.18 → 41.13 | 138973 → 188099 | 16.24 → 15.85 | 5.018 → 5.024 |
| 2 | 791.0 → 792.0 | 49.46 → 46.49 | 143419 → 192975 | 18.67 → 19.64 | 5.035 → 5.026 |
| 3 | 810.4 → 797.3 | 55.84 → 51.55 | 145018 → 194727 | 25.18 → 23.39 | 5.042 → 5.036 |
| 4 | 827.1 → 808.0 | 63.08 → 56.38 | 146982 → 196835 | 27.11 → 27.46 | 5.035 → 5.017 |
| 5 | 827.7 → 830.2 | 70.86 → 61.55 | 148617 → 199003 | 30.07 → 32.14 | 5.040 → 5.041 |

### anthropic: tool-use, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 107.5 → 114.2 | 11.34 → 10.40 | 14500 → 18830 | 8.15 → 8.75 | 4.850 → 4.864 |
| 1 | 152.0 → 152.9 | 13.77 → 13.28 | 17530 → 22248 | 9.45 → 9.25 | 5.033 → 5.017 |
| 2 | 159.4 → 153.4 | 15.57 → 14.26 | 18027 → 22696 | 10.60 → 9.23 | 5.030 → 5.021 |
| 3 | 157.5 → 152.2 | 17.71 → 15.52 | 18363 → 23181 | 10.97 → 10.57 | 5.024 → 5.009 |
| 4 | 164.8 → 156.9 | 19.50 → 16.54 | 18795 → 23721 | 11.51 → 11.11 | 5.038 → 5.011 |
| 5 | 162.4 → 146.5 | 21.42 → 17.67 | 19153 → 24188 | 12.31 → 11.46 | 5.046 → 5.018 |

### anthropic: tool-use, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 752.4 → 747.4 | 101.23 → 93.05 | 122095 → 166880 | 32.68 → 32.14 | 4.898 → 4.920 |
| 1 | 949.2 → 960.6 | 129.00 → 122.84 | 172181 → 218279 | 41.46 → 38.50 | 5.079 → 5.095 |
| 2 | 988.8 → 988.2 | 147.59 → 134.59 | 178807 → 226145 | 48.51 → 46.83 | 5.102 → 5.099 |
| 3 | 978.1 → 1015.8 | 167.30 → 145.45 | 182656 → 230899 | 51.01 → 52.99 | 5.103 → 5.105 |
| 4 | 1022.2 → 980.7 | 184.29 → 158.35 | 186174 → 235453 | 54.88 → 59.98 | 5.130 → 5.114 |
| 5 | 1048.7 → 997.5 | 202.51 → 170.56 | 189743 → 240542 | 59.03 → 61.63 | 5.116 → 5.133 |

### openai-compatible: text-only, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 82.1 → 85.2 | 2.69 → 2.70 | 10363 → 10405 | 4.80 → 4.85 | 4.846 → 4.847 |
| 1 | 125.3 → 123.6 | 3.63 → 3.48 | 14343 → 13953 | 6.01 → 6.79 | 5.003 → 5.014 |
| 2 | 121.1 → 120.8 | 3.99 → 3.87 | 14613 → 14380 | 6.44 → 6.28 | 5.014 → 5.003 |
| 3 | 127.1 → 130.0 | 4.50 → 4.22 | 14817 → 14530 | 6.54 → 7.02 | 5.004 → 5.009 |
| 4 | 119.8 → 119.4 | 4.85 → 4.55 | 14963 → 14648 | 7.05 → 6.57 | 5.008 → 5.002 |
| 5 | 125.6 → 126.8 | 5.18 → 4.83 | 15086 → 14805 | 7.41 → 7.20 | 5.022 → 5.015 |

### openai-compatible: text-only, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 516.4 → 522.9 | 22.18 → 22.16 | 92037 → 91996 | 11.74 → 11.83 | 4.858 → 4.854 |
| 1 | 718.4 → 691.6 | 33.26 → 30.92 | 141001 → 137877 | 15.74 → 17.74 | 5.022 → 5.000 |
| 2 | 750.9 → 726.6 | 37.46 → 34.54 | 145565 → 142296 | 17.23 → 18.76 | 5.031 → 5.027 |
| 3 | 736.0 → 731.7 | 41.24 → 37.87 | 147071 → 144161 | 22.05 → 21.93 | 5.032 → 5.049 |
| 4 | 765.5 → 755.6 | 44.96 → 41.28 | 148736 → 145932 | 23.82 → 24.20 | 5.031 → 5.044 |
| 5 | 738.0 → 757.0 | 48.79 → 44.57 | 150411 → 147333 | 27.23 → 26.36 | 5.040 → 5.023 |

### openai-compatible: tool-use, 1 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 100.4 → 104.6 | 11.64 → 11.67 | 13165 → 13202 | 10.25 → 8.48 | 4.855 → 4.853 |
| 1 | 152.1 → 154.7 | 16.19 → 15.66 | 17140 → 16498 | 11.93 → 12.64 | 5.027 → 5.031 |
| 2 | 159.9 → 150.9 | 19.67 → 18.38 | 17690 → 17022 | 14.42 → 13.25 | 5.029 → 5.014 |
| 3 | 163.2 → 161.4 | 23.22 → 21.30 | 17845 → 17276 | 16.36 → 16.11 | 5.031 → 5.022 |
| 4 | 162.8 → 161.7 | 26.12 → 23.70 | 18152 → 17705 | 18.30 → 16.60 | 5.042 → 5.019 |
| 5 | 170.6 → 166.7 | 29.26 → 26.23 | 18499 → 17946 | 20.52 → 19.71 | 5.052 → 5.046 |

### openai-compatible: tool-use, 10 task(s)

| Turn | CPU ms before → after | Allocated MiB | Objects allocated | Sampled peak heap MiB | Elapsed seconds |
|---:|---:|---:|---:|---:|---:|
| 0 | 685.9 → 677.1 | 103.38 → 101.35 | 109798 → 110124 | 34.45 → 37.25 | 4.907 → 4.905 |
| 1 | 913.8 → 914.2 | 152.32 → 145.18 | 168153 → 161734 | 56.45 → 68.78 | 5.096 → 5.067 |
| 2 | 990.1 → 982.3 | 185.03 → 173.50 | 174037 → 168152 | 87.15 → 88.97 | 5.096 → 5.074 |
| 3 | 1065.8 → 1010.0 | 222.84 → 202.69 | 177191 → 171274 | 110.24 → 112.27 | 5.096 → 5.114 |
| 4 | 1098.0 → 1073.4 | 249.83 → 225.19 | 179722 → 174181 | 137.63 → 143.08 | 5.105 → 5.097 |
| 5 | 1095.3 → 1144.4 | 280.26 → 249.82 | 182624 → 177362 | 151.48 → 155.96 | 5.108 → 5.117 |
