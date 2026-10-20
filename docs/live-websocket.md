# Browser and native live transport

Each document owns one WebSocket at `/events/ws`. Chat output, task-thread output,
project/task notifications, file changes, and system-update snapshots share it.
HTMX navigation, follow-ups, and subsequent runs replace subscriptions, not the
socket. Execution cleanup belongs to the outgoing view when navigation commits;
retargeting notifications during a pending or failed project switch does not stop
that view's output. Execution renderers capture the owning view's project ID, so
cached previews subscribe correctly while the URL and selector still refer to
the outgoing project. Each browser tab or native window has its own connection.
A full document reload necessarily creates a replacement connection.

HTTP still serves pages, history windows, fragments, reconciliation snapshots and
actions. Rendering, virtualization, draft ownership, scroll tracking and thread
caches retain their existing owners. **This does not implement background updates
to cached, unviewed threads.** Existing internal `sse-*` DOM event names and some
stream-handle variable names remain compatibility interfaces for those owners;
there are no EventSource constructors or browser-facing SSE routes in production.

## Wire protocol

Commands are JSON objects with a unique `id` for each subscription/view generation:

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
Late frames for detached view IDs are ignored.

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

There are at most 64 subscriptions per connection, a 4 KiB command limit, and a
128-frame outbound queue. Existing broadcaster/hub subscriber limits (50) remain;
active socket count is also capped at 50 per handler. The writer and queue have
five-second deadlines; stalled clients disconnect and recover through replay.
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
