package pages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/filecontent"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func fileViewerPDF(pageCounts ...int) string {
	stream := "BT /F1 18 Tf 72 120 Td (Offline PDF) Tj ET"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 600] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	if len(pageCounts) > 0 && pageCounts[0] == 2 {
		objects[1] = "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 >>"
		objects = append(objects, objects[2])
	}
	pdf := "%PDF-1.4\n"
	offsets := []int{}
	for i, object := range objects {
		offsets = append(offsets, len(pdf))
		pdf += fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := len(pdf)
	pdf += fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		pdf += fmt.Sprintf("%010d 00000 n \n", offset)
	}
	pdf += fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return pdf
}

func TestBrowserFunctional_FileViewers(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/fixture-list" {
			_ = components.FilesPanel([]models.FileListItem{{Name: "robot.svg", URL: "/fixture/robot.svg"}, {Name: "document.docx", URL: "/fixture/document.docx"}}).Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/files/abcdef" {
			if r.URL.Query().Get("metadata") == "1" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"file_name":"generated.html","media_type":"text/html","display":"canvas"}`)
			} else if r.URL.Query().Get("view") == "1" {
				_ = components.FilePreview("generated.html", "html", "<button>Generated</button>", r.URL.String()).Render(r.Context(), w)
			} else {
				w.Header().Set("Content-Type", "text/html")
				io.WriteString(w, "<button>Generated</button>")
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/fixture/") {
			name := strings.TrimPrefix(r.URL.Path, "/fixture/")
			kind := components.FilePreviewKind(name)
			text := map[string]string{"data.json": `{"amount":12345678901234567890,"nested":{"ok":true}}`, "table.csv": "Name,Value\nAlpha,42\n,\nBeta,84", "wide.csv": strings.Repeat("value,", 100) + "outside preview", "demo.html": "<button id='demo' onclick='this.textContent=\"Clicked\"'>Click</button>", "math.md": "Math: $x^2$"}[name]
			if r.URL.Query().Get("view") == "1" {
				w.Header().Set("Content-Type", "text/html")
				if name == "document.docx" {
					_ = components.FileDocumentPreview(name, filecontent.DocumentPreview{}, "/fixture/document.docx?download=1").Render(r.Context(), w)
				} else {
					_ = components.FilePreview(name, kind, text, r.URL.String()).Render(r.Context(), w)
				}
				return
			}
			if name == "sample.pdf" || name == "mixed.pdf" {
				w.Header().Set("Content-Type", "application/pdf")
				if name == "mixed.pdf" {
					io.WriteString(w, fileViewerPDF(2))
				} else {
					io.WriteString(w, fileViewerPDF())
				}
				return
			}
			if name == "robot.svg" {
				w.Header().Set("Content-Type", "image/svg+xml")
				io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="200"><rect width="200" height="200" fill="blue"/></svg>`)
				return
			}
			if name == "demo.html" {
				w.Header().Set("Content-Type", "text/html")
			}
			io.WriteString(w, text)
			return
		}
		if r.URL.Path != "/viewers" {
			w.WriteHeader(204)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, `<div id="mixed-files" data-image-gallery-group><a id="mixed-image" href="/fixture/robot.svg" data-image-gallery-item data-image-name="robot.svg">Robot</a><a id="mixed-pdf" href="/fixture/mixed.pdf" data-file-preview>mixed.pdf</a><a id="mixed-docx" href="/fixture/document.docx" data-file-preview>document.docx</a></div><a id="generated" href="/files/abcdef">Generated artifact</a> <a id="json" data-file-preview href="/fixture/data.json">JSON</a> <a id="sheet" data-file-preview href="/fixture/table.csv">Sheet</a> <a id="wide-sheet" data-file-preview href="/fixture/wide.csv">Wide sheet</a> <a id="pdf" data-file-preview href="/fixture/sample.pdf">PDF</a> <a id="html" data-file-preview href="/fixture/demo.html">HTML</a> <a id="math" data-file-preview href="/fixture/math.md">Math</a>`)
			return err
		})
		_ = layout.Base("Viewers", []models.Project{{ID: "p", Name: "P"}}, "p").Render(templ.WithChildren(r.Context(), child), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/viewers", "file-viewers", func(b *composerFocusCDP) {
		b.waitFor("viewer ready", `String(!!window.OVFileViewer)`, "true")

		b.click("#mixed-image")
		b.waitFor("single shared file window", `String(document.querySelectorAll('dialog[open]').length===1 && document.querySelector('[data-gallery-count]').textContent==='1 / 3' && !!document.querySelector('[data-gallery-image]').naturalWidth)`, "true")
		b.click("[data-gallery-plus]")
		b.waitFor("image-specific zoom", `document.querySelector('[data-gallery-fit]').getAttribute('aria-label')`, "Fit image (150%)")
		b.click("[data-gallery-next]")
		b.waitFor("PDF replaces image in same window", `String(document.getElementById('image-gallery').open && document.querySelector('[data-gallery-stage]').hidden && document.querySelector('[data-image-controls]').hidden && !!document.querySelector('[data-pdf-viewer] canvas') && document.querySelector('[data-gallery-count]').textContent==='2 / 3')`, "true")
		b.click("[data-pdf-page=next]")
		b.waitFor("PDF page navigation does not change file", `document.querySelector('[data-pdf-page-number]').textContent+':'+document.querySelector('[data-gallery-count]').textContent`, "2 / 2:2 / 3")
		b.click("[data-pdf-zoom=in]")
		b.waitFor("PDF has its own download", `String(document.querySelector('[data-gallery-download]').href.includes('mixed.pdf') && document.querySelector('[data-gallery-download]').href.includes('download=1'))`, "true")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowRight"}, nil)
		b.waitFor("right arrow selects DOCX not PDF page", `String(!!document.querySelector('.result-document') && !document.querySelector('[data-pdf-controls]') && document.querySelector('[data-image-controls]').hidden && document.querySelector('[data-gallery-count]').textContent==='3 / 3' && document.querySelector('[data-gallery-next]').disabled)`, "true")
		b.waitFor("DOCX download changes", `String(document.querySelector('[data-gallery-download]').href.includes('document.docx'))`, "true")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "ArrowLeft"}, nil)
		b.waitFor("PDF renderer resets on return", `document.querySelector('[data-pdf-page-number]')?.textContent||''`, "1 / 2")
		b.click("[data-gallery-prev]")
		b.waitFor("image renderer resets on return", `String(!document.querySelector('[data-image-controls]').hidden && !document.querySelector('[data-pdf-viewer]') && document.querySelector('[data-gallery-fit]').getAttribute('aria-label')==='Fit image (100%)')`, "true")
		b.click("[data-gallery-close]")
		b.waitFor("mixed gallery restores opener focus", `String(document.activeElement.id==='mixed-image')`, "true")
		b.evaluate(`window.OVFileViewer.open('/fixture-list'); 'ok'`)
		b.waitFor("Files list in shared window", `String(!!document.querySelector('[data-files-list]'))`, "true")
		b.click("[data-files-list] [data-file-preview]")
		b.waitFor("Files list image uses image controls", `String(!document.querySelector('[data-image-controls]').hidden && !!document.querySelector('[data-gallery-image]').naturalWidth && document.querySelector('[data-gallery-count]').textContent==='1 / 2')`, "true")
		b.click("[data-gallery-next]")
		b.waitFor("Files list navigation survives replacing list", `String(!!document.querySelector('.result-document') && document.querySelector('[data-gallery-count]').textContent==='2 / 2')`, "true")
		b.click("[data-gallery-close]")
		b.evaluate(`window.OVFileViewer.open('/fixture/document.docx'); 'ok'`)
		b.waitFor("document body loaded", `String(!!document.querySelector('.result-document'))`, "true")
		b.waitFor("single document toolbar", `String(document.querySelector('[data-result-title]').textContent==='document.docx' && !!document.querySelector('[data-result-actions] a[href*="download=1"]') && !document.querySelector('[data-document-header]'))`, "true")
		b.waitFor("document fills modal", `(()=>{const dialog=document.getElementById('image-gallery'),shell=dialog.querySelector('.file-viewer-shell'),body=dialog.querySelector('[data-result-content]'),article=body.querySelector('.result-document');if(!article)return 'false';const d=dialog.getBoundingClientRect(),s=shell.getBoundingClientRect(),a=article.getBoundingClientRect();return String(Math.abs(d.left-s.left)<1 && Math.abs(d.width-s.width)<1 && Math.abs(d.bottom-s.bottom)<1 && Math.abs(a.left-d.left)<1 && Math.abs(a.right-d.right)<1 && getComputedStyle(body).padding==='0px' && getComputedStyle(shell).boxShadow==='none')})()`, "true")
		b.click("[data-file-close]")
		b.waitFor("toolbar cleared on close", `String(document.querySelector('[data-gallery-download]').hidden && !document.querySelector('[data-gallery-download]').hasAttribute('href'))`, "true")

		b.evaluate(`window.robotSVG = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 260" width="240" height="260" role="img" aria-label="Friendly robot"><g stroke="#243449" stroke-width="6" stroke-linecap="round" stroke-linejoin="round"><path d="M120 45V25"/><circle cx="120" cy="18" r="9" fill="#ffcc66"/><rect x="45" y="48" width="150" height="105" rx="24" fill="#8bd3ff"/><circle cx="88" cy="90" r="12" fill="#243449"/><circle cx="152" cy="90" r="12" fill="#243449"/><path d="M92 120Q120 144 148 120" fill="none"/><rect x="65" y="165" width="110" height="60" rx="15" fill="#8bd3ff"/><circle cx="120" cy="194" r="10" fill="#ffcc66"/><path d="M65 182L35 200M175 182L210 155" fill="none"/><path d="M90 225V245H75M150 225V245H165" fill="none"/></g></svg>'; window.svgChat = document.createElement('div'); svgChat.className='chat-markdown'; document.body.append(svgChat); svgChat.innerHTML=window.renderChatMarkdown(robotSVG); 'ok'`)
		b.waitFor("robot SVG decoded", `String(!!svgChat.querySelector('img')?.naturalWidth)`, "true")
		b.waitFor("SVG is isolated with source and download", `String(!svgChat.querySelector('svg') && svgChat.querySelector('details code').textContent.trim()===robotSVG && !!svgChat.querySelector('a[download]'))`, "true")
		b.click(".chat-svg-preview [data-image-gallery-item]")
		b.waitFor("SVG gallery", `String(document.getElementById('image-gallery').open && !!document.querySelector('[data-gallery-image]').naturalWidth)`, "true")
		b.click("[data-gallery-close]")
		b.evaluate("svgChat.innerHTML=window.renderChatMarkdown('```svg\\n'+robotSVG+'\\n```'); 'ok'")
		b.waitFor("fenced SVG decoded", `String(!!svgChat.querySelector('img')?.naturalWidth)`, "true")
		b.evaluate(`svgChat.innerHTML=window.renderChatMarkdown(robotSVG.slice(0,-6)); 'ok'`)
		b.waitFor("incomplete SVG remains source", `String(!svgChat.querySelector('img') && svgChat.textContent.includes('<svg'))`, "true")
		b.evaluate(`svgChat.innerHTML=window.renderChatMarkdown(robotSVG); 'ok'`)
		b.waitFor("completed streamed SVG decoded", `String(!!svgChat.querySelector('img')?.naturalWidth)`, "true")
		b.evaluate("svgChat.innerHTML=window.renderChatMarkdown('```html\\n'+robotSVG+'\\n```'); 'ok'")
		b.waitFor("HTML code example stays source", `String(!svgChat.querySelector('img') && !!svgChat.querySelector('code.language-html'))`, "true")
		b.evaluateAwait(`window.renderChatMarkdownAsync('text\n\n'.repeat(12000)+robotSVG,{}).then(fragment=>{svgChat.replaceChildren(fragment);return 'ok'})`)
		b.waitFor("worker-rendered SVG decoded", `String(!!svgChat.querySelector('img')?.naturalWidth)`, "true")
		b.evaluate(`svgChat.innerHTML=window.renderChatMarkdown('<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" onload="window.svgExecuted=true"><script>window.svgExecuted=true</script><foreignObject><div xmlns="http://www.w3.org/1999/xhtml">unsafe</div></foreignObject><use href="https://example.invalid/external.svg#x"/><rect width="10" height="10" fill="red"/></svg>'); 'ok'`)
		b.waitFor("untrusted SVG decoded safely", `String(!!svgChat.querySelector('img')?.naturalWidth && !window.svgExecuted && !svgChat.querySelector('script,svg,foreignObject'))`, "true")
		if got := b.evaluateAwait(`fetch(svgChat.querySelector('img').src).then(r=>r.text()).then(text=>String(!/onload|<script|foreignObject|https:\/\/example/.test(text)))`); got != "true" {
			t.Fatalf("unsafe SVG image document: %s", got)
		}
		b.evaluate(`svgChat.innerHTML=svgChat.innerHTML; 'restored'`)
		b.waitFor("restored SVG decodes once", `String(svgChat.querySelectorAll('.chat-svg-preview').length===1 && !!svgChat.querySelector('img')?.naturalWidth)`, "true")
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('settled'),100))`)
		if got := b.evaluate(`String(svgChat.querySelectorAll('.chat-svg-preview').length)`); got != "1" {
			t.Fatal("duplicate restored SVG: " + got)
		}
		b.evaluate(`svgChat.remove(); 'ok'`)

		b.waitFor("generated canvas", `document.querySelector('.file-html-canvas iframe')?.getAttribute('sandbox')||''`, "allow-scripts")
		b.evaluate(`const canvas=document.querySelector('.file-html-canvas');canvas.outerHTML=canvas.outerHTML;'restored'`)
		b.click(".file-html-canvas button")
		b.waitFor("expanded generated artifact", `String(document.querySelector('[data-file-name]')?.dataset.fileName==='generated.html')`, "true")
		b.click("[data-file-close]")
		b.click("#json")
		b.waitFor("exact JSON number", `String(document.querySelector('[data-json-tree]')?.textContent.includes('12345678901234567890'))`, "true")
		b.click("[data-file-close]")
		b.click("#sheet")
		b.waitFor("spreadsheet cells", `String(document.querySelector('[data-sheet-grid]')?.textContent.includes('Alpha'))`, "true")
		b.click("[data-sheet-grid] [data-row='1'][data-col='1']")
		b.waitFor("cell selection", `String(!!document.querySelector('[data-sheet-grid] [aria-selected=true]'))`, "true")
		b.click("[data-sheet-grid] [data-row='3'][data-col='1']")
		b.waitFor("original sparse cell address", `document.querySelector('[data-sheet-address]')?.textContent||''`, "B4")
		b.waitFor("original sparse cell value", `document.querySelector('[data-sheet-value]')?.textContent||''`, "84")
		b.click("[data-file-close]")
		b.click("#wide-sheet")
		b.waitFor("visible spreadsheet truncation notice", `String(Array.from(document.querySelectorAll('[data-sheet-status]')).some(node=>!node.hidden && node.textContent.includes('Preview limited:') && node.textContent.includes('100 of 101 columns') && node.textContent.includes('Download the original')))`, "true")
		b.waitFor("bounded spreadsheet columns", `String(document.querySelectorAll('[data-sheet-grid] [role=columnheader]').length)`, "100")
		b.click("[data-file-close]")
		b.click("#pdf")
		b.waitFor("previous sheet controls removed", `String(!document.querySelector('[data-result-title] [data-sheet-count]') && !document.querySelector('#image-gallery header [data-sheet-tabs]'))`, "true")
		b.waitFor("offline PDF painted", `String(!!document.querySelector('[data-pdf-viewer] canvas'))`, "true")
		b.waitFor("PDF page count", `document.querySelector('[data-pdf-page-number]')?.textContent||''`, "1 / 1")
		b.click("[data-pdf-zoom=in]")
		b.click("[data-file-close]")
		b.click("#html")
		b.waitFor("HTML sandbox", `document.querySelector('[data-file-viewer=html] iframe')?.getAttribute('sandbox')||''`, "allow-scripts")
		b.click("[data-file-html-mode=source]")
		b.waitFor("HTML source", `String(!document.querySelector('[data-file-source]').hidden && document.querySelector('[data-file-source]').textContent.includes('<button'))`, "true")
		b.click("[data-file-close]")
		b.click("#math")
		b.waitFor("KaTeX math", `String(!!document.querySelector('[data-file-markdown] .katex'))`, "true")
		b.click("[data-file-close]")
	})
}
