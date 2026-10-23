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
| `header-footer` | `section`, `kind:header/footer`, `type:default/first/even`, `text` or the table specification below. Check inherited headers/footers on affected sections. |
| `header-footer-link` | `section`, `kind:header/footer`, optional `type:default/first/even`, boolean `linked`; see linkage below. |
| `batch` | `edits` array of these general operations. Indices reflect earlier edits. No nested batches or specialized review operations inside a batch. |

### Find the paragraph before editing

`inspect` has no `.paragraphs` field. For a document with no table or nested paragraphs, read the reported IDs and text rather than using JSON array positions:

```sh
"$PAPERWORKS" inspect input.docx > inspection.json
jq '.direct_formatting[] | {part, paragraph, text}' inspection.json
```

`direct_formatting` only includes paragraphs with direct formatting, so it is not a complete paragraph inventory. `/direct_formatting/16/text` means array entry 16, not paragraph 16; read that entry's `paragraph` field. `stories[].content` contains text fragments, not editable paragraph IDs.

Inspection numbers all descendant paragraphs in each part, including table paragraphs. General edits instead count direct `w:p` children of `w:body` (or of the selected header/footer root). If the part contains tables/nested paragraphs, or the target is absent from `direct_formatting`, inspect the DOCX ZIP's XML and enumerate those direct children from zero, including empty paragraphs. Select `run` by counting the target paragraph's direct `w:r` children. Do not derive edit IDs from search-result positions.

### Replace repeated matches in a batch

Each edit sees the result of earlier edits. For an inspected paragraph 1 containing exactly two ordinary-text matches of `Paver`, both edits below use occurrence 0: after the first replacement, the remaining match becomes the first match.

```json
{"operation":"batch","edits":[
 {"operation":"replace-text","paragraph":1,"old":"Paver","new":"Finn","occurrence":0},
 {"operation":"replace-text","paragraph":1,"old":"Paver","new":"Finn","occurrence":0}
]}
```

Run the spec with `word-edit INPUT --spec SPEC.json --output OUTPUT.docx`. Use the actual paragraph and match count. This repeated-zero pattern assumes `new` does not contain `old`; otherwise inspect between edits or replace matches from highest occurrence to lowest. Search the output for remaining old text and verify the new text before rendering.

### Change a run's font

For an inspected paragraph 1, run 0:

```json
{"operation":"run-format","paragraph":1,"run":0,"font":"Arial","font_size":11}
```

Run with `word-edit` as above. `font_size` is in points. Paperworks sets the XML font attributes and removes conflicting theme-font bindings; no separate XML patch is needed. This changes only the selected direct run, not the entire paragraph or its style. Use an available font and render to check substitution and reflow.

### Replace or clear header text

To replace the entire default header for section 0:

```json
{"operation":"header-footer","section":0,"kind":"header","type":"default","text":"Updated report title"}
```

Set `text` to `""` to replace it with an empty header. This replaces all content in that header, including tables and images; it does not clear just one paragraph. Later sections that inherit it can change too (see linkage below).

For a narrow text edit, preserve the header's other content by targeting its inspected part and direct paragraph instead:

```json
{"operation":"replace-text","part":"word/header1.xml","paragraph":0,"old":"Draft","new":"Final","occurrence":0}
```

Use the actual part path and indices. An empty `new` removes only that match and retains the paragraph. Do not delete/recreate a whole header for a narrow replacement; check which sections share the edited part.

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


## Header and footer tables

`header-footer` accepts `rows` instead of `text`. Use `widths_twips` for column widths.

```json
{"operation":"header-footer","section":0,"kind":"footer","type":"default",
 "rows":[["Left","Middle","Right"]],"widths_twips":[2000,2000,2000]}
```

Run with `word-edit` as above. The zero-based section receives a private header/footer part containing a native table; this replaces its selected header/footer without changing the body. Later sections that inherit it also display the replacement. Inspect section references before editing. A private part does not break inheritance: for a section-only edit, preserve later sections' original headers/footers with explicit references before replacing the selected one. Use `kind:header` for a header and `type:first/even` when requested. Rows must be rectangular strings; provide one positive column width in twips (1440 per inch), within the section's usable width. Text and rows are mutually exclusive. Inspect the saved part/section relationship and apply existing table formatting operations with its `part` path if needed. Render all pages to check header/footer placement and page variants.


## Link to the previous section

Check `schema word-edit` for `header-footer-link` and the boolean `linked` field.

```json
{"operation":"header-footer-link","section":1,"kind":"footer","type":"default","linked":false}
```

Run with `word-edit` as above, or include it in a general `batch`. Sections are zero-based. `linked:false` gives an inherited header/footer an **empty** independent definition; it does not copy inherited content. If already independent, its content stays unchanged. `linked:true` restores inheritance from the nearest preceding definition of that kind/type; without one, it displays nothing. Other sections' explicit references and their parts remain intact.

Inspect the target and following sections before changing linkage: later sections that inherit from the target can change appearance too. To keep their existing content, retain their original definitions explicitly; do not treat unlinking as a content-copy command. Linkage does not enable first/even-page display flags. Do not supply `text`, `rows`, `widths_twips` or a header/footer `part`; use `header-footer` separately to replace content. Verify effective references and render affected pages.
