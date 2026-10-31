# Archived benchmark evidence

## Current main versus optimized branch — main 85d0755c1, branch 611d5107f

[Repository report](MAIN_CURRENT_RESULTS.md) · [Download report](/files/8b0a285d41f11e6007c7fc6995c92acc?project_id=b184292efa07a415fc59967a5fc328d1) · [Raw measurements and reusable profiles](/files/b067fb7b0bce03a3c223a06a8c214f47?project_id=b184292efa07a415fc59967a5fc328d1).

Controlled main-versus-branch comparison across all three providers: 72 measured runs (2,376 turns), 12 smoke runs and six separate diagnostics passed. Two attempts overlapping unrelated Go tests were excluded and retried. The report separately compares the previous saved final candidate with this branch; that historical comparison does not isolate the later code changes or machine variation. The flow harness, mock peer sources and compiler version match the previous experiment.

Allocation volume and median process RSS fell in every scenario versus main; compatible single-task text CPU increased slightly. Delivery-gap, elapsed-time and storage tradeoffs are reported. Profiles, six-turn tables, per-run ranges, phase attribution, binary/source metadata, exact main-to-branch patch and SHA-256 file manifest are archived. App databases, fixtures and binaries are omitted. Frozen binaries were hash-verified after measurement; the baseline source checkout was already absent at final cleanup, and the remaining clean candidate checkout was removed.

Archive: 5,783,873 bytes; SHA256 `dd747b6c808cd7bec600153e65e6d90aaf2b8a391e9a115e5b9ee62a21345b7c`.

## Anthropic retained replay fix

Previous task branch `df2b9bf49` versus the same branch with corrected text-only output, native-history display omission and single replay decoding. This is not main versus branch. Twelve timing runs (396 turns), two separate heap diagnostic runs and two smoke scenarios passed.

- [Repository report](ANTHROPIC_RETENTION_RESULTS.md)
- [Download report](/files/47d07a3506044e88735bc465adf33b38?project_id=b184292efa07a415fc59967a5fc328d1)
- [Raw measurements, profiles, regression evidence and source patch](/files/aa9ae81840654748f364a760d667029a?project_id=b184292efa07a415fc59967a5fc328d1) (1,109,185 bytes)

Archive SHA-256: `2c9a7827e2631dbf1e4092d1b5f33638c8e4d6df6402920caa0d146abf06f6f3`.

The archive preserves the failed baseline regression and the initial fixture/timeout attempts as well as final passing tests. Timing and diagnostic results are separate. Existing stored headers are not rewritten. Temporary source worktrees were removed after checking their production sources against the measured patch.

## Phase-aligned memory investigation and estimator follow-up

Main `9e702736a` versus branch `659c90e24` phase discovery is separate from the final optimization comparison: previous branch `659c90e24` versus its estimator-only patch. Twelve final timing runs passed (396 turns). The Anthropic persistence candidate was rejected and reverted.

- [Repository report](PROVIDER_MEMORY_FOLLOWUP.md)
- [Download report](/files/238768abeed60d5ab1e53c4ca54fd0c4?project_id=b184292efa07a415fc59967a5fc328d1)
- [Raw phase samples, profiles, timings, and source evidence](/files/9e66a5692ce7b6059dc0bd040bb1567a?project_id=b184292efa07a415fc59967a5fc328d1) (2,374,928 bytes)

Archive SHA-256: `6abfb3a3b33d70c9bcb1986b5ba8ab6d6e0d370ad8a0fdc863468f89a4304ed1`.

The archive separates unprofiled measurements from diagnostics and rejected experiments. Final timing directory labels `main`/`branch` mean previous branch/candidate, not Git main/branch. Temporary build worktrees were removed; app databases and binaries are omitted.

## Anthropic and OpenAI-compatible comparison

[Full report](PROVIDER_RESULTS.md): main `9e702736a` versus task branch `dfe6f4b3f`, three repetitions per scenario. All 48 timing runs passed (1,584 turns), plus four separate diagnostic runs and ten smoke runs. This comparison measures the entire branch, not just the seven optimizations.

- [Download report](/files/1ed65c30ed0059c6cdfdc8489617fd39?project_id=b184292efa07a415fc59967a5fc328d1)
- [Raw evidence and profiles](/files/def8b7636f4517d5f8113eb37a1f2fd7?project_id=b184292efa07a415fc59967a5fc328d1) (2.19 MiB; requires access to this project)

Archive SHA-256: `c2b119c1175e229e2eebbec31e69d610daa46632ee83002f8421b0f27dd678a8`.

The archive contains timing samples, RSS samples, delivery observations, provider request statistics, acceptance records, logs, diagnostic pprof files, source/binary hashes, exact harness sources, and generated tables. App databases and executables are omitted. Timing and diagnostic directories are separate. Both temporary source checkouts were removed after validation.

## Earlier OpenAI and transcript evidence

All 217 original files under `scripts/replay-memory-bench` at commit `0d7d5157c` were archived before repository cleanup. This includes historical reports, raw JSON, profiles, nested evidence archives, obsolete prototypes, and exact harness sources. Application code is unchanged by cleanup. The original branch history also remains under the local backup refs; cleanup does not garbage-collect Git objects.

The 96,385,755-byte archive is split because chat artifacts have a 25 MiB upload limit. Download all four parts and the checksum file from the originating OpenVibely project (links require access to that instance/project):

- [Part 1](/files/7300e71f393edc64faf344010df25020?project_id=b184292efa07a415fc59967a5fc328d1)
- [Part 2](/files/5a62b61a040a123957e68181f78bbf9b?project_id=b184292efa07a415fc59967a5fc328d1)
- [Part 3](/files/7a5159904b1d1ea65b4e5896f1425f81?project_id=b184292efa07a415fc59967a5fc328d1)
- [Part 4](/files/c7fad850cb8ae305f9f8a1fd6f745ec3?project_id=b184292efa07a415fc59967a5fc328d1)
- [SHA256SUMS](/files/486332d06cb43d45cf5b1b9e745fefda?project_id=b184292efa07a415fc59967a5fc328d1)

Save the parts with their original filenames in one directory, then reassemble and verify before extraction:

```sh
cat benchmark-evidence.tar.gz.part01 benchmark-evidence.tar.gz.part02 benchmark-evidence.tar.gz.part03 benchmark-evidence.tar.gz.part04 > benchmark-evidence.tar.gz
shasum -a 256 -c SHA256SUMS
mkdir extracted-benchmark-evidence
tar -xzf benchmark-evidence.tar.gz -C extracted-benchmark-evidence
```

Full archive SHA256:

```text
abba0d660f896314ecd3d65b5d40649f2c0d4cecbcb2ff7bbc6f97efd1b3104e
```

`manifest.json` at the archive root records the original commit, file sizes, and SHA256 of every original file. Each archived file was read back and verified before the repository copies were removed. The numbered downloads contain the original compressed archive bytes in order.

After extraction, the latest complete report is:

```text
scripts/replay-memory-bench/concurrent/request-recovery/README.md
```

That directory contains every turn, phase, reclamation counts, compaction/browser results, rejected candidate comparisons, validation logs, pinned source/binary metadata, and its own detailed evidence archive. Historical rounds retain their original baselines and limitations. Earlier bulk-save microbenchmarks are not representative streaming measurements.

For new measurements, use the retained [concurrent harness](concurrent/README.md) or [sequential/browser harness](app-flow/README.md). Store generated results outside this worktree. The concise latest summary is [RESULTS.md](RESULTS.md).

## Compatible retention fix — baseline e7c495c38

[Report](/files/2a689ceb1f7670099282271d2f7dcabd?project_id=b184292efa07a415fc59967a5fc328d1) · [Raw evidence and profiles](/files/8e438250dbd07f668274bddeb1cf3a3d?project_id=b184292efa07a415fc59967a5fc328d1). Repository report: [COMPATIBLE_RETENTION_RESULTS.md](COMPATIBLE_RETENTION_RESULTS.md).

Previous task branch versus compatible retention fixes, not main. All 12 final timing runs (396 turns), two separate diagnostics, and final smoke runs passed. Includes before/after CPU, allocation and near-peak heap profiles, text summaries, exact production patch, source, phase samples, validation logs and per-file manifest. Superseded partial timing runs are labeled and excluded. Databases and generated fixture repositories are not archived.

Archive: 1,327,352 bytes; SHA256 `0f8f289c20d36669c27f0e41d8546c0277c53a53fe05eba6d1b4ea7b3e1e85d9`.

## Shared provider paths — baseline 036d7d15b

[Full report](/files/ce557305772b6ef9a736c5add78a67af?project_id=b184292efa07a415fc59967a5fc328d1) · [Raw measurements and profiles](/files/14bf6c874a8d73aff0c49bdfb914663c?project_id=b184292efa07a415fc59967a5fc328d1). Repository report: [SHARED_PROVIDER_PATHS_RESULTS.md](SHARED_PROVIDER_PATHS_RESULTS.md).

Previous task branch versus shared request/history paths, not main. Same revisions and explicit context budget for all three providers. All 72 timing runs (2,376 turns), six diagnostic runs, and six candidate smoke runs passed. Includes every turn, RSS phases, before/after CPU/allocation/near-peak profiles, readable profile summaries, exact measured patch, separately tested recovery follow-up patch, final source, harness, revision/binary metadata, validation logs and SHA256 manifest. The recovery follow-up is not included in measured binary results; see report. Databases and generated fixture repositories are excluded.

Archive: 4,931,481 bytes; SHA256 `d72899d49ff663f5494090b9d1d4bbacf354271b58ab40db26b5c44e71719510`.

## Remaining provider optimization parity — baseline 3ccd98cfc

Previous task branch `3ccd98cfc75931e2edaa3ea82c5a8afc2f0f952b` versus the same source plus all eight provider-parity optimizations. This is not main versus branch. All 72 timing runs (2,376 turns), six separate diagnostics (360 turns), and six smoke scenarios passed. Production files match the measured candidate exactly.

- [Repository report](PROVIDER_PARITY_RESULTS.md)
- [Download report](/files/bdec126d1f5374ed98b59a7e2b893abd?project_id=b184292efa07a415fc59967a5fc328d1)
- [Raw measurements, CPU/allocation/heap profiles, readable summaries and exact source patch](/files/021bc814c0602bc0ae2b71f19e6e154d?project_id=b184292efa07a415fc59967a5fc328d1) (4,938,126 bytes)

Archive SHA-256: `1975eb529abff6c3d6277a3a08bcfc418c90c12382a882113b9c8b4cdc844d4e`. All 1,857 payload files were read back and verified against the included checksum manifest. Databases, generated fixture repositories and binaries are excluded. Both temporary build worktrees were removed after source verification.

The report retains CPU regressions, RAM ranges, delivery gaps and unchanged storage costs. SSE allocations and median RAM improved in all eight scenarios; this is not a claim of universal CPU improvement. Early development recovery-test failure is retained separately from final passing validation. No failed timing runs were omitted.
