{
    await until(() => window.nativeTestInput, 'native input bridge');
    let wheelScale = 1;
    const nativeInput = {postMessage(data) {
        window.nativeTestInput.postMessage({...data,dy:(data.dy||0)*wheelScale});
    }};
    const webkit = /AppleWebKit/.test(navigator.userAgent) && !/Chrome|Chromium|Edg\//.test(navigator.userAgent);
    async function stableLayout(m) {
        let previous = '', stable = 0;
        for (let i=0;i<600;i++) {
            await new Promise(resolve => requestAnimationFrame(resolve));
            const state = [m.scrollTop,m.scrollHeight,m.clientHeight].join(',');
            const busy = m.dataset.transcriptHydrating === 'true' || m.dataset.earlierAnchorRestoring === 'true' ||
                window._chatContentRenderActive || window._chatContentRenderQueue?.length;
            stable = !busy && state === previous ? stable+1 : 0;
            if (stable >= 6) return;
            previous = state;
        }
        throw Error('Layout did not settle: '+debug());
    }
    const m = messages();
    if(document.fonts) await document.fonts.ready;
    m.dispatchEvent(new WheelEvent('wheel', {deltaY: -8, bubbles: true}));
    // Rendering a newly visited window can rebase its estimated heights. Place
    // the gesture after that work, otherwise CI can start thousands of pixels
    // from the page boundary despite the initial assignment of 160.
    for(let attempt=0;attempt<6;attempt++) {
        m.scrollTop = 160;
        await renderVisibleChatRows(m);
        await stableLayout(m);
        if(Math.abs(m.scrollTop-160)<1) break;
    }
    if(Math.abs(m.scrollTop-160)>=1) throw Error('Could not position native gesture: '+debug());
    // Native wheel units differ between AppKit, GDK and Windows settings.
    // Measure one trusted event rather than assuming the driver produces pixels
    // at the same scale on every runner. Use downward input away from the loader.
    let probe;
    const probeListener = event => { if(event.isTrusted) probe=event; };
    m.addEventListener('wheel',probeListener);
    const probeBounds=m.getBoundingClientRect();
    nativeInput.postMessage({dy:40,x:probeBounds.left+6,y:probeBounds.top+75});
    await until(()=>probe,'native calibration wheel');
    m.removeEventListener('wheel',probeListener);
    if(probe.deltaMode!==0 || !probe.deltaY) throw Error('Unsupported native wheel units: '+JSON.stringify({mode:probe.deltaMode,dy:probe.deltaY}));
    wheelScale=40/Math.abs(probe.deltaY);
    if(wheelScale<0.01 || wheelScale>256) throw Error('Invalid native wheel scale: '+wheelScale);
    await stableLayout(m);
    m.scrollTop=160;
    await stableLayout(m);
    if(Math.abs(m.scrollTop-160)>=1) throw Error('Native calibration changed starting window: '+debug());
    let input = 0, events = 0, cancelled = 0, loaded = false, measured = 0;
    m.addEventListener('wheel', event => {
        if (!event.isTrusted) return;
        input -= event.deltaY;
        events++;
        queueMicrotask(() => { if (event.defaultPrevented) cancelled++; });
    }, {passive: true});
    const bounds = m.getBoundingClientRect();
    nativeInput.postMessage({dy: -2, count: 600, x: bounds.left + 6, y: bounds.top + 75});
    let settledFrames = 0, previousLayout = '';
    for (let frame = 0; ; frame++) {
        if (frame > 1000) throw Error('Native paging did not settle: '+debug());
        const b = m.getBoundingClientRect(), ranges = [], before = m.scrollTop, startInput = input;
        // Whole-bubble bounds miss jumps within a bubble. Follow actual text at
        // several viewport positions, including partially clipped paragraphs.
        for (const y of [b.top + 24, b.top + 100, b.bottom - 30]) {
            for (let x = b.left + 30; x < b.right - 20; x += 90) {
                const range = (document.caretRangeFromPoint ? document.caretRangeFromPoint(x, y) : (() => { const pos = document.caretPositionFromPoint(x,y); if (!pos) return null; const range = document.createRange(); range.setStart(pos.offsetNode,pos.offset); range.collapse(true); return range; })());
                if (!range || range.startContainer.nodeType !== 3 || !range.startContainer.textContent.trim() ||
                    !m.contains(range.startContainer) || !range.startContainer.parentElement.closest('[data-execution-pair]')) continue;
                const rect = range.getBoundingClientRect();
                if (rect.height && Math.abs(rect.top - y) < 40) ranges.push({range, top: rect.top});
            }
        }
        await new Promise(resolve => requestAnimationFrame(resolve));
        const expected = Math.min(before, input - startInput);
        for (const {range, top} of ranges) {
            measured++;
            if (!range.startContainer.isConnected) throw Error('Reading text detached during native scroll');
            const shift = range.getBoundingClientRect().top - top - expected;
            // Allow event/frame coalescing, but not a page-sized offset overwrite.
            if (Math.abs(shift) > 10) throw Error('Native scroll moved reading text by ' + shift + 'px at frame ' + frame);
        }
        loaded ||= m.querySelectorAll(':scope > [data-execution-pair]').length > 5;
        // Keep measuring after input stops: the response and its asynchronous
        // rendering can arrive later. Readiness must not create an unobserved
        // interval in which a paging correction can move the reader's text.
        const layout = [m.scrollTop,m.scrollHeight,m.clientHeight].join(',');
        const busy = m.dataset.transcriptHydrating === 'true' || m.dataset.earlierAnchorRestoring === 'true' ||
            window._chatContentRenderActive || window._chatContentRenderQueue?.length;
        settledFrames = window._nativeBurstDone && loaded && !busy && layout === previousLayout && input === startInput
            ? settledFrames + 1 : 0;
        previousLayout = layout;
        if (frame >= 40 && settledFrames >= 6) break;
    }
    // Native events may be coalesced under CI load; require actual movement,
    // page insertion and measured text, not a particular event-delivery rate.
    if (!loaded || events < 2 || input < 148 || measured === 0 || (webkit && cancelled === 0))
        throw Error('Native paging coverage: '+JSON.stringify({events,input,cancelled,measured,top:m.scrollTop,height:m.scrollHeight,wheelScale,ua:navigator.userAgent}));

    // A nested tool scroller must retain native vertical and horizontal input.
    const tool = document.createElement('div');
    tool.style.cssText = `position:fixed;left:${bounds.left + 20}px;top:${bounds.top + 120}px;width:200px;height:100px;overflow:auto`;
    tool.innerHTML = '<div style="width:2000px;height:1000px">Scrollable tool output</div>';
    m.append(tool);
    await stableLayout(m);
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
            nativeInput.postMessage({...delta, x: bounds.left + 50, y: bounds.top + 150});
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
    await stableLayout(returned);
    const rect = returned.getBoundingClientRect();
    let delivered, initial, synchronousDelta;
    const capture = event => {
        if(!event.isTrusted)return;
        const bounds=returned.getBoundingClientRect();
        const node=Array.from(returned.querySelectorAll('.chat-markdown,.stream-tool')).find(node=>{
            const box=node.getBoundingClientRect();return box.top>=bounds.top && box.bottom<=bounds.bottom && box.height>0;
        });
        if(!node) throw Error('No visible reading anchor after return');
        initial={node,top:node.getBoundingClientRect().top,scrollTop:returned.scrollTop};
    };
    const receive = event => {
        if(event.isTrusted) {delivered=event;synchronousDelta=returned.scrollTop-initial.scrollTop;}
    };
    returned.addEventListener('wheel',capture,true);
    returned.addEventListener('wheel',receive);
    nativeInput.postMessage({dy: -4, x: rect.left + 6, y: rect.top + 75});
    await until(() => delivered, 'wheel after return');
    await stableLayout(returned);
    returned.removeEventListener('wheel',capture,true);
    returned.removeEventListener('wheel',receive);
    // The WebKit handler changes the offset during dispatch. Check exactly one
    // correction there; subsequent virtual-height corrections may legitimately
    // change scrollTop, but must preserve the visible message's position.
    if (webkit && (!delivered.defaultPrevented || Math.abs(synchronousDelta-delivered.deltaY)>2))
        throw Error('Restored wheel handler missing or duplicated: '+JSON.stringify({synchronousDelta,dy:delivered.deltaY}));
    if (!initial.node.isConnected || Math.abs(initial.node.getBoundingClientRect().top-initial.top+delivered.deltaY)>2)
        throw Error('Reading position moved after cached return: '+JSON.stringify({before:initial.top,after:initial.node.getBoundingClientRect().top,dy:delivered.deltaY}));
    if (errors.length) throw Error('Browser errors: ' + errors.join('; '));
    await fetch('/browser-result?status=pass');
    return;
}
