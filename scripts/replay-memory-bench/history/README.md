# History-loading harness

Synthetic backend-only history loading through actual HTTP handlers and production SQLite reader/writer routing. No model, credentials, existing database, or real conversations.

Validate the harness without running the large performance matrix:

```sh
python3 scripts/replay-memory-bench/history/run.py --smoke --output /tmp/history-smoke
```

When ready to benchmark:

```sh
python3 scripts/replay-memory-bench/history/run.py --output /tmp/history-results
```

Default matrix: 1,000 / 10,000 / 45,000 display blocks in one execution, completed / running / plan-bearing, three repetitions. Approximately 4 KiB synthetic escaped text per block. The smoke case uses 600 blocks (large enough to page) and one repetition. These are long individual executions, not thousands of separate executions. Existing tests cover tool-pair page integrity; this fixture isolates metadata scaling with simple text blocks.

Each fresh process creates a file-backed temporary database, opens the task route, requests the latest output page, then walks every older page. It checks latest content, exact reassembled output, cursor progress, stable revision, and denied access from another project. Running/plan responses may return everything; no artificial page limit is imposed on production behavior.

Outputs: manifest (revision, dirty flag, Go/platform, fixture parameters, harness hashes), individual case JSON/logs, combined results.json, report.md with per-operation samples. Setup is excluded from operation timing. The first HTTP open is measured before any history-page validation. Forced GC precedes each measured operation. Every repetition uses a fresh database/process; OS disk cache is not flushed, so these are not guaranteed cold-disk measurements.

Metrics:
- elapsed server request time and Go allocated bytes, including handler rendering/JSON encoding;
- response bytes;
- database requests, returned text bytes, and rows returned by the SQL driver.

**Returned rows are not SQLite internal rows visited or VM steps.** They expose metadata materialized in Go but not hidden index/aggregate work. No CPU percentage, browser rendering, process peak RAM, or retained-heap claim is made. Fixture/validation strings live in the same test process, so process RSS would mix harness memory with application memory.

Compare the same suite and parameters across revisions. It does not alter the provider benchmark, application behavior, or CI configuration.
