// Project terminal/stable output into lightweight rows before rendering it.
// The live tail remains mounted; legacy/user DOM rows share the same window.
(function () {
    'use strict';
    const lists = new Map();
    let nextID = 0;
    let suspended = false;
    const rowSelector = '.stream-activity, .chat-markdown, .stream-tool, details.stream-thinking, .chat-bubble-user-msg';
    const messagesSelector = '[data-chat-transcript]';
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
    // Index DOM order only when membership changes. Search sibling intervals using
    // real layout, so margins, non-row content and delayed heights need no stale
    // prefix-height estimates. Nested virtual owners form their own sibling level.
    function indexRows(list) {
        // Explicit hydration can run before the mutation observer's microtask.
        if (list.observer.takeRecords().some(record => !record.target.closest?.('[data-chat-lazy-row]'))) list.dirty = true;
        if (!list.dirty) return;
        collect(list);
        const ordered = Array.from(list.rows.values()).sort((a, b) =>
            element(a).compareDocumentPosition(element(b)) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1);
        list.index = [];
        list.mounted = new Set();
        const stack = [];
        for (const row of ordered) {
            row.list = list;
            row.children = [];
            while (stack.length && !element(stack[stack.length - 1]).contains(element(row))) stack.pop();
            row.siblings = stack.length ? stack[stack.length - 1].children : list.index;
            row.siblings.push(row);
            stack.push(row);
            if (row.node?.isConnected) list.mounted.add(row);
        }
        list.dirty = false;
    }
    function windowRows(list, top, bottom) {
        indexRows(list);
        const found = new Map();
        function visit(rows) {
            let low = 0, high = rows.length;
            while (low < high) {
                const mid = (low + high) >>> 1;
                if (element(rows[mid]).getBoundingClientRect().bottom < top) low = mid + 1;
                else high = mid;
            }
            for (let i = low; i < rows.length; i++) {
                const row = rows[i], rect = element(row).getBoundingClientRect();
                if (rect.top > bottom) break;
                found.set(row, rect);
                if (row.children.length) visit(row.children);
            }
        }
        visit(list.index);
        return found;
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
        windowRows(list, bounds.top, bounds.bottom).forEach((rect, row) => {
            const el = element(row);
            if (!el.isConnected || (readingPair && !readingPair.contains(el))) return;
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
        row.list?.mounted.add(row);
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
                // The preference may have changed while an async row rendered.
                window.syncToolDisclosures?.(row.node);
                row.ready = true;
                row.loading = null;
                if (row.node.isConnected) row.node.style.minHeight = '';
                const list = lists.get(row.owner.closest(messagesSelector));
                if (list) { list.renderRevision = (list.renderRevision || 0) + 1; list.schedule(); }
                row.restoreFocus?.();
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
            // Retained rows miss document-wide preference updates. Reattach first:
            // deferred activity content needs a connected transcript to render.
            row.placeholder.replaceWith(row.node);
            // A detached outer row may contain virtual owners omitted by the
            // last membership scan. Rediscover them when their parent returns.
            if (row.node.querySelector('[data-chat-virtual-owner]')) row.list.dirty = true;
            window.syncToolDisclosures?.(row.node);
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
    // Virtual layout and history insertion correct scrollTop on the main thread.
    // Apply vertical wheel input there too: asynchronous native scrolling can
    // otherwise race those corrections (including WebView2 smooth scrolling).
    // Nested scrollers and modified/horizontal gestures retain native handling.
    function scrollTranscript(list, event) {
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
        list.messages.removeEventListener('scroll', list.onScroll);
        if (list.wheel) list.messages.removeEventListener('wheel', list.wheel);
        list.rows.clear();
        list.index = [];
        list.mounted.clear();
        list.observedRows.clear();
        list.viewportReady?.(false);
        list.viewportReady = null;
        lists.delete(list.messages);
    }
    function ensureList(messages) {
        let list = lists.get(messages);
        if (!list) {
            list = {messages, rows:new Map(), frame:null, overflowAnchor:messages.style.overflowAnchor, observedRows:new Set(), mounted:new Set(), dirty:true, index:[]};
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
            list.observer = new MutationObserver(records => {
                if (records.some(record => !record.target.closest?.('[data-chat-lazy-row]'))) list.dirty = true;
                list.schedule();
            });
            list.resize = typeof ResizeObserver === 'function' ? new ResizeObserver(list.schedule) : null;
            list.resize?.observe(messages);
            list.rowResize = typeof ResizeObserver === 'function' ? new ResizeObserver(() => {
                // ResizeObserver runs before paint. Correct delayed image/diagram
                // layout now, not in a later animation frame after it has flashed.
                compensateLateLayout(list);
                list.schedule();
            }) : null;
            list.onScroll = () => {
                list.schedule();
            };
            messages.addEventListener('scroll', list.onScroll, {passive:true});
            list.wheel = event => scrollTranscript(list, event);
            messages.addEventListener('wheel', list.wheel, {passive:false});
            lists.set(messages, list);
        }
        return list;
    }
    function collect(list) {
        list.dirty = true;
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
        list.mounted.forEach(row => {
            if (row.node?.isConnected) mounted.add(row.node);
            else list.mounted.delete(row);
        });
        list.observedRows.forEach(node => { if (!mounted.has(node)) list.rowResize?.unobserve(node); });
        mounted.forEach(node => { if (!list.observedRows.has(node)) list.rowResize?.observe(node); });
        list.observedRows = mounted;
        // The final restore already captured this layout for delayed resizing.
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
        indexRows(list);
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
        const nearby = windowRows(list,
            bounds.top - (movement < 0 || prepareInitialLookahead ? lookahead + buffer : buffer),
            bounds.bottom + (movement > 0 ? lookahead : buffer));
        const candidates = suspended || list.selecting || list.rows.size < 40 ? Array.from(list.rows.values()) :
            Array.from(new Set([...list.mounted, ...nearby.keys()]));
        candidates.forEach(row => {
            const mounted = !!row.node?.isConnected;
            const el = element(row);
            const rect = nearby.get(row) || el.getBoundingClientRect();
            const protectedRow = mounted && (row.loading || row.node.contains(document.activeElement) ||
                (selection && !selection.isCollapsed && selection.containsNode(row.node, true)) ||
                row.node.querySelector('video, audio, iframe, [data-file-html-expanded], [data-chat-input-request-id]'));
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
                changes.push(() => {
                    row.node.replaceWith(row.placeholder);
                    list.mounted.delete(row);
                });
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
                windowRows(list, bounds.top, bounds.bottom).forEach((rect, row) => {
                    if (row.node?.isConnected && (!row.lazy || row.ready || (row.loading && !canRenderImmediately(row)))) return;
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
        list.mounted.forEach(row => { if (row.loading) pending.push(row.loading); });
        list.observer.observe(list.messages, {childList:true, subtree:true});
        if (changes.length) list.schedule();
        return pending;
    }
    window.refreshVirtualChatRows = function (root) {
        lists.forEach(list => { if (!list.messages.isConnected) destroy(list); });
        const messages = root?.matches?.(messagesSelector) ? root : root?.closest?.(messagesSelector);
        if (messages) {
            const list = ensureList(messages);
            const node = root?.closest?.('[data-chat-lazy-row]');
            const owner = node?.parentElement?.closest('[data-chat-virtual-owner]');
            const row = owner?._chatVirtualRows?.[Number(node.getAttribute('data-chat-lazy-row'))];
            // Updating a leaf row changes layout, not index membership. Keep the
            // scheduled viewport/anchor refresh without recollecting all history.
            // Nested owners can add or remove rows: inspect both the current DOM
            // and the previous index so removal is not mistaken for a leaf edit.
            if (!row || row.node !== node || row.children?.length || node.querySelector('[data-chat-virtual-owner]')) {
                list.dirty = true;
            }
            list.schedule();
        }
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
            // Count completed renders rather than recounting every pending row.
            // A shrinking row can expose a different window with the same count.
            const before = list.renderRevision || 0;
            await Promise.all(refresh(list, true));
            if (before === (list.renderRevision || 0)) break;
        }
        return true;
    };
    window.regroupChatActivity = function () {
        document.querySelectorAll('[data-chat-virtual-owner]').forEach(owner => {
            const data = owner._chatVirtualData;
            const work = model => model.kind === 'using' || model.kind === 'thinking';
            if (!data?.ungrouped && owner._chatVirtualRender && data?.sourceModels?.some((model, index, all) => index > 0 && work(model) && work(all[index - 1]))) window.installChatVirtualRows(owner, data, owner._chatVirtualRender);
        });
    };
    // A retained row must not keep a previous cumulative projection alive through
    // an old renderer closure. Dispatch through its owner's current renderer.
    function renderCurrentRow(...args) {
        return this.owner._chatVirtualRender(...args);
    }
    window.installChatVirtualRows = function (owner, data, render) {
        const generation = owner._chatVirtualInstallGeneration = (owner._chatVirtualInstallGeneration || 0) + 1;
        const renderVersion = owner._streamRenderVersion;
        owner._chatVirtualRender = render;
        const compact = !data.ungrouped && document.documentElement.getAttribute('data-output-detail') === 'compact';
        data.sourceModels ||= data.models;
        if (!compact) {
            data.models = data.sourceModels;
            data.activityMode = false;
        } else if (data.activityMode !== compact) {
            let grouped = [], start = 0, checkpoints = [];
            const prior = owner._chatVirtualData;
            if (compact && data.incremental && prior?.activityMode && prior.groupCheckpoints) {
                // Keep aggregate counts at source-row boundaries. Appending to one
                // Compact group need not enumerate all its tools to rebuild its label.
                while (start < data.sourceModels.length && start < prior.sourceModels.length &&
                    data.sourceModels[start] === prior.sourceModels[start] &&
                    prior.groupCheckpoints[start].version === data.sourceModels[start].segments[0]?._version) start++;
                if (start) {
                    const saved = prior.groupCheckpoints[start - 1];
                    grouped = prior.models.slice(0, saved.group + 1);
                    const last = grouped[saved.group];
                    grouped[saved.group] = {...last, segments:last.segments.slice(0, saved.length), activitySummary:{...saved.summary}};
                    checkpoints = prior.groupCheckpoints.slice(0, start);
                }
            }
            for (let index = start; index < data.sourceModels.length; index++) {
                const model = data.sourceModels[index];
                let previous = grouped[grouped.length - 1];
                const work = model.kind === 'using' || model.kind === 'thinking';
                if (compact && work && previous && (previous.kind === 'using' || previous.kind === 'thinking')) {
                    previous.segments.push(...model.segments);
                    delete previous.height;
                } else {
                    previous = compact ? {...model, segments:model.segments.slice(), activitySummary:{count:0, failures:0, running:null}} : model;
                    grouped.push(previous);
                }
                if (compact) {
                    if (work) model.segments.forEach(seg => {
                        if (seg.type !== 'using') return;
                        const summary = previous.activitySummary;
                        summary.count++;
                        if (seg.resultStatus === 'error') summary.failures++;
                        if (!seg.completed && !summary.running) summary.running = seg;
                    });
                    checkpoints.push({group:grouped.length - 1, length:previous.segments.length,
                        summary:{...previous.activitySummary}, version:model.segments[0]?._version});
                }
            }
            data.groupCheckpoints = checkpoints;
            data.models = grouped;
            data.activityMode = compact;
        }

        const planned = window.OVFileViewer?.previewRows(owner, data.models) || new Map();
        function commit(previews) {
            // Link extraction may yield to a newer stream update or mode change.
            if (!owner.isConnected || generation !== owner._chatVirtualInstallGeneration || renderVersion !== owner._streamRenderVersion) return false;
            const messages = owner.closest(messagesSelector);
            const list = ensureList(messages);
            const width = messages.clientWidth;
            const delta = !compact && data.incremental && data._rowDelta?.base === owner._chatVirtualData &&
                owner._chatVirtualData?.activityMode === false ? data._rowDelta : null;
            delete data._rowDelta;
            // Incremental installation leaves existing row nodes connected. A
            // preview needs the relocation fallback only when its destination
            // differs from the row that already owns it.
            const stablePreviews = Array.from(previews).every(([index, node]) =>
                owner._chatVirtualRows?.[index]?.node?.contains(node));
            const incrementalMedia = stablePreviews && !owner.querySelector('.chat-svg-preview');
            const externalChanges = list.observer.takeRecords().some(record => !record.target.closest?.('[data-chat-lazy-row]'));
            if (externalChanges) list.dirty = true;
            list.observer.disconnect();
            const restore = saveAnchor(list);
            // Live projections retain completed model identities. Update changed rows
            // in place instead of detaching the entire response for every delta.
            const previous = owner._chatVirtualRows;
            if (data.incremental && previous && owner._chatVirtualData?.activityMode === data.activityMode && incrementalMedia) {
                owner._chatVirtualData = data;
                const rows = previous;
                const update = index => {
                    const model = data.models[index];
                    let row = previous[index];
                    const version = model.segments.length === 1 ? model.segments[0]._version : undefined;
                    if (row) {
                        if (row.model !== model || row.version !== version) {
                            row.generation = (row.generation || 0) + 1;
                            row.ready = false;
                            row.loading = null;
                            row.sourceSize = undefined;
                            if (!row.node?.isConnected) sizePlaceholder(row, estimate(model, width));
                        }
                        Object.assign(row, {model, version, render:renderCurrentRow});
                    } else {
                        const placeholder = document.createElement('div');
                        placeholder.setAttribute('data-virtual-chat-row', String(++nextID));
                        placeholder.setAttribute('data-chat-lazy-row', String(index));
                        placeholder.setAttribute('aria-hidden', 'true');
                        row = {lazy:true, owner, model, version, index, render:renderCurrentRow, placeholder, node:null, ready:false};
                        sizePlaceholder(row, estimate(model, width));
                        list.rows.set(placeholder, row);
                        owner.appendChild(placeholder);
                        const preceding = rows[index - 1];
                        if (!list.dirty && preceding?.siblings) {
                            row.list = list;
                            row.children = [];
                            row.siblings = preceding.siblings;
                            row.siblings.splice(row.siblings.indexOf(preceding) + 1, 0, row);
                        } else list.dirty = true;
                    }
                    rows[index] = row;
                };
                if (delta) {
                    delta.changed.forEach(index => { if (index < data.models.length) update(index); });
                    for (let index = delta.start; index < data.models.length; index++) update(index);
                } else data.models.forEach((model, index) => update(index));
                previous.slice(data.models.length).forEach(row => {
                    row.generation = (row.generation || 0) + 1;
                    list.rows.delete(row.placeholder);
                    list.mounted.delete(row);
                    if (row.siblings) {
                        const position = row.siblings.indexOf(row);
                        if (position >= 0) row.siblings.splice(position, 1);
                    } else list.dirty = true;
                    row.node?.remove(); row.placeholder.remove();
                });
                rows.length = data.models.length;
                owner._chatVirtualRows = rows;
                restore();
                list.observer.observe(messages, {childList:true, subtree:true});
                if (messages.getAttribute('data-transcript-hydrating') === 'true') return true;
                return window.renderVisibleChatRows(messages);
            }
            // Prepending a byte window shifts row indexes. Reusing those nodes would
            // apply one tool's DOM/scroll state to another; new nodes recover state
            // by stable tool ID. Ordinary refreshes retain their existing reuse path.
            const shiftedWindow = owner._outputWindowSeed && owner._chatVirtualData?.source !== data.source;
            const active = document.activeElement;
            let focusedActivityKey = owner.contains(active) && active.matches('.stream-activity-summary')
                ? active.parentElement.getAttribute('data-tool-disclosure-key') : null;
            // The fallback retains live-tail wrappers around preview rows, so those
            // rows can be nested. Modern engines keep the direct-child fast path.
            const priorRows = owner.moveBefore ? Array.from(owner.children).filter(el => el.hasAttribute('data-chat-lazy-row')) :
                Array.from(owner.querySelectorAll('[data-chat-lazy-row]')).filter(el => el.closest('[data-chat-virtual-owner]') === owner);
            const old = new Map(shiftedWindow ? [] : priorRows.map(el => [Number(el.getAttribute('data-chat-lazy-row')), el]));
            const previewNodes = new Set(previews.values());
            const previewAncestors = new Set();
            for (const preview of previewNodes) {
                for (let node = preview; node && node !== owner; node = node.parentElement) previewAncestors.add(node);
            }
            const fragment = document.createDocumentFragment();
            const reused = [];
            owner._chatVirtualData = data;
            owner.setAttribute('data-chat-virtual-owner', 'true');
            const previousRows = owner._chatVirtualRows || [];
            owner._chatVirtualRows = data.models.map((model, index) => {
                const previousRow = previousRows[index];
                const version = model.segments.length === 1 ? model.segments[0]._version : undefined;
                const unchanged = !shiftedWindow && previousRow?.model === model && previousRow.version === version;
                const placeholder = unchanged ? previousRow.placeholder : document.createElement('div');
                placeholder.setAttribute('data-virtual-chat-row', String(++nextID));
                placeholder.setAttribute('data-chat-lazy-row', String(index));
                placeholder.setAttribute('aria-hidden', 'true');
                const indexed = old.get(index);
                const prior = previews.get(index) || (unchanged && previousRow.node) || (previewAncestors.has(indexed) ? null : indexed);
                const node = prior && !prior.hasAttribute('data-virtual-chat-row') ? prior : null;
                if (!model.height && prior?.dataset.chatRowHeight) {
                    model.height = Number(prior.dataset.chatRowHeight);
                    model.width = Number(prior.dataset.chatRowWidth);
                }
                if (node) node.setAttribute('data-chat-lazy-row', String(index));
                const row = unchanged ? Object.assign(previousRow, {render:renderCurrentRow, node}) : {lazy:true, owner, model, index, render:renderCurrentRow, placeholder, node, ready:false, version};
                if (focusedActivityKey) row.restoreFocus = () => {
                    if (!focusedActivityKey || !row.node?.isConnected) return;
                    const fold = Array.from(row.node.querySelectorAll('[data-tool-disclosure-key]')).find(el => el.getAttribute('data-tool-disclosure-key') === focusedActivityKey);
                    if (!fold) return;
                    if (document.activeElement === document.body || document.activeElement === active) fold.querySelector('.stream-activity-summary').focus({preventScroll:true});
                    focusedActivityKey = null;
                };
                sizePlaceholder(row, estimate(model, width));
                if (node && owner.isConnected && (owner.moveBefore || previewNodes.has(node)) && window.OVFileViewer?.commitStream) {
                    const marker = document.createComment('retained row');
                    fragment.appendChild(marker);
                    (fragment._fileStreamMoves ||= []).push([marker, node]);
                    reused.push(node);
                } else fragment.appendChild(node || placeholder);
                list.rows.set(placeholder, row);
                return row;
            });
            // Keep reused rows connected, and preserve equations/previews from
            // discarded DOM for adoption when the new lazy rows render.
            const pool = window.OVFileViewer?.retainVirtual(owner, data.source, reused);
            window.retainChatMathForVirtualRows?.(owner, data.source, reused);
            if (pool) {
                const marker = document.createComment('pending previews');
                fragment.appendChild(marker);
                (fragment._fileStreamMoves ||= []).push([marker, pool]);
            }
            if (window.OVFileViewer?.commitStream) window.OVFileViewer.commitStream(fragment, owner);
            else owner.replaceChildren(fragment);
            list.dirty = true;
            restore();
            list.observer.observe(messages, {childList:true, subtree:true});
            if (messages.getAttribute('data-transcript-hydrating') === 'true') return true;
            return window.renderVisibleChatRows(messages);
        }
        return planned?.then ? planned.then(commit) : commit(planned);
    };
    window.discardChatVirtualRows = function (root) {
        const owners = Array.from(root.querySelectorAll('[data-chat-virtual-owner]'));
        if (root.hasAttribute('data-chat-virtual-owner')) owners.push(root);
        owners.forEach(owner => {
            const list = lists.get(owner.closest(messagesSelector));
            if (list) list.dirty = true;
            owner._chatVirtualInstallGeneration = (owner._chatVirtualInstallGeneration || 0) + 1;
            delete owner._chatVirtualData;
            delete owner._chatVirtualRows;
            delete owner._chatMathNodes;
            delete owner._chatMathSource;
            owner._filePreviewPool?.remove();
            delete owner._filePreviewPool;
            delete owner._filePreviewSource;
            owner.removeAttribute('data-chat-virtual-owner');
        });
    };
    window.captureChatVirtualData = function (root) {
        // Expanded activity bodies are derived from the outer transcript models.
        // They have no source/key and must not be retained as standalone snapshots.
        return Array.from(root.querySelectorAll('[data-chat-virtual-owner]'), owner => owner._chatVirtualData).filter(data => data && !data.ungrouped).map(data => {
            // Live FIFO matching updates existing tool models. Navigation snapshots
            // must not acquire later results while retaining an older source revision.
            if (!data.incremental) return data;
            return window.structuredClone ? structuredClone(data) : JSON.parse(JSON.stringify(data));
        });
    };
    // Query transcript content in display order without mounting offscreen rows.
    // Unrendered models can be projected by the caller into lightweight elements.
    window.queryChatVirtualElements = function (root, selector, projectModel) {
        const rows = new Map(), found = [];
        lists.forEach(list => {
            if (!root.contains(list.messages) && !list.messages.contains(root)) return;
            collect(list);
            list.rows.forEach(row => {
                rows.set(row.placeholder, row);
                if (row.node) rows.set(row.node, row);
            });
        });
        function visit(node, expanded) {
            const row = !expanded && rows.get(node);
            if (row) {
                if (row.lazy && !row.ready) found.push(projectModel(row.model));
                else if (row.node) visit(row.node, true);
                return;
            }
            if (node.matches(selector)) found.push(node);
            for (const child of node.children) visit(child, false);
        }
        visit(root, false);
        return Promise.all(found).then(parts => parts.flat());
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
        if (options?.synchronousThinking) root.querySelectorAll('details.stream-thinking').forEach(details => details._ensureThinkingContent?.(true));
        return Promise.all(pending).then(() => {
            if (options?.renderLazy !== false) return Promise.all(Array.from(root.querySelectorAll('details.stream-thinking'), details => details._ensureThinkingContent?.(options?.synchronousThinking)));
        });
    };
    window.cloneVirtualChatRows = function (source, clone) {
        clone.querySelectorAll('[data-chat-preview-pool]').forEach(pool => pool.remove());
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
            // Select All is retained by selectionchange only while text is
            // selected. Find needs independent suspension until Escape/navigation.
            // Do not clear an existing Find suspension when Select All is used.
            if (event.key.toLowerCase() === 'f') {
                suspended = Array.from(lists.values()).some(list => hasVisibleArea(list.messages));
            }
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
    window.addEventListener('beforeprint', () => { suspended = true; window.materializeVirtualChatRows(document.body, {synchronousThinking:true}); });
    window.addEventListener('afterprint', () => { suspended = false; lists.forEach(list => list.schedule()); });
    window.addEventListener('resize', () => lists.forEach(list => list.schedule()));
})();
