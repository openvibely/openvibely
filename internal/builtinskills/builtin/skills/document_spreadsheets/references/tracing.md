## Formula dependency tracing

Trace an output back through its static formula references before explaining a suspicious result:

```sh
"$PAPERWORKS" trace workbook.xlsx --sheet Summary --cell F12 --max-nodes 200 --max-depth 20
```

The read-only JSON report contains cells, formulas, saved values, edges from each formula to its precedents, cycles, unresolved references, and `truncated`/`complete` flags. It supports cell ranges, absolute/relative cell references, quoted cross-sheet references, and local/global defined names. Summarize the relevant path and source values; redirect a large report to a task file rather than dumping it into chat.

This is static dependency analysis, not formula evaluation: all referenced branches are included and cached values may be stale. Literal/input-driven `INDIRECT` addresses (A1 or R1C1, including relative R1C1 and concatenated address inputs), `OFFSET` with literal/input offsets, table columns/column ranges/sections, whole-row/column ranges and 3D sheet spans are traced. Whole-row/column ranges use the saved populated extent of the referenced rows/columns and the same node budget. Address inputs that are themselves formulas remain unresolved: stale cached results are not trusted. Arbitrary address expressions, external workbooks, combined table-section selectors, relative defined names and spill syntax remain explicit limitations. Recalculate/inspect those with a capable application before making a conclusion. A trace with `complete:false` is partial; never describe it as the full dependency tree. Cycles are reported rather than followed indefinitely. Bounds limit report size; they do not silently turn omitted cells into zeroes.

