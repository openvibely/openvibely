package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ModalInitialFocusKeepsCloseKeyboardAccessible(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var content bytes.Buffer
	if err := layout.ModalInitialFocus().Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	if err := components.InspectorDialog("editor", "Edit goal").Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	page := `<!doctype html><html><body><button id="open" onclick="editor.showModal()">Open editor</button>` + content.String() + `<dialog id="input-dialog"><button class="ov-modal-close">Close</button><h3>Input editor</h3><input autofocus></dialog></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "modal-initial-focus", func(browser *composerFocusCDP) {
		for i := 0; i < 2; i++ {
			browser.click("#open")
			browser.waitFor("initial heading focus", `String(document.activeElement === document.querySelector('#editor h3'))`, "true")
			browser.waitFor("heading has no focus ring", `getComputedStyle(document.activeElement).outlineStyle`, "none")
			browser.waitFor("standard close icon", `String(!!document.querySelector('#editor .ov-modal-close.btn-xs svg path'))`, "true")
			// Shift+Tab from the heading reaches the X preceding it in DOM order.
			browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Tab", "code": "Tab", "windowsVirtualKeyCode": 9, "modifiers": 8}, nil)
			browser.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Tab", "code": "Tab", "windowsVirtualKeyCode": 9, "modifiers": 8}, nil)
			browser.waitFor("keyboard close focus", `String(document.activeElement === document.querySelector('#editor .ov-modal-close') && document.activeElement.matches(':focus-visible'))`, "true")
			browser.click("#editor .ov-modal-close")
			browser.waitFor("editor closed", `String(!editor.open)`, "true")
		}
		browser.evaluate(`document.getElementById('input-dialog').showModal(); 'opened'`)
		browser.waitFor("explicit autofocus preserved", `String(document.activeElement === document.querySelector('#input-dialog input'))`, "true")
	})
}
