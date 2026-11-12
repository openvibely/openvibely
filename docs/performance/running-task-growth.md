# Running-task output growth

This change addresses live rendering and hydration separately from #1622 / PR #1633 ordinary-scroll work. Measurements use production template JavaScript, bundled Marked, and shipped virtualization code in headless Chrome. They are renderer diagnostics, not a reproduction of the user’s hours-long session.

## Method

Baseline: `89f868fc7bb2a90b7408938a24824a458e46c6f4`. Browser: Google Chrome 157.0.8103.38 on macOS. Run `go test ./web/templates/components -run '^TestBrowserFunctional_RunningGrowthMeasurements$' -count=1 -v -timeout 120s`.

Each scenario grows one running execution through 200, 1,000 and 5,000 entries, then appends one identical-format entry at each size. The unfinished-result case appends 12 characters to a 70 KB unfinished result after a warm update; it closes that result before growing to the next size. Initial/growth render time, one-update elapsed time, normalized characters, linked segments, mounted tools and total DOM elements are recorded. The baseline ran this same fixture with only the new work-bound assertions disabled. The fixture passes `yieldBetweenBatches=false` to isolate preparation and DOM work; it still awaits the production renderer and virtual rows. Timing is one diagnostic run, includes layout/GC/scheduling, and is not a CI threshold. Deterministic counters and semantic assertions are the regression gates.

## Update elapsed time (milliseconds)

| Mode | Scenario | 200 before → after | 1,000 before → after | 5,000 before → after |
|---|---|---:|---:|---:|
| compact | tools | 2.1 → 0.7 | 14.3 → 0.5 | 261.8 → 1.3 |
| compact | unmatched | 1.2 → 0.3 | 11.5 → 0.5 | 216.1 → 1.3 |
| compact | thinking | 0.6 → 0.2 | 7.9 → 0.2 | 173.3 → 0.7 |
| compact | legacy | 0.4 → 0.2 | 2.9 → 0.3 | 49.5 → 1.0 |
| compact | unfinished | 31.4 → 5.0 | 154.2 → 5.1 | 922.9 → 6.0 |
| full | tools | 0.2 → 2.0 | 0.3 → 5.1 | 0.6 → 18.3 |
| full | unmatched | 6.2 → 1.2 | 36.6 → 4.7 | 363.4 → 19.3 |
| full | thinking | 1.3 → 0.3 | 12.0 → 0.4 | 214.2 → 1.6 |
| full | legacy | 0.9 → 0.3 | 6.9 → 0.6 | 87.0 → 2.7 |
| full | unfinished | 21.9 → 1.7 | 22.1 → 13.4 | 22.8 → 32.1 |

Full mode’s ordinary completed-call updates were already inexpensive because they split into sealed sections; its unfinished-result fixture also had a small isolated tail. The unified live row ledger adds per-update row reconciliation in that case; the table reports that regression rather than implying every case is faster. It bounds mounted tools and fixes the previously unbounded unmatched/thinking cases.

## Work at 5,000 entries

| Mode | Scenario | Normalized characters before → after | Linked segments before → after | Mounted tools before → after | DOM elements before → after |
|---|---|---:|---:|---:|---:|
| compact | tools | 347,850 → 70 | 15,003 → 3 | 0 → 0 | 9 → 8 |
| compact | unmatched | 347,884 → 70 | 15,005 → 3 | 0 → 0 | 9 → 8 |
| compact | thinking | 173,925 → 35 | 5,001 → 1 | 0 → 0 | 9 → 8 |
| compact | legacy | 163,923 → 34 | 10,003 → 3 | 0 → 0 | 9 → 8 |
| compact | unfinished | 558,003 → 0 | 15,009 → 2 | 0 → 0 | 9 → 8 |
| full | tools | 140 → 70 | 6 → 3 | 8 → 4 | 5,268 → 5,137 |
| full | unmatched | 347,884 → 70 | 15,005 → 3 | 5,002 → 4 | 135,041 → 5,138 |
| full | thinking | 173,925 → 35 | 5,001 → 1 | 0 → 0 | 5,015 → 14 |
| full | legacy | 163,923 → 34 | 10,003 → 3 | 0 → 0 | 5,015 → 14 |
| full | unfinished | 70,069 → 0 | 3 → 2 | 6 → 3 | 5,210 → 5,105 |

Full measurements include one lightweight placeholder per offscreen row; these are not mounted tool cards. Compact activity remains one disclosure. Raw measurements, including initial/growth render times, are in [running-task-growth-results.json](running-task-growth-results.json).

## Implementation and limits

- Parser checkpoints retain completed controls, normalized/raw offset mapping, pending FIFO tool queues and projected rows. Group boundaries do not constrain parser checkpoints. Source replacement discards checkpoints; terminal rendering reconciles the complete authoritative source. Running navigation snapshots copy the mutable model ledger so later results cannot alter an earlier source revision.
- Incomplete controls, Markdown and in-progress compaction remain provisional. A cached opaque unfinished result scans only new input plus a possible partial closing marker; the complete output string is still retained and may be copied or inspected for display.
- Compact grouping saves aggregate summaries at source-row boundaries. Completed rows retain model identities; virtual installation updates changed rows in place. Repeated thinking stays one disclosure, with Markdown deferred until opening or explicit materialization. Large opened thinking uses the Markdown worker.
- Running Full hydration uses the same visible-row projection as Compact. The controller’s hydration/pending-update lifecycle is unchanged. The existing enhanced SVG exclusion and preview-preserving installation path remain.
- This is not constant-time overall. Cumulative append validation, raw-content/revision handling, string retention, ledger/row array copying and reconciliation still scale with history. A changing thinking group can traverse its segment ledger; unmatched queue snapshots scale with outstanding calls. Initial parsing and final reconciliation cover the loaded source. One uninterrupted provisional Markdown block still requires reparsing. Expanded content, Find/selection/print intentionally materialize more work. Ordinary scroll windowing and overscan are unchanged.

## Validation

Real-browser regressions cover both display modes, growth work counters, fresh-render equivalence, byte-sized partial controls, fences, legacy thinking/parameter aliases, FIFO matching with aliases, late results, compaction, source replacement, large unfinished output and split closing markers. Running hydration coverage checks incoming updates, initial bottom visibility, reopening from row data, bounded mounted tools, Find and terminal reconciliation. Existing renderer/navigation/selection/focus/media/scroll suites supply the surrounding lifecycle checks.

Validation commands (full renderer/page suites were exercised; corrected cases were rerun with the focused browser suites below):

```sh
go test ./web/templates/components ./web/templates/pages ./web/templates/layout -skip 'Browser|Chrome' -count=1
go test ./web/templates/components -run 'TestChatRenderingPathsUseBaseSafeMarkdownRenderer|TestRenderStreamingContent|TestBrowserFunctional_(Running|IncrementalChatRendering|Compact)' -count=1
go test ./web/templates/components -run '^TestBrowserFunctional_(RunningHydrationAndMaterialization|ChatSelection|LazyTranscript|VirtualScrollLargeThinkingGroup)' -count=1
go test ./web/templates/pages -run '^TestBrowserFunctional_(StreamingHTMLPreview|StreamingMath|StreamingSVGPreview|MermaidRendersAfterLiveChatCompletion)' -count=1
go test ./internal/handler -run '^TestTask(LazyCachedNavigationInChrome|CachedNavigationRefreshInChrome)$' -count=1
go test ./web/static -count=1
make templ
scripts/build-assets.sh
go build -o /tmp/openvibely-running-task-validation ./cmd/server
```

Generated template and bundled-asset checksums were unchanged after regeneration. No merge, release, or deployment was performed.
