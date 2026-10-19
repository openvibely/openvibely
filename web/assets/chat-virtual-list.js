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

    function estimate(model, width) {
        if (model.width === width && model.height > 0) return model.height;
        if (model.kind === 'thinking') return 36;
        if (model.kind === 'using') return 180;
        if (model.kind === 'compaction') return 36;
        const text = model.segments.map(seg => seg.content || '').join('');
        const lines = Math.max(text.split('\n').length, Math.ceil(text.length / Math.max(30, width / 8)));
        return Math.max(28, Math.min(20000, lines * 24 + 16));
    }
    function sizePlaceholder(row, height, margins) {
        row.placeholder.style.cssText = 'height:' + height + 'px;min-height:' + height +
            'px;box-sizing:border-box;flex-shrink:0;margin-top:' + (margins?.marginTop || '0px') + ';margin-bottom:' + (margins?.marginBottom || '0px') + ';';
    }
    function saveAnchor(list) {
        const messages = list.messages;
        const bounds = messages.getBoundingClientRect();
        const pinned = messages.scrollHeight - messages.scrollTop - messages.clientHeight <= 2;
        let anchor = null;
        // Map insertion order can differ from DOM order after prepending history.
        list.rows.forEach(row => {
            const el = element(row);
            if (!el.isConnected) return;
            const rect = el.getBoundingClientRect();
            if (rect.bottom > bounds.top && rect.top < bounds.bottom && (!anchor || rect.top < anchor.top)) anchor = {row, top:rect.top};
        });
        return function () {
            if (pinned) messages.scrollTop = messages.scrollHeight;
            else if (anchor) {
                const row = anchor.row.lazy ? (anchor.row.owner._chatVirtualRows?.[anchor.row.index] || anchor.row) : anchor.row;
                if (element(row).isConnected) messages.scrollTop += element(row).getBoundingClientRect().top - anchor.top;
            }
        };
    }
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
            const finish = () => {
                row.ready = true;
                row.loading = null;
                if (row.node.isConnected) row.node.style.minHeight = '';
                lists.get(row.owner.closest(messagesSelector))?.schedule();
            };
            try {
                const rendered = row.render(row.node, row.model, row.index, synchronous);
                if (rendered?.then) row.loading = Promise.resolve(rendered).then(finish, error => {
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
    function destroy(list) {
        cancelAnimationFrame(list.frame);
        list.observer.disconnect();
        list.resize?.disconnect();
        list.messages.removeEventListener('scroll', list.schedule);
        list.rows.clear();
        lists.delete(list.messages);
    }
    function ensureList(messages) {
        let list = lists.get(messages);
        if (!list) {
            list = {messages, rows:new Map(), frame:null};
            list.schedule = function () {
                if (list.frame === null) list.frame = requestAnimationFrame(() => refresh(list));
            };
            list.observer = new MutationObserver(list.schedule);
            list.resize = typeof ResizeObserver === 'function' ? new ResizeObserver(list.schedule) : null;
            list.resize?.observe(messages);
            messages.addEventListener('scroll', list.schedule, {passive:true});
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
    function refresh(list, hydrating) {
        cancelAnimationFrame(list.frame);
        list.frame = null;
        if (!list.messages.isConnected) { destroy(list); return []; }
        if (suspended || list.selecting || (!hydrating && list.messages.getAttribute('data-transcript-hydrating') === 'true')) return [];
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
            const visible = (!row.lazy && list.rows.size < 40) || protectedRow ||
                (rect.bottom >= bounds.top - buffer && rect.top <= bounds.bottom + buffer);
            if (visible && (!mounted || (row.lazy && !row.ready))) changes.push(() => mount(row, restored));
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
        changes.forEach(change => change());
        restoreScroll(restored);
        restore();
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
        if (event.detail.shouldSwap !== false && event.detail.target) window.materializeVirtualChatRows(event.detail.target, {renderLazy:false});
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
            suspended = true;
            window.materializeVirtualChatRows(document.body);
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
