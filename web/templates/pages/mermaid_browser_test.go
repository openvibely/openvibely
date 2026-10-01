package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_MermaidCompletedMessagesAndGallery(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	base := renderTerminalBrowserComponent(t, layout.Base("Mermaid fixture", nil, ""))
	shared := renderTerminalBrowserComponent(t, components.ChatAutoScrollScript())
	fixture := strings.Replace(base, "</body>", shared+`<div id="chat-messages"></div><div id="task-thread-messages"></div></body>`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "mermaid", func(browser *composerFocusCDP) {
		browser.waitFor("shared renderer", `typeof window.renderStreamingContent`, "function")
		got := browser.evaluateAwait("(async()=>{" + `
   const assert=(ok,message)=>{if(!ok)throw new Error(message)};
   const fence=String.fromCharCode(96).repeat(3);
   const source=fence+'mermaid\nflowchart LR\n A[Start] --> B[Finish]\n'+fence;
   function message(parent,status,raw) {
    const pair=document.createElement('div'); pair.dataset.executionPair='true';pair.dataset.execStatus=status;
    pair.innerHTML='<div class="chat-bubble-assistant-msg"><div class="chat-stream-content"></div></div>';
    const content=pair.querySelector('.chat-stream-content');content.dataset.rawContent=raw;
    document.getElementById(parent).appendChild(pair);
    return {pair,content};
   }
   for (const parent of ['chat-messages','task-thread-messages']) {
    const live=message(parent,'running',source);
    await window.renderLiveChatContent(live.content,source);
    await window.renderMermaidDiagrams(live.pair);
    assert(!live.pair.querySelector('.chat-mermaid'),'stream rendered early');
    assert(live.content.querySelector('code').textContent.includes('Start'),'stream source missing');
    window.applyChatExecutionTerminalStatus(live.pair,'completed');
    await window.renderMermaidDiagrams(live.pair);
    assert(live.pair.querySelector('.chat-mermaid img'),'completion did not render');
    const saved=message(parent,'completed',source);
    await window.cleanAssistantMessages(document.getElementById(parent));
    assert(saved.pair.querySelector('.chat-mermaid img'),'saved diagram missing');
    const invalid=message(parent,'completed',fence+'mermaid\nflowchart LR\n A --> [');
    await window.cleanAssistantMessages(document.getElementById(parent));
    assert(invalid.content.querySelector('code').textContent.includes('A --> ['),'invalid unclosed source lost');
    assert(!invalid.content.querySelector('.chat-mermaid'),'invalid diagram rendered');
    const unclosed=message(parent,'completed',source.slice(0,-3));
    await window.cleanAssistantMessages(document.getElementById(parent));
    assert(unclosed.content.querySelector('.chat-mermaid'),'valid unclosed fence not rendered');
    // Terminal status may precede the authoritative final render (live Chat path).
    const final=message(parent,'running','partial');
    window.applyChatExecutionTerminalStatus(final.pair,'completed');
    await window.renderLiveChatContent(final.content,source);
    await window.renderMermaidDiagrams(final.pair);
    assert(final.content.querySelector('.chat-mermaid'),'terminal-before-final-render missed');
   }
   const large=message('chat-messages','completed','Context '.repeat(9500)+'\n\n'+source);
   await window.cleanAssistantMessages(document.getElementById('chat-messages'));
   assert(large.content.querySelector('.chat-mermaid'),'async Markdown worker missed diagram');
   const raw=message('chat-messages','completed','<svg onload="window.mermaidUnsafe=true"><script>window.mermaidUnsafe=true</script></svg>');
   await window.cleanAssistantMessages(document.getElementById('chat-messages'));
   assert(!raw.content.querySelector('svg,script')&&!window.mermaidUnsafe,'raw SVG accepted');
   const configured=message('chat-messages','completed',fence+'mermaid\n%%{init: {"securityLevel":"loose","htmlLabels":true,"flowchart":{"htmlLabels":true},"themeCSS":"body{color:red}"}}%%\nflowchart LR\n A[Safe] --> B[Node]\nclick A "javascript:alert(1)"\n'+fence);
   await window.cleanAssistantMessages(document.getElementById('chat-messages'));
   assert(configured.content.querySelector('.chat-mermaid'),'directive fixture failed');
   const exported=await (await fetch(configured.content.querySelector('.chat-mermaid').href)).text();
   const exportedDOM=new DOMParser().parseFromString(exported,'image/svg+xml');
   assert(!exportedDOM.querySelector('script,foreignObject,a[href]'),'active exported SVG content');
   assert(!Array.from(exportedDOM.querySelectorAll('*')).some(el=>Array.from(el.attributes).some(a=>/^on/i.test(a.name)||/javascript:/i.test(a.value))),'unsafe SVG attribute');
   const config=window.mermaid.mermaidAPI.getConfig();
   assert(config.securityLevel==='strict'&&config.htmlLabels===false&&config.flowchart.htmlLabels===false,'security override accepted');
   assert(config.themeCSS!=='body{color:red}','theme CSS override accepted');
   for (const diagram of ['sequenceDiagram\n Alice->>Bob: Hello', 'classDiagram\n Animal <|-- Duck', '---\nconfig:\n  securityLevel: loose\n  htmlLabels: true\n---\nflowchart LR\n A --> B']) {
    const extra=message('chat-messages','completed',fence+'mermaid\n'+diagram+'\n'+fence);
    await window.cleanAssistantMessages(document.getElementById('chat-messages'));
    assert(extra.content.querySelector('.chat-mermaid'),'additional diagram missing');
    assert(window.mermaid.mermaidAPI.getConfig().securityLevel==='strict','frontmatter security override');
   }
   // A render finishing after navigation must not replace or resurrect old DOM.
   const stale=message('chat-messages','completed',source);
   const originalRender=window.mermaid.render;
   let started,release;
   const began=new Promise(resolve=>started=resolve), gate=new Promise(resolve=>release=resolve);
   window.mermaid.render=async(...args)=>{started();await gate;return originalRender(...args)};
   await window.renderStreamingContent(stale.content,source);
   const pending=window.renderMermaidDiagrams(stale.pair);
   await began;stale.pair.remove();release();await pending;
   window.mermaid.render=originalRender;
   assert(!stale.pair.querySelector('.chat-mermaid'),'detached render committed');
   assert(!document.querySelector('[id^="ov-mermaid-"]'),'temporary render DOM leaked');
   const link=document.querySelector('.chat-mermaid');
   const svg=await (await fetch(link.href)).text();
   assert(svg.includes('<svg')&&svg.includes('Start'),'SVG download contents');
   const before=document.querySelectorAll('.chat-mermaid').length;
   await window.cleanAssistantMessages(document.getElementById('chat-messages'));
   assert(document.querySelectorAll('.chat-mermaid').length===before,'duplicate diagrams');
   link.click();
   const gallery=document.getElementById('image-gallery');
   assert(gallery.open,'gallery did not open');
   await gallery.querySelector('img').decode();
   assert(gallery.querySelector('[data-gallery-download]').download.endsWith('.svg'),'download filename');
   assert(gallery.querySelector('[data-gallery-download]').href===link.href,'download URL');
   gallery.querySelector('[data-gallery-plus]').click();
   assert(gallery.querySelector('img').style.transform.includes('scale(1.5)'),'zoom unavailable');
   gallery.querySelector('[data-gallery-fit]').click();
   assert(gallery.querySelector('img').style.transform.includes('scale(1)'),'fit unavailable');
   const closed=new Promise(resolve=>gallery.addEventListener('close',resolve,{once:true}));
   window.imageGallery.close();await closed;
   // Serialized history retains usable image/download URLs without object-URL leaks.
   const restored=document.createElement('div');restored.innerHTML=document.getElementById('chat-messages').innerHTML;
   document.body.appendChild(restored);
   const restoredImage=restored.querySelector('.chat-mermaid img');await restoredImage.decode();
   assert(restoredImage.naturalWidth>0,'restored SVG unavailable');
   link.click();await gallery.querySelector('img').decode();
   for(let i=0;i<6;i++)gallery.querySelector('[data-gallery-plus]').click();
   return 'pass';
  })()`)
		if got != "pass" {
			t.Fatalf("Mermaid browser coverage: %s", got)
		}
		var point struct{ X, Y float64 }
		coords := browser.evaluate(`(()=>{const r=document.querySelector('[data-gallery-stage]').getBoundingClientRect();return JSON.stringify({X:r.left+r.width/2,Y:r.top+r.height/2})})()`)
		if err := json.Unmarshal([]byte(coords), &point); err != nil {
			t.Fatal(err)
		}
		before := browser.evaluate(`document.querySelector('[data-gallery-image]').style.transform`)
		for _, params := range []map[string]any{
			{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1},
			{"type": "mouseMoved", "x": point.X + 80, "y": point.Y + 30, "button": "left", "buttons": 1},
			{"type": "mouseReleased", "x": point.X + 80, "y": point.Y + 30, "button": "left", "buttons": 0, "clickCount": 1},
		} {
			browser.call("Input.dispatchMouseEvent", params, nil)
		}
		if after := browser.evaluate(`document.querySelector('[data-gallery-image]').style.transform`); after == before {
			t.Fatal("gallery pan did not move the diagram")
		}
	})
}
