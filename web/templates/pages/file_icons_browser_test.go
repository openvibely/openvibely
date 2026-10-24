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

func TestBrowserFunctional_AttachmentGrid(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/fragment" {
			w.WriteHeader(204)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			return components.TaskFilesList([]models.Attachment{
				{ID: "one", FileName: "photo.png", MediaType: "image/png"},
				{ID: "two", FileName: "report.docx"},
				{ID: "three", FileName: "paper.pdf"},
				{ID: "four", FileName: "notes.txt"},
			}, "grid", nil, nil).Render(ctx, w)
		})
		if r.URL.Path == "/fragment" {
			if err := child.Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if err := layout.Base("Grid", []models.Project{{ID: "grid", Name: "Grid"}}, "grid").Render(templ.WithChildren(r.Context(), child), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "attachment-grid", func(b *composerFocusCDP) {
		// Simulate navigation in an already-open shell with no new grid CSS.
		b.waitFor("HTMX ready", `String(typeof htmx!=="undefined")`, "true")
		b.evaluate(`document.querySelectorAll('style').forEach(e=>{if(e.textContent.includes('.attachment-grid {'))e.remove()});htmx.ajax('GET','/fragment',{target:'#attachment-list',swap:'outerHTML'});'refresh'`)
		b.waitFor("fragment supplies its own styles", `getComputedStyle(document.querySelector('#attachment-list')).display`, "grid")
		b.waitFor("four tiles", `String(document.querySelectorAll('.attachment-grid-tile').length)`, "4")
		b.waitFor("three columns and square tiles", `String((()=>{const a=[...document.querySelectorAll('.attachment-grid-tile')].map(e=>e.getBoundingClientRect());return Math.abs(a[0].width-a[0].height)<1 && a[0].top===a[2].top && a[3].top>a[0].top})())`, "true")
		b.waitFor("image preview and document preview retained", `String(document.querySelector('.attachment-grid-open > img').parentElement.hasAttribute('data-image-gallery-item') && document.querySelectorAll('.attachment-grid-open[data-file-preview]').length===3)`, "true")
		b.waitFor("all tiles use pointer cursor", `String([...document.querySelectorAll(".attachment-grid-open, .attachment-grid-open img")].every(e=>getComputedStyle(e).cursor==="pointer"))`, "true")
		b.waitFor("extension labels", `[...document.querySelectorAll('.attachment-grid-extension')].map(e=>e.textContent).join(',')`, "DOCX,PDF,TXT")
		b.waitFor("remove routes retained", `String(document.querySelectorAll('.attachment-grid-remove[hx-target="#attachment-list"]').length===4)`, "true")
		b.waitFor("image fills card without padding", `String((()=>{const img=document.querySelector('.attachment-grid-open img'),a=img.parentElement,t=a.parentElement.getBoundingClientRect(),r=img.getBoundingClientRect();return getComputedStyle(a).padding==='0px' && Math.abs(r.width-(t.width-2))<1 && Math.abs(r.height-(t.height-2))<1})())`, "true")
		b.evaluate(`window.normalTileBorder=getComputedStyle(document.querySelector('.attachment-grid-tile')).borderTopColor;'saved'`)
		b.evaluate(`document.querySelector('.attachment-grid-open').focus();'ok'`)
		b.waitFor("interaction highlights border", `String(getComputedStyle(document.querySelector('.attachment-grid-tile')).borderTopColor!==window.normalTileBorder)`, "true")
		b.waitFor("keyboard exposes removal", `getComputedStyle(document.querySelector('.attachment-grid-remove')).opacity`, "1")
	})
}

func TestBrowserFunctional_TaskFilesIncludedOnProjectSwitch(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/project" {
			w.WriteHeader(204)
			return
		}
		project := r.URL.Query().Get("project_id")
		if project == "" {
			project = "a"
		}
		ctx := components.WithTaskFiles(r.Context(), components.TaskFilesSnapshot{TaskID: "task-" + project, ProjectID: project, Generated: []models.GeneratedFile{{ID: project, FileName: project + ".png", MediaType: "image/png"}}})
		child := components.AttachmentList(nil, project, "task-"+project)
		if r.URL.Path == "/project" {
			_ = child.Render(ctx, w)
			return
		}
		_ = layout.Base("Files", []models.Project{{ID: project, Name: project}}, project).Render(templ.WithChildren(ctx, child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "files-project-switch", func(b *composerFocusCDP) {
		b.waitFor("HTMX ready", `String(typeof htmx!=="undefined")`, "true")
		b.evaluate(`window.fileLoads=0;document.body.addEventListener('htmx:beforeRequest',e=>{if(e.detail.elt.matches('[data-task-files]'))window.fileLoads++});'ready'`)
		for _, project := range []string{"b", "a", "b", "a"} {
			b.evaluate(`htmx.ajax('GET','/project?project_id=` + project + `',{target:'#main-content',swap:'innerHTML'});'switch'`)
			b.waitFor("complete file grid for "+project, `document.querySelector('#attachment-list img')?.alt`, project+".png")
			b.evaluate(`window.projectImage=document.querySelector('#attachment-list img');'saved'`)
			b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),150))`)
			b.waitFor("image not replaced by second load", `String(document.querySelector('#attachment-list img')===window.projectImage && window.fileLoads===0)`, "true")
		}
	})
}
