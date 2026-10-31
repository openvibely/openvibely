package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_MenuCleanupDoesNotScanLargePagePerElement(t *testing.T) {
	chrome := testChromePath(t)
	var base bytes.Buffer
	if err := layout.Base("Cleanup", nil, "").Render(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	source := base.String()
	start := strings.Index(source, "function menuDropdownForTarget(target)")
	if start < 0 {
		t.Fatal("missing menu helpers")
	}
	end := strings.Index(source[start:], "function hoverMenuSubmenu(event, container)")
	if end < 0 {
		t.Fatal("missing menu cleanup boundary")
	}
	script := source[start : start+end]
	runner := `<script>
 window.addEventListener('load', async function() {
  try {
   const original = document.querySelectorAll.bind(document);
   let scans = 0, cleaned = 0;
   document.querySelectorAll = function(selector) {
    if (selector === '[data-menu-submenu-portaled="true"]') scans++;
    return original(selector);
   };
   document.addEventListener('htmx:beforeCleanupElement', function() { cleaned++; });
   // Exercise a real open portal and verify removal when its owner is swapped.
   positionMenuSubmenu(document.getElementById('submenu-row'));
   if (!original('[data-menu-submenu-portaled="true"]').length) throw Error('portal did not open');
   scans = 0;
   await htmx.ajax('GET', '/other', {target:'#main-content', swap:'innerHTML'});
   document.getElementById('fixture-root').setAttribute('data-scans', scans);
   if (cleaned < 10000) throw Error('large fixture was not cleaned');
   if (original('[data-menu-submenu-portaled="true"]').length) throw Error('portal leaked after navigation');
   if (scans > 2) throw Error('whole-document portal scans: '+scans);
   document.getElementById('fixture-root').setAttribute('data-test-result','pass');
  } catch(error) { document.getElementById('fixture-root').setAttribute('data-test-result','fail'); document.getElementById('fixture-root').setAttribute('data-test-error',String(error)); }
 });
 </script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(htmx204)
		case "/other":
			fmt.Fprint(w, `<div>Other project</div>`)
		default:
			fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script></head><body><main id="fixture-root" data-test-result="pending"><div id="main-content" hx-history="false"><div data-menu-dropdown><ul data-menu-content><li id="submenu-row"><button>Submenu</button><ul data-menu-submenu-panel><li>Child</li></ul></li></ul></div>`+strings.Repeat(`<div><span>diff line</span></div>`, 10000)+`</div></main><script>`+script+`</script>`+runner+`</body></html>`)
		}
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "large page menu cleanup", 10000, 30*time.Second)
}

func TestBrowserFunctional_DiffNavigationSkipsHistorySnapshot(t *testing.T) {
	chrome := testChromePath(t)
	var rendered bytes.Buffer
	diff := "diff --git a/example.go b/example.go\n--- a/example.go\n+++ b/example.go\n@@ -1 +1 @@\n-old\n+new\n"
	if err := DiffViewer(diff, "task", nil, "inline").Render(context.Background(), &rendered); err != nil {
		t.Fatal(err)
	}
	runner := `<script>
    window.addEventListener('load',async function(){
     const result=document.getElementById('fixture-root');
     try {
      let snapshots=0;
      document.addEventListener('htmx:beforeHistorySave',function(){snapshots++;});
      await htmx.ajax('GET','/other',{source:document.getElementById('navigate'),target:'#main-content',swap:'innerHTML'});
      if(snapshots) throw Error('diff page was cloned into history');
      if(location.pathname!='/other') throw Error('navigation URL was not pushed');
      const restored=new Promise(function(resolve,reject){
       document.addEventListener('htmx:historyRestore',resolve,{once:true});
       setTimeout(function(){reject(Error('back navigation did not restore'));},2000);
      });
      history.back(); await restored;
      if(!document.getElementById('fresh-diff')) throw Error('Back did not reload uncached diff page');
      result.setAttribute('data-test-result','pass');
     }catch(error){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(error));}
    });
   </script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(htmx204)
		case "/other":
			fmt.Fprint(w, `<div>Other project</div>`)
		default:
			if r.Header.Get("HX-History-Restore-Request") == "true" {
				fmt.Fprint(w, `<main id="main-content" hx-history-elt><div id="fresh-diff" hx-history="false">Fresh diff</div></main>`)
				return
			}
			fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script></head><body><main id="fixture-root" data-test-result="pending"></main><a id="navigate" hx-push-url="true"></a><main id="main-content" hx-history-elt>`+rendered.String()+`</main>`+runner+`</body></html>`)
		}
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "diff history opt out", 5000, 20*time.Second)
}
