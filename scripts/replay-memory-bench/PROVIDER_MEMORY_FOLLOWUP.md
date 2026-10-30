# Provider memory investigation and estimator optimization

The earlier RAM regression occurs during real request handling, not just final verification. A compatible-provider token-estimation change reduces repeated serialization and improves the repeated measurements below. An Anthropic persistence experiment did not improve CPU and was reverted.

## Comparisons and method

These are two distinct experiments:

1. **Phase investigation:** main `9e702736a2bb2d9d242915b827fe7dea68ce03e0` versus branch `659c90e24d6ca358c3fcea5712464c048a2bf1e6`. One unprofiled process per provider/version, ten parallel tool tasks. This locates peaks; it is not a repeated performance comparison. Branch 659 contains the same production implementation as the preceding provider report's dfe6f4b3f, plus its harness/report changes.
2. **Final optimization:** previous branch **659c90e24 → that same branch plus the compatible estimator change**. This is NOT main versus branch. Three fresh processes per version/concurrency, twelve timing runs and 396 completed turns. Candidate production patch SHA-256: `6c04daa3af82ecf9f59bc827e286cf412195188d88313fa1bb239f789690b878`.

Each task executes six sequential turns. Ten tasks run concurrently in one backend process with one SQLite database. Each tool turn reads a real 512 KiB file and streams a 48 KiB answer. Production provider clients, tools, replay, and persistence run against mocked model inference. No retries or compaction may silently alter the accepted workload. Versions alternate order across repetitions. No builds/tests run alongside timing processes.

RSS is sampled externally about every 100 ms and aligned with application timestamps for startup, each request wave, verification, final GC, and checkpoint/teardown. Short phases without samples are **unobserved, not zero**. Allocations and CPU totals below cover request waves; process RSS includes the complete process. MiB means 1,048,576 bytes.

## Where the original peaks occur

Unprofiled phase investigation; one process per cell:

|Provider|Version|Highest request-wave RSS MiB|Verification RSS MiB|Whole-process peak MiB / phase|
|---|---|---:|---:|---|
|Anthropic|Main|249.45|Not sampled|249.45 / turn 5|
|Anthropic|Branch|255.12|255.14|255.14 / verification|
|Compatible|Main|324.25|Not sampled|324.25 / turn 5|
|Compatible|Branch|348.45|Not sampled|348.45 / turn 5|

Anthropic's verification sample is only 0.02 MiB above its request peak. Compatible peaks during the fifth follow-up itself. Final-history verification does not explain away the higher request-time RAM.

## Near-peak heap captures

Four separate diagnostic processes capture heap profiles after external phase high-water samples, without forcing GC. The original turn-5 snapshots arrived about 12–16 ms after their trigger and remained in the same phase. A final optimized compatible snapshot arrived about 2 ms after its trigger. These are **near-peak snapshots**, not instantaneous measurements. Diagnostic runs are excluded from timing tables. Heap profile accounting can lag GC cycles, profiling itself allocates, and Go profiles do not account for all process RSS.

Largest sampled in-use allocation sites during branch request handling:

|Provider / snapshot|SQLite column text MiB|Decoded JSON strings MiB|Total sampled in-use MiB|
|---|---:|---:|---:|
|Anthropic, previous branch|36.47|24.84|99.99|
|Compatible, previous branch|56.77|27.36|104.53|
|Compatible, final candidate|59.99|17.99|97.99|

Stored replay text and decoded history remain substantial costs. These snapshots do not establish the cause of every RSS difference, nor justify assigning the unexplained remainder to SQLite or native allocations. Captures occur at different instants and cannot be subtracted as an exact allocation attribution.

## Final compatible-provider result

Six-turn totals and whole-process RSS, median of three runs. **Previous branch implementation → estimator-only implementation**, not main.

|Workload|CPU seconds|Allocated MiB|Peak process RAM MiB|Sum of request-wave elapsed seconds|
|---|---:|---:|---:|---:|
|1 tool task|1.023 → 0.990|238.383 → 178.921|101.766 → 96.328|30.105 → 30.043|
|10 parallel tool tasks|6.796 → 6.607|2276.937 → 1742.817|362.812 → 314.172|30.641 → 30.563|

All individual RSS observations, in repetition order:

|Workload|Before MiB|After MiB|
|---|---|---|
|1 task|105.438, 101.766, 101.734|97.422, 95.266, 96.328|
|10 tasks|324.797, 362.812, 385.156|324.703, 314.172, 312.953|

Ten-task request-wave RSS medians also fell: **362.55 → 293.02 MiB**. Its after-run whole-process peaks occurred in final GC or verification, above the request-wave peak. Thus phase placement can change after optimization. Three samples do not guarantee a 49 MiB saving in every run.

Not every memory metric improved: worst sampled Go heap across all ten-task turns/repetitions was **212.72 → 220.68 MiB**. Worst live-output gap was **28.76 → 32.23 ms**; persistence gap was **508.95 → 508.65 ms**. Full tables below include both medians and worst observations. The fixed model streaming cadence dominates elapsed time.

### What changed

For text-only compatible history, token estimation counts the exact JSON-escaped text byte length rather than encoding and decoding the entire large text payload again. It still serializes message metadata, including tool fields, and applies the same token rounding once to the whole request. Histories containing images retain the original estimator. Invalid UTF-8 strings use the real JSON encoder to preserve replacement behavior.

The isolated estimator benchmark changed from **8.479 → 4.927 ms**, **9,817,534 → 3,733,694 allocated bytes**, and **65 → 33 allocations**. Building/decoding client history remains necessary; this is not allocation-free estimation overall.

Equivalence tests cover escapes, control bytes, Unicode, reasoning, tool arguments/IDs, images, and malformed text. Fuzzing completed 69,836 cases. Final compatible, Anthropic, and transcript tests pass; the broader LLM suites, vet, and build also passed. The build reports existing macOS deployment-target linker warnings.

## Anthropic persistence experiment: rejected

An indexed replay-conflict lookup replacement passed conflict/idempotency/rollback tests but did not improve repeated CPU measurements. Twelve Anthropic timing runs passed before the experiment was reverted:

|Workload|CPU seconds before → candidate|Allocated MiB|Peak process RAM MiB|
|---|---:|---:|---:|
|1 tool task|0.928 → 0.968|196.1 → 195.7|92.0 → 91.9|
|10 parallel tool tasks|6.154 → 6.190|1811.7 → 1815.3|260.3 → 253.8|

**No Anthropic persistence production change remains.** Exploratory compatible runs using that rejected combined candidate were interrupted and excluded; all final compatible results were rerun with only the estimator change. Anthropic's remaining request-time memory cost is unresolved.

## Evidence and reproduction

The evidence archive linked from [EVIDENCE.md](EVIDENCE.md) includes phase timing and diagnostic captures, raw final timing samples, separate final diagnostic profiles, accepted Anthropic experiment runs, rejected/final patches, driver scripts, exact harness sources, hashes, and test logs. Databases, tool fixtures, and executables are omitted. Generated raw tables use runner labels `main` and `branch`; for the FINAL optimization directories these mean **before branch 659** and **after estimator patch**, respectively. They do not mean main and branch Git revisions.

See [concurrent/README.md](concurrent/README.md) for phase sampling and heap-trigger instructions. Heap diagnostics must be run separately from accepted timing comparisons. Existing provider reports are historical and remain unchanged.

## Final optimization: complete per-turn tables

# Concurrent streaming measurements

Previous branch → estimator candidate, medians of fresh processes. CPU % uses one core = 100%; peaks are rolling ≥1-second windows, not instantaneous peaks. Each turn row covers the concurrent wave, not one task. No pooled score.

## openai-compatible: tool-use, large, 1 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|111.02 → 110.83|14.59 → 14.60|34629.00 → 34670.00|10.17 → 10.71|3.73 → 3.88|10.72 → 10.72|0.15 → 0.16|4854.20 → 4862.53|
|1|164.62 → 157.60|25.73 → 21.55|37987.00 → 37908.00|13.68 → 13.59|5.55 → 5.51|20.61 → 16.44|0.33 → 0.26|5044.72 → 5023.61|
|2|173.76 → 169.97|34.44 → 27.10|38390.00 → 38338.00|16.61 → 16.79|6.09 → 5.57|29.31 → 22.00|0.34 → 0.27|5025.61 → 5033.75|
|3|185.43 → 173.96|44.26 → 33.07|38676.00 → 38549.00|20.57 → 20.22|7.21 → 6.83|39.11 → 27.92|0.35 → 0.29|5052.51 → 5041.25|
|4|192.66 → 182.76|54.23 → 38.78|38925.00 → 38834.00|26.34 → 22.14|8.34 → 7.56|49.13 → 33.68|0.28 → 0.28|5048.39 → 5050.23|
|5|205.03 → 184.88|65.11 → 44.65|39146.00 → 39089.00|28.07 → 24.31|8.86 → 8.16|61.17 → 39.50|0.29 → 0.23|5068.28 → 5046.06|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|40.48 → 43.03|190.08 → 190.66|12669.99 → 12662.49|
|1|70.66 → 58.45|384.55 → 299.38|14808.47 → 14725.11|
|2|83.22 → 71.72|561.77 → 417.20|15161.30 → 15123.11|
|3|99.76 → 96.68|734.68 → 533.69|15420.11 → 15303.24|
|4|103.89 → 97.07|704.64 → 533.16|15585.14 → 15519.23|
|5|105.14 → 97.65|832.89 → 484.36|15811.55 → 15747.02|

|Across six turns (median run peak/total)|Before|After|
|---|---:|---:|
|Peak process RSS MiB|101.77|96.33|
|Retained Go heap MiB|2.94|2.91|
|DB MiB|9.72|9.72|
|Longest observed live delta gap ms|9.84|10.42|
|Longest observed persistence notification gap ms|505.31|505.85|

|Worst observed across all six turns and repetitions|Before|After|
|---|---:|---:|
|Sampled heap MiB|32.45|25.61|
|Sampled CPU % (~50 ms)|105.39|99.92|
|Sampled allocation MiB/s (~50 ms)|913.19|648.15|
|Rolling 1s CPU %|9.10|8.36|
|Rolling 1s allocation MiB/s|61.24|42.00|
|Rolling 1s allocations/s|15831.09|15788.19|
|Process RSS MiB|105.44|97.42|
|Live delta gap ms|10.21|10.67|
|Persistence notification gap ms|505.52|506.89|

## openai-compatible: tool-use, large, 10 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|726.07 → 717.16|132.82 → 132.94|326711.00 → 326931.00|49.68 → 49.02|22.39 → 22.25|101.18 → 98.29|1.24 → 1.18|4905.67 → 4889.35|
|1|1001.99 → 1011.60|239.72 → 205.87|376199.00 → 375718.00|91.58 → 92.39|39.90 → 37.72|190.15 → 156.62|1.81 → 1.84|5093.44 → 5092.92|
|2|1127.40 → 1069.97|324.50 → 258.14|381595.00 → 381444.00|124.50 → 117.82|49.48 → 47.22|276.09 → 209.58|1.91 → 2.37|5138.56 → 5105.73|
|3|1242.63 → 1185.16|420.62 → 324.49|384143.00 → 383355.00|144.77 → 149.33|60.42 → 54.18|372.85 → 273.39|2.11 → 1.99|5151.44 → 5148.78|
|4|1335.57 → 1258.63|522.41 → 382.24|386842.00 → 385859.00|177.54 → 180.74|69.39 → 60.51|475.87 → 332.33|2.06 → 1.44|5175.03 → 5166.70|
|5|1412.66 → 1368.89|628.62 → 441.06|389141.00 → 388329.00|203.02 → 197.42|77.21 → 69.87|579.90 → 393.46|1.97 → 1.35|5181.43 → 5178.79|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|216.75 → 213.58|1807.10 → 1740.23|109114.19 → 108927.96|
|1|297.99 → 276.32|2728.88 → 2275.40|143965.35 → 143998.82|
|2|514.69 → 506.90|4758.46 → 3502.20|148858.69 → 148894.06|
|3|670.38 → 594.34|5044.51 → 3999.36|149966.37 → 150534.57|
|4|519.00 → 518.57|5332.70 → 3597.54|151863.86 → 150784.55|
|5|747.83 → 668.80|8681.85 → 5634.87|153451.78 → 152865.87|

|Across six turns (median run peak/total)|Before|After|
|---|---:|---:|
|Peak process RSS MiB|362.81|314.17|
|Retained Go heap MiB|12.78|11.01|
|DB MiB|73.04|73.05|
|Longest observed live delta gap ms|26.06|23.99|
|Longest observed persistence notification gap ms|508.37|508.08|

|Worst observed across all six turns and repetitions|Before|After|
|---|---:|---:|
|Sampled heap MiB|212.72|220.68|
|Sampled CPU % (~50 ms)|843.35|684.08|
|Sampled allocation MiB/s (~50 ms)|8893.66|5832.62|
|Rolling 1s CPU %|78.27|73.31|
|Rolling 1s allocation MiB/s|593.48|397.05|
|Rolling 1s allocations/s|153695.04|153109.81|
|Process RSS MiB|385.16|324.70|
|Live delta gap ms|28.76|32.23|
|Persistence notification gap ms|508.95|508.65|

