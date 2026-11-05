# Public argument validation benchmark evidence (#1576)

Measured 2026-11-05 on Apple M5 Pro, darwin/arm64, Go 1.27.3.
Baseline: `5b9e7b0d743f5c8fa83c57470b6372dd7c72c50c` with the new
`validation_arguments_bench_test.go` harness. The baseline package was copied to
a temporary directory with the same go.mod/go.sum; the updated package ran in
the working tree on the same host. Both used:

```sh
GOMAXPROCS=2 go test ./pkg/toolschema -run '^$' -bench '^BenchmarkValidateArguments$' -benchmem -count=7 -benchtime=300ms
```

Each case warms the cache outside the timer and then calls the public API with
`{"field-0":"value"}` and an unchanged closed object schema with 1, 40, or 500
optional string properties. Raw schemas use the external path; typed schemas
include serialization on every iteration. No network access is needed.

## Median results

Each column is the median of seven measurements. Timing is sensitive to shared
host load; an initial default-concurrency pilot was noisy (including a slower
typed one-property result). Two subsequent fixed-concurrency before/after runs
met the thresholds. The final run is recorded here in full.

| Schema/properties | Before ns/op | After ns/op | Before B/op | After B/op | Before allocs/op | After allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| typed/1 | 3,474 | 1,839 | 3,194 | 1,993 | 60 | 39 |
| raw/1 | 2,182 | 1,052 | 2,993 | 1,792 | 52 | 31 |
| typed/40 | 30,009 | 11,193 | 24,779 | 3,178 | 380 | 39 |
| raw/40 | 18,320 | 1,901 | 23,362 | 1,792 | 372 | 31 |
| typed/500 | 360,613 | 132,474 | 314,853 | 18,506 | 4,082 | 39 |
| raw/500 | 240,250 | 12,351 | 291,927 | 1,793 | 4,071 | 31 |

For 500 properties, median time fell 63.3% (typed) and 94.9% (raw), and
allocated bytes fell 94.1% and 99.4%. One-property median time improved 47.1%
and 51.8%, respectively. Both acceptance thresholds are met in this run.

## Retention and behavior

Cache keys still hash schema JSON contents. Typed serialization remains necessary
to detect caller mutations. Each entry now owns a compact normalization tree
containing property names, optional-null flags, child nodes and array items;
the generic decoded map is discarded. Strict-conversion eligibility for external
null normalization is computed once on demand, without retaining raw JSON.

The existing 128-entry FIFO and 120,000-byte source limit remain. Entries whose
normalization payload exceeds 120,000 bytes also bypass retention. Accounting
includes node structures, the entire allocated property-array capacity, and
property-name bytes. Retained normalization payload is therefore at most
15,360,000 bytes, plus bounded entry and allocator overhead; this is not a bound
on the existing compiled validators or transient in-flight work. Both validator
and metadata are evicted together. Oversized schemas still validate without
being retained.

Regression tests compare the previous path's normalized output and error
categories (individual error text where property ordering is unambiguous),
exercise typed/raw content changes, missing/null fields, nested objects/arrays,
unsupported external fallback, references, invalid/oversized schemas, metadata
reuse/limits, and concurrent warm normalization including lazy eligibility.

## Raw measurements

### Before

```text
goos: darwin
goarch: arm64
pkg: github.com/openvibely/openvibely/pkg/toolschema
cpu: Apple M5 Pro
BenchmarkValidateArguments/typed/1-2         	  123074	      3325 ns/op	    3194 B/op	      60 allocs/op
BenchmarkValidateArguments/typed/1-2         	  112977	      3484 ns/op	    3194 B/op	      60 allocs/op
BenchmarkValidateArguments/typed/1-2         	  114759	      3170 ns/op	    3194 B/op	      60 allocs/op
BenchmarkValidateArguments/typed/1-2         	   84626	      3624 ns/op	    3194 B/op	      60 allocs/op
BenchmarkValidateArguments/typed/1-2         	  112102	      3474 ns/op	    3194 B/op	      60 allocs/op
BenchmarkValidateArguments/typed/1-2         	  116467	      3616 ns/op	    3194 B/op	      60 allocs/op
BenchmarkValidateArguments/typed/1-2         	  109530	      3428 ns/op	    3194 B/op	      60 allocs/op
BenchmarkValidateArguments/raw/1-2           	  156830	      2745 ns/op	    2993 B/op	      52 allocs/op
BenchmarkValidateArguments/raw/1-2           	  146864	      2182 ns/op	    2993 B/op	      52 allocs/op
BenchmarkValidateArguments/raw/1-2           	  184942	      2097 ns/op	    2993 B/op	      52 allocs/op
BenchmarkValidateArguments/raw/1-2           	  167198	      2551 ns/op	    2993 B/op	      52 allocs/op
BenchmarkValidateArguments/raw/1-2           	  145232	      2270 ns/op	    2993 B/op	      52 allocs/op
BenchmarkValidateArguments/raw/1-2           	  141314	      2176 ns/op	    2993 B/op	      52 allocs/op
BenchmarkValidateArguments/raw/1-2           	  169460	      2115 ns/op	    2993 B/op	      52 allocs/op
BenchmarkValidateArguments/typed/40-2        	   10000	     31339 ns/op	   24776 B/op	     380 allocs/op
BenchmarkValidateArguments/typed/40-2        	   10000	     33246 ns/op	   24779 B/op	     380 allocs/op
BenchmarkValidateArguments/typed/40-2        	   12868	     29586 ns/op	   24786 B/op	     380 allocs/op
BenchmarkValidateArguments/typed/40-2        	   13614	     27111 ns/op	   24779 B/op	     380 allocs/op
BenchmarkValidateArguments/typed/40-2        	   12250	     28760 ns/op	   24785 B/op	     380 allocs/op
BenchmarkValidateArguments/typed/40-2        	   12772	     32382 ns/op	   24778 B/op	     380 allocs/op
BenchmarkValidateArguments/typed/40-2        	   10000	     30009 ns/op	   24783 B/op	     380 allocs/op
BenchmarkValidateArguments/raw/40-2          	   20971	     16322 ns/op	   23360 B/op	     372 allocs/op
BenchmarkValidateArguments/raw/40-2          	   20554	     20309 ns/op	   23362 B/op	     372 allocs/op
BenchmarkValidateArguments/raw/40-2          	   21540	     18320 ns/op	   23362 B/op	     372 allocs/op
BenchmarkValidateArguments/raw/40-2          	   18463	     19968 ns/op	   23362 B/op	     372 allocs/op
BenchmarkValidateArguments/raw/40-2          	   20632	     16595 ns/op	   23362 B/op	     372 allocs/op
BenchmarkValidateArguments/raw/40-2          	   17452	     18722 ns/op	   23363 B/op	     372 allocs/op
BenchmarkValidateArguments/raw/40-2          	   20802	     18151 ns/op	   23363 B/op	     372 allocs/op
BenchmarkValidateArguments/typed/500-2       	    1020	    357645 ns/op	  314853 B/op	    4082 allocs/op
BenchmarkValidateArguments/typed/500-2       	    1021	    352669 ns/op	  314633 B/op	    4082 allocs/op
BenchmarkValidateArguments/typed/500-2       	    1062	    340169 ns/op	  314485 B/op	    4082 allocs/op
BenchmarkValidateArguments/typed/500-2       	    1068	    370950 ns/op	  315002 B/op	    4082 allocs/op
BenchmarkValidateArguments/typed/500-2       	    1009	    360613 ns/op	  314712 B/op	    4082 allocs/op
BenchmarkValidateArguments/typed/500-2       	    1044	    368433 ns/op	  315024 B/op	    4082 allocs/op
BenchmarkValidateArguments/typed/500-2       	     892	    380542 ns/op	  316130 B/op	    4082 allocs/op
BenchmarkValidateArguments/raw/500-2         	    1306	    240250 ns/op	  291927 B/op	    4071 allocs/op
BenchmarkValidateArguments/raw/500-2         	    1365	    258382 ns/op	  291906 B/op	    4071 allocs/op
BenchmarkValidateArguments/raw/500-2         	    1634	    212090 ns/op	  291910 B/op	    4071 allocs/op
BenchmarkValidateArguments/raw/500-2         	    1815	    254525 ns/op	  291967 B/op	    4071 allocs/op
BenchmarkValidateArguments/raw/500-2         	    1761	    221433 ns/op	  291936 B/op	    4071 allocs/op
BenchmarkValidateArguments/raw/500-2         	    1574	    273388 ns/op	  291936 B/op	    4071 allocs/op
BenchmarkValidateArguments/raw/500-2         	    1640	    209834 ns/op	  291887 B/op	    4071 allocs/op
PASS
ok  	github.com/openvibely/openvibely/pkg/toolschema	19.761s
```

### After

```text
goos: darwin
goarch: arm64
pkg: github.com/openvibely/openvibely/pkg/toolschema
cpu: Apple M5 Pro
BenchmarkValidateArguments/typed/1-2         	  204686	      2128 ns/op	    1993 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/1-2         	  214474	      1684 ns/op	    1993 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/1-2         	  214039	      1728 ns/op	    1993 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/1-2         	  213561	      1839 ns/op	    1993 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/1-2         	  165367	      2354 ns/op	    1993 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/1-2         	  178885	      1937 ns/op	    1993 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/1-2         	  205058	      1702 ns/op	    1993 B/op	      39 allocs/op
BenchmarkValidateArguments/raw/1-2           	  299084	      1052 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/1-2           	  387951	      1158 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/1-2           	  370998	      1237 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/1-2           	  367106	      1152 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/1-2           	  336584	       925.5 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/1-2           	  389966	       915.1 ns/op	    1793 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/1-2           	  417891	       879.8 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/typed/40-2        	   33716	     12334 ns/op	    3179 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/40-2        	   33372	     10798 ns/op	    3178 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/40-2        	   33674	     11193 ns/op	    3178 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/40-2        	   27560	     11670 ns/op	    3178 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/40-2        	   35342	     11792 ns/op	    3179 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/40-2        	   30484	     10538 ns/op	    3179 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/40-2        	   35355	     10873 ns/op	    3178 B/op	      39 allocs/op
BenchmarkValidateArguments/raw/40-2          	  165843	      1897 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/40-2          	  219277	      1682 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/40-2          	  206278	      2014 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/40-2          	  166105	      2387 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/40-2          	  174379	      2378 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/40-2          	  167398	      1818 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/40-2          	  180102	      1901 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/typed/500-2       	    2662	    138615 ns/op	   18516 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/500-2       	    2757	    131607 ns/op	   18504 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/500-2       	    2677	    145885 ns/op	   18506 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/500-2       	    2822	    132474 ns/op	   18523 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/500-2       	    2758	    130920 ns/op	   18434 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/500-2       	    2786	    131232 ns/op	   18481 B/op	      39 allocs/op
BenchmarkValidateArguments/typed/500-2       	    2497	    145650 ns/op	   18529 B/op	      39 allocs/op
BenchmarkValidateArguments/raw/500-2         	   30588	     13027 ns/op	    1793 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/500-2         	   30763	     12838 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/500-2         	   30655	     11871 ns/op	    1793 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/500-2         	   28803	     12106 ns/op	    1793 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/500-2         	   30396	     11935 ns/op	    1793 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/500-2         	   27127	     13436 ns/op	    1792 B/op	      31 allocs/op
BenchmarkValidateArguments/raw/500-2         	   26278	     12351 ns/op	    1792 B/op	      31 allocs/op
PASS
ok  	github.com/openvibely/openvibely/pkg/toolschema	19.014s
```
