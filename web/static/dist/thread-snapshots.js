// Shared memory-only thread snapshots. Pages own render listeners; this owner
// retains execution subscriptions across navigation until final output arrives.
(function () {
    'use strict';
    if (window.openVibelyThreadCache) return;
    const pages = new Map(), runs = new Map();
    const ttl = 30 * 60 * 1000, maxBytes = 64 * 1024 * 1024, maxThreads = 20;
    const encoder = new TextEncoder();
    let discovery = null, stopped = false;
    const runKey = (project, id) => JSON.stringify([project, id]);
    const threadKey = (project, task) => JSON.stringify([project, task]);
    function projectID(value) {
        return value || document.getElementById('project-selector')?.value || new URLSearchParams(location.search).get('project_id') || '';
    }
    function group(run) { return threadKey(run.project, run.task || run.id); }
    function notify(view, name, data) {
        if (view.closed) return;
        const event = {type: name, data};
        const callbacks = (view.listeners[name] || []).slice();
        if (view['on' + name]) callbacks.push(view['on' + name]);
        callbacks.forEach(fn => { try { fn(event); } catch (error) { console.error('[thread-cache]', error); } });
    }
    function deliver(run, view) {
        if (view.closed || !view.ready) return;
        if (view.seed !== null && (run.offset >= view.offset || run.terminal)) {
            if (!run.output.startsWith(view.seed)) view.version = -1;
            view.seed = null;
        }
        if (view.version !== run.version || (run.terminal && view.offset > run.offset)) {
            view.version = run.version;
            view.seed = null;
            view.offset = 0;
            notify(view, 'reset', '');
        }
        if (view.offset < run.offset) {
            const delta = view.offset === run.deltaStart ? run.delta : new TextDecoder('utf-8', {ignoreBOM: true}).decode(encoder.encode(run.output).subarray(view.offset));
            view.offset = run.offset;
            notify(view, 'message', delta);
        }
        if (run.terminal && !view.terminal) {
            view.terminal = true;
            view.readyState = 2;
            notify(view, run.terminal.type, run.terminal.data);
        }
    }
    function changed(run) {
        run.time = Date.now();
        // Parsed rows belong to the old revision. Keep background updates as data,
        // never render into detached DOM or retain obsolete parsed transcripts.
        pages.forEach(page => {
            if (page.runKeys?.includes(run.key)) { page.virtual = []; page.time = run.time; }
        });
        run.views.forEach(view => deliver(run, view));
    }
    function start(run) {
        if (run.stream || run.terminal || stopped) return;
        run.live = true;
        const stream = window.openVibelyLive.execution(run.id, run.offset, run.project);
        run.stream = stream;
        stream.onmessage = event => {
            run.deltaStart = run.offset;
            run.delta = event.data;
            run.output += event.data;
            run.offset = stream.offset;
            changed(run);
        };
        stream.addEventListener('reset', event => {
            run.delta = null; run.deltaStart = -1;
            run.output = event.data;
            run.offset = stream.offset;
            run.version++;
            changed(run);
        });
        function finish(type, event) {
            // The transport's per-connection limit is backpressure, not an
            // execution failure. Discovery retries once capacity is available.
            if (type === 'error' && event.data === 'subscription limit reached') {
                run.stream = null;
                return;
            }
            if (type === 'error' && /^(execution not found|subscription denied)$/.test(event.data)) {
                run.output = ''; run.offset = 0; run.version++;
                pages.forEach((page, id) => { if (page.runKeys?.includes(run.key)) pages.delete(id); });
                runs.delete(run.key);
            }
            run.live = false;
            run.stream = null;
            run.terminal = {type, data: event.data};
            changed(run);
            sweep();
        }
        stream.addEventListener('done', event => finish('done', event));
        stream.addEventListener('error', event => finish('error', event));
    }
    function ensure(id, project, task, seed) {
        const key = runKey(project, id);
        let run = runs.get(key);
        if (!run) {
            const output = seed || '';
            run = {key, id, project, task: task || '', output, offset: encoder.encode(output).length,
                version: 0, time: Date.now(), live: false, stream: null, terminal: null, views: new Set()};
            runs.set(key, run);
        } else if (task) run.task = task;
        if (task) {
            // The execution may already be subscribed when navigation saves an
            // older shell. Reconcile the shell on every discovery, not only when
            // the stream is first created.
            pages.forEach((page, key) => {
                if (page.threadKey === group(run) && !page.executionIDs?.includes(id)) pages.delete(key);
            });
        }
        start(run);
        return run;
    }
    function execution(id, project, seed, container, initialOffset) {
        project = projectID(project);
        const seedBytes = encoder.encode(seed || '').length;
        const offset = Number.isSafeInteger(initialOffset) && initialOffset >= 0 ? initialOffset : seedBytes;
        if (!project) return window.openVibelyLive.execution(id, offset, project);
        const root = container?.closest('[data-task-id]');
        const task = container?.closest('#chat-page-root') ? 'chat' : (root?.getAttribute('data-task-id') || '');
        // HTML can contain normalized legacy markers, whose display bytes are
        // not the server cursor. Rebuild canonical output from zero in that case;
        // keep the HTTP seed visible until replay reaches its actual byte offset.
        const run = ensure(id, project, task, offset === seedBytes ? seed : '');
        if (container) container.setAttribute('data-live-snapshot-key', run.key);
        run.time = Date.now();
        const view = {listeners: {}, offset, version: run.version,
            seed: seed || '', ready: false, readyState: 1, closed: false, terminal: false};
        // A fresh HTTP snapshot can be ahead of replay, or differ after a reset.
        // Start this listener from the retained buffer, without mutating the stream cursor.
        view.addEventListener = (name, fn) => { (view.listeners[name] || (view.listeners[name] = [])).push(fn); };
        view.close = () => {
            if (view.closed) return;
            view.closed = true;
            view.readyState = 2;
            run.views.delete(view);
            run.time = Date.now();
            sweep();
        };
        run.views.add(view);
        queueMicrotask(() => {
            if (view.closed) return;
            view.ready = true;
            notify(view, 'open', '');
            deliver(run, view);
        });
        return view;
    }
    function sweep() {
        const groups = new Map();
        function add(key) {
            if (!groups.has(key)) groups.set(key, {key, pages: [], runs: [], strings: new Set(), bytes: 0, time: 0, pinned: false});
            return groups.get(key);
        }
        pages.forEach((page, id) => {
            const g = add(page.threadKey || id);
            g.pages.push(id); g.time = Math.max(g.time, page.time); g.strings.add(page.html);
            page.sources.forEach(source => g.strings.add(source.text));
            page.virtual?.forEach(data => {
                g.strings.add(data.source); g.strings.add(data.normalized);
                data.models.forEach(model => model.segments.forEach(seg => {
                    g.strings.add(seg.content || ''); g.strings.add(seg.resultOutput || ''); g.strings.add(seg.secondary || '');
                    g.bytes += 128;
                }));
            });
        });
        runs.forEach(run => {
            const g = add(group(run));
            g.runs.push(run.key); g.strings.add(run.output); g.time = Math.max(g.time, run.time);
            g.pinned ||= run.live || run.views.size > 0;
        });
        // Only five settled executions per thread survive as idle snapshots.
        groups.forEach(g => {
            const settled = g.runs.map(key => runs.get(key)).filter(run => !run.live && !run.views.size).sort((a,b) => b.time-a.time);
            settled.slice(5).forEach(run => { runs.delete(run.key); });
            g.strings.forEach(value => { g.bytes += (value || '').length * 2; });
        });
        let idle = Array.from(groups.values()).filter(g => !g.pinned).sort((a,b) => a.time-b.time);
        let bytes = idle.reduce((sum,g) => sum+g.bytes, 0), count = idle.length;
        idle.forEach(g => {
            if (Date.now()-g.time <= ttl && bytes <= maxBytes && count <= maxThreads) return;
            g.pages.forEach(id => pages.delete(id));
            g.runs.forEach(key => runs.delete(key));
            bytes -= g.bytes; count--;
        });
    }
    async function discover() {
        if (discovery || stopped) return;
        const controller = new AbortController();
        discovery = controller;
        const deadline = setTimeout(() => controller.abort(), 10000);
        try {
            const response = await fetch('/events/running-executions', {signal: controller.signal, cache: 'no-store'});
            if (response.status === 401 || response.status === 403) { clear(); return; }
            if (!response.ok) return;
            const active = await response.json();
            if (stopped || !Array.isArray(active)) return;
            active.forEach(item => {
                if (!item.id || !item.project_id || !item.task_id) return;
                const task = item.category === 'chat' ? 'chat' : item.task_id;
                ensure(item.id, item.project_id, task, '');
            });
            // Absence is not completion: final bytes arrive on the execution stream.
            runs.forEach(run => { if (run.live && !run.stream) start(run); });
        } catch (_) { /* Reconcile on the next tick or reconnect. */ }
        finally { clearTimeout(deadline); discovery = null; }
    }
    function clear() {
        runs.forEach(run => { run.stream?.close(); run.views.forEach(view => { view.closed = true; view.readyState = 2; }); });
        runs.clear(); pages.clear();
    }
    window.openVibelyThreadCache = {pages, runs, execution, sweep, discover, threadKey,
        source: key => runs.get(key), clear};
    window.addEventListener('live-transport-connected', discover);
    window.addEventListener('sse-task-event', event => {
        if (/execution|status|input_applied/.test(event.detail?.type || '')) discover();
    });
    const interval = setInterval(() => { sweep(); discover(); }, 5000);
    window.addEventListener('pagehide', () => { stopped = true; discovery?.abort(); clearInterval(interval); clear(); });
    window.addEventListener('pageshow', event => { if (event.persisted) location.reload(); });
})();
