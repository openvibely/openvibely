## Creation options

For `create-pptx`, positions `x`,`y`,`w`,`h` and slide dimensions are in inches; `font_size` is in points. Text elements accept `paragraphs`, for example `{"type":"text","x":1,"y":1,"w":8,"h":2,"font_size":24,"paragraphs":[{"runs":[{"text":"Revenue: ","bold":true},{"text":"$200","color":"17324D"}]},{"text":"Confirmed inputs","bullet":true,"level":0}]}`. Runs also accept `font_size`, `italic` and `underline`; use paragraph properties for bullets.

Image example: `{"type":"image","path":"chart.png","x":1,"y":2,"w":6,"h":3,"fit":"contain","description":"Revenue by category"}`. `fit` supports `contain` and `cover`; prefer `contain` for an entire image or `cover` for intentional cropping. Optional `anchor:{"x":0.5,"y":0.5}` chooses the visible region. Optional `crop:{"left":0.1,"top":0,"right":0.1,"bottom":0}` removes source-edge fractions in [0,1); opposite edges must sum to less than 1. Image paths resolve relative to the JSON spec. For existing images, use Object and deck editing in `references/editing.md`.

New native drawing elements also include `type:"connector"` with `connector.kind:straight/elbow/curved` and optional `start/end:{"shape_id":"2","site":1}` (zero-based connection site), and `type:"path"` with `commands` (`move`, `line`, `quadratic`, `cubic`, `close`). Paths use `[x,y]` points on a 0..100000 square scaled to the element bounds; start with `move`. A line needs one point, quadratic two and cubic three. New elements accept `drawing_style`; paragraph definitions accept `format` using the paragraph-style fields in `references/editing.md`. Use these for requested editable diagrams, not to replace supplied artwork.

For builds supporting the extensions in `references/editing.md`, created elements can also use pattern/image `drawing_style`, slides can use `notes_paragraphs` instead of `notes`, and slide `layout.root` can position the created shapes. Check `schema create-pptx` for those fields first; follow the availability rules and specifications in that reference. Creation applies layout before computing cover fills.

Chart creation supports bubble, radar, stock-hlc/stock-ohlc and area3D/bar3D/pie3D/line3D in addition to the existing types. Bubble series need numeric X `categories` and `sizes` alongside `values`. `options` supplies full Excelize settings, while type/series bindings come from the creation spec. Prefer undistorted 2-D charts unless 3-D is requested.

Top-level `style_resources` can specify `template_layout` (0..10 from pinned built-in layouts), or local XML paths for `theme`, `master`, `layout`, `table_styles`, `default_text`. Do not combine `template_layout` with custom `master`/`layout`. Resources must be self-contained supported XML without undeclared relationships; this is not a `.pptx` template filename. Use explicit `inherit_formatting:true` on supported elements/charts only when those resources should supply formatting.

### Editable shapes example

`create-pptx` accepts `type:"shape"` with `shape` set to `rect`, `roundRect`, `ellipse`, `triangle`, `chevron` or `rightArrow`. These are native editable shapes, not images. `fill` colors the shape; `color` colors its text (six-digit RGB without `#`). `align` is `l`, `ctr`, `r` or `just`, not `left`/`center`/`right`. Positions and dimensions are inches, font sizes are points.

```sh
cat > shapes.json <<'JSON'
{
  "width_in":10,"height_in":5.625,"font":"Arial",
  "slides":[{"background":"FFFFFF","elements":[
    {"type":"shape","shape":"roundRect","name":"Review stage","x":1,"y":2,"w":3,"h":1,
     "fill":"17324D","text":"Review","color":"FFFFFF","font_size":24,"align":"ctr"},
    {"type":"shape","shape":"rightArrow","name":"Next stage","x":4.3,"y":2.25,"w":1.2,"h":0.5,"fill":"527A9E"},
    {"type":"shape","shape":"ellipse","name":"Delivery stage","x":6,"y":2,"w":3,"h":1,
     "fill":"DCE8F2","text":"Deliver","color":"17324D","font_size":24,"align":"ctr"}
  ]}]
}
JSON
"$PAPERWORKS" create-pptx --spec shapes.json --output shapes.pptx
"$PAPERWORKS" inspect shapes.pptx
"$PAPERWORKS" render shapes.pptx --output shapes-preview
```

Inspect the native shape geometry, fills and editable text, then view the PNG. These options create new shapes. Use `slide-edit` from `references/editing.md` for later text, geometry (including rotation) and drawing-style changes. A `rightArrow` here is a standalone shape; use a connector element for an attached connector. Custom paths, outlines and shadows use the creation options above. For native review threads use `slide-comments` in `references/comments.md`; the `comments` command handles Excel threads.

## Small authoring example

Adapt this example to the requested content and layout.

```sh
cat > example.json <<'JSON'
{
  "width_in": 13.333,
  "height_in": 7.5,
  "font": "Arial",
  "slides": [
    {
      "notes": "Source: user-supplied revenue figures. Total: $200.",
      "elements": [
        {
          "type": "text",
          "x": 0.7,
          "y": 0.3,
          "w": 12,
          "h": 1,
          "text": "Subscriptions generated 60% of revenue",
          "font_size": 30
        },
        {
          "type": "chart",
          "x": 0.7,
          "y": 1.6,
          "w": 7,
          "h": 4.8,
          "chart": {
            "type": "column",
            "categories": [
              "Subscriptions",
              "Services"
            ],
            "series": [
              {
                "name": "Revenue (USD)",
                "values": [
                  120,
                  80
                ]
              }
            ]
          }
        },
        {
          "type": "table",
          "x": 8.2,
          "y": 2,
          "w": 4.3,
          "h": 2,
          "font_size": 18,
          "rows": [
            [
              "Item",
              "Revenue"
            ],
            [
              "Subscriptions",
              "$120"
            ],
            [
              "Services",
              "$80"
            ]
          ]
        }
      ]
    }
  ]
}
JSON
"$PAPERWORKS" create-pptx --spec example.json --output example.pptx
```

