// Fetch preceding complete response blocks as the reader scrolls upward.
// Navigation retains only the original latest window, not browsed history.
(function () {
    'use strict';
    const pending = new Map();
    const positions = new WeakMap();
    const selector = '.chat-stream-content[data-output-window]';
    function initialize(root) {
        root.querySelectorAll?.('[data-chat-transcript]').forEach(messages => {
            if (!positions.has(messages)) {
                positions.set(messages, messages.scrollTop);
                messages.dataset.outputCacheFirst = messages.querySelector(':scope > [data-execution-pair]')?.dataset.execId || '';
            }
        });
        root.querySelectorAll?.(selector).forEach(node => {
            if (!node.dataset.outputWindow || node._outputWindowSeed) return;
            node._outputWindowSeed = {
                text: node.getAttribute('data-raw-content') || '',
                cursor: node.dataset.outputWindow,
                revision: node.getAttribute('data-raw-revision') || ''
            };
        });
        for (const [node, controller] of pending) {
            if (!node.isConnected) { controller.abort(); pending.delete(node); }
        }
    }
    window.initChatOutputWindows = initialize;
    async function load(node, messages) {
        if (pending.has(node) || !node.dataset.outputWindow) return;
        const pair = node.closest('[data-execution-pair]');
        if (!pair) return;
        initialize(pair);
        let cursor = node.dataset.outputWindow;
        const project = pair.closest('[data-project-id]')?.dataset.projectId || document.getElementById('project-selector')?.value || '';
        const controller = new AbortController();
        pending.set(node, controller);
        const current = () => node.isConnected && !controller.signal.aborted && node.dataset.outputWindow === cursor;
        const request = (before, revision) => fetch('/chat/output/' + encodeURIComponent(pair.dataset.execId) + '?' +
            new URLSearchParams({before, revision, project_id: project}), {signal: controller.signal});
        async function apply(page, replace) {
            const height = messages.scrollHeight;
            const text = page.text + (replace ? '' : (node.getAttribute('data-raw-content') || ''));
            const rawRevision = window.setChatRawContent(node, text);
            cursor = page.before !== '0' ? page.before + ':' + page.revision : '';
            node.dataset.outputWindow = cursor;
            if (replace) {
                // Invalidate the old navigation seed too; never cache a mixture
                // of the obsolete response and newly fetched history.
                node._outputWindowSeed = {text, cursor, revision: rawRevision};
            }
            await window.renderStreamingContent(node, text);
            if (!current()) return;
            // Keep the visible suffix at the same reading position.
            messages.scrollTop += messages.scrollHeight - height;
            positions.set(messages, messages.scrollTop);
            window.rebaseChatVirtualScroll?.(messages);
        }
        try {
            let [before, revision] = cursor.split(':');
            let response = await request(before, revision);
            if (!current()) return;
            if (response.status === 409) {
                response = await request('0', '');
                if (!response.ok || !current()) return;
                const latest = await response.json();
                if (!current()) return;
                await apply(latest, true);
                if (!current() || !cursor) return;
                // Retry the user's upward request once with the refreshed cursor.
                // A second conflict waits for another gesture, rather than looping.
                [before, revision] = cursor.split(':');
                response = await request(before, revision);
            }
            if (!response.ok || !current()) return;
            const page = await response.json();
            if (!current() || page.revision !== revision) return;
            await apply(page, false);
        } catch (error) {
            if (error.name !== 'AbortError') console.debug('History load failed', error);
        } finally { pending.delete(node); }
    }
    function loadNearViewport(messages) {
        if (messages.dataset.transcriptHydrating === 'true') return;
        const bounds = messages.getBoundingClientRect();
        for (const node of messages.querySelectorAll(selector)) {
            if (!node.dataset.outputWindow) continue;
            const rect = node.getBoundingClientRect();
            if (rect.top >= bounds.top - 400 && rect.top <= bounds.bottom && rect.bottom > bounds.top) {
                void load(node, messages);
                break;
            }
        }
    }
    document.addEventListener('scroll', event => {
        const messages = event.target;
        if (!messages.matches?.('[data-chat-transcript]')) return;
        const previous = positions.get(messages);
        positions.set(messages, messages.scrollTop);
        if (previous !== undefined && messages.scrollTop < previous) loadNearViewport(messages);
    }, true);
    // An initial window shorter than the viewport cannot emit an upward scroll.
    // Still honor the reader's wheel/key request without adding paging controls.
    document.addEventListener('wheel', event => {
        if (event.deltaY >= 0) return;
        const messages = event.target.closest?.('[data-chat-transcript]');
        if (messages && messages.scrollTop <= 0) loadNearViewport(messages);
    }, {passive: true});
    const touchStarts = new WeakMap();
    document.addEventListener('touchstart', event => {
        const messages = event.target.closest?.('[data-chat-transcript]');
        if (messages && event.touches.length === 1) touchStarts.set(messages, event.touches[0].clientY);
    }, {passive: true});
    document.addEventListener('touchmove', event => {
        const messages = event.target.closest?.('[data-chat-transcript]');
        if (!messages || event.touches.length !== 1) return;
        const start = touchStarts.get(messages);
        // A downward finger movement asks to see earlier content. Do not
        // prevent native scrolling or require the current window to overflow.
        if (start !== undefined && event.touches[0].clientY > start + 12 && messages.scrollTop <= 0) {
            loadNearViewport(messages);
            touchStarts.delete(messages);
        }
    }, {passive: true});
    document.addEventListener('keydown', event => {
        if (!['ArrowUp', 'PageUp', 'Home'].includes(event.key) || event.target.closest?.('input, textarea, [contenteditable="true"]')) return;
        const messages = event.target.closest?.('[data-chat-transcript]');
        if (messages && messages.scrollTop <= 0) loadNearViewport(messages);
    });
    document.addEventListener('htmx:beforeCleanupElement', event => {
        const root = event.detail.elt;
        for (const [node, controller] of pending) {
            if (root === node || root.contains(node)) { controller.abort(); pending.delete(node); }
        }
    });
    document.addEventListener('htmx:afterSwap', () => initialize(document));
    document.addEventListener('DOMContentLoaded', () => initialize(document));
    initialize(document);
})();
