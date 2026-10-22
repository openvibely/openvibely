// Project terminal/stable output into lightweight rows before rendering it.
// The live tail remains mounted; legacy/user DOM rows share the same window.
(function () {
    'use strict';
    const lists = new Map();
    let nextID = 0;
    let suspended = false;
    const rowSelector = '.chat-markdown, .stream-tool, details.stream-thinking, .chat-bubble-user-msg';
    const messagesSelector = '#chat-messages, #task-thread-messages';
    const element = row => row.node?.isConnected ? row.node : row.placeholder;
    const hasVisibleArea = messages => messages.isConnected && messages.clientWidth > 0 &&
        messages.clientHeight > 0 && messages.getClientRects().length > 0;

    function estimate(model, width) {
        // Hidden panels have no measurable width. Retain the last measurement
        // until reveal instead of expanding measured rows back into estimates.
        if ((model.width === width || width === 0) && model.height > 0) return model.height;
        if (model.kind === 'thinking') return 36;
        if (model.kind === 'using') return 180;
        if (model.kind === 'compaction') return 36;
        const text = model.segments.map(seg => seg.content || '').join('');
        const lines = Math.max(text.split('\n').length, Math.ceil(text.length / Math.max(30, width / 8)));
        return Math.max(28, Math.min(20000, lines * 24 + 16));
    }
    // Match the renderer's large-content threshold. Entering the viewport must
    // not cancel worker preparation and reparse an arbitrarily large row on the
    // main thread. Explicit Find/print materialization still controls its own mode.
    function canRenderImmediately(row) {
        if (!row.lazy || row.ready) return true;
        if (row.sourceSize === undefined) row.sourceSize = row.model.segments.reduce((size, segment) =>
            size + (segment.content || '').length + (segment.resultOutput || '').length + (segment.secondary || '').length, 0);
        return row.sourceSize < 64 * 1024;
    }
    function sizePlaceholder(row, height, margins) {
        row.placeholder.style.cssText = 'height:' + height + 'px;min-height:' + height +
            'px;box-sizing:border-box;flex-shrink:0;margin-top:' + (margins?.marginTop || '0px') + ';margin-bottom:' + (margins?.marginBottom || '0px') + ';';
    }
    function saveAnchor(list, followMovement) {
        const messages = list.messages;
        // Hidden history may still be parsed, but zero-sized geometry must not
        // replace the reading anchor saved before the workspace was hidden.
        if (!hasVisibleArea(messages)) return function () {};
        // Settle pending non-virtual changes (such as an earlier image loading)
        // before taking ownership of the next row-height adjustment.
        if (!followMovement) window.compensateChatEarlierLayout?.(messages);
        const bounds = messages.getBoundingClientRect();
        const scrollTop = messages.scrollTop;
        const pinned = messages.scrollHeight - messages.scrollTop - messages.clientHeight <= 2;
        let anchor = null;
        // While older history settles, newly inserted estimates can overlap the
        // viewport. Keep following the execution the reader was already viewing,
        // rather than switching the anchor to one of those temporary estimates.
        const earlierPair = messages._earlierAnchor?.pair;
        const earlierBounds = earlierPair?.isConnected ? earlierPair.getBoundingClientRect() : null;
        const readingPair = earlierBounds && earlierBounds.bottom > bounds.top && earlierBounds.top < bounds.bottom ? earlierPair : null;
        if (readingPair && earlierBounds.top >= bounds.top) anchor = {row:{node:readingPair}, top:earlierBounds.top, bottom:earlierBounds.bottom};
        // Map insertion order can differ from DOM order after prepending history.
        list.rows.forEach(row => {
            const el = element(row);
            if (!el.isConnected || (readingPair && !readingPair.contains(el))) return;
            const rect = el.getBoundingClientRect();
            const estimated = row.lazy && !row.ready;
            // Follow actual content already being read, not a newly encountered
            // estimate whose height can change drastically when it finishes.
            if (rect.bottom > bounds.top && rect.top < bounds.bottom &&
                (!anchor || (anchor.estimated && !estimated) || (Boolean(anchor.estimated) === Boolean(estimated) && rect.top < anchor.top))) {
                anchor = {row, top:rect.top, bottom:rect.bottom, estimated};
            }
        });
        // An unrendered row has no readable interior to anchor. Keep the nearest
        // edge of its estimate: scrolling in from below must retain its end,
        // not jump toward its beginning when the real response is much taller.
        // Geometry also handles small direction reversals and Home correctly.
        const edge = anchor?.estimated && anchor.top < bounds.top &&
            anchor.bottom - bounds.bottom < bounds.top - anchor.top ? 'bottom' : 'top';
        return function () {
            if (!hasVisibleArea(messages)) return false;
            // Shrinking content can clamp scrollTop even without user input.
            const maximum = Math.max(0, messages.scrollHeight - messages.clientHeight);
            const beforeScroll = messages.scrollTop;
            const movement = followMovement ? messages.scrollTop - Math.min(scrollTop, maximum) : 0;
            // A deferred resize must retain input since the last measured frame.
            // A jump to a different window or away from the bottom owns its own
            // position; do not pull the reader back to the previous anchor.
            if (followMovement && ((pinned && movement !== 0) || (!pinned &&
                (!anchor || anchor.estimated || anchor.bottom - movement <= bounds.top || anchor.top - movement >= bounds.bottom)))) return false;
            let restored = pinned;
            if (pinned) {
                // Do not restart the browser's ongoing boundary/overscroll
                // gesture when layout has left us at the bottom already.
                const bottom = messages.scrollHeight - messages.clientHeight;
                if (bottom - messages.scrollTop > 1) messages.scrollTop = bottom;
            } else if (anchor) {
                const row = anchor.row.lazy ? (anchor.row.owner._chatVirtualRows?.[anchor.row.index] || anchor.row) : anchor.row;
                if (messages.contains(element(row))) {
                    const viewportShift = messages.getBoundingClientRect().top - bounds.top;
                    const delta = element(row).getBoundingClientRect()[edge] - anchor[edge] - viewportShift + movement;
                    if (delta !== 0) messages.scrollTop += delta;
                    restored = true;
                }
            }
            // Height compensation is not a reversal of the reader's gesture.
            if (list.scrollTop !== undefined) list.scrollTop = Math.min(list.scrollTop, maximum) + messages.scrollTop - beforeScroll;
            if (restored) window.acknowledgeChatEarlierLayout?.(messages);
            if (!followMovement) list.lateAnchor = saveAnchor(list, true);
            return restored;
        };
    }
    // Capture before hiding a workspace. Async rows can finish while it has no
    // geometry; restore the same message relative to the viewport after reveal.
    window.captureChatVirtualScroll = function (messages) {
        if (!hasVisibleArea(messages)) return null;
        const list = ensureList(messages);
        collect(list);
        return saveAnchor(list);
    };
    window.preserveChatVirtualScroll = function (node, commit) {
        const list = lists.get(node.closest(messagesSelector));
        const restore = list && !list.bulk && saveAnchor(list);
        commit();
        if (restore) restore();
    };
    window.restoreChatVirtualRowScroll = function (node, restore) {
        const list = lists.get(node.closest(messagesSelector));
        if (list?.bulk) list.scrollRestores.push(restore);
        else restore();
    };
    function mount(row, restored, synchronous) {
        if (!row.placeholder.isConnected && !row.node?.isConnected) return;
        if (row.lazy && !row.ready && (!row.loading || synchronous)) {
            if (!row.node) {
                row.node = document.createElement('div');
                row.node.setAttribute('data-chat-lazy-row', row.index);
                row.node.style.display = 'flow-root';
                row.node.style.minHeight = row.placeholder.style.height;
            }
            if (row.placeholder.isConnected) row.placeholder.replaceWith(row.node);
            const generation = row.generation = (row.generation || 0) + 1;
            const finish = () => {
                if (row.generation !== generation) return;
                row.ready = true;
                row.loading = null;
                if (row.node.isConnected) row.node.style.minHeight = '';
                lists.get(row.owner.closest(messagesSelector))?.schedule();
            };
            try {
                const rendered = row.render(row.node, row.model, row.index, synchronous);
                if (rendered?.then) row.loading = Promise.resolve(rendered).then(finish, error => {
                    if (row.generation !== generation) return;
                    // Keep raw source accessible if a Markdown worker fails.
                    row.node.textContent = row.model.segments.map(seg => seg.content || seg.resultOutput || seg.secondary || '').join('\n');
                    row.node.style.whiteSpace = 'pre-wrap';
                    finish();
                    console.error('Transcript row rendering failed', error);
                });
                else finish();
            } catch (error) {
                row.node.textContent = row.model.segments.map(seg => seg.content || seg.resultOutput || '').join('\n');
                finish();
                console.error('Transcript row rendering failed', error);
            }
        } else if (row.placeholder.isConnected && row.node) {
            row.placeholder.replaceWith(row.node);
            restored.push(row);
        }
    }
    function restoreScroll(rows) {
        // Insert all retained DOM before restoring offsets: scroll writes force layout.
        rows.forEach(row => row.scroll?.forEach(saved => {
            saved.node.scrollTop = saved.top;
            saved.node.scrollLeft = saved.left;
        }));
    }
    function batchRowChanges(list, commit) {
        // The caller owns the viewport anchor for this frame. Synchronous row
        // renderers must not each adjust scrollTop while the user is scrolling.
        if (list.bulk) { commit(); return; }
        const scrollRestores = [];
        list.bulk = true;
        list.scrollRestores = scrollRestores;
        try { commit(); }
        finally {
            list.bulk = false;
            list.scrollRestores = null;
        }
        scrollRestores.forEach(restore => restore());
    }
    // WebKit's scrolling thread can apply an old absolute offset after a prepend
    // has already corrected scrollTop on the main thread. Handle vertical wheel
    // input in the same thread as virtual layout, including trackpad momentum.
    const webkitWheel = /AppleWebKit/.test(navigator.userAgent) && !/Chrome|Chromium|Edg\//.test(navigator.userAgent);
    function scrollWebKitTranscript(list, event) {
        if (!event.isTrusted || !event.cancelable || event.defaultPrevented || event.ctrlKey || event.metaKey || event.shiftKey ||
            !event.deltaY || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
        const messages = list.messages;
        for (let node = event.target instanceof Element ? event.target : event.target?.parentElement; node && node !== messages; node = node.parentElement) {
            if (node.scrollHeight <= node.clientHeight + 1) continue;
            const style = getComputedStyle(node);
            if (!/^(auto|scroll|overlay)$/.test(style.overflowY)) continue;
            if (style.overscrollBehaviorY === 'contain' || style.overscrollBehaviorY === 'none' ||
                (event.deltaY < 0 ? node.scrollTop > 0 : node.scrollTop < node.scrollHeight - node.clientHeight - 1)) return;
        }
        event.preventDefault();
        const unit = event.deltaMode === 1 ? (parseFloat(getComputedStyle(messages).lineHeight) || 16) :
            event.deltaMode === 2 ? messages.clientHeight : 1;
        const maximum = Math.max(0, messages.scrollHeight - messages.clientHeight);
        const target = Math.max(0, Math.min(maximum, messages.scrollTop + event.deltaY * unit + (list.wheelRemainder || 0)));
        messages.scrollTop = target;
        list.wheelRemainder = target === 0 || target === maximum ? 0 : target - messages.scrollTop;
    }
    function destroy(list) {
        cancelAnimationFrame(list.frame);
        list.observer.disconnect();
        list.resize?.disconnect();
        list.rowResize?.disconnect();
        list.messages.style.overflowAnchor = list.overflowAnchor;
        list.messages.removeEventListener('scroll', list.schedule);
        if (list.wheel) list.messages.removeEventListener('wheel', list.wheel);
        list.rows.clear();
        list.viewportReady?.(false);
        list.viewportReady = null;
        lists.delete(list.messages);
    }
    function ensureList(messages) {
        let list = lists.get(messages);
        if (!list) {
            list = {messages, rows:new Map(), frame:null, overflowAnchor:messages.style.overflowAnchor, observedRows:new Set()};
            // One owner compensates for height changes in both Chrome and WebKit.
            messages.style.overflowAnchor = 'none';
            list.schedule = function () {
                if (list.viewportReady && hasVisibleArea(messages)) {
                    list.viewportReady(true);
                    list.viewportReady = null;
                    list.viewportPromise = null;
                }
                if (list.frame === null) list.frame = requestAnimationFrame(() => refresh(list));
            };
            list.observer = new MutationObserver(list.schedule);
            list.resize = typeof ResizeObserver === 'function' ? new ResizeObserver(list.schedule) : null;
            list.resize?.observe(messages);
            list.rowResize = typeof ResizeObserver === 'function' ? new ResizeObserver(() => {
                // ResizeObserver runs before paint. Correct delayed image/diagram
                // layout now, not in a later animation frame after it has flashed.
                compensateLateLayout(list);
                list.schedule();
            }) : null;
            messages.addEventListener('scroll', list.schedule, {passive:true});
            if (webkitWheel) {
                list.wheel = event => scrollWebKitTranscript(list, event);
                messages.addEventListener('wheel', list.wheel, {passive:false});
            }
            lists.set(messages, list);
        }
        return list;
    }
    function collect(list) {
        // Preserved executions can move to a new transcript during an HTMX swap.
        list.messages.querySelectorAll('[data-chat-virtual-owner]').forEach(owner => {
            owner._chatVirtualRows?.forEach(row => list.rows.set(row.placeholder, row));
        });
        list.rows.forEach((row, key) => {
            if (!list.messages.contains(element(row)) || (row.lazy && row.owner._chatVirtualRows?.[row.index] !== row)) list.rows.delete(key);
        });
        list.messages.querySelectorAll(rowSelector).forEach(node => {
            if (node.closest('[data-chat-lazy-row]')) return;
            const pair = node.closest('[data-execution-pair]');
            if (!pair) return;
            const terminal = /^(completed|failed|cancelled)$/.test(pair.getAttribute('data-exec-status'));
            if (!terminal && (!node.closest('[data-chat-stable-segment]') || node.matches('details.stream-thinking'))) return;
            if (node.parentElement.closest(rowSelector) || list.rows.has(node)) return;
            const placeholder = document.createElement('div');
            placeholder.setAttribute('data-virtual-chat-row', String(++nextID));
            placeholder.setAttribute('aria-hidden', 'true');
            list.rows.set(node, {node, placeholder});
        });
    }
    // Paging owns its prepend correction. Its resulting geometry must replace
    // the previous resize snapshot, or the next frame applies that change twice.
    window.rebaseChatVirtualScroll = function (messages) {
        const list = lists.get(messages);
        if (list) list.lateAnchor = saveAnchor(list, true);
    };
    function compensateLateLayout(list) {
        if (!hasVisibleArea(list.messages)) { list.lateAnchor = null; return; }
        const restore = list.lateAnchor;
        list.lateAnchor = null;
        restore?.();
        list.lateAnchor = saveAnchor(list, true);
    }
    function observeRowLayout(list) {
        const mounted = new Set();
        list.rows.forEach(row => { if (row.node?.isConnected) mounted.add(row.node); });
        list.observedRows.forEach(node => { if (!mounted.has(node)) list.rowResize?.unobserve(node); });
        mounted.forEach(node => { if (!list.observedRows.has(node)) list.rowResize?.observe(node); });
        list.observedRows = mounted;
        list.lateAnchor = saveAnchor(list, true);
    }
    function refresh(list, hydrating) {
        cancelAnimationFrame(list.frame);
        list.frame = null;
        if (!list.messages.isConnected) { destroy(list); return []; }
        if (!hydrating && list.messages.getAttribute('data-transcript-hydrating') === 'true') return [];
        // Hidden workspaces have zero-sized rectangles for every row. Treating
        // those as intersections would render the entire transcript. ResizeObserver
        // schedules another pass when the chat area becomes visible again.
        if (!hasVisibleArea(list.messages)) return [];
        compensateLateLayout(list);
        list.observer.disconnect();
        collect(list);
        const width = list.messages.clientWidth;
        if (list.width !== width) {
            const restore = saveAnchor(list), restored = [];
            list.rows.forEach(row => {
                if (row.lazy) {
                    if (!row.node?.isConnected) sizePlaceholder(row, estimate(row.model, width));
                } else mount(row, restored);
            });
            restoreScroll(restored);
            restore();
            list.width = width;
        }
        const bounds = list.messages.getBoundingClientRect();
        const buffer = 250;
        // Start expensive rows ahead of the reader, while keeping the retained
        // DOM window small. A 250px window alone can enter and leave the screen
        // before asynchronous Markdown preparation has finished.
        const movement = hydrating || list.scrollTop === undefined ? 0 : list.messages.scrollTop - list.scrollTop;
        const lookahead = Math.min(2400, Math.max(list.messages.clientHeight * 2, Math.abs(movement) * 10));
        const prepareInitialLookahead = hydrating && list.messages.scrollHeight - list.messages.scrollTop - list.messages.clientHeight <= 2;
        const selection = window.getSelection();
        const changes = [], restored = [], pending = [];
        const restore = saveAnchor(list);
        list.rows.forEach(row => {
            const mounted = !!row.node?.isConnected;
            const el = element(row);
            const rect = el.getBoundingClientRect();
            const protectedRow = mounted && (row.loading || row.node.contains(document.activeElement) ||
                (selection && !selection.isCollapsed && selection.containsNode(row.node, true)) ||
                row.node.querySelector('video, audio, iframe, [data-chat-input-request-id]'));
            // Find/selection retain all rows, but must still render newly added
            // or updated content instead of freezing it as empty placeholders.
            const visible = suspended || list.selecting || (!row.lazy && list.rows.size < 40) || protectedRow ||
                (rect.bottom >= bounds.top - buffer && rect.top <= bounds.bottom + buffer);
            // Measure the first upward lookahead before revealing a bottom-opened
            // thread. Otherwise the first tiny gesture changes offscreen heights
            // and requires scroll corrections while WebKit is scrolling natively.
            // Keep oversized rows asynchronous/on demand and leave the rest lazy.
            const initialLookahead = prepareInitialLookahead && canRenderImmediately(row) && rect.bottom >= bounds.top - lookahead - buffer && rect.top < bounds.top;
            const approaching = row.lazy && !row.ready && !row.loading &&
                (initialLookahead || (movement < 0 && rect.bottom >= bounds.top - lookahead && rect.top <= bounds.bottom) ||
                 (movement > 0 && rect.top <= bounds.bottom + lookahead && rect.bottom >= bounds.top));
            // Finish small viewport rows before paint. Large rows keep their
            // asynchronous preparation even if scrolling overtakes the lookahead.
            const immediate = !hydrating && rect.bottom > bounds.top && rect.top < bounds.bottom && canRenderImmediately(row);
            if ((visible || approaching) && (!mounted || (row.lazy && !row.ready && (!row.loading || immediate)))) changes.push(() => mount(row, restored, immediate));
            if (mounted && row.lazy && row.ready && rect.height > 0) {
                row.model.height = rect.height;
                row.model.width = width;
                row.node.dataset.chatRowHeight = String(rect.height);
                row.node.dataset.chatRowWidth = String(width);
                row.placeholder.dataset.chatRowHeight = String(rect.height);
                row.placeholder.dataset.chatRowWidth = String(width);
            }
            if (!visible && mounted && rect.height > 0) {
                const style = getComputedStyle(row.node);
                sizePlaceholder(row, rect.height, style);
                row.scroll = Array.from(row.node.querySelectorAll('.stream-tool-body-scroll, pre'), node => ({node, top:node.scrollTop, left:node.scrollLeft}));
                changes.push(() => row.node.replaceWith(row.placeholder));
            }
        });
        batchRowChanges(list, () => changes.forEach(change => change()));
        restoreScroll(restored);
        restore();
        if (!hydrating) {
            // A measured row can shrink enough to pull further placeholders into
            // view. Settle small/retained rows in this frame. A large pending row
            // must be left to its worker rather than retried in this loop.
            for (let pass = 0; pass < list.rows.size; pass++) {
                const exposed = [];
                list.rows.forEach(row => {
                    if (row.node?.isConnected && (!row.lazy || row.ready || (row.loading && !canRenderImmediately(row)))) return;
                    const rect = element(row).getBoundingClientRect();
                    if (rect.bottom > bounds.top && rect.top < bounds.bottom) exposed.push(row);
                });
                if (!exposed.length) break;
                const keepPosition = saveAnchor(list), newlyMounted = [];
                batchRowChanges(list, () => exposed.forEach(row => mount(row, newlyMounted, canRenderImmediately(row))));
                restoreScroll(newlyMounted);
                keepPosition();
            }
        }
        observeRowLayout(list);
        list.scrollTop = list.messages.scrollTop;
        list.rows.forEach(row => { if (row.loading) pending.push(row.loading); });
        list.observer.observe(list.messages, {childList:true, subtree:true});
        if (changes.length) list.schedule();
        return pending;
    }
    window.refreshVirtualChatRows = function (root) {
        lists.forEach(list => { if (!list.messages.isConnected) destroy(list); });
        const messages = root?.matches?.(messagesSelector) ? root : root?.closest?.(messagesSelector);
        if (messages) ensureList(messages).schedule();
    };
    // A hidden workspace has no viewport to render or restore scroll against.
    // Keep its initial hydration pending until resize makes that viewport real.
    window.waitForChatTranscriptViewport = function (messages) {
        if (!messages.isConnected) return Promise.resolve(false);
        if (hasVisibleArea(messages)) return Promise.resolve(true);
        const list = ensureList(messages);
        if (!list.viewportPromise) list.viewportPromise = new Promise(resolve => { list.viewportReady = resolve; });
        return list.viewportPromise;
    };
    window.renderVisibleChatRows = async function (messages) {
        const list = ensureList(messages);
        // New measurements can bring more rows into view. Fill that window before
        // revealing cold history; no Markdown work is done for the other rows.
        for (let pass = 0; pass <= list.rows.size + 1 && messages.isConnected; pass++) {
            const before = Array.from(list.rows.values()).filter(row => row.lazy && !row.ready).length;
            await Promise.all(refresh(list, true));
            const after = Array.from(list.rows.values()).filter(row => row.lazy && !row.ready).length;
            if (before === after) break;
        }
        return true;
    };
    window.installChatVirtualRows = function (owner, data, render) {
        const messages = owner.closest(messagesSelector);
        const list = ensureList(messages);
        const width = messages.clientWidth;
        const restore = saveAnchor(list);
        const old = new Map(Array.from(owner.querySelectorAll('[data-chat-lazy-row]'), el => [Number(el.getAttribute('data-chat-lazy-row')), el]));
        const fragment = document.createDocumentFragment();
        owner._chatVirtualData = data;
        owner.setAttribute('data-chat-virtual-owner', 'true');
        owner._chatVirtualRows = data.models.map((model, index) => {
            const placeholder = document.createElement('div');
            placeholder.setAttribute('data-virtual-chat-row', String(++nextID));
            placeholder.setAttribute('data-chat-lazy-row', String(index));
            placeholder.setAttribute('aria-hidden', 'true');
            const prior = old.get(index);
            const node = prior && !prior.hasAttribute('data-virtual-chat-row') ? prior : null;
            if (!model.height && prior?.dataset.chatRowHeight) {
                model.height = Number(prior.dataset.chatRowHeight);
                model.width = Number(prior.dataset.chatRowWidth);
            }
            const row = {lazy:true, owner, model, index, render, placeholder, node, ready:false};
            sizePlaceholder(row, estimate(model, width));
            fragment.appendChild(node || placeholder);
            list.rows.set(placeholder, row);
            return row;
        });
        owner.replaceChildren(fragment);
        restore();
        if (messages.getAttribute('data-transcript-hydrating') === 'true') return true;
        return window.renderVisibleChatRows(messages);
    };
    window.discardChatVirtualRows = function (root) {
        const owners = Array.from(root.querySelectorAll('[data-chat-virtual-owner]'));
        if (root.hasAttribute('data-chat-virtual-owner')) owners.push(root);
        owners.forEach(owner => {
            delete owner._chatVirtualData;
            delete owner._chatVirtualRows;
            owner.removeAttribute('data-chat-virtual-owner');
        });
    };
    window.captureChatVirtualData = function (root) {
        return Array.from(root.querySelectorAll('[data-chat-virtual-owner]'), owner => owner._chatVirtualData).filter(Boolean);
    };
    window.materializeVirtualChatRows = function (root, options) {
        const restored = [], pending = [], anchors = [], scrollRestores = [];
        lists.forEach(list => {
            if (!root.contains(list.messages) && !list.messages.contains(root)) return;
            if (options?.visibleOnly && !hasVisibleArea(list.messages)) return;
            list.observer.disconnect();
            collect(list);
            if (options?.renderLazy !== false) anchors.push(saveAnchor(list));
            list.bulk = true;
            list.scrollRestores = scrollRestores;
            try {
                list.rows.forEach(row => {
                    if (row.lazy && options?.renderLazy === false) return;
                    if (root.contains(element(row))) mount(row, restored, true);
                    if (row.loading) pending.push(row.loading);
                });
            } finally {
                list.bulk = false;
                list.scrollRestores = null;
            }
            list.schedule();
        });
        scrollRestores.forEach(restore => restore());
        restoreScroll(restored);
        anchors.forEach(restore => restore());
        return Promise.all(pending);
    };
    window.cloneVirtualChatRows = function (source, clone) {
        const placeholders = new Map(Array.from(clone.querySelectorAll('[data-virtual-chat-row]'), el => [el.getAttribute('data-virtual-chat-row'), el]));
        lists.forEach(list => list.rows.forEach(row => {
            // Raw source and measured placeholders are sufficient to restore a
            // projected row. Never render hidden rows just to cache/navigation.
            if (row.lazy || !source.contains(row.placeholder)) return;
            placeholders.get(row.placeholder.getAttribute('data-virtual-chat-row'))?.replaceWith(row.node.cloneNode(true));
        }));
    };
    document.addEventListener('htmx:beforeSwap', function (event) {
        if (event.detail.shouldSwap === false || !event.detail.target) return;
        if (event.detail.target.id === 'main-content') suspended = false;
        window.materializeVirtualChatRows(event.detail.target, {renderLazy:false});
    });
    document.addEventListener('htmx:historyRestore', function () {
        suspended = false;
        lists.forEach(list => list.schedule());
    });
    document.addEventListener('htmx:beforeHistorySave', function (event) {
        window.materializeVirtualChatRows(event.detail.historyElt, {renderLazy:false});
    });
    document.addEventListener('htmx:afterSwap', function () {
        lists.forEach(list => { if (!list.messages.isConnected) destroy(list); else list.schedule(); });
        document.querySelectorAll(messagesSelector).forEach(messages => ensureList(messages).schedule());
    });
    document.addEventListener('keydown', function (event) {
        if ((event.ctrlKey || event.metaKey) && (event.key.toLowerCase() === 'f' ||
            (event.key.toLowerCase() === 'a' && !event.target.closest?.('input, textarea, [contenteditable="true"]')))) {
            // Find/Select All in Changes must neither expand hidden chat nor
            // suspend windowing when the reader returns to it.
            suspended = Array.from(lists.values()).some(list => hasVisibleArea(list.messages));
            window.materializeVirtualChatRows(document.body, {visibleOnly:true});
        } else if (event.key === 'Escape') {
            suspended = false;
            lists.forEach(list => list.schedule());
        }
    });
    document.addEventListener('selectionchange', function () {
        const selection = window.getSelection();
        lists.forEach(list => {
            const selecting = selection && !selection.isCollapsed &&
                (list.messages.contains(selection.anchorNode) || list.messages.contains(selection.focusNode));
            if (!!selecting === !!list.selecting) return;
            list.selecting = !!selecting;
            if (selecting) window.materializeVirtualChatRows(list.messages);
            else list.schedule();
        });
    });
    window.addEventListener('beforeprint', () => { suspended = true; window.materializeVirtualChatRows(document.body); });
    window.addEventListener('afterprint', () => { suspended = false; lists.forEach(list => list.schedule()); });
    window.addEventListener('resize', () => lists.forEach(list => list.schedule()));
})();
