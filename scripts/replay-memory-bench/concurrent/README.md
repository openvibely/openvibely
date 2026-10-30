# Concurrent streaming resource checks

Latest summary: [results](../RESULTS.md). Historical reports, profiles, raw data, and build metadata are in the [evidence archive](../EVIDENCE.md). Keep new outputs outside this worktree.

This extends the frozen app-flow workload; sequential/browser measurements remain separate. No UI changes. One real app/service/database process runs either one task or ten tasks concurrently. Every task starts empty and completes its response before its next follow-up. Six turns per task. A round barrier launches each wave together, deliberately stressing simultaneous initialization and completion. This is not ten independent app processes.

Text-only and tool-use are separate runs. Small/medium/large answers are 2/16/48 KiB, emitted as 64-byte deltas at 20/5/5 ms; tool-use adds real 4/64/512 KiB file reads. These are deterministic accelerated streams, not claims about vendor token speed. The peer supports the actual client's HTTP/WebSocket selection. Model inference and peer CPU are outside the app process. Model request history and completed output are checked; `max_active` verifies actual overlapping streams. No compaction scenario or browser measurements are included here; use the separate [app-flow harness](../app-flow/README.md).

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


## Diagnostics and limitations

Use `phases.py` for preparation/streaming/completion intervals, `reclamation.py` for natural frees and live counts, and `timeline.py` for individual timelines. `allocation_attribution.py` uses the `profile_sites.go.txt` helper and full-sampling profiles; consult `--help` and archived reconstruction scripts. Diagnostic timing/GC/heap results must not enter ordinary performance tables. Frees can reclaim objects from earlier turns.

Only the OpenAI Responses path is exercised, with one subscriber per task, not ten browser views. Barriers deliberately synchronize tasks, and fixed accelerated pacing is not remote-model latency. Six turns do not establish endurance or leak freedom. Process RSS includes setup and final integrity checks; Go heap excludes native SQLite memory. Record source revisions, compiler, host, harness hashes, and rejected runs with each new result.
