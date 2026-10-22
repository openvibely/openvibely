# Tool schema validation cache benchmark evidence

Captured on 2026-10-22 at 10:21 UTC.

- Host: Apple M5 Pro, macOS `darwin/arm64`
- Go: `go1.27.3 darwin/arm64`
- Baseline cache source: `e45e37562` (serialized cache, with its cache test file selected through a Go source overlay)
- Updated cache source: `3f7f1f312532073eea439af46254ddde47d7938e`
- Benchmark harness: `pkg/toolschema/validation_cache_bench_test.go` from the updated source, identical for both runs
- Command for each run: `go test ./pkg/toolschema -run '^$' -bench '^BenchmarkValidatorCache' -benchtime=10x -benchmem`; the baseline run additionally used `-overlay=/tmp/openvibely_validation_cache_baseline_overlay.json` to substitute `validation_cache.go` and `validation_cache_test.go` from the baseline source revision.

The two runs were made sequentially on the same host. `ns/op` is the elapsed time per benchmark batch; the first benchmark batch contains eight distinct cold misses and the warm-only batch contains eight concurrent lookups. `warm-hit-ns/op` is the mean latency of each warm lookup measured alongside a cold compilation.

## Baseline output

```text
goos: darwin
goarch: arm64
pkg: github.com/openvibely/openvibely/pkg/toolschema
cpu: Apple M5 Pro
BenchmarkValidatorCacheConcurrentColdDistinct-18        10  80103162 ns/op       99.87 validations/s  48309211 B/op  718479 allocs/op
BenchmarkValidatorCacheConcurrentWarmHits-18            10     12462 ns/op   641926 validations/s      496 B/op       9 allocs/op
BenchmarkValidatorCacheWarmHitsAlongsideColdMiss-18     10  14021538 ns/op 11515361 warm-hit-ns/op 6039264 B/op  89826 allocs/op
PASS
ok github.com/openvibely/openvibely/pkg/toolschema 4.236s
```

## Updated output

```text
goos: darwin
goarch: arm64
pkg: github.com/openvibely/openvibely/pkg/toolschema
cpu: Apple M5 Pro
BenchmarkValidatorCacheConcurrentColdDistinct-18        10  36855367 ns/op      217.1 validations/s  48330215 B/op  718567 allocs/op
BenchmarkValidatorCacheConcurrentWarmHits-18            10      3175 ns/op  2519685 validations/s      544 B/op       9 allocs/op
BenchmarkValidatorCacheWarmHitsAlongsideColdMiss-18     10  10040233 ns/op      542.2 warm-hit-ns/op 6037300 B/op  89820 allocs/op
PASS
ok github.com/openvibely/openvibely/pkg/toolschema 1.054s
```

## Comparison

- Eight distinct cold schemas: batch time decreased from 80,103,162 ns to 36,855,367 ns, a 54.0% reduction (above the 30% target).
- Concurrent warm lookups: batch time decreased from 12,462 ns to 3,175 ns; no warm-cache regression was observed.
- Warm lookups during a cold compile: mean warm-hit latency decreased from 11,515,361 ns to 542.2 ns.
- Allocations are reported for every benchmark with `-benchmem`; the output above records both `B/op` and `allocs/op` for each run.
