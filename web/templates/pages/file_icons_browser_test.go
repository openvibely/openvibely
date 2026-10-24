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
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_AttachmentFileIcons(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(204)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			for _, name := range []string{"report.DOCX", "paper.pdf", "book.xlsx", "slides.pptx", "photo.png", "sound.mp3", "movie.mp4", "bundle.zip", "notes.txt"} {
				if err := components.ImageAttachmentRow(name, 1024, "application/octet-stream", "/download/"+name, "/remove/"+name, "#attachment-list").Render(ctx, w); err != nil {
					return err
				}
			}
			return components.ImageAttachmentRow("thumbnail.png", 1024, "image/png", "/thumbnail", "", "").Render(ctx, w)
		})
		returnErr := layout.Base("Icons", []models.Project{{ID: "icons", Name: "Icons"}}, "icons").Render(templ.WithChildren(r.Context(), child), w)
		if returnErr != nil {
			t.Error(returnErr)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "attachment-icons", func(b *composerFocusCDP) {
		b.waitFor("nine embedded SVG badges", `String(document.querySelectorAll('.attachment-file-icon > svg').length)`, "9")
		b.waitFor("file types mapped", `Array.from(document.querySelectorAll('[data-file-icon]')).map(e=>e.dataset.fileIcon).join(',')`, "word,pdf,spreadsheet,presentation,image,audio,video,archive,file")
		b.waitFor("image thumbnail retained", `String(document.querySelectorAll('.attachment-gallery-open > img').length)`, "1")
		b.waitFor("links and removal retained", `String(document.querySelector('a[href="/download/report.DOCX"]').hasAttribute('data-file-preview') && document.querySelector('button[hx-delete="/remove/report.DOCX"]').getAttribute('hx-target')==='#attachment-list')`, "true")
		for _, theme := range []string{"dark", "light"} {
			b.evaluate(`document.documentElement.setAttribute('data-theme','` + theme + `');'ok'`)
			b.waitFor("compact badges in "+theme, `String(Array.from(document.querySelectorAll('.attachment-file-icon')).every(e=>e.getBoundingClientRect().width===24 && e.getBoundingClientRect().height===24 && e.getAttribute('aria-hidden')==='true'))`, "true")
			b.waitFor("different document colors in "+theme, `String(getComputedStyle(document.querySelector('[data-file-icon="word"]')).color!==getComputedStyle(document.querySelector('[data-file-icon="pdf"]')).color)`, "true")
		}
	})
}
