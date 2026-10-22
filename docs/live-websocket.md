# Browser and native live transport

Each document owns one WebSocket at `/events/ws`. Chat output, task-thread output,
project/task notifications, file changes, and system-update snapshots share it.
HTMX navigation detaches execution render listeners, not running execution
subscriptions. Follow-ups add execution subscriptions on the same socket.
Retargeting project notifications does not stop background output updates. Execution renderers capture the owning view's project ID, so
cached previews subscribe correctly while the URL and selector still refer to
the outgoing project. Each browser tab or native window has its own connection.
A full document reload necessarily creates a replacement connection.

HTTP still serves pages, history windows, fragments, reconciliation snapshots and
actions. Rendering, virtualization, draft ownership and scroll tracking remain
view-owned. Existing internal `sse-*` DOM event names and some
stream-handle variable names remain compatibility interfaces for those owners;
there are no EventSource constructors or browser-facing SSE routes in production.

## Thread snapshots and background updates

`thread-snapshots.js` owns the memory-only snapshot cache: page shells, raw
execution output, byte offsets and terminal state. `thread-navigation.js` saves
shells and references these output records rather than freezing another copy of
running output. Each execution has one transport subscription and detachable
view listeners. Navigation closes the listener only; reopening replays the
retained output to the renderer and uses the same subscription. No background
Markdown rendering or detached page DOM is required.

Authenticated `GET /events/running-executions` returns active execution IDs,
projects, task IDs and categories without transcript blobs. Discovery runs on
connection, relevant task events and every five seconds across all projects,
in browser tabs as well as desktop windows. It also finds runs never viewed in
that document. Missing IDs in discovery are not completion: execution streams
must deliver their final bytes and terminal event first. Transport capacity
limits still apply; a refused subscription is retried by discovery.

Background running and finished snapshots share the existing 20-thread and
64 MiB estimated-size budget, with at most five execution records per thread.
Only the displayed thread (or a mounted execution listener) is exempt while in
use. Idle expiry is 30 minutes after viewing/leaving, or initial admission for
never-viewed threads. Streaming, completion and discovery do not renew that age.
Eviction closes background subscriptions and releases their data; backend tasks
continue running. Discovery remembers only evicted active identifiers so it does
not immediately reload them. Opening the thread explicitly admits it again using
current HTTP output and a fresh subscription. Suppression identifiers are removed
when discovery no longer lists that active work. Cache storage is not persisted
to disk.
A new execution missing from a saved page invalidates that shell; the normal HTTP
load supplies its markup while output is already retained. HTTP remains necessary
for page/composer metadata and authoritative history reconciliation.

## Wire protocol

Commands are JSON objects with a unique `id` for each subscription generation:

```json
{"op":"live","id":"1","project_id":"project-id","task_id":"optional-task-id"}
{"op":"execution","id":"2","project_id":"project-id","exec_id":"execution-id","offset":123}
{"op":"unsubscribe","id":"2"}
```

The server sends `ready` once per connection. Live subscriptions acknowledge with
`subscribed`, then forward the existing broadcaster event types and payloads.
Execution frames have `id`, `event`, `data`, and UTF-8 byte `start`/`offset` cursors.
`message` carries output; `done` carries completed/cancelled status; `error` carries
execution errors or subscription rejection. Omitted byte fields mean zero.
`retry` means capacity is temporarily unavailable; the client retries only that
subscription with bounded backoff. `done` and `error` retire only their subscription.
Late frames for unsubscribed IDs are ignored. Detached view listeners do not
receive output, while their running execution subscription continues updating its snapshot.

The client retains applied offsets across network failures, uses jittered
exponential reconnect delays (250 ms initial, capped at 15 s plus jitter), and
resubscribes after `ready`. The execution worker subscribes to the existing hub
before reading persistence, deduplicates overlapping deltas, and reconciles gaps
and terminal state against persistence every two seconds. Replay frames are
bounded to 32 KiB of output. A terminal authoritative snapshot shorter than the
client's cursor produces `reset` before the terminal event, covering loss of an
unpersisted tail during a crash. Output that never reached persistence cannot be
recovered after process loss.

Shared broadcasters are intentionally not durable event logs. On reconnection,
existing UI reconciliation hooks fetch current task/chat/pending-input/file state
and alert state. A 30-second `reconcile` signal also repairs broadcaster drops.
This restores current state; it does not replay historical toast notifications.
`system_update` sends the full current coordinator snapshot on change, sampled
once a second, including an initial snapshot on every connection.

## Security and bounds

The endpoint uses the existing instance-wide session middleware and revalidates
the session on commands and every 15 seconds. Each subscription checks project
existence and execution/task membership in that project. Browser upgrades require
an exact scheme/host origin match. Missing, opaque, and foreign browser origins
are rejected. Reverse proxies must forward WebSocket upgrades and preserve the
public Host and request scheme, as used by the existing HTTP deployment.

Wails documents use a custom asset protocol that cannot proxy an HTTP upgrade.
They POST to `/events/native-ticket` through the authenticated native asset proxy
with a custom request header. Desktop mode, native proxy marking and origin are
required. The endpoint returns a loopback WebSocket URL and a random, one-use,
60-second ticket. The ticket travels in a WebSocket subprotocol, not a URL or
cookie exposed to JavaScript. Consumption is restricted to loopback peers and
Wails origins, and restores the original HTTP session cookie server-side for
normal authentication and subsequent session-expiry checks. Ticket requests have
a ten-second deadline covering both headers and body; timeouts abort the request
and retry without allowing late responses to open another socket. Tickets never
grant additional project access. The ticket store is bounded to 256 outstanding
entries.

Admission budgets are configurable at startup in both server and desktop modes:

| Environment variable | Default | Scope |
| --- | ---: | --- |
| `OPENVIBELY_LIVE_MAX_CONNECTIONS` | 500 | WebSockets per backend instance |
| `OPENVIBELY_LIVE_MAX_SUBSCRIPTIONS_PER_CONNECTION` | 200 | Live + execution subscriptions on one connection |
| `OPENVIBELY_LIVE_MAX_PROJECT_SUBSCRIPTIONS` | 5,000 | Live subscriptions across all projects and connections |
| `OPENVIBELY_LIVE_MAX_EXECUTION_SUBSCRIPTIONS` | 25,000 | Execution-output subscriptions across all executions and connections |

Values must be positive integers; unset, malformed, nonpositive, or overflowing
values fall back to the defaults. Changes require restarting the backend. Built-in
project tabs share the window's WebSocket; separate browser tabs/native windows
have separate connections. An execution-output subscription counts once per
watcher, even when several watchers receive the same run. These budgets do not
limit the number of agents that can run.

Keep `OPENVIBELY_LIVE_MAX_SUBSCRIPTIONS_PER_CONNECTION` above the background
cache's allowance of 20 threads × five executions (100 execution subscriptions),
with additional room for the displayed thread and live notifications. The default
of 200 provides that headroom for normal use. Displayed threads are exempt from
cache limits, so 100 is not a hard cap on all execution subscriptions.

The client does not reserve slots for the displayed thread or preempt background
subscriptions. If a lower connection limit is filled by background runs, opening
another thread can leave its live output waiting for capacity. Discovery retries
refused execution subscriptions, but does not free a slot; an existing subscription
must finish, be evicted, or otherwise close first. Backend tasks continue running
and saving output. Size this limit for the expected foreground and background use
rather than relying on retries to prioritize the displayed thread.

Each live subscription counts once, although it attaches to task, Chat, and
optionally file broadcasters. Their individual budgets match the live budget,
so the former 50-subscriber ceiling no longer applies. The execution hub uses its
own budget. Connections above the cap receive HTTP 503. Subscriptions above the
per-connection cap receive an error; exhausted shared live/output budgets send a
retry frame. Unsubscribe, completion, cancellation, and disconnect release the
corresponding resources.

The defaults allow 200 windows with 100 output subscriptions each on paper,
with headroom. They are resource safeguards, **not load-tested capacity claims**.
Background subscriptions and output caching for unviewed threads obey the
[snapshot cache limits](#thread-snapshots-and-background-updates) as well as these
transport budgets. Execution concurrency limits are unchanged.

There is a 4 KiB command limit and a 128-frame outbound queue per connection.
The writer and queue have five-second deadlines; stalled clients disconnect and
recover through replay.
WebSocket pings run every 15 seconds with a ten-second timeout. Closing a window
cancels its workers and releases broadcaster/hub subscriptions.

The obsolete routes `/events/live`, `/events/chat/:exec_id`, and
`/api/system/update/events` return 404.

## Validation and measurements

All database-backed tests use disposable test stores; no production database is
used or migrated.

- Real-server integration covers concurrent executions, follow-up subscriptions,
  cancellation/completion, reconnect replay, multiple independent clients, output
  gaps, large UTF-8 replay, failed execution output ordering, slow consumers,
  server restart with a fresh handler/hub, system-update progress during execution,
  origin rejection, cross-project/missing-execution rejection, and removed routes.
- Chromium runs the real transport and streaming components through navigation,
  follow-up, completion and forced disconnect, asserting one live socket per
  document, one replacement on reconnect, zero EventSource connections, recovered
  output/status, and preserved draft content.
- Existing browser fixtures cover Chat navigation/cache restoration, thread
  reconnects, terminal ordering, pending attachments, scroll intent, and Automation
  board reconciliation using the migrated transport or injected subscription handles.
- Native ticket integration covers session preservation, single-use enforcement,
  unauthenticated access and foreign-origin rejection. Native desktop unit tests
  run on macOS; an interactive packaged WebView smoke test on each supported OS
  remains a release validation step.

Initial local measurements on Darwin/arm64, Apple M5 Pro (`-benchtime=1s`):

| Measurement | Result |
| --- | --- |
| Loopback delivery of a 1 KiB execution frame, including JSON encode/decode and socket IO | 22.15 µs/frame; 46.23 MB/s; 2,716 B and 33 allocations/frame |
| HTTP project navigation, idle | 0.638 ms p95 |
| HTTP project navigation with 30 simulated execution writers and 3 project WebSockets | 0.782 ms p95; ~582 background operations/s; ~586 live events/s |
| Chromium connection observation with concurrent executions | 1 open WebSocket, 0 SSE connections; 1 replacement after forced disconnect |

The delivery benchmark measures transport, not model generation or browser paint.
Navigation allocations include background goroutines. These are local current-code
measurements, not a controlled SSE-versus-WebSocket latency comparison. JSON
subscription metadata adds bytes per output frame; the demonstrated reduction is
connection count (formerly a shared notification stream plus one output stream
per viewed execution), not a claim of universal bandwidth or latency improvement.

Reproduce with:

```sh
go test ./internal/handler -run 'TestLiveWebSocket|TestNativeLive' -count=1
go test -race ./internal/handler -run 'TestLiveWebSocket|TestNativeLive' -count=1
go test ./internal/handler -run '^$' -bench 'BenchmarkLiveWebSocketDelivery|BenchmarkProjectTabNavigationUnderLoad' -benchtime=1s
```
