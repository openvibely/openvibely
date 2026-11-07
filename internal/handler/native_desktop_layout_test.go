package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// The same real desktop markup and geometry assertions run in WebKitGTK,
// WebView2, and WKWebView through the existing native regression host.
func TestNativeDesktopTaskListAlignment(t *testing.T) {
	if testing.Short() || os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires OPENVIBELY_RUN_NATIVE_UI=1 and a graphical session")
	}
	host := buildNativeChatHost(t)
	t.Setenv("OPENVIBELY_NATIVE_TEST_HOST", host)
	testDesktopTaskListAlignment(t, host, true)
}

func TestDesktopTaskListAlignmentInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	testDesktopTaskListAlignment(t, chrome, false)
}

func testDesktopTaskListAlignment(t *testing.T, host string, native bool) {
	for _, width := range []int{500, 1280} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			results := make(chan string, 1)
			var page bytes.Buffer
			ctx := layout.WithNativeWindow(layout.WithDesktopMode(context.Background(), true), native)
			ctx = layout.WithUIPreferences(ctx, layout.UIPreferences{PinnedProjectIDs: []string{"alpha", "beta", "gamma"}})
			projects := []models.Project{{ID: "alpha", Name: "Alpha"}, {ID: "beta", Name: "Beta"}, {ID: "gamma", Name: "Gamma"}}
			if err := layout.Base("Desktop alignment", projects, "alpha").Render(ctx, &page); err != nil {
				t.Fatal(err)
			}
			html := strings.Replace(page.String(), "</body>", desktopAlignmentScenario+"</body>", 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				switch r.URL.Path {
				case "/":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, html)
				case "/wails/runtime.js":
					// Native window decorations are exercised by the caption tests. Here we
					// supply their geometry contract and test the actual webview layout.
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprint(w, `export const Window = {};`)
				case "/browser-result":
					select {
					case results <- r.URL.Query().Get("status"):
					default:
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					// Includes the host's bridge registration and background shell requests.
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()
			if native {
				t.Setenv("OPENVIBELY_NATIVE_LAYOUT_WIDTH", fmt.Sprint(width))
			}
			fixtureURL := fmt.Sprintf("%s/?width=%d", server.URL, width)
			args := []string{fixtureURL}
			if !native {
				args = []string{"--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", fmt.Sprintf("--window-size=%d,800", width), fixtureURL}
			}
			cmd := exec.Command(host, args...)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := startHandlerBrowserProcess(t, cmd); err != nil {
				t.Fatal(err)
			}
			outcome := "native desktop layout timed out"
			select {
			case outcome = <-results:
			case <-time.After(90 * time.Second):
			}
			stopHandlerBrowserProcess(t, cmd)
			if outcome != "pass" {
				t.Fatalf("%s (%s, native=%v, width=%d)\n%s", outcome, runtime.GOOS, native, width, output.String())
			}
		})
	}
}

const desktopAlignmentScenario = `<script>
(async()=>{
 const assert=(value,message)=>{if(!value)throw Error(message)};
 const frame=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
 if(document.readyState==='loading')await new Promise(resolve=>document.addEventListener('DOMContentLoaded',resolve,{once:true}));
 const root=document.documentElement;
 const bar=document.getElementById('desktop-project-titlebar');
 const toggle=document.getElementById('task-list-toggle');
 const panel=document.getElementById('task-list-panel');
 const platform=bar.dataset.platform;
 const narrow=Number(new URLSearchParams(location.search).get('width'))<640;
 assert(narrow ? innerWidth<=640 : innerWidth>=1024,'host did not use requested viewport: '+innerWidth);
 assert(root.dataset.openvibelyRuntime==='desktop','fixture must use desktop runtime');
 assert(['linux','windows','darwin'].includes(platform),'unexpected desktop platform '+platform);
 assert(root.hasAttribute('data-shell-frame'),'desktop rail missing');
 // Include fallback controls, normal GTK right controls, and single/grouped
 // left controls using the geometry contract delivered by the GTK bridge.
 const insets=platform==='linux' ? [null,0,48,110] : [null];
 for(const inset of insets){
  if(inset!==null){
   root.setAttribute('data-native-linux-controls','');
   root.style.setProperty('--native-controls-left',inset+'px');
   root.style.setProperty('--native-controls-right',inset ? '0px' : '110px');
  }
  for(const layout of ['floating','unified'])for(const tabs of ['detached','attached']){
   root.setAttribute('data-layout',layout);
   root.setAttribute('data-tab-style',tabs);
   if(root.classList.contains('task-list-collapsed'))toggle.click();
   for(const collapsed of [false,true,false]){
    if(root.classList.contains('task-list-collapsed')!==collapsed)toggle.click();
    await frame();
    const projectTabs=document.querySelectorAll('#desktop-project-tabs [data-project-tab]');
    assert(projectTabs.length>1,'keyboard test requires multiple project tabs');
    const tab=projectTabs[0];
    tab.focus({preventScroll:true});
    tab.dispatchEvent(new KeyboardEvent('keydown',{key:'End',bubbles:true,cancelable:true}));
    await frame();
    const label=JSON.stringify({platform,inset,layout,tabs,collapsed,width:innerWidth,rootScroll:document.scrollingElement.scrollLeft,bodyScroll:document.body.scrollLeft});
    assert(document.activeElement===projectTabs[projectTabs.length-1],'End did not focus the last project tab '+label);
    assert(window.scrollX===0 && document.body.scrollLeft===0,'desktop shell scrolled horizontally '+label);
    const rect=toggle.getBoundingClientRect();
    const rail=document.getElementById('sidebar').getBoundingClientRect();
    const header=bar.getBoundingClientRect();
    assert(rect.width>0 && rect.height>0,'toggle hidden '+label);
    assert(rect.top>=header.top && rect.bottom<=header.bottom,'toggle outside title bar '+label);
    assert(toggle.getAttribute('aria-expanded')===String(!collapsed),'toggle state '+label);
    assert((panel.getClientRects().length===0)===collapsed,'task list visibility '+label);
    if(platform==='darwin'){
     assert(Math.abs(rect.left-80)<1,'macOS traffic-light reservation changed '+rect.left+' '+label);
    }else if(inset>0){
     assert(rect.left>=inset+8,'toggle overlaps left native controls '+rect.left+' '+label);
    }else{
     assert(Math.abs(rect.left+rect.width/2-(rail.left+rail.width/2))<1,'toggle not centered on rail '+JSON.stringify({toggle:rect.toJSON(),rail:rail.toJSON()})+' '+label);
    }
    const hit=document.elementFromPoint(rect.left+rect.width/2,rect.top+rect.height/2);
    assert(hit && toggle.contains(hit),'toggle covered by another element '+label);
    if(!collapsed){
     const actions=document.querySelector('#task-list-header .task-list-actions').getBoundingClientRect();
     const list=panel.getBoundingClientRect();
     assert(actions.left>=list.left && Math.abs(actions.right-list.right)<1,'header actions moved outside task list '+label);
    }
   }
  }
 }
 await fetch('/browser-result?status=pass');
})().catch(error=>fetch('/browser-result?status='+encodeURIComponent(String(error)+'\n'+(error.stack||''))));
</script>`
