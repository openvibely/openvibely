package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_DesktopWindowControls(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, platform := range []string{"windows", "linux"} {
		t.Run(platform, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprintf(w, `<!doctype html><html><head><style>body{margin:0;--bc:80%% .01 250}#desktop-project-titlebar{display:flex;height:46px;justify-content:flex-end}</style></head><body><header id="desktop-project-titlebar" data-platform="%s">`, platform)
				if err := layout.DesktopWindowControls(platform).Render(context.Background(), w); err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, `</header><dialog id="dialog">Modal</dialog><script>
     window.calls=[];window.maximized=false;window.fullscreen=false;window.handlers={};window.sent={};
     window.testRuntime={Window:{Close:()=>window.calls.push('Close'),Minimise:()=>window.calls.push('Minimise'),ToggleMaximise:()=>window.calls.push('ToggleMaximise'),UnFullscreen:()=>window.calls.push('UnFullscreen'),IsMaximised:async()=>window.maximized,IsFullscreen:async()=>window.fullscreen},Events:{On:(name,fn)=>window.handlers[name]=fn,Emit:(name,data)=>window.sent[name]=data}};
     window.openVibelyInstallWindowControls(window.testRuntime);
    </script></body></html>`)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "window-controls", func(browser *composerFocusCDP) {
				browser.waitFor("caption initialized", `document.querySelector('[data-maximize-icon]').parentElement.title`, "Maximize")
				browser.evaluate(`document.querySelector('[data-window-action="Minimise"]').click();document.querySelector('[data-window-action="ToggleMaximise"]').click();document.querySelector('[data-window-action="Close"] svg path').dispatchEvent(new MouseEvent('click',{bubbles:true}));'ok'`)
				browser.waitFor("caption clicks reach runtime", `window.calls.join(',')`, "Minimise,ToggleMaximise,Close")
				browser.evaluate(`window.maximized=true;window.handlers['common:WindowMaximise']();'ok'`)
				browser.waitFor("restore icon", `getComputedStyle(document.querySelector('[data-restore-icon]')).display`, "block")
				browser.waitFor("maximize hidden", `getComputedStyle(document.querySelector('[data-maximize-icon]')).display`, "none")
				browser.waitFor("restore label", `document.querySelector('[data-restore-icon]').parentElement.title`, "Restore")
				browser.evaluate(`window.fullscreen=true;window.handlers['common:WindowFullscreen']();'ok'`)
				browser.waitFor("fullscreen action", `document.querySelector('[data-restore-icon]').parentElement.getAttribute('data-window-action')`, "UnFullscreen")
				browser.evaluate(`document.querySelector('[data-window-action="UnFullscreen"]').click();'ok'`)
				browser.waitFor("fullscreen click reaches runtime", `window.calls.at(-1)`, "UnFullscreen")
				browser.evaluate(`window.fullscreen=false;window.maximized=false;window.handlers['common:WindowUnFullscreen']();'ok'`)
				browser.waitFor("restored action", `document.querySelector('[data-restore-icon]').parentElement.getAttribute('data-window-action')`, "ToggleMaximise")
				browser.evaluate(`window.handlers['common:WindowLostFocus']();'ok'`)
				browser.waitFor("inactive controls", `getComputedStyle(document.querySelector('[data-maximize-icon]')).opacity`, "0.5")
				browser.evaluate(`window.handlers['common:WindowFocus']();'ok'`)
				browser.waitFor("active controls", `getComputedStyle(document.querySelector('[data-maximize-icon]')).opacity`, "1")
				if platform == "windows" {
					browser.waitFor("native hit bounds", `String(window.sent['desktop:caption-bounds']?.width)`, "46")
					browser.evaluate(`document.getElementById('dialog').showModal();'ok'`)
					browser.waitFor("modal disables native hit target", `String(window.sent['desktop:caption-bounds'].width)`, "0")
					browser.evaluate(`document.getElementById('dialog').close();'ok'`)
					browser.waitFor("caption enabled after modal", `String(window.sent['desktop:caption-bounds'].width)`, "46")
					browser.evaluate(`window.handlers['desktop:caption-hover']({data:{hover:true,pressed:true}});'ok'`)
					browser.waitFor("native hover feedback", `String(document.querySelector('[data-restore-icon]').parentElement.hasAttribute('data-native-pressed'))`, "true")
				} else {
					browser.evaluate(`document.documentElement.setAttribute('data-theme','dark');'ok'`)
					browser.waitFor("native theme follows app", `String(window.sent['desktop:controls-theme'].dark)`, "true")
					browser.evaluate(`document.documentElement.setAttribute('data-theme','light');'ok'`)
					browser.waitFor("native light theme", `String(window.sent['desktop:controls-theme'].dark)`, "false")
					browser.evaluate(`window.handlers['desktop:linux-controls']({data:{left:110,right:0}});'ok'`)
					browser.waitFor("native controls replace fallback", `getComputedStyle(document.querySelector('.desktop-window-controls')).display`, "none")
					browser.waitFor("left controls reserve room", `getComputedStyle(document.documentElement).getPropertyValue('--native-controls-left')`, "110px")
					browser.evaluate(`window.handlers['desktop:linux-controls']({data:{left:0,right:110}});'ok'`)
					browser.waitFor("theme layout moves controls", `getComputedStyle(document.documentElement).getPropertyValue('--native-controls-right')`, "110px")
				}
			})
		})
	}
}
