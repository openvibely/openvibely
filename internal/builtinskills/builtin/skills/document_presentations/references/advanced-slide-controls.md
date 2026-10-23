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
