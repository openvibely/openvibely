## General workbook edits

Run `"$PAPERWORKS" workbook-edit input.xlsx --spec edits.json --output edited.xlsx` with `{"edits":[...]}`. Operations run in order; indices and sheet names refer to the workbook after preceding edits. This editor may normalize workbook XML. Prefer `workbook-inputs` for literal-only edits requiring its narrower preservation guarantees. A failed batch does not publish a partial output. Recalculate the saved workbook before using formula results.

| Operation | Fields beyond `operation` and `sheet` |
|---|---|
| `value` / `formula` | `cell` plus literal `value` or `formula`; formula has no leading `=`. Optional `formula_style:r1c1` for R1C1 formulas. |
| `fill-formula` | `range`, top-left `formula`, optional `formula_style`. Translates relative references; keep dynamic-array formulas at their anchor rather than filling spill cells. |
| `copy` / `copy-values` / `copy-formulas` / `copy-formats` | Source `range`, destination top-left `target`, optional destination sheet in `name`. Overlap uses a snapshot. Formula copies translate relative references; absolute references remain fixed. `copy-formulas` also copies literals without styles. `copy-values` uses saved results, so recalculate the source first. |
| `clear-values` / `clear-formats` / `clear-all` | `range`; values includes formulas. All clears cell content/styles, not comments or drawings. |
| `style` | `range`, Excelize `style`, e.g. `{"NumFmt":10,"Font":{"Bold":true}}`. Nested option keys use their exported capitalization; consult `schema workbook-edit`. |
| `column-width` / `autofit-columns` / `row-height` | Column `range` (e.g. `A:C`), with `size` for width; row height uses one-based `index` and point-valued `size`. Avoid sheet-wide autofit for narrow changes. |
| `panes` / `view` | Excelize `panes` or `view`; clear panes with `{"Freeze":false,"Split":false}`. |
| `add-sheet` / `rename-sheet` / `delete-sheet` / `move-sheet` | `name` for add/rename; move uses an existing destination sheet in `target`. Add does not need `sheet`. |
| `merge` / `unmerge` | `range`. Keep data tables unmerged. |
| `insert-rows` / `delete-row` | One-based `index`; insertion needs `count`. |
| `insert-columns` / `delete-column` | Column name in `cell`; insertion needs `count`. |
| `set-name` / `delete-name` | Excelize `defined_name`; no `sheet` required. |
| `add-table` / `set-table` / `delete-table` | Excelize `table` for add; set also needs old `name` and the complete replacement `table`; delete uses `name`. |
| `validation` / `delete-validation` | Excelize `validation` for add; `range` for delete. |
| `conditional-format` / `delete-conditional-format` | `range`; add needs Excelize `conditional` rules and optional differential `style`. |
| `add-chart` / `replace-chart` / `delete-chart` | Anchor `cell`; add/replace need complete Excelize `chart`, optional `combo`. Inspect the existing chart first; omitted settings are not inferred. |
| `add-picture` / `replace-picture` / `delete-picture` | Anchor `cell`; add/replace need `picture:{"path":"asset.png","options":{...}}`. |
| `note` / `delete-note` | Excelize `note:{"Cell":"C2","Author":"Reviewer","Text":"Check input"}` for add; `cell` for delete. Notes are separate from threaded comments. |

When ranges grow, check dependent formulas, charts, formatting and validation. Use Scope and source fidelity for the full preservation inventory.

### General editing example

Starting from `example.xlsx`, add a formula-driven share column and copy the existing revenue results as values. Recalculate before copying saved results and again after editing:

```sh
"$PAPERWORKS" recalculate example.xlsx --output copy-source
cat > workbook-edit.json <<'JSON'
{"edits":[
 {"operation":"value","sheet":"Revenue","cell":"E1","value":"Share"},
 {"operation":"fill-formula","sheet":"Revenue","range":"E2:E3","formula":"D2/$D$4"},
 {"operation":"style","sheet":"Revenue","range":"E2:E3","style":{"NumFmt":10}},
 {"operation":"copy-values","sheet":"Revenue","range":"D2:D3","target":"F2"}
]}
JSON
"$PAPERWORKS" workbook-edit copy-source/example.xlsx --spec workbook-edit.json --output formula-edit.xlsx
"$PAPERWORKS" recalculate formula-edit.xlsx --output formula-results
"$PAPERWORKS" inspect-select formula-results/formula-edit.xlsx --sheet Revenue --range A1:F4
"$PAPERWORKS" render formula-results/formula-edit.xlsx --sheet Revenue --range A1:F4 --output formula-preview
cat > import.csv <<'CSV'
ID,Label
00123,Example
CSV
"$PAPERWORKS" import-csv import.csv --sheet Data --output imported.xlsx
```

Verify E2/E3 remain formulas with results 0.6/0.4, F2/F3 are numeric constants 120/80, and the imported ID remains the string `00123`. View the preview before publishing the recalculated workbook.

## Editing example

After the authoring example creates `example.xlsx`, change one literal quantity and recalculate the existing formulas. For another workbook, inspect the actual sheet, driver cells and formula dependencies first.

```sh
"$PAPERWORKS" inspect example.xlsx > before.json
cat > inputs.json <<'JSON'
{"sheet":"Revenue","cells":{"B2":4}}
JSON
"$PAPERWORKS" workbook-inputs example.xlsx --requirements inputs.json --output changed.xlsx
"$PAPERWORKS" recalculate changed.xlsx --output calculated
"$PAPERWORKS" inspect calculated/changed.xlsx > after.json
"$PAPERWORKS" render calculated/changed.xlsx --sheet Revenue --range A1:D4 --output changed-preview
```

`recalculate` writes a copy with the input basename under its new output directory; confirm the path in its JSON result. Verify `B2=4`, `D2=160`, `D3=80`, `D4=240` and that `D2:D4` still contain formulas. Call `view_image` for the range PNG, and render the chart too if delivering the whole workbook. Publish `calculated/changed.xlsx`, not the unrecalculated `changed.xlsx`. `workbook-inputs` rejects formula overwrites, merged cells and array/table result cells; do not bypass that by replacing the workbook with a new creation spec.


## Fit row heights to content

Use `workbook-edit` with `autofit-rows` to size rows for a cell range.

```json
{"edits":[{"operation":"autofit-rows","sheet":"Data","range":"A1:D20"}]}
```

Set final column widths, formats and wrapping first. Recalculate formula results before fitting: this operation measures formatted saved values and rich-text runs, not recalculated formulas. Include all columns whose content should determine the selected row heights, up to 10,000 cells. Unselected rows remain unchanged. It uses available fonts and handles explicit newlines and complete horizontal merges. Vertical merges, rotated text, shrink-to-fit, unavailable fonts/glyphs, missing formula caches and heights beyond Excel's 409.5-point limit fail explicitly. Render the affected range afterward; fonts and shaping can differ in the target engine. Use explicit `row-height` when automatic measurement cannot handle the source.

### Existing worksheet tab colors

Use a `tab-color` operation with a six-digit RGB `color`; an empty string clears the tab color. The sheet must exist. Replacing a color removes its previous theme, indexed-color and tint settings.

```json
{"edits":[{"operation":"tab-color","sheet":"Summary","color":"4472C4"}]}
```

Run with `paperworks workbook-edit input.xlsx --spec edits.json --output updated.xlsx`.

## Add a sensitivity table to an existing workbook

Use `workbook-edit` with `add-data-table`. Set up the model inputs, output formula and trial headers first, either in earlier edits or in the existing workbook.
```json
{"edits":[{"operation":"add-data-table","sheet":"Model","data_table":{"range":"E3:F4","input1":"B1","input2":"B2"}}]}
```
Here `E3:F4` is the result body, `D2` holds the output formula, `E2:F2` holds row trials, and `D3:D4` holds column trials. `input1` is the row-input cell and `input2` the column-input cell on the same sheet. The command rejects occupied result cells, merges, structured tables and existing formula-range overlaps. It writes a native definition, not calculated results: recalculate the output, inspect the table values, and verify selected scenarios independently.
