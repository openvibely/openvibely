package pages

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_DesktopDownload(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var saves atomic.Int32
	var outcome atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/files/abc123" {
			if r.Method == http.MethodPost && r.Header.Get("X-OpenVibely-Save") == "1" && r.URL.Query().Get("project_id") == "project" {
				saves.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch outcome.Load() {
				case 1:
					io.WriteString(w, `{"saved":true}`)
				case 2:
					w.WriteHeader(http.StatusInternalServerError)
					io.WriteString(w, `{"message":"Could not save file"}`)
				default:
					io.WriteString(w, `{"saved":false,"canceled":true}`)
				}
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="report.docx"`)
			io.WriteString(w, "original")
			return
		}
		if r.URL.Path != "/test" {
			w.WriteHeader(204)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, `<a id="save-file" href="/files/abc123?download=1&project_id=project" download="report.docx">Download</a>`)
			return err
		})
		_ = layout.Base("Downloads", nil, "").Render(templ.WithChildren(layout.WithDesktopMode(r.Context(), true), child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/test", "desktop-download", func(b *composerFocusCDP) {
		b.waitFor("desktop viewer ready", `String(document.documentElement.dataset.runtime==='desktop' && !!window.OVFileViewer)`, "true")
		b.click("#save-file")
		b.waitFor("save request finished", `String(!document.querySelector('#save-file').hasAttribute('aria-busy'))`, "true")
		if saves.Load() != 1 {
			t.Fatalf("save requests: %d", saves.Load())
		}
		// A canceled native dialog must leave both the page and link usable.
		b.click("#save-file")
		b.waitFor("second save finished", `String(!document.querySelector('#save-file').hasAttribute('aria-busy'))`, "true")
		if saves.Load() != 2 {
			t.Fatalf("retry after cancellation: %d", saves.Load())
		}
		// Native modal dialogs live above ordinary page stacking contexts.
		b.evaluate(`const gallery=document.getElementById('image-gallery');gallery.showModal();gallery.append(document.querySelector('#save-file'));'opened'`)
		for _, result := range []int32{1, 2} {
			outcome.Store(result)
			b.evaluate(`document.querySelectorAll('.toast-notification').forEach(toast=>toast.remove());'cleared'`)
			b.click("#save-file")
			b.waitFor("download toast inside gallery", `String(!!document.querySelector('#image-gallery #toast-container .toast-notification'))`, "true")
			b.waitFor("download toast above gallery", `(()=>{const toast=document.querySelector('.toast-notification'),r=toast.getBoundingClientRect();return String(toast.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)))})()`, "true")
			b.waitFor("toast save finished", `String(!document.querySelector('#save-file').hasAttribute('aria-busy'))`, "true")
		}
		b.evaluate(`document.body.append(document.querySelector('#save-file'));document.getElementById('image-gallery').close();'closed'`)
		b.waitFor("toasts return to page", `String(document.querySelector('#toast-container').parentElement===document.body)`, "true")
		b.evaluate(`document.documentElement.dataset.runtime='web';document.querySelector('#save-file').addEventListener('click',e=>{window.webDownloadIntercepted=e.defaultPrevented;e.preventDefault()});document.querySelector('#save-file').click();'clicked'`)
		if got := b.evaluate(`String(window.webDownloadIntercepted)`); got != "false" {
			t.Fatal("desktop handler intercepted browser download")
		}
		if saves.Load() != 4 {
			t.Fatal("web download used native saver")
		}
	})
}
