package pages

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_PublishedImagesShareTaskGallery(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "files", Name: "Files"}
	files := []models.GeneratedFile{
		{ID: "abc123", FileName: "dog.svg", MediaType: "image/svg+xml"},
		{ID: "def456", FileName: "robot.svg", MediaType: "image/svg+xml"},
	}
	uploaded := []models.Attachment{{ID: "screenshot", FileName: "screenshot.png", MediaType: "image/png"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/files/") || strings.HasPrefix(r.URL.Path, "/attachments/") {
			if r.URL.Query().Get("metadata") == "1" {
				w.Header().Set("Content-Type", "application/json")
				name := "dog.svg"
				if strings.HasSuffix(r.URL.Path, "def456") {
					name = "robot.svg"
				}
				io.WriteString(w, `{"file_name":"`+name+`","media_type":"image/svg+xml"}`)
			} else {
				w.Header().Set("Content-Type", "image/svg+xml")
				io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="blue"/></svg>`)
			}
			return
		}
		if r.URL.Path != "/test" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			if err := components.ChatAutoScrollScript().Render(ctx, w); err != nil {
				return err
			}
			if err := components.TaskFilesList(uploaded, project.ID, nil, files).Render(ctx, w); err != nil {
				return err
			}
			_, err := io.WriteString(w, `<div class="chat-markdown" id="published-message"><a href="/files/abc123?project_id=files">dog.svg</a></div>`)
			return err
		})
		_ = layout.Base("Gallery", []models.Project{project}, project.ID).Render(templ.WithChildren(r.Context(), child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/test", "published-images", func(b *composerFocusCDP) {
		b.waitFor("plain published chat link enhanced", `String(!!document.querySelector('#published-message img')?.naturalWidth)`, "true")
		b.evaluateAwait(`(async()=>{window.publishedSource='[dog.svg](/files/abc123?project_id=files)';await renderLiveChatContent(document.querySelector('#published-message'),publishedSource);return 'ready'})()`)
		b.waitFor("streamed published image", `String(!!document.querySelector('#published-message img')?.naturalWidth)`, "true")
		b.click("#published-message [data-image-gallery-item]")
		b.waitFor("published gallery image", `String(!!document.querySelector('[data-gallery-image]')?.naturalWidth)`, "true")
		if got := b.evaluateAwait(`(async()=>{
          const root=document.querySelector('#published-message'),dialog=document.getElementById('image-gallery'),link=root.querySelector('a');
          window.OVFileImage.key('+');const image=dialog.querySelector('[data-gallery-image]'),transform=image.style.transform;
          for(const suffix of [' More',' More text.',' More text.\n\nAnother paragraph.']){
            await renderLiveChatContent(root,publishedSource+suffix);await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
            if(!dialog.open)throw Error('published preview closed during streaming');
            if(root.querySelector('a')!==link||dialog.querySelector('[data-gallery-image]')!==image||image.style.transform!==transform)throw Error('published preview state replaced');
          }
          await renderLiveChatContent(root,publishedSource+' Finished.',false,true);
          await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
          if(!dialog.open)throw Error('final render closed preview');
          root.remove();await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
          if(dialog.open)throw Error('removed response left preview open');
          return 'pass';
        })()`); got != "pass" {
			t.Fatal(got)
		}
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),200))`)
		if got := b.evaluate(`String(Array.from(document.querySelectorAll('#attachment-list .attachment-gallery-open')).every(link=>link.querySelectorAll('img').length===1))`); got != "true" {
			t.Fatal("Files rows have duplicate thumbnails")
		}
		for _, entry := range []struct{ selector, count string }{
			{`#attachment-list a[href^="/files/abc123"]`, "2 / 3"},
			{`#attachment-list a[href^="/files/def456"]`, "3 / 3"},
			{`#attachment-list a[href^="/attachments/"]`, "1 / 3"},
		} {
			b.click(entry.selector)
			b.waitFor("full Files gallery from each entry", `document.querySelector('[data-gallery-count]').textContent`, entry.count)
			if entry.count == "2 / 3" {
				b.click("[data-gallery-next]")
				b.waitFor("next published SVG", `document.querySelector('[data-gallery-name]').textContent`, "robot.svg")
				b.click("[data-gallery-prev]")
				b.click("[data-gallery-prev]")
				b.waitFor("previous uploaded screenshot", `document.querySelector('[data-gallery-name]').textContent`, "screenshot.png")
			}
			b.click("[data-gallery-close]")
		}
		b.evaluate(`const list=document.querySelector('#attachment-list');list.outerHTML=list.outerHTML;'refreshed'`)
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),200))`)
		b.click(`#attachment-list a[href^="/files/abc123"]`)
		b.waitFor("restored Files list retains gallery", `document.querySelector('[data-gallery-count]').textContent`, "2 / 3")
	})
}
