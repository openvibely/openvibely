# OpenAI-compatible retention fix

Comparison: previous task branch `e7c495c38e8b240fa10884ae10a2adfa4bdcf40b` → that revision plus the saved candidate patch. **Neither side is main.** Runner directory labels `main` and `branch` mean before and after in this experiment.

Candidate production patch SHA256: `9e38a1dfe2ff036b1664d3b1b97c29bb3ecef5b8e604ff4b7f5b4f0262a70d00`.

The change keeps assistant text separate from rendered tool output, preserves empty assistant answers, and preserves assistant text quoting tool-marker strings. Completed native Completions task histories omit redundant display output. Failed, cancelled, empty, legacy and foreign histories keep their fallback output. Full UI output and native tool replay remain intact. No migration or stored-history rewrite is performed.

## Results

Three repetitions, fresh processes, six sequential turns per task, 1 or 10 concurrent tasks. Each turn reads a real 512 KiB file and receives a simulated 48 KiB Chat Completions SSE answer. Backend, tools and SQLite are real; model inference is mocked. No compaction or hidden retries. Version order reverses in repetition two. All 12 runs passed: 396 completed turns. Two separate diagnostic runs also passed; their measurements are excluded from timing medians.

Totals across six turns, median of three runs. Ten-task values cover all ten tasks combined. Peak RAM is externally sampled process RSS, including setup and final verification.

|Tasks|CPU seconds before → after|Allocated MiB|Peak process RAM MiB|Elapsed request waves seconds|Database MiB|
|---|---:|---:|---:|---:|---:|
|1|1.015 → 0.984|178.74 → 175.29|94.97 → 85.16|27.36 → 27.40|9.71 → 9.71|
|10|6.676 → 6.668|1720.78 → 1657.04|322.06 → 254.55|27.90 → 27.86|73.04 → 73.00|

This is primarily a retention improvement. Ten-task CPU is effectively unchanged. Database size is unchanged because compatible replay did not have Anthropic's duplicated fallback header.

|Tasks|Before RSS samples, MiB|After RSS samples, MiB|
|---|---|---|
|1|95.50, 91.12, 94.97|86.89, 85.06, 85.16|
|10|299.53, 323.83, 322.06|248.59, 254.55, 262.23|

Request-phase RSS peaks also fell: medians approximately 92.14 → 85.16 MiB for one task and 299.53 → 248.59 MiB for ten. The improvement is not confined to final verification. Three repetitions do not guarantee the same saving in other workloads.

## Heap ownership evidence

Separate ten-task diagnostic runs captured turn-5 heaps without forcing GC. Capture delays from the external trigger were 22.653 ms before and 22.561 ms after. These are near-peak snapshots, not exact instantaneous peak measurements. Profiling perturbs memory, and sampled in-use accounting can lag garbage collection; these values must not be subtracted to explain every RSS byte.

|Near-peak sampled heap owner|Before MiB|After MiB|
|---|---:|---:|
|Total sampled heap|121.85|92.23|
|SQLite text: display-history projection|32.69|No large allocation sampled|
|SQLite text: native replay|28.05|28.09|
|Decoded JSON strings|34.34|33.01|

The large display-history allocation disappears while native replay and decoded messages remain. This matches the intended ownership change: avoid retaining the display copy alongside replay during the provider request.

## Tradeoffs and limits

Median longest live-output gap increased 8.43 → 11.41 ms for one task and 23.34 → 27.36 ms for ten; worst observed values increased 8.44 → 15.36 ms and 24.32 → 33.91 ms respectively. Ten-task median longest persistence gap increased 507.14 → 509.80 ms (worst 507.99 → 510.53 ms). These are observer gaps, not browser latency.

Worst sampled Go heap fell 26.24 → 25.03 MiB for one task and 214.00 → 174.65 MiB for ten. Retained heap after final GC was 2.93 → 2.94 MiB and 7.53 → 4.63 MiB respectively.

This measures fresh tool conversations with native replay. Text-only smoke tests passed, but this round does not claim repeated text-only performance results, image workloads, real model latency, or compaction performance.

## Validation and retained profiles

Regression tests fail on the old implementation and pass with the fix. Tests cover empty assistant answers, literal marker text, reasoning, native tool replay, cancellation and history fallbacks. LLM, persistence, service and OpenAI-client suites pass, as do focused handler tests, build and vet. Final text/tool smoke tests pass.

An earlier partial timing batch was stopped after finding the literal-marker filtering bug. It is retained under `superseded-timing`, explicitly excluded from all reported medians. `final-smoke` and `timing` use the final candidate; the older `smoke` directory does not.

The [evidence index](EVIDENCE.md) links the downloadable archive. It contains the exact patch, pinned baseline, harness source, test logs, raw timing/phase samples, profile summaries, and both versions' CPU, allocation and phase-triggered heap profiles. Databases, generated fixture repositories and test binaries are excluded.

Profiles can be inspected again without rerunning the workload:

```sh
go tool pprof -top diagnostic/tool-use-large-10-main-1/cpu.pprof
go tool pprof -sample_index=alloc_space -top diagnostic/tool-use-large-10-branch-1/mem.pprof
go tool pprof -sample_index=inuse_space -top diagnostic/tool-use-large-10-branch-1/memory-turn-5.pprof
```

Each diagnostic directory also contains `cpu-top.txt`, `alloc-top.txt`, `peak-inuse.txt`, `retention-tree.txt` and capture timestamps. A new implementation still requires fresh measurements to verify its effects. Whole-process allocation profiles include setup and verification; phase heap snapshots answer a different question.

## Detailed per-turn measurements

# Concurrent streaming measurements

Before → after, medians of fresh processes. CPU % uses one core = 100%; peaks are rolling ≥1-second windows, not instantaneous peaks. Each turn row covers the concurrent wave, not one task. No pooled score.

## openai-compatible: tool-use, large, 1 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|123.10 → 105.17|14.57 → 14.58|34548.00 → 34538.00|10.22 → 10.41|4.41 → 4.11|10.75 → 10.76|0.16 → 0.11|4407.53 → 4385.98|
|1|165.49 → 161.01|21.59 → 21.56|37953.00 → 37963.00|13.64 → 12.21|5.59 → 5.50|16.48 → 16.45|0.31 → 0.34|4584.56 → 4567.61|
|2|165.18 → 155.80|27.16 → 25.98|38290.00 → 38277.00|16.70 → 15.02|5.87 → 5.36|22.06 → 21.58|0.30 → 0.35|4584.39 → 4601.11|
|3|192.82 → 179.01|33.08 → 32.80|38477.00 → 38558.00|20.68 → 17.94|7.33 → 6.48|27.97 → 27.68|0.32 → 0.39|4611.05 → 4619.17|
|4|185.42 → 194.14|38.81 → 36.51|38761.00 → 38771.00|21.82 → 19.96|8.53 → 7.88|33.70 → 31.43|0.24 → 0.39|4588.97 → 4612.14|
|5|180.02 → 192.65|43.48 → 46.85|38942.00 → 39043.00|25.50 → 22.84|8.09 → 7.90|39.57 → 41.75|0.24 → 0.35|4603.25 → 4619.79|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|50.06 → 27.09|192.37 → 192.57|13182.77 → 13197.73|
|1|65.88 → 63.53|304.55 → 304.09|15281.83 → 15334.98|
|2|77.71 → 70.21|416.54 → 406.11|15658.09 → 15694.35|
|3|95.07 → 83.67|534.23 → 526.96|15822.46 → 15870.05|
|4|93.62 → 96.20|482.22 → 583.25|16020.71 → 16090.23|
|5|101.19 → 104.72|555.83 → 689.70|16215.00 → 16379.05|

|Across six turns (median run peak/total)|Before|After|
|---|---:|---:|
|Peak process RSS MiB|94.97|85.16|
|Retained Go heap MiB|2.93|2.94|
|DB MiB|9.71|9.71|
|Longest observed live delta gap ms|8.43|11.41|
|Longest observed persistence notification gap ms|501.30|501.37|

|Worst observed across all six turns and repetitions|Before|After|
|---|---:|---:|
|Sampled heap MiB|26.24|25.03|
|Sampled CPU % (~50 ms)|106.10|108.81|
|Sampled allocation MiB/s (~50 ms)|646.43|708.56|
|Rolling 1s CPU %|8.86|8.56|
|Rolling 1s allocation MiB/s|42.00|41.78|
|Rolling 1s allocations/s|16358.99|16408.15|
|Process RSS MiB|95.50|86.89|
|Live delta gap ms|8.44|15.36|
|Persistence notification gap ms|501.34|501.39|

## openai-compatible: tool-use, large, 10 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|740.44 → 757.99|132.72 → 130.56|325780.00 → 325757.00|48.85 → 50.74|23.53 → 24.21|99.55 → 97.46|1.39 → 1.31|4426.33 → 4448.75|
|1|992.76 → 1035.31|204.96 → 200.26|374618.00 → 375200.00|86.08 → 74.05|37.75 → 39.17|154.65 → 151.03|1.47 → 2.03|4659.44 → 4663.30|
|2|1098.99 → 1143.65|261.50 → 252.63|380271.00 → 380697.00|116.07 → 100.05|45.25 → 47.87|211.41 → 202.75|1.69 → 2.43|4667.37 → 4660.06|
|3|1187.50 → 1143.91|323.16 → 302.10|382334.00 → 383169.00|149.61 → 120.39|54.28 → 52.51|273.84 → 252.36|1.50 → 2.21|4697.27 → 4684.35|
|4|1294.91 → 1253.52|376.04 → 358.05|384920.00 → 385564.00|173.93 → 134.57|62.40 → 59.79|327.29 → 307.57|1.89 → 2.52|4713.42 → 4687.14|
|5|1383.09 → 1265.62|430.70 → 409.69|387485.00 → 387448.00|206.40 → 172.24|70.24 → 63.97|383.76 → 359.43|1.28 → 2.03|4730.36 → 4681.19|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|228.47 → 220.72|1769.57 → 1726.71|114173.94 → 113916.66|
|1|297.71 → 363.24|2414.54 → 1678.70|148838.35 → 149169.82|
|2|462.79 → 403.70|3440.92 → 2651.77|153106.62 → 154129.01|
|3|564.18 → 419.27|3283.06 → 3602.49|154331.67 → 156071.14|
|4|459.97 → 572.43|3852.19 → 4684.10|155197.54 → 157973.89|
|5|644.55 → 650.74|5260.39 → 5212.98|156941.78 → 159666.40|

|Across six turns (median run peak/total)|Before|After|
|---|---:|---:|
|Peak process RSS MiB|322.06|254.55|
|Retained Go heap MiB|7.53|4.63|
|DB MiB|73.04|73.00|
|Longest observed live delta gap ms|23.34|27.36|
|Longest observed persistence notification gap ms|507.14|509.80|

|Worst observed across all six turns and repetitions|Before|After|
|---|---:|---:|
|Sampled heap MiB|214.00|174.65|
|Sampled CPU % (~50 ms)|655.18|710.15|
|Sampled allocation MiB/s (~50 ms)|5459.95|5218.00|
|Rolling 1s CPU %|72.05|68.88|
|Rolling 1s allocation MiB/s|387.98|366.92|
|Rolling 1s allocations/s|157603.91|159896.84|
|Process RSS MiB|323.83|262.23|
|Live delta gap ms|24.32|33.91|
|Persistence notification gap ms|507.99|510.53|

