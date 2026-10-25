// Cached page shells reference the shared execution snapshots. HTMX owns
// requests, history, view cleanup, and authoritative replacements.
(function () {
    'use strict';
    const cache = window.openVibelyThreadCache;
    const entries = cache ? cache.pages : new Map();
    const composerRefreshes = new WeakMap();
    const preservedScrolls = new WeakMap();
    const ttl = 30 * 60 * 1000;
    const maxBytes = 64 * 1024 * 1024;
    const maxEntries = 20;
    let activeURL = location.pathname + location.search;
    let pending = null;
    let historySnapshot = null;
    let mutation = 0;
    let restoringEntry = null;
    let clearRefreshPreview = null;

    // Hold only the already-mounted view during hydration of a changed snapshot.
    // This is a visual handoff, never a source of conversation or streaming state.
    function holdRefreshPreview(messages, xhr) {
        clearRefreshPreview?.();
        if (!messages?.checkVisibility?.({visibilityProperty: true}) || !messages.clientHeight) return;
        const bounds = messages.getBoundingClientRect();
        const preview = messages.cloneNode(true);
        preview.removeAttribute('id');
        preview.removeAttribute('data-transcript-hydrating');
        preview.setAttribute('data-thread-refresh-preview', 'true');
        preview.setAttribute('aria-hidden', 'true');
        preview.inert = true;
        preview.querySelectorAll('script, iframe').forEach(el => el.remove());
        [preview, ...preview.querySelectorAll('*')].forEach(el => {
            Array.from(el.attributes).forEach(attr => {
                if (attr.name === 'id' || attr.name.startsWith('hx-') || attr.name.startsWith('on') ||
                    (attr.name.startsWith('data-') && attr.name !== 'data-thread-refresh-preview')) el.removeAttribute(attr.name);
            });
        });
        Object.assign(preview.style, {position: 'fixed', top: bounds.top + 'px', left: bounds.left + 'px',
            width: bounds.width + 'px', height: bounds.height + 'px', boxSizing: 'border-box',
            margin: '0', zIndex: '10', pointerEvents: 'none', visibility: 'visible'});
        document.body.appendChild(preview);
        preview.scrollTop = messages.scrollTop;
        let observer = null, timer = null;
        const cleanup = () => {
            observer?.disconnect();
            clearTimeout(timer);
            preview.remove();
            document.removeEventListener('htmx:afterSwap', swapped, true);
            if (clearRefreshPreview === cleanup) clearRefreshPreview = null;
        };
        const swapped = event => {
            if (event.detail.xhr !== xhr || event.detail.target?.id !== 'main-content') return;
            const next = event.detail.target.querySelector('#task-thread-messages, #chat-messages');
            const ready = () => {
                if (!next?.isConnected || !next.hasAttribute('data-transcript-hydrating')) cleanup();
            };
            if (next) {
                observer = new MutationObserver(ready);
                observer.observe(next, {attributes: true, attributeFilter: ['data-transcript-hydrating']});
            }
            ready();
        };
        clearRefreshPreview = cleanup;
        document.addEventListener('htmx:afterSwap', swapped, true);
        // A failed/cancelled hydration must never leave an overlay behind.
        timer = setTimeout(cleanup, 10000);
    }
    window.addEventListener('pagehide', () => clearRefreshPreview?.());
    window.addEventListener('resize', () => clearRefreshPreview?.());
    // If the reader interacts during hydration, yield to the live page rather
    // than leaving a static view over a moving or hidden viewport.
    ['pointerdown', 'wheel', 'keydown'].forEach(type => {
        document.addEventListener(type, () => clearRefreshPreview?.(), {capture: true, passive: true});
    });


    window.openVibelyDisplayedURL = function () {
        return pending?.cached ? new URL(activeURL, location.href).href : location.href;
    };
    window.updateCachedThreadNavigationURL = function (url) {
        if (!pending?.cached) return false;
        const next = new URL(url, location.href);
        if (next.origin !== location.origin || next.pathname !== new URL(pending.url, location.href).pathname) return false;
        activeURL = next.pathname + next.search;
        pending.url = activeURL;
        // Workspace scripts read the displayed URL when the refreshed page mounts.
        // Keep the latest tab without fencing out fresh conversation state; only
        // server writes supersede that state.
        if (pending.source) pending.source.setAttribute('hx-push-url', activeURL);
        return true;
    };
    function restoreSource(request) {
        if (!request?.source) return;
        if (request.pushURL === null) request.source.removeAttribute('hx-push-url');
        else request.source.setAttribute('hx-push-url', request.pushURL);
    }

    function key(url) {
        const parsed = new URL(url, location.href);
        if (parsed.origin !== location.origin) return null;
        if (parsed.pathname !== '/chat' && !/^\/tasks\/[^/]+$/.test(parsed.pathname)) return null;
        // Unscoped Chat URLs resolve through server-side current-project state.
        if (parsed.pathname === '/chat' && !parsed.searchParams.get('project_id')) return null;
        parsed.searchParams.sort();
        return parsed.pathname + parsed.search;
    }
    function transcript(root) {
        return root.querySelector('#task-thread-view, #chat-page-root');
    }
    function forgetExpired() {
        if (cache) { cache.sweep(); return; }
        let bytes = 0;
        entries.forEach(function (entry, id) {
            if (Date.now() - entry.time > ttl) entries.delete(id);
            else bytes += entry.bytes;
        });
        while (entries.size > maxEntries || bytes > maxBytes) {
            const id = entries.keys().next().value;
            bytes -= entries.get(id).bytes;
            entries.delete(id);
        }
    }
    window.findCachedChatVirtualData = function (key, source) {
        for (const entry of entries.values()) {
            if (!cache && Date.now() - entry.time > ttl) continue;
            const data = entry.virtual?.find(data => data.key === key && (data.source === source || data.normalized === source));
            if (data) return data;
        }
        return null;
    };
    // Called by the cached shell before its normal thread initialization scripts.
    // Restore strings directly into DOM properties/attributes, never through HTML.
    window.restoreCachedThreadSources = function (root) {
        if (!restoringEntry) return;
        root.querySelectorAll('[data-cached-thread-source]').forEach(node => {
            const source = restoringEntry.sources[Number(node.getAttribute('data-cached-thread-source'))];
            if (!source) return;
            if (source.kind === 'text') node.textContent = source.text;
            else {
                node.setAttribute('data-raw-content', source.text);
                if (node.hasAttribute('data-initial-byte-length')) node.setAttribute('data-initial-byte-length', String(source.byteLength ?? new TextEncoder().encode(source.text).length));
            }
            node.removeAttribute('data-cached-thread-source');
        });
        root.querySelector('[data-cached-thread-bootstrap]')?.remove();
    };
    function save(main) {
        const id = key(activeURL);
        const thread = transcript(main);
        if (!id || !thread) return;
        // Attachment selections and unresolved input controls have independent
        // lifetimes. Let their normal server/draft restoration handle navigation.
        if (Array.from(main.querySelectorAll('input[type="file"]')).some(input => input.files?.length)) return;
        if (Array.from(main.querySelectorAll('input[name="attachment_session_id"]')).some(input => input.value)) return;
        const clone = main.cloneNode(true);
        // A cached composer is a new DOM instance and needs its own focus attempt.
        clone.querySelectorAll('[data-composer-focus-initialized]').forEach(input => {
            input.removeAttribute('data-composer-focus-initialized');
        });
        clone.querySelectorAll('#chat-messages, #task-thread-messages').forEach(messages => {
            // Older pages belong to the current reading session, not the shared
            // navigation cache. Rebuild the paging cursor for the retained window.
            messages.setAttribute('data-window-limit', '5');
            if (window.pruneChatExecutionWindow) window.pruneChatExecutionWindow(messages);
            delete messages.dataset.earlierLoaderBound;
            messages.dataset.earlierRequestLoading = 'false';
            messages.dataset.earlierAnchorRestoring = 'false';
            messages.dataset.earlierGestureLocked = 'false';
            messages.querySelectorAll('[data-earlier-loader]').forEach(loader => {
                loader.dataset.loading = 'false';
                loader.querySelector('[data-earlier-loader-idle]')?.classList.remove('hidden');
                loader.querySelector('[data-earlier-loader-busy]')?.classList.add('hidden');
            });
        });
        if (window.cloneVirtualChatRows) window.cloneVirtualChatRows(main, clone);
        clone.querySelectorAll('.htmx-request').forEach(el => el.classList.remove('htmx-request'));
        clone.querySelectorAll('[data-disabled-by-htmx]').forEach(el => {
            el.removeAttribute('disabled');
            el.removeAttribute('data-disabled-by-htmx');
        });
        // Never replay stale preserve instructions across two different threads.
        clone.querySelectorAll('[hx-preserve]').forEach(el => el.removeAttribute('hx-preserve'));
        const retained = new Set(Array.from(clone.querySelectorAll('[data-execution-pair]'), pair => pair.getAttribute('data-exec-id')));
        const virtual = Array.from(main.querySelectorAll('[data-execution-pair]'))
            .filter(pair => retained.has(pair.getAttribute('data-exec-id')))
            .flatMap(pair => window.captureChatVirtualData?.(pair) || []);
        const sources = [];
        clone.querySelectorAll('[data-execution-pair] [data-raw-content], [data-execution-pair] .chat-bubble-user-msg .whitespace-pre-wrap').forEach(node => {
            // A live response can contain raw-content attributes on its stable
            // sections. The outer source already owns the complete response.
            if (!clone.contains(node) || node.parentElement.closest('[data-raw-content]')) return;
            const kind = node.hasAttribute('data-raw-content') ? 'output' : 'text';
            const liveKey = node.getAttribute('data-live-snapshot-key');
            const live = liveKey && cache?.source(liveKey);
            if (kind === 'output' && live) {
                // Discovery may still be replaying older bytes behind this HTTP
                // seed. Never replace the saved view with that shorter prefix.
                let seed = node.getAttribute('data-raw-content');
                const seedOffset = Number(node.getAttribute('data-initial-byte-length')) || new TextEncoder().encode(seed).length;
                function caughtUp() {
                    if (live.offset >= seedOffset || live.terminal) seed = null;
                    return seed === null;
                }
                sources.push({kind, liveKey,
                    get text() { return caughtUp() ? live.output : seed; },
                    get byteLength() { return caughtUp() ? live.offset : seedOffset; }});
            } else sources.push({kind, text: kind === 'output' ? node.getAttribute('data-raw-content') : node.textContent});
            node.replaceChildren();
            node.removeAttribute('data-raw-content');
            node.removeAttribute('data-rendered-revision');
            node.removeAttribute('data-chat-virtual-owner');
            node.setAttribute('data-cached-thread-source', String(sources.length - 1));
        });
        if (sources.length) {
            // This shell no longer contains rendered messages. Reveal it only
            // after the visible rows and scroll position are ready, just like a
            // cold transcript; otherwise readers see its height grow in stages.
            clone.querySelectorAll('#chat-messages, #task-thread-messages').forEach(messages => {
                messages.setAttribute('data-transcript-hydrating', 'true');
            });
            const bootstrap = document.createElement('script');
            bootstrap.setAttribute('data-cached-thread-bootstrap', '');
            bootstrap.textContent = 'window.restoreCachedThreadSources(document.currentScript.parentElement);';
            transcript(clone).prepend(bootstrap);
        }
        const html = clone.innerHTML;
        // Count each distinct retained string once. Sources and parsed models can
        // share strings; rendered message HTML is no longer retained at all.
        const strings = new Set([html]);
        sources.forEach(source => strings.add(source.text));
        let overhead = sources.length * 128;
        virtual.forEach(data => {
            strings.add(data.source); strings.add(data.normalized);
            data.models.forEach(model => model.segments.forEach(seg => {
                strings.add(seg.content || ''); strings.add(seg.resultOutput || ''); strings.add(seg.secondary || '');
                overhead += 128;
            }));
        });
        let bytes = overhead;
        strings.forEach(text => { bytes += text.length * 2; });
        entries.delete(id);
        const project = thread.getAttribute('data-project-id') || main.querySelector('[data-project-id]')?.getAttribute('data-project-id') || document.getElementById('project-selector')?.value || '';
        const task = thread.id === 'chat-page-root' ? 'chat' : thread.getAttribute('data-task-id');
        if (cache || bytes <= maxBytes) entries.set(id, { html: html, sources: sources, virtual: virtual, bytes: bytes, time: Date.now(),
            threadKey: cache && task ? cache.threadKey(project, task) : id, executionIDs: Array.from(retained),
            runKeys: sources.filter(source => source.liveKey).map(source => source.liveKey) });
        forgetExpired();
    }
    function releaseHistorySnapshot() {
        if (historySnapshot) historySnapshot.remove();
        historySnapshot = null;
        document.getElementById('main-content')?.setAttribute('hx-history-elt', '');
    }
    function keepOutgoingHistory(main) {
        if (historySnapshot) return;
        // A cached preview is displayed before HTMX receives the response. Keep
        // the outgoing page in an inert template so HTMX saves the correct page
        // under the old URL when it commits history. Template IDs/scripts are inert.
        historySnapshot = document.createElement('template');
        historySnapshot.setAttribute('hx-history-elt', '');
        if (main.matches('[hx-history="false" i], [data-hx-history="false" i]') ||
            main.querySelector('[hx-history="false" i], [data-hx-history="false" i]')) {
            historySnapshot.setAttribute('hx-history', 'false');
        }
        const clone = main.cloneNode(true);
        if (window.cloneVirtualChatRows) window.cloneVirtualChatRows(main, clone);
        historySnapshot.innerHTML = clone.innerHTML;
        main.removeAttribute('hx-history-elt');
        main.before(historySnapshot);
    }
    function noReplacement(detail) {
        // HTMX must still commit the navigation URL, even when the refreshed
        // transcript is unchanged or a local send superseded this response.
        const target = document.createElement('div');
        document.body.appendChild(target);
        detail.target = target;
        detail.swapOverride = 'none';
        // swap:none still processes hx-preserve and out-of-band elements. Drop
        // the stale markup too, or HTMX can detach live execution rows while
        // preparing a response that will never be inserted.
        detail.serverResponse = '';
        detail.shouldSwap = true;
        setTimeout(() => target.remove(), 0);
    }
    window.showCachedThreadNavigation = function (detail) {
        if (detail.requestConfig?.verb !== 'get') return;
        const main = detail.target;
        if (!pending && !historySnapshot) activeURL = location.pathname + location.search;
        const url = detail.pathInfo?.finalRequestPath || detail.requestConfig.path;
        const id = key(url);
        save(main);
        forgetExpired();
        const entry = id && entries.get(id);
        pending = { xhr: detail.xhr, url: url, mutation: mutation, cached: false, source: detail.elt, pushURL: detail.elt?.getAttribute('hx-push-url') ?? null };
        if (!entry || key(activeURL) === id) return;
        // Non-thread tabs return a lazy shell; ask for the embedded transcript so
        // the refresh never replaces the preview with a loading placeholder.
        if (new URL(url, location.href).pathname.startsWith('/tasks/')) {
            detail.xhr.setRequestHeader('X-OpenVibely-Thread-Refresh', 'true');
        }
        keepOutgoingHistory(main);
        const preview = Object.assign({}, detail, {
            serverResponse: entry.html, shouldSwap: true, _ovCachedPreview: true
        });
        if (!htmx.trigger(main, 'htmx:beforeSwap', preview) || !preview.shouldSwap) return;
        pending.cached = true;
        activeURL = url;
        entries.delete(id);
        entries.set(id, entry);
        restoringEntry = entry;
        try {
            htmx.swap(main, entry.html, { swapStyle: 'innerHTML', settleDelay: 0 }, { eventInfo: preview });
        } finally {
            restoringEntry = null;
        }
        return true;
    };

    document.addEventListener('htmx:beforeSend', function (event) {
        const detail = event.detail;
        const verb = detail.requestConfig?.verb;
        const target = detail.target;
        const owner = target?.closest('#task-detail-content, #chat-page-root');
        if (verb === 'get' && owner) {
            // A late fragment/poll can outlive its view. Even a swap into a
            // detached target processes hx-preserve against the live document,
            // stealing matching executions from a subsequently reopened thread.
            // Fence by DOM ownership, not task ID: reopening the same task is a
            // different view. Listen on the source so detached sources work too.
            const source = detail.elt;
            const xhr = detail.xhr;
            const discardDetachedResponse = response => {
                if (response.detail.xhr === xhr &&
                    (!owner.isConnected || !target.isConnected || !owner.contains(target))) {
                    response.preventDefault();
                }
            };
            const cleanup = response => {
                if (response.detail.xhr !== xhr) return;
                source.removeEventListener('htmx:beforeOnLoad', discardDetachedResponse);
                source.removeEventListener('htmx:afterRequest', cleanup);
            };
            source.addEventListener('htmx:beforeOnLoad', discardDetachedResponse);
            source.addEventListener('htmx:afterRequest', cleanup);
        }
    }, true);
    document.addEventListener('htmx:beforeRequest', function (event) {
        const verb = event.detail.requestConfig?.verb;
        if (verb && verb !== 'get') {
            mutation++;
            entries.clear();
        }
    }, true);
    document.addEventListener('htmx:beforeSwap', function (event) {
        const detail = event.detail;
        if (detail.target?.matches?.('#main-content, #thread-content, #task-thread-view, #chat-page-root, #task-thread-messages, #chat-messages')) clearRefreshPreview?.();
        if (detail._ovCachedPreview || detail.target?.id !== 'main-content') return;
        if (!pending || pending.xhr !== detail.xhr) return;
        if (!pending.cached) return;
        if (detail.xhr.status < 200 || detail.xhr.status >= 300) {
            entries.delete(key(pending.url));
            // A deleted/inaccessible thread must not stay visible from cache.
            detail.shouldSwap = true;
            detail.serverResponse = '<p role="alert">This thread could not be refreshed. Please reload or choose another thread.</p>';
            return;
        }
        if (pending.mutation !== mutation) {
            noReplacement(detail);
            return;
        }
        const next = document.createElement('template');
        next.innerHTML = detail.serverResponse;
        const currentRoot = transcript(detail.target);
        const nextRoot = transcript(next.content);
        if (!currentRoot || !nextRoot || currentRoot.id !== nextRoot.id) return;
        const scope = currentRoot.id === 'task-thread-view' ? 'data-task-id' : 'data-project-id';
        if (currentRoot.getAttribute(scope) !== nextRoot.getAttribute(scope)) return;
        // Live question events can supersede this snapshot without an HTMX
        // write. Keep the current Chat state, but finish the navigation just as
        // we do for a local write; cancelling the swap would strand the old URL.
        if (currentRoot.id === 'chat-page-root' && typeof detail.xhr._chatInputRevision === 'number' &&
            detail.xhr._chatInputRevision !== (window._chatInputRevision || 0)) {
            noReplacement(detail);
            return;
        }
        detail._ovThreadRefresh = true;
        // Preserve unchanged execution DOM during an authoritative full-page swap.
        const pairs = new Map(Array.from(currentRoot.querySelectorAll('[data-exec-id][data-execution-revision]'), el => [el.id, el]));
        nextRoot.querySelectorAll('[data-exec-id][data-execution-revision]').forEach(el => {
            // Full thread markup marks terminal pairs preserved by default.
            // A cached pair can be older, so preserve only after comparing it.
            el.removeAttribute('hx-preserve');
            const old = pairs.get(el.id);
            // A fast response can arrive while cached output is still hydrating.
            // Swapping cancels that work; preserving its placeholders would leave
            // a transcript with neither rendered rows nor a renderer to fill them.
            const hydrated = old && Array.from(old.querySelectorAll('[data-raw-content]')).every(source => {
                const raw = source.getAttribute('data-raw-content') || '';
                return !raw.trim() || source._renderedRevision === (source.getAttribute('data-raw-revision') || raw);
            });
            if (hydrated && !old.querySelector('[data-chat-input-request-id]') && !el.querySelector('[data-chat-input-request-id]') && old.getAttribute('data-execution-revision') === el.getAttribute('data-execution-revision') &&
                /^(completed|failed|cancelled)$/.test(old.getAttribute('data-exec-status'))) {
                el.setAttribute('hx-preserve', 'true');
            }
        });
        // A warm refresh can contain the exact terminal transcript already on
        // screen. Preserve its scroll container as well as its execution rows:
        // replacing it briefly hides the page and resets scrollTop before the
        // new page scripts run. Compare the surrounding rows too so paging,
        // agent banners and queued inputs still receive server updates.
        const messagesSelector = currentRoot.id === 'task-thread-view' ? '#task-thread-messages' : '#chat-messages';
        const revisionAttribute = currentRoot.id === 'task-thread-view' ? 'data-thread-revision' : 'data-chat-revision';
        const currentMessages = currentRoot.querySelector(messagesSelector);
        const nextMessages = nextRoot.querySelector(messagesSelector);
        if (currentMessages && nextMessages &&
            !currentMessages.hasAttribute('data-transcript-hydrating') &&
            currentRoot.getAttribute(revisionAttribute) &&
            currentRoot.getAttribute(revisionAttribute) === nextRoot.getAttribute(revisionAttribute)) {
            const oldChildren = Array.from(currentMessages.children);
            const newChildren = Array.from(nextMessages.children);
            const unchanged = newChildren.length === oldChildren.length && newChildren.every((child, i) => {
                const old = oldChildren[i];
                if (child.matches('[data-execution-pair]')) {
                    return child.id === old.id && child.getAttribute('hx-preserve') === 'true';
                }
                return child.outerHTML === old.outerHTML;
            });
            if (unchanged) {
                preservedScrolls.set(detail.xhr, {messages: currentMessages, top: currentMessages.scrollTop});
                nextMessages.setAttribute('hx-preserve', 'true');
                nextMessages.querySelectorAll('[hx-preserve]').forEach(el => el.removeAttribute('hx-preserve'));
            }
        }
        if (nextMessages?.hasAttribute('data-transcript-hydrating') && nextMessages.getAttribute('hx-preserve') !== 'true') {
            holdRefreshPreview(currentMessages, detail.xhr);
        }
        // Keep the live draft, attachment controls and their handlers. Queue and
        // active-turn controls belong to the server and must still be refreshed.
        const composers = [];
        composerRefreshes.set(detail.xhr, composers);
        ['chat-form', 'task-thread-form'].forEach(id => {
            const form = next.content.querySelector('#' + id);
            const live = document.getElementById(id);
            if (!form || !live) return;
            form.setAttribute('hx-preserve', 'true');
            composers.push({
                form: live,
                regions: ['pending-thread-inputs', id + '-primary-action'].map(regionID => ({
                    id: regionID, html: form.querySelector('#' + regionID)?.outerHTML
                })),
                turn: form.querySelector('input[name="expected_turn_id"]')?.cloneNode(true)
            });
        });
        detail.serverResponse = next.innerHTML;
    }, true);
    document.addEventListener('htmx:afterSwap', function (event) {
        const scroll = preservedScrolls.get(event.detail.xhr);
        if (scroll && event.detail.target?.id === 'main-content') {
            // HTMX temporarily parks preserved nodes outside their layout. That
            // move can clamp scrollTop; restore it synchronously before paint.
            if (scroll.messages.isConnected) scroll.messages.scrollTop = scroll.top;
            preservedScrolls.delete(event.detail.xhr);
        }
        // Direct cached swaps do not carry eventInfo into afterSwap detail.
        // Mark them while restoration is synchronous, before page handlers run.
        if (restoringEntry && event.detail.target?.id === 'main-content') {
            event.detail._ovCachedPreview = true;
        }
        // HTMX does not carry custom beforeSwap fields into afterSwap. Restore
        // the refresh marker before task handlers decide whether to reset scroll.
        if (event.detail.target?.id === 'main-content' && composerRefreshes.has(event.detail.xhr)) {
            event.detail._ovThreadRefresh = true;
        }
    }, true);
    document.addEventListener('htmx:afterSwap', function (event) {
        if (event.detail._ovCachedPreview) return;
        if (event.detail.target?.id === 'main-content') {
            activeURL = pending?.url || location.pathname + location.search;
            // HTMX copies only selected beforeSwap fields into afterSwap detail.
            // Carry the reconciliation data by request identity instead.
            const composers = composerRefreshes.get(event.detail.xhr) || [];
            composerRefreshes.delete(event.detail.xhr);
            composers.forEach(update => {
                if (!update.form.isConnected) return;
                update.regions.forEach(region => {
                    const target = update.form.querySelector('#' + region.id);
                    if (target && region.html) {
                        // Use HTMX so fresh queue buttons receive request handlers
                        // and replaced controls get their usual cleanup.
                        htmx.swap(target, region.html, { swapStyle: 'outerHTML', settleDelay: 0 });
                    }
                });
                update.form.querySelector('input[name="expected_turn_id"]')?.remove();
                if (update.turn) update.form.appendChild(update.turn);
            });
        }
    });
    document.addEventListener('htmx:pushedIntoHistory', releaseHistorySnapshot);
    document.addEventListener('htmx:replacedInHistory', releaseHistorySnapshot);
    document.addEventListener('htmx:afterRequest', function (event) {
        if (event.detail.requestConfig?.verb && event.detail.requestConfig.verb !== 'get') entries.clear();
        if (pending?.xhr !== event.detail.xhr) return;
        const snapshot = historySnapshot;
        const xhr = pending.xhr;
        restoreSource(pending);
        pending = null;
        if (xhr.status !== 0) { releaseHistorySnapshot(); return; }
        // Aborting A in favor of B must retain the original history owner until
        // B commits. A genuine network failure restores that outgoing page.
        setTimeout(function () {
            if (pending || !snapshot || historySnapshot !== snapshot) return;
            const main = document.getElementById('main-content');
            const rollback = { target: main, xhr: xhr, serverResponse: snapshot.innerHTML, shouldSwap: true, _ovCachedPreview: true };
            if (htmx.trigger(main, 'htmx:beforeSwap', rollback) && rollback.shouldSwap) {
                htmx.swap(main, snapshot.innerHTML, { swapStyle: 'innerHTML', settleDelay: 0 }, { eventInfo: rollback });
            }
            activeURL = location.pathname + location.search;
            releaseHistorySnapshot();
        }, 0);
    });
    window.addEventListener('popstate', function () {
        restoreSource(pending);
        pending = null;
        activeURL = location.pathname + location.search;
        // Let HTMX save the outgoing snapshot before releasing the temporary owner.
        setTimeout(releaseHistorySnapshot, 0);
    }, true);
    document.addEventListener('htmx:historyRestore', function () {
        activeURL = location.pathname + location.search;
        releaseHistorySnapshot();
    });
    window.addEventListener('pagehide', () => entries.clear());
})();
