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
		w.Header().Set("Content-Type", "text/html")
		if err := layout.Base("Models", nil, "default").Render(context.Background(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "modal-gutters", func(b *composerFocusCDP) {
		b.waitFor("sidebar rendered", `String(!!document.getElementById('sidebar'))`, "true")
		b.evaluate(`window.shellBounds=()=>JSON.stringify(['body','#sidebar','.drawer-content'].map(s=>{const r=document.querySelector(s).getBoundingClientRect();return [r.x,r.width]}));window.beforeModal=shellBounds();const d=document.createElement('dialog');d.id='gutter-test';d.className='modal';d.innerHTML='<div class="modal-box">Test</div>';document.body.appendChild(d);d.showModal();'ok'`)
		b.waitFor("modal does not reserve root gutters", `getComputedStyle(document.documentElement).scrollbarGutter`, "auto")
		b.waitFor("opening modal preserves shell bounds", `String(shellBounds()===beforeModal)`, "true")
		b.evaluate(`document.getElementById('gutter-test').close();'ok'`)
		b.waitFor("closing modal preserves shell bounds", `String(shellBounds()===beforeModal)`, "true")
	})
}
