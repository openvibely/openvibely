# Streaming and persistence benchmarks

This directory contains reusable streaming and persistence benchmark tools. Keep reports, raw results, profiles, and databases outside the repository.

- [History loading](history/README.md): file-backed synthetic thread opens and older pages, completed/running/plan responses, server duration, allocations, response sizes, and driver row counts.
- [Repeated checkpoint follow-ups](checkpoint/README.md): generated 11 MiB image checkpoint, real local provider transports, cache reuse/invalidation/eviction, allocations and retained memory. No user data or paid calls.
- [Concurrent streaming](concurrent/README.md): one or ten tasks in one backend/database process; text-only and tool-use separately; CPU, allocation/reclamation rates, heap/RSS, output gaps, and storage throughout each turn.
- [Sequential app flow and browser loading](app-flow/README.md): empty task followed by five completed follow-ups, native compaction/continuation, cold opens, and verified cache-hit reopens.

Build model peers and injected test harnesses in temporary checkouts. Keep generated results, profiles, model binaries, and databases outside this worktree. Pin baseline and candidate revisions, run identical workloads separately from compilation/tests, and report each operation and mode separately. The two model peers are different: concurrent tests require the concurrent peer.

For a focused repository microbenchmark:

```sh
go test ./internal/persistence/sqlite/ -run '^$' -bench BenchmarkTranscriptPath -benchtime 10x -count 3
```

That benchmark isolates persistence operations; it is not a naturally growing conversation or a browser test.

### Long-thread checkpoint resume

Keep the ordinary streaming runs above; they do not cover loading a saved
checkpoint on a long thread. The additional service benchmark creates twenty
persisted follow-ups with 10 MiB of replay data each, then resumes through the
production checkpoint/budget boundary:

```sh
go test ./internal/service -run '^$' -bench '^BenchmarkCheckpointResume$' -benchtime=1x -count=3 -timeout=10m
```

Cases cover a fully covered history, a newer uncovered follow-up, missing and
incompatible checkpoints, and native checkpoint resumes for OpenAI/Anthropic.
Textual checkpoint cases also cover OpenAI-compatible and Ollama. Native cases
include a 10 MiB saved checkpoint. `replays/op` reports how many executions were
hydrated (expected 0 when fully covered, 1 for the suffix, 20 for fallback).
The adapter is a local verifier: no model calls or inference timing are involved.
A deliberately large synthetic token window keeps the fallback cases admissible;
this is not a claim about real provider limits. Database/fixture creation is
excluded from timing and allocation counters. `B/op` measures Go allocations,
not peak RSS or SQLite's native memory. Use a fresh process and OS sampling if
measuring those separately. The matching regression test is
`TestCheckpointResumeLoadsOnlyUncoveredReplay`.
