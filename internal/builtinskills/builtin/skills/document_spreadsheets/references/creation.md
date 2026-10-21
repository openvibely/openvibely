## Creation options

A new `create-xlsx` sheet can use `rows` for a grid and `cells` for overrides. Cell objects choose one of `value`, `formula` (without a leading `=`), or `date` (RFC3339, for example `{"date":"2026-01-01T00:00:00Z"}`). Apply a named date style to display a date instead of a serial. Keep identifiers such as `0012` as strings. `style` selects an entry from top-level `styles`.

Additional sheet fields include `merges:["A1:C1"]`, `row_heights:{"1":24}`, `pictures:[{"cell":"F2","path":"chart.png"}]`, `hidden`, `show_grid_lines`, and the formatting/chart fields in the example. Use local image paths relative to the spec; use merges only for presentation areas, not the data grid. Top-level `names` uses entries such as `{"Name":"UnitPrice","RefersTo":"Revenue!$C$2"}`. For existing workbooks, use the `workbook-edit` operations in `references/editing.md`.

### Dropdowns and workbook themes example

`theme_file` loads a complete DrawingML theme XML file for a new workbook. To reuse a supplied workbook's theme, extract its theme part with the available archive tools (commonly `xl/theme/theme1.xml`; check the workbook relationships) to `company-theme.xml` beside the spec. It must contain an `<a:theme>` in the DrawingML namespace, not an `.xlsx` filename or a JSON color palette. This copies theme resources only, not worksheets, cell styles or content. Explicit fonts/RGB formatting can override the theme; loading a theme does not install its fonts.

With that local `company-theme.xml`, this creates two dropdowns: an inline list in A2:A20 and a named-range list in B2:B20.

```sh
cat > dropdown-theme.json <<'JSON'
{
  "theme_file":"company-theme.xml",
  "names":[{"Name":"AllowedStatuses","RefersTo":"'Lists'!$A$1:$A$3"}],
  "sheets":[
    {"name":"Report","rows":[["Status","Review status"],["Draft","Draft"]],
     "columns":{"A:B":22},
     "validations":[
       {"Type":"list","Sqref":"A2:A20","Formula1":"\"Draft,Review,Approved\"",
        "AllowBlank":true,"ShowDropDown":false,"ShowErrorMessage":true,
        "ErrorStyle":"stop","ErrorTitle":"Choose a status","Error":"Use a listed status."},
       {"Type":"list","Sqref":"B2:B20","Formula1":"AllowedStatuses",
        "AllowBlank":true,"ShowDropDown":false,"ShowErrorMessage":true,
        "ErrorStyle":"stop","ErrorTitle":"Choose a status","Error":"Use a listed status."}
     ]},
    {"name":"Lists","hidden":true,"rows":[["Draft"],["Review"],["Approved"]]}
  ]
}
JSON
"$PAPERWORKS" create-xlsx --spec dropdown-theme.json --output dropdown-theme.xlsx
"$PAPERWORKS" inspect dropdown-theme.xlsx
"$PAPERWORKS" render dropdown-theme.xlsx --sheet Report --output dropdown-theme-preview
```

Inline `Formula1` includes literal double quotes around the comma-separated choices; JSON escapes those quotes. Use a named worksheet range for long lists (over Excel's 255-character inline-list limit), values containing commas, or a maintained list on another sheet. Keep the name's scope/range correct and expand it when choices grow. `ShowDropDown:false` keeps the arrow visible: this OOXML flag is inverted. `AllowBlank` controls whether blanks are allowed; a stop alert requires `ShowErrorMessage:true` and `ErrorStyle:"stop"`.

These are creation fields, not existing-workbook editing commands. Validate the saved rules, target ranges, named-range references and theme part. Inspect the rendered layout with `view_image`; a PNG cannot prove dropdown interaction or input rejection. Check those in the intended spreadsheet application when required. Validation is an editing aid, not a security boundary or proof that existing/pasted values are valid.

### Image sizing and positioning example

For a local 400 × 200 pixel `chart.png`, this places a 200 × 100 pixel image at B2, offset 8 pixels right and 6 pixels down. Use the actual source dimensions when calculating scale: `ScaleX = targetWidth/sourceWidth`, `ScaleY = targetHeight/sourceHeight`. Keep both scales equal to preserve proportions. `LockAspectRatio` controls subsequent resizing in Excel; it does not correct unequal initial scales.

```sh
cat > picture.json <<'JSON'
{"sheets":[{"name":"Report","rows":[["Quarterly revenue"]],
  "pictures":[{"cell":"B2","path":"chart.png","options":{
    "ScaleX":0.5,"ScaleY":0.5,"OffsetX":8,"OffsetY":6,
    "Positioning":"oneCell","LockAspectRatio":true,
    "AltText":"Revenue by quarter","Name":"Revenue chart"
  }}]}]}
JSON
"$PAPERWORKS" create-xlsx --spec picture.json --output picture.xlsx
"$PAPERWORKS" render picture.xlsx --sheet Report --output picture-preview
```

`options` uses the exact capitalized keys shown. `Positioning:"oneCell"` moves the image with its anchor cell without resizing it; `twoCell` moves and sizes with cells; `absolute` keeps its position and size. Offsets are pixels. Set `AltText` to describe the actual image. Image paths resolve relative to the spec.

To fit within the anchor cell (or its merged range), use `AutoFit:true` with both scales set to 1 and offsets 0; size that cell/range first. This shrinks oversized images while preserving proportions and does not enlarge smaller ones. `AutoFitIgnoreAspect:true` instead fills the cell and can distort the image. Open the returned PNG using `view_image` and check size, placement and clipping. These options apply to new workbook creation, not image edits in an existing workbook.

### Charts and named tables example

For new workbooks, `sheets[].charts` supports `bar`, `column`, `line`, `scatter`, `pie`, `doughnut`, `area`, `radar`, `bubble`, `stock-hlc` and `stock-ohlc`. These charts use `width`/`height` in pixels, unlike `column_charts`' EMU dimensions. Each series binds `Name`, `Categories` and `Values` to worksheet ranges; exclude the header from value ranges. For scatter charts, `Categories` supplies **numeric X values**. Pie/doughnut normally use one series and do not have axes. Chart types outside this list are not exposed by this creation command.

```sh
cat > chart-table.json <<'JSON'
{
  "sheets":[{
    "name":"Trend",
    "rows":[["Year","Revenue"],[2024,100],[2025,140],[2026,180]],
    "columns":{"A:B":16},
    "tables":[{"Range":"A1:B4","Name":"RevenueData","StyleName":"TableStyleMedium2","ShowHeaderRow":true,"ShowRowStripes":true}],
    "charts":[{
      "type":"line","cell":"D2","title":"Revenue by year (USD)","width":640,"height":360,
      "series":[{
        "Name":"'Trend'!$B$1","Categories":"'Trend'!$A$2:$A$4","Values":"'Trend'!$B$2:$B$4",
        "Fill":{"Type":"pattern","Pattern":1,"Color":["17324D"]},
        "Line":{"Width":2},
        "Marker":{"Symbol":"circle","Size":6,"Fill":{"Type":"pattern","Pattern":1,"Color":["17324D"]}}
      }],
      "x_axis":{"Title":{"Paragraph":[{"Text":"Year"}]},"NumFmt":{"CustomNumFmt":"0","SourceLinked":false}},
      "y_axis":{"Title":{"Paragraph":[{"Text":"Revenue (USD)"}]},"Minimum":0,"NumFmt":{"CustomNumFmt":"$#,##0","SourceLinked":false}}
    }]
  }]
}
JSON
"$PAPERWORKS" create-xlsx --spec chart-table.json --output chart-table.xlsx
"$PAPERWORKS" inspect chart-table.xlsx
"$PAPERWORKS" render chart-table.xlsx --sheet Trend --output chart-table-preview
```

To use another supported chart type with this example's numeric year/value data, change only `charts[0].type` to `bar`, `column`, `scatter`, `pie`, `doughnut` or `area`, then save to a fresh output. For pie/doughnut remove `x_axis`/`y_axis`, `Line` and `Marker`; set `vary_colors:true` for distinct slices. For bar/column/area remove `Marker`; choose series `Fill` deliberately. A line's stroke color comes from the series `Fill`; `Line.Width` controls its width. Solid fills require `Type:"pattern"`, `Pattern:1` and one color, including marker fills. Axis options include `Minimum`, `Maximum`, `MajorUnit`, `NumFmt` and `Title` as shown; omit bounds that the task does not call for. The delegated series/axis/table fields use the spelling shown here, not invented `x`, `y`, `axis_title` or `table_name` keys.

A `tables` entry creates a real named Excel table with filter/header metadata. Its `Range` includes a row of unique nonempty headers and at least one data row; `Name` must be a valid unique Excel table name. `ShowColumnStripes`, `ShowFirstColumn` and `ShowLastColumn` are additional boolean styling options. This does not add a table to an existing workbook; creation and narrow input editing have different schemas. Inspect the saved table range/name and chart source bindings, then open the PNG with `view_image`. Do not infer success from a formatted grid alone.

## Small authoring example

Adapt this example to the requested content and layout.

```sh
cat > example.json <<'JSON'
{
  "styles": {
    "header": {
      "Font": {
        "Color": "FFFFFF",
        "Bold": true
      },
      "Fill": {
        "Type": "pattern",
        "Pattern": 1,
        "Color": [
          "17324D"
        ]
      }
    },
    "currency": {
      "CustomNumFmt": "$#,##0.00;[Red]($#,##0.00)"
    },
    "negative": {
      "Font": {
        "Color": "C00000"
      }
    }
  },
  "sheets": [
    {
      "name": "Revenue",
      "rows": [
        [
          "Item",
          "Quantity",
          "Price",
          "Revenue"
        ],
        [
          "Subscriptions",
          3,
          40
        ],
        [
          "Services",
          2,
          40
        ],
        [
          "Total"
        ]
      ],
      "cells": {
        "D2": {
          "formula": "B2*C2"
        },
        "D3": {
          "formula": "B3*C3"
        },
        "D4": {
          "formula": "SUM(D2:D3)"
        }
      },
      "freeze": "B2",
      "columns": {
        "A": 24,
        "B:D": 14
      },
      "formats": [
        {
          "range": "A1:D1",
          "style": "header"
        },
        {
          "range": "C2:D4",
          "style": "currency"
        }
      ],
      "validations": [
        {
          "Type": "whole",
          "Operator": "greaterThanOrEqual",
          "Formula1": "0",
          "Sqref": "B2:B3",
          "ShowErrorMessage": true
        }
      ],
      "conditional": [
        {
          "range": "D2:D3",
          "style": "negative",
          "type": "cell",
          "criteria": "<",
          "value": "0"
        }
      ],
      "print_area": "A1:Q23",
      "page_layout": {
        "Orientation": "landscape",
        "FitToWidth": 1,
        "FitToHeight": 1
      },
      "show_grid_lines": false,
      "sheet_props": {
        "BaseColWidth": 8,
        "DefaultRowHeight": 15
      },
      "page_margins": {
        "Left": 0.75,
        "Right": 0.75,
        "Top": 1,
        "Bottom": 1,
        "Header": 0.5,
        "Footer": 0.5
      },
      "column_charts": [
        {
          "cell": "F2",
          "title": "Revenue by item",
          "value_title": "USD",
          "width_emu": 5400000,
          "height_emu": 2700000,
          "series": [
            {
              "name": "'Revenue'!D1",
              "categories": "'Revenue'!$A$2:$A$3",
              "values": "'Revenue'!$D$2:$D$3"
            }
          ]
        }
      ]
    }
  ]
}
JSON
"$PAPERWORKS" create-xlsx --spec example.json --output example.xlsx
```

