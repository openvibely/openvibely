package pages

import (
	"bytes"
	"context"
	"fmt"
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
	var failB atomic.Bool
	failB.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/ui/preferences" {
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
		browser.evaluate(`window.pickProject=function(id){var s=document.getElementById('project-selector');s.value=id;s.dispatchEvent(new Event('change',{bubbles:true}));};pickProject('b');'ok';`)
		browser.waitFor("failed switch rolls back", `document.getElementById('project-selector').value`, "a")
		failB.Store(false)
		browser.evaluate(`pickProject('b');setTimeout(function(){pickProject('c');},30);'ok';`)
		browser.waitFor("missing destination falls back", `location.pathname+':'+(document.getElementById('loaded-project')||{}).textContent`, "/chat:c")
		// Wait past B's delayed response to catch a stale overwrite.
		browser.evaluate(`setTimeout(function(){window.raceSettled=true;},500);'ok';`)
		browser.waitFor("race settled", `String(window.raceSettled)`, "true")
		browser.waitFor("latest project retained", `document.getElementById('project-selector').value+':'+document.getElementById('loaded-project').textContent`, "c:c")
		browser.evaluate(`window.savedRequests=0;var originalFetch=window.fetch;window.fetch=function(url,options){if(url==='/ui/preferences' && options.body.includes('project_locations')){window.savedRequests++;if(window.savedRequests===1)return Promise.resolve({ok:false});}return originalFetch.apply(this,arguments);};location.hash='retry';'ok';`)
		browser.waitFor("unchanged location retried", `String(window.savedRequests >= 2)`, "true")
	})
}
