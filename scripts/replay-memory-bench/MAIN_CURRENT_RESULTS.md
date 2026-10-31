# Current main versus optimized branch

## Verdict

The current branch is substantially cheaper than local main on these ordinary text/tool workloads: allocated bytes and median process RSS are lower in all twelve scenarios. CPU is lower in eleven; compatible single-task text is slightly slower (0.870 → 0.904 seconds), with overlapping run ranges.

All ten-task tool CPU and RAM ranges are separated from main's ranges. Current branch peak RAM is 140.75 MiB for OpenAI, 133.28 MiB for Anthropic, and 187.86 MiB for compatible. Compatible remains the largest, but its main baseline is 330.02 MiB.

Compared with the previous saved branch candidate, allocated bytes are lower in all scenarios. Ten-task tool RAM is 144.22 → 140.75 MiB (OpenAI), 146.86 → 133.28 MiB (Anthropic), and 191.11 → 187.86 MiB (compatible). Historical CPU is mixed: several single-task values are higher, and compatible ten-task tool CPU is 6.041 → 6.083 seconds. These historical observations do not isolate the intervening code changes from newer main code or machine variation.

Tradeoffs remain: compatible ten-task longest live-output gaps rise from 10.11 → 28.26 ms for text and 17.93 → 26.94 ms for tools. Anthropic ten-task text elapsed time rises 28.81 → 29.99 seconds; fixed peer cadence dominates most other elapsed results. Database size grows slightly in most scenarios, while Anthropic ten-task tool storage falls 71.13 → 46.23 MiB. No claim of a universal latency or storage improvement.

## Scope and sources

Main `85d0755c1d809810c9a1b00092fb7af37edabe57` → branch `611d5107f7a8f201e04f283c7f59f676de4a18b4`. Both were pinned and built with `go version go1.27.3 darwin/arm64` on `macOS-27.0.1-arm64-arm-64bit`. Main is an ancestor of the branch. This compares the full branch with current local main, not the preceding branch implementation.

Historical results below come from the last saved final candidate in `RAW_REPLAY_SHARED_RESULTS.md`. They were collected earlier and are not a controlled before/after comparison. The Go flow harness and mock-peer source are identical, and the Go version matches; changes in main and machine conditions can affect historical differences.

## Method

Three providers; text-only and real file-tool workloads; 1 and 10 concurrent tasks; three fresh processes per version/workload. Each task executes six turns sequentially, with concurrent waves synchronized across tasks. 72 measured processes complete 2,376 turns. Version order reverses in repetition two. No pooling between providers or scenarios.

The external model peer mocks inference and emits 48 KiB answers with a fixed 64-byte/5-ms cadence. Tool runs read a real 512 KiB file each turn. Backend handlers, provider adapters, tools, SQLite and SSE/WebSocket transports are real. Every run validates full output, history restoration, tool results, concurrency, request counts, and no hidden retries/compaction. An explicit 2,000,000-token context and 1,500,000-token compaction threshold keep context truncation out of the ordinary workload.

CPU and allocations below sum the six measured waves, covering all tasks. Process RSS is sampled externally at approximately 100 ms; its peak includes startup and final history verification. RSS is not Go heap or total allocated bytes. Fixed peer pacing dominates elapsed time. Reported cells are medians of three independent runs.

Smoke runs and six separate diagnostic captures are excluded from timing results. Diagnostics save CPU, total-allocation and request-phase heap profiles without forcing GC for phase snapshots; capture lag and profile overhead mean these are near-peak observations. Go test/build/vet overlap is detected and rejected by the runner, but other machine activity is not fully controlled.

## Controlled comparison: main → current branch

|Provider|Mode|Tasks|CPU seconds|Allocated MiB|Peak RSS MiB|
|---|---|---|---|---|---|
|openai|text-only|1|1.099 → 0.922|81.41 → 25.30|63.06 → 58.03|
|openai|text-only|10|5.772 → 4.673|788.09 → 233.42|122.61 → 93.16|
|openai|tool-use|1|1.292 → 1.014|242.73 → 69.85|77.81 → 69.89|
|openai|tool-use|10|7.701 → 5.674|2360.14 → 669.15|224.59 → 140.75|
|anthropic|text-only|1|0.853 → 0.771|73.88 → 27.94|60.86 → 55.12|
|anthropic|text-only|10|5.099 → 4.592|705.84 → 255.82|111.47 → 86.44|
|anthropic|tool-use|1|1.023 → 0.915|253.20 → 75.38|79.30 → 68.27|
|anthropic|tool-use|10|6.428 → 5.453|2436.50 → 701.23|243.02 → 133.28|
|openai-compatible|text-only|1|0.870 → 0.904|66.42 → 21.42|60.09 → 55.73|
|openai-compatible|text-only|10|5.339 → 4.441|641.33 → 190.70|111.09 → 87.34|
|openai-compatible|tool-use|1|1.319 → 1.083|423.30 → 104.46|87.59 → 73.06|
|openai-compatible|tool-use|10|8.518 → 6.083|4063.69 → 981.26|330.02 → 187.86|

## Historical comparison: previous measured branch → current branch

These columns are historical observations, not a controlled attribution of the later changes. The previous baseline was a task-branch revision, not main. Only its final candidate results are used here.

|Provider|Mode|Tasks|CPU seconds|Allocated MiB|Peak RSS MiB|
|---|---|---|---|---|---|
|openai|text-only|1|0.771 → 0.922|27.07 → 25.30|57.67 → 58.03|
|openai|text-only|10|4.810 → 4.673|249.12 → 233.42|93.95 → 93.16|
|openai|tool-use|1|0.941 → 1.014|78.24 → 69.85|74.88 → 69.89|
|openai|tool-use|10|5.727 → 5.674|748.10 → 669.15|144.22 → 140.75|
|anthropic|text-only|1|0.771 → 0.771|29.52 → 27.94|55.00 → 55.12|
|anthropic|text-only|10|4.708 → 4.592|270.57 → 255.82|90.92 → 86.44|
|anthropic|tool-use|1|0.915 → 0.915|83.07 → 75.38|73.67 → 68.27|
|anthropic|tool-use|10|5.677 → 5.453|776.01 → 701.23|146.86 → 133.28|
|openai-compatible|text-only|1|0.714 → 0.904|22.51 → 21.42|55.78 → 55.73|
|openai-compatible|text-only|10|4.539 → 4.441|200.03 → 190.70|88.44 → 87.34|
|openai-compatible|tool-use|1|0.991 → 1.083|110.34 → 104.46|77.70 → 73.06|
|openai-compatible|tool-use|10|6.041 → 6.083|1041.26 → 981.26|191.11 → 187.86|

## Variability: per-run minima–maxima

Ranges show the three observed samples; they are not confidence intervals.

|Provider|Mode|Tasks|Version|CPU seconds|Allocated MiB|Peak RSS MiB|
|---|---|---|---|---|---|---|
|openai|text-only|1|main|0.775–1.201|81.16–81.71|62.78–63.33|
|openai|text-only|1|branch|0.654–0.937|25.17–25.37|57.75–62.17|
|openai|text-only|10|main|5.654–5.929|787.86–789.35|118.09–123.41|
|openai|text-only|10|branch|4.632–4.771|233.09–234.09|91.39–94.98|
|openai|tool-use|1|main|0.908–1.326|242.42–247.79|77.02–80.38|
|openai|tool-use|1|branch|0.680–1.114|69.59–69.93|69.02–74.09|
|openai|tool-use|10|main|7.641–7.961|2357.17–2373.07|223.95–227.09|
|openai|tool-use|10|branch|5.616–5.777|666.95–671.54|136.53–141.02|
|anthropic|text-only|1|main|0.773–0.996|72.94–75.82|59.77–61.23|
|anthropic|text-only|1|branch|0.723–1.017|27.88–27.94|54.84–55.38|
|anthropic|text-only|10|main|4.975–5.138|705.32–705.96|111.05–116.30|
|anthropic|text-only|10|branch|4.562–4.821|254.81–255.98|84.22–87.20|
|anthropic|tool-use|1|main|0.985–1.103|253.08–257.17|78.84–86.27|
|anthropic|tool-use|1|branch|0.893–0.917|75.31–75.45|67.30–73.08|
|anthropic|tool-use|10|main|6.193–6.925|2429.04–2440.56|242.42–257.72|
|anthropic|tool-use|10|branch|5.281–5.627|700.77–709.43|130.64–137.73|
|openai-compatible|text-only|1|main|0.826–0.985|65.87–66.44|59.19–60.28|
|openai-compatible|text-only|1|branch|0.636–0.906|21.42–21.51|55.41–58.70|
|openai-compatible|text-only|10|main|5.221–5.346|640.21–644.45|110.83–111.12|
|openai-compatible|text-only|10|branch|4.416–4.583|190.46–191.14|85.47–88.44|
|openai-compatible|tool-use|1|main|1.123–1.369|418.77–426.31|87.14–90.06|
|openai-compatible|tool-use|1|branch|1.010–1.107|104.23–104.47|72.14–78.97|
|openai-compatible|tool-use|10|main|8.356–8.588|4056.22–4080.59|317.47–337.36|
|openai-compatible|tool-use|10|branch|5.774–6.221|970.65–981.48|177.50–193.81|

## Storage, delivery, elapsed time and retained heap

Main → branch. Each delivery gap is the median of each run’s largest observed gap; it is not a percentile of all deltas.

|Provider|Mode|Tasks|Elapsed s|Database MiB|Retained heap MiB|Longest live gap ms|Longest persist gap ms|
|---|---|---|---|---|---|---|---|
|openai|text-only|1|27.28 → 27.30|3.33 → 3.50|3.93 → 3.32|7.03 → 6.56|501.05 → 501.06|
|openai|text-only|10|27.39 → 27.38|15.26 → 16.00|13.61 → 8.42|8.58 → 8.14|506.93 → 507.12|
|openai|tool-use|1|27.43 → 27.37|7.82 → 8.12|7.05 → 3.62|8.58 → 6.58|502.25 → 500.98|
|openai|tool-use|10|27.91 → 27.71|55.43 → 57.51|44.22 → 11.00|37.53 → 17.85|508.48 → 511.78|
|anthropic|text-only|1|27.37 → 27.45|2.88 → 2.95|2.94 → 2.81|7.00 → 6.51|501.08 → 501.65|
|anthropic|text-only|10|28.81 → 29.99|10.74 → 11.16|3.30 → 3.22|9.60 → 7.04|504.64 → 506.00|
|anthropic|tool-use|1|27.42 → 27.23|9.39 → 6.95|2.99 → 2.88|11.22 → 8.08|501.03 → 501.42|
|anthropic|tool-use|10|27.78 → 27.80|71.13 → 46.23|3.34 → 3.24|27.09 → 19.78|507.80 → 505.57|
|openai-compatible|text-only|1|27.23 → 27.35|2.59 → 2.71|2.98 → 2.88|6.62 → 6.25|501.12 → 501.13|
|openai-compatible|text-only|10|27.43 → 27.39|7.94 → 8.38|4.39 → 3.98|10.11 → 28.26|504.19 → 505.99|
|openai-compatible|tool-use|1|27.60 → 27.48|9.45 → 9.67|3.04 → 2.94|7.82 → 8.21|500.86 → 500.75|
|openai-compatible|tool-use|10|27.93 → 27.83|71.79 → 72.99|4.09 → 4.65|17.93 → 26.94|506.58 → 507.66|

## Worst sampled Go heap across waves and repetitions

|Provider|Mode|Tasks|Main MiB|Branch MiB|
|---|---|---|---|---|
|openai|text-only|1|10.29|8.16|
|openai|text-only|10|48.85|33.53|
|openai|tool-use|1|20.29|11.37|
|openai|tool-use|10|129.77|53.91|
|anthropic|text-only|1|10.60|7.76|
|anthropic|text-only|10|52.72|28.49|
|anthropic|tool-use|1|34.07|11.43|
|anthropic|tool-use|10|164.42|59.82|
|openai-compatible|text-only|1|10.36|8.08|
|openai-compatible|text-only|10|53.79|27.66|
|openai-compatible|tool-use|1|33.46|17.43|
|openai-compatible|tool-use|10|225.11|106.78|

## Separate profile observations

The diagnostic profiles corroborate lower allocation work but include startup, verification and profiling overhead, so their totals must not replace the timing tables. Full CPU, alloc-space and in-use-space profiles and readable top-30 summaries are saved.

- Anthropic streaming parser cumulative allocations fell from about 268 to 107 MiB in the diagnostic pair. SQLite text allocation fell about 250 to 57 MiB.
- Compatible streaming parser cumulative allocations fell about 292 to 87 MiB. Its main profile includes about 713 MiB under replay transcript decoding; that old decoder is absent from the branch's top allocation paths.
- OpenAI's main profile has large Markdown-range, replay-save and SQLite-text allocations; the branch shifts more of its remaining cost into request construction, immutable replay reads and output assembly.
- Compatible's final-wave candidate heap still has about 34.3 MiB of SQLite-loaded text and 6.9 MiB of decoded JSON strings. This identifies remaining allocation origins; it does not establish another specific fix.

Unprofiled RSS phase attribution is included in `phases/PHASES.md`. Several branch process peaks occur in final verification; the request-wave peaks are listed separately. Do not interpret every whole-process peak as memory owned by an active provider request.

## Validation and evidence

- Measured runs: 72 accepted; 2376 completed turns.
- Smoke runs: 12 accepted. Separate diagnostic runs: 6 accepted.
- Detected Go test/build overlaps excluded: 2.
- No production source changes during the experiment. Test harness injection was identical and removed from the detached checkouts after compilation.
- `RESULTS.md` contains all six turns, sampled heap, GC, rolling CPU/allocation rates and worst gaps.
- `results.json` preserves all measured run summaries; timing folders preserve raw samples, manifests, verification results and logs.
- `PROFILE_FINDINGS.md` and diagnostic `.pprof` files preserve separate allocation/CPU/heap evidence.
- `source.json`, per-provider `experiment.json`, harness source and binary hashes record the exact sources and configuration.

## Limits

These are mocked-model ordinary text/tool workloads. They do not measure actual network/inference latency, images, long reasoning, compaction, interrupted requests, recovery or provider switching. Correctness tests for those paths are not performance measurements. Small changes and historical comparisons should be read with the per-run ranges. The branch can reduce allocations without reducing RSS, and an RSS decrease alone does not prove which ownership change caused it.
