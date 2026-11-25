package pages

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_RemoteGeneratedImage(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/files/r-0123456789abcdef-abcdef" && r.URL.Query().Get("project_id") == "r-0123456789abcdef-project" {
			if r.URL.Query().Get("metadata") == "1" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"file_name":"image.svg","media_type":"image/svg+xml"}`)
				return
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="red"/></svg>`)
			return
		}
		if r.URL.Path != "/viewers" {
			http.NotFound(w, r)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, `<div id="task-detail-content" data-project-id="r-0123456789abcdef-project"><div id="response" class="chat-markdown"><img id="initial" src="/files/abcdef?project_id=project"><a id="artifact" href="/files/abcdef?project_id=project">Image</a><a id="external" href="https://example.com/files/abcdef?project_id=project">External</a></div></div>`)
			return err
		})
		_ = layout.Base("Remote image", []models.Project{{ID: "p", Name: "P"}}, "p").Render(templ.WithChildren(r.Context(), child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/viewers", "remote-image", func(b *composerFocusCDP) {
		b.waitFor("refreshed thread image", `String(!!document.querySelector('#initial')?.naturalWidth)`, "true")
		b.waitFor("generated link preview", `String(!!document.querySelector('#artifact img')?.naturalWidth)`, "true")
		b.click("#artifact")
		b.waitFor("remote image gallery", `String(document.getElementById('image-gallery').open && !!document.querySelector('[data-gallery-image]')?.naturalWidth)`, "true")
		b.waitFor("remote download", `String(document.querySelector('[data-gallery-download]').href.includes('/files/r-0123456789abcdef-abcdef') && document.querySelector('[data-gallery-download]').href.includes('project_id=r-0123456789abcdef-project'))`, "true")
		b.click("[data-gallery-close]")
		b.waitFor("external unchanged", `document.querySelector('#external').getAttribute('href')`, "https://example.com/files/abcdef?project_id=project")
		b.evaluate(`window.originalMessage='![image](/files/abcdef?project_id=project)';var target=document.getElementById('response');var fragment=document.createDocumentFragment();var image=document.createElement('img');image.id='live';image.src='/files/abcdef?project_id=project';fragment.append(image);window.OVFileViewer.prepareStream(fragment,target);window.OVFileViewer.commitStream(fragment,target);'ok'`)
		b.waitFor("live image", `String(!!document.querySelector('#live')?.naturalWidth)`, "true")
		b.waitFor("source unchanged", `window.originalMessage`, "![image](/files/abcdef?project_id=project)")
	})
}
