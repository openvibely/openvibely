package pages

import (
	"bytes"
	"context"
	"fmt"
	"github.com/openvibely/openvibely/web/static"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_ServerPasswordReveal(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var content bytes.Buffer
	if err := ServersContent(ServersPageData{Servers: []SavedServer{{ID: "0123456789abcdef", Name: "Remote", URL: "http://localhost:3001", Enabled: true, Password: "saved-password", Icon: "spider", MachineType: "laptop"}}}).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		fmt.Fprintf(w, `<!doctype html><html><head><link rel="stylesheet" href="%s"></head><body>%s</body></html>`, static.URL("app.css"), content.String())
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "servers-password", func(browser *composerFocusCDP) {
		browser.waitFor("server modal", `document.readyState+':'+typeof openServerModal`, "complete:function")
		browser.evaluate(`openServerModal();document.getElementById('server_password').value='test-password';'ok'`)
		browser.click("#server_modal .password-toggle-btn")
		browser.waitFor("revealed password", `document.getElementById('server_password').type`, "text")
		browser.click("#server_modal .password-toggle-btn")
		browser.waitFor("hidden password", `document.getElementById('server_password').type`, "password")
		browser.click("#server_modal .password-toggle-btn")
		browser.evaluate(`server_modal.close();openServerModal();'ok'`)
		browser.evaluate(`server_modal.close();openServerModal(document.querySelector('[data-server-id]'));'ok'`)
		browser.waitFor("saved icon", `document.getElementById('server_icon').value`, "spider")
		browser.click("#server-icon-trigger")
		browser.waitFor("icon menu open", `String(document.getElementById('server-icon-picker').matches(':popover-open'))`, "true")
		browser.waitFor("category separated from search divider", `String(document.getElementById('server-icon-category').getBoundingClientRect().top-document.querySelector('#server-icon-picker .ov-menu-search').getBoundingClientRect().bottom>=12)`, "true")
		browser.evaluate(`document.querySelector('#server-icon-picker .ov-menu-scroll').dispatchEvent(new WheelEvent('wheel',{deltaY:140,bubbles:true,cancelable:true}));'ok'`)
		browser.waitFor("wheel scroll", `String(document.querySelector('#server-icon-picker .ov-menu-scroll').scrollTop>0)`, "true")
		browser.evaluate(`document.querySelector('#server-icon-picker .ov-menu-scroll').scrollTop=0;'ok'`)
		browser.evaluate(`document.getElementById('server-icon-category').value='Food & drink';document.getElementById('server-icon-category').dispatchEvent(new Event('change'));let search=document.querySelector('#server-icon-picker input');search.value='cherry';search.dispatchEvent(new Event('input',{bubbles:true}));'ok'`)
		browser.waitFor("cherry in food", `Array.from(document.querySelectorAll('#server-icon-picker button[data-value]')).filter(b=>!b.hidden).map(b=>b.dataset.value).join(',')`, "cherry")
		browser.click("#server-icon-picker button[data-value=cherry]")
		browser.waitFor("chosen icon", `document.getElementById('server_icon').value+':'+document.getElementById('server-icon-trigger').textContent.trim()`, "cherry:Cherry")
		browser.evaluate(`server_modal.close();openServerModal(document.querySelector('[data-server-id]'));'ok'`)
		browser.waitFor("reloaded saved choice", `document.getElementById('server_icon').value`, "spider")
		browser.waitFor("saved password masked", `document.getElementById('server_password').type+':'+document.getElementById('server_password').value`, "password:saved-password")
		browser.click("#server_modal .password-toggle-btn")
		browser.waitFor("saved password revealed", `document.getElementById('server_password').type+':'+document.getElementById('server_password').value`, "text:saved-password")
		browser.evaluate(`server_modal.close();openServerModal();'ok'`)
		browser.waitFor("reset icon", `document.getElementById('server_icon').value`, "")
		browser.waitFor("reset password", `document.getElementById('server_password').type+':'+document.getElementById('server_password').value`, "password:")
	})
}
