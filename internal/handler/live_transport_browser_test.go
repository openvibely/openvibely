package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

// Run the production transport in Chromium with controlled network completions
// and clocks. The native proxy deliberately ignores aborts to exercise both a
// stalled response and a late response after another attempt has succeeded.
func TestLiveWebSocketBrowserByteReplayAndNativeTicketRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	cases := []struct {
		name   string
		native bool
		script string
	}{
		{name: "byte_exact_replay", script: `
const encoder = new TextEncoder();
const length = value => encoder.encode(value).length;
let output = '';
let subscription = openVibelyLive.execution('execution', 0, 'project');
subscription.onmessage = event => { output += event.data; };
let socket = sockets[0];
frame(socket, {event:'ready'});
let id = socket.sent[0].id;
const initial = '\uFEFFhello';
frame(socket, {id, event:'message', data:initial, offset:length(initial)});
frame(socket, {id, event:'message', data:initial, offset:length(initial)});
assert(output === initial, 'leading BOM lost or duplicate replay applied');
socket.close();
fireTimer(delay => delay < 1000);
socket = sockets[1];
frame(socket, {event:'ready'});
assert(socket.sent[0].offset === length(output), 'reconnect offset differs from applied bytes');
const replay = initial + '\uFEFF日本語';
frame(socket, {id, event:'message', data:replay, offset:length(replay)});
assert(output === replay, 'BOM lost at overlap boundary');
// Recreate the view using its persisted output length on the same connection.
subscription.close();
subscription = openVibelyLive.execution('execution', length(output), 'project');
subscription.onmessage = event => { output += event.data; };
id = socket.sent.at(-1).id;
assert(socket.sent.at(-1).offset === length(replay), 'navigation persisted the wrong byte cursor');
const finalOutput = replay + '\uFEFFtail';
frame(socket, {id, event:'message', start:length(replay), data:'\uFEFFtail', offset:length(finalOutput)});
frame(socket, {id, event:'done', data:'completed'});
assert(output === finalOutput, 'content changed across frame/navigation boundary');
assert(subscription.readyState === 2 && sockets.length === 2 && socket.readyState === 1, 'completion replaced the shared connection');
assert(timers.size === 0, 'unexpected timer left after recovery');
`},
		{name: "stalled_native_headers", native: true},
		{name: "stalled_native_body", native: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nativeAttr := "false"
			setup, script := "", tc.script
			if tc.native {
				nativeAttr = "true"
				setup = fmt.Sprintf(`
const stallBody = %t;
let releaseStalled, requests = [];
window.fetch = function(url, options) {
    assert(url === '/events/native-ticket', 'unexpected request');
    requests.push(options);
    if (requests.length === 1) {
        const pending = new Promise(resolve => { releaseStalled = resolve; });
        return stallBody ? Promise.resolve({ok:true, json:() => pending}) : pending;
    }
    return Promise.resolve({ok:true, json:async() => ({url:'ws://localhost/recovered', ticket:'replacement'})});
};`, tc.name == "stalled_native_body")
				script = `
const subscription = openVibelyLive.execution('execution', 7, 'project');
await checkpoint();
assert(requests.length === 1 && sockets.length === 0, 'initial ticket request not pending');
fireTimer(delay => delay === 10000);
await checkpoint();
assert(requests[0].signal.aborted, 'deadline did not abort native request');
assert(timers.size === 1, 'timeout did not schedule recovery');
fireTimer(delay => delay < 1000);
await checkpoint();
assert(requests.length === 2 && sockets.length === 1, 'native ticket retry did not recover');
assert(!requests[1].signal.aborted && timers.size === 0, 'successful ticket retained its deadline');
const socket = sockets[0];
assert(socket.protocols[1] === 'ticket.replacement', 'wrong ticket selected');
frame(socket, {event:'ready'});
assert(socket.sent[0].offset === 7, 'pending execution was not resumed');
// Even a proxy that delivers an aborted response late cannot open another socket.
const stale = {url:'ws://localhost/stale', ticket:'stale'};
releaseStalled(stallBody ? stale : {ok:true, json:async() => stale});
await checkpoint();
window.dispatchEvent(new Event('online'));
await checkpoint();
assert(sockets.length === 1 && requests.length === 2 && timers.size === 0, 'late ticket created a duplicate connection');
`
			}
			page := `<html data-openvibely-native-window="` + nativeAttr + `"><head><script>
const reportFetch = window.fetch.bind(window);
const originalSetTimeout = window.setTimeout.bind(window);
const checkpoint = () => new Promise(resolve => originalSetTimeout(resolve, 0));
function assert(value, message) { if (!value) throw Error(message); }
const timers = new Map();
let nextTimer = 0;
window.setTimeout = function(callback, delay) { const id = ++nextTimer; timers.set(id, {callback, delay}); return id; };
window.clearTimeout = function(id) { timers.delete(id); };
function fireTimer(match) {
    const entry = Array.from(timers).find(([, timer]) => match(timer.delay));
    assert(entry, 'expected timer missing');
    timers.delete(entry[0]); entry[1].callback();
}
const sockets = [];
window.WebSocket = class {
    static OPEN = 1;
    constructor(url, protocols) { this.url = url; this.protocols = protocols; this.readyState = 1; this.sent = []; sockets.push(this); }
    send(command) { this.sent.push(JSON.parse(command)); }
    close() { this.readyState = 3; this.onclose?.(); }
};
function frame(socket, payload) { socket.onmessage({data:JSON.stringify(payload)}); }
` + setup + `
window.addEventListener('load', async function() {
    try {
` + script + `
        await reportFetch('/test/result?result=pass');
    } catch (error) {
        await reportFetch('/test/result?result=' + encodeURIComponent(error.stack));
    }
});
</script><script src="/static/live-transport.js"></script></head><body></body></html>`
			result := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/test/result" {
					result <- r.URL.Query().Get("result")
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, page)
			}))
			defer server.Close()
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL)
			require.NoError(t, startHandlerBrowserProcess(cmd))
			defer stopHandlerBrowserProcess(cmd)
			select {
			case got := <-result:
				require.Equal(t, "pass", strings.TrimSpace(got))
			case <-time.After(30 * time.Second):
				t.Fatal("transport browser regression timed out")
			}
		})
	}
}
