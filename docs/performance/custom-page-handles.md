# Custom-page handle creation (#1639)

## Method

Baseline: `26a38d16c61c83dcb7aba7133e533ad3d676a718` with the new
`BenchmarkIssueHandle` harness. Updated: the same harness with the map sweep
removed and lookup rejecting the exact expiry boundary.

Environment: Apple M5 Pro, darwin/arm64, Go 1.27.4, GOMAXPROCS=4.
Run each version with:

```sh
go test ./internal/pageplugins -run '^$' -bench '^BenchmarkIssueHandle$' -benchtime=100000x -count=5 -cpu=4
```

For the recorded comparison, each version was compiled with `go test -c` and
its binary run sequentially using the equivalent `-test.*` flags, without
concurrent builds or test suites. Preliminary shorter runs overlapped build
activity and were excluded from this comparison.

Each case maintains 100, 1,000, or 5,000 unrelated live handles with real timers.
Each sample measures one IssueHandle call, including lock acquisition. The
measured handle is retired outside that sample to maintain a fixed population.
Parallel cases use four workers; their issuance and retirement contend on the
same manager mutex. Setup, final cleanup, and percentile sorting are untimed.
The standard ns/op includes retirement and sampling overhead; the custom p50-ns
and p95-ns metrics measure creation latency only. The table reports the median
of five runs' individual-call p50 and p95 values, in nanoseconds.

| Live handles | Creation | Before p50 | Before p95 | After p50 | After p95 |
| ---: | --- | ---: | ---: | ---: | ---: |
| 100 | Serial | 875 | 1,083 | 375 | 500 |
| 1,000 | Serial | 6,208 | 7,000 | 375 | 584 |
| 5,000 | Serial | 26,458 | 31,833 | 375 | 584 |
| 100 | Concurrent | 1,042 | 18,708 | 750 | 6,750 |
| 1,000 | Concurrent | 6,459 | 10,541 | 708 | 8,000 |
| 5,000 | Concurrent | 30,750 | 40,417 | 750 | 7,541 |

At 5,000 live handles, p95 improves **98.2% serial** and **81.3% concurrent**
(target: at least 80%). Updated p95 growth from 1,000 to 5,000 is **0% serial**
and **-5.7% concurrent** (target: at most 20%). Scheduler, GC, and lock contention
make concurrent percentiles noisier than serial samples; these are local
measurements, not portable timing assertions in unit tests.

## Validation

- `go build ./...` passed (macOS deployment-target linker warnings).
- `go test -race ./internal/pageplugins` passed as part of the combined package run.
- `go test -race ./internal/pageplugins -run '^TestHandle' -count=10 -timeout=60s` passed.
- `go test -race ./internal/handler -run '^TestPlugin' -timeout=120s` passed.
- The broader handler race suite was stopped after several minutes and replaced
  by the focused plugin handler run; no full handler-suite pass is claimed.
- `git diff --check` passed.

## Separate read-only audit

After implementation and validation, reviewed IssueHandle, LookupHandle,
RetireHandle, retireHandleLocked, ReleaseProject, releaseInstanceRuntime, the
benchmark harness, and the regression tests. No blocking findings.

- Creation no longer iterates over handles under the manager mutex. Lookup
  accesses only its token and retires only that token on expiry. Token generation
  remains outside the lock; insertion and timer registration remain serialized.
- Lookup rejects at and after ExpiresAt even when the timer has not run. The
  stopped-timer test isolates this guarantee from scheduling.
- Per-handle AfterFunc callbacks still reclaim handles without further issuance
  or lookup. A virtual-time test holds the manager lock through expiry, releases
  it, waits for the callback, and checks map removal before calling LookupHandle.
  Delayed callbacks reclaim entries once they can run; no cleanup scheme can
  progress while execution or lock acquisition is indefinitely prevented.
- All retirement paths delete under the same mutex before closing Done. Repeated
  retirement and already-running timer callbacks therefore cannot double-close.
  The concurrent test races timers, expired lookups, explicit retirement, project
  release, and creation/lookup of fresh handles; race-detector runs passed.
- Project release retains its existing deliberate project/instance scans and
  leaves other projects' handles usable. These lifecycle scans are outside the
  per-page creation/lookup paths addressed by this change.

## Raw baseline output

```text
goos: darwin
goarch: arm64
pkg: github.com/openvibely/openvibely/internal/pageplugins
cpu: Apple M5 Pro
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	      1015 ns/op	       875.0 p50-ns	      1041 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	      1026 ns/op	       875.0 p50-ns	      1083 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	       995.7 ns/op	       834.0 p50-ns	      1000 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	      1020 ns/op	       875.0 p50-ns	      1083 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	      1084 ns/op	       875.0 p50-ns	      1333 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	      1498 ns/op	      1083 p50-ns	     16917 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	      1458 ns/op	      1083 p50-ns	     20250 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	      1322 ns/op	       958.0 p50-ns	     18541 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	      1348 ns/op	       958.0 p50-ns	     19667 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	      1439 ns/op	      1042 p50-ns	     18708 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	      6661 ns/op	      6291 p50-ns	      7791 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	      6374 ns/op	      6125 p50-ns	      6667 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	      6629 ns/op	      6334 p50-ns	      7000 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	      6475 ns/op	      6167 p50-ns	      7000 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	      6642 ns/op	      6208 p50-ns	      7792 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	      7552 ns/op	      6625 p50-ns	     12333 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	      7980 ns/op	      6958 p50-ns	     16708 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	      7253 ns/op	      6458 p50-ns	     10541 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	      7255 ns/op	      6458 p50-ns	     10292 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	      7274 ns/op	      6459 p50-ns	     10500 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	     27303 ns/op	     26458 p50-ns	     30500 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	     28054 ns/op	     26916 p50-ns	     31917 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	     27795 ns/op	     26500 p50-ns	     33875 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	     27507 ns/op	     26417 p50-ns	     31833 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	     26458 ns/op	     25875 p50-ns	     28792 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	     30597 ns/op	     30750 p50-ns	     41458 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	     30536 ns/op	     30709 p50-ns	     40375 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	     30709 ns/op	     30792 p50-ns	     42875 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	     30619 ns/op	     30791 p50-ns	     40417 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	     30132 ns/op	     30708 p50-ns	     39375 p95-ns
PASS
```

## Raw updated output

```text
goos: darwin
goarch: arm64
pkg: github.com/openvibely/openvibely/internal/pageplugins
cpu: Apple M5 Pro
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	       497.3 ns/op	       375.0 p50-ns	       500.0 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	       481.9 ns/op	       375.0 p50-ns	       500.0 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	       491.1 ns/op	       375.0 p50-ns	       500.0 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	       488.4 ns/op	       375.0 p50-ns	       500.0 p95-ns
BenchmarkIssueHandle/live=100/parallel=false-4         	  100000	       486.7 ns/op	       375.0 p50-ns	       500.0 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	       739.0 ns/op	       750.0 p50-ns	      6917 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	       729.0 ns/op	       750.0 p50-ns	      6584 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	       724.5 ns/op	       750.0 p50-ns	      6750 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	       727.4 ns/op	       750.0 p50-ns	      6625 p95-ns
BenchmarkIssueHandle/live=100/parallel=true-4          	  100000	       857.3 ns/op	       709.0 p50-ns	      8750 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	       536.8 ns/op	       375.0 p50-ns	       584.0 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	       525.9 ns/op	       375.0 p50-ns	       584.0 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	       499.0 ns/op	       375.0 p50-ns	       542.0 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	       538.7 ns/op	       375.0 p50-ns	       625.0 p95-ns
BenchmarkIssueHandle/live=1000/parallel=false-4        	  100000	       505.8 ns/op	       375.0 p50-ns	       542.0 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	       834.8 ns/op	       709.0 p50-ns	      8000 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	       809.8 ns/op	       708.0 p50-ns	      8417 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	       835.9 ns/op	       708.0 p50-ns	      8542 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	       754.5 ns/op	       709.0 p50-ns	      6875 p95-ns
BenchmarkIssueHandle/live=1000/parallel=true-4         	  100000	       726.5 ns/op	       708.0 p50-ns	      6750 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	       501.5 ns/op	       375.0 p50-ns	       583.0 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	       547.0 ns/op	       375.0 p50-ns	       667.0 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	       516.2 ns/op	       375.0 p50-ns	       584.0 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	       535.6 ns/op	       375.0 p50-ns	       625.0 p95-ns
BenchmarkIssueHandle/live=5000/parallel=false-4        	  100000	       488.4 ns/op	       375.0 p50-ns	       542.0 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	       816.8 ns/op	       709.0 p50-ns	      7541 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	       772.3 ns/op	       750.0 p50-ns	      7416 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	       792.4 ns/op	       750.0 p50-ns	      7750 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	       826.7 ns/op	       709.0 p50-ns	      8334 p95-ns
BenchmarkIssueHandle/live=5000/parallel=true-4         	  100000	       755.7 ns/op	       750.0 p50-ns	      7042 p95-ns
PASS
```
