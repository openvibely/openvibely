## Creation options

`create-docx` creates a new file; it does not patch an existing template. Top-level `header`, `footer` and `page_numbers` add those elements. `page` accepts `width_in`, `height_in`, `margin_in` and `landscape`. A paragraph can use `runs` instead of `text`, for example `{"type":"paragraph","runs":[{"text":"Total: ","bold":true},{"text":"$200","color":"17324D","size":12}]}`. Runs also accept `italic`, `underline` and `link`. An image block is `{"type":"image","path":"chart.png","width_in":4,"description":"Revenue by category"}`; image paths resolve relative to the spec. Lists use `{"type":"list","items":["First","Second"],"ordered":true}`. Keep tables within usable page width (`widths_twips`; 1440 twips per inch).

### Styles and table layout example

For new documents, `styles_file` replaces the complete styles part; supply a complete WordprocessingML `<w:styles>` resource with all styles you need. `styles_overlay_file` instead replaces/adds the specified definitions in the built-in template, keeping unmentioned definitions. The two options are mutually exclusive. An overlay replaces a whole selected style definition, not individual properties. Its styles should include appropriate `basedOn`/font settings; top-level `font`/`font_size` do not override that overlay's definitions.

`theme_file` accepts a complete DrawingML `<a:theme>` XML file, not a `.docx`/`.dotx` path or a JSON palette. Resource paths resolve relative to the JSON spec. A supplied Office package is a ZIP: use the available archive tools to read `word/styles.xml` and the related theme part into local XML files if reusing those resources. Inspect dependencies before using custom styles that refer to numbering or other resources. For an existing document use the `style-pack` edit documented in `references/review-and-fields.md` instead of recreating its body.

```sh
cat > report-styles.xml <<'XML'
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:style w:type="paragraph" w:styleId="ReportLead">
    <w:name w:val="Report Lead"/><w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:after="160"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial"/><w:b/><w:color w:val="17324D"/><w:sz w:val="28"/></w:rPr>
  </w:style>
</w:styles>
XML
cat > styled-table.json <<'JSON'
{
  "styles_overlay_file":"report-styles.xml",
  "blocks":[
    {"type":"paragraph","style":"ReportLead","text":"Revenue summary"},
    {"type":"table","rows":[["Item","USD"],["Subscriptions","120"],["Services","80"]],
     "style":"TableGrid","widths_twips":[5400,2520],"autofit":false,
     "header":true,"header_bold":true,"cell_margin_twips":120,
     "cell_align":[["left","right"],["left","right"],["left","right"]]}
  ]
}
JSON
"$PAPERWORKS" create-docx --spec styled-table.json --output styled-table.docx
"$PAPERWORKS" inspect styled-table.docx
"$PAPERWORKS" render styled-table.docx --output styled-table-preview
```

`style` on a paragraph is a style **ID**, such as `ReportLead`, not its display name. OOXML font sizes are half-points (`28` above means 14 pt); XML spacing and table widths/padding are twips (1440 per inch). For another document with complete exported resources, replace `styles_overlay_file` with `"styles_file":"styles.xml"` and optionally add `"theme_file":"theme.xml"`.

Table `widths_twips` needs one width per column; keep the sum inside the usable section width. `autofit:false` emits a fixed layout. `autofit:true` permits automatic sizing, so the application may change final column widths. `cell_align` is a row/column matrix of `left`, `center`, `right` or `justify`; it controls horizontal paragraph alignment, not vertical alignment. Omitted entries use the default. `cell_margin_twips` applies on all four sides; omitting it or setting zero uses the helper's 100-twip default. `header:true` repeats the first row and `header_bold:true` bolds its text; set `header:false` for a table with no header. `style` selects a defined table style, defaulting to `TableGrid`. Arbitrary merged cells, vertical alignment and per-cell fills are not fields in this table creation schema. Inspect the rendered table for reflow and padding before publishing.

## Small authoring example

Adapt this example to the requested content and layout.

```sh
cat > example.json <<'JSON'
{
  "page": {
    "width_in": 8.5,
    "height_in": 11,
    "margin_in": 1
  },
  "font": "Arial",
  "font_size": 11,
  "blocks": [
    {
      "type": "heading",
      "level": 0,
      "text": "Revenue report"
    },
    {
      "type": "paragraph",
      "text": "Revenue totaled $200 across subscriptions and services."
    },
    {
      "type": "heading",
      "level": 1,
      "text": "Breakdown"
    },
    {
      "type": "table",
      "rows": [
        [
          "Item",
          "Amount"
        ],
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
      "header_bold": true,
      "widths_twips": [
        4680,
        4680
      ],
      "cell_margin_twips": 100
    }
  ]
}
JSON
"$PAPERWORKS" create-docx --spec example.json --output example.docx
```

