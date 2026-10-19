// A small, memory-only cache for normal thread navigation. HTMX remains the
// owner of requests, history, stream teardown, and authoritative replacements.
(function () {
    'use strict';
    const entries = new Map();
    const composerRefreshes = new WeakMap();
    const ttl = 30 * 60 * 1000;
    const maxBytes = 64 * 1024 * 1024;
    const maxEntries = 20;
    let activeURL = location.pathname + location.search;
    let pending = null;
    let historySnapshot = null;
    let mutation = 0;

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
            if (Date.now() - entry.time > ttl) continue;
            const data = entry.virtual?.find(data => data.key === key && (data.source === source || data.normalized === source));
            if (data) return data;
        }
        return null;
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
        if (window.cloneVirtualChatRows) window.cloneVirtualChatRows(main, clone);
        clone.querySelectorAll('.htmx-request').forEach(el => el.classList.remove('htmx-request'));
        clone.querySelectorAll('[data-disabled-by-htmx]').forEach(el => {
            el.removeAttribute('disabled');
            el.removeAttribute('data-disabled-by-htmx');
        });
        // Never replay stale preserve instructions across two different threads.
        clone.querySelectorAll('[hx-preserve]').forEach(el => el.removeAttribute('hx-preserve'));
        const html = clone.innerHTML;
        const virtual = window.captureChatVirtualData?.(main) || [];
        // Budget source models along with serialized HTML. Measurements add only
        // a few numbers per row; model strings are conservatively counted again.
        let bytes = html.length * 2;
        virtual.forEach(data => {
            bytes += (data.source.length + data.normalized.length) * 2;
            data.models.forEach(model => model.segments.forEach(seg => {
                bytes += ((seg.content || '').length + (seg.resultOutput || '').length + (seg.secondary || '').length) * 2 + 128;
            }));
        });
        entries.delete(id);
        if (bytes <= maxBytes) entries.set(id, { html: html, virtual: virtual, bytes: bytes, time: Date.now() });
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
        // The normal task response is a lazy shell. A warm refresh needs the
        // bounded transcript in that response so it never replaces the preview
        // with a loading placeholder and discards the live composer.
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
        htmx.swap(main, entry.html, { swapStyle: 'innerHTML', settleDelay: 0 }, { eventInfo: preview });
    };

    document.addEventListener('htmx:beforeRequest', function (event) {
        const verb = event.detail.requestConfig?.verb;
        if (verb && verb !== 'get') {
            mutation++;
            entries.clear();
        }
    }, true);
    document.addEventListener('htmx:beforeSwap', function (event) {
        const detail = event.detail;
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
