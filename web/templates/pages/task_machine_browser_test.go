package pages

import (
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	templateui "github.com/openvibely/openvibely/web/templates"
	"github.com/openvibely/openvibely/web/templates/layout"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_TaskMachineTooltips(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><body><style>svg{width:14px;height:14px}.task-list-row{display:flex;gap:8px}</style>`)
		ctx := layout.WithTaskMachines(r.Context(), map[string]layout.TaskMachine{"0123456789abcdef": {Name: "oldmac", Kind: "laptop"}})
		_ = layout.TaskListPanelContent("__repo_group", []layout.TaskListGroup{{Label: "Active", Tasks: []models.Task{{ID: "a", ProjectID: "local", Title: "Local"}, {ID: "b", ProjectID: remoteid.ID("0123456789abcdef", "p"), Title: "Remote"}}}}).Render(ctx, w)
		_ = templateui.Tooltips().Render(ctx, w)
		fmt.Fprint(w, `</body></html>`)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "machine-tooltip", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(!!window.openVibelyTooltip)`, "true")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true}));document.querySelector('[aria-label="This computer"]').focus({preventScroll:true});document.querySelector('[aria-label="This computer"]').dispatchEvent(new FocusEvent('focusin',{bubbles:true}));'focus'`)
		b.waitFor("local focus tooltip", `document.getElementById('ov-shared-tooltip').textContent`, "This computer")
		b.evaluate(`document.querySelector('[aria-label="This computer"]').blur();document.querySelector('[aria-label="oldmac"]').dispatchEvent(new PointerEvent('pointerover',{bubbles:true}));'hover'`)
		b.waitFor("remote tooltip", `document.getElementById('ov-shared-tooltip').textContent`, "oldmac")
	})
}
