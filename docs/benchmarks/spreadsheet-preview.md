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

Measured on Google Chrome 156.0.8078.47, macOS arm64, with three fresh pages per revision:

| Worker | Median first-sheet ready | Median peak worker JS heap |
| --- | ---: | ---: |
| Parent revision | 3,928.1 ms | 373.3 MiB |
| Selective worker | 847.4 ms | 26.7 MiB |

That is 78.4% lower median ready time and 92.8% lower median peak heap. Both revisions returned 5,000 preview rows, 25,000 total rows, 20 total columns, and `truncated: true`; the displayed first-sheet row hash matched (`188cadcb`).
