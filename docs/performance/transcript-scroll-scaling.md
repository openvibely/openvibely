# Transcript scroll scaling (#1622)

## Implementation

Ordinary scroll refreshes no longer discover, measure, or inspect every loaded row. A DOM-ordered index searches sibling bounds with binary search, then walks only the intersecting viewport/overscan range. Nested owners have separate sibling levels. Actual browser bounds keep the search correct across variable heights, margins, non-row content, and delayed layout without maintaining a second height model. Range measurements are reused for anchor and mount decisions. The final anchor restore supplies the resize snapshot instead of measuring it again during observer bookkeeping.

A mounted-row set bounds detach/protection checks, pending-render collection, and ResizeObserver subscriptions. Content mutations invalidate the index; synchronous hydration also consumes pending mutation records. Prepending history rebuilds DOM order. Retained nested owners are rediscovered on remount, and transcript teardown releases the index and mounted set.

Search costs are logarithmic in the number of siblings plus the intersecting rows and mounted/protected rows. Content changes can still rebuild the full index; width changes revisit estimates. Find, selection, and printing deliberately materialize the complete transcript. The DOM still retains placeholders for loaded rows, and browser layout costs are not independent of DOM size.

## Measurement

Baseline: `9c7bc835f746e834e31d3f54cc4afd6a3393c26a`, using its unmodified `web/assets/chat-virtual-list.js`.

Environment: Apple M5 Pro (arm64), macOS 27.0.1, Google Chrome 157.0.8103.38, headless with GPU disabled. Both versions run in the same browser process on the same machine. No other build or test runs concurrently with the paired measurements.

`TestBrowserPerformance_VirtualScrollRowCountScaling` uses deterministic 100px rows at counts of 200, 1,000, and 5,000, an 800px-wide transcript, and a 900px transcript viewport. Each trial creates a fresh iframe and settles initial rendering before measurement. Its fixed 25-position sequence includes 300px movements, distant windows, both ends, backward travel, and direction reversals. Visible text and scroll offsets are checked after each movement; the mounted-row count must remain below 40.

There are 30 paired trials per row count, alternating which version runs first. A frame probe records time inside refresh and resize-anchor callbacks, excluding nested double-counting, and counts all row/placeholder `getBoundingClientRect()` calls in each measured frame. Viewport/container reads are excluded from the geometry count. For each run it takes the nearest-rank p95 across work-bearing frames, then reports the median of the 30 run-level p95 values. These are instrumented wall-clock measurements, including synchronous layout charged to those callbacks, rather than total frame/paint latency.

Final results (medians of 30 run-level p95 values):

| Rows | Baseline refresh/anchor time | Indexed refresh/anchor time | Baseline row reads/frame | Indexed row reads/frame |
| ---: | ---: | ---: | ---: | ---: |
| 200 | 2.75 ms | 1.10 ms | 1,388 | 159 |
| 1,000 | 4.85 ms | 1.60 ms | 6,988 | 173 |
| 5,000 | 13.75 ms | 0.90 ms | 34,988 | 189 |

All acceptance thresholds passed: 5,000-row time improved by 93.5%; 200-row time improved by 60%; and 5,000-row geometry reads were 1.19 times the 200-row result. Absolute timings include timer quantization, JIT, layout, and scheduling variability; the nonmonotonic indexed times should not be interpreted as larger transcripts being faster.

Reproduce from the repository root:

```sh
git show 9c7bc835f746e834e31d3f54cc4afd6a3393c26a:web/assets/chat-virtual-list.js > /tmp/chat-virtual-list-baseline.js
OPENVIBELY_VIRTUAL_SCROLL_BASELINE=/tmp/chat-virtual-list-baseline.js \
  go test ./web/templates/components -run '^TestBrowserPerformance_VirtualScrollRowCountScaling$' -count=1 -v
```

Without the environment variable, the test runs two trials per fixture against the current source and enforces geometry scaling, visible content, scroll offsets, and the mounted-row bound. Timing thresholds are enforced only in paired mode: at least 50% improvement at 5,000 rows, and no more than a 10% regression at 200 rows. Geometry reads at 5,000 rows must not exceed twice the 200-row count.

## Regression validation

The browser suite covers variable/estimated heights, delayed resizing, bottom following, prepend/history restoration, hidden workspaces, compact/nested output, focused rows, Find, selection, and printing. The added variable-height test checks both scroll directions and reversal, retains the same video element, materializes all 200 rows for printing, and resumes windowing afterward. The existing page fixture retains its fewer-than-40 mounted tool-row assertion.

```sh
scripts/build-assets.sh
go build ./cmd/server
go test ./web/templates/components ./web/templates/pages \
  -run 'TestBrowserFunctional_(Virtual|LazyTranscript|HiddenVirtual|SelectAll|Compact|Paging|ThreadCache|Earlier|Upward|ColdThread)' -count=1
```

## Memory impact

Memory was measured separately after rebasing onto local main `e16aa84df`, using that commit's unmodified virtual-list source as the baseline. The timing results above were not rerun or replaced. The memory run used the same machine and Chrome version, with **10 paired trials per row count**, alternating version order. Every individual trial started a fresh Chrome process and profile. The fixture uses the same 100px rows, 900px viewport, and 25-position scroll sequence, with visible-content and mounted-row assertions.

Snapshots were taken before installing rows, after installation, after scrolling, and after removing the transcript and disposing its virtual list. Each snapshot followed two forced garbage collections. These are retained-memory measurements, not peak allocations during a frame, and the fixture contains short text rather than real message bodies or media.

Median V8 used heap (`Runtime.getHeapUsage.usedSize`), including fixture/script overhead:

| Rows | Before: initial | After: initial | Before: after scrolling | After: after scrolling | Median paired increase after scrolling |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 200 | 712.5 KiB | 725.6 KiB | 746.8 KiB | 767.9 KiB | 21.1 KiB |
| 1,000 | 871.1 KiB | 899.1 KiB | 909.1 KiB | 947.7 KiB | 38.6 KiB |
| 5,000 | 1,694.4 KiB | 1,804.7 KiB | 1,728.9 KiB | 1,853.7 KiB | 125.0 KiB |

The added index and tracking structures therefore have a measurable JavaScript memory cost: approximately **0.12 MiB at 5,000 rows after scrolling**. Median paired differences need not exactly equal differences between the two medians.

JavaScript heap is not the whole cost. Chrome's experimental `embedderHeapUsedSize` counter was unchanged initially, but its median paired increase after scrolling was 33.2, 59.9, and 70.0 KiB respectively. Backing storage increased by approximately 3.0 KiB at each size. These counters are recorded separately in the raw results; they are not a complete accounting of browser memory.

Process RAM was also measured using `ps` RSS summed over this fresh browser's renderer processes:

| Rows | Before: renderer RSS after scrolling | After: renderer RSS after scrolling |
| ---: | ---: | ---: |
| 200 | 456.8 MiB | 451.1 MiB |
| 1,000 | 464.1 MiB | 454.1 MiB |
| 5,000 | 480.3 MiB | 466.0 MiB |

Every post-scroll sample had five renderer processes. One pre-installation sample had four; raw results preserve this rather than excluding it. RSS includes allocator reservations, shared pages, and other renderer overhead, and is noisier than the heap counters. The lower observed RSS does **not** establish a guaranteed whole-app RAM saving. It does show that the extra retained JavaScript heap did not produce an observed increase in post-scroll renderer RSS in these trials.

Both versions retained identical DOM-node counts at each fixture size and 12 mounted rows after scrolling. After transcript removal, both returned to 11 DOM nodes, nine event listeners, and zero mounted rows. The remaining paired JavaScript heap differences were 10.7, 8.3, and 6.5 KiB, rather than increasing with transcript size. Native heap differences remained approximately 33–70 KiB after removal; these measurements do not identify their allocation sources or prove absence of all leaks.

[Raw snapshots and summaries](transcript-scroll-memory-results.json) include source SHA-256 hashes, Chrome versions, process counts, and every trial. Reproduce with Node 22+ and Chrome installed (`CHROME_PATH` optionally selects the executable):

```sh
git show e16aa84df:web/assets/chat-virtual-list.js > /tmp/chat-virtual-list-memory-baseline.js
node scripts/benchmark-transcript-memory.mjs \
  /tmp/chat-virtual-list-memory-baseline.js \
  /tmp/transcript-scroll-memory-results.json 10
```
