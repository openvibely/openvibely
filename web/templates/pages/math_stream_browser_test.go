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
	runStreamingMathBrowser(t, func(b *composerFocusCDP) {
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

func runStreamingMathBrowser(t *testing.T, run func(*composerFocusCDP)) {
	t.Helper()
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
	runComposerFocusCDP(t, chrome, server.URL, "streaming-math", run)
}

func TestBrowserFunctional_StreamingMathWithLazyTools(t *testing.T) {
	runStreamingMathBrowser(t, func(b *composerFocusCDP) {
		b.waitFor("streaming fixture ready", `String(document.readyState === 'complete' && typeof window.renderLiveChatContent === 'function' && !!window.installChatVirtualRows)`, "true")
		b.evaluateAwait(`(async()=>{
   const messages=document.getElementById('chat-messages');
   messages.style.cssText='height:280px;overflow:auto';
   window.mathSource='Equation \\(x=1\\). Another \\(x=1\\).';
   await renderLiveChatContent(document.getElementById('math-stream'),window.mathSource);
   return 'ready';
  })()`)
		b.waitFor("both equations styled", `String(document.querySelectorAll('#math-stream [data-math-ready="true"]').length===2)`, "true")
		result := b.evaluateAwait(`(async()=>{
   const root=document.getElementById('math-stream'),messages=document.getElementById('chat-messages');
   const original=Array.from(root.querySelectorAll('[data-math-ready]'));
   const tools=Array.from({length:160},(_,i)=>'[Using tool: bash | echo '+i+']\n[Tool bash done]\nresult '+i+'\n[/Tool]\n').join('');
   const source=window.mathSource+'\n\n'+tools+'\nTail \\(x=2\\).\n\n'+('Later paragraph.\n\n').repeat(60);
   await renderLiveChatContent(root,source,true,true);
   messages.scrollTop=0;messages.dispatchEvent(new Event('scroll'));
   await new Promise(requestAnimationFrame);
   await renderVisibleChatRows(messages);
   const math=Array.from(root.querySelectorAll('[data-math-ready]'));
   if(math[0]!==original[0]||math[1]!==original[1]||math[0]===math[1])throw Error('equations replaced or duplicate nodes merged');
   if(!root._chatVirtualData||root._chatVirtualData.source!==source)throw Error('math disabled transcript virtualization');
   const button=root.querySelector('.stream-activity-summary');
   if(!button||!button.textContent.includes('160 tool calls'))throw Error('missing grouped tools');
   if(root.querySelector('.stream-tool'))throw Error('collapsed tools rendered eagerly');
   messages.scrollTop=0;messages.dispatchEvent(new Event('scroll'));
   await new Promise(requestAnimationFrame);
   button.click();
   await new Promise(requestAnimationFrame);
   await renderVisibleChatRows(messages);
   const rendered=root.querySelectorAll('.stream-tool');
   if(!rendered.length||rendered.length>=160)throw Error('expanded tools not bounded: '+rendered.length);
   if(!root.textContent.includes('result 0'))throw Error('first tool output missing');
   button.click();
   await renderVisibleChatRows(messages);
   if(root.querySelector('.stream-tool'))throw Error('collapse retained tool DOM');
   if(root.querySelector('[data-math-ready]')!==original[0])throw Error('expansion replaced math');
   return 'pass';
  })()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}
