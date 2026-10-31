package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_WorktreeActionsAboveDiffHeader(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := worktreeActionsFixture(t, "", nil)
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "worktree-menu-layer", func(b *composerFocusCDP) {
		b.waitFor("actions ready", `String(!!document.querySelector('#changes-actions-dropdown'))`, "true")
		b.waitFor("menu starts hidden", `String(document.querySelector('#changes-actions-menu').getClientRects().length === 0)`, "true")
		// Force a stacking context below the sticky cards, as in the desktop failure.
		b.evaluate(`document.querySelector('#changes-actions-dropdown').parentElement.style.cssText='position:relative;z-index:0;transform:translateZ(0)'; 'ok'`)
		for _, mode := range []string{"inline", "split"} {
			b.evaluate(fmt.Sprintf(`switchDiffView(%q,false); 'ok'`, mode))
			b.waitFor("diff header loaded", `String(Array.from(document.querySelectorAll('.diff-file-header')).some(el=>el.getClientRects().length>0))`, "true")
			b.click(`#changes-actions-dropdown > [role="button"]`)
			b.waitFor("menu visible", `String(document.querySelector('#changes-actions-menu').matches(':popover-open'))`, "true")
			b.waitFor("menu within viewport", `(function(){const r=document.querySelector('#changes-actions-menu').getBoundingClientRect();return String(r.width>0 && r.left>=0 && r.top>=0 && r.right<=innerWidth && r.bottom<=innerHeight)})()`, "true")
			b.waitFor("menu above "+mode+" header", `(function(){var m=document.querySelector('#changes-actions-dropdown > ul'),h=Array.from(document.querySelectorAll('.diff-file-header')).find(e=>e.getClientRects().length),a=m.getBoundingClientRect(),b=h.getBoundingClientRect();var x=(Math.max(a.left,b.left)+Math.min(a.right,b.right))/2,y=(Math.max(a.top,b.top)+Math.min(a.bottom,b.bottom))/2;if(Math.min(a.bottom,b.bottom)<=Math.max(a.top,b.top))return JSON.stringify({reason:'no overlap',menu:a.toJSON(),header:b.toJSON(),style:m.getAttribute('style')});return m.contains(document.elementFromPoint(x,y)) ? 'true' : JSON.stringify({hit:document.elementFromPoint(x,y)?.outerHTML.slice(0,400),a:a.toJSON(),b:b.toJSON(),x,y,open:m.matches(':popover-open'),css:[getComputedStyle(m).opacity,getComputedStyle(m).visibility,getComputedStyle(m).pointerEvents]})})()`, "true")
			b.click(`#changes-actions-dropdown > [role="button"]`)
			b.waitFor("menu closed", `String(document.querySelector('#changes-actions-menu').matches(':popover-open'))`, "false")
			b.waitFor("closed menu has no layout", `String(document.querySelector('#changes-actions-menu').getClientRects().length === 0)`, "true")
		}
		b.click(`#changes-actions-dropdown > [role="button"]`)
		b.waitFor("reopened", `String(document.querySelector('#changes-actions-menu').matches(':popover-open'))`, "true")
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27}, nil)
		b.waitFor("escape dismissal", `String(document.querySelector('#changes-actions-menu').matches(':popover-open'))`, "false")
		b.waitFor("closed menu has no layout", `String(document.querySelector('#changes-actions-menu').getClientRects().length === 0)`, "true")
		b.click(`#changes-actions-dropdown > [role="button"]`)
		b.click(`#changes-content h4`)
		b.waitFor("outside dismissal", `String(document.querySelector('#changes-actions-menu').matches(':popover-open'))`, "false")
		b.waitFor("closed menu has no layout", `String(document.querySelector('#changes-actions-menu').getClientRects().length === 0)`, "true")
		b.click(`#changes-actions-dropdown > [role="button"]`)
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap',{detail:{target:document.getElementById('changes-content')}})); 'ok'`)
		b.waitFor("refresh dismissal", `String(document.querySelector('#changes-actions-menu').matches(':popover-open'))`, "false")
		b.waitFor("closed menu has no layout", `String(document.querySelector('#changes-actions-menu').getClientRects().length === 0)`, "true")
		b.evaluate(`window.oldChangesMenu=document.querySelector('#changes-actions-menu'); htmx.ajax('GET','/tasks/layer-task/changes',{target:'#changes-content',swap:'innerHTML'}); 'ok'`)
		b.waitFor("refreshed menu", `String(!!document.querySelector('#changes-actions-menu') && document.querySelector('#changes-actions-menu') !== window.oldChangesMenu)`, "true")
		b.waitFor("refreshed menu starts hidden", `String(document.querySelector('#changes-actions-menu').getClientRects().length === 0)`, "true")
		b.click(`#changes-actions-dropdown > [role="button"]`)
		b.waitFor("refreshed menu opens at trigger", `(function(){const m=document.querySelector('#changes-actions-menu'),r=m.getBoundingClientRect(),t=document.querySelector('[popovertarget="changes-actions-menu"]').getBoundingClientRect();return String(m.matches(':popover-open') && Math.abs(r.right-t.right)<2 && r.left>=0 && r.bottom<=innerHeight)})()`, "true")
	})
}

func worktreeActionsFixture(t *testing.T, runner string, results chan string) *httptest.Server {
	task := &models.Task{ID: "layer-task", WorktreeBranch: "task/layer", MergeStatus: models.MergeStatusPending}
	diff := "diff --git a/example.go b/example.go\n--- a/example.go\n+++ b/example.go\n@@ -1 +1 @@\n-old\n+new\n"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/menu-result" {
			if results != nil {
				results <- r.URL.Query().Get("status")
			}
			w.WriteHeader(204)
			return
		}
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/tasks/layer-task/changes/file" {
			if err := components.DiffChunk(components.ParseDiffOutput(diff)[0], 0, r.URL.Query().Get("view"), task.ID, nil, true, 0, 1, components.DiffCodeRevision(components.ParseDiffOutput(diff)[0])).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path == "/tasks/layer-task/changes" {
			if err := TaskChangesWorktreeContent(diff, task, nil, nil, nil, false, false).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(204)
			return
		}
		content := TaskDetailPage(nil, task, nil, nil, nil, nil, nil, nil, "changes", nil)
		rec := httptest.NewRecorder()
		if err := content.Render(r.Context(), rec); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1)))
	}))
}
