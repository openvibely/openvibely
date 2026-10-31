# Separate text-only and tool-use app benchmarks

Keep generated output outside this worktree. Text-only and tool-use are separate benchmarks; never combine them into one score.

## Each run

Start a fresh application process and empty file-backed database. Create one task and execute its prompt through the actual service/provider client. **Wait until the response is completed**, then send the next prompt through the real task-thread HTTP handler. Repeat until six sequential turns have completed: initial prompt plus five follow-ups. No overlapping turns and no bulk-inserted history.

The real OpenAI Responses client connects over WebSocket to a separate deterministic model process. Its CPU/memory are excluded. Text-only emits one streamed response per turn. Tool-use emits a tool request, receives the real result, and then streams the same final text. Generated IDs/temporary paths differ; final answer text and tool-file contents are deterministic.

| Profile | Assistant text per turn | Tool file in tool-use only | Stream pacing | Context tokens |
|---|---:|---:|---:|---:|
| Small | 2 KiB | 4 KiB | 64 bytes / 20 ms | 272,000 |
| Medium | 16 KiB | 64 KiB | 64 bytes / 5 ms | 272,000 |
| Large | 48 KiB | 512 KiB | 64 bytes / 5 ms | 272,000 |
| Compaction, separate scenario | 48 KiB | 64 KiB | 64 bytes / 5 ms | 64,000 |

Targets end on complete paragraphs/lines. These fixed-rate profiles are accelerated and measure application overhead; their elapsed times are not typical vendor response latency. Both versions receive the same pacing. The optional `mixed-paced` profile uses 2/8/16 KiB text repeated at 64 bytes / 80 ms; it is not part of the required four-profile comparison. Normal production 500 ms persistence batching remains enabled. Model tool-result truncation remains enabled only where applicable. Tools remain available to the real client in text-only mode, but none are requested/executed, as can happen in normal app use.

Compaction is triggered by growing context, not an injected near-full usage count. The peer estimates input usage from accumulated JSON bytes/4 and checks replies against the requested output budget. These scenarios exercise native compaction and saved checkpoint recovery. The peer also supports summary fallback, but its occurrence is reported from the trace rather than assumed. The peer's opaque checkpoint/summary is scripted: this tests mechanics, not summary quality. Native checkpoints are small marker strings; these results do not characterize the copying or storage cost of large vendor-generated encrypted checkpoints. The report identifies actual compaction turns and verifies continuation after native compaction. It does not assume identical compaction timing between text-only and tool-use.

## Measurements

Three fresh-process repetitions per mode/profile/version. Main/branch order alternates. **Each turn has its own row**, including the initial task; there is no combined score.

- CPU: app user + system CPU during the whole turn. Elapsed includes the paced model wait, tools where enabled, and real completion grace periods.
- Churn: allocated bytes and allocation counts separately. GC collections and stop-the-world pauses are reported; pauses are not total GC CPU.
- Peak Go heap: sampled every 20 ms plus the ending sample; brief peaks may be missed. End heap is measured without forced GC.
- Two GCs clear startup/migration buffers before the first turn. No forced GC between turns. Final retained heap uses two GCs while retaining the application objects.
- RSS: process high-water mark including startup and all preceding turns, not an independently reset peak per turn.
- Storage: database logical/free pages and DB/WAL/SHM file sizes per turn. Final DB size is measured after WAL checkpoint, without VACUUM. Initial schema footprint is recorded for net growth.

Fixture setup and integrity assertions are outside measured turns. Each next turn starts only after the real tracked worker has completed; a completed-execution count is verified before continuing. No simulated user think-time delay is inserted. First task creation uses the repository/service, not the scheduler or task-create form.

## Cold and cached browser opens

After generating each conversation, start that version's real handlers, templates and embedded assets, plus a fresh Chromium profile. Open the actual task page, wait for viewport hydration/Markdown, verify the latest response, navigate away, and return through the real cache. A cached-preview event must occur. Cold and cached timing/renderer CPU/JS heap are separate columns.

Cached source bytes count unique source strings, not parsed objects or total browser memory. Retained JS heap is measured after GC while on the other task. JS peaks are sampled and exclude native/GPU/worker memory. Cold means no browser cache, not a flushed OS disk cache. Cache revalidation can still occur. Browser order is branch then main.

This measures completed-thread loading, not live browser rendering of each token. It does not cover concurrent tasks, other providers, attachments, real Git worktrees, vendor latency, network faults or real inference. Three repetitions are not a latency distribution; small differences can be noise.

Tool-group boundaries, tool-state preservation, and touch access are covered by application regression tests. Benchmark completion alone is not evidence of UI correctness.

## Reproduce

Freeze local main and each candidate in separate temporary checkouts; keep the working tree free of injected tests. Copy `flow_test.go.txt` to `internal/handler/appflow_measure_test.go` and `browser_test.go.txt` to `web/templates/pages/appflow_measure_test.go` in both checkouts. Build handler/page test binaries with `go test -c` using separate output names, and build the external peer:

```sh
go test -c -o /tmp/flow-branch.test ./internal/handler
go test -c -o /tmp/browser-branch.test ./web/templates/pages
# In the main checkout:
go test -c -o /tmp/flow-main.test ./internal/handler
go test -c -o /tmp/browser-main.test ./web/templates/pages
# In this checkout:
go build -o /tmp/flow-model ./scripts/replay-memory-bench/app-flow/model-server
```

Verify the binaries list `TestAppFlow` or `TestAppFlowBrowser` with `-test.list` before running. The runners reject missing tests and incomplete artifacts. Run serially, with compilation/tests stopped. Repeat for both modes, and repetitions 1, 2, 3 (change `--first-run` and the output directory):

```sh
python3 scripts/replay-memory-bench/app-flow/run.py \
  --mode text-only --output /tmp/separate-flow/text-only/r1 --runs 1 --first-run 1 \
  --branch-binary /tmp/flow-branch.test --main-binary /tmp/flow-main.test \
  --model-binary /tmp/flow-model
python3 scripts/replay-memory-bench/app-flow/browser.py \
  --results /tmp/separate-flow/text-only/r1 \
  --branch-binary /tmp/flow-branch.test --main-binary /tmp/flow-main.test \
  --branch-browser /tmp/browser-branch.test --main-browser /tmp/browser-main.test
python3 scripts/replay-memory-bench/app-flow/summarize.py \
  --mode text-only --results /tmp/separate-flow/text-only \
  --output /tmp/report/text-only --metadata /path/to/build-metadata.json
```

Repeat using `tool-use` instead of `text-only`. Report generation rejects mixed modes, missing/extra normal model requests, tools in the text-only baseline, missing tool exchanges, missing prior context outside compaction, output-budget violations, missing compaction continuation, and missing cache hits. Remove temporary installed tests afterward.
