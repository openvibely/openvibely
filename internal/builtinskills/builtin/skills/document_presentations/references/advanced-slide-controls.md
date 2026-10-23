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

## Chart text underline and alignment (after RC4)

All chart `text_style` objects and `title_style` accept `alignment`
(`left`, `center`, `right`, `justify`) and native `underline` tokens.
Use `sng` for single, `dbl` for double and `none` to remove underline.
Other tokens are `words`, `heavy`, `dotted`, `dottedHeavy`, `dash`,
`dashHeavy`, `dashLong`, `dashLongHeavy`, `dotDash`, `dotDashHeavy`,
`dotDotDash`, `dotDotDashHeavy`, `wavy`, `wavyHeavy`, `wavyDbl`.
Example: `{"legend":{"text_style":{"alignment":"right","underline":"sng"}}}`
inside `chart_format`. Alignment applies to paragraphs; underline applies to
run/default properties. Omission preserves existing settings. Check the local
schema and render to verify the intended application displays the style.

## Trendline label formatting (after RC4)

A `chart_format.series[].trendlines[]` entry accepts `label` with `text`,
`text_style`, `fill` and `outline`. Example:
`{"type":"linear","display_equation":true,"label":{"text":"Trend","text_style":{"font_size":18},"fill":{"fill":"EEEEEE"},"outline":{"width":1,"fill":{"fill":"17324D"}}}}`.
Without `text`, the label can style the generated equation/R-squared display.
Set the applicable `display_equation` or `display_r_squared` flag and inspect
the render; label formatting alone does not enable an equation. The existing
`trendlines` operation replaces the whole series trendline list, so include
all trendlines/settings that should remain. Omit `trendlines` to preserve them.
Use only with a newer local schema exposing `label`.

## Series-label visibility, paint and source formats (after RC4)

Series `labels` accepts `visible`, `fill`, `outline` and
`number_format_source_linked`. Example:
`{"visible":true,"fill":{"fill":"FFFFDD"},"outline":{"width":1,"fill":{"fill":"17324D"}},"number_format":"0.0","number_format_source_linked":true}`.
Visibility controls the collection's native delete flag; explicit per-point
settings remain unchanged. Fills/outlines use the existing chart paint format.
To change source linkage, a number format must already exist or be supplied.
Setting `number_format` alone retains the existing command behavior of disabling
source linkage. Otherwise omitted properties remain unchanged. Inspect the newer
local schema and the saved labels, then render to check the final appearance.

## Separate axis-title styling (after RC4)

Each inspected `chart_format.axes[]` entry accepts `title_style`, independently of `text_style` for tick labels. For example: `{"id":"INSPECTED_AXIS_ID","title":"Revenue","title_style":{"font_size":21,"bold":true},"text_style":{"font_size":10}}`.
Omit `title` to style the existing title without replacing its text or formula reference. Styling requires an existing or nonempty new title; it fails if the title is removed. Supplied properties apply to its rich text and fallback text properties; omitted properties remain unchanged. Use a newer local build exposing this field, then render and inspect the title and tick labels.

## Chart text fills (after RC4)

Chart `text_style` and `title_style` accept `fill` using Paperworks' existing solid, gradient or pattern text-paint format. This styles glyphs, not the label box. For example:
```json
{"title_style":{"fill":{"gradient":{"angle":45,"stops":[{"position":0,"color":"FF0000"},{"position":1,"color":"0000FF"}]}}}}
```
Gradient stop positions are ascending fractions from 0 to 1; angles are degrees. The existing `color` field remains a solid-color shortcut. Supply `color` or `fill`, never both. Supplied fills replace existing run/default fills; omission preserves them. Image fills, shadows and outlines are not accepted inside text `fill`. Use a newer local build exposing this field and render to verify the result in the intended viewer.

## Picture corner radius (after RC4)

`slide-edit` `image-mask` accepts `corner_radius_points` for rectangular/rounded-rectangle pictures:
```json
{"edits":[{"operation":"image-mask","slide":1,"shape_id":"INSPECTED_ID","mask":"roundRect","corner_radius_points":18}]}
```
Radius is in points, from zero to half the shorter picture side; picture dimensions must be explicit. Setting a radius on `rect` converts it to `roundRect`. Omit `mask` to adjust the current rectangular mask. Omitted radius retains adjustments when the mask type stays the same; a different mask uses its defaults. Crop, placement and image bytes remain unchanged. Use a newer local build and render the result.

## Additional picture mask shapes (after RC4)

`image-mask` accepts native DrawingML preset shape names, including `hexagon`, `triangle`, `diamond`, `star5`, `heart` and `rightArrow`, as well as `rect`, `roundRect` and `ellipse`. These change the picture's clipping geometry without replacing its image data, crop or placement.
```json
{"edits":[{"operation":"image-mask","slide":1,"shape_id":"INSPECTED_ID","mask":"hexagon"}]}
```
Names are case-sensitive. Unknown names, `textbox` and `custom` are rejected. A new mask uses the preset's default adjustments; keeping the same mask preserves existing adjustments. `corner_radius_points` remains restricted to rectangular masks. Use a newer local build and render the result; RC4 supports only the original three mask shapes.

## Shape corner radii and adjustments (after RC4)

Use a newer build exposing `shape_geometry`. For an existing ordinary shape:
`{"operation":"shape-geometry","slide":1,"shape_id":"INSPECTED_ID","shape_geometry":{"corner_radius_points":18}}`.
New text/shape elements accept the same `shape_geometry` object. Radius requires
rect/roundRect and explicit dimensions, from zero to half the shorter side.
Optional `preset` changes the native preset. For exact preset adjustments, use
`adjustments:[{"name":"adj","formula":"val 20000"}]` instead of radius.
Use adjustment names/formulas appropriate to that preset. A supplied list replaces
all adjustments; `[]` clears them. Omission retains same-preset adjustments;
changing the preset resets them. Text, fills and placement remain unchanged.
Inspect and render the saved slide, including text fit after changing geometry.

## Picture aspect-ratio locking (after RC4)

Use a newer build exposing `lock_aspect_ratio`:
`{"operation":"image-lock","slide":1,"shape_id":"INSPECTED_PICTURE_ID","lock_aspect_ratio":true}`.
Use `false` to unlock. New image elements accept the same field. It preserves
placement, crop, image bytes and other locks. This is a native interactive-resize
setting; it does not constrain subsequent CLI geometry edits. Inspect saved
properties and verify interactive behavior in the target application when needed.

## Native table style options (after RC4)

Use a newer build exposing `table_format.style_options`:
`{"operation":"table-format","slide":1,"shape_id":"INSPECTED_TABLE_ID","table_format":{"style_options":{"header_row":true,"total_row":false,"banded_rows":false,"banded_columns":true}}}`.
Also accepts `first_column` and `last_column`. New table elements accept the same
`table_format`. Explicit false disables a flag; omission retains it. These flags
apply regions of the existing native table style without changing its ID, cells
or direct formatting. Direct cell formatting may override the visual effect.
Total-row styling does not add a row or calculate totals. Reopen and render to
check the requested appearance before delivery.

## Select a native table style (after RC4)

With a newer build exposing `table_format.style_id`, set a known built-in or
existing deck style GUID:
`{"operation":"table-format","slide":1,"shape_id":"INSPECTED_TABLE_ID","table_format":{"style_id":"{5940675A-B579-460E-94D1-54222C63F5DA}"}}`.
New table elements accept the same `table_format`. This replaces an inline style
if present and retains region flags and direct formatting. It does not import
custom style definitions; use the existing style resources mechanism for those.
Omitting `style_id` preserves the current style. Direct cell formatting can mask
the style's appearance, so render and inspect the result.

## Mixed-format text in table cells (after RC4)

Use a newer build exposing structured table-cell paragraphs:
`{"operation":"table-cell","slide":1,"shape_id":"INSPECTED_TABLE_ID","row":0,"column":0,"paragraphs":[{"runs":[{"text":"Revenue:","bold":true},{"text":" $200","bold":false}]}]}`.
Use `paragraphs` instead of `text`. For creation or `table-format`, put the same
`paragraphs` on the selected zero-based `table_format.cells` entry. This replaces
all text paragraphs in that cell; an empty array clears it. Omission preserves
text during table formatting. Use merged-cell origins. Borders, margins, body
properties and unrelated cells remain unchanged.
Runs accept bold, italic, underline, font size and color. Set `font` on each
run for mixed typefaces, for example `{"text":"Code","font":"Courier New"}`;
omitting it uses the default font. Paragraphs accept
bullet/level and `format`; named run styles are not supported in this path.
Unspecified authored text defaults to Arial 12pt and the helper's text color;
specify intended sizes/colors when matching existing content. Render and inspect
wrapping and row height; writing rich text does not automatically expand the row.

## Arrowhead sizing (after RC4)

In a newer build, `drawing_style.outline` accepts `head_width`, `head_length`,
`tail_width`, `tail_length`, each `sm`, `med` or `lg`. Example outline:
`{"color":"17324D","width":2,"tail":"arrow","tail_width":"lg","tail_length":"sm"}`.
Supply the corresponding head/tail type. Use this on created drawing elements or
in `slide-edit` operation `drawing-style`. An outline replaces existing line
formatting, so supply all properties you want retained. Omitted sizes use viewer
defaults. Attachments remain unchanged. Render and check arrow direction and size.

## Line caps and joins (after RC4)

With a newer build, use `drawing_style.outline.cap` (`flat`, `round`, `square`)
and `join` (`round`, `bevel`, `miter`). Example outline:
`{"color":"17324D","width":3,"cap":"round","join":"bevel"}`.
These work for creation and `drawing-style` edits on supported drawing objects.
An outline replaces existing line formatting: include desired dash and arrowhead
settings as well. Omission uses viewer defaults. Connector routing/attachments
stay unchanged. Render to inspect the ends and corners in the intended viewer.

### Table-run underline styles (after RC4)

Use `underline_style` instead of boolean `underline` for a native DrawingML
style: `{"text":"Total","underline_style":"dbl"}` gives double underline.
Accepted values: `none`, `words`, `sng`, `dbl`, `heavy`, `dotted`,
`dottedHeavy`, `dash`, `dashHeavy`, `dashLong`, `dashLongHeavy`, `dotDash`,
`dotDashHeavy`, `dotDotDash`, `dotDotDashHeavy`, `wavy`, `wavyHeavy`, `wavyDbl`.
The boolean `underline: true` still means single underline.

### Hyperlinks within table-cell text (after RC4)

Set `hyperlink` on the intended run, for example
`{"text":"Source","hyperlink":"https://example.com","underline_style":"sng","color":"2563EB"}`.
This works in `table-cell.paragraphs` and in `table_format.cells[].paragraphs`
for creation and `table-format`. Links accept HTTP, HTTPS and mailto URLs.
The link is attached only to that run; set its color and underline explicitly
if desired. Replacing the paragraphs without a hyperlink removes the link from
the replacement text. This does not change the other cells.
