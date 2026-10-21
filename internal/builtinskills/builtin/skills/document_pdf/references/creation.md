## Embedded fonts and vector creation

Use `canvas.pages` with explicit page `width/height` and `elements`. Coordinates are points from the **top-left**, unlike `pdf-layout` extraction and the legacy fixed-page schema. Elements support `text`, `rect`, `ellipse`, `line`, `polygon`, `bezier`, `image`. Text uses `x/y/text/font/font_size`; boxes/images use `x/y/w/h`, with a local `path` for images. Vector styles use six-digit hex `fill/stroke` and point-valued `line_width`; polygons use `points`, cubic Bézier curves use four points.

The default embedded Go font covers Latin, Greek and Cyrillic. Map font aliases to local TTF files in `canvas.fonts`, then select `font` on text elements for other glyphs. Check glyph coverage in the rendered page.

## Creation options

`flow.blocks` supports `heading`, `paragraph`, `table`, `figure`, `spacer`, `page_break`. A figure is `{"kind":"figure","src":"chart.png","width":120,"height":60,"caption":"Revenue by category"}`; dimensions are points (72 per inch) and the local path resolves relative to the spec. Set dimensions to the image's aspect ratio. Top-level `paper` selects page size; `flow.margin` and `flow.leading` are points and `flow.font_size` is in points. Fixed `pages` uses a separate schema: `text`, `images`, `text_fields`, `checkboxes`, `radio_groups` and `combo_boxes` arrays. Do not pass flow-block keys to fixed pages. Use the tested form example in `references/forms.md` for basic fields; inspect existing forms before filling them.

## Small authoring example

Adapt this example to the requested content and layout.

```sh
cat > example.json <<'JSON'
{
  "paper": "A4",
  "flow": {
    "blocks": [
      {
        "kind": "heading",
        "text": "Revenue report"
      },
      {
        "kind": "paragraph",
        "text": "Revenue totaled $200 across subscriptions and services."
      },
      {
        "kind": "table",
        "header": [
          "Item",
          "Amount"
        ],
        "rows": [
          [
            "Subscriptions",
            "$120"
          ],
          [
            "Services",
            "$80"
          ],
          [
            "Total",
            "$200"
          ]
        ],
        "column_widths": [
          75,
          25
        ]
      }
    ]
  }
}
JSON
"$PAPERWORKS" create-pdf --spec example.json --output example.pdf
```

