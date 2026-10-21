# Template-controlled documents

Use this workflow when the user requires a supplied template's structure and layout. An ordinary replacement does not need a template contract.

Before authoring, retain the original and record its hash, page count, section geometry, styles and numbering. Inventory editable slots in body paragraphs, tables, headers/footers, textboxes and content controls. For each slot record a stable locator (part plus bookmark, control tag or inspected XML location), allowed action, expected content type and available space. Paragraph indices alone can move after earlier edits.

Record exact source measurements rather than reconstructing the template by eye. Separate editable parts from preserve-only parts, including opaque embedded objects. Resolve ambiguous slots and unsupported structures before filling them; do not rebuild the document from extracted text as a substitute.

Use the operations documented in the main skill to fill supported slots. Preserve content-control properties, fields, relationships and deliberate formatting. Check the original hash before applying an edit plan; if the source changed, inspect and relocate the targets again.

Run `"$PAPERWORKS" verify-preservation before.docx --against after.docx --requirements contract.json`. The contract uses `{"allow_changed":["word/document.xml"]}` with exact authorized part paths. Build the contract from actual authorized package paths, not every part in the document. After edits, compare package hashes and inspect changed XML to confirm edits within allowed parts stayed within the intended slots. An allowed part can contain unrelated content too.

Render and compare all pages against the source, checking wrapping, overflow, pagination, table widths and header/footer placement. Verify updated fields using the main skill's field-refresh policy. Neither unchanged package hashes nor successful rendering alone proves that the filled content is correct.

For strict unchanged-part preservation, avoid an Office re-save because it can rewrite unrelated markup. Use `materialize` for supported SEQ/REF fields; resolve conflicts between pagination refresh and preservation before proceeding.
