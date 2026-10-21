## Native sparklines

Use the same document helper to create or edit sparklines without rewriting unrelated workbook parts:

```sh
"$PAPERWORKS" sparklines source.xlsx --output sparklines.xlsx --requirements sparklines.json
```

Example `sparklines.json`:

```json
{"sheet":"Revenue","action":"set","sparklines":[{"cell":"F2","range":"'Revenue'!B2:E2"}],"options":{"type":"line","markers":true,"high":true,"colorSeries":"17324D","colorHigh":"008000"}}
```

`set` adds or replaces the specified target cells, retaining other sparklines. Supply all desired options when replacing a target. Types: `line`, `column`, `stacked` (win/loss). Sources must be local row/column ranges; each target is one cell. Options include `lineWeight`, `displayEmptyCellsAs` (`gap`, `zero`, `span`), `displayHidden`, `displayXAxis`, `rightToLeft`, `markers`, `first`, `last`, `high`, `low`, `negative`; RGB/ARGB colors `colorSeries`, `colorNegative`, `colorAxis`, `colorMarkers`, `colorFirst`, `colorLast`, `colorHigh`, `colorLow`; `minAxisType`/`maxAxisType` (`individual`, `group`, `custom`) and `manualMin`/`manualMax` for custom bounds.

For deletion use `action:"delete"` with `sparklines:[{"cell":"F2"}]`; `action:"clear"` removes that sheet's sparklines. `inspect` lists native groups. Work on a new output file, render and inspect the small charts, and verify source ranges and styling. Avoid a general library re-save after insertion: it can remove unsupported extension parts. These helpers preserve the native XLSX sparklines through narrow input edits and recalculation.

The render helper works around LibreOffice’s missing line-sparkline PDF strokes on a **temporary preview copy**. It recalculates the original workbook first, then draws the line, gaps, markers and axes using ordinary vector drawing objects. The delivered XLSX retains its editable native sparkline groups. Column/win-loss rendering stays native. Use the helper instead of plain LibreOffice PDF export when line sparklines are present; inspect the resulting image.

