package pages

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ProjectSwitchFailures(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var saves atomic.Int32
	var replayed atomic.Bool
	var failB atomic.Bool
	failB.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/ui/preferences" {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), `"pinned_project_ids":["c","b","a"]`) && strings.Contains(string(body), `/schedule?project_id=b`) {
				replayed.Store(true)
			}
			if saves.Add(1) == 1 {
				http.Error(w, "temporary", 503)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/wails/runtime.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, "export const Window = {};")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/events/") {
			w.WriteHeader(204)
			return
		}
		id := r.URL.Query().Get("project_id")
		if r.Header.Get("HX-Request") == "true" {
			if id == "b" && failB.Load() {
				time.Sleep(300 * time.Millisecond)
				http.Error(w, "failed", 500)
				return
			}
			if r.URL.Path == "/deleted" {
				http.NotFound(w, r)
				return
			}
			if id == "b" {
				time.Sleep(300 * time.Millisecond)
			}
			fmt.Fprintf(w, `<div id="loaded-project">%s</div>`, id)
			return
		}
		var buf bytes.Buffer
		ctx := layout.WithUIPreferences(layout.WithDesktopMode(context.Background(), true), layout.UIPreferences{PinnedProjectIDs: []string{"a", "b", "c"}, ProjectLocations: `{"c":"/deleted?project_id=c"}`})
		if err := layout.Base("Test", []models.Project{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}}, "a").Render(ctx, &buf); err != nil {
			t.Error(err)
		}
		w.Write(buf.Bytes())
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks?project_id=a", "project-failures", func(browser *composerFocusCDP) {
		browser.waitFor("ready", `String(typeof window.openVibelyProjectTabsSync === 'function')`, "true")
		browser.evaluate(`var trackFetch=window.fetch;window.savedSelectedProject='';window.fetch=function(url,options){return trackFetch.apply(this,arguments).then(function(response){if(url==='/ui/preferences'&&response.ok){var value=JSON.parse(options.body);if(value.project_id)window.savedSelectedProject=value.project_id;}return response;});};'ok';`)
		browser.evaluate(`window.pickProject=function(id){var s=document.getElementById('project-selector');s.value=id;s.dispatchEvent(new Event('change',{bubbles:true}));};pickProject('b');'ok';`)
		browser.waitFor("failed switch rolls back", `document.getElementById('project-selector').value`, "a")
		browser.evaluate(`document.querySelector('[data-close-project="a"]').click();document.querySelector('[data-close-project="a"]').click();'ok';`)
		browser.waitFor("repeated failed close retains active tab", `document.getElementById('project-selector').value+':'+document.querySelector('[data-project-tab="a"]').getAttribute('aria-selected')+':'+document.getElementById('desktop-project-titlebar').dataset.pinnedProjects`, `a:true:["a","b","c"]`)
		browser.evaluate(`pickProject('b');document.querySelector('[data-close-project="a"]').click();'ok';`)
		browser.waitFor("loaded tab protected during ordinary switch", `String(document.querySelector('[data-close-project="a"]').disabled)+':'+document.getElementById('desktop-project-titlebar').dataset.pinnedProjects`, `true:["a","b","c"]`)
		browser.waitFor("failed ordinary switch retains loaded tab", `document.getElementById('project-selector').value+':'+document.querySelector('[data-project-tab="a"]').getAttribute('aria-selected')+':'+String(document.querySelector('[data-close-project="a"]').disabled)`, "a:true:false")
		failB.Store(false)
		browser.evaluate(`pickProject('b');setTimeout(function(){pickProject('c');},30);'ok';`)
		browser.waitFor("missing destination falls back", `location.pathname+':'+(document.getElementById('loaded-project')||{}).textContent`, "/chat:c")
		// Wait past B's delayed response to catch a stale overwrite.
		browser.evaluate(`setTimeout(function(){window.raceSettled=true;},500);'ok';`)
		browser.waitFor("race settled", `String(window.raceSettled)`, "true")
		browser.waitFor("latest project retained", `document.getElementById('project-selector').value+':'+document.getElementById('loaded-project').textContent`, "c:c")
		// Start a delayed switch, then restore cached history before it completes.
		browser.evaluate(`pickProject('b');'ok';`)
		browser.navigateHistory(-1)
		browser.waitFor("Back restores project A", `document.getElementById('project-selector').value+':'+location.pathname`, "a:/tasks")
		browser.evaluate(`setTimeout(function(){window.historyRaceSettled=true;},500);'ok';`)
		browser.waitFor("history race settled", `String(window.historyRaceSettled)`, "true")
		browser.waitFor("late switch cannot overwrite Back", `document.getElementById('project-selector').value+':'+location.pathname+':'+String(!!window.openVibelyProtectedProjectID)`, "a:/tasks:false")
		browser.waitFor("Back persists selected project", `window.savedSelectedProject`, "a")
		browser.navigateHistory(1)
		browser.waitFor("Forward restores project C", `document.getElementById('project-selector').value+':'+document.getElementById('loaded-project').textContent`, "c:c")
		browser.waitFor("Forward persists selected project", `window.savedSelectedProject`, "c")
		browser.evaluate(`window.savedRequests=0;var originalFetch=window.fetch;window.fetch=function(url,options){if(url==='/ui/preferences' && options.body.includes('project_locations')){window.savedRequests++;if(window.savedRequests===1)return Promise.resolve({ok:false});}return originalFetch.apply(this,arguments);};location.hash='retry';'ok';`)
		browser.waitFor("unchanged location retried", `String(window.savedRequests >= 2)`, "true")
		browser.evaluate(`window.tabSaveAttempts=0;window.latestTabSave='';var previousFetch=window.fetch;window.fetch=function(url,options){if(url==='/ui/preferences' && options.body.includes('pinned_project_ids')){window.tabSaveAttempts++;window.latestTabSave=JSON.stringify(JSON.parse(options.body).pinned_project_ids);if(window.tabSaveAttempts===1)return Promise.resolve({ok:false});}return previousFetch.apply(this,arguments);};document.querySelector('[data-close-project="a"]').click();'ok';`)
		browser.waitFor("tab list save retried", `String(window.tabSaveAttempts >= 2)+':'+window.latestTabSave`, `true:["b","c"]`)
		browser.evaluate(`document.querySelector('[data-close-project="c"]').click();'ok';`)
		browser.waitFor("closing tab retained during navigation", `String(!!document.querySelector('[data-project-tab="c"]'))`, "true")
		browser.waitFor("successful close removes tab", `document.getElementById('loaded-project').textContent+':'+document.getElementById('desktop-project-titlebar').dataset.pinnedProjects`, `b:["b"]`)
		browser.waitFor("latest tab list saved", `window.latestTabSave`, `["b"]`)

		browser.evaluate(`window.permanentAttempts=0;window.validPreferenceSaved=false;window.releaseRejectedSave=null;window.fetch=function(url,options){if(url==='/ui/preferences'){var value=JSON.parse(options.body);if(value.project_id==='deleted'){window.permanentAttempts++;return new Promise(function(resolve){window.releaseRejectedSave=function(){resolve({ok:false,status:400,json:async function(){return {message:'invalid project'};}});};});}if(value.project_id==='b' && value.pinned_project_ids[0]==='b')window.validPreferenceSaved=true;return Promise.resolve({ok:true,status:204});}return originalFetch.apply(this,arguments);};window.openVibelySaveProjectPreferences({project_id:'deleted',pinned_project_ids:['b']});'ok';`)
		browser.waitFor("validation request pending", `typeof window.releaseRejectedSave`, "function")
		browser.evaluate(`window.openVibelySaveProjectPreferences({project_id:'b'});window.releaseRejectedSave();'ok';`)
		browser.waitFor("newer values survive validation rejection", `String(window.validPreferenceSaved)`, "true")
		browser.evaluate(`window.openVibelySaveProjectPreferences({project_id:'deleted'});'ok';`)
		browser.waitFor("second rejection pending", `String(window.permanentAttempts)`, "2")
		browser.evaluate(`window.releaseRejectedSave();setTimeout(function(){window.permanentSettled=true;},1200);'ok';`)
		browser.waitFor("permanent error settles", `String(window.permanentSettled)`, "true")
		browser.waitFor("permanent error not retried", `String(window.permanentAttempts)`, "2")
		// Hold a save open, queue a newer tab order and page, then destroy the
		// document. keepalive cannot send the newer in-memory queue by itself.
		browser.evaluate(`window.fetch=function(url,options){if(url==='/ui/preferences')return new Promise(function(resolve){window.releaseOldSave=resolve;});return originalFetch.apply(this,arguments);};window.openVibelySaveProjectPreferences({pinned_project_ids:['a','b','c']});window.openVibelySaveProjectPreferences({pinned_project_ids:['c','b','a'],project_locations:{b:'/schedule?project_id=b'}});window.releaseOldSave({ok:true,status:204});'ok';`)
		browser.waitFor("old acknowledgment retains latest durable order", `JSON.stringify(JSON.parse(localStorage.getItem('openvibely.project-preference-outbox.pinned_project_ids')).value)`, `["c","b","a"]`)
		browser.evaluate(`sessionStorage.clear();'ok';`)
		browser.reload()
		browser.waitFor("startup replays and clears outbox", `String(!localStorage.getItem('openvibely.project-preference-outbox.pinned_project_ids')&&!localStorage.getItem('openvibely.project-preference-outbox.project_locations'))`, "true")
		browser.waitFor("recovered tabs rendered in order", `Array.from(document.querySelectorAll('[data-project-tab]')).map(el=>el.dataset.projectTab).join(',')`, "c,b,a")
		browser.evaluate(`var s=document.getElementById('project-selector');s.value='b';s.dispatchEvent(new Event('change',{bubbles:true}));'ok';`)
		browser.waitFor("recovered page used when switching", `location.pathname+':'+document.getElementById('loaded-project').textContent`, "/schedule:b")
		// A fresh desktop launch must recover the selected project too, before
		// the server's stale A selection can be added back to the saved list.
		browser.evaluate(`var launchFetch=window.fetch;window.fetch=function(url,options){if(url==='/ui/preferences')return new Promise(function(){});return launchFetch.apply(this,arguments);};window.openVibelySaveProjectPreferences({project_id:'b',pinned_project_ids:['c','b'],project_locations:{b:'/schedule?project_id=b'}});sessionStorage.clear();'ok';`)
		browser.call("Page.navigate", map[string]any{"url": server.URL + "/chat"}, nil)
		browser.waitFor("desktop launch restores recovered page", `location.pathname+':'+(document.getElementById('loaded-project')||{}).textContent`, "/schedule:b")
		browser.waitFor("desktop launch uses recovered selection and tabs", `document.getElementById('project-selector').value+':'+Array.from(document.querySelectorAll('[data-project-tab]')).map(el=>el.dataset.projectTab).join(',')`, "b:c,b")
		if !replayed.Load() {
			t.Fatal("startup did not replay the latest tab order and project page")
		}

	})
}
