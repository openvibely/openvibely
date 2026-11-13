package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ModalPreservesShellWidth(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if err := layout.Base("Models", nil, "default").Render(context.Background(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "modal-gutters", func(b *composerFocusCDP) {
		b.waitFor("shell loaded", `String(document.readyState === 'complete' && !!document.getElementById('sidebar'))`, "true")
		// Finish startup font/layout work before attributing bounds changes to a modal.
		b.evaluateAwait(`(async()=>{await document.fonts.ready;await Promise.all(document.getAnimations().filter(a=>a.effect.getComputedTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})));await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));return 'ready'})()`)
		b.evaluate(`window.shellBounds=()=>JSON.stringify(['body','#sidebar','.drawer-content'].map(s=>{const r=document.querySelector(s).getBoundingClientRect();return [r.x,r.width]}));const d=document.createElement('dialog');d.id='gutter-test';d.className='modal';d.innerHTML='<div class="modal-box">Test</div>';document.body.appendChild(d);'ok'`)
		b.waitFor("closed modal excluded from layout", `getComputedStyle(document.getElementById('gutter-test')).display`, "none")
		b.waitFor("shell clips without scrolling", `getComputedStyle(document.querySelector('.drawer-content')).overflow`, "clip")
		b.evaluate(`window.beforeModal=shellBounds();document.getElementById('gutter-test').showModal();'ok'`)
		b.waitFor("modal does not reserve root gutters", `getComputedStyle(document.documentElement).scrollbarGutter`, "auto")
		b.waitFor("opening modal preserves shell bounds", `shellBounds()===beforeModal ? "true" : JSON.stringify({before:JSON.parse(beforeModal),after:JSON.parse(shellBounds())})`, "true")
		b.evaluate(`document.getElementById('gutter-test').close();'ok'`)
		b.waitFor("closed modal releases layout", `String(document.getElementById('gutter-test').getClientRects().length===0)`, "true")
		b.waitFor("closing modal preserves shell bounds", `shellBounds()===beforeModal ? "true" : JSON.stringify({before:JSON.parse(beforeModal),after:JSON.parse(shellBounds())})`, "true")
	})
}
