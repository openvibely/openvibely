# Spreadsheet preview benchmark

Run the generated 8 worksheet × 25,000 row × 20 column XLSX benchmark three times in fresh Chrome pages:

```sh
node scripts/benchmark-spreadsheet-preview.mjs --runs 3
```

The benchmark prints each run's worker time to first sheet ready, sampled peak worker JavaScript heap, dimensions, truncation state, and a hash of every displayed first sheet value. It uses Chrome DevTools Protocol heap metrics and disables the page cache for each run. Use the same Chrome binary and machine for both revisions.

To measure the parent revision with the same harness and fixture, save its worker and run the legacy protocol:

```sh
git show <parent-revision>:web/assets/files/file-spreadsheet-worker.js > /tmp/file-spreadsheet-worker.before.js
node scripts/benchmark-spreadsheet-preview.mjs --worker /tmp/file-spreadsheet-worker.before.js --protocol legacy --runs 3
```

Compare the reported medians. The preview signature must match between revisions; the new worker should reduce both median ready time and median peak heap by at least 50%.

## Recorded run

Measured on Google Chrome 156.0.8078.73, Darwin 27.0.0 arm64, with three fresh pages per revision. The baseline worker is from `e0f67d6e5`; both runs use the same harness, generated fixture, and vendored SheetJS library. Baseline and selective measurements were run sequentially.

| Worker | Run | First-sheet ready | Sampled peak worker JS heap |
| --- | ---: | ---: | ---: |
| Baseline | 1 | 3,547.0 ms | 360.2 MiB |
| Baseline | 2 | 3,715.9 ms | 363.2 MiB |
| Baseline | 3 | 3,732.2 ms | 363.0 MiB |
| Selective | 1 | 320.5 ms | 27.0 MiB |
| Selective | 2 | 312.6 ms | 16.3 MiB |
| Selective | 3 | 326.4 ms | 33.5 MiB |

Median ready time fell from 3,715.9 ms to 320.5 ms (91.4% lower); median sampled peak heap fell from 363.0 MiB to 27.0 MiB (92.6% lower). Both revisions returned 5,000 preview rows, 25,000 total rows, 20 total columns, and `truncated: true`; the displayed first-sheet row hash matched (`188cadcb`) in every run. The harness now fails if any run's dimensions, truncation, or value hash differs from this expected preview, so performance gains cannot hide changed output. Heap measurements are CDP samples, not an allocation trace.
