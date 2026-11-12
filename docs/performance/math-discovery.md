# Math discovery on unrelated updates

Issue: [#1630](https://github.com/openvibely/openvibely/issues/1630).

The shared math renderer scans the document once at startup. Its mutation observer then inspects added elements and their descendants, skipping disconnected elements and text-only descendants. Equations discovered while KaTeX loads are queued; successful loading renders that queue without rescanning the document. A later equation discovery retries failed loading, including still-connected equations from the earlier attempt. Removed equations are dropped from the queue. Preview exclusion and chat equation reuse remain in place.

## Measurement method

`TestBrowserPerformance_MathDiscovery` renders the production page shell with a fixed 400-message transcript without equations. Each run performs 500 child-list updates in separate animation frames, replacing the final message content and, every tenth update, an unrelated fragment. Thirty before/after pairs run in the same Chrome session, alternating order.

The test wraps only the math observer. The baseline executes its previous full-document discovery callback; with no equations present, there is no library loading or rendering in either arm. The after arm executes the production incremental callback. Other shared-shell observers remain active.

Instrumentation counts full-document math queries and times math selectors on both documents and elements. It separately records the entire math observer callback, including record iteration and instrumentation. Update duration starts immediately before constructing and inserting the update and ends in a queued microtask after mutation observers. The p95 is the nearest-rank percentile of those 500 durations, excluding subsequent paint. Timer precision affects these small durations.

## Results

Captured on macOS arm64 using headless Chrome 157.0.8103.38. [All 30 before/after runs](math-discovery-results.json) are retained with the summary.

| Metric | Before | After |
| --- | ---: | ---: |
| Full-document math queries across all runs | 15,000 | 0 |
| Median cumulative math-selector time per 500 updates | 3.60 ms | 0 ms |
| Median cumulative math observer callback time | 4.20 ms | 1.15 ms |
| Median per-run p95 update duration | 0.10 ms | 0.10 ms |

Math-selector time fell by 100%, exceeding the 90% query-time reduction target, with no measured p95 regression. Total observer time fell by approximately 73%; inspecting added nodes still has a cost even when no selector is needed. These figures describe this fixed workload, not all possible inserted subtrees.

## Reproduce

```sh
OPENVIBELY_MATH_PROFILE=1 go test ./web/templates/pages \
  -run '^TestBrowserPerformance_MathDiscovery$' -count=1 -v -timeout 11m
```

The profile takes roughly nine minutes. Functional coverage uses real KaTeX and includes initial inline/display math, dynamic root and descendant insertion, script-load failure and retry, preview exclusion, streaming, and virtualized equation retention:

```sh
go test ./web/templates/pages \
  -run 'TestBrowserFunctional_(MathDiscovery|StreamingMath|ThreadRefreshPreview)' \
  -count=1 -timeout 120s
```
