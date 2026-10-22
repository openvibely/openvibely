## Threaded comments

For Excel comment conversations, use `"$PAPERWORKS" comments`. Ordinary cell notes are not threads. List existing threads read-only:

```sh
"$PAPERWORKS" comments source.xlsx
```

Create a thread using a JSON specification, saving a new workbook:

```json
{"action":"add","sheet":"Revenue","cell":"F2","author":"User","text":"Confirm this assumption."}
```

```sh
"$PAPERWORKS" comments source.xlsx --requirements comment.json --output commented.xlsx
```

Use the returned root `id` as `thread_id` for `reply`, `resolve`, or `reopen`. A reply supplies `author` and `text`; resolve/reopen need only `action`, `sheet`, and `thread_id`. Reopen a resolved thread before replying. Use the user’s supplied display name, otherwise `User`; do not impersonate someone else. Existing authors are reused by exact display name; if ambiguous, supply the listed `author_id`. Listing returns the cells, authors, reply parents, timestamps, text and resolution states. Reopen the final saved file with `comments` to verify the conversation.

Edits patch the native XLSX thread/person parts and relationships while preserving other parts, including existing notes. Avoid a general library re-save afterward that cannot preserve threaded comments. For subsequent narrow edits use the package-preserving input helpers. PNG/PDF previews do not establish comment-thread appearance or resolution in Excel; package and independent-reader tests do not replace native application verification.

