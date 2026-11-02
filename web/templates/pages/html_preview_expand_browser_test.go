package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func runHTMLPreviewBrowser(t *testing.T, run func(*composerFocusCDP, *atomic.Int32)) {
	t.Helper()
	chrome := chatNavigationChromePath(t)
	fixture := renderTerminalBrowserComponent(t, layout.Base("HTML preview", nil, ""))
	var loads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, fixture)
		case "/slow-image":
			<-r.Context().Done()
		case "/files/abcdef", "/files/abcdee":
			switch {
			case r.URL.Query().Has("metadata"):
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"file_name":"mockup.html","media_type":"text/html"}`)
			case r.URL.Query().Has("view"):
				w.Header().Set("Content-Type", "text/html")
				components.FilePreview("mockup.html", "html", "Mockup source", r.URL.Path).Render(r.Context(), w)
			default:
				loads.Add(1)
				w.Header().Set("Content-Type", "text/html")
				if r.URL.Path == "/files/abcdee" {
					fmt.Fprint(w, `<html><body><input value="Interactive mockup"><img src="/slow-image"><script>parent.postMessage('mockup-parsed','*')</script></body></html>`)
				} else {
					fmt.Fprint(w, `<html><body><input value="Interactive mockup"></body></html>`)
				}
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "html-preview", func(b *composerFocusCDP) { run(b, &loads) })
}

func TestBrowserFunctional_HTMLPreviewExpansion(t *testing.T) {
	runHTMLPreviewBrowser(t, func(b *composerFocusCDP, loads *atomic.Int32) {
		b.waitFor("viewer", `typeof window.OVFileViewer?.open`, "function")
		b.evaluate(`const link=document.createElement('a');link.href='/files/abcdef';link.textContent='Mockup';document.body.append(link);'ready'`)
		b.waitFor("loaded inline preview", `String(!!document.querySelector('.file-html-canvas iframe') && document.querySelector('.file-html-canvas iframe').style.visibility==='')`, "true")
		b.evaluate(`window.htmlFrame=document.querySelector('.file-html-canvas iframe');window.htmlLoads=0;htmlFrame.addEventListener('load',()=>htmlLoads++);'ready'`)
		for i := 0; i < 2; i++ {
			b.evaluate(`document.querySelector('.file-html-canvas button').click();'opened'`)
			b.waitFor("reused expanded frame", `String(document.querySelector('#image-gallery [data-file-viewer="html"] iframe')===htmlFrame)`, "true")
			b.evaluate(`document.querySelector('[data-file-html-mode="source"]').click();'source'`)
			b.evaluate(`window.imageGallery.close();'closed'`)
			b.waitFor("restored visible frame", `String(document.querySelector('.file-html-canvas iframe')===htmlFrame && !htmlFrame.hidden)`, "true")
		}
		// Give browser load events time to expose accidental browsing-context resets.
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('ready'),150))`)
		if got := b.evaluate(`String(htmlLoads)`); got != "0" {
			t.Fatalf("preview reloaded %s times", got)
		}
		if loads.Load() != 1 {
			t.Fatalf("raw HTML requested %d times, want 1", loads.Load())
		}
		// Older engines still open a fresh, sandboxed preview and reveal it on load.
		b.evaluate(`document.querySelector('[data-result-content]').moveBefore=undefined;document.querySelector('.file-html-canvas button').click();'opened'`)
		b.waitFor("fallback loaded", `String(!!document.querySelector('#image-gallery [data-file-viewer="html"] iframe') && document.querySelector('#image-gallery iframe').style.visibility==='')`, "true")
		if got := b.evaluate(`String(document.querySelector('#image-gallery iframe')!==htmlFrame && document.querySelector('#image-gallery iframe').getAttribute('sandbox')==='allow-scripts' && document.querySelector('.file-html-canvas iframe')===htmlFrame)`); got != "true" {
			t.Fatal("fallback changed the inline frame or sandbox")
		}
		b.evaluate(`window.imageGallery.close();'closed'`)
	})
}

func TestBrowserFunctional_HTMLPreviewDetachedRow(t *testing.T) {
	runHTMLPreviewBrowser(t, func(b *composerFocusCDP, _ *atomic.Int32) {
		b.waitFor("viewer", `typeof window.OVFileViewer?.open`, "function")
		b.evaluate(`const link=document.createElement('a');link.href='/files/abcdef';link.textContent='Mockup';document.body.append(link);'ready'`)
		b.waitFor("inline frame", `String(!!document.querySelector('.file-html-canvas iframe') && document.querySelector('.file-html-canvas iframe').style.visibility==='')`, "true")
		b.evaluate(`window.canvas=document.querySelector('.file-html-canvas');window.frame=canvas.querySelector('iframe');canvas.querySelector('button').click();'opened'`)
		b.waitFor("expanded frame", `String(document.querySelector('#image-gallery iframe')===frame)`, "true")
		result := b.evaluateAwait(`(async()=>{
 canvas.remove();
 await new Promise(resolve=>{document.getElementById('image-gallery').addEventListener('close',resolve,{once:true});imageGallery.close()});
 if(canvas.querySelector('iframe')!==frame||frame.hidden||canvas.hasAttribute('data-file-html-expanded'))return 'frame not restored';
 document.body.append(canvas);
 return 'restored';
 })()`)
		if result != "restored" {
			t.Fatal(result)
		}
		b.evaluate(`canvas.querySelector('button').click();'opened'`)
		b.waitFor("expand works again", `String(document.querySelector('#image-gallery iframe')===frame)`, "true")
		b.evaluate(`imageGallery.close();'closed'`)
	})
}

func TestBrowserFunctional_HTMLPreviewSlowResource(t *testing.T) {
	runHTMLPreviewBrowser(t, func(b *composerFocusCDP, _ *atomic.Int32) {
		b.waitFor("viewer", `typeof window.OVFileViewer?.open`, "function")
		b.evaluate(`window.addEventListener('message',event=>{if(event.data==='mockup-parsed')window.mockupParsed=true});const link=document.createElement('a');link.href='/files/abcdee';link.textContent='Mockup';document.body.append(link);'ready'`)
		b.waitFor("HTML parsed", `String(window.mockupParsed)`, "true")
		b.evaluate(`window.frame=document.querySelector('.file-html-canvas iframe');window.frameLoaded=false;frame.addEventListener('load',()=>frameLoaded=true);'ready'`)
		b.waitFor("usable HTML revealed", `String(frame.style.visibility==='')`, "true")
		if got := b.evaluate(`String(frameLoaded)`); got != "false" {
			t.Fatal("slow fixture unexpectedly finished loading")
		}
		// Also exercise a new iframe in the expanded viewer on older engines.
		b.evaluate(`document.querySelector('[data-result-content]').moveBefore=undefined;document.querySelector('.file-html-canvas button').click();'opened'`)
		b.waitFor("expanded HTML revealed", `String(!!document.querySelector('#image-gallery iframe') && document.querySelector('#image-gallery iframe').style.visibility==='')`, "true")
		b.evaluate(`imageGallery.close();'closed'`)
	})
}

func TestBrowserFunctional_HTMLPreviewExpandedRowStaysMounted(t *testing.T) {
	runHTMLPreviewBrowser(t, func(b *composerFocusCDP, _ *atomic.Int32) {
		b.waitFor("viewer", `typeof window.OVFileViewer?.open`, "function")
		b.evaluate(`
 window.messages=document.createElement('div');messages.id='chat-messages';messages.style.cssText='height:300px;overflow:auto';
 const pair=document.createElement('div');pair.setAttribute('data-execution-pair','');pair.setAttribute('data-exec-status','completed');messages.append(pair);
 window.previewRow=document.createElement('div');previewRow.className='chat-markdown';previewRow.innerHTML='<a href="/files/abcdef">Mockup</a>';pair.append(previewRow);
 for(let i=0;i<45;i++){const row=document.createElement('div');row.className='chat-markdown';row.style.height='100px';row.textContent='Row '+i;pair.append(row)}
 document.body.append(messages);'ready'`)
		b.waitFor("inline frame", `String(!!previewRow.querySelector('iframe') && previewRow.querySelector('iframe').style.visibility==='')`, "true")
		b.evaluate(`window.frame=previewRow.querySelector('iframe');window.frameLoads=0;frame.addEventListener('load',()=>frameLoads++);previewRow.querySelector('button').click();'opened'`)
		b.waitFor("expanded frame", `String(document.querySelector('#image-gallery iframe')===frame)`, "true")
		b.evaluateAwait(`(async()=>{messages.scrollTop=messages.scrollHeight;await renderVisibleChatRows(messages);return 'scrolled'})()`)
		b.waitFor("offscreen rows virtualized", `String(!!messages.querySelector('[data-virtual-chat-row]'))`, "true")
		if got := b.evaluate(`String(previewRow.isConnected && previewRow.getBoundingClientRect().bottom<messages.getBoundingClientRect().top)`); got != "true" {
			t.Fatal("expanded preview row was not retained offscreen")
		}
		b.evaluate(`imageGallery.close();'closed'`)
		b.waitFor("frame restored", `String(previewRow.querySelector('iframe')===frame && !previewRow.querySelector('[data-file-html-expanded]'))`, "true")
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('ready'),100))`)
		if got := b.evaluate(`String(frameLoads)`); got != "0" {
			t.Fatal("retained iframe reloaded: " + got)
		}
	})
}

func TestBrowserFunctional_HTMLPreviewThinkingLinks(t *testing.T) {
	runHTMLPreviewBrowser(t, func(b *composerFocusCDP, loads *atomic.Int32) {
		b.waitFor("viewer", `typeof window.OVFileViewer?.open`, "function")
		b.evaluate(`const thinking=document.createElement('details');thinking.className='stream-thinking';thinking.open=true;thinking.innerHTML='<summary>Thinking</summary><div class="chat-markdown"><a href="/files/abcdef">Mockup</a></div>';document.body.append(thinking);window.thinkingLink=thinking.querySelector('a');'ready'`)
		b.waitFor("HTML metadata processed", `String(thinkingLink.dataset.fileName)`, "mockup.html")
		if got := b.evaluate(`String(!document.querySelector('.file-html-canvas') && thinkingLink.hasAttribute('data-file-preview'))`); got != "true" {
			t.Fatal("thinking link created an inline preview or lost viewer binding")
		}
		if loads.Load() != 0 {
			t.Fatalf("thinking link loaded HTML %d times before opening", loads.Load())
		}
		b.evaluate(`thinkingLink.click();'opened'`)
		b.waitFor("viewer loaded", `String(!!document.querySelector('#image-gallery[open] iframe') && document.querySelector('#image-gallery iframe').style.visibility==='')`, "true")
		if loads.Load() != 1 {
			t.Fatalf("opening thinking link requested HTML %d times, want 1", loads.Load())
		}
		b.evaluate(`imageGallery.close();const link=document.createElement('a');link.href='/files/abcdef';link.textContent='Final mockup';document.body.append(link);'ready'`)
		b.waitFor("normal response inline preview", `String(!!document.querySelector('.file-html-canvas iframe'))`, "true")
	})
}
