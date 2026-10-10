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
	"regexp"
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
	// Extract the production SSE coalescer, rather than duplicating its scheduling.
	var bubble bytes.Buffer
	if err := ChatBubbleStreaming("Assistant", "fixture", "messages", "", false).Render(context.Background(), &bubble); err != nil {
		t.Fatal(err)
	}
	bubbleJS := bubble.String()
	coalescerStart := strings.Index(bubbleJS, "function renderBufferedOutput(force, yieldLarge)")
	coalescerEnd := strings.Index(bubbleJS, "function flushBufferedOutput()")
	if coalescerStart < 0 || coalescerEnd <= coalescerStart {
		t.Fatal("missing production stream coalescer")
	}
	coalescer := `window.makeMeasuredStream = function(container, interval) {
	 var textBuffer = '', renderScheduled = false, renderDelayTimer = null;
	 var lastRenderFinishedAt = 0, lastRenderedSourceLength = 0;
	 var largeStreamRenderThreshold = 100 * 1024, largeStreamRenderInterval = interval;
	 var messagesId = 'messages', trackerKey = '_measurementTracker', tracker = {shouldAutoScroll:()=>false};
	 ` + bubbleJS[coalescerStart:coalescerEnd] + `
	 return {append: function(delta) {textBuffer += delta; renderBufferedOutput(false);},
	 idle: function() {return !renderScheduled && lastRenderedSourceLength === textBuffer.length;},
	 finish: function() {return renderBufferedOutput(true, true);}};
	};`
	// Test-only CPU probes time synchronous preparation/rendering batches, including
	// batches resumed after yielding, and worker handler CPU. They never time waits
	// as CPU or change yielding, worker selection, or stream scheduling.
	probe := `window.streamCPU = 0; window.streamCPUDepth = 0; window.streamWorkerReplies = 0;
	window.measureStreamCPU = function(fn) {return function() {
	 const outer = window.streamCPUDepth++ === 0, start = performance.now();
	 try {return fn.apply(this, arguments);} finally {window.streamCPUDepth--; if (outer) window.streamCPU += performance.now()-start;}
	};};
	window.streamWorkerProbe = ';const originalHandler=self.onmessage, originalPost=self.postMessage; self.onmessage=function(event){const started=performance.now();self.postMessage=function(value){value.streamCPU=performance.now()-started;return originalPost.call(self,value);};return originalHandler.call(self,event);};';
	const OriginalStreamWorker = window.Worker;
	window.Worker = class extends OriginalStreamWorker { constructor(...args) {super(...args); this.addEventListener('message', event=>{window.streamWorkerReplies++; window.streamCPU += event.data.streamCPU || 0;});} };
	`
	baseJS := strings.ReplaceAll(source[start:end], "new Blob([workerSource]", "new Blob([workerSource + window.streamWorkerProbe]")
	sharedJS := shared.String()
	sharedJS = strings.Replace(sharedJS, "function renderPreparedSegments() {", "preparationPhases = preparationPhases.map(window.measureStreamCPU);\nfunction renderPreparedSegments() {", 1)
	sharedJS = strings.Replace(sharedJS, "function fillLargeToolOutputChunks(done, fail) {", "renderSegment = window.measureStreamCPU(renderSegment); finishRender = window.measureStreamCPU(finishRender);\nfunction fillLargeToolOutputChunks(done, fail) {", 1)
	styles := strings.Join(regexp.MustCompile(`(?s)<style>.*?</style>`).FindAllString(source, -1), "")
	fixture := `<!doctype html><html><head>` + styles + `<script src="` + static.URL("vendor/marked.min.js") + `" data-ov-asset="marked"></script></head><body>
 <div id="messages" style="height:300px;overflow:auto"><div id="pair" data-execution-pair="true" data-exec-id="fixture" data-exec-status="running"><div id="stream" class="chat-bubble-assistant-msg"></div></div></div>
 <script>` + probe + baseJS + `</script>` + sharedJS + `<script>
 ` + coalescer + `
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
 // Hydrate empty server-style resume markup after expanding both committed and tail thinking.
 const thinkingText = '[Thinking]\nFirst thought.\n[/Thinking]\n'+prose+'[Thinking]\nSecond thought.\n[/Thinking]\n';
 await renderLiveChatContent(replacement, thinkingText, false);
 assert(replacement._incrementalChat.offset > 8192, 'thinking fixture did not commit a prefix');
 let sections = replacement.querySelectorAll('details.stream-thinking');
 assert(sections.length === 2, 'thinking fixture missing sections');
 sections.forEach(section=>{ section.open = true; section.dispatchEvent(new Event('toggle')); });
 const resumedThinking = replacement.cloneNode(false);
 resumedThinking.setAttribute('data-streaming-resume', 'true');
 resumedThinking.setAttribute('data-raw-content', thinkingText);
 resumedThinking.removeAttribute('data-rendered-revision');
 replacement.replaceWith(resumedThinking);
 await cleanAssistantMessages(document.getElementById('pair'));
 sections = resumedThinking.querySelectorAll('details.stream-thinking');
 assert(sections.length === 2 && Array.from(sections).every(section=>section.open), 'resume hydration lost expanded thinking');
 sections[0].open = false; sections[0].dispatchEvent(new Event('toggle'));
 await renderLiveChatContent(resumedThinking, thinkingText+'Continued.', false);
 const remorphedThinking = resumedThinking.cloneNode(false);
 remorphedThinking.setAttribute('data-raw-content', thinkingText+'Continued.');
 remorphedThinking.removeAttribute('data-rendered-revision');
 resumedThinking.replaceWith(remorphedThinking);
 await cleanAssistantMessages(document.getElementById('pair'));
 sections = remorphedThinking.querySelectorAll('details.stream-thinking');
 assert(sections.length === 2 && !sections[0].open && sections[1].open, 'resume hydration lost mixed thinking states');
 remorphedThinking.replaceWith(replacement);
 // Fresh hydration has no incremental tail cache to restore from.
 const freshThinking = document.createElement('div');
 freshThinking.id = 'fresh-hydrated-thinking';
 freshThinking.setAttribute('data-streaming-resume', 'true');
 freshThinking.setAttribute('data-raw-content', thinkingText);
 replacement.replaceWith(freshThinking);
 await cleanAssistantMessages(document.getElementById('pair'));
 sections = freshThinking.querySelectorAll('details.stream-thinking');
 assert(sections.length === 2, 'fresh hydration missing thinking sections');
 sections[1].open = true; sections[1].dispatchEvent(new Event('toggle'));
 await renderLiveChatContent(freshThinking, thinkingText+'First appended delta.', false);
 assert(freshThinking._incrementalChat.offset > 8192, 'fresh hydration did not split');
 sections = freshThinking.querySelectorAll('details.stream-thinking');
 assert(!sections[0].open && sections[1].open, 'fresh split lost later thinking state');
 await renderLiveChatContent(freshThinking, thinkingText+'First appended delta. More.', false);
 sections = freshThinking.querySelectorAll('details.stream-thinking');
 assert(!sections[0].open && sections[1].open, 'subsequent delta lost transferred thinking state');
 freshThinking.replaceWith(replacement);
 // No controls: completed Markdown blocks are reusable too; open fences remain tail.
 assert(chatStreamStableBoundary(prose.repeat(10)) > 0, 'large prose resume did not retain a prefix');
 // A completed control followed by a long answer must not keep the entire answer provisional.
 for (const prefix of ['[Thinking]\nDone.\n[/Thinking]\n', '[Using tool: bash]\n[Tool bash done]\nDone.\n[/Tool]\n', '[Compaction started]\n[Compaction done | 50]\n']) {
  const answer = prefix+prose;
  assert(chatStreamStableBoundary(answer) > prefix.length, 'closed control blocked prose boundaries');
  await renderLiveChatContent(replacement, answer, false);
  const stable = replacement.firstChild;
  const offset = replacement._incrementalChat.offset;
  await renderLiveChatContent(replacement, answer+prose, false);
  assert(replacement.firstChild === stable && replacement._incrementalChat.offset > offset, 'closed control caused a growing full-prefix render');
 }
 // Commit compaction before the tool arrives: activity rows must not consume tool IDs.
 const compacted = '[Compaction started]\n[Compaction done | 50]\n'+prose;
 await renderLiveChatContent(replacement, compacted, false);
 assert(replacement._incrementalChat.offset > 8192, 'compaction prefix was not committed');
 assert(replacement.firstChild.querySelector('[data-compaction-state]'), 'committed compaction row missing');
 // Expansion and a parked output scroll survive terminal reconciliation and a morph.
 const output = compacted+'[Using tool: bash]\n[Tool bash done]\n'+('a line of output\n').repeat(100)+'[/Tool]\n'+prose;
 await renderLiveChatContent(replacement, output, false);
 const toolID = replacement.querySelector('.stream-tool[data-tool-render-id]').getAttribute('data-tool-render-id');
 await renderStreamingContent(full, output, false);
 assert(toolID === full.querySelector('.stream-tool[data-tool-render-id]').getAttribute('data-tool-render-id'), 'committed compaction changed tool ordinal');
 let toggle = replacement.querySelector('.stream-tool-output-toggle');
 assert(toggle && toggle.getAttribute('aria-expanded') === 'false', 'missing collapsed output fixture');
 toggle.click();
 let outputScroll = replacement.querySelector('[data-tool-row="out"]');
 outputScroll.style.height = '50px'; outputScroll.style.overflow = 'auto'; outputScroll.scrollTop = 30;
 outputScroll.dispatchEvent(new Event('scroll'));
 await renderLiveChatContent(replacement, output, false, true);
 assert(replacement.querySelector('.stream-tool[data-tool-render-id]').getAttribute('data-tool-render-id') === toolID, 'terminal reconciliation changed tool ID');
 assert(replacement.querySelector('.stream-tool-output-toggle').getAttribute('aria-expanded') === 'true', 'terminal reconciliation collapsed output');
 assert(replacement.querySelector('[data-tool-row="out"]').getAttribute('data-scroll-pinned') === 'false', 'terminal reconciliation lost output scroll intent');
 const morphed = replacement.cloneNode(false); replacement.replaceWith(morphed);
 await renderLiveChatContent(morphed, output, false);
 assert(morphed.querySelector('.stream-tool-output-toggle').getAttribute('aria-expanded') === 'true', 'morph collapsed output');
 morphed.replaceWith(replacement);

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
 const live = window.renderLiveChatContent;
 window.chatStreamStableBoundary = window.measureStreamCPU(window.chatStreamStableBoundary);
 let chars = 0, yielded = 0;
 window.renderStreamingContent = function(container, text, yielding, options) {
  chars += text.length;
  if (yielding !== false && text.length >= 65536) yielded++;
  return original(container, text, yielding, options);
 };
 let report = {};
 for (const size of [65536,262144,1048576]) {
  const fixture = record.repeat(Math.ceil(size/record.length)).slice(0,size);
  report[size] = {};
  for (const mode of ['baseline','incremental']) {
   stream.replaceChildren(); delete stream._incrementalChat;
   window.streamCPU = 0; window.streamWorkerReplies = 0; chars = 0; yielded = 0;
   const latencies = [], arrivals = [];
   let pendingPaints = 0;
   window.renderLiveChatContent = function(container, text, yielding, final) {
    const result = mode === 'baseline' ? window.renderStreamingContent(container,text,yielding) : live(container,text,yielding,final);
    return Promise.resolve(result).then(committed=>{
     if (committed !== false && !final) {
      pendingPaints++;
      paint().then(()=>{
       const visible = performance.now();
       arrivals.forEach(delta=>{if (!delta.visible && delta.end <= text.length) {delta.visible = true; latencies.push(visible-delta.received);}});
       pendingPaints--;
      });
     }
     return committed;
    });
   };
   // Same production coalescer in both cases; the baseline used 250 ms and
   // full-content rendering, whereas the incremental path uses 50 ms.
   const input = makeMeasuredStream(stream, mode === 'baseline' ? 250 : 50);
   const start = performance.now();
   // Schedule every delta in advance, independently of renderer completion.
   await Promise.all(Array.from({length:size/8192}, (_,i)=>new Promise(resolve=>{
    setTimeout(()=>{
     const end = (i+1)*8192;
     arrivals.push({end,received:performance.now(),visible:false});
     input.append(fixture.slice(end-8192,end));
     resolve();
    }, i*50);
   })));
   while (!input.idle() || pendingPaints) await pause(10);
   assert(latencies.length === size/8192, 'missing delta visibility samples');
   await input.finish();
   latencies.sort((a,b)=>a-b);
   report[size][mode] = {cpuMS:window.streamCPU,characters:chars,p95MS:latencies[Math.ceil(latencies.length*.95)-1],yieldedRenders:yielded,workerReplies:window.streamWorkerReplies,arrivalSpanMS:arrivals.at(-1).received-start};
   if (size >= 100*1024) {
    assert(yielded > 0, 'production yielding was bypassed');
    assert(window.streamWorkerReplies > 0, 'production code-range workers were bypassed');
   }
  }
 }
 return report;`)
	t.Logf("64/256/1024 KiB, independent 8 KiB arrivals at 20 Hz; production coalescer/workers; renderer batch and worker CPU including final reconciliation: %s", report)
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
