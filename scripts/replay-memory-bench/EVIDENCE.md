# Archived benchmark evidence

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
