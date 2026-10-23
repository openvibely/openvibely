# Long PDF preview performance

`TestBrowserPerformance_LongPDFWindowMeasurements` serves generated 100-, 1,000-, and 2,000-page PDFs, opens each in headless Chrome, scrolls to the end, and records a Chrome Performance trace. The fixture uses 400 × 600 point pages and checks page surfaces and canvas rendering near the final pages. The 2,000-page case also scrolls backward, jumps by page input, zooms, resizes, uses the next-page control, and closes while rendering. A separate 200-page mixed-size PDF checks geometry for unvisited pages, direct navigation alignment, and forward/backward scroll tracking.

The viewer reads page dimensions only through the current page window and its overscan. Scrolling or direct navigation requests the newly needed prefix in asynchronous batches of 16; each batch applies its height corrections with one layout synchronization and adjusts the scroll anchor. This keeps initial page setup bounded while preserving exact geometry for reached or directly selected pages, without canvases for unobserved pages.

## Measurements

Captured on the same Apple Silicon Mac with Headless Chrome 156.0.0.0 at 1280 × 900. The test captures `devtools.timeline` and `blink.user_timing`; p95 is calculated from the last 80 `EventDispatch` scroll durations in each trace. Chrome trace durations are microseconds and are reported below in milliseconds.

| PDF pages | Page surfaces before (open / end) | Page surfaces after (open / end) | Canvas nodes near end | Scroll p95 before | Scroll p95 after | p95 reduction |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100 | 100 / 100 | 11 / 11 | 1 | 0.041 ms | 0.011 ms | 73.2% |
| 1,000 | 1,000 / 1,000 | 11 / 11 | 1 | 0.299 ms | 0.006 ms | 98.0% |
| 2,000 | 2,000 / 2,000 | 11 / 11 | 1 | 0.867 ms | 0.006 ms | 99.3% |

The after surface count stayed at 11 at all three document lengths, and the trace p95 did not increase with page count. In this viewport, only the one canvas in or near the visible window was painted.

The before trace used the embedded PDF viewer bundle from parent commit `6192b86cb42eb8365b375639e7f9841491acf7b0`. The compressed trace files contain Chrome `traceEvents` without the machine metadata block:

- [Before change trace](traces/pdf-preview-before.trace.json.gz)
- [After change trace](traces/pdf-preview-after.trace.json.gz)

## Reproducing

Run the after measurement and keep the 2,000-page trace at the requested path:

```sh
OPENVIBELY_PDF_TRACE_PATH=/tmp/pdf-preview-after.trace.json \
  go test ./web/templates/pages -run '^TestBrowserPerformance_LongPDFWindowMeasurements$' -count=1 -v
```

The test also captures the 100- and 1,000-page traces in its temporary test directory. To repeat the before measurement from the parent bundle, restore the old embedded viewer just for the test run, then rebuild assets from the current source:

```sh
git show 6192b86cb42eb8365b375639e7f9841491acf7b0:web/static/dist/file-pdf.js > web/static/dist/file-pdf.js
OPENVIBELY_PDF_CAPTURE_BASELINE=1 \
OPENVIBELY_PDF_TRACE_PATH=/tmp/pdf-preview-before.trace.json \
  go test ./web/templates/pages -run '^TestBrowserPerformance_LongPDFWindowMeasurements$' -count=1 -v
scripts/build-assets.sh
```

To import a committed trace into Chrome DevTools, decompress it first:

```sh
gzip -dc docs/performance/traces/pdf-preview-after.trace.json.gz > /tmp/pdf-preview-after.trace.json
```
