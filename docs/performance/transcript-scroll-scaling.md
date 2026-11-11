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
