// One transport owner per document (browser tab or native window). View cleanup
// releases subscriptions only. HTTP remains authoritative for history and state.
(function () {
    'use strict';
    if (window.openVibelyLive) return;
    var socket = null, retry = null, attempt = 0, nextID = 0, connected = false, connecting = false;
    var subscriptions = new Map();
    // U+FEFF is output content, including at a replay/frame boundary.
    var encoder = new TextEncoder(), decoder = new TextDecoder('utf-8', {ignoreBOM: true});
    function send(command) {
        if (socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(command));
    }
    function notify(entry, type, data) {
        var event = {type: type, data: data};
        var callbacks = (entry.listeners[type] || []).slice();
        if (entry['on' + type]) callbacks.push(entry['on' + type]);
        callbacks.forEach(function (fn) { try { fn(event); } catch (error) { console.error('[live] subscriber callback', error); } });
    }
    function command(entry) {
        return Object.assign({}, entry.command, {offset: entry.offset});
    }
    function scheduleReconnect() {
        if (retry) return;
        retry = setTimeout(function () { retry = null; connect(); }, Math.min(15000, 250 * Math.pow(2, attempt++)) + Math.random() * 250);
    }
    async function nativeTicket() {
        var controller = new AbortController();
        var deadline;
        var timeout = new Promise(function (_, reject) {
            deadline = setTimeout(function () {
                controller.abort();
                reject(Error('Native live ticket timed out'));
            }, 10000);
        });
        try {
            // Bound headers and body together. The race also releases connection
            // ownership if a native proxy fails to settle the aborted request.
            return await Promise.race([timeout, (async function () {
                var response = await fetch('/events/native-ticket', {
                    method: 'POST', headers: {'X-OpenVibely-Live': '1'}, signal: controller.signal
                });
                if (!response.ok) throw Error('Native live ticket unavailable');
                return await response.json();
            })()]);
        } finally {
            clearTimeout(deadline);
        }
    }
    async function connect() {
        if (socket || retry || connecting) return;
        connecting = true;
        var url = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/events/ws';
        var protocols = [];
        var ws;
        try {
            if (document.documentElement.getAttribute('data-openvibely-native-window') === 'true') {
                var ticket = await nativeTicket();
                url = ticket.url;
                protocols = ['openvibely-live', 'ticket.' + ticket.ticket];
            }
            ws = new WebSocket(url, protocols);
        } catch (error) {
            connecting = false;
            scheduleReconnect();
            return;
        }
        connecting = false;
        socket = ws;
        ws.onmessage = function (event) {
            if (socket !== ws) return;
            var frame;
            try { frame = JSON.parse(event.data); } catch (_) { ws.close(); return; }
            if (frame.event === 'ready') {
                attempt = 0;
                subscriptions.forEach(function (entry) { clearTimeout(entry.retryTimer); send(command(entry)); });
                connected = true;
                window.dispatchEvent(new CustomEvent('live-transport-connected'));
                return;
            }
            if (frame.event === 'system_update') {
                window.dispatchEvent(new CustomEvent('live-system-update', {detail: frame.data}));
                return;
            }
            if (frame.event === 'reconcile') {
                window.dispatchEvent(new CustomEvent('sse-live-connected', {detail: {reconnected: true, reconciliation: true}}));
                if (window.htmx) window.htmx.trigger('body', 'alertUpdate');
                return;
            }
            var entry = subscriptions.get(frame.id);
            if (!entry) return; // A late frame from a detached view or old project.
            if (frame.event === 'retry') {
                clearTimeout(entry.retryTimer);
                entry.retryDelay = Math.min(15000, (entry.retryDelay || 500) * 2);
                entry.retryTimer = setTimeout(function() {
                    if (connected && subscriptions.get(frame.id) === entry) send(command(entry));
                }, entry.retryDelay);
                return;
            }
            if (frame.event === 'subscribed') { notify(entry, 'open', ''); return; }
            if (frame.event === 'message') {
                var start = frame.start || 0, end = frame.offset || 0;
                if (end <= entry.offset) return;
                if (start > entry.offset) { ws.close(); return; }
                var bytes = encoder.encode(frame.data);
                var delta = decoder.decode(bytes.subarray(Math.max(0, entry.offset - start)));
                entry.offset = end;
                notify(entry, 'message', delta);
                return;
            }
            if (frame.event === 'reset') {
                entry.offset = frame.offset || 0;
                notify(entry, 'reset', frame.data || '');
                return;
            }
            if (frame.event === 'done' || frame.event === 'error') {
                subscriptions.delete(frame.id);
                clearTimeout(entry.retryTimer);
                entry.readyState = 2;
            }
            notify(entry, frame.event, typeof frame.data === 'string' ? frame.data : JSON.stringify(frame.data));
        };
        ws.onclose = function () {
            if (socket !== ws) return;
            socket = null;
            connected = false;
            // Connection failures are not execution failures. Keep buffers and
            // offsets and replay only unapplied bytes after the new handshake.
            scheduleReconnect();
        };
        ws.onerror = function () { ws.close(); };
    }
    function subscribe(cmd, handlers) {
        var id = String(++nextID);
        cmd.id = id;
        var entry = {command: cmd, offset: cmd.offset || 0, listeners: {}, readyState: 1};
        entry.addEventListener = function (name, fn) { (entry.listeners[name] || (entry.listeners[name] = [])).push(fn); };
        entry.close = function () {
            if (!subscriptions.has(id)) return;
            subscriptions.delete(id);
            clearTimeout(entry.retryTimer);
            entry.readyState = 2;
            send({op: 'unsubscribe', id: id});
        };
        if (handlers) {
            entry.onopen = handlers.onopen;
            entry.onmessage = handlers.onmessage;
            Object.keys(handlers.listeners || {}).forEach(function (name) { entry.addEventListener(name, handlers.listeners[name]); });
        }
        subscriptions.set(id, entry);
        if (connected) send(command(entry));
        connect();
        return entry;
    }
    window.openVibelyLive = {
        execution: function (execID, offset, projectID) {
            var selector = document.getElementById('project-selector');
            projectID = projectID || (selector && selector.value) || new URLSearchParams(location.search).get('project_id');
            return subscribe({op: 'execution', exec_id: execID, project_id: projectID, offset: offset});
        },
        live: function (projectID, taskID, handlers) {
            // The view owns execution cleanup when navigation commits. A live
            // scope can change before a project navigation succeeds or rolls back.
            return subscribe({op: 'live', project_id: projectID, task_id: taskID || ''}, handlers);
        }
    };
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', connect, {once: true});
    else connect();
    window.addEventListener('online', function () {
        if (!socket) { clearTimeout(retry); retry = null; connect(); }
    });
})();
