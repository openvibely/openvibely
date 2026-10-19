// Window transcript rows without changing execution identity or raw output.
// The mutable streaming tail stays mounted; sealed output can be windowed.
(function () {
    'use strict';
    const lists = new Map();
    let nextID = 0;
    let suspended = false;
    const rowSelector = '.chat-markdown, .stream-tool, details.stream-thinking, .chat-bubble-user-msg';

    function mount(row) {
        if (!row.placeholder.isConnected) return;
        row.placeholder.replaceWith(row.node);
        row.scroll?.forEach(saved => { saved.node.scrollTop = saved.top; saved.node.scrollLeft = saved.left; });
    }
    function destroy(list) {
        cancelAnimationFrame(list.frame);
        list.observer.disconnect();
        list.resize?.disconnect();
        list.messages.removeEventListener('scroll', list.schedule);
        list.rows.forEach(mount);
        lists.delete(list.messages);
    }
    function refresh(list) {
        list.frame = null;
        if (!list.messages.isConnected) { destroy(list); return; }
        if (suspended || list.selecting || list.messages.getAttribute('data-transcript-hydrating') === 'true') return;
        list.observer.disconnect();
        // Drop removed executions rather than retaining their detached output.
        list.rows.forEach((row, node) => {
            if (!node.isConnected && !row.placeholder.isConnected) list.rows.delete(node);
        });
        list.messages.querySelectorAll(rowSelector).forEach(node => {
            const pair = node.closest('[data-execution-pair]');
            if (!pair) return;
            const terminal = /^(completed|failed|cancelled)$/.test(pair.getAttribute('data-exec-status'));
            if (!terminal && (!node.closest('[data-chat-stable-segment]') || node.matches('details.stream-thinking'))) return;
            // Markdown inside thinking/tool rows belongs to that row.
            if (node.parentElement.closest(rowSelector)) return;
            if (list.rows.has(node)) return;
            const placeholder = document.createElement('div');
            placeholder.setAttribute('data-virtual-chat-row', String(++nextID));
            placeholder.setAttribute('aria-hidden', 'true');
            list.rows.set(node, { node: node, placeholder: placeholder });
        });
        const bounds = list.messages.getBoundingClientRect();
        const buffer = Math.max(600, list.messages.clientHeight);
        const selection = window.getSelection();
        const changes = [];
        let anchor = null;
        // Read geometry before changing any rows to avoid repeated layout passes.
        list.rows.forEach(row => {
            const mounted = row.node.isConnected;
            const el = mounted ? row.node : row.placeholder;
            const rect = el.getBoundingClientRect();
            if (!anchor && rect.bottom > bounds.top && rect.top < bounds.bottom) anchor = { row: row, top: rect.top };
            const protectedRow = mounted && (row.node.contains(document.activeElement) ||
                (selection && !selection.isCollapsed && selection.containsNode(row.node, true)) ||
                row.node.querySelector('video, audio, iframe, [data-chat-input-request-id]'));
            const visible = list.rows.size < 40 || protectedRow ||
                (rect.bottom >= bounds.top - buffer && rect.top <= bounds.bottom + buffer);
            if (visible && !mounted) changes.push(() => mount(row));
            if (!visible && mounted && rect.height > 0) {
                const style = getComputedStyle(row.node);
                row.placeholder.style.cssText = 'height:' + rect.height + 'px;min-height:' + rect.height +
                    'px;box-sizing:border-box;flex-shrink:0;margin-top:' + style.marginTop + ';margin-bottom:' + style.marginBottom + ';';
                row.scroll = Array.from(row.node.querySelectorAll('.stream-tool-body-scroll, pre'), node => ({ node: node, top: node.scrollTop, left: node.scrollLeft }));
                changes.push(() => row.node.replaceWith(row.placeholder));
            }
        });
        const pinned = list.messages.scrollHeight - list.messages.scrollTop - list.messages.clientHeight <= 2;
        changes.forEach(change => change());
        if (pinned) list.messages.scrollTop = list.messages.scrollHeight;
        else if (anchor) {
            const node = anchor.row.node.isConnected ? anchor.row.node : anchor.row.placeholder;
            list.messages.scrollTop += node.getBoundingClientRect().top - anchor.top;
        }
        list.observer.observe(list.messages, { childList: true, subtree: true });
        if (changes.length) list.schedule();
    }
    window.refreshVirtualChatRows = function (root) {
        lists.forEach(list => { if (!list.messages.isConnected) destroy(list); });
        const messages = root?.matches?.('#chat-messages, #task-thread-messages') ? root :
            root?.closest?.('#chat-messages, #task-thread-messages');
        if (!messages) return;
        let list = lists.get(messages);
        if (!list) {
            list = { messages: messages, rows: new Map(), frame: null };
            list.schedule = function () { if (list.frame === null) list.frame = requestAnimationFrame(() => refresh(list)); };
            list.observer = new MutationObserver(list.schedule);
            list.resize = typeof ResizeObserver === 'function' ? new ResizeObserver(list.schedule) : null;
            list.resize?.observe(messages);
            messages.addEventListener('scroll', list.schedule, { passive: true });
            lists.set(messages, list);
        }
        list.schedule();
    };
    window.materializeVirtualChatRows = function (root) {
        lists.forEach(list => {
            list.observer.disconnect();
            list.rows.forEach(row => {
                if (root.contains(row.placeholder)) mount(row);
            });
            list.schedule();
        });
    };
    window.cloneVirtualChatRows = function (source, clone) {
        lists.forEach(list => list.rows.forEach(row => {
            if (!source.contains(row.placeholder)) return;
            const id = row.placeholder.getAttribute('data-virtual-chat-row');
            clone.querySelector('[data-virtual-chat-row="' + id + '"]')?.replaceWith(row.node.cloneNode(true));
        }));
    };
    document.addEventListener('htmx:beforeSwap', function (event) {
        if (event.detail.shouldSwap !== false && event.detail.target) window.materializeVirtualChatRows(event.detail.target);
    });
    document.addEventListener('htmx:beforeHistorySave', function (event) {
        window.materializeVirtualChatRows(event.detail.historyElt);
    });
    document.addEventListener('htmx:afterSwap', function () {
        lists.forEach(list => { if (!list.messages.isConnected) destroy(list); else list.schedule(); });
    });
    // Native find, select-all, copying and printing must include offscreen text.
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
    window.addEventListener('resize', () => {
        // Re-measure wrapped text at the new width before removing rows again.
        window.materializeVirtualChatRows(document.body);
    });
})();
