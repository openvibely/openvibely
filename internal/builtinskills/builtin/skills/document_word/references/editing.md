## General edits to an existing document

Use `word-edit` with the operations below. `paragraph`, `run`, `table`, `row`, `column`, `image` and `section` are zero-based. Body paragraph indices exclude table paragraphs; a title counts. Inspect before selecting. Optional `part` targets an existing header/footer XML part for paragraph, table and image edits. Paths resolve relative to the JSON spec.

| Operation | Fields and behavior |
|---|---|
| `replace-text` | `paragraph`, `old`, `new`, optional `occurrence` (default 0). Replaces one match across consecutive ordinary runs; preserves surrounding formatting. Does not flatten fields, hyperlinks, revisions or annotation boundaries. |
| `run-format` | `paragraph`, `run`; optional `font`, `font_size` (points), `bold`, `italic`, `strike`, `color` (six hex digits), `style`. |
| `paragraph-format` | `paragraph`; optional `style`, `alignment` (`left`, `right`, `center`, `both`), `before`, `after`, `line` (twips). |
| `paragraph-insert` / `paragraph-delete` | `paragraph`; insertion also uses `position:before/after` and `text`. Delete refuses section-break or directly annotated paragraphs. |
| `table-cell-text` / `table-cell-format` | `table`, `row`, `column`; text uses `text`, replacing the cell's content. Format uses `fill` and paragraph/run formatting fields. |
| `table-row-insert/delete` / `table-column-insert/delete` | `table` and `row` or `column`; insert a blank row/column before the selected index. Requires an unmerged rectangular table. |
| `table-merge` / `table-unmerge` | `table`, `row`, `column`; merge also needs `end_column`, retains text and handles horizontal spans only. |
| `image-insert` | `paragraph`, local PNG/JPEG `path`, optional `width_in`, `height_in`, `description`. |
| `image-replace` / `image-resize` / `image-delete` | `image`; replace needs local PNG/JPEG `path`, resize needs `width_in` and/or `height_in`. Replacement uses a private media part. Image indices count standalone pictures in the selected part, excluding charts and grouped artwork. |
| `section-format` | `section`; page `width`, `height`, margins `top`, `bottom`, `left`, `right`, `header`, `footer`, `gutter`, all in twips (1440 per inch). |
| `section-insert` | Source `section`, terminating `paragraph`, `type:nextPage/continuous/evenPage/oddPage`. |
| `header-footer` | `section`, `kind:header/footer`, `type:default/first/even`, `text`. Check inherited headers/footers on affected sections. |
| `batch` | `edits` array of these general operations. Indices reflect earlier edits. No nested batches or specialized review operations inside a batch. |

### General editing example

After the small authoring example creates `example.docx`, change one phrase without tracking or accepting existing revisions, and format that paragraph:

```sh
cat > ordinary-edit.json <<'JSON'
{"operation":"batch","edits":[
 {"operation":"replace-text","paragraph":1,"old":"Revenue totaled","new":"Recorded revenue totaled","occurrence":0},
 {"operation":"paragraph-format","paragraph":1,"after":160}
]}
JSON
"$PAPERWORKS" word-edit example.docx --spec ordinary-edit.json --output ordinary-edit.docx
"$PAPERWORKS" inspect-select ordinary-edit.docx --search "Recorded revenue" --max-items 50
"$PAPERWORKS" render ordinary-edit.docx --output ordinary-preview
```

Verify the paragraph changed, the table and unrelated text remain, and no tracked replacement was introduced. Use `redline` for tracked changes.

