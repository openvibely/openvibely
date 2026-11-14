# Frontend renderer benchmarks

Manual Chrome benchmarks for a single growing running execution, in Compact and Full modes. These use the production renderer and bundled assets from each selected Git revision, with synthetic local data. No provider, database, or external service is needed.

These measure renderer updates, **not end-to-end page loading or scrolling**. Normal virtualization stays enabled. There is no fully materialized preview stress case in this harness.

## Run

Requirements: Python 3.9+, Go matching the repository, Git, tar, macOS or Linux `ps`, and Google Chrome/Chromium. On macOS the harness uses the standard Google Chrome application; otherwise it searches PATH for google-chrome, chromium, or chromium-browser. The Go module cache must be populated or module downloads must be available during builds.

From any directory:

```sh
python3 scripts/replay-memory-bench/frontend/run.py \
  --baseline main --candidate HEAD --suite all --runs 5 \
  --output /tmp/openvibely-frontend-comparison
```

The output directory must not exist and must be outside this worktree. Both refs are resolved to immutable commits before running. **Uncommitted renderer changes are not included**. The harness itself comes from the directory containing this runner, so the same fixtures are injected into both revisions. Revisions need the existing component browser-test helpers and renderer APIs; incompatible older revisions fail compilation rather than silently skipping coverage.

For a quick verification:

```sh
python3 scripts/replay-memory-bench/frontend/run.py \
  --baseline main --candidate HEAD --suite timing --mode compact --runs 1 \
  --output /tmp/openvibely-frontend-smoke
```

Options:

- `--suite timing`: ordinary output without previews; tools, earlier unmatched tool, repeated thinking, unfinished result.
- `--suite memory`: ordinary output without previews; heap, DOM counts, Chrome process RSS, transcript reopen cycles, and page reload cycles.
- `--suite preview`: one real HTML preview near the live tail, inline and expanded; streaming time, heap, process RSS, removal and reload.
- `--suite all`: all three, reported separately.
- `--mode compact|full|both` (default both).
- `--runs N` (default five). Use five for comparisons; one only verifies the runner.

All suites use 200, 1,000 and 5,000 calls. The full five-run comparison takes many minutes. Builds finish before browser measurements. Browser runs are sequential, with baseline/candidate order alternating between repetitions. Each leaf case starts a fresh Chrome process/profile. Temporary source archives and binaries are deleted on exit; original worktrees remain untouched. Missing Chrome, skipped cases, failed assertions, and missing results fail the run. On failure, completed samples and logs remain in the output directory; rerun with a new output directory.

The `.go.txt` files are manual fixtures, injected as `_test.go` files only into temporary archives. They do not add slow performance benchmarks to ordinary CI. Existing committed browser regressions provide deterministic correctness coverage.

## What is measured

### Ordinary output: timing

Each fixture grows through all three sizes and times an awaited `renderLiveChatContent` update. It records initial render time, update time, repeated source processed, segments linked, DOM elements, and mounted tools. An unfinished result contains 10,000 repetitions of `output\n` (about 70 KB) per size, is warmed once, then receives `more output\n`. Other scenarios append one completed call or thinking section. Timing uses the existing production browser test helper, with no memory polling or forced GC. The renderer promise is not an end-to-end paint measurement.

### Ordinary output: memory

Each size receives a fresh transcript. The unfinished-result fixture uses 100,000 repetitions of `output\n` (about 700 KB), deliberately larger than the timing fixture. After loading and one update, it measures three remove/reopen cycles at 5,000 calls and two full page reload/reopen cycles. These are fixture reloads, not the complete application navigation lifecycle.

### With one preview

The production preview loader and viewer use a local HTML fixture. Only visible tool cards are mounted (asserted below 30). Three calls arrive while the preview is inline, then one arrives while expanded. Identity, entered text, iframe load count, and scroll state are checked. Closing restores the inline frame; removal and reload must leave zero iframes.

Opening the viewer can itself reset scroll on either revision. The fixture records `scrollBeforeReset`, then sets and verifies scroll immediately before streaming. It tests **streaming preservation**, not a guarantee that viewer opening preserves scroll. Inline timings summarize the three updates per sample first, then take the median across repetitions.

### Memory interpretation

Heap is Chrome's `performance.memory.usedJSHeapSize`, with precise-memory-info enabled. Retained snapshots call exposed GC twice; `beforeGC` is recorded separately. Heap peaks are sampled every 25 ms and process RSS every 250 ms, so neither is an exact maximum. RSS sums the Chrome process tree from `ps`, can double-count shared pages, and is not exact physical RAM usage. RSS may remain high after objects/iframes are removed because Chrome retains allocated pages.

The summary subtracts each browser's empty-page heap/RSS from corresponding snapshots and expresses memory in MiB. Peak counters span the process lifetime for RSS; heap peak counters reset on page reload, so inspect phases rather than interpreting the last heap sample as the whole-run maximum. Process peak deltas subtract baseline RSS, not baseline peak. Memory polling and forced GC perturb timing; compare `timing` suite values separately from memory/preview runs.

Short reopen cycles can reveal retained growth but do not establish leak freedom. Process memory is noisy: keep the machine idle and compare medians and ranges, not isolated samples.

## Outputs

- `manifest.json`: pinned commits, arguments, platform, Go/Chrome versions, harness hashes.
- `*-build.log`, per-case `*.log`: compilation and full browser-test output.
- `samples.json`: all raw reports, saved after every completed case (bytes for memory, milliseconds for time).
- `summary.json`, `summary.md`: per-revision, per-case, per-size medians, ranges and sample counts. Preview/no-preview and measured phases stay distinct.

Keep results, profiles, and dumps outside the repository. Do not turn noisy timing or RSS thresholds into CI assertions.

Check the summary calculations without launching Chrome:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/replay-memory-bench/frontend -p 'test_*.py'
```

### Running-turn HTTP loading

The renderer harness above does not measure application loading. The handler test
below loads a real task page, uses the production websocket and history endpoint,
and exercises concurrent paging, late results, navigation, and completion:

```sh
go test ./internal/handler -run '^TestRunningOutputWindowInChrome$' -count=5 -v > /tmp/running-window.log
OV_RUNNING_WINDOW_FULL_SEED=1 go test ./internal/handler -run '^TestRunningOutputWindowInChrome$' -count=5 -v > /tmp/running-full-seed.log
```

The second command is a test-only control: it substitutes the full transcript in
the initial HTML while retaining the same current renderer. It is **not a Git
revision comparison**. Both cases use 5,000 calls plus an earlier pending tool.
Diagnostics report time from navigation start to hydrated rows, uncompressed HTML
and transcript bytes, retained JS heap after GC, DOM count, and one sampled Chrome
process-tree RSS total. RSS is absolute (not baseline-adjusted), can double-count
shared memory, and is not a peak. These are local-fixture measurements, not real
network latency. Timing/RSS values are reported rather than used as CI thresholds.

### Mixed-output application loading

```sh
python3 scripts/replay-memory-bench/frontend/mixed_loading.py \
  --baseline main --candidate HEAD --runs 3 \
  --output /tmp/ov-mixed-loading
```

This separate harness opens the actual task page and uses its HTTP history and
WebSocket endpoints. It compares pinned Git revisions, with prose, lists, fenced
code, thinking, small/large tool results, and an earlier unresolved tool. Sizes
are approximately 1, 10, and 50 MiB (`--sizes 1` for a smoke test). Both modes run.
Use `--huge` to append an additional unfinished result of approximately the chosen
size; that fixture's total source is consequently about twice the selected size.
Use `--mode compact` or `--mode full` to split long batches. `--resume` reuses saved
samples and archives after verifying the revisions and fixture have not changed;
failed samples remain recorded rather than being silently retried.

Results include navigation-to-renderer-idle latency (not first paint), uncompressed
initial HTML and source bytes, retained JS heap after GC, DOM count, and a single
absolute process-tree RSS sample. `updateMS` includes the append HTTP request,
stream delivery, waiting for the live render promise for the exact received source,
and a 30 ms polling interval; it is **not** comparable to renderer-only update timings.
Global idle flags alone are insufficient: a streaming timer/frame may not have started
rendering yet. The fixture tracks the renderer's source and promise across reopening.
`updateRenderOutcome` and `splitRenderOutcome` distinguish `rendered` from `cancelled`
(including timeout); summaries count each outcome separately. Cancelled renders are
not counted as successful tool pairing and their old row models are not inspected.
Earlier results using only idle flags may undercount HTTP update latency or falsely
report a missing result association; rerun them with this harness. In `--huge` cases,
the update closes the large unfinished result before delivering the late result.
RSS is not a peak, is not baseline-adjusted, and can count shared memory twice.
There are no previews. Paging/concurrent updates are checked when a revision
supports running windows. Reopening and completion are checked on both revisions.
Failed or timed-out cases remain in the results and are excluded from medians.
The runner saves isolated revision archives and logs outside the repository.

The `fallback` field records whether initial display used the renderer's plain-text
fallback. A run can pass transport/paging checks yet fall back instead of displaying
normal tool cards; report that separately, rather than describing it as successful
normal rendering. Large-item cases explicitly exercise this limitation.
