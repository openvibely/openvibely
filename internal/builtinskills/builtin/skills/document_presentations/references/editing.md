## Edit an existing deck

Run `"$PAPERWORKS" inspect input.pptx` first and identify the target slide and its shape ID. Render and view affected source slides before editing to establish their layout and content. If the report lacks run indexes, inspect that slide's XML with the available file tools. IDs are slide-local and are not the displayed shape names. Edit the supplied deck with `slide-edit`/`deck-edit`; rebuilding from extracted text can discard masters, animations and embedded media.

Save `{"edits":[...operations...]}` and run `"$PAPERWORKS" slide-edit input.pptx --spec edits.json --output edited.pptx`. Operations run in array order. `slide` starts at **1**; `paragraph`, `run`, `occurrence`, `row` and `column` start at **0**. Pass `shape_id` as a string. These sample IDs must be replaced with actual IDs from the source:

| Change | Operation inside `edits` |
|---|---|
| Substring in one paragraph | `{"operation":"replace-text","slide":1,"shape_id":"2","paragraph":0,"match":"Old","occurrence":0,"text":"New"}` |
| One ordinary text run | `{"operation":"run-text","slide":1,"shape_id":"2","paragraph":0,"run":0,"text":"New wording"}` |
| Entire paragraph | `{"operation":"text","slide":1,"shape_id":"2","paragraph":0,"text":"New paragraph"}` |
| Table cell | `{"operation":"table-cell","slide":1,"shape_id":"4","row":1,"column":1,"text":"$180"}` |
| Existing notes body | `{"operation":"notes","slide":1,"text":"Updated speaker notes"}` |

Prefer `replace-text` or `run-text` for narrow changes. `replace-text` can span adjacent ordinary runs, but not fields or line-break boundaries. `text` retains paragraph/first-run styling but replaces its contents, flattening mixed formatting and fields. Those three operations do not accept newlines. `table-cell` and plain-text `notes` replace the whole body, using its first paragraph as the formatting prototype; they can contain newlines. `notes` creates a missing notes part when needed; an existing notes part must have one body placeholder. Reopen the output and verify notes text and relationships.

## Object and deck editing

Use `slide-edit` for changes inside existing slides. Geometry is in inches; rotation is degrees; font sizes are points. Keep IDs from inspection. To edit an existing master/layout object, provide its exact XML `part` instead of `slide`; inspect all slides affected by shared layout/theme changes. Notes apply to slides only.

| `slide-edit` operation | Additional fields |
|---|---|
| `geometry` | Optional `x`, `y`, `w`, `h`, `rotation`, `flip_h`, `flip_v`. If geometry is inherited, supply explicit `x/y/w/h` to retain the intended bounds. |
| `delete` / `front` / `back` | Selected `shape_id`; removes or reorders the object. |
| `run-format` | `paragraph`, `run`; optional `font`, `font_size`, `bold`, `italic`, `underline`, `color`, `hyperlink` (http/https/mailto; empty removes). |
| `paragraph-format` | `paragraph`, optional `align:l/r/ctr/just` and `paragraph_style` with `align`, `before`, `after`, `indent`, `margin_left` in points, `level:0..8`, `bullet`, single-character `bullet_char`. |
| `fill` | `fill` as six hex digits; empty clears. |
| `replace-image` | Local PNG/JPEG `path`; creates a private media relationship. |
| `crop` | `crop:{"left":0.1,"right":0,"top":0,"bottom":0}`; fractions in [0,1), with each pair summing to less than 1. |
| `image-mask` | RC5 supports preset mask shapes and `corner_radius_points`; see [advanced controls](advanced-slide-controls.md#additional-picture-mask-shapes-after-rc4). |
| `table-row-insert/delete` / `table-column-insert/delete` | `row` or `column`; requires an unmerged table. |
| `table-cell-format` | `row`, `column`, optional `fill`, `font_size`, `bold`, `color`. |
| `layout` | `layout:{"shape_ids":["2","4"],"mode":"row","x":1,"y":1,"width":10,"height":3,"gap":0.2}`; modes row/column/grid, `columns` for grid. Positions **and resizes** objects into equal cells. |
| `drawing-style` | `drawing_style` with solid `fill`/`opacity` or `gradient`, plus `outline` and `shadow`; see the example below. A new shadow replaces existing shape effects. |

A drawing-style operation can use:

```json
{"operation":"drawing-style","slide":1,"shape_id":"2","drawing_style":{
 "gradient":{"angle":90,"stops":[{"position":0,"color":"2463EB"},{"position":1,"color":"FFFFFF"}]},
 "outline":{"color":"172B4D","width":2,"dash":"dash","tail":"triangle"},
 "shadow":{"color":"000000","opacity":0.25,"blur":4,"distance":2,"direction":45}
}}
```

Opacity/gradient positions are 0..1, angles are degrees, outline width and shadow blur/distance are points. Inspect placement, crop, aspect ratio, inherited formatting and relationships before/after image or object edits.

Run `"$PAPERWORKS" deck-edit input.pptx --spec deck.json --output new.pptx` with `{"edits":[...]}` for these structural edits:

| Operation | Fields |
|---|---|
| `add-slide` | `content` using the `create-pptx` slide schema; optional `position`. |
| `duplicate-slide` / `move-slide` / `delete-slide` | Source `slide`; optional insertion `position` for duplicate, required final `position` for move. Cannot delete the last slide. |
| `add-element` / `replace-element` | `slide`, native `element` using the creation schema; replacement also needs `shape_id` and a complete replacement spec. |
| `set-layout` | `slide`, existing layout XML `part`. |
| `replace-theme` | Existing theme XML `part`, local DrawingML theme `path`; affects every object using that theme. |

Slide numbers and positions are one-based and reflect preceding operations; omitted insertion positions append. Deleting slides/objects can leave orphaned ZIP parts; this is not a privacy scrub.

### General editing example

After creating `example.pptx`, duplicate the slide, then edit only the duplicate:

```sh
cat > deck-edit.json <<'JSON'
{"edits":[{"operation":"duplicate-slide","slide":1,"position":2}]}
JSON
"$PAPERWORKS" deck-edit example.pptx --spec deck-edit.json --output duplicated.pptx
cat > object-edit.json <<'JSON'
{"edits":[
 {"operation":"text","slide":2,"shape_id":"2","paragraph":0,"text":"Revenue detail"},
 {"operation":"geometry","slide":2,"shape_id":"2","x":0.8,"y":0.4,"w":11.5,"h":1},
 {"operation":"run-format","slide":2,"shape_id":"2","paragraph":0,"run":0,"font_size":28,"bold":true},
 {"operation":"notes","slide":2,"text":"Duplicate for detailed discussion."}
]}
JSON
"$PAPERWORKS" slide-edit duplicated.pptx --spec object-edit.json --output object-edited.pptx
"$PAPERWORKS" inspect-select object-edited.pptx --search "Revenue detail" --max-items 50
"$PAPERWORKS" preview-export object-edited.pptx --output object-previews --format jpeg --columns 2
```

Verify two slides, unchanged content on the first, and the intended title/geometry/notes on the second. Inspect both full-size previews and use the contact sheet for comparison. For another input, select shape IDs from its report.

### Editing example

Run the small authoring example first. Its title has shape ID `2` and table has ID `4`; inspect to confirm before adapting this to another deck. This example changes the title, makes the table currency explicit and updates the existing notes without rebuilding the chart or slide.

```sh
"$PAPERWORKS" inspect example.pptx > before.json
cat > edits.json <<'JSON'
{"edits":[
  {"operation":"replace-text","slide":1,"shape_id":"2","paragraph":0,"match":"generated","occurrence":0,"text":"contributed"},
  {"operation":"table-cell","slide":1,"shape_id":"4","row":1,"column":1,"text":"USD 120"},
  {"operation":"notes","slide":1,"text":"Subscriptions: USD 120; services: USD 80. Total: USD 200."}
]}
JSON
"$PAPERWORKS" slide-edit example.pptx --spec edits.json --output edited.pptx
"$PAPERWORKS" inspect edited.pptx > after.json
cat > requirements.json <<'JSON'
{"slides":1,"native_tables":[1],"native_charts":[1]}
JSON
"$PAPERWORKS" verify-slides edited.pptx --requirements requirements.json
"$PAPERWORKS" render edited.pptx --output edited-preview
```

Confirm the title, table-cell and notes changes, with unchanged chart data in `after.json`.

## Repair notes

If inspection finds a missing notes-master reference, run `"$PAPERWORKS" repair-notes draft.pptx --output final.pptx`. Ambiguous references are rejected. Check slide order/count, notes and expected text afterward.


## Extended editing operations

In `slide-edit`, table merges use `end_row`/`end_column`, formatted notes use `paragraphs`, nested layouts use `Layout.root`, and drawing styles use `pattern`/`image`.

Use the same `slide-edit` command and `{"edits":[...]}` wrapper as above. Replace sample IDs with inspected IDs.

### Table merge and unmerge

```json
{"edits":[{"operation":"table-merge","slide":1,"shape_id":"4","row":0,"column":0,"end_row":1,"end_column":1}]}
```

The rectangle is inclusive and zero-based. All selected text is retained in the top-left cell in row order, with paragraph/run formatting. Existing merged regions cannot overlap the selection. To split it, use `table-unmerge` with the same origin `row`/`column`, omitting end coordinates. Combined text remains at the origin; unmerge does not restore its original distribution. Edit merged-cell text through the origin. Row/column insertion and deletion still require an unmerged table. Reopen and inspect the merge and all retained text, then view the rendered table.

### Pattern and image fills

```json
{"edits":[
 {"operation":"drawing-style","slide":1,"shape_id":"2","drawing_style":{"pattern":{"preset":"diagCross","foreground":"16324F","background":"FFFFFF"}}},
 {"operation":"drawing-style","slide":1,"shape_id":"3","drawing_style":{"image":{"path":"texture.png","fit":"cover"}}}
]}
```

Patterns use DrawingML preset names, such as `diagCross`, `pct50`, `cross`, `smCheck`, `horzBrick`, `weave` or `zigZag`, and six-digit RGB colors. Image fills embed PNG/JPEG files relative to the spec. `fit` is `stretch` (default), `cover` or `tile`; `cover` accepts the image `anchor`/`crop` fields from `references/creation.md`. Solid, gradient, pattern and image fills are mutually exclusive; outline and shadow may accompany a fill. Apply geometry/layout changes before cover fills so cropping uses final dimensions. Check the rendered crop, pattern and proportions.

### Formatted speaker notes

```json
{"edits":[{"operation":"notes","slide":1,"paragraphs":[
 {"runs":[{"text":"Opening: ","bold":true},{"text":"Explain the result.","italic":true}]},
 {"text":"Mention the caveat","bullet":true,"level":1,"format":{"after":6}}
]}]}
```

Use `paragraphs` instead of `text`. It replaces the notes body while preserving other placeholders. Runs accept bold, italic, underline, font size and color; paragraphs accept bullets, levels, alignment, spacing and indentation. An empty array clears the body. To append, preserve the existing paragraphs in the new specification. Inspect saved notes text and XML formatting; slide previews do not verify notes appearance.

### Nested layouts

```json
{"edits":[{"operation":"layout","slide":1,"layout":{
 "x":0.5,"y":0.5,"width":12,"height":6,
 "root":{"mode":"row","gap":0.3,"children":[
  {"shape_id":"2","width":3},
  {"mode":"column","gap":0.2,"children":[
   {"shape_id":"3","weight":1},{"shape_id":"4","weight":2}
  ]}
 ]}
}}]}
```

Use `root` instead of the flat layout's `shape_ids`, `mode`, `gap` and `columns`. Containers accept `row`, `column`, or `grid` with `columns`, plus `gap` and `padding`. Leaves name distinct top-level shapes. Dimensions and spacing are inches; positive `width`/`height` fix a node's size. Other row/column children divide remaining space by `weight` (default 1); grid cells have equal sizes. Fixed cross-axis sizes align top/left.

Layout moves and resizes shapes; it does not react to later edits. Use `fit_text` below for measured text height. Group children are rejected. Render afterward and adjust dimensions for any clipped or overflowing content.

### Fit text height and span grid cells

Use `LayoutNode.fit_text`, `column_span` and `row_span` for measured text and grid spans.

```json
{"edits":[{"operation":"layout","slide":1,"layout":{
 "x":0.5,"y":0.5,"width":4,"height":6,
 "root":{"mode":"column","gap":0.2,"children":[
  {"shape_id":"2","fit_text":true},{"shape_id":"3","fit_text":true}
 ]}
}}]}
```

`fit_text:true` sizes a text leaf's height at its allocated width; omit its fixed `height`. Column siblings follow the measured height and gap. Runs must have resolved, available fonts (`Go` faces are bundled for measurement). Measurement uses glyph advances, wrapping, mixed run sizes, text insets, point-based paragraph spacing and proportional line spacing. Unavailable fonts/glyphs, unresolved fields, rotated/vertical text, fixed-point line spacing, nonzero character spacing, percentage-based before/after paragraph spacing and overflow fail explicitly. Use explicit sizes for those cases; verify rendered text for font substitution and complex-script shaping. Rerun layout after text/width edits.

Grid children may use positive `column_span` and `row_span`, defaulting to 1. For a heading across two columns above two items, use `{"mode":"grid","columns":2,"children":[{"shape_id":"2","column_span":2},{"shape_id":"3"},{"shape_id":"4"}]}`. Items occupy free cells in row-major order; rows divide the frame equally and spans include internal gaps. Check both placement and text fit in the resulting preview.

## Line spacing

`paragraph_style.line_spacing` sets a line-height multiplier
(`1` is single spacing, `1.2` is 120%; supported range 0.00001–1000).
This differs from `before`/`after`, which are paragraph spacing in points.
Paperworks RC4 supports this field in `ParagraphStyle`, including
`slide-edit` paragraph formatting and new paragraph definitions.

Save an edit spec using the actual inspected shape ID and zero-based paragraph:

```json
{"edits":[{"operation":"paragraph-format","slide":1,"shape_id":"2","paragraph":0,"paragraph_style":{"line_spacing":1.2}}]}
```

Run `"$PAPERWORKS" slide-edit input.pptx --spec spacing.json --output spaced.pptx`.
New paragraph definitions accept the same setting in `format`. Reapply any
measured layout after changing spacing, then render and inspect wrapping and overflow.


## Native bullets and hanging indentation

```sh
cat > bullets.json <<'JSON'
{"slides":[{"elements":[{"type":"text","x":1,"y":1,"w":5,"h":4,"font_size":24,
 "paragraphs":[
  {"text":"Inspect the original document before choosing which content and formatting to change.","format":{"bullet":true,"margin_left":18,"indent":-9,"after":8,"line_spacing":1.2}},
  {"text":"Render the edited document and check wrapped lines, spacing and clipping before delivery.","format":{"bullet":true,"margin_left":18,"indent":-9,"after":8,"line_spacing":1.2}}
 ]}]}]}
JSON
"$PAPERWORKS" create-pptx --spec bullets.json --output bullets.pptx
"$PAPERWORKS" render bullets.pptx --output bullets-preview
```

Use native `bullet:true`, not a typed bullet character. `margin_left`, `indent` and paragraph spacing are **points**; the box coordinates are inches. Negative `indent` places the bullet to the left of the text. Open the PNG with `view_image` and verify wrapped continuation lines align with the text, not the bullet, and the last line stays inside the box. Existing paragraphs use the same settings under `paragraph_style` in `paragraph-format` edits; inspect their shape IDs and zero-based paragraph indices first.

## Connect shapes with attached arrows

```sh
cat > connectors.json <<'JSON'
{"slides":[{"elements":[
 {"type":"shape","shape":"rect","x":1,"y":2,"w":3,"h":1,"fill":"D9EAF7","text":"Source"},
 {"type":"shape","shape":"rect","x":6,"y":3,"w":3,"h":1,"fill":"D9EAF7","text":"Target"},
 {"type":"connector","x":4,"y":2.5,"w":2,"h":1,
  "connector":{"kind":"straight","start":{"shape_id":"2","site":3},"end":{"shape_id":"3","site":1}},
  "drawing_style":{"outline":{"color":"17324D","width":2,"tail":"triangle"}}}
]}]}
JSON
"$PAPERWORKS" create-pptx --spec connectors.json --output connectors.pptx
"$PAPERWORKS" inspect connectors.pptx
"$PAPERWORKS" render connectors.pptx --output connectors-preview
```

The two shapes in this new deck receive IDs `2` and `3`. For a supplied deck, inspect actual slide-local IDs; do not assume creation order or use shape names as IDs. These rectangles use site 3 (right) and site 1 (left); connection-site indices depend on shape geometry. `start`/`end` store native attachments, while the connector box supplies its initial geometry. `tail` puts the arrow at the end/target; `head` puts it at the start. Open the PNG with `view_image` to verify endpoints and direction. Check attachments in the saved package or presentation application as well; a line touching a shape is not proof of an attachment.

### Connector endpoints in builds after RC5

RC5 accepts `straight`, `elbow`, and `curved` with explicit endpoint site indices.
Newer builds also accept `elbow2`, `elbow3`, `elbow4`, and `elbow5` and expose
`slide-edit` operation `connector`:

```json
{"edits":[{"operation":"connector","slide":1,"shape_id":"4","connector":{
 "kind":"elbow4","start":{"shape_id":"2","side":"auto"},
 "end":{"shape_id":"3","side":"auto"}
}}]}
```
Check the runtime's schema before using this operation. Omitted endpoints/kind
retain their values. `side` accepts `top`, `left`, `bottom`, `right`, or `auto`
for unrotated, unflipped rectangles, rounded rectangles and diamonds; otherwise
supply an inspected `site`. Automatic selection faces the other endpoint's
center; it does not move the connector or avoid obstacles. Set its geometry
separately, inspect saved attachments and view the rendered result.

### Internal text links in builds after RC5

Structured text runs and `run-format` can use
`link:{"uri":"ppt/slides/slide2.xml","is_external":false}` instead of
`hyperlink`. Use an exact inspected slide part that exists. For external links,
use `is_external:true` with HTTP, HTTPS or mailto. Do not supply both fields.
An empty `link:{}` removes the link.

For navigation without a fixed slide target, use
`link:{"action":"ppaction://hlinkshowjump?jump=nextslide"}`. Supported jumps
also include `previousslide`, `firstslide`, `lastslide`, `endshow`, and
`lastslideviewed`; program/macro actions are rejected. Verify the runtime schema
first: RC5 does not expose `link`. Inspect saved relationships and check click
behavior in PowerPoint; a PNG does not verify interactive links.

### Add masters and layouts in builds after RC5

After confirming `schema deck-edit` exposes these fields, use:

```json
{"edits":[
 {"operation":"add-master","part":"ppt/slideMasters/brand.xml","name":"Brand"},
 {"operation":"add-layout","part":"ppt/slideLayouts/brand-title.xml","parent_part":"ppt/slideMasters/brand.xml","name":"Brand title",
  "placeholders":[{"type":"title","index":0,"x":1,"y":0.5,"w":10,"h":1}]},
 {"operation":"set-layout","slide":1,"part":"ppt/slideLayouts/brand-title.xml"}
]}
```
Choose unused part paths. `add-master` registers a master and blank layout;
`theme_part` selects an inspected existing theme (omission uses the first theme
by part name). `add-layout` registers a layout under `parent_part`.
Placeholder coordinates are inches and indices must be unique/nonnegative.
Types include `title`, `ctrTitle`, `subTitle`, `body`, `obj`, `pic`, `chart`,
`tbl`, `dt`, `ftr`, and `sldNum`.

Alternatively, `path` reads self-contained master/layout XML, including its
color map/background, instead of `placeholders`. Relationship-dependent assets
are rejected; a master resource's layout list is replaced with a registered
blank layout. Adding or assigning a layout does not relocate existing slide
content. Inspect native relationships and placeholders, then render every slide
affected by inheritance changes. These operations are not in pinned RC5.
