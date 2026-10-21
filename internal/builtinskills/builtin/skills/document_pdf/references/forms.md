## Form repair and verification

For an existing form, use `"$PAPERWORKS" fill input.pdf --values values.json --output completed.pdf`. Keep it interactive unless the user requests `--flatten`. The JSON maps inspected field names to values, for example `{"Full name":"Alex Smith","Consent":"/Yes","Interests":["Alpha","Beta"]}`. Use these only if inspection reports those fields and export states. Choices must match allowed values; checkbox/radio states are not guessed booleans.

Before filling, inspect the canonical field tree and page widgets, following inherited names/values through parents. `fill` can recover genuinely orphaned widgets; a separate field with the same name is ambiguous and must be resolved before repair. Unknown fields, invalid states or inconsistent saved values fail without publishing output.

Reopen the result and compare every requested value, not just field counts. Canonical values and widget values must agree, with usable normal appearances (`/AP` → `/N`). For radio groups the parent stores the selection while inactive widgets can be `/Off`. Render filled pages to check visible selections, text and clipping; neither appearance-stream presence nor a screenshot proves the saved values are correct.

For flattening, the helper refreshes all values, including unchanged fields, paints appearances, then removes widgets and the AcroForm tree. Verify zero widgets and no editable field tree after saving, and inspect affected pages. Keep the editable source. Signed forms require a separate explicit workflow; do not flatten signature fields or claim edits preserve signature validity.

### Form example

This creates a small fixed-page form, then fills a copy. Coordinates and sizes are PDF points, with the page origin at bottom-left. Use a new directory for each run.

```sh
cat > form.json <<'JSON'
{"paper":"A4","pages":[{
  "text":[{"value":"Reviewer","pos":[72,740],"font":{"name":"Helvetica","size":12}}],
  "text_fields":[{"id":"reviewer","pos":[72,700],"width":220,"height":24,"value":"Draft","font":{"name":"Helvetica","size":12}}],
  "checkboxes":[{"id":"approved","pos":[320,700],"width":16,"value":false}]
}]}
JSON
"$PAPERWORKS" create-pdf --spec form.json --output form.pdf
"$PAPERWORKS" inspect form.pdf > form-before.json
cat > values.json <<'JSON'
{"reviewer":"Alex Smith","approved":"/Yes"}
JSON
"$PAPERWORKS" fill form.pdf --values values.json --output filled.pdf
"$PAPERWORKS" inspect filled.pdf > form-after.json
"$PAPERWORKS" render filled.pdf --output filled-preview
```

For this fixture, verify `reviewer` is `Alex Smith` and `approved` is `/Yes`, and inspect the PNG to confirm the field text and selected checkbox are visible. For a different input, replace the names/export states with those reported by `inspect`. To produce a static copy when requested, use `fill filled.pdf --values values.json --flatten --output flattened.pdf`, then inspect and render that result too.

