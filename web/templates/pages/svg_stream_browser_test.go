package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_StreamingSVGPreview(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	base := renderTerminalBrowserComponent(t, layout.Base("Math", nil, ""))
	shared := renderTerminalBrowserComponent(t, components.ChatAutoScrollScript())
	fixture := strings.Replace(base, "</body>", shared+`<div id="chat-page-root"><div id="chat-messages"><div id="math-stream"></div></div></div></body>`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "streaming-svg", func(b *composerFocusCDP) {
		b.waitFor("renderer", `typeof window.renderLiveChatContent`, "function")
		b.evaluateAwait(`(async()=>{window.svgSource='<svg xmlns="http://www.w3.org/2000/svg" width="1000" height="1000"><rect width="1000" height="1000" fill="red"/></svg>';await renderLiveChatContent(document.getElementById('math-stream'),svgSource);return 'ready'})()`)
		b.waitFor("SVG decoded", `String(!!document.querySelector('.chat-svg-preview img')?.naturalWidth)`, "true")
		b.click(".chat-svg-preview [data-image-gallery-item]")
		b.waitFor("gallery decoded", `String(document.getElementById('image-gallery').open && !!document.querySelector('[data-gallery-image]')?.naturalWidth)`, "true")
		result := b.evaluateAwait(`(async()=>{
          const root=document.getElementById('math-stream'),figure=root.querySelector('.chat-svg-preview'),dialog=document.getElementById('image-gallery');
          window.OVFileImage.key('+');
          const image=dialog.querySelector('[data-gallery-image]'),transform=image.style.transform,url=image.src;
          const paint=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
          for(const suffix of ['\n\nMore','\n\nMore text.','\n\nMore text.\n\n'+ 'Paragraph. '.repeat(1000)+'\n\nEnd.']){
            await renderLiveChatContent(root,svgSource+suffix);await paint();
            if(!dialog.open)throw Error('stream closed SVG preview');
            if(root.querySelector('.chat-svg-preview')!==figure)throw Error('SVG element replaced');
            if(image.src!==url||image.style.transform!==transform)throw Error('image or zoom reset');
          }
          await renderLiveChatContent(root,svgSource+'\n\nFinished.',false,true);await paint();
          if(!dialog.open||root.querySelector('.chat-svg-preview')!==figure)throw Error('final render closed SVG preview');
          if(!(await fetch(url)).ok)throw Error('retained blob was revoked');
          document.getElementById('chat-page-root').remove();await paint();
          if(dialog.open)throw Error('removed conversation left gallery open');
          let revoked=false;try{await fetch(url)}catch{revoked=true}
          if(!revoked)throw Error('removed SVG leaked blob');
          return 'pass';
        })()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}
