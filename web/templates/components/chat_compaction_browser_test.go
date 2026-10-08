package components

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowserChatCompactionActivity(t *testing.T) {
	chrome := testChromePath(t)
	var script bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &script); err != nil {
		t.Fatal(err)
	}
	fixture := `<!doctype html><html><body><main id="fixture-root" data-test-result="pending"><div id="response"></div></main><script>` + renderedBaseMarkdownCodeHelpers(t) + `</script>` + script.String() + `<script>
window.addEventListener('DOMContentLoaded', async function() {
 var root=document.getElementById('fixture-root'), response=document.getElementById('response');
 function assert(ok, message) {if(!ok) throw Error(message);}
 try {
  var before='Checking files.\n[Compaction started]\n';
  await window.renderStreamingContent(response,before,false);
  assert(response.querySelector('[data-compaction-state="started"] .loading-spinner'), 'missing live spinner');
  assert(response.textContent.includes('Compacting context…'),'missing live label');
  var complete=before+'[Compaction done | 4000]\nContinuing work.';
  await window.renderStreamingContent(response,complete,false);
  assert(response.querySelectorAll('[data-compaction-state]').length===1,'start and finish duplicated');
  assert(!response.querySelector('.loading-spinner'),'spinner remained after completion');
  assert(response.textContent.includes('Context compacted · 4.0s'),'missing duration');
  assert(response.textContent.indexOf('Checking files.')<response.textContent.indexOf('Context compacted'),'wrong position');
  assert(response.textContent.indexOf('Context compacted')<response.textContent.indexOf('Continuing work.'),'wrong continuation position');
  response.replaceChildren();
  await window.renderStreamingContent(response,complete,false);
  assert(response.querySelector('[data-compaction-state="done"]'),'history reload lost activity');
  await window.renderStreamingContent(response,complete+'\n[Compaction started]\n[Compaction failed | 500]\n',false);
  assert(response.querySelectorAll('[data-compaction-state]').length===2,'repeated attempts merged');
  assert(response.textContent.includes('Compaction failed · 0.5s'),'missing failure');
  var fence=String.fromCharCode(96).repeat(3);
  var quoted=fence+'text\n[Compaction started]\n[Compaction done | 4000]\n'+fence;
  await window.renderStreamingContent(response,quoted,false);
  assert(!response.querySelector('[data-compaction-state]'),'code example rendered as activity');
  await window.renderStreamingContent(response,'[Using tool: bash]\n[Tool bash done]\n[Compaction started]\n[/Tool]\n',false);
  assert(!response.querySelector('[data-compaction-state]'),'tool output rendered as activity');
  assert(!window.cleanTranscriptControls(complete).includes('[Compaction'),'controls leaked into plain display');
  root.setAttribute('data-exec-status','failed');
  await window.renderStreamingContent(response,before,false);
  assert(response.textContent.includes('Compaction interrupted') && !response.querySelector('.loading-spinner'),'ended execution retained live compaction');
  root.setAttribute('data-test-result','pass');
 } catch(error) {root.setAttribute('data-test-result','fail'); root.setAttribute('data-test-error',error.stack || error);}
});
</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "Compaction activity", 10000, 30*time.Second)
}
