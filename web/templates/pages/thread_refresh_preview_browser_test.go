package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ThreadRefreshPreviewIsDisplayOnly(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	fixture := renderTerminalBrowserComponent(t, layout.Base("Refresh preview", nil, ""))
	var metadata atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, fixture)
		case "/files/abc123":
			if r.URL.Query().Get("metadata") == "1" {
				metadata.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"file_name":"image.svg","media_type":"image/svg+xml"}`)
			} else {
				w.Header().Set("Content-Type", "image/svg+xml")
				fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="blue"/></svg>`)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "refresh-preview", func(b *composerFocusCDP) {
		b.waitFor("file renderer", `typeof window.OVFileViewer?.prepareStream`, "function")
		b.evaluateAwait(`(async()=>{
   const preview=document.createElement('section');preview.dataset.threadRefreshPreview='true';preview.inert=true;
   const link=document.createElement('a');link.href='/files/abc123';link.textContent='Saved image';
   const image=document.createElement('img');image.src='data:image/svg+xml,<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"/>';link.prepend(image);preview.append(link);
   // An already-rendered SVG retains its source in a details/code element.
   const figure=document.createElement('figure');figure.className='chat-svg-preview';figure.append(image.cloneNode());
   const details=document.createElement('details'),pre=document.createElement('pre'),code=document.createElement('code');code.className='language-svg';code.textContent='<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="red"/></svg>';pre.append(code);details.append(pre);figure.append(details);preview.append(figure);
   const math=document.createElement('span');math.dataset.mathTex='x^2';preview.append(math);
   window.refreshPreviewFixture=preview;
   window.refreshPreviewOriginal=preview.innerHTML;
   window.refreshPreviewMutations=0;
   window.refreshPreviewObserver=new MutationObserver(records=>{window.refreshPreviewMutations+=records.length});
   refreshPreviewObserver.observe(preview,{childList:true,subtree:true,attributes:true,characterData:true});
   document.body.append(preview);
   // Allow the real document observers and their animation-frame scans to run.
   await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
   await new Promise(resolve=>setTimeout(resolve,150));
   return 'ready';
  })()`)
		if metadata.Load() != 0 {
			t.Fatalf("display-only preview fetched file metadata %d times", metadata.Load())
		}
		if got := b.evaluate(`String(refreshPreviewMutations===0 && refreshPreviewFixture.innerHTML===refreshPreviewOriginal)`); got != "true" {
			t.Fatal("background renderers changed the display-only preview")
		}
		// The same elements outside the preview must still be enhanced normally.
		b.evaluate(`refreshPreviewObserver.disconnect();refreshPreviewFixture.removeAttribute('data-thread-refresh-preview');refreshPreviewFixture.append(document.createTextNode('live'));'ready'`)
		b.waitFor("ordinary file and SVG rendering", `String(!!refreshPreviewFixture.querySelector('a[data-image-gallery-item]') && !!refreshPreviewFixture.querySelector('.chat-svg-preview img[src^="blob:"]') && !!refreshPreviewFixture.querySelector('[data-math-ready="true"]'))`, "true")
		if metadata.Load() != 1 {
			t.Fatalf("ordinary file link metadata requests = %d, want 1", metadata.Load())
		}
	})
}
