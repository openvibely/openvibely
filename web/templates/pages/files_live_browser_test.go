package pages

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_PublishedFileRefreshesTaskPanel(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "files", Name: "Files"}
	var published atomic.Bool
	var loads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/tasks/task/attachments" {
			loads.Add(1)
			var files []models.GeneratedFile
			if published.Load() {
				files = []models.GeneratedFile{{ID: "abc123", FileName: "report.pdf", MediaType: "application/pdf"}}
			}
			_ = components.TaskFilesList(nil, project.ID, nil, files).Render(r.Context(), w)
			return
		}
		if r.URL.Path != "/test" {
			w.WriteHeader(204)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			return components.AttachmentList(nil, project.ID, "task").Render(ctx, w)
		})
		_ = layout.Base("Files", []models.Project{project}, project.ID).Render(templ.WithChildren(layout.WithDesktopMode(r.Context(), true), child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/test", "files-live", func(b *composerFocusCDP) {
		b.waitFor("initial files load", `String(!!window.OVFileViewer && document.querySelector('[data-task-files]')?.classList.contains('htmx-settling')===false && document.querySelector('#attachment-list')?.textContent.includes('No files yet.'))`, "true")
		published.Store(true)
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_files_changed',task_id:'task',project_id:'files'}}));'sent'`)
		b.waitFor("published PDF appears without navigation", `String(document.querySelector('#attachment-list')?.textContent.includes('report.pdf'))`, "true")
		b.click("#attachment-list a")
		b.waitFor("preview opened", `String(document.getElementById('image-gallery').open)`, "true")
		b.evaluate(`window.previewContent=document.querySelector('[data-result-content]');window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_files_changed',task_id:'task',project_id:'files'}}));'sent'`)
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),300))`)
		if got := b.evaluate(`String(document.getElementById('image-gallery').open && document.querySelector('[data-result-content]')===window.previewContent)`); got != "true" {
			t.Fatal("file refresh closed or replaced preview")
		}
		b.click("[data-file-close]")
		before := loads.Load()
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_files_changed',task_id:'other',project_id:'files'}}));window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_files_changed',task_id:'task',project_id:'other'}}));'sent'`)
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),100))`)
		if loads.Load() != before {
			t.Fatal("unrelated task/project refreshed Files")
		}
	})
}
