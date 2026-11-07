package pages

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAppScrollingUsesPanelHelper(t *testing.T) {
	// Vendor libraries and browser-test drivers are not application call sites.
	direct := regexp.MustCompile(`\.scrollIntoView(?:IfNeeded)?\s*\(`)
	for _, root := range []string{"..", "../../assets"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "file-vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".templ") && !strings.HasSuffix(path, ".js") {
				return nil
			}
			if strings.HasSuffix(path, ".min.js") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if direct.Match(content) {
				t.Errorf("%s: use openVibelyScrollIntoPanel so reveal actions cannot scroll the app shell", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrowserFunctional_PanelScrollIsolation(t *testing.T) {
	fixture := `<style>
html,body{margin:0;height:100%;overflow:hidden}
#shell{height:400px;overflow:hidden}header{height:54px}
#panel{height:400px;width:300px;overflow:auto;border:3px solid transparent}
#space{height:1200px;width:900px;position:relative}
#target{position:absolute;top:700px;left:600px;width:100px;height:80px}
</style><main id="reconnect-result"></main><div id="shell"><header>Tabs</header><div id="panel"><div id="space"><div id="target">Target</div></div></div></div>
<script>
try {
 const panel=document.getElementById('panel'),target=document.getElementById('target');
 const check=(ok,msg)=>{if(!ok)throw Error(msg)};
 const stable=()=>{check(document.querySelector('header').getBoundingClientRect().top===0,'title bar moved');check(!document.body.scrollTop&&!document.documentElement.scrollTop&&!document.getElementById('shell').scrollTop,'shell scrolled')};
 check(openVibelyScrollIntoPanel(target,{block:'center',inline:'center'}),'panel not discovered');
 const centerTop=Math.round(740-panel.clientHeight/2),centerLeft=Math.round(650-panel.clientWidth/2);
 check(panel.scrollTop===centerTop&&panel.scrollLeft===centerLeft,'centering must account for borders and scrollbars');stable();
 openVibelyScrollIntoPanel(target);check(panel.scrollTop===centerTop&&panel.scrollLeft===centerLeft,'nearest moved a visible target');stable();
 panel.scrollTop=0;panel.scrollLeft=0;openVibelyScrollIntoPanel(target);
 check(panel.scrollTop===780-panel.clientHeight&&panel.scrollLeft===700-panel.clientWidth,'nearest reveal failed');stable();
 target.style.height='600px';openVibelyScrollIntoPanel(target,{block:'center'});
 check(panel.scrollTop===700,'oversized target should start at panel top');stable();
 check(!openVibelyScrollIntoPanel(target,{container:document.body}),'body must not be scrollable');
 check(!openVibelyScrollIntoPanel(target,{container:document.querySelector('header')}),'unrelated container accepted');
 check(!openVibelyScrollIntoPanel(document.createElement('div')),'detached target accepted');
 check(!openVibelyScrollIntoPanel(null),'missing target accepted');stable();
 document.getElementById('reconnect-result').dataset.testResult='pass';
}catch(e){const r=document.getElementById('reconnect-result');r.dataset.testResult='fail';r.dataset.testError=e.message;}
</script>`
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_PanelScrollNestedBoundaries(t *testing.T) {
	fixture := `<style>
 html,body{margin:0;height:100%;overflow:hidden}
 .drawer{height:300px;overflow:auto}header{height:54px}
 #outer{height:300px;width:300px;overflow:auto}
 #inner{height:120px;overflow:auto;margin-top:400px}
 #target{height:40px;margin-top:500px}
 </style><main id="reconnect-result"></main><div class="drawer"><header>Tabs</header><div id="outer"><div id="inner"><div id="target">Target</div></div><div style="height:400px"></div></div></div>
 <script>
 (async()=>{const result=document.getElementById('reconnect-result');try{
 const outer=document.getElementById('outer'),inner=document.getElementById('inner'),target=document.getElementById('target');
 const assert=(ok,message)=>{if(!ok)throw Error(message)};
 const stable=()=>assert(document.querySelector('header').getBoundingClientRect().top===0&&!document.body.scrollTop&&!document.documentElement.scrollTop&&!document.querySelector('.drawer').scrollTop,'shell moved');
 openVibelyScrollIntoPanel(target,{container:inner});
 assert(inner.scrollTop>0&&outer.scrollTop===0,'explicit boundary ignored');stable();
 for(const behavior of ['instant','smooth']) {
 outer.scrollTo({top:0,behavior:'instant'});inner.scrollTo({top:0,behavior:'instant'});
 openVibelyScrollIntoPanel(target,{behavior});
 for(let i=0;i<100;i++) {const t=target.getBoundingClientRect(),o=outer.getBoundingClientRect();if(inner.scrollTop>0&&outer.scrollTop>0&&t.top>=o.top&&t.bottom<=o.bottom)break;await new Promise(r=>setTimeout(r,20));}
 const t=target.getBoundingClientRect(),o=outer.getBoundingClientRect();
 assert(inner.scrollTop>0&&outer.scrollTop>0,'both content panels must scroll: '+behavior+' '+inner.scrollTop+' '+outer.scrollTop);
 assert(t.top>=o.top&&t.bottom<=o.bottom,'target remains outside outer panel');stable();
 }
 result.dataset.testResult='pass';
 }catch(e){result.dataset.testResult='fail';result.dataset.testError=e.message;}})();
 </script>`
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><script src="/static/panel-scroll.js"></script></head><body>` + fixture + `</body></html>`))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "nested-panel-scroll", func(b *composerFocusCDP) {
		b.waitFor("nested scrolling result", `document.getElementById('reconnect-result')?.dataset.testResult || ''`, "pass")
	})
}

func TestBrowserFunctional_HTMXAnchorsStayInsidePanels(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_ = layout.Base("Anchor navigation", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(r.Context(), w)
		case "/anchor":
			_, _ = w.Write([]byte(`<div style="height:1200px"></div><div id="detail target" style="height:60px">Target</div><div style="height:1200px"></div>`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "htmx-panel-anchors", func(b *composerFocusCDP) {
		b.waitFor("navigation ready", `String(document.readyState==='complete' && typeof window.openVibelyNavigate==='function')`, "true")
		result := b.evaluateAwait(`(async()=>{
 let nativeCalls=0;const native=Element.prototype.scrollIntoView;
 Element.prototype.scrollIntoView=function(){nativeCalls++;return native.apply(this,arguments)};
 const check=(ok,message)=>{if(!ok)throw Error(message)};
 // Cached previews run before the network response computes its history URL.
 const previewPath={anchor:'detail%20target'};
 document.dispatchEvent(new CustomEvent('htmx:beforeSwap',{detail:{shouldSwap:true,_ovCachedPreview:true,pathInfo:previewPath}}));
 check(previewPath.anchor==='detail%20target','cached preview consumed the pending request anchor');
 for(const hash of ['#detail%20target','#missing','']) {
 await window.openVibelyNavigate('/anchor'+hash);
 // HTMX settles after its navigation promise resolves.
 await new Promise(r=>setTimeout(r,100));
 check(location.hash===hash,'URL fragment changed');
 check(document.getElementById('desktop-project-titlebar').getBoundingClientRect().top===0&&!document.body.scrollTop&&!document.documentElement.scrollTop,'shell moved');
 if(hash==='#detail%20target') {
 const panel=document.getElementById('main-content'),target=document.getElementById('detail target');
 check(panel.scrollTop>0,'anchor not revealed');
 check(Math.abs(target.getBoundingClientRect().top-panel.getBoundingClientRect().top-panel.clientTop)<2,'anchor not aligned inside panel');
 }
 }
 check(nativeCalls===0,'HTMX bypassed panel scrolling');
 return 'pass';
 })()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}
