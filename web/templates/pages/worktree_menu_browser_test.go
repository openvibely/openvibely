package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_WorktreeActionsAboveDiffHeader(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{ID: "layer-task", WorktreeBranch: "task/layer", MergeStatus: models.MergeStatusPending}
	diff := "diff --git a/example.go b/example.go\n--- a/example.go\n+++ b/example.go\n@@ -1 +1 @@\n-old\n+new\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
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
		if err := content.Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/", "worktree-menu-layer", func(b *composerFocusCDP) {
		b.waitFor("actions ready", `String(!!document.querySelector('#changes-actions-dropdown'))`, "true")
		b.click(`#changes-actions-dropdown > [role="button"]`)
		b.waitFor("menu visible", `getComputedStyle(document.querySelector('#changes-actions-dropdown > ul')).visibility`, "visible")
		for _, mode := range []string{"inline", "split"} {
			b.evaluate(fmt.Sprintf(`switchDiffView(%q,false); 'ok'`, mode))
			b.waitFor("menu above "+mode+" header", `(function(){var m=document.querySelector('#changes-actions-dropdown > ul'),h=Array.from(document.querySelectorAll('.diff-file-header')).find(e=>e.getClientRects().length),a=m.getBoundingClientRect(),b=h.getBoundingClientRect();var x=(Math.max(a.left,b.left)+Math.min(a.right,b.right))/2,y=(Math.max(a.top,b.top)+Math.min(a.bottom,b.bottom))/2;if(Math.min(a.bottom,b.bottom)<=Math.max(a.top,b.top))return 'no overlap';return String(m.contains(document.elementFromPoint(x,y)))})()`, "true")
		}
	})
}
