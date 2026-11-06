package pages

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_MenuBackdropCornersAndKeyboard(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path != "/chat" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				content := components.ChatInputForm(components.ChatInputFormConfig{FormID: "picker-form", InputID: "picker-message", TargetID: "messages", PostEndpoint: "/chat/send", ProjectID: "project", ShowModelSelector: true, Agents: []models.LLMConfig{{ID: "model", Name: "Test model", Provider: models.ProviderOpenAI, Model: "test"}}, SelectedAgentID: "model"})
				var page bytes.Buffer
				if err := layout.Base("Backdrop", nil, "project").Render(templ.WithChildren(r.Context(), content), &page); err != nil {
					t.Error(err)
				}
				html := page.String()
				if legacy {
					html = strings.Replace(html, "<head>", `<head><script>delete HTMLElement.prototype.showPopover; delete HTMLElement.prototype.hidePopover;</script>`, 1)
				}
				fmt.Fprint(w, html)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL+"/chat", "backdrop-corners-keyboard", func(b *composerFocusCDP) {
				b.waitFor("ready", `String(!!window.ovModelPicker && document.readyState==='complete')`, "true")
				// Put a real pointer control beneath a menu, including its transparent corners.
				b.evaluate(`document.body.insertAdjacentHTML('beforeend','<button id="corner-under" style="position:fixed;left:80px;top:80px;width:260px;height:160px;z-index:9990;cursor:pointer" onclick="window.cornerActivated=true">Background</button><div id="corner-menu" class="dropdown dropdown-open" style="position:fixed;left:100px;top:100px;z-index:9991"><ul class="dropdown-content ov-menu" data-menu-content style="position:fixed;left:100px;top:100px;width:200px;height:100px;visibility:visible;opacity:1;border-radius:12px"><li><button id="corner-action" onclick="window.menuActivated=true">Action</button></li></ul></div>');'ready'`)
				b.waitFor("backdrop", `String(!!document.querySelector('#ov-menu-backdrop')?.getClientRects().length)`, "true")
				for _, radius := range []string{"12px", "30% / 40%"} {
					b.evaluate(`document.querySelector('#corner-menu ul').style.borderRadius='` + radius + `';'radius'`)
					for _, point := range [][2]int{{101, 101}, {299, 101}, {101, 199}, {299, 199}} {
						b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point[0], "y": point[1]}, nil)
						b.waitFor("rounded corner blocks hover", fmt.Sprintf(`String(!document.getElementById('corner-under').matches(':hover') && !!document.elementFromPoint(%d,%d).closest('#ov-menu-backdrop') && getComputedStyle(document.elementFromPoint(%d,%d)).cursor==='default')`, point[0], point[1], point[0], point[1]), "true")
					}
				}
				b.click(`#corner-action`)
				b.waitFor("menu interior receives clicks", `String(!!window.menuActivated && !window.cornerActivated)`, "true")
				// Another visible menu overlapping a rounded corner must remain interactive.
				b.evaluate(`document.body.insertAdjacentHTML('beforeend','<div id="overlap-menu" class="dropdown dropdown-open" style="position:fixed;z-index:9992"><ul class="dropdown-content ov-menu" data-menu-content style="position:fixed;left:90px;top:90px;width:60px;height:60px;visibility:visible;opacity:1;border-radius:0"><li><button id="overlap-action">Other</button></li></ul></div>');'overlap'`)
				b.waitFor("overlapping surfaces stay exposed", `String(!!document.elementFromPoint(101,101).closest('#overlap-menu'))`, "true")
				b.evaluate(`document.getElementById('overlap-menu').remove();document.getElementById('corner-menu').remove();document.getElementById('corner-under').remove();window.ovModelPicker.open(document.querySelector('.chat-model-select'));'model'`)
				b.waitFor("model open", `String(!document.getElementById('conversation-model-picker').hidden)`, "true")
				// Finishing the favorites load re-renders the rows captured below.
				b.waitFor("favorites loaded", `String(!document.getElementById('conversation-model-picker').hasAttribute('aria-busy'))`, "true")
				b.evaluate(`document.getElementById('conversation-model-picker').insertAdjacentHTML('afterend','<button id="keyboard-background">Background action</button>');'background'`)
				tab := func(reverse bool) {
					modifiers := 0
					if reverse {
						modifiers = 8
					}
					b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Tab", "code": "Tab", "windowsVirtualKeyCode": 9, "modifiers": modifiers}, nil)
					b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Tab", "code": "Tab", "windowsVirtualKeyCode": 9, "modifiers": modifiers}, nil)
				}
				for _, provider := range []bool{false, true} {
					if provider {
						b.click(`#conversation-model-picker .ov-mp-provider`)
					}
					b.evaluate(`window.menuTabs=Array.from(document.querySelectorAll('#conversation-model-picker input,#conversation-model-picker button,#conversation-provider-models button')).filter(el=>el.tabIndex>=0&&!el.disabled&&el.getClientRects().length&&getComputedStyle(el).visibility==='visible');window.menuTabs.at(-1).focus();'last'`)
					tab(false)
					b.waitFor("Tab wraps to first menu control", `String(document.activeElement===window.menuTabs[0] && !document.getElementById('conversation-model-picker').hidden)`, "true")
					tab(true)
					b.waitFor("Shift+Tab wraps to last menu control", `String(document.activeElement===window.menuTabs.at(-1))`, "true")
				}
				// Escape first dismisses the provider submenu, then restores the composer trigger.
				for i := 0; i < 2; i++ {
					b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Escape"}, nil)
					b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Escape"}, nil)
				}
				b.waitFor("Escape releases boundary and restores focus", `String(document.getElementById('conversation-model-picker').hidden && !document.querySelector('#ov-menu-backdrop')?.getClientRects().length && document.activeElement===document.querySelector('.chat-model-select'))`, "true")
				b.click(`#keyboard-background`)
				b.waitFor("background focus works after dismissal", `String(document.activeElement.id==='keyboard-background')`, "true")
			})

		})
	}
}
