package components

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

type chatMatcherBrowserReport struct {
	Matcher map[string]map[string]map[string]struct {
		P95MS  float64 `json:"p95MS"`
		Visits int     `json:"visits"`
	} `json:"matcher"`
	Preparation map[string]map[string]struct {
		P95MS float64 `json:"p95MS"`
	} `json:"preparation"`
	LargeRender    map[string]chatMatcherLargeRenderReport `json:"largeRender"`
	Improvement400 float64                                 `json:"improvement400"`
	Growth100To400 float64                                 `json:"growth100To400"`
}

type chatMatcherLargeRenderReport struct {
	InputBytes      int     `json:"inputBytes"`
	MatcherRuns     int     `json:"matcherRuns"`
	FullRenderP95MS float64 `json:"fullRenderP95MS"`
	FrameGapP95MS   float64 `json:"frameGapP95MS"`
	RenderedDOMSame bool    `json:"renderedDOMSame"`
}

func TestBrowserPerformance_ChatToolResultMatchingScalesLinearlyInChrome(t *testing.T) {
	chrome := testChromePath(t)

	var baseHTML bytes.Buffer
	if err := layout.Base("Chat tool matching fixture", nil, "").Render(context.Background(), &baseHTML); err != nil {
		t.Fatalf("render base layout: %v", err)
	}
	var chatScript bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &chatScript); err != nil {
		t.Fatalf("render shared chat script: %v", err)
	}
	styles := strings.Join(regexp.MustCompile(`(?s)<style>.*?</style>`).FindAllString(baseHTML.String(), -1), "")

	fixture := `<!doctype html><html><head><meta charset="utf-8">` + styles + `</head><body>
<main id="fixture-root" data-test-result="pending"><div id="stream" class="chat-stream-content chat-bubble-assistant-msg"></div></main>
<script>` + renderedBaseMarkdownCodeHelpers(t) + `</script>` + chatScript.String() + `<script>
window.addEventListener('DOMContentLoaded', function() {
  var root = document.getElementById('fixture-root');
  var container = document.getElementById('stream');
  var report = {matcher: {legacy: {}, linear: {}}, preparation: {legacy: {}, linear: {}}};
  function fail(message) {
    root.setAttribute('data-test-result', 'fail');
    root.setAttribute('data-test-error', String(message));
    throw new Error(message);
  }
  function assert(condition, message) { if (!condition) fail(message); }
  function legacyLinkStreamingToolResults(segments) {
    var queues = Object.create(null);
    var ordinal = 0;
    for (var i = 0; i < segments.length; i++) {
      var segment = segments[i];
      if (segment.type === 'using') {
        var name = segment.normalizedName || segment.name;
        segment.toolRenderID = 'tool-' + segment.index + '-' + ordinal++;
        if (!queues[name]) queues[name] = [];
        queues[name].push(segment);
        continue;
      }
      if (segment.type !== 'tool_result') continue;
      var resultName = segment.normalizedName || segment.name;
      var queue = queues[resultName] || [];
      for (var j = 0; j < queue.length; j++) {
        if (queue[j].resultLinked) continue;
        queue[j].resultLinked = true;
        queue[j].resultStatus = segment.status;
        queue[j].resultOutput = segment.output || '';
        break;
      }
    }
    for (var i = 0; i < segments.length; i++) {
      if (segments[i].type !== 'using') continue;
      if (segments[i].resultLinked) { segments[i].completed = true; continue; }
      for (var j = i + 1; j < segments.length; j++) {
        if (segments[j].type !== 'text' || segments[j].content.trim()) {
          segments[i].completed = true;
          break;
        }
      }
    }
    return true;
  }

  function makeSegments(count, scenario) {
    var segments = [];
    var names = [];
    for (var i = 0; i < count; i++) {
      var name = scenario === 'mixed' ? (i % 2 === 0 ? 'bash' : 'read_file') : 'bash';
      names.push(name);
      segments.push({type: 'using', name: name, normalizedName: name, secondary: 'command-' + i, index: i * 19 + 3, ordinal: i});
    }
    if (scenario !== 'missing') {
      for (var r = 0; r < count; r++) {
        var resultIndex = scenario === 'mixed' ? (r % 2 === 0 ? Math.floor(r / 2) * 2 + 1 : Math.floor(r / 2) * 2) : r;
        var resultName = names[resultIndex];
        segments.push({type: 'tool_result', name: resultName, normalizedName: resultName, status: r % 2 === 0 ? 'done' : 'error', output: 'output-' + resultIndex, ordinal: resultIndex});
      }
    } else {
      segments.push({type: 'text', content: '   '});
    }
    return segments;
  }
  function legacyQueueComparisons(count, scenario) {
    if (scenario === 'missing') return 0;
    if (scenario === 'same') return count * (count + 1) / 2;
    var firstQueue = Math.ceil(count / 2);
    var secondQueue = Math.floor(count / 2);
    return firstQueue * (firstQueue + 1) / 2 + secondQueue * (secondQueue + 1) / 2;
  }
  function measureMatcher(fn, count, scenario) {
    var samples = [];
    var repetitionsPerSample = 128;
    for (var warm = 0; warm < 4; warm++) fn(makeSegments(count, scenario));
    for (var sample = 0; sample < 30; sample++) {
      var batch = [];
      for (var repeat = 0; repeat < repetitionsPerSample; repeat++) batch.push(makeSegments(count, scenario));
      var started = performance.now();
      for (var item = 0; item < batch.length; item++) fn(batch[item]);
      samples.push((performance.now() - started) / repetitionsPerSample);
    }
    var queueVisits;
    if (fn === legacyLinkStreamingToolResults) queueVisits = legacyQueueComparisons(count, scenario);
    else queueVisits = count + (scenario === 'missing' ? 0 : count);
    samples.sort(function(a, b) { return a - b; });
    return {p95MS: samples[Math.ceil(samples.length * 0.95) - 1], visits: queueVisits};
  }
  function medianP95(samples) {
    samples.sort(function(a, b) { return a - b; });
    return {p95MS: samples[Math.ceil(samples.length * 0.95) - 1]};
  }
  function makeLargeStreamTranscript(count) {
    var transcript = 'large streamed response context '.repeat(2400) + '\n';
    var callIndexes = [];
    for (var i = 0; i < count; i++) {
      callIndexes.push(transcript.length);
      transcript += '[Using tool: bash | command-' + i + ']\n';
    }
    for (var resultIndex = 0; resultIndex < count; resultIndex++) {
      var status = resultIndex % 2 === 0 ? 'done' : 'error';
      transcript += '[Tool bash ' + status + ']\nlarge-output-' + resultIndex + '\n[/Tool]\n';
    }
    return {text: transcript, callIndexes: callIndexes};
  }
  function verifyLargeRenderedDOM(transcript, markup, count) {
    var cards = Array.from(container.querySelectorAll('.stream-tool'));
    assert(cards.length === count, 'large stream rendered ' + cards.length + ' tool cards, expected ' + count);
    for (var i = 0; i < count; i++) {
      var expectedID = 'tool-' + transcript.callIndexes[i] + '-' + i;
      assert(cards[i].getAttribute('data-tool-render-id') === expectedID, 'large stream tool ID changed at call ' + i);
      var output = cards[i].querySelector('.stream-tool-body-scroll[data-tool-row="out"] > pre');
      assert(output && output.textContent === 'large-output-' + i, 'large stream output was misplaced at call ' + i);
      var icon = cards[i].querySelector('.stream-tool-summary svg');
      var expectedStatus = i % 2 === 0 ? 'tool-status-done' : 'tool-status-error';
      assert(icon && icon.classList.contains(expectedStatus), 'large stream result status changed at call ' + i);
    }
    assert(markup === container.innerHTML, 'large stream DOM changed during inspection');
  }
  var activeFrameGaps = null;
  var previousFrameAt = 0;
  var frameTracking = false;
  function recordRenderFrame(timestamp) {
    if (!frameTracking) return;
    if (activeFrameGaps) {
      activeFrameGaps.push(timestamp - previousFrameAt);
      previousFrameAt = timestamp;
    }
    requestAnimationFrame(recordRenderFrame);
  }
  async function measureLargeStream(fn, transcript, count, restoreMatcher) {
    var matcherCalls = 0;
    var fullRenderSamples = [];
    var frameGapSamples = [];
    var repetitions = 12;
    for (var sample = 0; sample < repetitions; sample++) {
      window.linkStreamingToolResults = function(segments) {
        matcherCalls++;
        return fn(segments);
      };
      var frameGaps = [];
      activeFrameGaps = frameGaps;
      previousFrameAt = performance.now();
      frameTracking = true;
      requestAnimationFrame(recordRenderFrame);
      var renderStartedAt = performance.now();
      var committed = await window.renderStreamingContent(container, transcript.text, true);
      fullRenderSamples.push(performance.now() - renderStartedAt);
      await new Promise(function(resolve) { requestAnimationFrame(resolve); });
      frameTracking = false;
      activeFrameGaps = null;
      assert(committed !== false, 'large asynchronous render did not commit at sample ' + sample);
      frameGapSamples.push(frameGaps.length ? Math.max.apply(Math, frameGaps) : 0);
    }
    assert(matcherCalls === repetitions, 'large asynchronous render invoked the matcher ' + matcherCalls + ' times, expected ' + repetitions);
    window.linkStreamingToolResults = restoreMatcher;
    var largeMarkup = container.innerHTML;
    verifyLargeRenderedDOM(transcript, largeMarkup, count);
    return {
      matcherRuns: matcherCalls,
      fullRenderP95MS: p95(fullRenderSamples),
      frameGapP95MS: p95(frameGapSamples),
      inputBytes: transcript.text.length,
      markup: largeMarkup
    };
  }
  function makeTranscript(count) {
    var text = '';
    for (var i = 0; i < count; i++) text += '[Using tool: bash | echo command-' + i + ']\n';
    for (var r = 0; r < count; r++) text += '[Tool bash ' + (r % 2 === 0 ? 'done' : 'error') + ']\noutput-' + r + '\n[/Tool]\n';
    return text;
  }
  async function measurePreparation(fn, count) {
    var transcript = makeTranscript(count);
    var samples = [];
    window.linkStreamingToolResults = fn;
    for (var sample = 0; sample < 30; sample++) {
      var started = performance.now();
      var committed = await window.renderStreamingContent(container, transcript, true);
      samples.push(performance.now() - started);
      assert(committed !== false, 'full render did not commit for ' + count + ' calls');
    }
    return medianP95(samples);
  }
  function checkMatcherBehavior(linear) {
    var same = makeSegments(5, 'same');
    var sameOrder = same.map(function(s) { return s.ordinal; }).join(',');
    linear(same);
    assert(same.map(function(s) { return s.ordinal; }).join(',') === sameOrder, 'matching reordered same-tool segments');
    for (var i = 0; i < 5; i++) {
      var call = same[i];
      assert(call.resultLinked && call.resultStatus === (i % 2 === 0 ? 'done' : 'error'), 'same-tool FIFO status mismatch at call ' + i);
      assert(call.resultOutput === 'output-' + i, 'same-tool FIFO output mismatch at call ' + i);
      assert(call.toolRenderID === 'tool-' + call.index + '-' + i, 'stable tool ID mismatch at call ' + i);
      assert(call.completed === true, 'paired tool was not marked complete at call ' + i);
    }

    var mixed = makeSegments(4, 'mixed');
    linear(mixed);
    var expectedByName = {bash: 0, read_file: 0};
    for (var j = 4; j < mixed.length; j++) {
      var result = mixed[j];
      var expectedOrdinal = result.ordinal;
      var matchingCall = mixed.slice(0, 4).find(function(s) { return s.name === result.name && s.ordinal === expectedOrdinal; });
      assert(matchingCall && matchingCall.resultLinked && matchingCall.resultStatus === result.status && matchingCall.resultOutput === 'output-' + expectedOrdinal && matchingCall.completed === true, 'interleaved tool names did not retain per-name FIFO pairing and status');
      expectedByName[result.name]++;
    }
    assert(expectedByName.bash === 2 && expectedByName.read_file === 2, 'mixed-name fixture did not cover both queues');

    var missing = makeSegments(4, 'missing');
    linear(missing);
    for (var k = 0; k < 3; k++) assert(missing[k].completed === true && !missing[k].resultLinked, 'missing-result completion marker changed before trailing calls');
    assert(!missing[3].completed && !missing[3].resultLinked, 'final call without a result must stay in progress when only whitespace follows');
    var followedByText = [{type: 'using', name: 'bash', normalizedName: 'bash', index: 9}, {type: 'text', content: 'answer'}];
    linear(followedByText);
    assert(followedByText[0].completed === true, 'a call followed by visible text must retain its completion marker');
  }
  function verifyRenderedDOM(transcript, markup) {
    var cards = Array.from(container.querySelectorAll('.stream-tool'));
    assert(cards.length === 2, 'expected two rendered tool cards, got ' + cards.length);
    var firstCallIndex = transcript.indexOf('[Using tool: bash | echo one]');
    var secondCallIndex = transcript.indexOf('[Using tool: bash | echo two]');
    assert(cards[0].getAttribute('data-tool-render-id') === 'tool-' + firstCallIndex + '-0', 'first rendered tool ID changed');
    assert(cards[1].getAttribute('data-tool-render-id') === 'tool-' + secondCallIndex + '-1', 'second rendered tool ID changed');
    var firstOutput = cards[0].querySelector('.stream-tool-body-scroll[data-tool-row="out"] > pre');
    var secondOutput = cards[1].querySelector('.stream-tool-body-scroll[data-tool-row="out"] > pre');
    assert(firstOutput && firstOutput.textContent === 'first-result', 'first result output mismatch: selector=' + JSON.stringify(firstOutput && firstOutput.textContent) + ' card=' + cards[0].outerHTML);
    assert(secondOutput && secondOutput.textContent === 'second-error', 'second result output mismatch: ' + cards[1].outerHTML);
    var firstIcon = cards[0].querySelector('.stream-tool-summary svg');
    var secondIcon = cards[1].querySelector('.stream-tool-summary svg');
    assert(firstIcon && secondIcon && firstIcon.outerHTML !== secondIcon.outerHTML, 'done/error status indicators were not preserved per result');
    assert(markup === container.innerHTML, 'rendered DOM snapshot changed during comparison');
  }
  function p95(samples) {
    samples.sort(function(a, b) { return a - b; });
    return samples[Math.ceil(samples.length * 0.95) - 1];
  }

  (async function() {
    window.renderChatMarkdown = function(text) { return String(text).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); };
    window.renderChatMarkdownAsync = null;
    window.addCodeCopyButtons = function() {};
    window.codeRangesAsync = function(text) { return Promise.resolve(window.codeRanges(text)); };
    await window.renderStreamingContent(container, '', false);
    var linearLink = window.linkStreamingToolResults;
    assert(typeof linearLink === 'function', 'renderer did not install its production matcher');
    checkMatcherBehavior(legacyLinkStreamingToolResults);
    checkMatcherBehavior(linearLink);

    var domTranscript = '[Using tool: bash | echo one]\n[Using tool: bash | echo two]\n' +
      '[Tool bash done]\nfirst-result\n[/Tool]\n[Tool bash error]\nsecond-error\n[/Tool]';
    window.linkStreamingToolResults = legacyLinkStreamingToolResults;
    assert((await window.renderStreamingContent(container, domTranscript, false)) !== false, 'legacy rendering did not commit');
    var legacyMarkup = container.innerHTML;
    verifyRenderedDOM(domTranscript, legacyMarkup);
    window.linkStreamingToolResults = linearLink;
    assert((await window.renderStreamingContent(container, domTranscript, false)) !== false, 'linear rendering did not commit');
    verifyRenderedDOM(domTranscript, legacyMarkup);

    var sizes = [50, 100, 200, 400];
    var scenarios = ['same', 'mixed', 'missing'];
    for (var mode of ['legacy', 'linear']) {
      var matcher = mode === 'legacy' ? legacyLinkStreamingToolResults : linearLink;
      for (var scenario of scenarios) {
        report.matcher[mode][scenario] = {};
        for (var size of sizes) report.matcher[mode][scenario][String(size)] = measureMatcher(matcher, size, scenario);
      }
    }
    for (var renderMode of ['legacy', 'linear']) {
      var renderMatcher = renderMode === 'legacy' ? legacyLinkStreamingToolResults : linearLink;
      for (var renderSize of sizes) report.preparation[renderMode][String(renderSize)] = await measurePreparation(renderMatcher, renderSize);
    }
    window.linkStreamingToolResults = linearLink;

    var largeTranscript = makeLargeStreamTranscript(400);
    assert(largeTranscript.text.length >= 64 * 1024, 'large streamed fixture does not enter asynchronous preparation');
    var legacyLarge = await measureLargeStream(legacyLinkStreamingToolResults, largeTranscript, 400, linearLink);
    var linearLarge = await measureLargeStream(linearLink, largeTranscript, 400, linearLink);
    assert(legacyLarge.markup === linearLarge.markup, 'legacy and linear large-stream rendering differ');
    function largeMetrics(result) {
      return {
        inputBytes: result.inputBytes,
        matcherRuns: result.matcherRuns,
        fullRenderP95MS: result.fullRenderP95MS,
        frameGapP95MS: result.frameGapP95MS,
        renderedDOMSame: true
      };
    }
    report.largeRender = {legacy: largeMetrics(legacyLarge), linear: largeMetrics(linearLarge)};
    assert(linearLarge.frameGapP95MS <= legacyLarge.frameGapP95MS + 2, 'large-stream p95 frame gap exceeded baseline by more than 2ms: legacy=' + legacyLarge.frameGapP95MS.toFixed(3) + 'ms linear=' + linearLarge.frameGapP95MS.toFixed(3) + 'ms');

    for (var size of sizes) {
      for (var scenario of scenarios) {
        var legacyVisits = report.matcher.legacy[scenario][String(size)].visits;
        assert(legacyVisits === legacyQueueComparisons(size, scenario), 'legacy ' + scenario + ' comparisons at ' + size + ' = ' + legacyVisits);
        var visits = report.matcher.linear[scenario][String(size)].visits;
        assert(visits <= 2 * size, 'linear ' + scenario + ' queue visits at ' + size + ' exceeded 2N: ' + visits);
      }
    }
    var legacy400 = report.matcher.legacy.same['400'].p95MS;
    var linear400 = report.matcher.linear.same['400'].p95MS;
    var linear100 = report.matcher.linear.same['100'].p95MS;
    report.improvement400 = legacy400 > 0 ? 1 - linear400 / legacy400 : 0;
    report.growth100To400 = linear100 > 0 ? linear400 / linear100 : 0;
    assert(report.improvement400 >= 0.70, '400-pair p95 matching improvement was ' + (report.improvement400 * 100).toFixed(3) + '%, legacy=' + legacy400 + 'ms linear=' + linear400 + 'ms');
    assert(report.growth100To400 <= 6, 'linear matching p95 grew ' + report.growth100To400.toFixed(2) + 'x from 100 to 400 pairs');

    var largeStale = '[Using tool: bash]\n[Tool bash done]\n' + 'x'.repeat(70 * 1024) + '\n[/Tool]';
    var stalePromise = window.renderStreamingContent(container, largeStale, true);
    var newestTranscript = '[Using tool: bash]\n[Tool bash done]\nnewest render\n[/Tool]';
    assert((await window.renderStreamingContent(container, newestTranscript, false)) !== false, 'replacement render did not commit');
    var staleCommitted = await stalePromise;
    assert(staleCommitted === false, 'superseded large render did not report cancellation');
    assert(container.textContent.indexOf('newest render') !== -1 && container.textContent.indexOf('xxxxxxxx') === -1, 'stale render replaced newer output');

    root.setAttribute('data-matcher-metrics', btoa(unescape(encodeURIComponent(JSON.stringify(report)))));
    root.setAttribute('data-test-result', 'pass');
  })().catch(function(error) {
    root.setAttribute('data-test-result', 'fail');
    root.setAttribute('data-test-error', String(error && error.stack || error));
  });
});
</script></body></html>`

	fixtureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixture))
	}))
	defer fixtureServer.Close()

	result := runHeadlessChromeFixture(t, chrome, fixtureServer.URL, "Chat tool matching performance", 180000, 4*time.Minute)
	encoded := regexp.MustCompile(`data-matcher-metrics="([^"]+)"`).FindStringSubmatch(result)
	if len(encoded) != 2 {
		t.Fatalf("browser tool-matching fixture omitted metrics: %s", html.UnescapeString(regexp.MustCompile(`<main id="fixture-root"[^>]*>`).FindString(result)))
	}
	decoded, err := base64.StdEncoding.DecodeString(html.UnescapeString(encoded[1]))
	if err != nil {
		t.Fatalf("decode browser tool-matching metrics: %v", err)
	}
	var report chatMatcherBrowserReport
	if err := json.Unmarshal(decoded, &report); err != nil {
		t.Fatalf("parse browser tool-matching metrics: %v\n%s", err, decoded)
	}
	if report.Improvement400 < 0.70 {
		t.Fatalf("400-pair browser matching p95 improved %.1f%%; need at least 70%%", report.Improvement400*100)
	}
	if report.Growth100To400 > 6 {
		t.Fatalf("browser matching p95 grew %.2fx from 100 to 400 pairs; expected at most 6x", report.Growth100To400)
	}
	legacyLarge, legacyLargeOK := report.LargeRender["legacy"]
	linearLarge, linearLargeOK := report.LargeRender["linear"]
	if !legacyLargeOK || !linearLargeOK {
		t.Fatal("browser fixture omitted large asynchronous render measurements")
	}
	if legacyLarge.MatcherRuns != 12 || linearLarge.MatcherRuns != 12 {
		t.Fatalf("large async matcher measured %d legacy and %d optimized invocations; expected 12 each", legacyLarge.MatcherRuns, linearLarge.MatcherRuns)
	}
	if legacyLarge.InputBytes < 64*1024 || linearLarge.InputBytes < 64*1024 {
		t.Fatalf("large render did not enter asynchronous preparation: legacy=%d bytes linear=%d bytes", legacyLarge.InputBytes, linearLarge.InputBytes)
	}
	if !legacyLarge.RenderedDOMSame || !linearLarge.RenderedDOMSame {
		t.Fatal("large streamed output did not render equivalently under both matchers")
	}
	if linearLarge.FrameGapP95MS > legacyLarge.FrameGapP95MS+2 {
		t.Fatalf("large-render p95 frame gap exceeded baseline by more than 2ms: legacy %.3fms linear %.3fms", legacyLarge.FrameGapP95MS, linearLarge.FrameGapP95MS)
	}
	for scenario, sizes := range report.Matcher["linear"] {
		for size, measurement := range sizes {
			n := mustAtoi(t, size)
			wantLinearVisits := n
			if scenario != "missing" {
				wantLinearVisits += n
			}
			if measurement.Visits != wantLinearVisits {
				t.Fatalf("linear %s matcher visited %d queue entries at N=%s, want %d", scenario, measurement.Visits, size, wantLinearVisits)
			}
			legacy := report.Matcher["legacy"][scenario][size]
			wantLegacyVisits := 0
			switch scenario {
			case "same":
				wantLegacyVisits = n * (n + 1) / 2
			case "mixed":
				firstQueue, secondQueue := (n+1)/2, n/2
				wantLegacyVisits = firstQueue*(firstQueue+1)/2 + secondQueue*(secondQueue+1)/2
			}
			if legacy.Visits != wantLegacyVisits {
				t.Fatalf("legacy %s matcher visited %d queue entries at N=%s, want %d", scenario, legacy.Visits, size, wantLegacyVisits)
			}
		}
	}
	t.Logf("large streamed render (400 calls, %d bytes; matcher runs legacy %d linear %d): full-render p95 legacy %.3fms linear %.3fms; frame-gap p95 legacy %.3fms linear %.3fms; DOM equivalent=%t",
		linearLarge.InputBytes, legacyLarge.MatcherRuns, linearLarge.MatcherRuns,
		legacyLarge.FullRenderP95MS, linearLarge.FullRenderP95MS,
		legacyLarge.FrameGapP95MS, linearLarge.FrameGapP95MS, linearLarge.RenderedDOMSame)
	t.Logf("browser matcher measurements (same Chrome/runtime): %s", decoded)
	for size, legacy := range report.Preparation["legacy"] {
		linear := report.Preparation["linear"][size]
		t.Logf("renderStreamingContent preparation N=%s: legacy p95 %.3fms, linear p95 %.3fms", size, legacy.P95MS, linear.P95MS)
	}
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		t.Fatalf("parse benchmark size %q: %v", value, err)
	}
	return n
}
