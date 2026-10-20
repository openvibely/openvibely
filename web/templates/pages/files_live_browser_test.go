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
	var missedPublication atomic.Bool
	var loads atomic.Int32
	liveEvents := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/events/ws" {
			serveBrowserLiveFixture(w, r, liveEvents, func(text string) bool {
				if text == "disconnect-and-publish" {
					missedPublication.Store(true)
					return true
				}
				return false
			})
			return
		}

		if r.URL.Path == "/emit" {
			data, _ := io.ReadAll(r.Body)
			liveEvents <- string(data)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/tasks/task/attachments" {
			loads.Add(1)
			var files []models.GeneratedFile
			if published.Load() {
				files = []models.GeneratedFile{{ID: "abc123", FileName: "report.pdf", MediaType: "application/pdf"}}
			}
			if missedPublication.Load() {
				files = append(files, models.GeneratedFile{ID: "def456", FileName: "missed.pdf", MediaType: "application/pdf"})
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
		b.evaluate(`const execution=document.createElement('div');execution.dataset.executionPair='true';execution.dataset.execId='dependency-test';document.body.append(execution);fetch('/emit',{method:'POST',body:JSON.stringify({type:'skill_dependency_progress',dependency_name:'paperworks',exec_id:'dependency-test',phase:'downloading',message:'paperworks: downloading'})});'sent'`)
		b.waitFor("dependency setup progress", `String(document.querySelector('[data-skill-dependency-status]')?.textContent.includes('paperworks: downloading'))`, "true")
		b.evaluate(`fetch('/emit',{method:'POST',body:JSON.stringify({type:'skill_dependency_progress',dependency_name:'paperworks',exec_id:'other',phase:'failed',message:'other: failed'})});'sent'`)
		b.waitFor("unrelated dependency ignored", `String(document.querySelector('[data-skill-dependency-status]')?.textContent.includes('paperworks: downloading'))`, "true")
		b.evaluate(`fetch('/emit',{method:'POST',body:JSON.stringify({type:'skill_dependency_progress',dependency_name:'paperworks',exec_id:'dependency-test',phase:'ready',message:'paperworks: ready'})});'sent'`)
		b.waitFor("dependency progress cleared", `String(!document.querySelector('[data-skill-dependency-status]'))`, "true")
		published.Store(true)
		b.evaluate(`fetch('/emit',{method:'POST',body:JSON.stringify({type:'task_files_changed',task_id:'task',project_id:'files'})});'sent'`)
		b.waitFor("published PDF appears without navigation", `String(document.querySelector('#attachment-list')?.textContent.includes('report.pdf'))`, "true")
		b.click("#attachment-list a")
		b.waitFor("preview opened", `String(document.getElementById('image-gallery').open)`, "true")
		b.evaluate(`window.previewContent=document.querySelector('[data-result-content]');fetch('/emit',{method:'POST',body:JSON.stringify({type:'task_files_changed',task_id:'task',project_id:'files'})});'sent'`)
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),300))`)
		if got := b.evaluate(`String(document.getElementById('image-gallery').open && document.querySelector('[data-result-content]')===window.previewContent)`); got != "true" {
			t.Fatal("file refresh closed or replaced preview")
		}
		b.evaluate(`window.reconnected=false;window.addEventListener('sse-live-connected',event=>{if(event.detail?.reconnected)window.reconnected=true});fetch('/emit',{method:'POST',body:'disconnect-and-publish'});'sent'`)
		b.waitFor("real stream reconnect", `String(window.reconnected)`, "true")
		b.waitFor("missed publication appears after reconnect", `String(document.querySelector('#attachment-list')?.textContent.includes('missed.pdf'))`, "true")
		if got := b.evaluate(`String(document.getElementById('image-gallery').open && document.querySelector('[data-result-content]')===window.previewContent)`); got != "true" {
			t.Fatal("reconnect closed or replaced preview")
		}
		b.click("[data-file-close]")
		before := loads.Load()
		b.evaluate(`fetch('/emit',{method:'POST',body:JSON.stringify({type:'task_files_changed',task_id:'other',project_id:'files'})});fetch('/emit',{method:'POST',body:JSON.stringify({type:'task_files_changed',task_id:'task',project_id:'other'})});'sent'`)
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),100))`)
		if loads.Load() != before {
			t.Fatal("unrelated task/project refreshed Files")
		}
	})
}
