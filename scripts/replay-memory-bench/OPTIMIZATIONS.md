# Follow-up optimizations

Base: task branch commit `1920be9db`. These changes are not a comparison against main.

1. Format read_file lines using byte searches and a pre-sized builder. Preserve offsets, limits, trailing newlines and truncation text.
2. Hash finalized output through a read-only string view instead of copying the full transcript.
3. Search for delimiter candidates before applying the display-control regexp; find closing marker lines without scanning all tool text through a regexp.
4. Migration 220 adds a covering index on stream execution ID and end offset. Keep offset assignment in SQLite, preserving retry/finalization behavior rather than caching offsets in the writer.
5. Use a separate history projection for deferred OpenAI task follow-ups. Omit display output only for completed turns with a nonempty native Responses checkpoint. Failed turns, empty checkpoints, foreign formats and other providers retain their existing path.
6. Retain the SQLite driver's owned string storage as immutable replay JSON instead of copying each provider item again. This relies on the existing immutable ReplayRows contract.
7. Reuse encoded WebSocket request properties, including tool schemas. Splice valid raw input items when present; decoded-map histories retain whole-payload JSON encoding. Preserve null input and JSON encoding errors.

## Measurements and validation

Earlier focused measurements in this worktree:

| Operation | Before | After |
| --- | --- | --- |
| 512 KiB read_file fixture | ~740 µs, 3.53 MB, 9,796 allocations | ~173 µs, 0.90 MB, 12 allocations |
| Display parsing and records fixture | ~4.0 ms, 1.14 MB | ~0.94 ms, 0.99 MB |

These are microbenchmarks, not whole-app speedups. The raw-input WebSocket microbenchmark reduced bytes/time but increased allocation count for mixed raw/map inputs; it does not establish a whole-app improvement.

The intermediate `/tmp/ovbench2` comparison was stopped and rejected: it used an unfinished build and overlapped with compilation/tests. Do not use its timing data. The final-build concurrent harness run at `/tmp/ovbench-final-validation` is an integrity check (one repetition of each large text/tool, one/ten-worker scenario), not a final three-repetition performance comparison.

Regression tests cover exact read_file formatting, delimiter equivalence, JSON request semantics, native-history fallback boundaries, and the stream lookup's covering-index plan. Migration tests include upgrades, downgrades and adoption of older development schemas.
