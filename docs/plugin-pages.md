# Plugin pages

Plugin pages let a project add its own page to the OpenVibely left rail. A page runs as a sandboxed iframe inside the main content area. It can optionally have a local backend process, operations that the page buttons and the side-chat agent both use, live refresh events, and a side chat that knows which page it is on.

The primary workflow is to ask an agent in a task thread to create a page. The agent writes and validates the bundle and publishes it into that task's project; users do not need to author packages or manually import them. OpenVibely does not need Node at runtime. There is no marketplace.

## Create a page through a task

Ask, for example: **“Create a sales report page for this project with filters and a page-aware chat.”**

Task agents have two tools, on initial runs and task-thread follow-ups:

- `get_custom_page_guide` returns the bundled manifest, bridge, context, operation, and Go backend authoring instructions. It works even when the user's repository contains no OV source.
- `publish_custom_page` takes `package_path` relative to the task workspace and an optional `page_id`. The server resolves the owning project from the persisted task, validates paths and package contents, copies the package outside the worktree, and registers the project instance. The response includes a page URL and enabled/approval status. Successful publication emits a live navigation refresh event.

Frontend-only pages are enabled automatically. Packages with native backends are installed without execution and remain disabled until an administrator approves and enables them; the task returns an approval link. The agent cannot grant itself backend approval. Existing agent/model selection and task tool policies apply. Publication is unavailable to project chat and automation auditor tasks; project chat can delegate page creation to a coding task.

Identical retries reuse the same package and project instance. Changed content requires a version bump; a new version currently creates a separate instance rather than replacing an existing page's state or conversation. Task publication requires a configured task workspace. The source bundle remains in the task's changes for review; the installed copy survives worktree deletion.

Local import in settings is an advanced fallback for existing bundles, not the normal creation workflow.

## Concepts

| Term | Meaning |
| --- | --- |
| **Package** | One immutable, installed `plugin.json` bundle version (`<id>@<version>`). It is content-hashed and stored under the managed root, outside any task worktree. |
| **Instance** | One page of a package that has been added to **one** project. It has its own enabled flag, config, state, approval, and side-chat conversation. |
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
  "permissions": ["backend.exec", "storage", "events", "agent"],
  "operations": [
    {
      "name": "refresh_data",
      "description": "Reload widget data",
      "kind": "read",
      "expose_as_tool": true,
      "requires_approval": false,
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
| `backend.exec` | Runs the backend executable. Requires administrator approval. |

Operation `kind` is either `read` or `action`. Actions, and any operation with `requires_approval: true`, need the user to confirm in the page before an **agent** can run them. A page button click counts as the user's own action.

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

## Backend processes (JSON-RPC 2.0 over stdio)

- **Launch:** started with `os/exec` and never through a shell. The working directory is the package directory. The environment is minimal (PATH, LANG, TMPDIR; or SystemRoot, PATH, TEMP, TMP on Windows), so no OpenVibely secrets are inherited.
- **One process per enabled instance.** It is started on enable or first use. It is stopped on disable, instance removal, project deletion, and server shutdown.
- **Framing:** one JSON object per line on stdin and stdout. Messages are limited to 1 MiB each; a larger message makes the host stop the process. stderr is captured as sanitized log lines, keeping the last 200.
- **Handshake:** the host sends `initialize {protocol:"openvibely.page/1", params:{protocol, instance{id,project_id,page_id}, config, state}}`. The backend must answer `{"protocol":"openvibely.page/1"}` within 10 s.
- **Calls:** `operation/invoke {operation, input, caller{via}}`. Every request has an integer `id`. The default timeout is 30 s. When a call is cancelled or times out, the host sends the notification `$/cancelRequest {id}`.
- **Notifications from the backend:** `state/set {state}` (requires `storage`) and `events/publish {type, data}` (requires `events`).
- **Shutdown:** the host sends `shutdown` (request) and then `exit` (notification), and closes stdin. If the process is still running after 3 s, its process tree is killed.
- **Restarts:** a crashed backend is restarted on the next call, with backoff, at most 3 times per 5 minutes. After that the instance reports `failed` until it is re-enabled.
- **Limits:** backends run with your OpenVibely OS user's permissions and are **not** sandboxed at the OS level. That is why `backend.exec` requires administrator approval.

## Install, approve, enable, remove

Use **Settings → Plugin pages** (`/settings/plugin-pages?project_id=<project>`).

1. **Install**: either "Install Sales example" or "Install from directory" with an absolute path to a prebuilt bundle. You need to be an administrator when organizations are enabled.
2. **Add to project**: pick an installed package and a page to create a disabled instance in the current project.
3. **Approve** (only for `backend.exec`): the approval screen shows the permissions and the content hash. Approval is bound to that hash. If the package's files change on disk, or a different version is installed, the backend refuses to start until it is approved again.
4. **Enable**: the page appears in the left rail. Disable hides it and releases its runtime resources.
5. **Remove instance**: deletes only that instance, together with its state, side-chat conversation, and turn-context snapshots. Other projects' instances of the same package are unaffected.
6. **Remove package**: only allowed once no instance uses it. Its files are deleted from the managed root.

### Data lifecycle policy

- Instance config and state, the side-chat binding, and per-turn context snapshots are deleted with the instance. They are also cascade-deleted when the owning project is deleted.
- Packages are kept until an administrator removes them explicitly.
- Page context held in memory (`context.set`) is per principal and per instance. It is never persisted on its own; it is only saved as the snapshot attached to a chat turn.

## All Projects

In All Projects the left rail lists every enabled page, labelled with its owning project. Opening a page from there redirects to `/p/<instance>?project_id=<owning project>`, so all calls stay scoped to that project. The API endpoints reject `__all_projects__`.

## Side chat

The "Chat about this page" panel reuses the standard chat message components and streaming pipeline (`startStreamingResponse`, live execution streams, Stop and cancellation).
- **Persistence:** each instance has one persisted conversation task, stored in the hidden `freezer` category. The conversation resumes when you reopen the page.
- **Context:** each turn stores a snapshot of the page's explicitly shared context (`page_plugin_turn_contexts`). That snapshot is injected into the system context with provenance, fenced as `<untrusted_page_context>` data. OpenVibely never reads or scrapes the iframe's DOM.
- **Tools:** page tools (`page_<operation>`) are built server-side from the manifest. Each tool call re-resolves and re-authorizes the instance. Agent calls to actions wait for Approve or Deny in the page.
- **In-page analysis:** `analyze.request` runs the same kind of turn and streams `analysis.delta` messages into the page.

## Sales example

The bundled example (`internal/pageplugins/examples/sales`) uses a host-implemented builtin backend, so it does not execute any process. It generates deterministic sample data per instance, keeps state per instance, and offers these operations:
- `refresh_data` (read, tool, emits `data.changed`)
- `get_report` (read, tool)
- `set_target` (action, tool, requires approval when the agent calls it)

To try it:
1. Open Settings → Plugin pages in project A, click **Install Sales example**, add the `sales` page, and click **Enable**.
2. Switch to project B and add and enable it again. Each project now has its own Sales page with independent data.
3. Open **Sales** in the left rail. Click **Refresh** to see the revision change live, or use **Analyze** to stream an analysis into the page.
4. Open the side chat and ask "Refresh the data and summarize it". The agent calls `page_refresh_data`, which is the same operation the button uses, and the page updates through the `data.changed` event.

## Adopting the interface in built-in pages (Analytics, Automations)

These pages are not retrofitted yet. To adopt the interface, a built-in page would do the following (no iframe or plugin package needed):

1. Implement `pageintegration.Integration` (`Ref()`, `Operations()`, `Invoke()`). Use a distinct `PageRef.Kind` such as `"analytics"`, the owning project ID, and handlers that call existing services.
2. When the page's selection, filters, or date range change, post them to a small endpoint that calls `ContextStore.Set(principal, ref, ctx, source)`. Never read them from the DOM.
3. For a side-chat turn, call `RenderContextForPrompt(ref, snapshot, ops)` and add the result to `SystemContext`. Pass `BuildRuntimeTools(integration, resolver, approvals, caller)` as `RuntimeTools`, where `resolver` re-authorizes the project on every call.
4. Publish `pageintegration.Event`s on a `Hub` after mutations so the page can refresh.

The side chat itself depends only on `pageintegration`, not on plugins or iframes.

## Known limitations

- Installation is from a local directory or the bundled example only. There is no upload and no signing.
- Backends are not OS-sandboxed (see above). Frontend-only plugins and builtins never execute code on the host.
- Approvals for agent actions and page contexts are kept in memory and are lost on restart; the agent is told the action was declined.
- The side chat uses the project's default agent selection and runs one turn at a time per instance.
