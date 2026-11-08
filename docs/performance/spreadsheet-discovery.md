# Spreadsheet discovery on unrelated updates

Issue: [#1593](https://github.com/openvibely/openvibely/issues/1593).

Spreadsheet previews now use an `ov-spreadsheet` element in the shared preview template. Its connection callback loads the versioned spreadsheet script once and registers that element with the renderer. Initial markup, gallery opens, and later fragment insertion all use this path. No spreadsheet discovery observer or document query remains. Pages without spreadsheet previews do not request `file-sheets.js`, even when the shared file viewer is present.

Worker parsing and sheet rendering retain their existing bounds. Script-load failures offer a retry; worker and data-fetch failures retain the existing retry. A disconnected preview that finishes parsing clears its loading marker so it can initialize when reinserted.

## Measurement method

`TestBrowserPerformance_SheetDiscovery` uses the production page shell and a fixed 400-message Chat-style transcript without previews. Each run performs 500 child-list updates in separate real animation frames: streaming replacements in the final message, with every tenth update replacing a task fragment. Thirty before/after pairs run in one Chrome session, alternating pair order.

The baseline reinstalls the previous spreadsheet observer and exact full-document selector, with callback/query timing instrumentation. Because the transcript has no spreadsheets, the old render callback would never run. The after arm uses the new shared shell without this observer. This isolates discovery overhead; it does not measure workbook parsing or the old script's network/setup cost.

Callback count and cumulative discovery time are recorded per run. Update duration is measured from immediately before the DOM mutation through a queued microtask after mutation observers, including spreadsheet discovery. Each run records the nearest-rank p95 of its 500 durations. This measures update work, not frame intervals or subsequent paint. Other shared-shell observers remain active in both arms.

## Results

Captured on the same macOS arm64 host with headless Chrome 157.0.8103.38 at 1280 × 900. [Per-run measurements](spreadsheet-discovery-results.json) include callback count, cumulative discovery time, and p95 update duration for all 30 runs in each arm.

| Metric | Before | After |
| --- | ---: | ---: |
| Discovery callbacks / full-document spreadsheet scans, all runs | 15,000 | 0 |
| Median cumulative discovery time per 500 updates | 2.00 ms | 0 ms |
| Median per-run p95 update duration | 0.10 ms | 0.10 ms |

Median cumulative spreadsheet-discovery time fell by 100%, with no p95 update-duration regression. Durations are subject to Chrome's timer precision; the zero after-discovery cost follows from the absence of both the spreadsheet script and any discovery observer on this page.

## Reproduce

```sh
OPENVIBELY_SHEET_PROFILE=1 go test ./web/templates/pages \
  -run '^TestBrowserPerformance_SheetDiscovery$' -count=1 -v -timeout 12m
```

The benchmark takes roughly nine minutes and logs all 30 runs. Functional coverage is separate:

```sh
go test ./web/templates/pages \
  -run 'TestBrowserFunctional_(SheetRegistration|FileViewers)$|TestSpreadsheetWorkerBoundsSparsePreview' \
  -count=1 -v -timeout 3m
```

These tests cover lazy loading, direct gallery opening, initial server markup, dynamic insertion, script/worker/fetch retries, multiple sheet tabs, bounded rows and columns, absence of document-wide spreadsheet queries, and existing non-spreadsheet previews.
