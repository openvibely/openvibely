package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_StreamingMath(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	base := renderTerminalBrowserComponent(t, layout.Base("Math", nil, ""))
	shared := renderTerminalBrowserComponent(t, components.ChatAutoScrollScript())
	fixture := strings.Replace(base, "</body>", shared+`<div id="chat-messages"><div id="math-stream"></div></div></body>`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "streaming-math", func(b *composerFocusCDP) {
		b.waitFor("streaming fixture ready", `String(document.readyState !== 'loading' && typeof window.renderLiveChatContent === 'function' && !!document.getElementById('math-stream'))`, "true")
		b.evaluateAwait(`(async()=>{window.mathSource='Bayes: \\(P(A \\mid B)=\\frac{P(B \\mid A)P(A)}{P(B)}\\).';await window.renderLiveChatContent(document.getElementById('math-stream'),window.mathSource);return 'ready'})()`)
		b.waitFor("styled equation", `String(!!document.querySelector('#math-stream [data-math-ready="true"] .katex'))`, "true")
		result := b.evaluateAwait(`(async()=>{
   const root=document.getElementById('math-stream'),first=root.querySelector('[data-math-ready]');
   const bounds=first.getBoundingClientRect();
   for(const suffix of [' Here',' Here is',' Here is the prior \\(P(A)\\).',' Here is the prior \\(P(A)\\). Next: \\(P(R',' Here is the prior \\(P(A)\\). Next: \\(P(R)=0.55\\).']){
    await window.renderLiveChatContent(root,window.mathSource+suffix);
    if(root.querySelector('[data-math-ready]')!==first)throw Error('completed equation replaced');
    if(root.querySelector('[data-math-tex]:not([data-math-ready="true"])'))throw Error('unformatted equation committed');
    if(Math.abs(first.getBoundingClientRect().height-bounds.height)>1)throw Error('equation height changed');
    await new Promise(requestAnimationFrame);
   }
   await window.renderLiveChatContent(root,window.mathSource+' Here is the prior \\(P(A)\\). Next: \\(P(R)=0.55\\).',true,true);
   if(root.querySelector('[data-math-ready]')!==first)throw Error('final render replaced equation');
   if(root.querySelectorAll('.katex').length!==3)throw Error('missing equations');
   return 'pass';
  })()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}
