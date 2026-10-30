// Fetch preceding complete response blocks as the reader scrolls upward.
// Navigation retains only the original latest window, not browsed history.
(function () {
    'use strict';
    const pending = new Map();
    const positions = new WeakMap();
    const selector = '.chat-stream-content[data-output-window]';
    function initialize(root) {
        root.querySelectorAll?.('#chat-messages, #task-thread-messages').forEach(messages => {
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
        const [before, revision] = node.dataset.outputWindow.split(':');
        const project = pair.closest('[data-project-id]')?.dataset.projectId || document.getElementById('project-selector')?.value || '';
        const controller = new AbortController();
        pending.set(node, controller);
        const params = new URLSearchParams({ before, revision, project_id: project });
        try {
            const response = await fetch('/chat/output/' + encodeURIComponent(pair.dataset.execId) + '?' + params, {signal: controller.signal});
            if (!response.ok) return; // A subsequent upward scroll can retry transient failures.
            const page = await response.json();
            if (!node.isConnected || controller.signal.aborted || page.revision !== revision) return;
            const height = messages.scrollHeight;
            const text = page.text + (node.getAttribute('data-raw-content') || '');
            window.setChatRawContent(node, text);
            node.dataset.outputWindow = page.before !== '0' ? page.before + ':' + revision : '';
            await window.renderStreamingContent(node, text);
            if (!node.isConnected) return;
            // Keep the already visible suffix at the same reading position.
            messages.scrollTop += messages.scrollHeight - height;
            positions.set(messages, messages.scrollTop);
            window.rebaseChatVirtualScroll?.(messages);
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
        if (!messages.matches?.('#chat-messages, #task-thread-messages')) return;
        const previous = positions.get(messages);
        positions.set(messages, messages.scrollTop);
        if (previous !== undefined && messages.scrollTop < previous) loadNearViewport(messages);
    }, true);
    // An initial window shorter than the viewport cannot emit an upward scroll.
    // Still honor the reader's wheel/key request without adding paging controls.
    document.addEventListener('wheel', event => {
        if (event.deltaY >= 0) return;
        const messages = event.target.closest?.('#chat-messages, #task-thread-messages');
        if (messages && messages.scrollTop <= 0) loadNearViewport(messages);
    }, {passive: true});
    const touchStarts = new WeakMap();
    document.addEventListener('touchstart', event => {
        const messages = event.target.closest?.('#chat-messages, #task-thread-messages');
        if (messages && event.touches.length === 1) touchStarts.set(messages, event.touches[0].clientY);
    }, {passive: true});
    document.addEventListener('touchmove', event => {
        const messages = event.target.closest?.('#chat-messages, #task-thread-messages');
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
        const messages = event.target.closest?.('#chat-messages, #task-thread-messages');
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
