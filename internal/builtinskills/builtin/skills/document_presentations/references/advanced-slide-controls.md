# Advanced slide controls

These additions are implemented in Paperworks after RC4. The skill still downloads **0.0.0-rc4**, which does not support them.
Use this reference only with a verified newer local build. Check `schema
create-pptx` and `schema slide-edit` for `hidden`, `text_box`, `background_fill`,
`svg`, `auto_layout`, `text_paint`, `table_format` and `chart_format` before using
the corresponding feature. Otherwise use the pinned commands
from the other references; do not send unsupported fields that may be ignored.

All edit examples below go inside `{"edits":[...]}` and run with
`"$PAPERWORKS" slide-edit input.pptx --spec edits.json --output edited.pptx`.
Use inspected slide numbers and shape IDs. Preserve the source and render the
final output as required by the main skill.

## Hide or show a slide

```json
{"operation":"visibility","slide":1,"hidden":true}
```

Use `false` to show it. No shape ID is needed. New slides accept `hidden` in
`create-pptx`. Hidden slides remain in previews and in the file; slideshow
applications skip them.

## Text-box padding, alignment and fitting

```json
{"operation":"text-box","slide":1,"shape_id":"2","text_box":{"insets":{"left":12,"right":12,"top":6,"bottom":6},"vertical_align":"middle","wrap":"square","auto_fit":"none"}}
```

New text/shape elements accept the same `text_box` object. Insets are points;
omitted sides/settings remain unchanged when editing. Vertical alignment is
`top`, `middle` or `bottom`; wrapping is `square` or `none`. Autofit is `none`,
`shrinkText` or `resizeShapeToFitText`. Autofit stores application behavior; it
is not a guarantee that text fits. Prefer content/layout changes over shrinking
text, rerun measured layout after padding changes, and inspect the final render.

## Native slide backgrounds

```json
{"operation":"background","slide":1,"background_fill":{"gradient":{"angle":90,"stops":[{"position":0,"color":"FFFFFF"},{"position":1,"color":"BBD8EE"}]}}}
```

Created slides accept `background_fill` instead of `background`. It uses the
same solid `fill`, `gradient`, `pattern` or `image` definitions as drawing styles,
without outline/shadow. Choose exactly one fill. Image paths resolve relative
to the spec; `cover` uses the slide dimensions, and `stretch`/`tile` are also
supported. Check cropping and text contrast in the preview.

## SVG pictures

New image elements accept a local SVG `path` or inline `svg` instead of a path:

```json
{"type":"image","x":1,"y":1,"w":4,"h":2,"svg":"<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 200 100\"><rect width=\"200\" height=\"100\" fill=\"#17324D\"/></svg>"}
```

Use `replace-image` with `path` or `svg` for an existing picture. Sources must
be self-contained and specify positive dimensions or a viewBox. SVG remains
vector image data, not editable slide shapes. No legacy PNG fallback is generated;
verify in the intended SVG-capable application when compatibility matters.
Existing contain/cover/crop options apply to creation. Replacement preserves
existing picture geometry and crop. Use this for supplied artwork or permitted
editable-diagram workflows, not to bypass the skill's illustration rules.

## One-position stacking

```json
{"operation":"forward","slide":1,"shape_id":"2"}
```

`forward`/`backward` move one drawable position within the current slide/group.
`front`/`back` move to the ends. Moving beyond an end does nothing. These operations
change overlap order without changing position or group membership.

## Automatic spacing and alignment without resizing

```json
{"operation":"auto-layout","slide":1,"auto_layout":{"shape_ids":["2","3"],"direction":"horizontal","x":1,"y":1,"width":10,"height":4,"align":"center","auto_gap":true}}
```

Created slides accept the same `auto_layout` object instead of `layout`.
It preserves sizes and arranges objects in the supplied order. Directions are
`horizontal` and `vertical`. Alignment is `center`, `topLeft`, `topCenter`,
`topRight`, `left`, `right`, `bottomLeft`, `bottomCenter` or `bottomRight`.
Use a numeric `gap` or `auto_gap:true`, never both. Automatic gaps distribute
remaining space between objects; a single object uses alignment. Optional
`padding_x`/`padding_y` inset both edges. All dimensions and gaps are inches.

Omit frame coordinates to use the whole slide, or use `frame_shape_id` to use
an existing top-level shape's frame instead of coordinates. The frame cannot
also be in `shape_ids`. Selected shapes need explicit geometry and must be
top-level. Overflow fails rather than shrinking objects. This uses unrotated
frames; inspect rotated objects and rerun after content or geometry changes.

## Text glyph fills and outlines

Use `text-paint` for a whole text shape or `run-format` with zero-based
`paragraph` and `run` for a selected run. New text elements accept `text_paint`.
This paints the letters; it does not change the box background or text.

```json
{"operation":"text-paint","slide":1,"shape_id":"2","text_paint":{"fill":{"gradient":{"angle":90,"stops":[{"position":0,"color":"17324D"},{"position":1,"color":"4488BB"}]}},"outline":{"width":0.5,"fill":{"fill":"FFFFFF"}}}}
```

Fills accept solid `fill`, `gradient`, or `pattern`, not images. Outline `width`
is in points; zero removes it. Omitted paint settings remain unchanged. Gradient
`path` can be `circle`, `rect`, or `shape` instead of `angle`; optional `focus`
insets `left`, `top`, `right`, `bottom` are fractions in 0..1 (default center).
Opposing focus insets must not sum above 1. LibreOffice may render glyph gradients
as flat color; check the intended application when that effect matters. Render
and check letter readability.

## Table cell styling and individual sizes

```json
{"operation":"table-format","slide":1,"shape_id":"2","table_format":{"column_widths":[180,240],"row_heights":[36,48],"cells":[{"row":0,"column":0,"margins":{"left":8,"right":8},"vertical_align":"middle","borders":{"bottom":{"width":1,"fill":{"fill":"17324D"}}}}]}}
```

New table elements also accept `table_format`. Rows and columns are zero-based;
all sizes, margins and border widths are points. Size arrays must cover the
entire corresponding axis; omit either array to retain it. Cell targets must be
merged-cell origins, not continuation cells. Cells also accept `fill` and
`text_paint`. Borders use `left`, `right`, `top`, `bottom`; width zero removes a
border. The table frame resizes to the total row/column sizes. Check that it
still fits on the slide and that text fits inside the adjusted cells.

## Additional chart families

Newer `create-pptx` builds accept chart types `surface`, `surface3D`, `ofPie`,
and `barOfPie`. The split-pie types require one series. These are native charts
with embedded data; inspect and render them in the intended application.

Newer `extended-charts` builds also add/replace literal `waterfall` and `funnel`
data. Check command help for these types before use; `schema create-pptx` does
not describe `extended-charts`. Use `--requirements`, not `--spec`:

```json
{"action":"add","type":"waterfall","slide":1,"name":"Change","position_emu":[914400,914400,5486400,3657600],"categories":["Start","Increase","Decrease"],"values":[100,40,-20]}
```

```sh
"$PAPERWORKS" extended-charts input.pptx --requirements chart.json --output updated.pptx
```

Waterfall values are signed increments. Funnel values must be nonnegative with
at least one positive value. For replacement, use `action:"replace-data"`, the
inspected `chart_part`, and `values`; omitted categories keep existing labels,
so provide new categories when changing the count. Linked workbook data still
uses `chart-inputs`. Histogram bin settings and hierarchy paths do not apply.
Do not infer map, combo, box-and-whisker or a cumulative Pareto curve from this
list. Speaker notes visibility is not a native PPTX edit command.

## Edit existing chart formatting

Use `chart-format` to retain the data/workbook while changing selected settings:

```json
{"operation":"chart-format","slide":1,"shape_id":"2","chart_format":{"legend":{"position":"b"},"series":[{"index":0,"fill":{"fill":"336699"},"labels":{"show_value":true}}]}}
```

- `axes`: existing native `id`, with optional `visible`, `title` (empty removes),
  `minimum`, `maximum`, `major_unit`, `number_format`. Read IDs from `c:axId` in
  the inspected chart part's XML; do not guess. Bounds require a value/date axis.
- `legend`: `visible`, `position` (`b`, `t`, `l`, `r`, `tr`), `overlay`.
- `series`: zero-based document-order `index`; optional `fill`, `outline`,
  `labels`. Outline uses the text-paint definition above. Labels accept
  `show_value`, `show_category`, `show_series`, `show_percent`, `position`,
  `number_format`. Percent labels require pie/doughnut; surface/stock labels
  are rejected. Position values are `bestFit`, `b`, `ctr`, `inBase`, `inEnd`,
  `l`, `outEnd`, `r`, `t`; choose a position appropriate to the chart family.

Omitted properties and per-point overrides remain intact. This edits the native
chart part, including any objects sharing it. It does not style ChartEx charts
or reconstruct charts stored only as images. Verify data, legend, axis labels
and layout after saving; this is not a replacement for the chart-data checks.

## Detailed chart styling (newer local build)

Check `schema slide-edit` for these fields before use. Existing standard charts
accept `chart-format` with axis `minor_unit`, `text_style`, `major_gridline` and
`minor_gridline`; legend `text_style`, `fill`, `outline`; and series label
`text_style`. Text style uses `font`, `font_size` in points, hex `color`, `bold`,
and `italic`. Gridlines/outlines use the text outline format above.

```json
{"operation":"chart-format","slide":1,"shape_id":"2","chart_format":{"series":[{"index":0,"points":[{"index":1,"fill":{"fill":"FF0000"}}],"trendlines":[{"type":"linear","display_equation":true}],"error_bars":{"type":"percentage","value":5}}]}}
```

Point indices are zero-based cached value positions. `trendlines` replaces the
series' trendlines (`[]` clears); types are `linear`, `exponential`,
`logarithmic`, `polynomial`, `power`, `movingAverage`. Use `order` (2..6) for
polynomial or `period` (2..255) for moving average. Forecast `forward`/`backward`,
`intercept`, `display_equation`, `display_r_squared`, `name` and `outline` are
available where applicable; moving averages reject forecast/intercept/equation.
Error bars support `standardError`, `percentage`, `standardDeviation`, `none`,
optional `value` (not standardError), `direction` (`y` default; `x` only for
scatter/bubble), `end_style` (`cap`/`noCap`), and `outline`. `none` removes bars
in that direction. Trendlines/bars require non-stacked 2D bar, line, area,
scatter or bubble charts. ChartEx is not supported by this editing action.
Preserve data, render, and check labels/legend for overlap.

The tested LibreOffice preview ignores line-chart marker colors, including
series-level colors, even when native marker fills are stored. Verify those colors in the
intended PowerPoint application; do not claim the preview confirms them.

## Reusable named text styles (newer local build)

Check `schema create-pptx` / `schema slide-edit` for `named_styles`. Define text
formatting once and reuse it across shapes:

```json
{"named_styles":{"Heading":{"font_size":28,"bold":true,"color":"17324D","align":"ctr"}},"edits":[{"operation":"apply-style","slide":1,"shape_id":"2","style":"Heading"}]}
```

Definitions accept `description`, `font`, `font_size` (points), hex `color`,
`bold`, `italic`, `underline`, `align` (`l`, `r`, `ctr`, `just`). Omitted properties
stay unchanged. `scope` defaults to `shape`; `paragraph` and `run` scopes use
zero-based `paragraph` and `run` selectors. Run styles cannot set alignment.

For creation, put `style` on a text/shape element or one of its paragraph runs.
The named style wins over conflicting authored text properties; run styles apply
after the element style. Explicit paragraph `format` and element `text_paint`
apply afterward. Notes do not accept named styles. The PPTX stores the resolved
formatting, not live named-style bindings: retain the definitions in your JSON
and supply them again when editing later. Inspect the rendered layout after
changing font sizes or alignment.

## Individual chart labels

With a newer build exposing `label_overrides` in `schema slide-edit`, select a
series and cached point index to change one label without changing its data:

```json
{"operation":"chart-format","slide":1,"shape_id":"2","chart_format":{"series":[{"index":0,"label_overrides":[{"index":1,"text":"Priority","text_style":{"font_size":18,"bold":true},"position":"outEnd"}]}]}}
```

Overrides accept `text`, `text_style`, `fill`, `outline`, `position`; indices are
zero-based. Omitted fields/other labels remain unchanged. Custom text replaces
the label's existing text formula/runs; chart data is preserved. Hidden-label
settings remain unchanged. Render and inspect the label placement.

## Axis placement

Check the newer build's schema, then use inspected axis IDs:

```json
{"operation":"chart-format","slide":1,"shape_id":"2","chart_format":{"axes":[{"id":"INSPECTED_AXIS_ID","position":"right","tick_label_position":"high"}]}}
```

`position` is `bottom`, `left`, `right` or `top`; `tick_label_position` is
`nextTo`, `high`, `low` or `none`. Unspecified settings and axis-crossing values
remain unchanged. Render to check the final placement.

## Chart markers and creation formatting

With a newer build exposing series `marker`, use:

```json
{"operation":"chart-format","slide":1,"shape_id":"2","chart_format":{"series":[{"index":0,"marker":{"symbol":"diamond","size":12,"fill":{"fill":"FF0000"}}}]}}
```

Symbols: `auto`, `circle`, `diamond`, `dot`, `none`, `plus`, `square`, `star`,
`triangle`, `x`. Sizes are 2..72 points; `outline` uses the text outline format.
Supported families are line, scatter and radar. Existing point-specific marker
overrides remain unchanged.

Standard chart creation now preserves `chart.options.Series` formatting by index,
including `Marker`, `Line` and `DataLabel` (Excelize field names). Paperworks still
generates all `Name`, `Categories`, `Values`, `Sizes` references from `chart.series`;
do not supply external workbook references. More option entries than data series
is rejected. Check the build before relying on this post-RC4 behavior.

## Existing chart backgrounds

Check for `chart_fill` / `plot_area_fill` in the newer build's schema:

```json
{"operation":"chart-format","slide":1,"shape_id":"2","chart_format":{"chart_fill":{"fill":"FFFFFF"},"plot_area_fill":{"fill":"F2F5F9"}}}
```

These change the outer chart background and plot interior independently. Solid,
gradient and pattern fills use the existing drawing-fill format; image fills,
shadows and embedded outlines are not accepted. Borders, data, axes and labels
are preserved. Inspect contrast in the final render.


## Edit a chart title (after RC4)

Use `chart_format.title` with `slide-edit` to replace an existing standard
chart title or add one: `{"operation":"chart-format","slide":1,"shape_id":"2","chart_format":{"title":"Revenue by quarter"}}`.
An empty string removes the title and disables automatic titles. Omission
preserves it. Replacement uses plain text instead of the previous title's
formula/runs; title layout and other title properties remain. Chart data and
the embedded workbook are unchanged. Verify a newer local build exposes
`title` in `schema slide-edit`; render and inspect the result.


## Chart and plot borders (after RC4)

`chart_format.chart_outline` and `plot_area_outline` independently set
the outer chart and plot-interior borders. For example:
`{"chart_outline":{"width":2,"fill":{"fill":"17324D"}},"plot_area_outline":{"width":0}}`
inside `chart_format`. Widths are points; zero removes the border.
Use the same solid/gradient/pattern fill and dash options as other outlines.
Omitted outlines and existing backgrounds remain unchanged. Check the newer
local schema before use, then render and inspect border placement.


## Axis crossing, label offset and line (after RC4)

On an inspected `chart_format.axes` entry, `crosses_at` writes the crossing
coordinate on that native axis and removes its mutually exclusive automatic
`crosses` mode. For a value axis it specifies the value at which the other axis
crosses; category-axis coordinates represent category positions.
`label_offset_percent` is an integer 0..1000 for category/date axes only
(100 means 100% of the label font size). `outline` uses point-valued line width,
fill and optional dash; width zero removes the axis line.
Example: `{"id":"INSPECTED_VALUE_AXIS_ID","crosses_at":25,"outline":{"width":1,"fill":{"fill":"17324D"}}}`.
Omitted settings remain unchanged. These are post-RC4 fields; check the local
schema and render to verify crossing, tick labels and line placement.


## Additional chart label controls (after RC4)

Series `labels` accepts `separator`, `show_legend_key`, `show_leader_lines`
and `show_bubble_size`, alongside the existing value/category/series/percent
flags. Each `label_overrides` entry accepts `separator`, `show_value`,
`show_category`, `show_series`, `show_percent`, `show_legend_key` and
`show_bubble_size`. For example, inside a selected series:
`{"labels":{"separator":" / ","show_legend_key":true},"label_overrides":[{"index":1,"show_value":false,"show_category":true,"separator":""}]}`.
Booleans accept explicit false; an empty separator clears it. Omission preserves
existing settings/inheritance. Percent=true requires pie/doughnut, and bubble
size/leader-line display depends on the chart family and application. Existing
hidden-label settings remain unchanged. Leader lines are a series-label
setting, not a per-point field. Check the newer local schema, then render to
verify the resulting labels; native flags do not guarantee visible leader lines.

The tested LibreOffice preview retained a separator for an individual label
even when its native separator was saved as empty. Verify exact point-label
separator behavior in the intended PowerPoint application.

## Chart title style and overlay (after RC4)

Inside `chart_format`, `title_style` uses the chart text style fields and
`title_overlay` selects overlay (`true`) or above-chart placement (`false`).
Example: `{"title":"Revenue","title_style":{"font_size":24,"bold":true},"title_overlay":false}`.
Formatting without `title` requires an existing title; a removed/missing title
cannot be styled. Existing rich-text runs receive supplied style properties;
omitted properties and manual layout are retained. Formula-backed title text is
not replaced by style-only edits. Verify these fields in the newer local schema
and render to check title placement, especially with an existing manual layout.
