# Template-controlled documents

Use this workflow when the user requires a supplied template's structure and layout. An ordinary replacement does not need a template contract.

Before authoring, retain the original and record its hash, page count, section geometry, styles and numbering. Inventory editable slots in body paragraphs, tables, headers/footers, textboxes and content controls. For each slot record a stable locator (part plus bookmark, control tag or inspected XML location), allowed action, expected content type and available space. Paragraph indices alone can move after earlier edits.

Record exact source measurements rather than reconstructing the template by eye. Separate editable parts from preserve-only parts, including opaque embedded objects. Resolve ambiguous slots and unsupported structures before filling them; do not rebuild the document from extracted text as a substitute.

Use `references/editing.md` for ordinary edits and `references/review-and-fields.md` for controls and fields to fill supported slots. Preserve content-control properties, fields, relationships and deliberate formatting. Check the original hash before applying an edit plan; if the source changed, inspect and relocate the targets again.

If content exceeds a slot, shorten it without losing required meaning or reuse a source component permitted by the template contract. Do not silently shrink text or impose a different layout. Leave optional slots blank or remove them only when permitted; never invent facts to fill space. If the required content cannot fit within the permitted changes, report the constraint.

Run `"$PAPERWORKS" verify-preservation before.docx --against after.docx --requirements contract.json`. The contract uses `{"allow_changed":["word/document.xml"]}` with exact authorized part paths. Build the contract from actual authorized package paths, not every part in the document. After edits, compare package hashes and inspect changed XML to confirm edits within allowed parts stayed within the intended slots. An allowed part can contain unrelated content too.

Render and compare all pages against the source, checking wrapping, overflow, pagination, table widths and header/footer placement. Verify updated fields using the field-refresh policy in `references/review-and-fields.md`. Neither unchanged package hashes nor successful rendering alone proves that the filled content is correct.

For strict unchanged-part preservation, avoid an Office re-save because it can rewrite unrelated markup. Use `materialize` for supported SEQ/REF fields. If Word refresh is unavailable, follow the `update-fields` fallback in `references/review-and-fields.md` and disclose deferred refresh; do not claim saved pagination fields are current.

## Clone a template component

Use `word-edit` with `clone-component`; preserve the template rather than rebuilding it.

Save the operation in a JSON spec and run `"$PAPERWORKS" word-edit input.docx --spec clone.json --output edited.docx`:

```json
{"operation":"clone-component","path":"template.docx","start":1,"end":2,"before":0}
```

`start` and `end` select an inclusive, zero-based range of direct body blocks: paragraphs, tables and content controls, excluding final section properties. Inspect the source body XML to locate these blocks; paragraph-only indices are different. Optional `path` resolves relative to the spec; omit it to copy from the input. `before` is the destination body-block index; omit it to append.

Cloning imports related images, links, charts, styles and numbering, renaming conflicting style IDs and bookmark names. The destination retains its theme, defaults, page setup, headers and footers. Components containing section breaks, incomplete bookmarks, fields, comments, notes, tracked revisions, bound controls or `altChunk` dependencies are rejected. Choose a self-contained source component; do not remove those features merely to make cloning succeed.

After saving, inspect copied text and relationships, numbering, style conflicts and internal links. Render the final document using the skill's verification workflow and confirm that the inserted component and surrounding layout match the requested design.
