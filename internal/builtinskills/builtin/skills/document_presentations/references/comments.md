## Native review comments

Use `"$PAPERWORKS" slide-comments` to list modern PowerPoint comments:

```sh
"$PAPERWORKS" slide-comments source.pptx
```

The report includes author IDs, thread/reply IDs, text, resolution state, reactions and anchor XML. To add a comment, save a JSON specification and write a new PPTX:

```json
{"action":"add","slide":1,"author":"Reviewer","text":"Check the reporting period.","position_emu":[914400,914400]}
```

```sh
"$PAPERWORKS" slide-comments source.pptx --requirements comment.json --output reviewed.pptx
```

Slides are one-based. Specify `shape_id` to anchor to an object, including a grouped object, table, chart or image. Add `text_match` and zero-based `occurrence` for shape text. Matches can span paragraphs or hard line breaks: represent either separator as `\n` in the query. Field display text participates in matching; fields and formatting are preserved. Native offsets account for UTF-16 characters. Existing imported anchors are preserved when replying or changing state.

Use `reply` with `thread_id`, `author`, and `text`; use `resolve`/`reopen` with the root `thread_id`. Reopen before replying to a resolved thread. `react` toggles the specified `reaction` for `author` on the thread or optional `reply_id`. The visible author is required; supply `author_id` when display names are ambiguous. Re-list the saved file and verify the intended thread and state. Preserve other review content; do not convert modern comments into legacy comments or speaker notes. Native application display requires separate verification.

