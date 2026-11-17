# Project computer browser tools

`project_browser` controls the Chromium instance inside the task's computer using
Playwright over CDP. It does not control the user's local browser. The existing
`computer` tool remains available for desktop screenshots, coordinates and keys.

## Task user

The browser, desktop applications and agent shell use the same non-root Linux
user within a task. Files created by either are readable by the other, including
browser downloads with owner-only permissions. The shell can also access that
task's browser profile and local services. Different tasks retain separate users,
PID/mount/network namespaces and files; the trusted bridge remains root-only.

Shell processes use a separate primary group solely to identify jobs for
cancellation and idle tracking without stopping the desktop. This is not a
separate login user or a browser credential boundary. On startup, files owned by
the previous shell user are migrated to the shared task user.

## Tabs

Use `tabs`, `open_tab`, `select_tab`, `close_tab`, and `reload`. `tabs` returns IDs,
URLs and titles. Pass `tab_id` to target a tab explicitly. Otherwise operations use
the last selected tab, falling back to the first non-DevTools page. `open_tab`
selects its new blank tab; call `navigate` with a URL afterwards. `close_tab`
requires an explicit ID. IDs survive tool calls, but not browser restarts.

## Locators and reads

A locator contains exactly one of `selector`, `role`, `text`, `label`,
`placeholder`, or `test_id`. Role locators may include `name`. `exact`, `index`,
`has_text`, `has_not_text`, and `visible` narrow the match. `and`, `or`, `has`,
`has_not`, and `child` accept another locator; the tool schema exposes one nested
combination level. `frames` is an outer-to-inner array of iframe selectors.

```json
{"action":"find","tab_id":"TAB","frames":["iframe.checkout"],"locator":{"role":"button","name":"Pay","exact":true}}
```

`find` returns matching element metadata. `inspect` takes `property` equal to
`count`, `text`, `attribute`, `visible`, or `enabled`. Attribute reads require an
`attribute` name from the public-attribute allowlist; input `value`, inline event
handlers and arbitrary data attributes are not exposed.

`read` preserves the original snapshot/reference interface. Page controls use
`offset` and `limit` (default 100, maximum 500); text uses `text_offset` in chunks
of 20,000 characters. Follow `next_offset` and `next_text_offset` until null.
Reference actions require a fresh default `read` snapshot; use locators for
controls found through pagination or frames. Locator actions re-resolve the
element on each call and reject ambiguous matches.

## Actions

- `navigate`, `back`, `forward`, `reload` operate on a tab.
- `click`, `double_click` accept `button`, `modifiers` and `force`.
- `type` replaces text; `append` and `type_sequential` type without clearing.
- `press_key` targets a locator, or uses the existing focused-element/reference
  flow with its restricted key set.
- `set_checked` takes a boolean `checked`.
- `select` accepts a single `value` or `options`, an array of objects containing
  `value`, `label` or `index`.
- `scroll` takes a vertical `amount`, capped at 2,000 pixels per call.

Use either `locator` and optional `frames`, or the existing `snapshot` and `ref`
for targeted actions. Credential fields still require a private login handoff.
All browser actions remain blocked while that handoff holds the task computer.

## Waits and diagnostics

`timeout_ms` defaults to 10,000 and is limited to 20,000. The HTTP worker request
has a separate 30-second cancellation bound.

- `wait_element`: `state` is visible, hidden, attached, detached, enabled or disabled.
- `wait_url`: wait for `url` (Playwright URL glob supported).
- `wait_load`: load, domcontentloaded or networkidle.
- `click_navigation`: register the navigation wait before clicking; optionally
  supply the expected `url`.
- `logs`: last 100 console/page-error entries captured during agent actions in
  that tab. Entries are capped at 2,000 characters. Idle/user activity is not
  collected. Logs are lost when the worker restarts.

Errors carry a code such as `multiple_matches`, `timeout`, `page_changed`,
`not_found` or `private_login_required`. Raw Playwright traces are withheld because
those can include typed values or page data. A timeout does not imply the action
had no effect; read the current page before retrying.

## Screenshots and dialogs

`screenshot` returns a model image through OV's existing image-result format.
Use the default viewport, `full_page: true`, or `clip: {x,y,width,height}`. Images
are JPEG. Capture is bounded to 32 million CSS pixels and 8 MB; use a smaller clip
when a capture exceeds those limits.

When an action opens a dialog, the result reports `dialog_pending`. Use `dialog`
with `dialog_action: inspect`, `accept`, or `dismiss`. Accepting a prompt can
supply `text`. The helper keeps a task-local Playwright connection alive so the
dialog survives between requests. Cancellation kills that worker; the next
request reconnects, without automatically replaying the cancelled action.

## Website files

`upload` takes a file-input or file-chooser-button locator and `files` paths under
the task computer's Files directory. Use `computer_attachment` or
`computer_transfer` to put local attachments there first. Uploads are limited to
20 files and 25 MB total; escaping paths and symlinks outside Files are rejected.

`download` clicks a download control and waits for the download. `download_media`
saves the selected element's HTTP(S) media/link URL, with authenticated cookies
kept internal. Blob/data URLs and segmented video are not supported by
`download_media`; use the site's download control when available. Saved files
have unique names, never overwrite an existing file, and are limited to 25 MB.
The result includes `computer_path`; publish it with `computer_attachment` using
`from_computer`. Download files are not automatically attached to the chat.

## JavaScript evaluation boundary

There is deliberately no model-supplied `evaluate` action. Playwright's ordinary
`evaluate` can mutate the page and read input values, cookies and browser storage.
An isolated JavaScript world does not by itself restrict DOM access; a Node VM is
not a substitute for a security boundary either. An enforceable restricted page
API would require separate design and testing. Structured reads above provide
the supported inspection interface. This is an explicit difference from the
OpenAI plugin's documented read-only evaluation API, not a claim of full parity.

## Regression tests

Tests use a disposable container and fixture pages, not the user's browser,
accounts, database or running project computers. From the repository root:

```sh
# IMAGE must contain the pinned Playwright installation and Chromium from the
# project's computer Dockerfile. Mount current sources read-only.
docker run --rm --network none --entrypoint node \
  -v "$PWD/packaging/computer:/suite:ro" IMAGE \
  --test /suite/browser_features_test.cjs

docker run --rm --network none --entrypoint node \
  -v "$PWD:/src:ro" IMAGE /src/scripts/test-computer-browser.cjs

go test ./internal/service ./pkg/agenttools ./internal/persistence/sqlite

# Live shared-user checks require an image built with current binary/scripts.
# Creates and removes its own container and volume.
python3 scripts/test-computer-shared-user.py IMAGE

# On macOS, cross-compile the Linux helper tests, then run in a matching image.
GOOS=linux GOARCH=arm64 go test -c ./cmd/computer -o /tmp/ov-computer.test
docker run --rm --network none --entrypoint /suite/computer.test \
  -v /tmp/ov-computer.test:/suite/computer.test:ro IMAGE -test.run TestBrowserWorker
```

Rebuild the OV server and project-computer image together: the action schema,
server allowlist, helper worker and browser script form one interface. These
fixture tests do not establish compatibility with every external website.
