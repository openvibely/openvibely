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

func TestBrowserFunctional_PublishedDocumentSurvivesStreaming(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/files/abc123" {
			if r.URL.Query().Get("metadata") == "1" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"file_name":"report.md","media_type":"text/plain"}`)
				return
			}
			_ = components.FilePreview("report.md", "markdown", "# Report\n\n"+strings.Repeat("Details.\n\n", 100), r.URL.String()).Render(r.Context(), w)
			return
		}
		if r.URL.Path != "/test" {
			w.WriteHeader(204)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			if err := components.ChatAutoScrollScript().Render(ctx, w); err != nil {
				return err
			}
			_, err := io.WriteString(w, `<div class="chat-markdown" id="published-message"></div>`)
			return err
		})
		_ = layout.Base("Gallery", nil, "").Render(templ.WithChildren(r.Context(), child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/test", "audit-document-stream", func(b *composerFocusCDP) {
		b.waitFor("renderer", `typeof window.renderLiveChatContent`, "function")
		b.evaluateAwait(`(async()=>{window.source='[report.md](/files/abc123)';await renderLiveChatContent(document.querySelector('#published-message'),source);return 'ready'})()`)
		b.waitFor("published document link", `String(!!document.querySelector('#published-message [data-file-preview]'))`, "true")
		b.click("#published-message a")
		b.waitFor("document loaded", `String(document.getElementById('image-gallery').open&&!!document.querySelector('[data-file-markdown] h1'))`, "true")
		got := b.evaluateAwait(`(async()=>{
 const root=document.querySelector('#published-message'),dialog=document.getElementById('image-gallery'),link=root.querySelector('a'),content=dialog.querySelector('[data-result-content]'),documentNode=content.firstElementChild;
 content.scrollTop=100;const scroll=content.scrollTop;if(!scroll)throw Error('fixture must scroll');
 const paint=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
 for(const suffix of [' More text.',' More text.\n\nAnother paragraph.']){
  await renderLiveChatContent(root,source+suffix);await paint();
  if(!dialog.open||root.querySelector('a')!==link||content.firstElementChild!==documentNode||content.scrollTop!==scroll)throw Error('stream reset document preview');
 }
 await renderLiveChatContent(root,source+' Finished.',false,true);await paint();
 if(!dialog.open||content.firstElementChild!==documentNode||content.scrollTop!==scroll)throw Error('final render reset document preview');
 await renderLiveChatContent(root,'Link removed.');await paint();
 if(dialog.open)throw Error('removed link left preview open');
 return 'true';
})()`)
		if got != "true" {
			t.Fatal("published Markdown preview closed on next streamed text update")
		}
	})
}
