# Streaming and persistence benchmarks

This directory contains reusable streaming and persistence benchmark tools. Keep reports, raw results, profiles, and databases outside the repository.

- [Concurrent streaming](concurrent/README.md): one or ten tasks in one backend/database process; text-only and tool-use separately; CPU, allocation/reclamation rates, heap/RSS, output gaps, and storage throughout each turn.
- [Sequential app flow and browser loading](app-flow/README.md): empty task followed by five completed follow-ups, native compaction/continuation, cold opens, and verified cache-hit reopens.

Build model peers and injected test harnesses in temporary checkouts. Keep generated results, profiles, model binaries, and databases outside this worktree. Pin baseline and candidate revisions, run identical workloads separately from compilation/tests, and report each operation and mode separately. The two model peers are different: concurrent tests require the concurrent peer.

For a focused repository microbenchmark:

```sh
go test ./internal/persistence/sqlite/ -run '^$' -bench BenchmarkTranscriptPath -benchtime 10x -count 3
```

That benchmark isolates persistence operations; it is not a naturally growing conversation or a browser test.
