package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserPerformance_LongPDFWindowMeasurements(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	captureBaseline := os.Getenv("OPENVIBELY_PDF_CAPTURE_BASELINE") == "1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/fixture/perf-") && strings.HasSuffix(r.URL.Path, ".pdf") {
			var pageCount int
			if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/fixture/perf-"), ".pdf"), "%d", &pageCount); err != nil || pageCount < 1 || pageCount > 2000 {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("view") == "1" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_ = components.FilePreview(fmt.Sprintf("perf-%d.pdf", pageCount), "pdf", "", r.URL.String()).Render(r.Context(), w)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = io.WriteString(w, fileViewerPDF(pageCount))
			return
		}
		if r.URL.Path != "/pdf-performance" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, `<main><a href="/fixture/perf-100.pdf" data-file-preview>100 pages</a><a href="/fixture/perf-1000.pdf" data-file-preview>1000 pages</a><a href="/fixture/perf-2000.pdf" data-file-preview>2000 pages</a></main>`)
			return err
		})
		_ = layout.Base("PDF performance", []models.Project{{ID: "p", Name: "P"}}, "p").Render(templ.WithChildren(r.Context(), child), w)
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/pdf-performance", "pdf-long-document", func(b *composerFocusCDP) {
		b.waitFor("PDF viewer API ready", `String(!!window.OVFileViewer&&!!window.OVFilesPDF)`, "true")
		for _, pageCount := range []int{100, 1000, 2000} {
			b.traceStream = ""
			b.call("Tracing.start", map[string]any{"categories": "devtools.timeline,blink.user_timing", "transferMode": "ReturnAsStream"}, nil)
			b.evaluate(fmt.Sprintf(`window.OVFileViewer.open('/fixture/perf-%d.pdf');'opened'`, pageCount))
			b.waitFor(fmt.Sprintf("%d-page PDF opens", pageCount), `document.querySelector('[data-pdf-page-number]')?.textContent||''`, fmt.Sprintf("1 / %d", pageCount))
			b.waitFor(fmt.Sprintf("%d-page PDF paints a visible canvas", pageCount), `String(!!document.querySelector('[data-pdf-viewer] canvas'))`, "true")
			initialSurfaces := b.evaluate(`String(document.querySelectorAll('[data-pdf-viewer] .pdf-page-surface').length)`)
			b.evaluate(`(()=>{const viewer=document.querySelector('[data-pdf-viewer]');viewer.scrollTop=viewer.scrollHeight;viewer.dispatchEvent(new Event('scroll'));const event=new Event('scroll');for(let i=0;i<80;i++)viewer.dispatchEvent(event);return 'scrolled'})()`)
			b.waitFor(fmt.Sprintf("%d-page PDF scrolls near its end", pageCount), fmt.Sprintf(`String(Number(document.querySelector('[data-pdf-page-input]').value)>=%d)`, pageCount-2), "true")
			b.waitFor(fmt.Sprintf("%d-page PDF paints near its end", pageCount), `String(!!document.querySelector('[data-pdf-viewer] canvas'))`, "true")

			tracePath := filepath.Join(t.TempDir(), fmt.Sprintf("pdf-%d-pages.trace.json", pageCount))
			if pageCount == 2000 {
				if requestedPath := os.Getenv("OPENVIBELY_PDF_TRACE_PATH"); requestedPath != "" {
					tracePath = requestedPath
					if err := os.MkdirAll(filepath.Dir(tracePath), 0o755); err != nil {
						t.Fatalf("create trace output directory: %v", err)
					}
				}
			}
			b.call("Tracing.end", map[string]any{}, nil)
			b.saveChromeTrace(tracePath)
			p95Millis, scrollSamples, err := pdfTraceScrollP95(tracePath)
			if err != nil {
				t.Fatalf("measure scroll dispatches in %s: %v", tracePath, err)
			}
			metrics := b.evaluate(`(()=>{const viewer=document.querySelector('[data-pdf-viewer]'),surfaces=Array.from(viewer.querySelectorAll('.pdf-page-surface')).map(node=>Number(node.dataset.pdfSlot));return JSON.stringify({pages:Number(document.querySelector('[data-pdf-page-input]').max),initialSurfaces:` + initialSurfaces + `,mountedSurfaces:surfaces.length,firstMountedPage:Math.min(...surfaces),lastMountedPage:Math.max(...surfaces),canvases:viewer.querySelectorAll('canvas').length})})()`)
			t.Logf("PDF fixture measurement: %s p95 scroll-handler scripting %.4f ms (%d traced scroll events); trace %s", metrics, p95Millis, scrollSamples, tracePath)
			var measured struct {
				Pages, InitialSurfaces, MountedSurfaces, FirstMountedPage, LastMountedPage, Canvases int
			}
			if err := json.Unmarshal([]byte(metrics), &measured); err != nil {
				t.Fatalf("decode PDF fixture measurement %s: %v", metrics, err)
			}
			if !captureBaseline {
				if measured.Pages != pageCount || measured.InitialSurfaces > 40 || measured.MountedSurfaces > 40 {
					t.Fatalf("%d-page PDF mounted unbounded page surfaces: %+v", pageCount, measured)
				}
				if measured.FirstMountedPage > pageCount-2 || measured.LastMountedPage < pageCount-1 || measured.Canvases == 0 || measured.Canvases > 4 {
					t.Fatalf("%d-page PDF window or canvas rendering is not near the visible final pages: %+v", pageCount, measured)
				}
			}
			if pageCount == 2000 && !captureBaseline {
				b.evaluate(`(()=>{const viewer=document.querySelector('[data-pdf-viewer]');viewer.scrollTop=viewer.scrollHeight*.5;viewer.dispatchEvent(new Event('scroll'));return 'middle'})()`)
				b.waitFor("long PDF page tracking follows a middle scroll", `String(Number(document.querySelector('[data-pdf-page-input]').value)>900&&Number(document.querySelector('[data-pdf-page-input]').value)<1100&&document.querySelectorAll('.pdf-page-surface').length<=40)`, "true")
				b.evaluate(`(()=>{const viewer=document.querySelector('[data-pdf-viewer]');viewer.scrollTop=viewer.scrollHeight*.9;viewer.dispatchEvent(new Event('scroll'));return 'forward'})()`)
				b.waitFor("long PDF page tracking follows a forward window crossing", `String(Number(document.querySelector('[data-pdf-page-input]').value)>1500&&Number(document.querySelector('[data-pdf-page-input]').value)<1950&&document.querySelectorAll('.pdf-page-surface').length<=40)`, "true")
				b.evaluate(`(()=>{const viewer=document.querySelector('[data-pdf-viewer]');viewer.scrollTop=0;viewer.dispatchEvent(new Event('scroll'));return 'top'})()`)
				b.waitFor("long PDF scrolls back to its first page", `document.querySelector('[data-pdf-page-input]').value`, "1")
				b.evaluate(`(()=>{const input=document.querySelector('[data-pdf-page-input]');input.value='1999';input.dispatchEvent(new Event('change',{bubbles:true}));return 'jump'})()`)
				b.waitFor("long PDF page input jumps to page 1999", `String(document.querySelector('[data-pdf-page-input]').value==='1999'&&!!document.querySelector('[data-pdf-slot="1999"]')&&document.querySelectorAll('.pdf-page-surface').length<=40)`, "true")
				b.waitFor("long PDF page jump aligns the selected surface", `(()=>{const viewer=document.querySelector('[data-pdf-viewer]'),surface=viewer.querySelector('[data-pdf-slot="1999"]'),offset=surface.getBoundingClientRect().top-viewer.getBoundingClientRect().top;return String(offset>=-10&&offset<=40)})()`, "true")
				b.click("[data-pdf-zoom=in]")
				b.waitFor("long PDF zoom keeps rendering", `String(!!document.querySelector('[data-pdf-viewer] canvas')&&document.querySelectorAll('.pdf-page-surface').length<=40)`, "true")
				b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 900, "height": 700, "deviceScaleFactor": 1, "mobile": false}, nil)
				b.waitFor("long PDF resize changes viewer width", `String(innerWidth===900&&document.querySelector('[data-pdf-viewer]').clientWidth<900)`, "true")
				b.waitFor("long PDF resize keeps rendering", `String(!!document.querySelector('[data-pdf-viewer] canvas'))`, "true")
				b.evaluate(`(()=>{const input=document.querySelector('[data-pdf-page-input]');input.value='1999';input.dispatchEvent(new Event('change',{bubbles:true}));return 'jump'})()`)
				b.waitFor("long PDF page input works after resize", `String(document.querySelector('[data-pdf-page-input]').value==='1999'&&!!document.querySelector('[data-pdf-slot="1999"]')&&document.querySelectorAll('.pdf-page-surface').length<=40)`, "true")
				b.waitFor("long PDF page jump remains aligned after resize", `(()=>{const viewer=document.querySelector('[data-pdf-viewer]'),surface=viewer.querySelector('[data-pdf-slot="1999"]'),offset=surface.getBoundingClientRect().top-viewer.getBoundingClientRect().top;return String(offset>=-10&&offset<=40)})()`, "true")
				b.click("[data-pdf-page=next]")
				b.waitFor("long PDF next page control reaches page 2000", `String(document.querySelector('[data-pdf-page-input]').value==='2000'&&!!document.querySelector('[data-pdf-slot="2000"]')&&document.querySelectorAll('.pdf-page-surface').length<=40)`, "true")
				b.evaluate(`(()=>{document.querySelector('[data-gallery-close]').click();return 'closed'})()`)
				b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),150))`)
				b.waitFor("closing a rendering PDF removes page surfaces", `String(!document.getElementById('image-gallery').open&&!document.querySelector('.pdf-page-surface'))`, "true")
			}
		}
	})
}

func pdfTraceScrollP95(path string) (float64, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	var trace struct {
		Events []struct {
			Name string  `json:"name"`
			TS   float64 `json:"ts"`
			Dur  float64 `json:"dur"`
			Args struct {
				Data struct {
					Type string `json:"type"`
				} `json:"data"`
			} `json:"args"`
		} `json:"traceEvents"`
	}
	if err := json.Unmarshal(data, &trace); err != nil {
		return 0, 0, err
	}
	var durations []float64
	for _, event := range trace.Events {
		if event.Name == "EventDispatch" && event.Args.Data.Type == "scroll" && event.Dur > 0 {
			durations = append(durations, event.Dur)
		}
	}
	if len(durations) < 80 {
		return 0, len(durations), fmt.Errorf("found only %d scroll events", len(durations))
	}
	// The benchmark dispatches 80 synchronous scroll events immediately before ending the trace.
	durations = durations[len(durations)-80:]
	sort.Float64s(durations)
	return durations[75] / 1000, len(durations), nil
}
