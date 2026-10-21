{
    const m = messages();
    m.dispatchEvent(new WheelEvent('wheel', {deltaY: -8, bubbles: true}));
    m.scrollTop = 160;
    await renderVisibleChatRows(m);
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    let input = 0, events = 0, cancelled = 0, loaded = false;
    m.addEventListener('wheel', event => {
        if (!event.isTrusted) return;
        input -= event.deltaY;
        events++;
        if (event.defaultPrevented) cancelled++;
    }, {passive: true});
    const bounds = m.getBoundingClientRect();
    window.webkit.messageHandlers.nativeWheel.postMessage({dy: -2, count: 600, x: bounds.left + 6, y: bounds.top + 75});
    for (let frame = 0; !window._nativeBurstDone || frame < 40; frame++) {
        if (frame > 1000) throw Error('Native wheel burst did not finish');
        const b = m.getBoundingClientRect(), ranges = [], before = m.scrollTop, startInput = input;
        // Whole-bubble bounds miss jumps within a bubble. Follow actual text at
        // several viewport positions, including partially clipped paragraphs.
        for (const y of [b.top + 24, b.top + 100, b.bottom - 30]) {
            for (let x = b.left + 30; x < b.right - 20; x += 90) {
                const range = document.caretRangeFromPoint(x, y);
                if (!range || range.startContainer.nodeType !== 3 || !range.startContainer.textContent.trim() ||
                    !m.contains(range.startContainer) || !range.startContainer.parentElement.closest('[data-execution-pair]')) continue;
                const rect = range.getBoundingClientRect();
                if (rect.height && Math.abs(rect.top - y) < 40) ranges.push({range, top: rect.top});
            }
        }
        await new Promise(resolve => requestAnimationFrame(resolve));
        const expected = Math.min(before, input - startInput);
        for (const {range, top} of ranges) {
            if (!range.startContainer.isConnected) throw Error('Reading text detached during native scroll');
            const shift = range.getBoundingClientRect().top - top - expected;
            // Allow event/frame coalescing, but not a page-sized offset overwrite.
            if (Math.abs(shift) > 10) throw Error('Native scroll moved reading text by ' + shift + 'px at frame ' + frame);
        }
        loaded ||= m.querySelectorAll(':scope > [data-execution-pair]').length > 5;
    }
    if (!loaded || events < 100 || cancelled === 0) throw Error('Native paging path was not exercised');

    // A nested tool scroller must retain native vertical and horizontal input.
    const tool = document.createElement('div');
    tool.style.cssText = `position:fixed;left:${bounds.left + 20}px;top:${bounds.top + 120}px;width:200px;height:100px;overflow:auto`;
    tool.innerHTML = '<div style="width:2000px;height:1000px">Scrollable tool output</div>';
    m.append(tool);
    await wait(200);
    const outerTop = m.scrollTop;
    async function wheel(delta) {
        let delivered;
        await new Promise((resolve, reject) => {
            const timer = setTimeout(() => reject(Error('Native nested wheel not delivered')), 1500);
            tool.addEventListener('wheel', function received(event) {
                if (!event.isTrusted) return;
                tool.removeEventListener('wheel', received);
                clearTimeout(timer);
                delivered = event;
                resolve();
            });
            window.webkit.messageHandlers.nativeWheel.postMessage({...delta, x: bounds.left + 50, y: bounds.top + 150});
        });
        await wait(100);
        if (delivered.defaultPrevented) throw Error('Nested tool wheel was intercepted');
    }
    await wheel({dy: 40});
    if (tool.scrollTop <= 0 || Math.abs(m.scrollTop - outerTop) > 2) throw Error('Nested vertical scroll: tool=' + tool.scrollTop + ', outer=' + outerTop + '->' + m.scrollTop);
    await wheel({dy: 0, dx: 40});
    if (tool.scrollLeft <= 0 || Math.abs(m.scrollTop - outerTop) > 2) throw Error('Nested horizontal scrolling changed');
    tool.remove();

    // Navigation destroys the old listener; the restored transcript gets one
    // handler, not none or multiple corrections for a single wheel event.
    const taskPath = location.pathname + location.search;
    await openVibelyNavigate('/tasks');
    await until(() => !messages(), 'left thread');
    await openVibelyNavigate(taskPath);
    await until(visible, 'returned thread');
    await wait(250);
    const returned = messages();
    returned.dispatchEvent(new WheelEvent('wheel', {deltaY: -8, bubbles: true}));
    returned.scrollTop = Math.max(0, returned.scrollHeight - returned.clientHeight - 300);
    await renderVisibleChatRows(returned);
    await wait(200);
    const top = returned.scrollTop, rect = returned.getBoundingClientRect();
    let delivered;
    const receive = event => { if (event.isTrusted) delivered = event; };
    returned.addEventListener('wheel', receive);
    window.webkit.messageHandlers.nativeWheel.postMessage({dy: -4, x: rect.left + 6, y: rect.top + 75});
    await until(() => delivered, 'wheel after return');
    await wait(100);
    returned.removeEventListener('wheel', receive);
    if (!delivered.defaultPrevented || Math.abs(returned.scrollTop - top + 4) > 1)
        throw Error('Restored wheel handler missing or duplicated: ' + top + ' -> ' + returned.scrollTop);
    if (errors.length) throw Error('Browser errors: ' + errors.join('; '));
    await fetch('/browser-result?status=pass');
    return;
}
