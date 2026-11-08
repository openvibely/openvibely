# Plugin pages

Plugin pages let a project add its own page to the OpenVibely left rail. A page runs as a sandboxed iframe inside the main content area. Every page has side chat that knows which page it is on. Pages can also provide a local backend process, shared operations for page buttons and the assistant, and live refresh events.

The primary workflow is to ask an agent in a task thread to create a page. The agent writes and validates the bundle and publishes it into that task's project; users do not need to author packages or manually import them. OpenVibely does not need Node at runtime. There is no marketplace.

## Create a page through a task

Ask, for example: **“Create a sales report page for this project with filters and a page-aware chat.”**

Task agents have two tools, on initial runs and task-thread follow-ups:

- `get_custom_page_guide` returns the bundled manifest, bridge, context, operation, language-independent backend, and optional storage authoring instructions. It works even when the user's repository contains no OV source.
- `publish_custom_page` takes `package_path` relative to the task workspace and an optional `page_id`. The server resolves the owning project from the persisted task, validates paths and package contents, copies the package outside the worktree, and registers the project instance. The response includes a page URL and enabled status. Successful publication emits a live navigation refresh event.

Published pages are enabled automatically, including their declared native backend. There is no separate custom-page approval workflow. Existing agent/model selection and task tool policies apply. Publication is unavailable to project chat and automation auditor tasks; project chat can delegate page creation to a coding task.

Identical retries reuse the same package and project instance. Changed content requires a version bump; the matching page instance is updated while preserving its state and conversation. Supply `instance_id` when known. Task publication requires a configured task workspace. The source bundle remains in the task's changes for review; the installed copy survives worktree deletion.

Local import in settings is an advanced fallback for existing bundles, not the normal creation workflow.

## Concepts

| Term | Meaning |
| --- | --- |
| **Package** | One immutable, installed `plugin.json` bundle version (`<id>@<version>`). It is content-hashed and stored under the managed root, outside any task worktree. |
| **Instance** | One page of a package that has been added to **one** project. It has its own enabled flag, config, state, and side-chat conversation. |
| **Page integration** | The shared contract in `internal/pageintegration`. It covers page identity, explicit context, operations, events, and agent tools. Plugin pages use it through an adapter, and built-in pages can adopt it later. |

The managed root is `$OPENVIBELY_PAGE_PLUGIN_ROOT`, or `<AppDataDir>/page-plugins` by default. Packages live at `<root>/<plugin id>/<version>/`.

## Manifest (`plugin.json`, schema_version 1)

```json
{
  "schema_version": 1,
  "id": "acme.widgets",
  "name": "Widgets",
  "version": "1.2.0",
  "description": "optional",
  "pages": [{"id": "main", "title": "Widgets", "icon": "W", "entry": "index.html"}],
  "backend": {
    "executables": {"darwin-arm64": "bin/widgets-darwin-arm64", "linux-amd64": "bin/widgets-linux-amd64", "any": "bin/widgets.sh"},
    "args": ["--stdio"]
  },
  "permissions": ["backend.exec", "storage", "events"],
  "operations": [
    {
      "name": "refresh_data",
      "description": "Reload widget data",
      "kind": "read",
      "permissions": ["events"],
      "emits": ["data.changed"],
      "input_schema": {"type": "object", "properties": {}},
      "output_schema": {"type": "object"}
    }
  ]
}
```

Validation is strict:
- Unknown fields are rejected.
- `id` must match `^[a-z0-9][a-z0-9._-]{1,63}$`, and `version` must be a semantic version.
- Each page `entry` must be a relative `.html` path without `..`, backslashes, or NUL characters.
- Executable paths follow the same rules as page entries.
- Packages may not contain symlinks or special files. Limits are 2000 files and 64 MiB per package, and 256 KiB for `plugin.json`.
- Re-installing the same `id@version` with different content fails. Bump the version instead.

### Permissions

| Permission | Grants |
| --- | --- |
| `storage` | Lets the backend persist per-instance state (max 1 MiB) |
| `events` | Lets the plugin publish and subscribe to live refresh events. Required for any `emits`. |
| `agent` | Enables the side chat and `analyze.request` |
| `backend.exec` | Runs the backend executable. Starts when the page is enabled. |

Operation `kind` is either `read` or `action`. Agent tool calls run directly, like other OV tools; there is no per-action confirmation.

### Platform executable selection

The host looks for an exact `"<GOOS>-<GOARCH>"` key first, such as `darwin-arm64`, `linux-amd64`, or `windows-amd64`. If there is no exact match it uses `"any"`. If neither exists, enabling fails. Scripts selected via `any` must be directly executable (shebang plus the exec bit), because OpenVibely never invokes a shell.

## Isolation and the bridge (v1)

- The page is loaded in `<iframe sandbox="allow-scripts allow-forms">`, with no `allow-same-origin`, so it runs in an opaque origin. Its assets are served from `/plugin-assets/<handle>/…` with a `sandbox` CSP and `connect-src 'none'`, plus `nosniff` and `no-store`. `plugin.json` and backend executables are never served.
- The page never receives API keys, cookies, or database access. All server access goes through the host page via `postMessage`.
- Each page load gets an unguessable **handle**. The handle is bound to the instance, its owning project, and the signed-in principal, and it expires after 12 h at most. It is retired when the page unloads, when the user navigates or changes project, and when the instance is disabled or removed or its project is deleted.
- The host only accepts messages whose `event.source` is the frame's own window. It also requires `v === 1`, a matching handle, a known type, a string id, and a message of at most 64 KiB. The server validates the same envelope again (`pageplugins.ParseBridgeMessage`), rejecting unknown fields, and re-authorizes the instance against the owning project on every request.

Envelope: `{"v":1,"type":"…","id":"<client id>","handle":"<handle>","payload":{…}}`. The handle is passed to the page in the iframe URL query (`?handle=…`).

Page → host:

| type | payload | reply `result` |
| --- | --- | --- |
| `ready` | `{}` | `{bridge_version, instance{id,page_id,title,project_id}, permissions, operations[]}` |
| `op.invoke` | `{operation, input}` | the operation output |
| `context.set` | `{summary, selection[], filters{}, date_range{from,to}, data}` (≤64 KiB, ≤200 selected) | `{revision}` |
| `events.subscribe` | `{}` | `{subscribed:true}` |
| `analyze.request` | `{prompt}` (≤4000 chars) | `{exec_id}` |

Host → page messages use the envelope `{"v":1,"type":"…","replyTo":<id or null>,"handle":"<handle>","payload":{…}}`:

| type | payload |
| --- | --- |
| `result` | the reply `result` for request `replyTo` |
| `error` | `{message}` for request `replyTo` |
| `event` | `{type, operation, data, at}` (live refresh events) |
| `analysis.delta` | `{exec_id, text}` |
| `analysis.done` | `{exec_id, error?}` |

## Project permissions

Custom pages use the same `ProjectAccessService` as task threads. View access is required for the page, transcript, asset capabilities, live events, and declared read operations. Edit access is required for chat sends, Analyze, steering, Stop, publication, and action operations. The navigation rail lists only accessible projects' pages, including in All Projects. Package administration remains an organization-admin operation.

Queued turns and installation continuations restore the saved submitting user and recheck Edit access before starting. Page tools recheck the user's current role on every invocation. As with task threads, revoking Edit access does not cancel a reply already running, but blocks its unauthorized tools and subsequent turns. Losing View access closes live page subscriptions and prevents reuse of asset capabilities.

Operation kinds are a backend contract: `read` must not change stored data; mutations must use `action`. The Sales example's Refresh is an action, while Get report reads without persisting initialization. Installed native processes remain outside the application sandbox and must honor their declared operation semantics.

## Backend processes (JSON-RPC 2.0 over stdio)

- **Launch:** started with `os/exec` and never through a shell. The working directory is the package directory. The environment is minimal (PATH, LANG, TMPDIR; or SystemRoot, PATH, TEMP, TMP on Windows), so no OpenVibely secrets are inherited.
- **One process per enabled instance.** It is started on enable or first use. It is stopped on disable, instance removal, project deletion, and server shutdown.
- **Framing:** one JSON object per line on stdin and stdout. Messages are limited to 1 MiB each; a larger message makes the host stop the process. stderr is captured as sanitized log lines, keeping the last 200.
- **Handshake:** the host sends `initialize {protocol:"openvibely.page/1", params:{protocol, instance{id,project_id,page_id}, config, state}}`. The backend must answer `{"protocol":"openvibely.page/1"}` within 10 s. Every actual launch, including automatic crash recovery, reloads persisted instance state/configuration after checking enabled status and package integrity. A failed read prevents launch; an old startup snapshot is never used as a fallback.
- **Calls:** `operation/invoke {operation, input, caller{via}}`. Every request has an integer `id`. The default timeout is 30 s, including waiting for and writing to stdin. Cancellation while awaiting a response attempts `$/cancelRequest {id}` with a separate 100 ms bound. If a write is interrupted, OV terminates that process and closes its pipe: a partially written JSON-RPC frame cannot safely be reused. Restart backoff also respects cancellation.
- **Notifications from the backend:** `state/set {state}` (requires `storage`) and `events/publish {type, data}` (requires `events`).
- **Shutdown:** the host attempts `shutdown` (request), then `exit` (notification), and closes stdin. These steps share one 3 s grace period. An independent timer kills the process tree at that deadline even if stdin is blocked. Waiting for the killed process to exit is bounded to one additional second.
- **Restarts:** a crashed backend is restarted on the next call, with backoff, at most 3 times per 5 minutes. After that the instance reports `failed` until it is re-enabled.
- **Limits:** backends run with your OpenVibely OS user's permissions and are **not** sandboxed at the OS level. Installing and enabling a package authorizes that backend to run.

## Install, enable, remove

Use **Settings → Plugin pages** (`/settings/plugin-pages?project_id=<project>`).

1. **Install**: either "Install Sales example" or "Install from directory" with an absolute path to a prebuilt bundle. You need to be an administrator when organizations are enabled.
2. **Add to project**: pick an installed package and a page to create a disabled instance in the current project.
4. **Enable**: the page appears in the left rail. **Disable** closes admission to new operations, cancels that instance's running side-chat and inline analysis through OV's existing cancellation machinery, interrupts backend startup and active calls, and releases its handles and subscriptions. Backend termination does not depend on the process reading stdin. Saved state and conversation history remain intact. Re-enabling creates a new runtime lifetime; cancelled work from the previous lifetime stays cancelled. Other instances are unaffected.
5. **Remove instance**: deletes only that instance, together with its state, side-chat conversation, and turn-context snapshots. Other projects' instances of the same package are unaffected.
6. **Remove package**: only allowed once no instance uses it. Its files are deleted from the managed root.

### Optional storage

OV saved state is a convenience for small JSON data (up to 1 MiB per instance), not a required database for custom apps. A backend declares `storage` and sends `state/set` to replace that instance's saved JSON. This notification has no persistence acknowledgment. Each launch receives the latest successfully saved state in `initialize`, including after a crash.

A backend can instead use any user-chosen database and manage its schema and data itself. OV does not provision or manage that database; credentials and database access stay in the backend, not in the iframe. Do not use OV's internal database or store mutable data in installed package files. The authoring tool explains both choices. No new database directories or per-plugin tables are introduced by this feature.

### Data lifecycle policy

- Instance config and state, the side-chat binding, and per-turn context snapshots are deleted with the instance. They are also cascade-deleted when the owning project is deleted.
- Packages are kept until an administrator removes them explicitly.
- Page context held in memory (`context.set`) is per principal and per instance. It is never persisted on its own; it is only saved as the snapshot attached to a chat turn.

## All Projects

In All Projects the left rail lists every enabled page, labelled with its owning project. Opening a page from there redirects to `/p/<instance>?project_id=<owning project>`, so all calls stay scoped to that project. The API endpoints reject `__all_projects__`.

## Side chat

The "Chat about this page" panel reuses the standard chat message components and streaming pipeline (`startStreamingResponse`, live execution streams, Stop and cancellation).
- **Persistence:** each instance has one persisted conversation task, stored in the hidden `freezer` category. The conversation resumes when you reopen the page. Provider transport sessions, saved provider state, and compaction summaries use `task:<conversation-task-id>`, separate from every other page and from normal `chat:project:<project-id>` chat. This scope survives restarts without switching the page chat to coding-agent prompts. Previously shared project checkpoints are not copied into page conversations; existing page message history remains available.
- **Context:** each turn stores a snapshot of the page's explicitly shared context (`page_turn_contexts`). That snapshot is injected into the system context with provenance, fenced as `<untrusted_page_context>` data. OpenVibely never reads or scrapes the iframe's DOM.
- **Tools:** page tools (`page_<operation>`) are built server-side from the manifest. Each tool call re-resolves and re-authorizes the instance. Agent calls to actions wait for Approve or Deny in the page.
- **In-page analysis:** `analyze.request` runs the same kind of turn and streams `analysis.delta` messages into the page.

## Sales example

The bundled example (`internal/pageplugins/examples/sales`) uses a host-implemented builtin backend, so it does not execute any process. It generates deterministic sample data per instance, keeps state per instance, and offers these operations:
- `refresh_data` (read, tool, emits `data.changed`)
- `get_report` (read, tool)
- `set_target` (action, tool)

To try it:
1. Open Settings → Plugin pages in project A, click **Install Sales example**, add the `sales` page, and click **Enable**.
2. Switch to project B and add and enable it again. Each project now has its own Sales page with independent data.
3. Open **Sales** in the left rail. Click **Refresh** to see the revision change live, or use **Analyze** to stream an analysis into the page.
4. Open the side chat and ask "Refresh the data and summarize it". The agent calls `page_refresh_data`, which is the same operation the button uses, and the page updates through the `data.changed` event.

## Adopting the interface in built-in pages (Analytics, Automations)

These pages are not retrofitted yet. To adopt the interface, a built-in page would do the following (no iframe or plugin package needed):

1. Implement `pageintegration.Integration` (`Ref()`, `Operations()`, `Invoke()`). Use a distinct `PageRef.Kind` such as `"analytics"`, the owning project ID, and handlers that call existing services.
2. When the page's selection, filters, or date range change, post them to a small endpoint that calls `ContextStore.Set(principal, ref, ctx, source)`. Never read them from the DOM.
3. Build a `pageintegration.Conversation` binding with the server-resolved `PageRef`, `stores.PageConversations.ForPage(ref)`, a `ContextStore`, and an authorized `Resolver`. Its `Begin` callback rechecks access and admits work against the page's enabled lifetime. Its `Lifetime` callback returns a context cancelled on disable/removal, independent of the request or browser lifetime. The page adapter owns authorization and enabled-state admission; frontend IDs never grant access.
4. Use `pageConversationTask` to obtain the backing conversation, `loadPageChatWindow` for its initial history, `startPageChatTurn` for sends, and `cancelPageConversation` for Stop. Enable `pageChatInput.AllowQueue` for composer sends. These methods save page context and supply authorized page tools to the shared execution pipeline.
   Render `components.PageSideChat` with a `PageChatView`, using `components.WithThreadSurface` to supply your authorized thread base URL, project ID, Send URL, and Stop URL. The panel renders the existing `TaskThreadView`; it does not implement its own transcript or reconnect loop. Authorize each thread request before delegating to the shared readers, as `pluginThreadContext` and `PluginChatThread` do. Supply routes beneath the thread base for pending inputs, composer actions, execution fragments, and steering. History pagination uses the thread endpoint's `before` and `limit` parameters.
   Wrap the panel in `data-page-chat-root` and supply a `data-page-chat-toggle` control. Load `page-chat.js` for panel controls; refreshes call the existing task-thread reconciliation function. No plugin manager or iframe is needed for the view. Built-in adapters must provide their own authorized binding and recovery resolver. Do not duplicate the conversation runtime.
5. Publish `pageintegration.Event`s on a `Hub` after mutations. Close subscriptions on view disposal and refetch data when reconnecting. Use a request generation counter to ignore responses belonging to previous filters/views; do not replay mutating operations to refresh the view.

The plugin adapter is `internal/pageplugins/conversation.go`. Shared persistence lives in `page_conversations` (project + kind + instance + page identity) , `page_turn_contexts`, and queued submission contexts in `page_queued_contexts`. Migration 226 creates this schema directly. `TestPageConversationWithoutPlugin` exercises the shared runtime with a non-plugin page and no plugin manager. Analytics and Automations have not been integrated.

### Navigation and reconnect behavior

- Leaving a page aborts its browser requests, closes its event and execution-stream subscriptions, and retires its handle. Late responses cannot update another page. This detaches the UI; it does **not** send Stop or cancel an admitted AI turn. The turn can finish and remains in saved conversation history.
- Startup recovery routes queued page messages back through their page adapter, checking the enabled state and submitting user before FIFO promotion. Installation continuations restore the saved turn context and page tools and retain the hidden page-chat category. Turn snapshots include the submitting identity; older snapshots without that identity cannot be automatically resumed as installation continuations.
- Reopening resumes the same conversation. Disabling/removing the page explicitly cancels its AI work and stops its backend through the existing enabled-lifetime mechanism.
- A retired or expired handle closes existing server subscriptions immediately. Organization access is rechecked before each event and on the 25-second heartbeat.
- Bridge subscribers receive `event` with `{type:"page.refresh"}` after subscribing and whenever the event connection opens/reconnects. Reload relevant data with a read operation. The Sales example handles this event and ignores out-of-order report responses. It also awaits context publication before Analyze.
- Browser lifecycle regression checks can be run with `node --test web/tests/page_lifecycle.test.cjs` using a development-only JavaScript runner. Node is not an application or plugin runtime dependency.

## Known limitations

- Installation is from a local directory or the bundled example only. There is no upload and no signing.
- Backends are not OS-sandboxed (see above). Frontend-only plugins and builtins never execute code on the host.
- Page contexts are kept in memory and are lost on restart.
- The side chat supports the shared composer's model selection and attachments. It runs one turn at a time per instance; follow-up messages wait in the durable input queue with their submitting identity and page context snapshot. Stop and disable cancel pending messages for that page.

## Browser startup reports

Task agents can query `get_custom_page_status(instance_id)` for instances in their task's project. Publishing alone never proves browser activation. The response contains separate browser and backend states; the tool does not open a browser, launch code, or reset retry counters.

A page must send `page.loaded` with `{}` after initialization and required initial reads. It can send `page.failed` with `{error:"reason"}` (maximum 2048 bytes). The host injects an error observer before page scripts to capture uncaught errors, failed resources, and unhandled rejections without weakening the iframe sandbox or modifying installed files. Missing readiness becomes failure after 30 seconds. Successful startup is not a functional test.

Reports are in memory, tied to package content and the latest page-load handle. States are `not_opened`, `loading`, `loaded`, and `failed`. No browser load means `not_opened`; OV restart clears observations. Old tabs cannot overwrite newer reports; a failed load requires reload to retry. Error text is untrusted plugin data. Page navigation still detaches the UI without cancelling saved AI work.

A backend that exhausts its crash-restart budget stays failed: ordinary calls cannot replace the supervisor to reset the counter. Explicitly disable and enable the instance to retry after correcting the problem. Startup reports do not affect agent tool permissions or database storage.

## Updating an existing page

`publish_custom_page` accepts an optional `instance_id`. By default it matches the task project's stable plugin ID and page ID. A new manifest version replaces that instance, preserving its URL, configuration, state and conversation. Explicit IDs are checked against the task project and the same plugin/page identity; ambiguous existing duplicates require an explicit ID. Use a different stable identity only when intentionally creating another page.

Publication validates the bundle and imports external task files into the page's permanent directory in place. The directory is never swapped or deleted. Each instance serves assets and runs its backend from that same directory. Publication retires browser handles and restarts the backend while preserving the chat. Failed startup retains the edited files and leaves chat available for repairs. There is no rollback or conflict merging; last successful publication wins. Shared imported packages remain installation templates, not per-edit runtime snapshots.

Side chat is available on every enabled page. All declared operations are automatically exposed to its assistant. Legacy `agent` and `expose_as_tool` declarations are accepted for existing packages but do not control chat or tool availability. Project access checks still apply.

## Editing in side chat

Side chat uses the standard coding tools directly against the page's permanent files. Editing and shell tools do not publish or request a refresh. The assistant builds if needed, then calls its bound `publish_custom_page` tool with `{}` to validate and publish the current files. On success only the page frame refreshes; the URL, data, conversation and composer remain intact. Files persist across replies and restarts, including failed edits. There is no saved working-version guarantee: reloading a page can read unpublished edits because assets use the same files. Native crash recovery will not launch files that differ from the last explicit publication. Publish after repairing them to restart the backend. Page tools recheck current user/project authorization. Task publications accept a source bundle and overwrite files in place, without coordinating concurrent editing sessions.
