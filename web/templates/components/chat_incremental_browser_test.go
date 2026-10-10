package components

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// Use real wall time: virtual-time Chrome does not measure delta-to-paint latency.
func runIncrementalChatBrowser(t *testing.T, script string) json.RawMessage {
	t.Helper()
	chrome := testChromePath(t)
	var base, shared bytes.Buffer
	if err := layout.Base("Incremental stream", nil, "").Render(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	if err := ChatAutoScrollScript().Render(context.Background(), &shared); err != nil {
		t.Fatal(err)
	}
	source := base.String()
	start := strings.Index(source, "window.configureChatMarked = function()")
	end := strings.Index(source, "// Theme toggle function")
	if start < 0 || end < start {
		t.Fatal("missing production markdown helpers")
	}
	fixture := `<!doctype html><html><head><script src="` + static.URL("vendor/marked.min.js") + `" data-ov-asset="marked"></script></head><body>
 <div id="messages" style="height:300px;overflow:auto"><div id="pair" data-execution-pair="true" data-exec-id="fixture" data-exec-status="running"><div id="stream" class="chat-bubble-assistant-msg"></div></div></div>
 <script>` + source[start:end] + `</script>` + shared.String() + `<script>
 const assert = (ok, message) => { if (!ok) throw Error(message); };
 const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
 const paint = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
 const fence = String.fromCharCode(96).repeat(3);
 const stream = document.getElementById('stream');
 async function run() {` + script + `}
 run().then(report => fetch('/report', {method:'POST',body:JSON.stringify({report})})).catch(error => fetch('/report', {method:'POST',body:JSON.stringify({error:String(error.stack || error)})}));
 </script></body></html>`
	type result struct {
		Report json.RawMessage `json:"report"`
		Error  string          `json:"error"`
	}
	reports := make(chan result, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/report" {
			var report result
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				report.Error = err.Error()
			}
			reports <- report
			return
		}
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--no-first-run", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome"), server.URL)
	configureTestBrowserProcess(cmd)
	if err := startTestBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { stopTestBrowserProcess(cmd); _ = cmd.Wait() }()
	select {
	case result := <-reports:
		if result.Error != "" {
			t.Fatal(result.Error)
		}
		return result.Report
	case <-time.After(150 * time.Second):
		t.Fatal("incremental browser fixture timed out")
	}
	return nil
}

func TestBrowserFunctional_IncrementalChatRendering(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const full = document.createElement('div'); document.body.appendChild(full);
 stream.classList.add('whitespace-pre-wrap');
 const prose = ('A completed paragraph with **bold** and inline '+String.fromCharCode(96)+'code'+String.fromCharCode(96)+'.\n\n').repeat(180);
 let text = prose + '[Thinking]\nInspecting.\n[/Thinking]\n[Using tool: bash | echo example]\n[Tool bash done]\nresult\n[/Tool]\n' + 'Explanation. '.repeat(20) + '\n';
 await renderLiveChatContent(stream, text, false);
 let retained = stream.firstChild.firstChild;
 assert(retained, 'initial content missing');
 assert(!stream.classList.contains('whitespace-pre-wrap'), 'stream leaked plaintext whitespace into Markdown');
 text += '[Using tool: read_file | example.go]\n';
 await renderLiveChatContent(stream, text, false);
 // The next update must reuse the completed prefix, including event listeners.
 retained = stream.firstChild;
 text += '[Tool read_file done]\nfile\n[/Tool]\n\n'+fence+'js\nconst example = "[Thinking]";\n';
 await renderLiveChatContent(stream, text, false);
 assert(stream.firstChild === retained, 'completed DOM was replaced');
 assert(stream.querySelector('pre code'), 'unfinished fence lost code formatting');
 text += fence+'\n\nEnd with [link](https://example.com).\n\nCreated task:\n- "Example" (backlog) [TASK_ID:abc123]';
 await renderLiveChatContent(stream, text, false);
 const thinking = stream.querySelector('details.stream-thinking'); thinking.open = true;
 await renderLiveChatContent(stream, text, false, true);
 await renderStreamingContent(full, text, false);
 assert(stream.textContent === full.textContent, 'final text differs');
 assert(stream.querySelectorAll('pre code').length === full.querySelectorAll('pre code').length, 'final fences differ');
 assert(stream.querySelector('details.stream-thinking').open, 'thinking state lost at finish');
 assert(stream.querySelectorAll('.stream-tool').length === 2, 'tool calls duplicated');
 assert(stream.querySelector('a[href="/tasks/abc123"]'), 'task link missing');
 const actual = stream.cloneNode(true), expected = full.cloneNode(true);
 actual.querySelectorAll('details').forEach(el=>el.removeAttribute('open'));
 expected.querySelectorAll('details').forEach(el=>el.removeAttribute('open'));
 assert(actual.innerHTML === expected.innerHTML, 'authoritative markup differs');
 assert(!stream._incrementalChat, 'terminal render kept provisional state');
 // Morph/reload: serialized content has no JS parser state. A fresh stream rebuilds once.
 const replacement = stream.cloneNode(true); stream.replaceWith(replacement);
 await renderLiveChatContent(replacement, text+'\nResumed.', false);
 await renderLiveChatContent(replacement, text+'\nResumed again.', false);
 await renderLiveChatContent(replacement, text+'\nResumed again.', false, true);
 assert(replacement.querySelectorAll('.stream-tool').length === 2, 'resume duplicated calls');
 for (const status of ['completed','failed','cancelled']) {
  document.getElementById('pair').setAttribute('data-exec-status','running');
  await renderLiveChatContent(replacement, text, false);
  replacement._authoritativeTerminalContent = text+'\nAuthoritative '+status;
  applyChatExecutionTerminalStatus(document.getElementById('pair'), status);
  await renderLiveChatContent(replacement, replacement._authoritativeTerminalContent, false);
  await renderStreamingContent(full, replacement._authoritativeTerminalContent, false);
  assert(replacement.textContent === full.textContent, status+' did not reconcile authoritative output');
  assert(!replacement._incrementalChat, status+' retained incremental state');
  delete replacement._authoritativeTerminalContent;
 }
 document.getElementById('pair').setAttribute('data-exec-status','running');
 await renderLiveChatContent(replacement, prose, false);
 const messages = document.getElementById('messages');
 messages.scrollTop = 100;
 await renderLiveChatContent(replacement, prose+'More text.\n\n', false);
 assert(messages.scrollTop === 100, 'render moved reader away from chosen position');
 const tracker = resolveScrollTracker('_incrementalFixtureTracker', messages);
 messages.dispatchEvent(new WheelEvent('wheel', {deltaY:100}));
 chatAutoScroll.scrollToBottom(messages, false);
 messages.dispatchEvent(new Event('scroll'));
 await paint();
 assert(tracker.shouldAutoScroll(), 'bottom reader not eligible to follow');
 await renderLiveChatContent(replacement, prose+'More text.\n\nNext paragraph.', false);
 if (tracker.shouldAutoScroll()) chatAutoScroll.scrollToBottom(messages, false);
 await paint();
 assert(chatAutoScroll.isNearBottom(messages), 'pinned reader did not follow');
 // No controls: completed Markdown blocks are reusable too; open fences remain tail.
 assert(chatStreamStableBoundary(prose.repeat(10)) > 0, 'large prose resume did not retain a prefix');
 assert(chatStreamStableBoundary(prose+fence+'js\nunfinished\n\n') > 0, 'plain Markdown prefix not committed');
 assert(chatStreamStableBoundary(fence+'js\n'+prose) === 0, 'open fence was split');
 // Large provisional tails still yield, and cancellation cannot commit stale work.
 const huge = fence+'text\n'+'unclosed code\n'.repeat(7000);
 const stale = renderLiveChatContent(replacement, huge, true);
 await renderLiveChatContent(replacement, 'Newest authoritative text', false, true);
 assert(await stale === false, 'stale worker render committed');
 assert(replacement.textContent.trim() === 'Newest authoritative text', 'stale worker replaced final text');
 await renderLiveChatContent(replacement, huge, true);
 await renderLiveChatContent(replacement, huge+fence+'\nDone.', true, true);
 assert(replacement.querySelector('pre code').textContent.includes('unclosed code'), 'yielded fence lost content');
 assert(chatStreamStableBoundary('[Using tool: Read]\n[Tool bash done]\norphan\n[/Tool]\n'+prose+'[Using tool: bash]\n') === 0, 'orphan result completed a different tool');
 return {passed:true};`)
}

func TestBrowserPerformance_IncrementalChatRendering(t *testing.T) {
	if os.Getenv("OPENVIBELY_SKIP_BROWSER_PERF") == "1" {
		t.Skip("browser performance runs separately")
	}
	report := runIncrementalChatBrowser(t, `
 const record = '## Response section\n\n'+('Deterministic prose with **bold**, *emphasis* and inline '+String.fromCharCode(96)+'code'+String.fromCharCode(96)+'.\n\n').repeat(60)+fence+'js\nconst answer = 42;\n'+fence+'\n\n[Thinking]\nChecking the result.\n[/Thinking]\n[Using tool: bash | echo answer]\n[Tool bash done]\nanswer\n[/Tool]\n'+'Finished this section. '.repeat(8)+'\n\n';
 const original = window.renderStreamingContent;
 const boundary = window.chatStreamStableBoundary;
 let cpu = 0, chars = 0;
 window.renderStreamingContent = function(container, text, yielding, options) {
  const start = performance.now();
  // Both versions use synchronous preparation to measure actual renderer CPU,
  // excluding worker and timer wait time. Production retains yielding for large tails.
  try { chars += text.length; return original(container, text, false, options); }
  finally { cpu += performance.now()-start; }
 };
 window.chatStreamStableBoundary = function(text) {
  const start = performance.now();
  try { return boundary(text); } finally { cpu += performance.now()-start; }
 };
 let report = {};
 for (const size of [65536,262144,1048576]) {
  const fixture = record.repeat(Math.ceil(size/record.length)).slice(0,size);
  report[size] = {};
  for (const mode of ['baseline','incremental']) {
   stream.replaceChildren(); delete stream._incrementalChat;
   cpu = 0; chars = 0;
   let latencies = [];
   let due = performance.now();
   for (let end = 8192; end <= size; end += 8192) {
    await pause(Math.max(0,due-performance.now())); due += 50;
    const received = performance.now();
    if (mode === 'baseline') await renderStreamingContent(stream,fixture.slice(0,end),false);
    else await renderLiveChatContent(stream,fixture.slice(0,end),false);
    await paint();
    latencies.push(performance.now()-received);
   }
   // Include authoritative reconciliation in cumulative CPU and character counts.
   await renderLiveChatContent(stream,fixture,false,true);
   latencies.sort((a,b)=>a-b);
   report[size][mode] = {cpuMS:cpu,characters:chars,p95MS:latencies[Math.ceil(latencies.length*.95)-1]};
  }
 }
 return report;`)
	t.Logf("64/256/1024 KiB, 8 KiB deltas at 20 Hz (CPU includes final reconciliation): %s", report)
	var metrics map[string]map[string]struct {
		CPU        float64 `json:"cpuMS"`
		Characters int     `json:"characters"`
		P95        float64 `json:"p95MS"`
	}
	if err := json.Unmarshal(report, &metrics); err != nil {
		t.Fatal(err)
	}
	before, after := metrics["1048576"]["baseline"], metrics["1048576"]["incremental"]
	if after.CPU > before.CPU*.5 || after.Characters > before.Characters/2 || after.P95 > 250 {
		t.Fatalf("incremental acceptance threshold failed: baseline=%+v incremental=%+v", before, after)
	}
}
