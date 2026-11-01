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

func TestBrowserFunctional_HTMLPreviewExpansion(t *testing.T) {
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
		case "/files/abcdef":
			switch {
			case r.URL.Query().Has("metadata"):
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"file_name":"mockup.html","media_type":"text/html"}`)
			case r.URL.Query().Has("view"):
				w.Header().Set("Content-Type", "text/html")
				components.FilePreview("mockup.html", "html", "Mockup source", "/files/abcdef").Render(r.Context(), w)
			default:
				loads.Add(1)
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<html><body><input value="Interactive mockup"></body></html>`)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "html-expand", func(b *composerFocusCDP) {
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
