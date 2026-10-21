### Editing chart data

For native chart formatting, use chart labels rather than duplicate text boxes. Place stacked-series labels inside segments where supported; suppress labels/outlines for positioning-only helper series while preserving signed values. Set fonts for chart titles, axes, legends and data labels explicitly through supported chart options, then verify the rendered result.

For literal input changes in any embedded chart workbook, including one containing formulas, use `chart-inputs`. It preserves formulas and their source lineage:
`"$PAPERWORKS" chart-inputs source.pptx --requirements chart-edit.json --output edited.pptx`.
The JSON specifies a discovered chart, worksheet and literal input cells. Prefer the exact inspected `chart_part`, for example `{"chart_part":"ppt/charts/chart7.xml","sheet":"Sheet1","cells":{"B2":180}}` only when that part and cell exist. Alternatively, `"chart":1` selects the first discovered chart; it does not mean the filename ends in `1.xml`. Inspect the embedded workbook first to choose actual driver cells. This operation refuses to overwrite formulas, recalculates the workbook, updates caches for charts sharing it and preserves unrelated slide/package parts. It needs the ordinary office calculation runtime. It does not infer which prose, percentages or notes depend on the chart: update those explicitly, validate their totals and render the final deck.

For an intentional replacement of a single-series column chart's categories/values, use `"$PAPERWORKS" chart-series-replace input.pptx --spec series.json --output updated.pptx`, with `{"part":"ppt/charts/chart1.xml","name":"Revenue","categories":["A","B"],"values":[120,80]}` using an inspected part. This replaces the embedded workbook while retaining chart placement/formatting. Use `chart-inputs` instead when existing workbook cells/formulas must survive; this is not a general multi-series chart editor.


## Native hierarchical charts and histograms

List and validate extended native charts with:

```sh
"$PAPERWORKS" extended-charts source.pptx
```

Create a treemap using this specification:

```json
{"action":"add","type":"treemap","slide":1,"name":"Revenue","categories":["A","B","C"],"values":[10,20,5],"position_emu":[914400,914400,5486400,3657600]}
```

```sh
"$PAPERWORKS" extended-charts source.pptx --requirements chart.json --output charted.pptx
```

For a numeric `histogram`, omit `categories` and supply observations in `values`. For a histogram that also stores category labels, supply a flat string `categories` list with one label per value. When editing an imported categorical histogram, omitted categories retain its existing labels; changing the number of values requires matching new labels. Category labels are never silently discarded. This preserves the categorical ChartEx data; it does not add a Pareto cumulative line or guarantee that a renderer displays the labels instead of numeric bins. Without a bin setting, the native application chooses automatic bins. To specify bins, use `bin_size` (positive number) or `bin_count` (positive integer), never both. Optional `interval_closed` is `l` or `r`; `underflow` and `overflow` accept finite boundaries or `auto`. Data-only edits retain existing bin settings. The four position values are left, top, width and height in EMUs (914400 per inch); the chart must fit the slide. These are editable native ChartEx objects, not pictures. For changes use `action:"replace-data"`, the listed `chart_part`, new `values`, and treemap `categories`. This retains placement, axes and styles. Use `type:"sunburst"` for a native sunburst. For hierarchical treemaps or sunbursts, replace `categories` with `category_paths`, such as `[["A","North"],["B","North"],["C","South"]]`, one equal-depth path for each value. Paths run from the leaf label to its parent and then toward the root (matching native ChartEx category-level order). Literal authoring uses one series. Imported charts can reference ranges in an embedded XLSX workbook: validation compares every cached level/value to that workbook, and `chart-inputs` updates the workbook and chart caches together. Use that operation for linked charts; `replace-data` rejects them to preserve their formulas. Remote workbooks, arbitrary chart formula expressions and other extended chart types remain unsupported.

`verify-slides` validates supported extended chart data. `chart-inputs` accepts a listed `chart_part` or one-based discovered `chart` number for standard and supported extended embedded-workbook charts. Use the inspected identifier rather than assuming a package path.

`render` previews treemaps, sunbursts, histograms, waterfalls and funnels while preserving native charts in the delivered PPTX. Unsupported styles/formats or invalid scales can fail rendering.

Histogram previews plot observation frequencies, not category counts or Pareto lines. Use explicit bins when boundaries matter: automatic bins may differ from the target application's. The preview limit is 1000 bins, including tail bins. Inspect frequencies, labels, placement and clipping.

Waterfall and funnel charts can be inspected and previewed, but `extended-charts` does not edit their data.

