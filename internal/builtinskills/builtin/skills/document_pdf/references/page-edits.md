## Page operations

Page numbers are one-based. Use a fresh output file/directory for each command. Adjust these ranges to the inspected page count:

```sh
"$PAPERWORKS" pdf-select input.pdf --pages 3,1-2 --output reordered.pdf
"$PAPERWORKS" pdf-rotate input.pdf --degrees 90 --pages 1 --output rotated.pdf
"$PAPERWORKS" pdf-merge first.pdf second.pdf --output combined.pdf
"$PAPERWORKS" pdf-split input.pdf --ranges "1-2;3" --output split-pages
"$PAPERWORKS" pdf-watermark input.pdf --text DRAFT --pages 1 --output draft.pdf
```

Omit `--ranges` to split into individual pages. Omit `--pages` for rotation/watermarking of all pages. Reopen and render the changed pages; verify order/count and that unrelated content remains.

### Editing example

After the small authoring example creates `example.pdf`, this stamps its first page while preserving the original:

```sh
"$PAPERWORKS" inspect example.pdf > before.json
"$PAPERWORKS" pdf-watermark example.pdf --text DRAFT --pages 1 --output draft.pdf
"$PAPERWORKS" inspect draft.pdf > after.json
"$PAPERWORKS" pdf-text draft.pdf --output draft-text.txt
"$PAPERWORKS" render draft.pdf --output draft-preview
```

Check that the source text and page count remain and the DRAFT stamp appears in the preview.

## Positioned extraction and page-object edits

`"$PAPERWORKS" pdf-layout input.pdf --spec selection.json` returns characters with bounding boxes and optional table cells. For example, `{"pages":[1],"bounds":[36,72,576,720],"columns":[36,300,576],"rows":[72,120,168],"max_chars":20000}` selects one-based pages and explicitly bounded cells. Coordinates are PDF points from the **bottom-left**. Both row and column boundaries must be strictly ascending with at least two values each; returned table rows are top-to-bottom. Use boundaries from the actual page, not these sample coordinates. Select smaller regions if the character limit is reached. Scanned artwork still needs image inspection or an external OCR tool.

`"$PAPERWORKS" pdf-objects input.pdf` lists top-level objects with page, index, type and bounds. Types are 1 text, 2 path, 3 image, 4 shading, 5 form. Render and inspect before selecting indices; a visual object may consist of several PDF objects. Nested artwork within a form is handled as one form, not as separate semantic objects.

Run `"$PAPERWORKS" pdf-object-edit input.pdf --spec edits.json --output edited.pdf` with, for example:

```json
{"edits":[{"page":1,"remove":[3,4],"overlay_pdf":"replacement.pdf","overlay_page":1,"matrix":[1,0,0,1,0,0]}]}
```

Page numbers are one-based; object indices come from inspection and refer to the page before each edit. Omit `remove` for a pure overlay. The PDF transform is `[a,b,c,d,e,f]` and defaults to identity; overlay paths resolve relative to the spec. This preserves vector artwork but provides neither semantic “replace the robot” detection nor secure redaction. Compare the edited region and surrounding titles/captions with the source.

For canvas fields used in this example, see `references/creation.md`.

### General editing example

Starting from the small authoring example's `example.pdf`, create an embedded-font annotation and overlay it without deleting original objects:

```sh
cat > annotation.json <<'JSON'
{"canvas":{"pages":[{"width":595.28,"height":841.89,"elements":[
 {"type":"text","x":36,"y":780,"text":"Café — Δοκιμή","font_size":14}
]}]}}
JSON
"$PAPERWORKS" create-pdf --spec annotation.json --output annotation.pdf
cat > overlay.json <<'JSON'
{"edits":[{"page":1,"overlay_pdf":"annotation.pdf","overlay_page":1}]}
JSON
"$PAPERWORKS" pdf-objects example.pdf > original-objects.json
"$PAPERWORKS" pdf-object-edit example.pdf --spec overlay.json --output annotated.pdf
cat > layout.json <<'JSON'
{"pages":[1],"max_chars":20000}
JSON
"$PAPERWORKS" pdf-layout annotated.pdf --spec layout.json > annotated-layout.json
"$PAPERWORKS" pdf-text annotated.pdf --output annotated-text.txt
"$PAPERWORKS" preview-export annotated.pdf --output annotated-preview --format webp --columns 1
```

Check that the original revenue text and added Unicode text survive extraction, then inspect placement and glyphs in the preview.

