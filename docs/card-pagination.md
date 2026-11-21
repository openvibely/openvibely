# Card-list pagination

The shared infinite-scroll loader uses stateless cursors. Initial HTML includes `data-card-pagination-cursor` on the pagination status element. Subsequent responses return `X-OpenVibely-Card-Cursor` and the existing has-more header. Search, filter, sort and project changes start a new list; loaded-window refreshes rebuild the visible window and return a fresh cursor.

## List inventory

| List | Paging implementation |
| --- | --- |
| Automations | SQLite seek predicate; per-source remote cursors and global merge |
| Alerts | SQLite seek predicate; per-source remote cursors and global merge |
| Models | SQLite seek predicate, also used when routed to a selected remote |
| Agents | SQLite seek predicate, also used when routed to a selected remote |
| Webhooks in Channels | SQLite seek predicate; per-source remote cursors and global merge |
| Skills | Cursor selection over the existing assembled filesystem/remote collection |
| Personalities | Cursor selection over the existing built-in/custom collection |
| Custom Pages | Already loads its complete collection and advertises no further pages; no offset paging to replace |

Fixed Channels cards and the pinned Personality card retain their existing behavior. Agent maintenance scans and non-card store APIs retain their offset interfaces. Older explicit offset requests are still accepted, but the current browser loader sends cursors.

## Ordering and continuation

`internal/cardcursor` defines sort terms shared by Go merging and SQLite seek queries. Name folding matches SQLite's ASCII-only `NOCASE`. Names and timestamps have an ID tie-breaker. Global merging additionally breaks identical keys by server ID. Timestamp keys normalize the supported SQLite and Go timestamp representations without losing nanoseconds.

Each source supplies at most the requested batch plus one lookahead row. Only positions of cards actually emitted to the browser advance. Unused rows are discarded and may be fetched again on the next request; there is no persistent result buffer or whole-list snapshot. A token contains one position per participating source, scoped to the user and the displayed list's workspace, normalized search, sort and supported filters. Initial loads, action refreshes and subsequent scroll requests use the same scope builder. Action-only parameters (such as a skill's owner or a replacement default model) are excluded; an aggregate action's temporary owner project does not replace the displayed workspace. A token is a position, not an authorization grant: normal query permissions still apply.

All Projects records its participating sources on the first request. A source that disconnects during scrolling causes a retryable page failure instead of silently advancing past its records. The browser keeps the previous cursor on failure. Refresh starts over with the currently connected sources. New remote read responses acknowledge cursor support; older servers are rejected for cursor reads rather than silently returning offset results.

Updates, additions and deletions during scrolling can still cause a skipped or repeated card. That is intentional for this version; refresh rebuilds the list. The browser also retains its existing duplicate-card suppression.

## Verification

Tests cover SQLite continuation for every supported sort, ties and Unicode names, sub-millisecond timestamps, Local HTTP continuation and loaded-window refresh, query-scope rejection, and two real remote sockets plus Local. The remote case includes 100 early names on one remote and checks all 150 records appear exactly once. Browser tests cover scrolling, replacement-window cursor continuation, search reset, stale responses, retry and focus preservation. HTTP regression tests traverse the refreshed list after automation run/pause/resume, skill changes, personality selection and default-model deletion, checking that the same cards appear exactly once. Scope tests also verify that list filters still invalidate old cursors while action-only parameters do not.
