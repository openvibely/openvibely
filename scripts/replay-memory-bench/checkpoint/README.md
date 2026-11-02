# Repeated follow-ups from a large checkpoint

Run from any directory (Go, Python 3, and `ps` on Linux/macOS):

```sh
python3 scripts/replay-memory-bench/checkpoint/run.py --output /tmp/checkpoint-results --runs 3
```

Use a new output directory for each run. No API keys, network model services,
user database, or saved conversation are needed. The runner builds the existing
concurrent model peer and the service test binary in a temporary directory.
Each case gets fresh app and mock processes. Both provider endpoints are pinned
to the loopback peer, and the app uses a disposable SQLite database.

The fixture generates **11 MiB of base64 image data** (a small valid PNG with
padding), marker text, and a saved checkpoint. It sends six follow-ups through
real service orchestration, persistence, and provider adapters. The checkpoint
remains the same across follow-ups; newer replay history grows normally. An
artificially large context limit avoids accidental compaction: this is a memory
workload, not a claim about vendor image/context limits.

Providers: OpenAI Responses WebSocket, Anthropic Messages SSE, and
OpenAI-compatible Chat Completions SSE. Ollama uses the compatible implementation;
its checkpoint compatibility checks remain covered by the service test matrix.

Modes:

- `reload`: disables only the service checkpoint cache; transport behavior remains normal.
- `reuse`: cold first request, then normal cache reuse.
- `update`: changes checkpoint metadata before turn 2; exactly one reload is required.
- `evict`: explicitly removes the cache entry before turn 2; exactly one reload is required.

The peer checks reconstructed context, including incremental WebSocket requests.
Every run must preserve the image, checkpoint marker, and previous answer, return
the expected answer, make exactly six requests, and read checkpoint payloads
exactly as expected. The runner also executes the existing cache/provider-switch
correctness tests before measurement, including chronological reconstruction
beyond the UI window for every provider. New OpenAI exchanges must not repeat
the image or write execution links. RAM/timing numbers are diagnostic, not
flaky pass/fail thresholds. This runner is standalone; it does not modify CI.

Each case writes `result.json` (per-turn allocations, forced-GC retained Go heap,
request duration, checkpoint reads), `rss.json` (app RSS sampled approximately
every 100 ms), and logs. `manifest.json` records revision, dirty state, Go version,
workload, and host. No conversation bodies are written to results. Synthetic
fixture creation is outside per-request allocation/timing counters. RSS includes
setup and SQLite; it can miss short peaks. Go heap excludes native SQLite memory.
GC after each request deliberately measures retained state; this is not a natural
streaming-latency benchmark. The peer's memory is excluded. Other normal app
work—including uncovered replay loading, request encoding and checkpoint saves—
is included: this is broader than the checkpoint-loading microbenchmark.

For a quick run:

```sh
python3 scripts/replay-memory-bench/checkpoint/run.py --output /tmp/checkpoint-smoke --providers openai --modes reload,reuse --turns 4
```

Keep the existing streaming and checkpoint-resume benchmarks; this adds the
large, repeated-follow-up case rather than replacing them.
