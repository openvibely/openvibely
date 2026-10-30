# Concurrent streaming resource checks

Provider comparison: [Anthropic and OpenAI-compatible results](../PROVIDER_RESULTS.md). Earlier OpenAI Responses measurements: [results](../RESULTS.md). Historical reports, profiles, raw data, and build metadata are in the [evidence archive](../EVIDENCE.md). Keep raw outputs outside this worktree.

This extends the frozen app-flow workload; sequential/browser measurements remain separate. No UI changes. One real app/service/database process runs either one task or ten tasks concurrently. Every task starts empty and completes its response before its next follow-up. Six turns per task. A round barrier launches each wave together, deliberately stressing simultaneous initialization and completion. This is not ten independent app processes.

Text-only and tool-use are separate runs. Small/medium/large answers are 2/16/48 KiB, emitted as 64-byte deltas at 20/5/5 ms; tool-use adds real 4/64/512 KiB file reads. These are deterministic accelerated streams, not claims about vendor token speed. The peer supports OpenAI Responses HTTP/WebSocket, Anthropic Messages SSE, and OpenAI-compatible Chat Completions SSE. Select the app adapter with `--provider openai|anthropic|openai-compatible` (default: `openai`). Model inference and peer CPU are outside the app process. Model request history and completed output are checked; `max_active` verifies actual overlapping streams. No compaction scenario or browser measurements are included here; use the separate [app-flow harness](../app-flow/README.md).

## Measurements during each wave

- Every 50 ms: cumulative process CPU, live Go heap, allocated and reclaimed bytes/objects, live-object count, GC count and total stop-the-world pause time. Samples are retained in a preallocated slice and written **after** the timed wave.
- CPU/allocation peaks use rolling windows of at least one second within a wave, and adjacent sampled intervals of at least 40 ms (labelled ~50 ms). CPU 100% means one core, so a brief 400% sample is about four cores, not 400% of the machine. Short small-response waves may have no full one-second window (reported n/a). Peaks include request preparation and completion, not only token reception. The reports include medians of peaks and the worst observed values across all three repetitions. No duration-normalized rate is a resident-memory measurement.
- An external `ps` samples current RSS every ~100 ms; Go also records process lifetime RSS high-water. Startup is included in process high-water, and polling can miss shorter spikes.
- A constant-size observer per execution records longest observed live-output and persistence-notification gaps. Follow-up IDs are looked up once after HTTP admission; the observer can miss the earliest deltas. These are **not** browser latency or true time-to-first-token measurements. Tool gaps can legitimately include tool execution. No text is retained by the observer.
- Timed-wave allocation totals include instrumentation cost. `runtime.ReadMemStats` briefly coordinates the Go runtime, so sampling itself can perturb short pauses; the same harness is used on both versions. GC pause is not total GC CPU. No forced GC between turns; two collections after setup and after all turns measure retained memory with the app alive.
- Output integrity, completed turn counts, zero tools in text-only, real tool results in tool-use, and overlapping streams must pass before a run is accepted. Database size is measured after final WAL checkpoint; per-wave files retain WAL/SHM measurements too.

## Reproduce

Build the peer with `go build -o /tmp/concurrent-model ./scripts/replay-memory-bench/concurrent/model-server`. Use separate temporary checkouts for the pinned versions. Copy this directory's `flow_test.go.txt` to `internal/handler/concurrent_measure_test.go` in each temporary checkout, then build with `go test -c -o /tmp/concurrent-VERSION.test ./internal/handler`. Remove injected files after building. Verify `-test.list='^TestConcurrentFlow$'` lists the test. Physical injection avoids `/tmp` versus `/private/tmp` overlay identity problems. Never install measurement files in the working application packages. Both versions use the **same** harness and concurrent peer; the sequential app-flow peer is not interchangeable.

```
python3 scripts/replay-memory-bench/concurrent/run.py \
  --output /tmp/concurrent-r1 --main-binary /tmp/concurrent-main.test \
  --branch-binary /tmp/concurrent-branch.test --model-binary /tmp/concurrent-model \
  --runs 1 --first-run 1
```

Repeat with new directories and `--first-run 2` / `3`; version order alternates. Run one measurement process at a time, with no tests/builds competing. `--profile` collects diagnostic pprof files: profile runs must remain separate from timing runs. `summarize.py DIR... --output REPORT_DIR` preserves per-turn/scenario results and raw counters; it produces no combined performance score. Raw `samples-N.json`, `rss.json`, `delivery.json`, and `turns.json` remain in each run directory.


## Phase-aligned memory diagnostics

The harness writes `phases.json` with wall-clock nanoseconds for startup, each request wave, between-turn work, final full-history verification, final GC, validation, and checkpoint/teardown. External `rss.json` samples now include wall-clock nanoseconds as a third column; the first two columns remain elapsed seconds and RSS bytes. Use `memory_phases.py ROOT... --output REPORT` to attribute samples to phases. A short unsampled phase is reported as absent, not as zero memory. A phase's RSS includes memory retained from earlier work.

Run an ordinary measurement first. Then repeat separately with `--memory-profile`. In that diagnostic run the external sampler requests a heap profile when a phase's sampled RSS rises by at least 4 MiB above its last capture request. The app polls requests every 25 ms, writes `memory-PHASE.pprof`, and records trigger/capture times, phases and heap counters in `memory-captures.json`. Later high-water captures overwrite the same phase's profile; the metadata retains all capture attempts. Check for a phase change between trigger and capture before attribution. An approximately 100 ms RSS sampler can miss short peaks.

No GC is forced at the peak: profiles identify sampled allocation sites but Go's in-use accounting can lag by up to two GC cycles. The pre/post counters and capture delay are provided rather than claiming an exact instantaneous live-heap snapshot. Profiling itself allocates and can perturb RSS; diagnostic runs are rejected by `summarize.py` and must not be used as timing/RAM comparison results. Startup/final retained-heap GC behavior is unchanged.

## Diagnostics and limitations

Use `phases.py` for preparation/streaming/completion intervals, `reclamation.py` for natural frees and live counts, and `timeline.py` for individual timelines. `allocation_attribution.py` uses the `profile_sites.go.txt` helper and full-sampling profiles; consult `--help` and archived reconstruction scripts. Diagnostic timing/GC/heap results must not enter ordinary performance tables. Frees can reclaim objects from earlier turns.

Each run exercises only its selected provider, with one subscriber per task, not ten browser views. Reports keep providers separate. Anthropic and compatible runs set the same explicit 2,000,000-token context budget on both revisions (compaction threshold 1,500,000) to measure growing history without compaction; this is a benchmark setting, not a claim about vendor model limits. The fixture emits text and one real read_file tool round per turn, without thinking/reasoning, images, OAuth, retries, or vendor prompt-cache behavior. Barriers deliberately synchronize tasks, and fixed accelerated pacing is not remote-model latency. Six turns do not establish endurance or leak freedom. Process RSS includes setup and final integrity checks; Go heap excludes native SQLite memory. Record source revisions, compiler, host, harness hashes, and rejected runs with each new result.

## Comparing the additional providers

Use `--provider anthropic` or `--provider openai-compatible` and distinct output directories. The same injected Go harness, peer binary, workload, and settings must be used for baseline and candidate. Pin `--baseline-revision` and `--candidate-revision`; the runner records revisions, binary and harness SHA-256 hashes, host details, and settings in `experiment.json`. Every accepted run also writes `accepted.json`. Do not combine smoke/profile runs with measurements.

For the full large-workload comparison, use `--profiles large --modes text-only,tool-use --workers 1,10 --runs 3` for each provider. This is 48 fresh app/database processes and 1,584 completed turns across the two providers and revisions. Run the two providers sequentially with no builds/tests/profiling alongside them. `summarize.py` accepts both output directories and groups by provider as well as mode, size, and concurrency. Turn 0 is the initial request; turn 5 is the fifth follow-up. Process CPU and allocations cover the whole concurrent wave.

Integrity gates check exact request counts (no hidden retry or extra tool round), prior-turn context, protocol identity, actual tool-result messages, complete stored answers, persistence/live events, and overlapping streams. The mock understands each provider's real tool-call/result schema; it does not route these providers through the OpenAI Responses client.

Phase investigation and the tested estimator optimization: [memory follow-up report](../PROVIDER_MEMORY_FOLLOWUP.md).

Anthropic ownership tracing and repeated retention-fix measurements: [retention results](../ANTHROPIC_RETENTION_RESULTS.md).
