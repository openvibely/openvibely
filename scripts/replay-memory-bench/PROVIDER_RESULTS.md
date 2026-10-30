# Anthropic and OpenAI-compatible provider comparison

## Verdict

Compared with the main implementation already merged into this task branch, the full branch reduces allocations in every measured workload. It is **not an across-the-board CPU or resident-memory improvement**. Anthropic six-turn CPU is approximately unchanged, and its fifth-follow-up tool CPU is higher. Compatible tool-use saves CPU and allocations, but peak process RAM increases for both providers’ tool workloads. Keep these tradeoffs visible when assessing the branch.

## What was compared

- **Before:** main commit `9e702736a2bb2d9d242915b827fe7dea68ce03e0`, the main revision already merged into the task branch.
- **After:** task branch commit `dfe6f4b3f66b3f13ad67bf26a0af33f749644773`, containing the full transcript/storage/streaming/UI work and seven optimizations. This is **not** a comparison against the previous branch implementation and does not isolate individual optimizations.
- Host: Apple M5 Pro, Darwin arm64; Go `go1.27.3`. Both test binaries use the identical injected harness; the external peer is identical across revisions. Exact host, settings, revisions, and SHA-256 hashes are in `experiment.json` for each provider.
- 48 fresh app/database processes: 2 providers × 2 modes × 2 concurrency levels × 2 versions × 3 repetitions. **All 48 passed, covering 1,584 completed task turns. No rejected or retried timing runs.** Version order alternates each repetition. Providers run sequentially, with no concurrent builds/tests/profiles.
- Each task completes six turns: initial turn 0 and five follow-ups. Each answer is approximately 48 KiB, in 64-byte chunks at a configured 5 ms cadence. Tool mode also executes a real read_file on a 512 KiB fixture each turn. Ten tasks means ten parallel tasks in one backend process sharing one SQLite database, with a barrier between turns.
- Anthropic uses the production Messages/SSE client; compatible uses the production Chat Completions/SSE client. Only model inference is mocked. The mock runs in a separate process excluded from app CPU/RSS. Both providers use a benchmark-only 2,000,000-token context budget and 1,500,000-token compaction threshold to keep compaction out of this comparison; this is not a vendor model-limit claim.
- Integrity gates require expected provider protocol, prior-turn context, exact request/tool-result counts, stored answer/tool end markers, live and persisted events, and one/ten overlapping streams. Text mode has zero tool results. No hidden retries or summaries were accepted.
- Ten preliminary smoke runs passed (both new providers × both modes × both versions, plus both existing OpenAI modes on the candidate). Four separate ten-task tool diagnostic runs collect CPU and heap profiles; their measurements are excluded from these timing tables.

## Fifth follow-up (turn 5)

Every cell is **main → branch (percentage change)**, using the median of three fresh runs. CPU, allocations and object counts cover the whole concurrent wave, not each task. Lower is generally better. A positive percentage is an increase, not an improvement.

### anthropic

|Workload|CPU ms|Allocated MiB|Allocated objects|Sampled peak Go heap MiB|Elapsed ms|
|---|---:|---:|---:|---:|---:|
|text-only, 1 task(s)|140.87 → 148.94 (+5.7%)|19.51 → 14.50 (-25.7%)|22,734.00 → 17,348.00 (-23.7%)|10.38 → 10.10 (-2.7%)|4,563.77 → 5,000.99 (+9.6%)|
|text-only, 10 task(s)|946.72 → 876.67 (-7.4%)|174.24 → 140.09 (-19.6%)|227,246.00 → 171,776.00 (-24.4%)|52.21 → 48.02 (-8.0%)|4,613.25 → 4,623.48 (+0.2%)|
|tool-use, 1 task(s)|171.54 → 191.40 (+11.6%)|63.51 → 53.61 (-15.6%)|39,734.00 → 21,172.00 (-46.7%)|24.31 → 21.99 (-9.6%)|4,584.70 → 4,610.82 (+0.6%)|
|tool-use, 10 task(s)|1,241.24 → 1,332.11 (+7.3%)|592.46 → 493.65 (-16.7%)|397,378.00 → 209,541.00 (-47.3%)|174.50 → 167.02 (-4.3%)|4,664.13 → 4,785.79 (+2.6%)|

### openai-compatible

|Workload|CPU ms|Allocated MiB|Allocated objects|Sampled peak Go heap MiB|Elapsed ms|
|---|---:|---:|---:|---:|---:|
|text-only, 1 task(s)|117.24 → 139.06 (+18.6%)|15.53 → 11.12 (-28.4%)|41,777.00 → 35,882.00 (-14.1%)|10.38 → 10.25 (-1.2%)|4,546.37 → 4,564.62 (+0.4%)|
|text-only, 10 task(s)|946.91 → 894.08 (-5.6%)|153.34 → 110.91 (-27.7%)|418,243.00 → 357,121.00 (-14.6%)|45.49 → 45.86 (+0.8%)|4,573.70 → 4,601.13 (+0.6%)|
|tool-use, 1 task(s)|224.74 → 189.46 (-15.7%)|109.05 → 66.43 (-39.1%)|58,174.00 → 39,102.00 (-32.8%)|31.01 → 30.35 (-2.1%)|4,639.34 → 4,598.37 (-0.9%)|
|tool-use, 10 task(s)|1,784.36 → 1,510.04 (-15.4%)|1,066.81 → 620.91 (-41.8%)|580,502.00 → 388,237.00 (-33.1%)|233.64 → 214.28 (-8.3%)|4,727.11 → 4,775.15 (+1.0%)|

## Entire six-turn conversation

These are medians of each run’s **sum across six measured waves**, not sums of the per-turn medians. CPU/allocations exclude app setup and final integrity checking; elapsed is the sum of measured waves, not process lifetime.

|Provider / workload|CPU ms|Allocated MiB|Elapsed ms|
|---|---:|---:|---:|
|anthropic, text-only, 1|763.73 → 771.37 (+1.0%)|75.80 → 58.69 (-22.6%)|27,588.27 → 29,827.85 (+8.1%)|
|anthropic, text-only, 10|4,980.67 → 4,895.72 (-1.7%)|703.07 → 551.57 (-21.5%)|27,364.60 → 27,413.91 (+0.2%)|
|anthropic, tool-use, 1|962.63 → 934.87 (-2.9%)|255.37 → 197.59 (-22.6%)|27,291.68 → 27,279.34 (-0.0%)|
|anthropic, tool-use, 10|6,497.90 → 6,503.41 (+0.1%)|2,448.85 → 1,834.25 (-25.1%)|27,710.49 → 28,073.26 (+1.3%)|
|openai-compatible, text-only, 1|764.02 → 758.02 (-0.8%)|66.03 → 48.09 (-27.2%)|27,152.87 → 27,140.95 (-0.0%)|
|openai-compatible, text-only, 10|5,156.10 → 4,989.99 (-3.2%)|641.05 → 461.04 (-28.1%)|27,335.09 → 27,297.32 (-0.1%)|
|openai-compatible, tool-use, 1|1,124.51 → 984.77 (-12.4%)|419.87 → 240.95 (-42.6%)|27,363.29 → 27,271.95 (-0.3%)|
|openai-compatible, tool-use, 10|8,426.75 → 7,119.92 (-15.5%)|4,060.78 → 2,261.46 (-44.3%)|27,945.65 → 28,028.88 (+0.3%)|

## Process memory and storage

RSS is the peak external process-memory sample over the **whole process**, including setup and final history verification. It is not turn-5 heap and not retained memory. Retained Go heap is measured after two final collections with the app alive. DB size is after WAL checkpoint.

|Provider / workload|Peak RSS MiB|Retained Go heap MiB|Checkpointed DB MiB|
|---|---:|---:|---:|
|anthropic, text-only, 1|60.91 → 60.00 (-1.5%)|3.69 → 3.34 (-9.5%)|2.91 → 3.00 (+2.9%)|
|anthropic, text-only, 10|115.38 → 115.94 (+0.5%)|3.29 → 3.20 (-2.8%)|10.78 → 11.18 (+3.8%)|
|anthropic, tool-use, 1|79.48 → 91.59 (+15.2%)|2.98 → 2.87 (-3.8%)|9.43 → 9.11 (-3.3%)|
|anthropic, tool-use, 10|242.66 → 259.31 (+6.9%)|3.29 → 3.21 (-2.4%)|71.16 → 72.40 (+1.7%)|
|openai-compatible, text-only, 1|60.06 → 59.58 (-0.8%)|2.98 → 2.87 (-3.8%)|2.63 → 2.75 (+4.5%)|
|openai-compatible, text-only, 10|110.11 → 112.52 (+2.2%)|4.06 → 4.34 (+7.0%)|7.94 → 8.33 (+4.9%)|
|openai-compatible, tool-use, 1|88.25 → 103.48 (+17.3%)|3.02 → 2.92 (-3.3%)|9.49 → 9.71 (+2.3%)|
|openai-compatible, tool-use, 10|322.92 → 364.53 (+12.9%)|7.00 → 7.52 (+7.4%)|71.79 → 73.02 (+1.7%)|

## Variation and regressions

Three samples establish repeatability for this fixture, not statistical confidence or a production SLA. The following ranges are **min–max**, not confidence intervals. Small changes and overlapping ranges need caution.

|Provider / workload|Turn-5 CPU main range ms|Turn-5 CPU branch range ms|Peak RSS main range MiB|Peak RSS branch range MiB|
|---|---:|---:|---:|---:|
|anthropic, text-only, 1|138.56–147.44|121.57–151.90|60.36–60.92|59.62–60.03|
|anthropic, text-only, 10|911.58–947.99|822.29–925.33|113.14–119.92|114.42–118.88|
|anthropic, tool-use, 1|171.08–190.38|174.75–215.51|78.38–86.52|90.80–92.47|
|anthropic, tool-use, 10|1,185.17–1,245.97|1,275.69–1,361.53|233.45–249.62|253.30–276.44|
|openai-compatible, text-only, 1|115.90–129.75|100.70–140.93|59.84–60.56|58.97–59.80|
|openai-compatible, text-only, 10|897.34–971.37|837.48–897.28|109.94–112.81|109.95–113.52|
|openai-compatible, tool-use, 1|200.30–238.05|187.05–195.41|83.64–88.28|100.77–105.64|
|openai-compatible, tool-use, 10|1,712.75–1,991.67|1,442.05–1,633.55|322.78–335.83|310.17–376.47|

- **Anthropic CPU:** fifth-follow-up tool CPU increases 11.6% for one task and 7.3% for ten tasks. Whole-conversation CPU is −2.9% and +0.1%, respectively; the costs move across turns. Text single-task turn-5 CPU is +5.7%, while its whole-conversation CPU is +1.0%.
- **Compatible CPU:** single-task text turn-5 CPU is +18.6%, but individual run ranges overlap substantially and whole-conversation CPU is −0.8%. Tool-use saves approximately 12–16% whole-conversation CPU.
- **Peak process RAM:** Anthropic tool-use rises 15.2% / 6.9% for one / ten tasks. Compatible tool-use rises 17.3% / 12.9%. Lower cumulative allocation does not imply lower resident memory. These runs do not establish the cause of the RSS increase; runtime heap reservation, GC timing, final verification and native memory all need separate phase-specific investigation.
- **Retained heap:** compatible ten-task tool-use is variable (3.99–13.37 MiB on the branch versus approximately 7.00 MiB on main). Six turns do not establish a memory leak or leak freedom.
- **Storage:** checkpointed DB size generally grows about 2–5%; single-task Anthropic tool-use is the exception (−3.3%).
- **Elapsed time:** single-task Anthropic text is +8.1% over all six waves (+9.6% at turn 5). Run totals span 27.07–29.88 s on main and 27.06–30.04 s on the branch, so this median difference has overlapping ranges; do not infer a vendor-latency regression. Fixed mock cadence dominates. Ten-task Anthropic tool-use takes about 1.3% longer over all six waves; compatible ten-task tool-use is about 0.3% longer. These are backend fixture timings, not live vendor or browser latency.

## Delivery gaps

Median of each run’s longest observed gap across six turns. An observer starts after admission and can miss the first events. Tool execution can legitimately create a live-output gap. Persistence is configured around a 500 ms flush interval. Worst observed values are retained in the full tables below.

|Provider / workload|Live delta gap ms|Persistence notification gap ms|
|---|---:|---:|
|anthropic, text-only, 1|6.90 → 6.58 (-4.6%)|501.18 → 501.36 (+0.0%)|
|anthropic, text-only, 10|7.27 → 6.56 (-9.9%)|504.64 → 505.08 (+0.1%)|
|anthropic, tool-use, 1|10.33 → 10.65 (+3.1%)|502.43 → 501.05 (-0.3%)|
|anthropic, tool-use, 10|26.45 → 18.44 (-30.3%)|506.59 → 507.96 (+0.3%)|
|openai-compatible, text-only, 1|6.25 → 6.12 (-2.0%)|501.22 → 502.43 (+0.2%)|
|openai-compatible, text-only, 10|7.04 → 8.53 (+21.2%)|503.32 → 505.30 (+0.4%)|
|openai-compatible, tool-use, 1|13.49 → 8.57 (-36.5%)|501.25 → 501.12 (-0.0%)|
|openai-compatible, tool-use, 10|21.46 → 26.25 (+22.3%)|506.28 → 506.74 (+0.1%)|

Compatible ten-task tool-use has a median longest live gap of **26.25 ms versus 21.46 ms (+22.3%)**, with worst observed gaps **31.94 ms versus 24.54 ms**. This is a measured regression in this fixture, not a browser latency measurement.

## Profile attribution

Four separate diagnostic runs cover ten parallel tool tasks and six turns, once per provider/version. Figures below are sampled cumulative attribution, approximately MiB; they are **not** the unprofiled timing medians. Call-tree entries can overlap and must not be added together.

|Allocation path|Anthropic main → branch|Compatible main → branch|Interpretation|
|---|---:|---:|---|
|Whole diagnostic process|2,509 → 1,920|4,167 → 2,367|Direction agrees with unprofiled allocation totals.|
|Real `execReadFile`|211 → 56|222 → 69|The shared file formatter improvement transfers to both providers.|
|History token estimator|100 → 84|872 → 803|Compatible estimation remains expensive: about 34% of branch diagnostic allocation.|
|`json.Marshal`, all callers|669 → 788|985 → 848|Anthropic still re-encodes substantial content; reduced overall allocation does not mean every serialization path improved.|
|Replay persistence: old `SaveConversationReplay` / new recorder save path|33 → 59|478 → 135|Different implementations, with different bookkeeping; useful directional attribution, not a like-for-like isolated function benchmark. Compatible replay saving benefits much more.|

Anthropic diagnostic CPU attribution shows SQLite stepping growing from approximately **0.80 s to 1.37 s**, `AppendOutput` from **0.20 s to 0.30 s**, and the old replay save/new recorder path from **0.10 s to 0.27 s**. These overlapping call-tree figures point to extra persistence work as a candidate for the late-turn CPU regression. One sampled whole-process profile cannot prove the cause of a particular turn’s regression.

The compatible branch still allocates approximately **531 MiB** decoding replay into client history, and **803 MiB** estimating history tokens. Those paths overlap; do not sum them. Its SSE parsing and ordinary request encoding do not benefit from the Responses-only WebSocket optimizations. Neither provider receives the OpenAI-native history-output omission optimization.

Next work supported by these measurements:

1. Investigate tool-use RSS peaks with phase-aligned snapshots, including final history verification and native/process memory. The current samples do not identify the cause, so do not claim the branch lowers process RAM for these workloads.
2. Profile and reduce Anthropic recorder/SQLite work on later turns while preserving replay correctness and transaction semantics.
3. Avoid repeatedly decoding/serializing compatible history just to estimate its token budget; retain the existing estimator’s semantics and validate with the same fixture.

No further production optimization was made during this experiment. The new harness provides a reproducible baseline for those follow-ups.

## Limits and reproduction

These measurements cover local text and one real file-reading tool round per turn. They do not cover extended thinking/reasoning, images, OAuth, prompt caches, real inference/network latency, compaction, retries, other tool distributions, browser rendering, or long-lived production endurance. UI changes are present in the candidate but this harness does not run a browser. Do not transfer the earlier Responses/WebSocket percentages to these providers. No individual optimization is isolated by this experiment.

Build and run instructions are in [the concurrent harness README](concurrent/README.md). Use the pinned revisions above, the same `flow_test.go.txt` in both checkouts, and the same peer binary. For each provider, run `--profiles large --modes text-only,tool-use --workers 1,10 --runs 3`, adding `--provider anthropic` or `--provider openai-compatible`, and explicit `--baseline-revision` / `--candidate-revision`. Run serially. Feed both roots to `summarize.py`. Keep `--profile` runs separate; the summarizer rejects them.

Evidence archive: `timing/` contains all 48 accepted runs, raw 50 ms samples, RSS samples, logs, manifests, delivery observations and request statistics. `diagnostics/` contains four separate pprof runs and text attribution. `report/results.json` contains every derived per-run measurement. `experiment.json` records binary/harness hashes and the actual host/settings. DB files and executables are omitted from the portable archive.

## Full per-turn measurements

The following generated tables preserve every scenario and all six turns, including GC pauses, transient peaks, storage, and worst observed gaps.

## anthropic: text-only, large, 1 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|81.58 → 94.39|6.33 → 5.12|13615.00 → 13196.00|7.88 → 6.60|2.27 → 3.11|3.19 → 3.21|0.05 → 0.05|4806.99 → 4822.94|
|1|117.02 → 127.57|9.27 → 7.38|22072.00 → 16498.00|7.99 → 7.36|3.71 → 3.84|5.98 → 5.26|0.33 → 0.15|4530.59 → 5014.29|
|2|135.80 → 134.92|11.56 → 8.95|22321.00 → 16712.00|8.26 → 8.26|3.96 → 4.35|8.19 → 6.82|0.25 → 0.24|4560.14 → 5005.89|
|3|148.65 → 136.10|13.43 → 10.37|22463.00 → 17008.00|8.75 → 8.41|4.51 → 4.26|10.07 → 8.24|0.33 → 0.22|4565.47 → 5008.02|
|4|141.49 → 140.58|15.88 → 12.32|22552.00 → 17111.00|9.41 → 9.44|4.68 → 4.72|12.61 → 10.17|0.33 → 0.31|4561.31 → 4975.72|
|5|140.87 → 148.94|19.51 → 14.50|22734.00 → 17348.00|10.38 → 10.10|4.96 → 4.83|16.16 → 12.35|0.28 → 0.34|4563.77 → 5000.99|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|9.67 → 17.65|56.95 → 57.35|5804.27 → 5638.17|
|1|37.83 → 30.02|111.85 → 96.96|13328.07 → 7999.43|
|2|40.55 → 49.27|156.25 → 128.29|13609.26 → 8274.70|
|3|51.72 → 33.76|193.20 → 156.02|13732.99 → 8550.91|
|4|56.20 → 48.84|243.71 → 194.01|13825.01 → 8717.59|
|5|58.07 → 42.55|313.86 → 238.87|13966.06 → 8890.17|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|60.91|60.00|
|Retained Go heap MiB|3.69|3.34|
|DB MiB|2.91|3.00|
|Longest observed live delta gap ms|6.90|6.58|
|Longest observed persistence notification gap ms|501.18|501.36|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|11.86|10.39|
|Sampled CPU % (~50 ms)|65.44|60.79|
|Sampled allocation MiB/s (~50 ms)|314.31|253.30|
|Rolling 1s CPU %|5.36|4.93|
|Rolling 1s allocation MiB/s|16.18|13.22|
|Rolling 1s allocations/s|14025.96|8973.01|
|Process RSS MiB|60.92|60.03|
|Live delta gap ms|11.00|14.01|
|Persistence notification gap ms|501.40|501.61|

## anthropic: text-only, large, 10 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|619.02 → 584.94|59.21 → 45.87|122677.00 → 116513.00|25.83 → 27.71|20.29 → 21.05|27.94 → 27.38|1.51 → 1.09|4402.95 → 4411.72|
|1|837.82 → 805.56|86.41 → 68.08|218767.00 → 160805.00|33.48 → 27.12|27.49 → 26.25|52.73 → 46.90|2.22 → 1.64|4649.75 → 4605.64|
|2|865.03 → 842.94|108.76 → 83.84|223669.00 → 165654.00|36.40 → 33.12|28.97 → 26.80|75.20 → 62.45|2.41 → 2.18|4576.36 → 4594.49|
|3|864.30 → 846.96|126.87 → 97.64|225027.00 → 167380.00|41.42 → 39.31|30.91 → 28.18|93.47 → 76.66|2.92 → 1.52|4573.38 → 4597.44|
|4|882.22 → 867.08|147.85 → 116.41|226063.00 → 169713.00|49.70 → 43.80|31.75 → 29.94|114.36 → 95.28|2.81 → 1.82|4600.14 → 4615.57|
|5|946.72 → 876.67|174.24 → 140.09|227246.00 → 171776.00|52.21 → 48.02|34.71 → 30.80|141.12 → 119.21|2.74 → 1.99|4613.25 → 4623.48|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|204.32 → 139.82|497.84 → 482.75|49706.55 → 47600.15|
|1|305.16 → 272.10|936.46 → 848.51|132963.55 → 79313.76|
|2|357.60 → 315.71|1198.90 → 951.50|137981.92 → 84534.62|
|3|377.26 → 298.77|1302.42 → 845.90|139162.16 → 86415.38|
|4|388.48 → 324.68|1546.86 → 940.71|140457.36 → 88396.06|
|5|407.99 → 275.29|1730.58 → 1633.86|141440.73 → 90462.43|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|115.38|115.94|
|Retained Go heap MiB|3.29|3.20|
|DB MiB|10.78|11.18|
|Longest observed live delta gap ms|7.27|6.56|
|Longest observed persistence notification gap ms|504.64|505.08|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|56.57|52.38|
|Sampled CPU % (~50 ms)|425.87|328.20|
|Sampled allocation MiB/s (~50 ms)|1935.95|1807.79|
|Rolling 1s CPU %|36.67|31.96|
|Rolling 1s allocation MiB/s|144.42|120.17|
|Rolling 1s allocations/s|141872.12|90577.15|
|Process RSS MiB|119.92|118.88|
|Live delta gap ms|15.68|6.81|
|Persistence notification gap ms|506.06|507.17|

## anthropic: tool-use, large, 1 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|111.76 → 104.05|21.96 → 13.96|25932.00 → 16671.00|9.28 → 9.51|4.18 → 3.01|14.55 → 10.49|0.23 → 0.16|4395.07 → 4382.08|
|1|158.53 → 165.38|31.79 → 21.02|38644.00 → 19672.00|13.14 → 12.62|5.26 → 5.87|21.29 → 15.67|0.53 → 0.33|4572.20 → 4584.49|
|2|179.21 → 167.19|38.84 → 28.05|39034.00 → 20139.00|14.44 → 14.20|6.68 → 6.25|28.35 → 22.73|0.45 → 0.35|4574.24 → 4590.39|
|3|175.52 → 162.10|45.85 → 36.79|39154.00 → 20530.00|18.13 → 17.31|6.50 → 6.03|35.37 → 31.49|0.46 → 0.34|4591.08 → 4576.80|
|4|166.07 → 176.43|54.74 → 44.62|39455.00 → 20760.00|24.53 → 19.54|6.99 → 7.44|44.24 → 39.29|0.35 → 0.29|4574.40 → 4595.36|
|5|171.54 → 191.40|63.51 → 53.61|39734.00 → 21172.00|24.31 → 21.99|7.01 → 7.93|52.82 → 47.54|0.38 → 0.37|4584.70 → 4610.82|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|58.51 → 22.36|252.02 → 190.06|18434.50 → 9481.16|
|1|65.26 → 57.53|385.61 → 291.62|29783.22 → 11299.32|
|2|74.84 → 70.81|524.44 → 434.63|30179.99 → 11728.19|
|3|85.47 → 86.64|665.91 → 603.56|30374.71 → 12076.58|
|4|95.94 → 90.89|813.62 → 725.43|30667.67 → 12361.11|
|5|98.81 → 100.94|1017.94 → 692.29|30855.80 → 12735.46|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|79.48|91.59|
|Retained Go heap MiB|2.98|2.87|
|DB MiB|9.43|9.11|
|Longest observed live delta gap ms|10.33|10.65|
|Longest observed persistence notification gap ms|502.43|501.05|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|33.09|22.76|
|Sampled CPU % (~50 ms)|103.44|103.68|
|Sampled allocation MiB/s (~50 ms)|1024.88|785.45|
|Rolling 1s CPU %|7.91|9.30|
|Rolling 1s allocation MiB/s|53.35|48.33|
|Rolling 1s allocations/s|30988.59|12802.89|
|Process RSS MiB|86.52|92.47|
|Live delta gap ms|11.43|11.34|
|Persistence notification gap ms|502.70|501.18|

## anthropic: tool-use, large, 10 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|726.44 → 740.31|212.60 → 126.13|236956.00 → 143530.00|53.93 → 45.08|22.52 → 23.02|138.01 → 97.34|2.51 → 1.52|4417.92 → 4441.62|
|1|1102.65 → 1033.52|304.22 → 197.22|385263.00 → 193073.00|81.19 → 64.25|39.89 → 34.73|198.50 → 146.48|4.66 → 2.12|4668.52 → 4656.99|
|2|1148.88 → 1117.76|375.12 → 267.76|390631.00 → 199708.00|95.36 → 87.81|42.98 → 41.33|270.12 → 216.24|4.62 → 1.64|4652.40 → 4711.57|
|3|1150.51 → 1174.67|444.76 → 336.11|392916.00 → 203061.00|117.32 → 115.19|47.60 → 49.32|339.28 → 286.02|2.61 → 2.41|4664.21 → 4748.46|
|4|1176.64 → 1174.08|514.62 → 412.14|395513.00 → 206194.00|139.65 → 145.15|51.87 → 52.95|409.11 → 362.55|4.66 → 1.72|4663.49 → 4751.21|
|5|1241.24 → 1332.11|592.46 → 493.65|397378.00 → 209541.00|174.50 → 167.02|57.95 → 58.10|486.06 → 444.01|2.88 → 1.38|4664.13 → 4785.79|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|202.18 → 214.21|2389.46 → 1757.37|164187.56 → 73653.43|
|1|378.94 → 257.60|2341.54 → 2153.17|298557.34 → 110690.86|
|2|400.13 → 394.23|3429.04 → 3748.99|304313.75 → 116720.43|
|3|425.85 → 349.78|4263.88 → 3228.57|306084.25 → 119849.33|
|4|447.39 → 437.70|6045.82 → 5600.93|308314.99 → 122798.51|
|5|543.86 → 418.27|7128.82 → 4601.49|310559.90 → 125941.80|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|242.66|259.31|
|Retained Go heap MiB|3.29|3.21|
|DB MiB|71.16|72.40|
|Longest observed live delta gap ms|26.45|18.44|
|Longest observed persistence notification gap ms|506.59|507.96|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|181.38|173.92|
|Sampled CPU % (~50 ms)|548.81|449.41|
|Sampled allocation MiB/s (~50 ms)|7439.16|5728.54|
|Rolling 1s CPU %|59.27|59.73|
|Rolling 1s allocation MiB/s|492.29|447.26|
|Rolling 1s allocations/s|310876.47|126076.04|
|Process RSS MiB|249.62|276.44|
|Live delta gap ms|28.30|23.51|
|Persistence notification gap ms|507.24|512.00|

## openai-compatible: text-only, large, 1 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|97.50 → 82.56|6.34 → 4.95|32031.00 → 31414.00|6.62 → 7.14|3.16 → 2.85|2.72 → 2.71|0.11 → 0.04|4387.51 → 4371.34|
|1|137.45 → 128.87|8.65 → 6.42|41055.00 → 35112.00|7.74 → 7.26|3.92 → 3.83|4.80 → 4.03|0.25 → 0.22|4544.72 → 4554.64|
|2|130.23 → 136.80|10.19 → 7.43|41389.00 → 35337.00|9.25 → 8.08|3.96 → 3.78|6.39 → 5.03|0.20 → 0.18|4535.16 → 4560.11|
|3|137.46 → 131.39|11.73 → 8.42|41510.00 → 35519.00|9.46 → 8.40|4.08 → 3.94|7.93 → 5.99|0.29 → 0.21|4568.78 → 4537.22|
|4|143.28 → 129.31|13.55 → 9.71|41563.00 → 35675.00|9.33 → 9.25|4.64 → 3.73|9.71 → 7.27|0.30 → 0.21|4565.15 → 4539.90|
|5|117.24 → 139.06|15.53 → 11.12|41777.00 → 35882.00|10.38 → 10.25|4.38 → 3.83|11.67 → 8.64|0.23 → 0.22|4546.37 → 4564.62|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|21.92 → 19.72|44.59 → 43.99|10224.09 → 10004.29|
|1|31.94 → 32.99|85.67 → 69.82|17912.57 → 12625.79|
|2|33.00 → 33.42|117.12 → 89.69|18355.11 → 12934.50|
|3|37.21 → 37.27|147.89 → 109.35|18449.46 → 13120.30|
|4|47.33 → 33.83|182.78 → 134.78|18491.01 → 13261.54|
|5|47.25 → 30.01|221.91 → 161.98|18711.72 → 13405.85|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|60.06|59.58|
|Retained Go heap MiB|2.98|2.87|
|DB MiB|2.63|2.75|
|Longest observed live delta gap ms|6.25|6.12|
|Longest observed persistence notification gap ms|501.22|502.43|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|10.45|10.45|
|Sampled CPU % (~50 ms)|56.96|42.69|
|Sampled allocation MiB/s (~50 ms)|222.84|169.65|
|Rolling 1s CPU %|5.00|4.23|
|Rolling 1s allocation MiB/s|11.73|9.02|
|Rolling 1s allocations/s|18727.51|13413.08|
|Process RSS MiB|60.56|59.80|
|Live delta gap ms|14.48|12.12|
|Persistence notification gap ms|502.40|504.24|

## openai-compatible: text-only, large, 10 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|596.89 → 648.71|60.06 → 45.25|311475.00 → 303665.00|27.15 → 28.38|18.71 → 20.35|23.86 → 24.10|1.20 → 0.92|4394.84 → 4390.35|
|1|868.38 → 840.01|82.68 → 60.26|408881.00 → 348502.00|33.77 → 32.34|29.52 → 25.64|44.58 → 36.60|2.40 → 1.94|4594.70 → 4553.34|
|2|891.41 → 835.36|98.87 → 72.09|413940.00 → 352689.00|37.16 → 36.20|28.72 → 26.02|61.02 → 48.50|2.68 → 0.96|4586.79 → 4596.32|
|3|927.75 → 844.24|115.57 → 80.90|415428.00 → 353987.00|39.25 → 41.02|30.14 → 26.87|77.58 → 57.26|1.91 → 1.23|4579.19 → 4595.34|
|4|882.75 → 913.69|132.95 → 93.15|416702.00 → 355778.00|44.81 → 44.25|31.67 → 29.76|95.04 → 69.66|3.50 → 1.88|4588.37 → 4586.85|
|5|946.91 → 894.08|153.34 → 110.91|418243.00 → 357121.00|45.49 → 45.86|32.80 → 29.49|115.25 → 87.32|2.80 → 1.16|4573.70 → 4601.13|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|146.02 → 133.59|381.18 → 382.54|95328.41 → 93229.87|
|1|296.10 → 265.32|733.59 → 632.52|178597.21 → 124842.67|
|2|342.27 → 292.64|979.72 → 767.05|183896.55 → 129374.54|
|3|367.42 → 305.03|1148.28 → 855.46|185485.25 → 130252.11|
|4|391.08 → 351.80|1444.10 → 862.69|186208.47 → 132066.97|
|5|416.20 → 295.84|1786.60 → 1007.42|188183.45 → 133019.11|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|110.11|112.52|
|Retained Go heap MiB|4.06|4.34|
|DB MiB|7.94|8.33|
|Longest observed live delta gap ms|7.04|8.53|
|Longest observed persistence notification gap ms|503.32|505.30|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|47.26|50.20|
|Sampled CPU % (~50 ms)|436.54|352.64|
|Sampled allocation MiB/s (~50 ms)|1820.03|1350.91|
|Rolling 1s CPU %|34.22|30.48|
|Rolling 1s allocation MiB/s|115.62|87.80|
|Rolling 1s allocations/s|188226.79|133344.46|
|Process RSS MiB|112.81|113.52|
|Live delta gap ms|14.17|13.54|
|Persistence notification gap ms|503.85|505.45|

## openai-compatible: tool-use, large, 1 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|102.68 → 90.05|27.92 → 14.58|43990.00 → 34534.00|12.32 → 10.01|3.27 → 2.95|15.33 → 10.74|0.18 → 0.13|4368.20 → 4356.50|
|1|184.76 → 155.11|48.98 → 24.63|57244.00 → 37918.00|16.27 → 14.75|6.56 → 5.07|31.48 → 19.49|0.58 → 0.34|4593.47 → 4563.00|
|2|193.50 → 172.54|61.18 → 34.41|57592.00 → 38334.00|19.45 → 17.27|7.62 → 6.67|46.00 → 29.31|0.65 → 0.38|4578.73 → 4601.95|
|3|197.14 → 175.62|76.82 → 45.36|57742.00 → 38520.00|25.86 → 22.39|8.67 → 7.28|61.70 → 40.28|0.55 → 0.33|4585.34 → 4574.73|
|4|208.77 → 198.10|96.55 → 55.57|58040.00 → 38865.00|28.31 → 26.44|9.75 → 9.05|79.51 → 50.42|0.47 → 0.27|4616.00 → 4607.17|
|5|224.74 → 189.46|109.05 → 66.43|58174.00 → 39102.00|31.01 → 30.35|11.48 → 9.55|95.01 → 61.31|0.48 → 0.28|4639.34 → 4598.37|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|22.58 → 24.13|264.82 → 190.99|22196.94 → 13148.56|
|1|76.00 → 65.24|584.38 → 366.89|33989.18 → 15334.56|
|2|103.57 → 90.12|820.38 → 558.30|34242.26 → 15718.43|
|3|109.36 → 96.95|931.58 → 692.26|34433.20 → 15844.47|
|4|108.98 → 105.64|944.59 → 793.09|34639.39 → 16132.59|
|5|107.12 → 100.57|1095.95 → 734.77|34700.26 → 16307.14|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|88.25|103.48|
|Retained Go heap MiB|3.02|2.92|
|DB MiB|9.49|9.71|
|Longest observed live delta gap ms|13.49|8.57|
|Longest observed persistence notification gap ms|501.25|501.12|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|31.37|30.74|
|Sampled CPU % (~50 ms)|121.60|112.07|
|Sampled allocation MiB/s (~50 ms)|1098.47|809.20|
|Rolling 1s CPU %|11.97|9.58|
|Rolling 1s allocation MiB/s|95.55|61.34|
|Rolling 1s allocations/s|34707.34|16358.27|
|Process RSS MiB|88.28|105.64|
|Live delta gap ms|14.87|12.97|
|Persistence notification gap ms|501.33|501.33|

## openai-compatible: tool-use, large, 10 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|817.58 → 759.66|272.83 → 131.80|421539.00 → 325573.00|61.98 → 52.13|26.78 → 24.61|147.45 → 98.77|1.80 → 1.08|4443.92 → 4440.84|
|1|1190.86 → 1050.41|460.06 → 242.62|569737.00 → 375326.00|88.54 → 90.27|50.67 → 42.73|305.82 → 192.80|6.18 → 2.56|4700.64 → 4662.09|
|2|1358.54 → 1181.72|601.83 → 323.84|575195.00 → 380692.00|133.04 → 117.27|63.01 → 50.16|446.94 → 277.35|6.42 → 2.01|4656.09 → 4680.59|
|3|1579.03 → 1307.31|756.77 → 422.90|577115.00 → 383199.00|162.59 → 151.70|77.23 → 63.88|600.52 → 375.98|6.50 → 2.19|4683.86 → 4719.77|
|4|1711.41 → 1381.98|905.46 → 519.88|579083.00 → 385454.00|186.65 → 184.08|91.54 → 72.82|757.80 → 470.34|5.40 → 2.85|4735.18 → 4715.86|
|5|1784.36 → 1510.04|1066.81 → 620.91|580502.00 → 388237.00|233.64 → 214.28|104.04 → 83.29|915.98 → 572.56|5.71 → 2.77|4727.11 → 4775.15|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|198.10 → 220.44|2534.97 → 1749.69|204611.88 → 114123.41|
|1|503.31 → 327.90|3118.56 → 3205.85|337243.36 → 148953.97|
|2|536.69 → 534.21|6054.83 → 4736.24|342320.10 → 153232.34|
|3|817.42 → 602.61|8669.26 → 4214.15|343245.58 → 154001.40|
|4|983.50 → 547.92|9911.55 → 5697.76|343723.85 → 155412.47|
|5|983.51 → 845.80|10607.83 → 8275.71|345011.38 → 157128.73|

|Across six turns (median run peak/total)|Main|Branch|
|---|---:|---:|
|Peak process RSS MiB|322.92|364.53|
|Retained Go heap MiB|7.00|7.52|
|DB MiB|71.79|73.02|
|Longest observed live delta gap ms|21.46|26.25|
|Longest observed persistence notification gap ms|506.28|506.74|

|Worst observed across all six turns and repetitions|Main|Branch|
|---|---:|---:|
|Sampled heap MiB|252.00|225.86|
|Sampled CPU % (~50 ms)|990.72|880.18|
|Sampled allocation MiB/s (~50 ms)|10653.56|8807.32|
|Rolling 1s CPU %|109.09|84.65|
|Rolling 1s allocation MiB/s|917.94|589.23|
|Rolling 1s allocations/s|345098.94|157776.66|
|Process RSS MiB|335.83|376.47|
|Live delta gap ms|24.54|31.94|
|Persistence notification gap ms|508.10|511.77|

