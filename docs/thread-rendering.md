# Thread navigation and rendering

Normal HTMX navigation can show a previously visited thread before its GET completes. `web/assets/thread-navigation.js` keeps up to twenty rendered page fragments in memory, capped at 64 MiB of estimated string storage, with a thirty-minute idle expiry. It does not preload conversations or persist them to browser storage. Chat cache keys require an explicit `project_id`; task keys include the complete URL and query so different views do not collide.

The server response remains authoritative. Refreshes preserve unchanged terminal executions and the current composer, including edits made while waiting. HTMX writes invalidate the cache; a write started after the navigation request also fences out that older response. Deleted/inaccessible threads cannot remain visible from a cached preview. Existing request cancellation prevents late responses from replacing newer navigation.

While a preview is visible, an inert template retains the outgoing history element until HTMX commits navigation. This prevents saving the destination under the previous URL, including when a warm navigation is aborted by another navigation. Browser back/forward remains owned by HTMX.

`web/assets/chat-virtual-list.js` windows long transcripts (40 or more eligible rows). It replaces offscreen message, thinking and tool rows with measured spacers, retaining the original nodes for remounting. The buffer is one viewport or 600 pixels, whichever is larger. Raw execution output and execution wrappers remain intact. Completed executions and sealed incremental output are eligible; the mutable streaming tail and its thinking state remain mounted.

Focused rows and active media stay mounted. Selection, native find/select-all and printing materialize rows; Escape resumes windowing after find. History serialization, composer history, bubble navigation, full rerenders and viewport resizing materialize rows as needed. Detached transcripts release their observers and row references.

Available transcript content is revealed without waiting for fonts or attachment images. The existing layout observer follows late growth only while the user is pinned to the bottom. Deferred image-scroll callbacks recheck that the original transcript is still mounted and that the reader still wants to follow output.

## Limits

A cold open still fetches and parses the bounded execution history. Windowing reduces mounted DOM and subsequent layout work; it does not avoid the first Markdown parse or discard retained offscreen nodes. A single large Markdown block remains one row. These changes do not add background subscriptions to unopened threads or replace snapshot refreshes with event-sequence synchronization.

## Validation

Browser coverage exercises cached display before a delayed response, authoritative refresh, draft preservation, back/forward, aborted requests, eviction/expiry, mutation invalidation, deleted threads, stable scroll height, remounting, native find, real tool rendering and continued incremental output. Existing Chat/task navigation and scroll tests also load the shipped scripts.

```sh
go test ./web/templates/pages -run 'TestBrowserFunctional_(ThreadCacheAndVirtualRows|TaskThreadNavigationScrollState|ChatNavigationStateAndActiveStream)InChrome' -count=1
go test ./web/templates/components -run 'TestBrowserFunctional_(VirtualizedCompletedTranscript|TranscriptScrollCoordinatorInChrome|IncrementalChatRendering|EarlierPrependPreservesMovingReader|EarlierLoaderContinuousUpwardInput)' -count=1
```
