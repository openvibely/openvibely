# Shared provider request and history paths

Comparison: **previous task branch `036d7d15bbe8072f8f7b11e22a1dba82f9725bb1` → that same revision plus the shared-path patch**. This is not main versus branch. All providers use the same revisions and explicit context budget. Raw directory labels `main` and `branch` mean before and after.

## Verdict

Keep the shared paths: allocated bytes fell in all 12 scenarios, and tool-use CPU fell for all three providers. This is not a universal RAM or latency improvement.

- OpenAI text CPU rose from 0.747 to 0.777 seconds (one task) and 4.560 to 4.620 seconds (ten); text RAM was essentially unchanged.
- Compatible ten-task tool RAM rose from 252.03 to 256.34 MiB; ten-task text RAM rose from 105.16 to 106.80 MiB. The allocation savings do not establish a RAM improvement there.
- Anthropic improved CPU, allocations and median peak RAM in all four workloads in this batch.
- Database size and elapsed time were essentially unchanged. Delivery gaps varied: Anthropic ten-task tool persistence gap rose 505.43 to 513.59 ms; OpenAI single-task tool live gap rose 6.72 to 10.49 ms.
- Three repetitions are not proof of statistical significance for small differences. The fixed mock streaming cadence dominates elapsed time.

## Diagnostic findings

Separate ten-task tool allocation profiles support the intended mechanism. These are sampled cumulative allocations in diagnostics, not the timing medians above:

| Allocation stack | Before MiB | After MiB |
|---|---:|---:|
| OpenAI history reconstruction | 56.03 | 28.20 |
| Anthropic history reconstruction | 21.46 | 13.05 |
| Compatible history reconstruction | 560.30 | 277.46 |

The shared cache eliminates the second reconstruction for sending. Prepared request encoding moves work into `requestjson.Prepare`/`Marshal`, so comparing the old send function alone would misattribute the gain. Provider metadata/tools are still encoded separately; this does not eliminate every request copy. Compatible still has substantial SQLite replay text and decoded JSON strings in its near-peak heap. Heap snapshots occur at different execution/GC points and cannot be treated as exact paired live-memory accounting or as an explanation of every RSS difference.

## Final review correction and source provenance

After the measured binaries were built, review found OpenAI task/direct recovery did not consume the new cache, and an empty send could leave a pre-compaction cache entry behind. `recovery-followup.patch` fixes those boundary paths and adds regression tests; it is saved separately from the unchanged measured `candidate.patch`. These corrections were tested after timing. They have no claimed measured performance benefit. The timing results describe the shared-path candidate before that small final correction, rather than pretending the final source and measured binary are byte-identical.

## What is shared

- `internal/llm/requestjson`: prepared message encoding for SSE preflight and sending, plus raw-item encoding used by OpenAI Responses WebSockets. Anthropic and Completions encode message arrays once per prepared turn; the final body and retry options share the encoded message storage.
- `internal/llm/preparedhistory`: one request-scoped decoded-history lifecycle. Budgeting reads prepared history; sending takes ownership and removes the cached value. Changed histories invalidate the value, and nested provider calls create new scopes.
- `internal/llm/tokenestimate`: one prepared-array image adjustment scanner, with provider-specific image names/costs supplied as parameters. It scans encoded text without copying every message content solely for budgeting.
- Removed a Completions history clone immediately preceding nonmutating repair, which already constructs a separate slice. Public history behavior and copies needed to isolate caller-owned mutable tool calls remain.

Provider formats, reasoning fields, tool-result structures and API limits remain provider-specific. There is no new migration. This does not remove every serialization or every provider-specific estimator.

## Protocol and correctness

72 timing runs: three providers × text/tool workloads × 1/10 parallel tasks × before/after × three repetitions. Each task completes six sequential turns: 2,376 completed task turns in total. Large answers are 48 KiB; tool scenarios read a real 512 KiB file each turn. Inference is mocked; backend, SQLite, tool execution, replay and stream clients are real. Both versions use the same harness and a 2,000,000-token context window with 1,500,000 compaction threshold. This round does not measure compaction performance or real vendor latency.

Version order reverses in repetition two. No builds or tests run alongside timing. Six small candidate smoke scenarios and six separate ten-task tool diagnostic runs are kept apart from the timing medians. Diagnostics capture CPU, allocation and phase-triggered near-peak heaps. Profiles and source metadata are preserved in the evidence archive.

Relevant LLM, provider-client, persistence and service test suites pass, along with race checks for the new helpers, build and vet. Tests cover invalidation/handoff, exact request JSON and image-budget equivalence, quoted text, raw Responses items, repeated encoding, cancellation, tools and recovery. This is not a claim that the entire repository test suite was run.

## Six-turn results

Values are medians of three fresh processes. CPU/allocation totals cover all tasks combined; elapsed time is the sum of the six request waves. Peak process RAM includes setup and final verification and is sampled externally. No pooled cross-provider score.

| Provider | Workload | Tasks | CPU seconds before → after | Allocated MiB | Peak RAM MiB |
|---|---|---:|---:|---:|---:|
| openai | text-only | 1 | 0.747 → 0.777 | 29.98 → 28.01 | 58.05 → 57.89 |
| openai | text-only | 10 | 4.560 → 4.620 | 275.20 → 257.87 | 92.39 → 92.75 |
| openai | tool-use | 1 | 0.886 → 0.851 | 80.59 → 77.72 | 74.50 → 74.08 |
| openai | tool-use | 10 | 5.637 → 5.454 | 767.69 → 741.97 | 152.38 → 142.70 |
| anthropic | text-only | 1 | 0.779 → 0.729 | 55.48 → 46.17 | 58.61 → 57.70 |
| anthropic | text-only | 10 | 4.874 → 4.678 | 523.30 → 440.79 | 108.56 → 103.97 |
| anthropic | tool-use | 1 | 0.920 → 0.885 | 163.56 → 120.40 | 76.66 → 75.08 |
| anthropic | tool-use | 10 | 6.024 → 5.848 | 1516.56 → 1155.12 | 176.70 → 165.81 |
| openai-compatible | text-only | 1 | 0.780 → 0.763 | 42.83 → 39.73 | 58.25 → 57.75 |
| openai-compatible | text-only | 10 | 4.882 → 4.708 | 414.34 → 373.30 | 105.16 → 106.80 |
| openai-compatible | tool-use | 1 | 0.990 → 0.911 | 170.23 → 142.41 | 84.94 → 83.41 |
| openai-compatible | tool-use | 10 | 6.727 → 6.260 | 1664.18 → 1380.24 | 252.03 → 256.34 |

## Elapsed time, storage and delivery gaps

These are observer gaps, not browser latency. Database size is measured after final WAL checkpoint.

| Provider | Workload | Tasks | Elapsed seconds | DB MiB | Median longest live gap ms | Median longest persistence gap ms |
|---|---|---:|---:|---:|---:|---:|
| openai | text-only | 1 | 27.14 → 27.16 | 3.54 → 3.54 | 8.22 → 6.10 | 501.07 → 501.26 |
| openai | text-only | 10 | 27.31 → 27.36 | 15.98 → 16.00 | 6.49 → 8.12 | 509.98 → 505.48 |
| openai | tool-use | 1 | 27.22 → 27.17 | 8.16 → 8.16 | 6.72 → 10.49 | 501.10 → 501.31 |
| openai | tool-use | 10 | 27.65 → 27.67 | 57.55 → 57.55 | 21.85 → 19.98 | 509.70 → 510.09 |
| anthropic | text-only | 1 | 27.10 → 27.14 | 2.99 → 2.99 | 7.26 → 6.30 | 501.41 → 501.29 |
| anthropic | text-only | 10 | 27.36 → 27.37 | 11.20 → 11.19 | 12.40 → 8.19 | 505.24 → 505.46 |
| anthropic | tool-use | 1 | 27.17 → 27.20 | 6.99 → 6.99 | 13.51 → 11.39 | 501.33 → 501.07 |
| anthropic | tool-use | 10 | 27.82 → 27.80 | 46.39 → 46.29 | 27.31 → 23.26 | 505.43 → 513.59 |
| openai-compatible | text-only | 1 | 27.08 → 27.12 | 2.75 → 2.75 | 6.20 → 6.42 | 502.94 → 502.69 |
| openai-compatible | text-only | 10 | 27.30 → 27.36 | 8.35 → 8.31 | 6.51 → 7.29 | 505.99 → 504.73 |
| openai-compatible | tool-use | 1 | 27.41 → 27.37 | 9.71 → 9.71 | 17.06 → 13.99 | 501.27 → 501.49 |
| openai-compatible | tool-use | 10 | 27.82 → 27.78 | 73.02 → 73.00 | 26.22 → 25.45 | 510.96 → 505.76 |

## Peak RAM samples and CPU ranges

All three RSS samples are listed in repetition order; CPU range is minimum–maximum across the three runs.

| Provider | Workload | Tasks | Before RAM samples MiB | After RAM samples MiB | Before CPU range s | After CPU range s |
|---|---|---:|---|---|---|---|
| openai | text-only | 1 | 58.05, 57.28, 58.17 | 57.89, 57.83, 58.66 | 0.739–0.792 | 0.749–0.822 |
| openai | text-only | 10 | 93.81, 92.12, 92.39 | 92.75, 92.12, 93.17 | 4.282–4.889 | 4.318–4.679 |
| openai | tool-use | 1 | 74.58, 73.44, 74.50 | 73.16, 74.20, 74.08 | 0.868–0.978 | 0.795–0.912 |
| openai | tool-use | 10 | 152.38, 153.33, 152.02 | 142.86, 141.97, 142.70 | 5.148–5.673 | 5.061–5.624 |
| anthropic | text-only | 1 | 59.11, 58.58, 58.61 | 57.39, 57.70, 57.72 | 0.760–0.847 | 0.580–0.759 |
| anthropic | text-only | 10 | 108.56, 105.80, 110.06 | 102.12, 104.67, 103.97 | 4.363–5.011 | 4.514–4.927 |
| anthropic | tool-use | 1 | 76.58, 76.92, 76.66 | 75.56, 75.08, 74.09 | 0.733–0.947 | 0.870–0.929 |
| anthropic | tool-use | 10 | 168.27, 176.70, 180.00 | 170.23, 159.77, 165.81 | 5.842–6.245 | 5.257–6.114 |
| openai-compatible | text-only | 1 | 59.23, 58.03, 58.25 | 57.75, 58.14, 57.69 | 0.681–0.783 | 0.752–0.772 |
| openai-compatible | text-only | 10 | 105.22, 105.16, 104.14 | 107.28, 102.38, 106.80 | 4.276–4.890 | 4.615–4.882 |
| openai-compatible | tool-use | 1 | 83.14, 84.94, 85.00 | 82.50, 83.91, 83.41 | 0.935–1.008 | 0.837–0.970 |
| openai-compatible | tool-use | 10 | 250.67, 252.03, 253.53 | 244.81, 256.34, 269.31 | 6.598–6.849 | 6.112–6.378 |

## Reusable evidence

The archive includes the exact patch, harness, pinned baseline and binary hashes, test/build logs, raw accepted runs, all profiles and pre-generated profile summaries. Databases and generated fixture repositories are excluded. Exploratory microbenchmarks are retained separately and are not substituted for final end-to-end results.

Profile inspection does not require another workload run:

```sh
go tool pprof -top diagnostic-anthropic/tool-use-large-10-branch-1/cpu.pprof
go tool pprof -sample_index=alloc_space -top diagnostic-openai-compatible/tool-use-large-10-branch-1/mem.pprof
go tool pprof -sample_index=inuse_space -top diagnostic-openai/tool-use-large-10-branch-1/memory-turn-5.pprof
```

Heap captures are near-peak observations with timestamps, not exact accounting for every RSS byte. Sampling can miss brief peaks, profiling changes runtime behavior, and in-use profiles can lag GC. New code requires new measurements to validate an improvement.

Full per-turn tables are in `per-turn/RESULTS.md`; timing and diagnostic phase tables are in `phase-timing/PHASES.md` and `phase-diagnostic/PHASES.md`. The evidence archive contains all of them.
