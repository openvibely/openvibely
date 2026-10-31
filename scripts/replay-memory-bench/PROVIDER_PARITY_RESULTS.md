# Remaining provider optimization parity

Comparison: **previous task branch `3ccd98cfc75931e2edaa3ea82c5a8afc2f0f952b` → that same revision plus the provider-parity patch**. This is not main versus branch. All providers use the same revisions and explicit context budget. Raw directory labels `main` and `branch` mean before and after.

## Verdict

Keep the changes for the two SSE providers: allocated bytes and median peak process RAM fell in all eight Anthropic/compatible scenarios. Compatible CPU medians fell in all four scenarios. Anthropic CPU was mixed: single-task tool CPU increased 0.900 → 0.919 seconds; ten-task tool CPU decreased 5.959 → 5.896 seconds. OpenAI Responses was the regression control and is broadly unchanged in allocations; its text CPU medians increased slightly. This is not a universal speed or RAM improvement.

The ten-task compatible tool RAM median fell 257.70 → 236.45 MiB, but the samples overlap (before 232.53–275.58; after 233.59–237.33 MiB). Anthropic ten-task tool RAM samples do not overlap (before 164.94–172.42; after 147.39–160.92 MiB). Three repetitions do not establish statistical certainty.

Delivery tradeoffs remain visible: Anthropic ten-task text median longest live gap increased 7.80 → 11.64 ms; its persistence gap increased 503.88 → 506.15 ms. Compatible single-task tool live gap increased 8.87 → 9.11 ms. Database sizes are essentially unchanged. Fixed mock pacing dominates elapsed time; these are not real vendor latency measurements.

## What is shared

- Shared `streamdecode` byte framing and 64 KiB initial scanner storage; each provider keeps its previous maximum event size and framing acceptance rules.
- Compatible stream chunks use typed fields; reasoning text and tool arguments use builders rather than repeated string concatenation.
- Compatible parsing and failure recovery use one text builder. Anthropic recovery borrows immutable parser views for ordinary text and uses separate filtered fragments when tool markup requires it. Boundaries preserve exactly delivered text.
- `requestjson.Metadata` shares prepared tool schemas between budgeting and sending when the tool set is unchanged. Per-attempt permission filtering still runs; changed tool sets and Anthropic native web-tool schemas use the appropriate encoding path.
- Native compatible replay rows are reused as raw JSON only while their fields remain unchanged. Edits, truncation, legacy rows, image content and unsupported fields retain normal encoding. This preserves raw wire JSON; it does not eliminate every initial decode needed for history semantics.
- Shared image adjustment scanning now also serves Anthropic content-block and compatible multimodal service estimators, without constructing another large wrapper/content copy.

Provider formats, reasoning fields, tool-result structures and API limits remain provider-specific. There is no new migration. This does not remove every serialization or every provider-specific estimator.

## Protocol and correctness

72 timing runs: three providers × text/tool workloads × 1/10 parallel tasks × before/after × three repetitions. Each task completes six sequential turns: 2,376 completed task turns in total. Large answers are 48 KiB; tool scenarios read a real 512 KiB file each turn. Inference is mocked; backend, SQLite, tool execution, replay and stream clients are real. Both versions use the same harness and a 2,000,000-token context window with 1,500,000 compaction threshold. This round does not measure compaction performance or real vendor latency.

Version order reverses in repetition two. No builds or tests run alongside timing. Six small candidate smoke scenarios and six separate ten-task tool diagnostic runs are kept apart from the timing medians. Diagnostics capture CPU, allocation and phase-triggered near-peak heaps. Profiles and source metadata are preserved in the evidence archive.

Relevant LLM, provider-client, persistence and service test suites pass, along with race checks for the new helpers, build and vet. Tests cover borrowed text boundaries, filtered recovery, scanner growth/limits, typed chunk/tool arguments, raw replay invalidation, exact schema/image-budget equivalence, repeated encoding, cancellation, tools and recovery. This is not a claim that the entire repository test suite was run.

## Six-turn results

Values are medians of three fresh processes. CPU/allocation totals cover all tasks combined; elapsed time is the sum of the six request waves. Peak process RAM includes setup and final verification and is sampled externally. No pooled cross-provider score.

| Provider | Workload | Tasks | CPU seconds before → after | Allocated MiB | Peak RAM MiB |
|---|---|---:|---:|---:|---:|
| openai | text-only | 1 | 0.747 → 0.756 | 28.35 → 28.50 | 57.30 → 58.16 |
| openai | text-only | 10 | 4.487 → 4.588 | 258.69 → 258.73 | 94.72 → 94.06 |
| openai | tool-use | 1 | 0.903 → 0.896 | 77.85 → 77.91 | 74.56 → 75.00 |
| openai | tool-use | 10 | 5.407 → 5.438 | 739.44 → 739.54 | 149.03 → 141.88 |
| anthropic | text-only | 1 | 0.707 → 0.706 | 46.58 → 34.13 | 57.66 → 55.55 |
| anthropic | text-only | 10 | 4.813 → 4.582 | 440.08 → 316.06 | 105.31 → 95.03 |
| anthropic | tool-use | 1 | 0.900 → 0.919 | 120.21 → 99.41 | 75.61 → 74.50 |
| anthropic | tool-use | 10 | 5.959 → 5.896 | 1150.00 → 941.10 | 167.42 → 153.92 |
| openai-compatible | text-only | 1 | 0.763 → 0.721 | 39.42 → 24.52 | 57.55 → 56.05 |
| openai-compatible | text-only | 10 | 4.738 → 4.289 | 373.55 → 226.66 | 106.02 → 87.09 |
| openai-compatible | tool-use | 1 | 0.949 → 0.812 | 143.47 → 124.78 | 83.91 → 82.47 |
| openai-compatible | tool-use | 10 | 6.192 → 5.942 | 1385.95 → 1195.57 | 257.70 → 236.45 |

## Elapsed time, storage and delivery gaps

These are observer gaps, not browser latency. Database size is measured after final WAL checkpoint.

| Provider | Workload | Tasks | Elapsed seconds | DB MiB | Median longest live gap ms | Median longest persistence gap ms |
|---|---|---:|---:|---:|---:|---:|
| openai | text-only | 1 | 27.17 → 27.18 | 3.54 → 3.54 | 6.15 → 6.06 | 501.06 → 501.38 |
| openai | text-only | 10 | 29.80 → 28.39 | 16.09 → 16.07 | 6.75 → 7.16 | 507.68 → 508.96 |
| openai | tool-use | 1 | 29.64 → 29.00 | 8.17 → 8.16 | 6.67 → 6.66 | 501.15 → 501.24 |
| openai | tool-use | 10 | 27.69 → 27.73 | 57.54 → 57.59 | 19.93 → 18.70 | 509.57 → 506.62 |
| anthropic | text-only | 1 | 27.07 → 27.18 | 2.99 → 2.99 | 6.29 → 6.33 | 501.04 → 501.21 |
| anthropic | text-only | 10 | 27.44 → 27.47 | 11.19 → 11.20 | 7.80 → 11.64 | 503.88 → 506.15 |
| anthropic | tool-use | 1 | 27.26 → 27.23 | 6.99 → 6.99 | 11.25 → 10.89 | 501.23 → 501.17 |
| anthropic | tool-use | 10 | 27.83 → 27.80 | 46.35 → 46.32 | 22.18 → 20.98 | 511.46 → 508.91 |
| openai-compatible | text-only | 1 | 26.98 → 27.17 | 2.75 → 2.75 | 6.27 → 6.25 | 501.60 → 504.27 |
| openai-compatible | text-only | 10 | 27.39 → 27.30 | 8.34 → 8.35 | 8.32 → 6.62 | 506.27 → 505.07 |
| openai-compatible | tool-use | 1 | 27.32 → 27.32 | 9.71 → 9.71 | 8.87 → 9.11 | 501.26 → 501.32 |
| openai-compatible | tool-use | 10 | 27.87 → 27.82 | 73.03 → 73.05 | 31.21 → 28.56 | 512.92 → 507.40 |

## Peak RAM samples and CPU ranges

All three RSS samples are listed in repetition order; CPU range is minimum–maximum across the three runs.

| Provider | Workload | Tasks | Before RAM samples MiB | After RAM samples MiB | Before CPU range s | After CPU range s |
|---|---|---:|---|---|---|---|
| openai | text-only | 1 | 57.30, 57.25, 57.97 | 57.89, 58.66, 58.16 | 0.715–0.769 | 0.754–0.772 |
| openai | text-only | 10 | 95.48, 94.72, 94.28 | 94.06, 92.83, 95.53 | 4.374–4.566 | 4.355–4.656 |
| openai | tool-use | 1 | 75.19, 74.55, 74.56 | 75.00, 73.64, 75.36 | 0.852–0.920 | 0.808–0.916 |
| openai | tool-use | 10 | 149.03, 143.08, 151.97 | 141.88, 151.16, 140.73 | 5.358–5.698 | 5.383–5.531 |
| anthropic | text-only | 1 | 57.66, 57.06, 58.38 | 55.30, 55.55, 56.88 | 0.690–0.737 | 0.627–0.745 |
| anthropic | text-only | 10 | 107.14, 105.31, 103.33 | 95.53, 89.84, 95.03 | 4.498–5.059 | 4.453–4.633 |
| anthropic | tool-use | 1 | 75.03, 76.19, 75.61 | 73.97, 76.03, 74.50 | 0.868–1.029 | 0.845–1.063 |
| anthropic | tool-use | 10 | 164.94, 172.42, 167.42 | 153.92, 147.39, 160.92 | 5.882–6.260 | 5.647–6.210 |
| openai-compatible | text-only | 1 | 57.06, 57.55, 58.06 | 56.05, 55.62, 56.33 | 0.513–0.842 | 0.488–0.853 |
| openai-compatible | text-only | 10 | 106.03, 106.02, 105.38 | 88.06, 87.09, 86.83 | 4.500–4.756 | 3.700–4.512 |
| openai-compatible | tool-use | 1 | 83.91, 84.06, 82.67 | 82.94, 81.69, 82.47 | 0.870–0.957 | 0.686–0.947 |
| openai-compatible | tool-use | 10 | 275.58, 232.53, 257.70 | 237.33, 236.45, 233.59 | 6.123–6.339 | 5.569–6.017 |

## What the separate profiles show

These are sampled cumulative allocated bytes from separate ten-task tool diagnostics, not timing medians or peak retained bytes. Totals include setup, verification and profiling itself. Do not add cumulative parent/child rows together.

| Allocation path | Before MiB | After MiB |
|---|---:|---:|
| Anthropic streaming parser, cumulative | 238.56 | 88.19 |
| Compatible streaming parser, cumulative | 269.52 | 77.04 |
| Compatible JSON decoding under stream parser | 66.02 | 8.51 |
| Compatible initial history construction, cumulative | 279.38 | 265.72 |

The parser reductions support the scanner, byte framing, typed decoding and recovery-buffer changes as a combined improvement. The profile does not isolate each of the eight changes independently. Native compatible replay still requires initial decoding: wire reuse avoids re-encoding unchanged rows, not the first decode. Anthropic still has significant request serialization allocation. Tool-schema reuse does not eliminate metadata-header assembly or encoding of changed/native tool sets.

`focus-parser-alloc.txt`, `focus-metadata-alloc.txt` and `focus-recovery-alloc.txt` are saved beside each SSE diagnostic profile. Full flat/cumulative summaries for CPU, allocation and every captured heap are included. `phase-timing` and `phase-diagnostic` retain phase-aligned RSS/capture data; diagnostic peaks are excluded from the timing table.

Reasoning fragments, images, filtered tools, retries and interrupted streams are correctness-tested but are not separate repeated performance workloads here. No claim is made that every one of those cases improved. All eight implementation areas are included in the measured source patch; no production changes were made after building it.

## Reusable evidence

The archive includes the exact patch, harness, pinned baseline and binary hashes, test/build logs, raw accepted runs, all profiles and pre-generated profile summaries. Databases and generated fixture repositories are excluded. The final source matches the measured candidate patch; development failure logs are retained separately and excluded from results.

Profile inspection does not require another workload run:

```sh
go tool pprof -top diagnostic-anthropic/tool-use-large-10-branch-1/cpu.pprof
go tool pprof -sample_index=alloc_space -top diagnostic-openai-compatible/tool-use-large-10-branch-1/mem.pprof
go tool pprof -sample_index=inuse_space -top diagnostic-openai/tool-use-large-10-branch-1/memory-turn-5.pprof
```

Heap captures are near-peak observations with timestamps, not exact accounting for every RSS byte. Sampling can miss brief peaks, profiling changes runtime behavior, and in-use profiles can lag GC. New code requires new measurements to validate an improvement.
