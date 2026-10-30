# Anthropic replay retention: fix and measured results

Baseline: task branch df2b9bf49c84f4683db20022f27e7f33ad079b9b, after the compatible estimator fix. Candidate: same branch plus candidate.patch. This comparison is previous branch versus candidate, NOT main versus branch.

The previous turn's near-peak heap was reanalyzed by caller stacks, rather than allocator names alone. Of the 36.47 MiB attributed to SQLite columnText, 30.18 MiB came from ListByTaskChronologicalLimit/scanExecutionsNewestFirst and 6.29 MiB from readReplayMessages. A further 24.84 MiB of JSON strings came from readReplayMessages decoding replay headers, not from constructing the provider message slice. See retention-tree.txt.

Ownership: processStreamingResponse retains its historical Execution slice during the call. The service hydrates replay into another execution slice and retains original/uncompacted histories for recovery/retry. Consequently the display Output and replay fallback AssistantContent coexist with native provider rows while the response streams. These are deliberate owners, not leaked goroutines. Go heap profiling identifies allocations, not reference paths; ownership was traced through the source code and verified against database field sizes.

The old Anthropic chat path returned sw.String() as BOTH Output and TextOnlyOutput. This includes rendered thinking/tool markers and tool results. Adapter.Call used TextOnlyOutput as ReplayHeader.AssistantContent, storing an extra full rendered transcript as assistant fallback. The old fixture's 60 replay headers contain 30,288,530 assistant_content bytes. Native messages already preserve tool calls/results. This header text is then decoded on follow-up and retained for the request lifetime.

The full-output-as-text-only behavior also exists in main 9e702736a. This identifies a concrete retained-memory cost and fixes it; it does not by itself prove the cause of every prior main-versus-branch RSS difference.

Candidate changes:
- Return sw.TextString() separately from sw.String() on the chat path, matching the ordinary task path. Full UI output and native tool replay remain intact; newly stored assistant fallback contains only assistant text. Existing headers are not rewritten.
- Omit display Output on the deferred task-followup history path only when an execution is completed and has nonempty native Anthropic replay. Failed/cancelled/legacy/empty/foreign histories retain fallback output. Other history/UI readers remain unchanged.
- Decode each replay once in buildRecoveryHistory, not once for messages and again for environment metadata.

Regression: TestKnowledgeReplayAfterCancellationAndCompaction now asserts that a tool-using chat has full tool markers in Output and native tool_result blocks in replay, but exactly Done in TextOnlyOutput and AssistantContent. This fails on baseline with the complete edit_file transcript in TextOnlyOutput. Existing cancellation, compaction and fresh-adapter history checks remain in the test. The fixture now sends its final text through text_delta, matching the benchmark and normal streaming protocol.

Measurement: same phase-aware harness and peer for both revisions. Six turns per task, one or ten parallel tasks, a real 512 KiB read_file per turn and a 48 KiB streamed answer. Three repetitions per version/concurrency, alternate ordering; profiles kept separate. Fresh databases isolate newly written representation; this does not claim that old persisted fallback headers shrink automatically.

## Verdict and repeated results

Keep the three changes: the redundant representations shrink in storage and the diagnostic heap, and all three unprofiled repetitions show lower process RAM. This is a retained-memory fix, not just an allocation-throughput optimization.

**Previous branch df2b9bf49 → the same branch with the retention fix. NOT main versus branch.** Three runs per version/concurrency; twelve accepted runs, 396 completed turns. Each row covers all six request waves and all tasks combined. RSS is the whole-process peak; elapsed is the sum of request-wave durations. MiB = 1,048,576 bytes. The fixed mock streaming cadence dominates elapsed time.

|Workload|CPU seconds|Allocated MiB|Peak process RAM MiB|Request-wave elapsed seconds|Final database MiB|
|---|---:|---:|---:|---:|---:|
|1 tool task|0.981 → 0.918|198.51 → 165.30|92.64 → 76.31|30.034 → 29.957|9.12 → 7.00|
|10 parallel tool tasks|6.196 → 5.921|1835.90 → 1549.78|261.27 → 172.66|30.708 → 30.419|72.41 → 46.34|

Peak process RSS samples in repetition order:

|Workload|Before MiB|After MiB|
|---|---|---|
|1 task|91.94, 92.64, 92.69|76.23, 76.62, 76.31|
|10 tasks|271.27, 260.72, 261.27|178.42, 163.64, 172.66|

Median request-phase RSS also fell: **91.94 → 76.31 MiB** for one task and **254.39 → 169.88 MiB** for ten. Verification can create a slightly higher whole-process peak; the improvement is already visible during requests.

Tradeoffs: ten-task median longest live-output gap increased **21.60 → 25.69 ms**, and its worst observed gap increased **23.86 → 25.91 ms**. Persistence gaps stayed around 0.51 seconds. Single-task turn-0 CPU increased **109.13 → 111.38 ms**; the six-turn total improved. The worst ten-task sampled heap fell **173.15 → 87.21 MiB**, but the worst short-interval allocation rate increased **5716.57 → 5792.02 MiB/s**. Three repetitions do not establish endurance, all-workload performance, or a guaranteed saving for every run.

## Evidence linking the gain to the fix

A separate before/after pair captured turn-5 near-peak heaps without forcing GC. Capture delays were 14.917 ms before and 1.379 ms after; both remained in turn 5. These are sampled diagnostics, excluded from the repeated timing medians. Heap accounting may lag GC cycles, and a missing sampled site is not proof of literally zero bytes.

|Near-peak allocation owner / site|Before sampled in-use MiB|After sampled in-use MiB|
|---|---:|---:|
|Display history loaded by scanExecutionsNewestFirst|28.16|No large allocation sampled|
|All SQLite columnText allocations|32.37|7.34|
|Decoded JSON strings|22.32|3.17|
|Total sampled in-use heap|90.87|62.13|

The new profile's SQLite text allocations come from native replay reads, not the display-history projection. The existing raw provider rows remain intentionally available for retry and recovery.

Direct database inspection of 60 finalized turns shows assistant fallback metadata falling from **30,288,530 bytes to 2,956,680 bytes**. Full display output and native provider tool messages remain present, and the harness's output/history/tool-result checks pass. This establishes the representation change independently of fluctuating RSS measurements.

## Validation and limits

- The new regression assertion fails on baseline and passes with the fix: the old TextOnlyOutput includes the entire edit_file transcript; the new value is only `Done`. Native `tool_result` replay and full UI output remain intact.
- Anthropic adapter, persistence, service and Anthropic client suites pass. Focused handler history/replay/attachment tests pass. Vet, build and diff whitespace checks pass.
- The first combined-suite command hit the shell's 120-second timeout after Anthropic and SQLite passed. Service/client were rerun with a longer timeout and passed (service: 124.334 seconds). This was not a test assertion failure.
- Two candidate smoke scenarios (text and tool use) and two separate ten-task diagnostic runs passed, in addition to the twelve measured processes.
- The output omission currently targets deferred task-followup history. UI readers and other history-loading paths retain their existing behavior.
- Existing database headers are not migrated or rewritten. The corrected representation applies to new saves. No migration was added.
- This does not retrospectively attribute all earlier main-versus-branch differences to the text-only bug, which also existed on main. It demonstrates an independently verified fix against the current branch.

## Reproduction and evidence

Use the same phase-aware harness, peer and pinned temporary source checkouts for both binaries, as described in [concurrent/README.md](concurrent/README.md). Select `--provider anthropic --profiles large --modes tool-use --workers 1,10 --runs 3`. Run `--memory-profile` separately for ten tasks. The evidence archive in [EVIDENCE.md](EVIDENCE.md) contains the exact production patch and hash, driver, raw timing and phase samples, field-size queries, diagnostic profiles/caller stacks, regression logs, and harness sources. Raw runner labels `main` and `branch` mean **previous branch** and **candidate** in this experiment, not main/branch Git revisions. Databases and executables are omitted from the archive.

## Complete per-turn measurements

# Concurrent streaming measurements

Previous branch → retention fix, medians of fresh processes. CPU % uses one core = 100%; peaks are rolling ≥1-second windows, not instantaneous peaks. Each turn row covers the concurrent wave, not one task. No pooled score.

## anthropic: tool-use, large, 1 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|109.13 → 111.38|13.93 → 13.77|16757.00 → 16724.00|8.65 → 8.87|3.97 → 3.91|10.41 → 10.26|0.14 → 0.14|4846.89 → 4848.95|
|1|168.60 → 154.77|21.62 → 19.72|19821.00 → 19723.00|11.77 → 11.81|5.51 → 5.07|16.18 → 14.88|0.36 → 0.40|5041.70 → 5004.62|
|2|172.86 → 147.69|28.46 → 24.59|20273.00 → 20128.00|14.45 → 12.77|6.13 → 5.19|23.08 → 19.78|0.39 → 0.32|5018.55 → 5022.91|
|3|170.84 → 165.28|36.77 → 30.76|20644.00 → 20421.00|18.78 → 12.03|6.10 → 5.92|31.46 → 25.98|0.28 → 0.36|5033.03 → 5015.27|
|4|170.86 → 166.63|46.01 → 34.75|20889.00 → 20797.00|19.32 → 13.38|7.26 → 5.69|40.68 → 29.98|0.31 → 0.37|5023.70 → 5028.27|
|5|186.36 → 168.50|50.97 → 42.81|21249.00 → 21078.00|22.83 → 15.31|7.12 → 6.43|45.62 → 37.98|0.26 → 0.37|5054.73 → 5013.45|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|44.13 → 44.32|186.19 → 182.96|9346.37 → 9350.31|
|1|65.65 → 60.02|298.69 → 276.10|11157.62 → 11244.77|
|2|76.21 → 59.23|434.56 → 374.68|11663.21 → 11598.81|
|3|79.38 → 71.76|608.65 → 499.13|12061.74 → 11949.17|
|4|98.57 → 77.97|792.09 → 572.98|12306.14 → 12256.31|
|5|101.99 → 89.94|880.18 → 729.35|12671.58 → 12591.47|

|Across six turns (median run peak/total)|Before|After|
|---|---:|---:|
|Peak process RSS MiB|92.64|76.31|
|Retained Go heap MiB|2.90|2.87|
|DB MiB|9.12|7.00|
|Longest observed live delta gap ms|10.63|13.15|
|Longest observed persistence notification gap ms|501.56|501.21|

|Worst observed across all six turns and repetitions|Before|After|
|---|---:|---:|
|Sampled heap MiB|25.26|16.06|
|Sampled CPU % (~50 ms)|102.83|90.93|
|Sampled allocation MiB/s (~50 ms)|888.40|777.02|
|Rolling 1s CPU %|7.59|6.52|
|Rolling 1s allocation MiB/s|47.51|39.89|
|Rolling 1s allocations/s|12833.74|12653.76|
|Process RSS MiB|92.69|76.62|
|Live delta gap ms|11.36|13.48|
|Persistence notification gap ms|501.69|501.33|

## anthropic: tool-use, large, 10 task(s)

|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|
|---|---:|---:|---:|---:|---:|---:|---:|---:|
|0|715.61 → 714.99|126.48 → 127.74|143801.00 → 143994.00|46.54 → 48.27|22.84 → 23.16|97.15 → 96.63|1.42 → 1.39|4906.82 → 4882.98|
|1|977.07 → 976.51|197.70 → 184.83|193562.00 → 193825.00|66.12 → 57.62|34.46 → 33.82|145.78 → 140.74|2.95 → 2.45|5112.42 → 5115.85|
|2|1068.51 → 1013.91|269.54 → 230.94|200378.00 → 200416.00|88.06 → 66.93|40.48 → 39.85|219.16 → 187.48|2.20 → 4.56|5096.49 → 5072.09|
|3|1083.44 → 1047.69|341.61 → 281.50|203394.00 → 203414.00|114.40 → 67.47|45.63 → 41.34|291.37 → 237.19|1.52 → 3.46|5144.36 → 5096.24|
|4|1168.50 → 1075.82|414.07 → 333.08|206470.00 → 206643.00|144.98 → 73.79|52.93 → 44.46|364.79 → 288.66|1.74 → 3.05|5188.39 → 5087.56|
|5|1209.69 → 1124.09|490.08 → 391.13|210296.00 → 210269.00|168.98 → 79.27|55.35 → 49.00|440.42 → 344.98|1.86 → 2.90|5205.34 → 5135.74|

|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|
|---|---:|---:|---:|
|0|212.93 → 220.45|1743.95 → 1741.02|72570.79 → 72528.85|
|1|262.24 → 311.19|2148.05 → 1639.18|109419.97 → 110069.58|
|2|390.70 → 348.24|3839.89 → 2390.22|116045.84 → 116338.14|
|3|348.39 → 347.51|2942.70 → 3225.76|118722.67 → 119622.66|
|4|421.95 → 366.08|5550.89 → 4720.96|121926.84 → 122931.96|
|5|381.35 → 426.44|4267.05 → 5728.31|124971.02 → 125672.94|

|Across six turns (median run peak/total)|Before|After|
|---|---:|---:|
|Peak process RSS MiB|261.27|172.66|
|Retained Go heap MiB|3.25|3.25|
|DB MiB|72.41|46.34|
|Longest observed live delta gap ms|21.60|25.69|
|Longest observed persistence notification gap ms|509.87|506.70|

|Worst observed across all six turns and repetitions|Before|After|
|---|---:|---:|
|Sampled heap MiB|173.15|87.21|
|Sampled CPU % (~50 ms)|424.33|433.63|
|Sampled allocation MiB/s (~50 ms)|5716.57|5792.02|
|Rolling 1s CPU %|59.49|49.02|
|Rolling 1s allocation MiB/s|440.65|354.17|
|Rolling 1s allocations/s|125883.14|126866.46|
|Process RSS MiB|271.27|178.42|
|Live delta gap ms|23.86|25.91|
|Persistence notification gap ms|510.94|510.62|

