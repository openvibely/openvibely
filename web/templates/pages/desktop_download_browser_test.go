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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/files/abc123" {
			if r.Method == http.MethodPost && r.Header.Get("X-OpenVibely-Save") == "1" && r.URL.Query().Get("project_id") == "project" {
				saves.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"saved":false,"canceled":true}`)
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
		b.evaluate(`document.documentElement.dataset.runtime='web';document.querySelector('#save-file').addEventListener('click',e=>{window.webDownloadIntercepted=e.defaultPrevented;e.preventDefault()});document.querySelector('#save-file').click();'clicked'`)
		if got := b.evaluate(`String(window.webDownloadIntercepted)`); got != "false" {
			t.Fatal("desktop handler intercepted browser download")
		}
		if saves.Load() != 2 {
			t.Fatal("web download used native saver")
		}
	})
}
