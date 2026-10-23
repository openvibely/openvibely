## Executable Word operations

Invoke `"$PAPERWORKS" word-edit input.docx --output output.docx --spec operation.json`; the output must not exist. Use the following operations for tracked changes, comments, equations, accessibility and metadata.

The redline operation supports one unique match across plain runs in a paragraph or a hyperlink, preserving run formatting and the hyperlink target. A match cannot span a hyperlink boundary. It rejects fields, existing revisions and nontext content in the matched container instead of flattening them. Accept/reject handles paragraph, hyperlink and inline content-control insertions, deletions and tracked move wrappers, preserves control properties, removes move range markers and disables tracking by default. Formatting/table revisions remain explicitly unsupported. Equation conversion supports identifiers, numbers, operators, text, fractions, superscripts/subscripts, roots, fences and rectangular matrices; unsupported MathML fails explicitly. Comments target a unique whole paragraph by `text`, or the first substring match by `contains`; `ignore_case` and `all_matches` support explicit broader selection, including text in tracked deletions and tables. Accessibility changes need image names and zero-based body table indexes.

For a before/after comparison, use `"$PAPERWORKS" compare before.docx --against after.docx --output new-dir`. Read the returned `text_diff` and page statuses. View changed source/output pages and the returned diff images; review added/removed pages for intended content and pagination. A comparison report is not visual inspection. Increase render resolution only when rasterization noise makes a difference ambiguous.

After accepting or rejecting revisions, inspect for remaining supported revisions and verify the expected text: acceptance retains new text; rejection restores old text. Report unsupported revision types instead of silently discarding them.

## Editing example

After running the small authoring example, this changes one phrase with tracked changes, then adds a comment to the edited copy. For a real document, inspect first and replace the sample match and author with the intended values.

```sh
"$PAPERWORKS" inspect example.docx > before.json
cat > tracked.json <<'JSON'
{"operation":"redline","old":"Revenue totaled $200","new":"Recorded revenue totaled $200","author":"Reviewer"}
JSON
"$PAPERWORKS" word-edit example.docx --spec tracked.json --output tracked.docx
cat > comment.json <<'JSON'
{"operation":"comment","contains":"Recorded revenue","comment":"Confirm the reporting period.","author":"Reviewer"}
JSON
"$PAPERWORKS" word-edit tracked.docx --spec comment.json --output reviewed.docx
"$PAPERWORKS" inspect reviewed.docx > after.json
"$PAPERWORKS" render reviewed.docx --output reviewed-preview
```

Read `after.json` to verify both the revision and comment. To test revision handling, apply `{"operation":"accept"}` and `{"operation":"reject"}` separately to `tracked.docx`, each with a new output.

### Word operation specifications

Save one JSON object from this table to a spec file and run `word-edit INPUT.docx --spec SPEC.json --output NEW.docx`. These are examples, not a batch to apply to every file. Select real IDs/tags/paragraphs from the input; never infer them from a rendered page. `paragraph` is the zero-based direct body-paragraph index (not the position among table-cell paragraphs); body table indexes are also zero-based. Inspect the body XML with available file tools if the report does not identify the required index.

| Operation | Complete example specification | Scope / prerequisite |
|---|---|---|
| Tracked text replacement | `{"operation":"redline","old":"Old wording","new":"New wording","author":"Reviewer"}` | One unique match in supported plain runs; refuses ambiguous matches and unsupported containers. |
| Accept revisions | `{"operation":"accept","keep_tracking_on":true}` | All supported text revisions; `keep_tracking_on:true` preserves the existing tracking setting. Omit it to disable tracking afterward; the same option applies to reject. |
| Reject revisions | `{"operation":"reject"}` | All supported text revisions, not one selected change. |
| Add comment | `{"operation":"comment","text":"Exact paragraph text","comment":"Please confirm.","author":"Reviewer"}` | `text` must match one body paragraph. Instead use `contains` for a substring; `all_matches:true` and `ignore_case:true` opt into broader matching. Anchors cover the paragraph. |
| Edit comment | `{"operation":"comment-edit","id":0,"text":"Revised comment","resolved":false}` | Use the inspected comment ID. `append` adds text instead of replacing it; `resolved:true` resolves. |
| Remove all comments | `{"operation":"comments-remove"}` | All comments, not selective deletion. |
| Editable equation | `{"operation":"equation","mathml":"<math><mfrac><mi>x</mi><mn>2</mn></mfrac></math>"}` | Appends an OMML equation; no arbitrary target position. |
| Image/table accessibility | `{"operation":"accessibility","image_alt":{"Picture 1":"Revenue by quarter"},"header_tables":[0]}` | Match actual drawing names; omit `image_alt` when only setting table headers. |
| Clear selected metadata | `{"operation":"metadata","clear":["author","last_modified_by"]}` | Other accepted names: `comments`, `keywords`, `subject`, `category`, `identifier`, `language`, `title`, `version`, `content_status`. |
| Bookmark paragraph | `{"operation":"bookmark","paragraph":0,"name":"Overview"}` | Unique valid bookmark name. |
| Static linked contents | `{"operation":"navigation","max_level":3,"include_captions":false,"prefix":"Contents","title":"Contents","back_links":true}` | Requires existing headings; adds links, not page numbers. |
| Wrap a paragraph in a control | `{"operation":"control","paragraph":1,"tag":"customer"}` | Unused tag; marker-based controls are documented below. |
| Fill controls | `{"operation":"control-fill","tag":"customer","text":"Acme Ltd"}` | All controls with this tag; only unlocked, unbound plain-text controls. |
| Editing restriction | `{"operation":"protection","mode":"comments"}` | Modes: `readOnly`, `comments`, `forms`, `trackedChanges`, `none`; no password unlocking/encryption. |
| Merge documents | `{"operation":"merge","files":["appendix.docx"]}` | Appends files, resolving paths relative to the spec; verify styles and relationships. |
| Remove personal metadata | `{"operation":"privacy"}` | Broad privacy cleanup; only when requested. |
| Refresh cached fields | `{"operation":"materialize","only":["SEQ","REF"]}` | Use `only:["SEQ"]` or `only:["REF"]` to select one family; omitting `only` selects both. Pagination fields require Office refresh. |
| Replace/redact throughout package | `{"operation":"redact","values":["Acme Ltd"],"replacement":"Example Ltd"}` | Broad, case-sensitive replacement across stories, comments and properties; **not a one-occurrence paragraph edit**. May reject embedded files or remaining matching bytes. |
| Pattern redaction | `{"operation":"redact","patterns":["ID-[0-9]+"],"replacement":"X","preserve_length":true}` | Fixed-length masking cycles the replacement characters; inspect images separately. |
| Normalize selected styles | `{"operation":"normalize","styles":["Heading 1"],"heading_spacing":[12,6],"clear_paragraph_overrides":false}` | Uses existing style names, removes direct run overrides; spacing in points. |
| Freeze references | `{"operation":"freeze-references"}` | Replaces supported cached REF/PAGEREF fields with static results; refresh first. |
| Add watermark | `{"operation":"watermark","text":"DRAFT"}` | Header VML text watermark. |
| Remove watermark | `{"operation":"watermark-remove","contains":"DRAFT"}` | Matching VML header text only. |

For `redact`, omitting `replacement` inserts `[REDACTED]`; an empty string deletes matches unless `preserve_length` is enabled. A replacement containing the original match can fail the final residue check. For a narrow untracked edit, use `replace-text` under General edits to an existing document in `references/editing.md`.

## Targeted revision and inspection options

- To supersede a known tracked insertion, inspect its `w:id` in the source and use `{"operation":"redline-insertion","id":"7","new":"Revised wording","author":"Reviewer"}`. The old insertion becomes a deletion and the new wording becomes an insertion, so accept and reject remain meaningful. Use this only for the identified plain-text revision; complex revision contents are rejected.
## Positioned edits and preservation

Use these JSON operations with the existing Word helper:

- `{"operation":"control","marker":"{{NAME}}","tag":"customer"}` wraps every matching ordinary-text placeholder, including repeated occurrences in body text, tables and headers/footers. Each new control receives its own ID and retains the placeholder’s run formatting. `control-fill` updates every existing plain-text control with the requested tag, including repeated fields in body, tables, headers and footers. If any matching control is bound, locked or unsupported, the operation fails without saving an output. Creating a batch of controls requires an unused tag. Empty plain-text controls can be filled; values containing line breaks or tabs can be edited again.
- `{"operation":"caption","label":"Table","text":"Revenue","after_table":0,"bookmark":"revenue"}` places a caption after a table and bookmarks its number. Use `after_paragraph` for a figure paragraph. Check for existing captions and a defined `Caption` paragraph style before adding any. The operation references the style; it does not create a missing definition. If absent, resolve the style definition before insertion; do not replace the whole styles part just to add a caption.
- `{"operation":"reference","name":"revenue","field":true,"marker":"[[REF]]"}` inserts a REF field at a unique marker. `materialize` refreshes supported simple and complex SEQ/REF fields in document order, then resolves references using updated bookmark text. Nested fields, unsupported switches and malformed field boundaries fail explicitly; page-dependent fields stay unchanged. `freeze-references` preserves cached simple REF/PAGEREF results as ordinary runs; refresh missing caches first. Cached complex REF/PAGEREF fields are supported within one paragraph. Nested or cross-paragraph fields fail explicitly.
- `{"operation":"toc","marker":"[[TOC]]","levels":"1-3"}` inserts a dynamic TOC at the intended location. Use `navigation` for a static linked contents list that does not need field refresh.
- `{"operation":"table-geometry","table":0,"widths_twips":[1800,3600,2400],"indent_twips":360}` synchronizes table, grid and cell widths, including merged cells, and sets fixed layout. Supply the intended indentation explicitly; see the preservation example below.
- `{"operation":"style-pack","template":"reference.dotx"}` explicitly replaces styles/theme/fonts/numbering while preserving document content and other package bytes. This is restyling, not a narrow template edit. Packs with linked resources require explicit resource import first.

### Preserve indentation when resizing a table

`table-geometry` defaults `indent_twips` to **zero**, not the table's current indentation. Inspect the target table's `w:tblPr/w:tblInd` in `word/document.xml` and account for inherited table-style settings when needed. For `w:type="dxa"`, `w:w` is the indent in twips (1440 per inch). Preserve that value for a width-only edit. Do not guess zero when the effective indent is unknown. Negative or differently expressed indentation requires a suitable editor or an explicit conversion decision; this command accepts only nonnegative integer twips.

For an existing `indented.docx` whose first body table has two columns and a verified 360-twip indent, change its column widths while retaining that quarter-inch indent:

```sh
cat > table-widths.json <<'JSON'
{"operation":"table-geometry","table":0,"widths_twips":[4200,2400],"indent_twips":360}
JSON
"$PAPERWORKS" word-edit indented.docx --spec table-widths.json --output resized-table.docx
"$PAPERWORKS" inspect resized-table.docx
"$PAPERWORKS" render resized-table.docx --output resized-table-preview
```

Replace the table index, widths and indent with values from the actual task. The widths plus indent must fit the active section's usable column width, accounting for margins, gutter and multiple columns. Pass `indent_twips:0` only when zero indentation is intended. Reopen the saved XML to verify `tblInd`, grid/cell widths and unchanged cell contents; use `view_image` on the returned PNGs to check table alignment and wrapping. The operation sets fixed layout, so an auto-fitting table needs an explicit decision before using it.

For template-controlled authoring, load `references/template-editing.md` with `skill_view` using this skill’s handle and `file_path` before editing. It covers source hashes, exact measurements, editable slots and package preservation. For pagination-dependent fields, follow Field refresh policy below; retained caches alone do not prove freshness.

`inspect` reports heading jumps, vague links, drawing placement/extents, section references, direct formatting, note mismatches, anchored comment text and VML watermarks. Direct formatting is evidence, not automatically a defect. Decide whether the named style or a deliberate local exception is appropriate before normalization. `normalize` accepts `heading_spacing:[before,after]` in points for selected heading styles.

Use `comment-edit` to append text or change resolution state. Check the target application when its native comment display matters.

## Field refresh policy

Use `materialize` for supported SEQ/REF updates without an Office re-save. Pagination-dependent fields and indexes need an Office engine. When the task permits an Office re-save, use `"$PAPERWORKS" refresh input.docx --output new-directory` to create a refreshed copy through LibreOffice. Reopen it, inspect every rendered page and check TOC/PAGE/REF results against the document. Check TOC entries against heading pages, PAGE/NUMPAGES against page count, and REF/PAGEREF against bookmarked targets. A refresh-on-open flag does not establish saved results.

`refresh` can rewrite unrelated package parts. For strict preservation, use `materialize` where sufficient. If a Word refresh is unavailable, request deferred refresh with `word-edit` and `{"operation":"update-fields"}`. Save the spec as `refresh-on-open.json`, then run:

```sh
"$PAPERWORKS" word-edit input.docx --spec refresh-on-open.json --output deferred.docx
```

This sets `w:updateFields=true`; it does not calculate fields or refresh cached page numbers. When settings already exist, only that part changes. If absent, the command also registers the settings part in content types and document relationships. Authorize those exact package changes in the preservation contract. State that refresh is deferred until Word opens and updates the document, then verify the displayed results; locked fields or application settings can prevent updating. If the command is unavailable, report the runtime limitation rather than treating stale caches as verified.

## Advanced document edits

Use built-in heading styles at the correct hierarchy levels for navigation; typed number prefixes do not create automatic numbering. Verify hyperlink/bookmark destinations separately from their rendered appearance. Add image descriptions and table header designations where appropriate; review flagged omissions. Treat decorative images separately from informative images.

For captions, references, page numbers and TOCs, use fields/bookmarks with unique IDs and valid relationships. See Field refresh policy for calculation and page checks. Preserve control properties, tags, choices and bindings when filling forms. When merging documents, check styles, numbering and relationship IDs for collisions, then add requested tracked changes/comments or perform requested metadata cleanup on the merged result. Preserve existing review markup unless its removal was requested.

Direct formatting can mask a style-pack change. Normalize only selected styles when restyling is intended, preserving deliberate emphasis and code formatting. `privacy` is broad: it also removes custom properties and their relationships. Use targeted metadata edits when custom properties must remain. Document protection is an advisory editing restriction, not encryption or a security boundary; viewers may ignore it.

Redaction must remove content, not cover it with a shape. When requested, examine body text, comments, tracked deletions, headers, footers, embedded files, alternative text and document properties. Extract and search the final package for the sensitive values, then inspect rendered pages. Do not clear authorship metadata or accept all revisions during an unrelated edit. Metadata removal and revision acceptance are separate requested operations.

## Complex mathematical notation

Prefer native OMML for editable, searchable and accessible equations, including when preserving existing math. Use the OMML `word-edit` operation for supported expressions; do not replace native math with raw TeX or a plain-text approximation. For an equation image, save UTF-8 TeX without dollar delimiters in `expression.txt`, then run `"$PAPERWORKS" equation expression.txt equation.png`. Add `--inline` for compact inline notation; display notation is the default. Check the rendered notation at its final physical size.

Insert the PNG through a `create-docx` image block with a local path and physical width appropriate to the expression and page. Add a meaningful image description through `accessibility`, then render and inspect legibility and clipping. The result is a picture, not editable Office Math; do not use it as an undisclosed substitute for native equations. Do not load external packages or file/network includes from equation content.

## Footnotes and endnotes example

Use `note` for true Word notes, not footer text or comments. A unique marker is replaced with a note reference while retaining surrounding runs. Omitting `kind` defaults to a footnote. `kind:"footnote"` places the note at the page bottom; `kind:"endnote"` uses the document's endnote area. Preserve existing note settings and inspect the rendered placement. This example adds one of each to a new fixture; for an existing document, use its actual unique markers and retain the original.

```sh
cat > notes-source.json <<'JSON'
{"blocks":[{"type":"paragraph","runs":[
  {"text":"Revenue increased","bold":true},{"text":"[[FN]]. See the methodology[[EN]]."}
]}]}
JSON
"$PAPERWORKS" create-docx --spec notes-source.json --output notes-source.docx
cat > footnote.json <<'JSON'
{"operation":"note","kind":"footnote","marker":"[[FN]]","text":"Source: quarterly report."}
JSON
"$PAPERWORKS" word-edit notes-source.docx --spec footnote.json --output with-footnote.docx
cat > endnote.json <<'JSON'
{"operation":"note","kind":"endnote","marker":"[[EN]]","text":"Methodology: compare matching reporting periods."}
JSON
"$PAPERWORKS" word-edit with-footnote.docx --spec endnote.json --output with-notes.docx
"$PAPERWORKS" inspect with-notes.docx
"$PAPERWORKS" render with-notes.docx --output notes-preview
```

When there is no marker, omit `marker` and use `paragraph:0` (the zero-based direct body-paragraph index) to append the note reference at the paragraph's end. Choose the actual paragraph from the source; this index excludes paragraphs inside tables. The helper allocates note IDs and creates the required note parts/relationships. Do not invent IDs or insert note text into a footer.

Reopen the output and check that each body reference resolves to the correct note text, existing notes remain, and the markers are gone. Open the returned page PNGs with `view_image` to verify numbering, placement and wrapping. Rendered placement can differ between Word and LibreOffice; verify in Word when the task requires it.

