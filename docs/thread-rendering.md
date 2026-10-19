# Thread navigation and rendering

Normal HTMX navigation can show a previously visited thread before its GET completes. `web/assets/thread-navigation.js` keeps up to twenty page fragments with parsed row data and height measurements in memory, capped at 64 MiB of estimated string storage across the HTML and row data, with a thirty-minute idle expiry. It does not preload conversations or persist them to browser storage. Chat cache keys require an explicit `project_id`; task keys include the complete URL and query so different views do not collide.

The server response remains authoritative. Cached task navigation requests an embedded bounded transcript using `X-OpenVibely-Thread-Refresh`, so the page response replaces the preview directly instead of briefly showing the lazy loading shell. Cold task navigation retains its normal lazy load. Refreshes preserve unchanged terminal executions and the live composer draft and attachments, including edits made while waiting. The server's queue, send/stop controls and active-turn guard are reconciled into the preserved composer. Changing workspace tabs during a refresh keeps the selected view and still applies fresh conversation state. HTMX writes invalidate the cache; a write started after the navigation request also fences out that older response. Deleted/inaccessible threads cannot remain visible from a cached preview. Existing request cancellation prevents late responses from replacing newer navigation.

Restored visible HTML is hydrated again to rebuild tool-control event handlers. Matching source reuses cached row descriptions; changed source is projected again. Only a rendered revision held on a live DOM node can skip hydration; serialized revision attributes alone cannot.

While a preview is visible, an inert template retains the outgoing history element until HTMX commits navigation. This prevents saving the destination under the previous URL, including when a warm navigation is aborted by another navigation. Browser back/forward remains owned by HTMX.

`web/assets/chat-virtual-list.js` renders terminal executions and sealed streaming sections from lightweight row descriptions. The existing control-marker parser links tool calls/results and groups message text, thinking and compaction activity without creating their HTML. Only rows in the viewport plus 250 pixels above and below it receive Markdown parsing and tool DOM. Unvisited rows use estimated heights; visited rows retain their nodes and measurements. User bubbles and other already-rendered rows still use measured spacers once the list reaches 40 rows. Execution wrappers and raw source stay intact; the mutable streaming tail remains mounted.

Height corrections preserve the visible row or bottom pin. Width changes invalidate offscreen estimates and measure visible content without rendering the rest of the transcript. Focused rows and active media stay mounted. Selection, native find/select-all and printing explicitly materialize hidden rows; Escape resumes windowing after find. Navigation and history serialization retain source and placeholders without rendering unvisited rows. Composer history and keyboard bubble navigation only materialize existing user-bubble DOM. Detached transcripts release their observers and row references without remounting discarded rows.

Cold transcript history is revealed together after its visible window has rendered and height estimates have settled, without waiting for fonts or attachment images. Cached rendered content stays visible during hydration. Visibility uses the `data-transcript-hydrating` attribute rather than an inline style, so HTMX's delayed style settling cannot hide a completed transcript again. The existing layout observer follows late growth only while the user is pinned to the bottom. Deferred image-scroll callbacks recheck that the original transcript is still mounted and that the reader still wants to follow output.

## Limits

A cold open still fetches and parses the bounded execution history. Control-marker parsing still covers the loaded source, but Markdown parsing and HTML construction are deferred for offscreen rows. A single uninterrupted Markdown message remains one row and is fully parsed when that row enters the window. Visited offscreen nodes are retained for the lifetime of the mounted transcript. These changes do not add background subscriptions to unopened threads or replace snapshot refreshes with event-sequence synchronization.

## Validation

Browser coverage exercises cached display before a delayed response, authoritative refresh, draft preservation, back/forward, aborted requests, eviction/expiry, mutation invalidation, deleted threads, scroll anchoring during height corrections, remounting, native find, real tool rendering and continued incremental output. Lazy-rendering tests count Markdown calls, verify that large offscreen Markdown is not parsed, compare grouped output with the eager renderer, and exercise cached/history navigation through the real task endpoint. Existing Chat/task navigation and scroll tests also load the shipped scripts.

```sh
go test ./web/templates/pages -run 'TestBrowserFunctional_(ThreadCacheAndVirtualRows|TaskThreadNavigationScrollState|ChatNavigationStateAndActiveStream)InChrome' -count=1
go test ./web/templates/components -run 'TestBrowserFunctional_(LazyTranscript|VirtualizedCompletedTranscript|TranscriptScrollCoordinatorInChrome|IncrementalChatRendering|EarlierPrependPreservesMovingReader|EarlierLoaderContinuousUpwardInput)' -count=1
go test ./internal/handler -run '^TestTask(LazyCachedNavigationInChrome|CachedNavigationRefreshInChrome)$' -count=1
```
